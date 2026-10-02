package codexlive

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/logging"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/internal/livesession"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/quicksilver"
)

// Frames the transport synthesizes for the dialect. They never reach the
// wire: the peer's decoded audio and the transport's own end are delivered
// through the same read loop as the sideband's events, in arrival order.
const (
	typeOutputAudio = "codexlive.output_audio"
	typeEnded       = "codexlive.ended"
	// typeInputEnd goes the other way: the end of a user turn, which flushes
	// the partial input frame the transport holds.
	typeInputEnd = "codexlive.input_end"
)

// Close reasons the transport reports for ends the quicksilver dialect has no
// event for. A call the backend no longer knows is reported verbatim.
const (
	reasonCallEnded  = "call_ended"
	reasonAuthFailed = "authentication_failed"
)

// SignInHint ends every credential failure message.
const SignInHint = "sign in again with `yui auth chatgpt`"

// ErrSignInAgain reports a ChatGPT credential the backend rejected or the
// token manager can no longer refresh. The session ends; a new sign-in is
// the only fix.
var ErrSignInAgain = errors.New("openai-live: ChatGPT sign-in rejected; " + SignInHint)

// outputAudioFrame is one frame of peer audio at the session rate. Silent
// marks a frame below the speech floor.
type outputAudioFrame struct {
	Type   string `json:"type"`
	Audio  []byte `json:"audio"`
	Silent bool   `json:"silent,omitempty"`
}

// endedFrame is the transport's end: a close reason, or a credential
// failure with its detail.
type endedFrame struct {
	Type    string `json:"type"`
	Reason  string `json:"reason"`
	Auth    bool   `json:"auth,omitempty"`
	Message string `json:"message,omitempty"`
}

// dialect is the quicksilver vocabulary on the shared session state
// machine.
type dialect struct{}

// Inbound maps one sideband event or synthesized transport frame.
func (dialect) Inbound(frame []byte, in *livesession.Inbound) {
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(frame, &head); err != nil {
		in.Logger().Warn(in.Name()+": undecodable frame", logging.Field{Key: "error", Value: err})
		return
	}
	switch head.Type {
	case typeOutputAudio:
		var audio outputAudioFrame
		if err := json.Unmarshal(frame, &audio); err != nil {
			in.Logger().Warn(in.Name()+": undecodable output audio", logging.Field{Key: "error", Value: err})
			return
		}
		if audio.Silent {
			in.OutputSilence(audio.Audio)
			return
		}
		in.OutputAudio(audio.Audio)
	case typeEnded:
		var ended endedFrame
		if err := json.Unmarshal(frame, &ended); err != nil {
			in.Closed(livesession.CloseReasonConnectionLost)
			return
		}
		if ended.Auth {
			in.Fail(reasonAuthFailed, authFailure(ended.Message))
			return
		}
		in.Closed(ended.Reason)
	default:
		event, err := quicksilver.DecodeServerEvent(frame)
		if err != nil {
			in.Logger().Warn(in.Name()+": undecodable server event", logging.Field{Key: "error", Value: err})
			return
		}
		mapServerEvent(event, in)
	}
}

// mapServerEvent maps one decoded quicksilver server event.
func mapServerEvent(event quicksilver.Event, in *livesession.Inbound) {
	switch typed := event.(type) {
	case quicksilver.OutputTranscriptAdded:
		in.OutputTranscript(livesession.Transcript{Delta: typed.Item.Text})
	case quicksilver.InputTranscriptAdded:
		in.InputTranscript(livesession.Transcript{Delta: typed.Item.Text})
	case quicksilver.TurnDone:
		mapTurnDone(typed.Turn, in)
	case quicksilver.OutputAudioBufferCleared:
		in.InterruptSegment()
	case quicksilver.SessionUpdated:
		id := ""
		if typed.Session != nil {
			id = typed.Session.ID
		}
		in.SessionUpdated(id)
	case quicksilver.ErrorEvent:
		mapError(typed, in)
	case quicksilver.DelegationCreated:
		// Delegations reach the harness in a later phase, as on the public
		// route: logged and dropped, never turned into tool calls.
		in.Logger().Info(in.Name()+": delegation ignored",
			logging.Field{Key: "delegation_id", Value: typed.Item.ID}, logging.Field{Key: "target", Value: typed.Item.Target})
	default:
		// session.started needs no handshake on an attached sideband (the
		// call was configured at creation); the sideband's output_audio.delta
		// copies are dropped because the WebRTC track carries the media; and
		// unknown events carry nothing for the stream.
		in.Logger().Debug(in.Name()+": server event not mapped", logging.Field{Key: "type", Value: event.EventType()})
	}
}

// mapTurnDone ends the assistant segment or the user utterance the turn
// belongs to.
func mapTurnDone(turn quicksilver.Turn, in *livesession.Inbound) {
	switch turn.Role {
	case quicksilver.RoleAssistant:
		in.EndSegment()
	case quicksilver.RoleUser:
		in.EndUtterance(turn.Transcript)
	default:
		in.Logger().Debug(in.Name()+": turn.done for an unknown role", logging.Field{Key: "role", Value: turn.Role})
	}
}

// mapError reports a command error, or ends the session on a credential
// failure.
func mapError(event quicksilver.ErrorEvent, in *livesession.Inbound) {
	code, errorType := "", ""
	if event.Error != nil {
		code, errorType = event.Error.Code, event.Error.Type
	}
	if event.AuthFailure() {
		in.Fail(reasonAuthFailed, authFailure(fmt.Sprintf("the backend rejected the credential (%s)", code)))
		return
	}
	in.Error(livesession.CommandError(event.Text(), errorType, code, "", ""))
}

// authFailure is the terminal error of a rejected ChatGPT credential.
func authFailure(detail string) *messages.ErrorValue {
	err := ErrSignInAgain
	if detail != "" {
		err = fmt.Errorf("%w: %s", ErrSignInAgain, detail)
	}
	value := messages.NewErrorValueWithTerminal(err.Error(), providers.ErrorClassAuthentication,
		messages.TerminalReasonTerminalFailure, messages.TerminalProvenanceProvider, messages.TerminalOutputNotApplicable)
	value.Err = err
	return value
}

// InputAudio wraps one chunk of session-rate PCM as input_audio.append. The
// transport takes it off the write path and sends it as Opus media.
func (dialect) InputAudio(audio []byte) models.SessionEvent {
	return models.SessionEvent{Type: quicksilver.TypeInputAudioAppend, Data: []byte(`{"audio":"` + base64.StdEncoding.EncodeToString(audio) + `"}`)}
}

// InputEnd flushes the transport's partial input frame at the end of a user
// turn, so the turn's last milliseconds are not held until the next one.
func (dialect) InputEnd() models.SessionEvent { return models.SessionEvent{Type: typeInputEnd} }

// CloseEvent is session.close, sent on the sideband.
func (dialect) CloseEvent() models.SessionEvent {
	return models.SessionEvent{Type: quicksilver.TypeSessionClose}
}
