package openailive

import (
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/logging"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/internal/livesession"
)

// MaxAppendTokens is GPT-Live's limit on one context append; longer
// CONTEXT.APPEND content is split into several appends.
const MaxAppendTokens = livesession.MaxAppendTokens

// appendEventIDPrefix numbers the event ids of context appends, so an error
// or acknowledgement can be matched to the append it answers.
const appendEventIDPrefix = "evt_ctx_"

// ErrNoWireEvent reports a stream message GPT-Live has no channel for:
// response requests, tool results and typed text. Senders must not treat it
// as delivered.
var ErrNoWireEvent = livesession.ErrNoWireEvent

// publicDialect is the public gpt-live-1 vocabulary on the shared session
// state machine (internal/livesession).
type publicDialect struct{}

// Inbound maps one primary-WebSocket server frame.
func (publicDialect) Inbound(frame []byte, in *livesession.Inbound) {
	decoded, err := DecodeServerEvent(frame)
	if err != nil {
		in.Logger().Warn("openai live: undecodable server event", logging.Field{Key: "error", Value: err})
		return
	}
	switch typed := decoded.(type) {
	case SessionClosed:
		in.Closed(typed.Reason)
	case OutputAudioDelta:
		audio, err := typed.Bytes()
		if err != nil {
			in.Logger().Warn("openai live: undecodable output audio", logging.Field{Key: "error", Value: err})
			return
		}
		in.OutputAudio(audio)
	case DelegationCreated:
		// Client delegation is the default mode, so an absent target is one.
		// Responses delegations occur only in a Responses-mode session, which
		// this provider does not start, so they are logged and dropped.
		if target := typed.Delegation.Target; target != DelegationClient && target != "" {
			in.Logger().Info("openai live: delegation ignored",
				logging.Field{Key: "delegation_id", Value: typed.Delegation.ID}, logging.Field{Key: "target", Value: target})
			return
		}
		in.Delegation(typed.Delegation.ID, typed.OffsetMS, "")
	case OutputTranscriptDelta:
		in.OutputTranscript(transcript(TranscriptDelta(typed)))
	case InputTranscriptDelta:
		in.InputTranscript(transcript(TranscriptDelta(typed)))
	case SessionUpdated:
		in.SessionUpdated(typed.Session.ID)
	case ErrorEvent:
		in.Error(commandError(typed))
	case UsageUpdated:
		// Usage is a cumulative snapshot; its stream mapping is open (Q7).
		in.Logger().Debug("openai live: usage", logging.Field{Key: "seconds", Value: typed.Usage.Seconds})
	default:
		// Acknowledgements, session.started, info, response.event,
		// transport.* and unknown events carry nothing for the stream.
		in.Logger().Debug("openai live: server event not mapped", logging.Field{Key: "type", Value: decoded.EventType()})
	}
}

// InputAudio wraps one chunk as session.input_audio.append. Standard base64
// needs no JSON escaping, so the body is built directly.
func (publicDialect) InputAudio(audio []byte) models.SessionEvent {
	return models.SessionEvent{Type: TypeInputAudioAppend, Data: []byte(`{"audio":"` + base64.StdEncoding.EncodeToString(audio) + `"}`)}
}

// ContextAppend maps one chunk of a CONTEXT.APPEND to
// session.instructions.append, session.thinking.append or
// session.commentary.append by its kind, with delegation_id the value's id or
// null.
func (publicDialect) ContextAppend(value *messages.ContextAppendValue, chunk string, seq int64) (models.SessionEvent, bool, error) {
	eventType, ok := appendEventType(value.Kind)
	if !ok {
		return models.SessionEvent{}, false, nil
	}
	data, err := json.Marshal(ContextAppend{
		EventID:      fmt.Sprintf("%s%d", appendEventIDPrefix, seq),
		DelegationID: value.DelegationID,
		Content:      chunk,
	})
	if err != nil {
		return models.SessionEvent{}, false, err
	}
	return models.SessionEvent{Type: models.SessionEventType(eventType), Data: data}, true, nil
}

// appendEventType maps a CONTEXT.APPEND kind to its append command.
func appendEventType(kind messages.ContextAppendKind) (string, bool) {
	switch kind {
	case messages.ContextAppendInstructions:
		return TypeInstructionsAppend, true
	case messages.ContextAppendThinking:
		return TypeThinkingAppend, true
	case messages.ContextAppendCommentary:
		return TypeCommentaryAppend, true
	default:
		return "", false
	}
}

// CloseEvent is session.close.
func (publicDialect) CloseEvent() models.SessionEvent {
	return models.SessionEvent{Type: TypeSessionClose}
}

func transcript(delta TranscriptDelta) livesession.Transcript {
	return livesession.Transcript{Delta: delta.Delta, StartMS: delta.StartMS, EndMS: delta.EndMS}
}

// commandError maps an error event to a non-terminal stream error.
func commandError(event ErrorEvent) *messages.ErrorValue {
	body := event.Error
	param := ""
	if body.Param != nil {
		param = *body.Param
	}
	return livesession.CommandError(body.Message, body.Type, body.Code, param, body.ClientEventID)
}
