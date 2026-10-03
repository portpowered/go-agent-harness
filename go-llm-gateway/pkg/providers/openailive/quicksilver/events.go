package quicksilver

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive"
)

// Client event types.
const (
	TypeInputAudioAppend        = "input_audio.append"
	TypeSessionUpdate           = "session.update"
	TypeSessionContextAppend    = "session.context.append"
	TypeDelegationContextAppend = "delegation.context.append"
	TypeSessionClose            = "session.close"
)

// Server event types.
const (
	TypeSessionStarted           = "session.started"
	TypeSessionUpdated           = "session.updated"
	TypeOutputAudioDelta         = "output_audio.delta"
	TypeInputTranscriptAdded     = "input_transcript.added"
	TypeOutputTranscriptAdded    = "output_transcript.added"
	TypeTurnDone                 = "turn.done"
	TypeDelegationCreated        = "delegation.created"
	TypeOutputAudioBufferCleared = "output_audio_buffer.cleared"
	TypeError                    = "error"
)

// Content part types, item types, delegation targets and turn roles.
const (
	PartInputText  = "input_text"
	PartOutputText = "output_text"

	ItemTypeDelegation = "delegation"
	ItemTypeMessage    = "message"

	TargetClient = "client"

	RoleDeveloper = "developer"
	RoleUser      = "user"
	RoleAssistant = "assistant"
)

// Channel selects how the model treats a context append: speakable text is
// for the model to say, commentary is progress it may paraphrase.
type Channel string

// The two documented channels. An empty channel is omitted from the frame.
const (
	ChannelSpeakable  Channel = "speakable"
	ChannelCommentary Channel = "commentary"
)

// Event is one wire event. EventType is the value of its "type" field; the
// struct holds every other field.
type Event = openailive.Event

// UnknownEvent is an event whose type this package does not model. Raw is the
// complete frame; it re-encodes unchanged.
type UnknownEvent = openailive.UnknownEvent

// ErrOddPCMLength reports a PCM16 payload that does not hold whole samples.
var ErrOddPCMLength = errors.New("quicksilver: PCM16 audio must contain an even number of bytes")

// ContentPart is one typed text part of a context append, a delegation item
// or an initial item.
type ContentPart struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// --- Client events -------------------------------------------------------

// InputAudioAppend streams base64 input audio on a plain WebSocket. It is not
// needed when WebRTC carries the media.
type InputAudioAppend struct {
	Audio string `json:"audio"`
}

// SessionUpdate configures a plain-WebSocket session. A WebRTC call takes the
// same session in its creation request instead, without the model.
type SessionUpdate struct {
	Session SessionConfig `json:"session"`
}

// SessionContextAppend adds text the model should know or say, outside any
// delegation.
type SessionContextAppend struct {
	Channel Channel       `json:"channel,omitempty"`
	Content []ContentPart `json:"content"`
}

// DelegationContextAppend returns progress or a result for one delegation.
type DelegationContextAppend struct {
	DelegationItemID string        `json:"delegation_item_id"`
	Channel          Channel       `json:"channel,omitempty"`
	Content          []ContentPart `json:"content"`
}

// SessionClose asks the server to end the session.
type SessionClose struct{}

// --- Server events -------------------------------------------------------

// SessionResource is the resolved session the server reports. Every field is
// optional on the wire.
type SessionResource struct {
	ID           string `json:"id,omitempty"`
	Instructions string `json:"instructions,omitempty"`
	ExpiresAt    int64  `json:"expires_at,omitempty"`
}

// SessionStarted reports that the session is running.
type SessionStarted struct {
	Session *SessionResource `json:"session,omitempty"`
}

// SessionUpdated reports a configuration change.
type SessionUpdated struct {
	Session *SessionResource `json:"session,omitempty"`
}

// OutputAudioDelta carries base64 output audio. Over WebRTC the sideband
// receives copies; the media itself is the negotiated Opus track.
type OutputAudioDelta struct {
	Audio   string `json:"audio"`
	StartMS *int64 `json:"start_ms,omitempty"`
	EndMS   *int64 `json:"end_ms,omitempty"`
}

// TranscriptItem is one transcript fragment.
type TranscriptItem struct {
	ID   string `json:"id,omitempty"`
	Type string `json:"type,omitempty"`
	Text string `json:"text"`
}

// InputTranscriptAdded is a fragment of the user's speech.
type InputTranscriptAdded struct {
	Item TranscriptItem `json:"item"`
}

// OutputTranscriptAdded is a fragment of the model's speech.
type OutputTranscriptAdded struct {
	Item TranscriptItem `json:"item"`
}

// Turn is the final transcript of one user or assistant turn.
type Turn struct {
	ID         string `json:"id,omitempty"`
	Role       string `json:"role"`
	Transcript string `json:"transcript"`
}

// TurnDone ends a turn with its final transcript.
type TurnDone struct {
	Turn Turn `json:"turn"`
}

// DelegationItem is the work the model hands to the client.
type DelegationItem struct {
	ID      string        `json:"id"`
	Type    string        `json:"type"`
	Target  string        `json:"target"`
	Content []ContentPart `json:"content,omitzero"`
}

// DelegationCreated asks the client to do backend work. Answer it with
// DelegationContextAppend events carrying Item.ID.
type DelegationCreated struct {
	OffsetMS *int64         `json:"offset_ms,omitempty"`
	Item     DelegationItem `json:"item"`
}

// OutputAudioBufferCleared reports that queued output audio was dropped,
// for example on barge-in.
type OutputAudioBufferCleared struct{}

// ErrorDetail is the nested error object. Status is an HTTP status, sent as
// a number or a string.
type ErrorDetail struct {
	Type    string          `json:"type,omitempty"`
	Code    string          `json:"code,omitempty"`
	Message string          `json:"message,omitempty"`
	Status  json.RawMessage `json:"status,omitempty"`
}

// ErrorEvent reports a failure. The message, code and status appear at the
// top level or in the nested error object.
type ErrorEvent struct {
	Message string          `json:"message,omitempty"`
	Code    string          `json:"code,omitempty"`
	Status  json.RawMessage `json:"status,omitempty"`
	Error   *ErrorDetail    `json:"error,omitempty"`
}

// EventType methods name each event's wire type.

func (InputAudioAppend) EventType() string         { return TypeInputAudioAppend }
func (SessionUpdate) EventType() string            { return TypeSessionUpdate }
func (SessionContextAppend) EventType() string     { return TypeSessionContextAppend }
func (DelegationContextAppend) EventType() string  { return TypeDelegationContextAppend }
func (SessionClose) EventType() string             { return TypeSessionClose }
func (SessionStarted) EventType() string           { return TypeSessionStarted }
func (SessionUpdated) EventType() string           { return TypeSessionUpdated }
func (OutputAudioDelta) EventType() string         { return TypeOutputAudioDelta }
func (InputTranscriptAdded) EventType() string     { return TypeInputTranscriptAdded }
func (OutputTranscriptAdded) EventType() string    { return TypeOutputTranscriptAdded }
func (TurnDone) EventType() string                 { return TypeTurnDone }
func (DelegationCreated) EventType() string        { return TypeDelegationCreated }
func (OutputAudioBufferCleared) EventType() string { return TypeOutputAudioBufferCleared }
func (ErrorEvent) EventType() string               { return TypeError }

// NewInputAudioAppend base64-encodes one chunk of PCM16 input audio. An odd
// byte count is refused with ErrOddPCMLength.
func NewInputAudioAppend(pcm []byte) (InputAudioAppend, error) {
	if len(pcm)%2 != 0 {
		return InputAudioAppend{}, fmt.Errorf("%w: got %d bytes", ErrOddPCMLength, len(pcm))
	}
	return InputAudioAppend{Audio: base64.StdEncoding.EncodeToString(pcm)}, nil
}

// Bytes decodes the audio of an output delta.
func (e OutputAudioDelta) Bytes() ([]byte, error) {
	audio, err := base64.StdEncoding.DecodeString(e.Audio)
	if err != nil {
		return nil, fmt.Errorf("quicksilver: decode output audio: %w", err)
	}
	return audio, nil
}

// IsClient reports whether the delegation targets the client. Codex and
// OpenClaw both ignore other targets.
func (e DelegationCreated) IsClient() bool {
	return e.Item.Type == ItemTypeDelegation && e.Item.Target == TargetClient
}

// Prompt joins the input_text parts of the delegation, the way Codex and
// OpenClaw build the task text; other part types are skipped.
func (e DelegationCreated) Prompt() string {
	var prompt strings.Builder
	for _, part := range e.Item.Content {
		if part.Type == PartInputText {
			prompt.WriteString(part.Text)
		}
	}
	return prompt.String()
}

// Text returns the error message: the top-level message, else the nested
// one, else the nested code.
func (e ErrorEvent) Text() string {
	if e.Message != "" {
		return e.Message
	}
	if e.Error == nil {
		return e.Code
	}
	if e.Error.Message != "" {
		return e.Error.Message
	}
	return e.Error.Code
}

// AuthFailure reports whether the error is one OpenClaw treats as a fatal
// credential failure (realtime-quicksilver-events.ts,
// isFatalQuicksilverAuthError): an HTTP 401 status, or one of the credential
// codes, at the top level or in the nested error. A refreshed token may
// succeed on a new call.
func (e ErrorEvent) AuthFailure() bool {
	status, code := e.Status, e.Code
	if e.Error != nil {
		if len(status) == 0 {
			status = e.Error.Status
		}
		if code == "" {
			code = e.Error.Code
		}
	}
	if s := strings.Trim(strings.TrimSpace(string(status)), `"`); s == "401" {
		return true
	}
	switch strings.ToLower(code) {
	case "authentication_error", "invalid_api_key", "invalid_token", "token_expired":
		return true
	}
	return false
}

// ErrorCode is the error's code: the top-level one, else the nested one.
func (e ErrorEvent) ErrorCode() string {
	if e.Code != "" || e.Error == nil {
		return e.Code
	}
	return e.Error.Code
}
