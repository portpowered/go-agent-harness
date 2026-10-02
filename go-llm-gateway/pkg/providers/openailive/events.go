package openailive

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
)

// Client event types.
const (
	TypeSessionStart       = "session.start"
	TypeSessionUpdate      = "session.update"
	TypeInputAudioAppend   = "session.input_audio.append" // also the sideband's reflected input
	TypeInputAudioMute     = "session.input_audio.mute"
	TypeInputAudioUnmute   = "session.input_audio.unmute"
	TypeInstructionsAppend = "session.instructions.append"
	TypeThinkingAppend     = "session.thinking.append"
	TypeCommentaryAppend   = "session.commentary.append"
	TypeResponseItemCreate = "response.item.create"
	TypeResponseCreate     = "response.create"
	TypeSessionClose       = "session.close"
)

// Server event types.
const (
	TypeSessionStarted        = "session.started"
	TypeSessionUpdated        = "session.updated"
	TypeInputAudioMuted       = "session.input_audio.muted"
	TypeInputAudioUnmuted     = "session.input_audio.unmuted"
	TypeInstructionsAppended  = "session.instructions.appended"
	TypeThinkingAppended      = "session.thinking.appended"
	TypeCommentaryAppended    = "session.commentary.appended"
	TypeOutputAudioDelta      = "session.output_audio.delta"
	TypeInputTranscriptDelta  = "session.input_transcript.delta"
	TypeOutputTranscriptDelta = "session.output_transcript.delta"
	TypeDelegationCreated     = "session.delegation.created"
	TypeResponseEvent         = "response.event"
	TypeUsageUpdated          = "session.usage.updated"
	TypeSessionClosed         = "session.closed"
	TypeError                 = "error"
	TypeInfo                  = "info"
	TypeTransportRinging      = "transport.ringing"
	TypeTransportAnswered     = "transport.answered"
	TypeTransportFailed       = "transport.failed"
	TypeDTMFReceived          = "transport.dtmf.received"
	TypeDTMFSend              = "transport.dtmf.send"
)

// Event is one GPT-Live wire event. EventType is the value of its "type"
// field; the struct holds every other field.
type Event interface {
	EventType() string
}

// ErrOddPCMLength reports a PCM16 payload that does not hold whole samples.
var ErrOddPCMLength = errors.New("openailive: PCM16 audio must contain an even number of bytes")

// --- Client events -------------------------------------------------------

// SessionStart must be the first message on a primary or fork socket.
type SessionStart struct {
	EventID string        `json:"event_id,omitempty"`
	Session SessionConfig `json:"session"`
}

// SessionUpdate changes the Responses delegation settings. Nothing else is
// mutable after startup.
type SessionUpdate struct {
	EventID string       `json:"event_id,omitempty"`
	Session SessionPatch `json:"session"`
}

// SessionPatch is the sparse session object of session.update.
type SessionPatch struct {
	Delegation *Delegation `json:"delegation,omitempty"`
}

// InputAudioAppend streams base64 input audio on the primary socket.
type InputAudioAppend struct {
	EventID string `json:"event_id,omitempty"`
	Audio   string `json:"audio"`
}

// Command is the body of a client event that has only an event id.
type Command struct {
	EventID string `json:"event_id,omitempty"`
}

// InputAudioMute stops sending input to the model; output continues.
type InputAudioMute Command

// InputAudioUnmute resumes sending input to the model.
type InputAudioUnmute Command

// ResponseCreate starts or continues the Responses backend response.
type ResponseCreate Command

// SessionClose requests a graceful shutdown; session.closed follows.
type SessionClose Command

// ContextAppend is the body of the three append commands. DelegationID is
// required but nullable, so it always encodes, as null when nil.
type ContextAppend struct {
	EventID      string  `json:"event_id,omitempty"`
	DelegationID *string `json:"delegation_id"`
	Content      string  `json:"content"`
}

// InstructionsAppend adds trusted instructions. It can interrupt speech.
type InstructionsAppend ContextAppend

// ThinkingAppend adds quiet context that does not directly request speech.
type ThinkingAppend ContextAppend

// CommentaryAppend adds speakable context that the model paraphrases.
type CommentaryAppend ContextAppend

// ResponseItemCreate queues one Responses input item (Responses delegation).
type ResponseItemCreate struct {
	EventID string          `json:"event_id,omitempty"`
	Item    json.RawMessage `json:"item"`
}

// --- Server events -------------------------------------------------------

// SessionAck is the body of session.started and session.updated.
type SessionAck struct {
	EventID       string          `json:"event_id,omitempty"`
	ClientEventID string          `json:"client_event_id,omitempty"`
	Session       SessionResource `json:"session"`
}

// SessionStarted acknowledges session.start.
type SessionStarted SessionAck

// SessionUpdated reports the complete resolved session after session.update.
type SessionUpdated SessionAck

// Ack is the body of an acknowledgement with no other fields.
type Ack struct {
	EventID       string `json:"event_id,omitempty"`
	ClientEventID string `json:"client_event_id,omitempty"`
}

// InputAudioMuted acknowledges session.input_audio.mute.
type InputAudioMuted Ack

// InputAudioUnmuted acknowledges session.input_audio.unmute.
type InputAudioUnmuted Ack

// TimelineAck acknowledges an append when the session timeline reaches the
// estimated end of the added context. It does not mean the context was
// spoken or played.
type TimelineAck struct {
	EventID       string `json:"event_id,omitempty"`
	ClientEventID string `json:"client_event_id,omitempty"`
	StartMS       int64  `json:"start_ms"`
	EndMS         int64  `json:"end_ms"`
}

// InstructionsAppended acknowledges session.instructions.append.
type InstructionsAppended TimelineAck

// ThinkingAppended acknowledges session.thinking.append.
type ThinkingAppended TimelineAck

// CommentaryAppended acknowledges session.commentary.append.
type CommentaryAppended TimelineAck

// OutputAudioDelta carries base64 output audio. The primary socket omits the
// timing and the event id; sideband reflections (and Azure) include timing.
type OutputAudioDelta struct {
	EventID string `json:"event_id,omitempty"`
	Delta   string `json:"delta"`
	StartMS *int64 `json:"start_ms,omitempty"`
	EndMS   *int64 `json:"end_ms,omitempty"`
}

// TranscriptDelta is one transcript fragment. Times are milliseconds from
// session start, as a half-open interval.
type TranscriptDelta struct {
	EventID       string `json:"event_id,omitempty"`
	ClientEventID string `json:"client_event_id,omitempty"`
	Delta         string `json:"delta"`
	StartMS       int64  `json:"start_ms"`
	EndMS         int64  `json:"end_ms"`
}

// InputTranscriptDelta is a fragment of user speech.
type InputTranscriptDelta TranscriptDelta

// OutputTranscriptDelta is a fragment of assistant speech.
type OutputTranscriptDelta TranscriptDelta

// DelegationCreated announces that backend work is needed.
type DelegationCreated struct {
	EventID       string         `json:"event_id,omitempty"`
	ClientEventID string         `json:"client_event_id,omitempty"`
	OffsetMS      int64          `json:"offset_ms"`
	Delegation    DelegationInfo `json:"delegation"`
}

// ResponseEvent wraps one nested Responses streaming event.
type ResponseEvent struct {
	EventID       string          `json:"event_id,omitempty"`
	ClientEventID string          `json:"client_event_id,omitempty"`
	DelegationID  OptionalID      `json:"delegation_id,omitzero"`
	Event         json.RawMessage `json:"event"`
}

// OptionalID is an optional, nullable id field. Present is false when the
// key is absent; a present null has Present true and a nil Value. Both forms
// re-encode as they arrived.
type OptionalID struct {
	Present bool
	Value   *string
}

// SomeID returns a present, non-null OptionalID.
func SomeID(id string) OptionalID { return OptionalID{Present: true, Value: &id} }

// IsZero reports an absent field, so omitzero leaves it out.
func (o OptionalID) IsZero() bool { return !o.Present }

// MarshalJSON encodes the id, or null.
func (o OptionalID) MarshalJSON() ([]byte, error) { return json.Marshal(o.Value) }

// UnmarshalJSON records a present field, null or string.
func (o *OptionalID) UnmarshalJSON(data []byte) error {
	var value *string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*o = OptionalID{Present: true, Value: value}
	return nil
}

// UsageUpdated is a cumulative usage snapshot, sent about once a minute.
type UsageUpdated struct {
	EventID       string         `json:"event_id,omitempty"`
	ClientEventID string         `json:"client_event_id,omitempty"`
	Usage         Usage          `json:"usage"`
	ContextWindow *ContextWindow `json:"context_window,omitempty"`
}

// SessionClosed is terminal. Usage is the final voice duration.
type SessionClosed struct {
	EventID       string          `json:"event_id,omitempty"`
	ClientEventID string          `json:"client_event_id,omitempty"`
	Reason        string          `json:"reason"`
	Session       SessionResource `json:"session"`
	Usage         Usage           `json:"usage"`
}

// ErrorEvent reports a failed command, or a startup failure before
// session.started.
type ErrorEvent struct {
	EventID       string `json:"event_id,omitempty"`
	ClientEventID string `json:"client_event_id,omitempty"`
	Error         Error  `json:"error"`
}

// Info is a notice, for example data_channel_permissions.
type Info struct {
	EventID       string `json:"event_id,omitempty"`
	ClientEventID string `json:"client_event_id,omitempty"`
	Code          string `json:"code"`
	Message       string `json:"message"`
}

// ReflectedInputAudio is the sideband copy of input audio (24 kHz PCM16,
// captured before model-input muting). Its type is session.input_audio.append.
type ReflectedInputAudio struct {
	Audio string `json:"audio"`
}

// TransportCall is the body of the sideband SIP call-progress events.
type TransportCall struct {
	EventID       string `json:"event_id,omitempty"`
	ClientEventID string `json:"client_event_id,omitempty"`
	SessionID     string `json:"session_id"`
}

// TransportRinging reports an outbound SIP call ringing.
type TransportRinging TransportCall

// TransportAnswered reports an outbound SIP call answered.
type TransportAnswered TransportCall

// TransportFailed reports a failed outbound SIP call.
type TransportFailed struct {
	EventID       string `json:"event_id,omitempty"`
	ClientEventID string `json:"client_event_id,omitempty"`
	SessionID     string `json:"session_id"`
	Error         Error  `json:"error"`
}

// DTMF is the body of the SIP DTMF events. Key is the pressed key.
type DTMF struct {
	EventID       string `json:"event_id,omitempty"`
	ClientEventID string `json:"client_event_id,omitempty"`
	Key           string `json:"event"`
}

// DTMFReceived reports a key the caller pressed.
type DTMFReceived DTMF

// DTMFSend reports a key sent to the caller.
type DTMFSend DTMF

// UnknownEvent is any event whose type this package does not model. Raw is
// the complete frame; it re-encodes unchanged.
type UnknownEvent struct {
	Type string
	Raw  json.RawMessage
}

// EventType methods name each event's wire type.

func (SessionStart) EventType() string          { return TypeSessionStart }
func (SessionUpdate) EventType() string         { return TypeSessionUpdate }
func (InputAudioAppend) EventType() string      { return TypeInputAudioAppend }
func (InputAudioMute) EventType() string        { return TypeInputAudioMute }
func (InputAudioUnmute) EventType() string      { return TypeInputAudioUnmute }
func (InstructionsAppend) EventType() string    { return TypeInstructionsAppend }
func (ThinkingAppend) EventType() string        { return TypeThinkingAppend }
func (CommentaryAppend) EventType() string      { return TypeCommentaryAppend }
func (ResponseItemCreate) EventType() string    { return TypeResponseItemCreate }
func (ResponseCreate) EventType() string        { return TypeResponseCreate }
func (SessionClose) EventType() string          { return TypeSessionClose }
func (SessionStarted) EventType() string        { return TypeSessionStarted }
func (SessionUpdated) EventType() string        { return TypeSessionUpdated }
func (InputAudioMuted) EventType() string       { return TypeInputAudioMuted }
func (InputAudioUnmuted) EventType() string     { return TypeInputAudioUnmuted }
func (InstructionsAppended) EventType() string  { return TypeInstructionsAppended }
func (ThinkingAppended) EventType() string      { return TypeThinkingAppended }
func (CommentaryAppended) EventType() string    { return TypeCommentaryAppended }
func (OutputAudioDelta) EventType() string      { return TypeOutputAudioDelta }
func (InputTranscriptDelta) EventType() string  { return TypeInputTranscriptDelta }
func (OutputTranscriptDelta) EventType() string { return TypeOutputTranscriptDelta }
func (DelegationCreated) EventType() string     { return TypeDelegationCreated }
func (ResponseEvent) EventType() string         { return TypeResponseEvent }
func (UsageUpdated) EventType() string          { return TypeUsageUpdated }
func (SessionClosed) EventType() string         { return TypeSessionClosed }
func (ErrorEvent) EventType() string            { return TypeError }
func (Info) EventType() string                  { return TypeInfo }
func (ReflectedInputAudio) EventType() string   { return TypeInputAudioAppend }
func (TransportRinging) EventType() string      { return TypeTransportRinging }
func (TransportAnswered) EventType() string     { return TypeTransportAnswered }
func (TransportFailed) EventType() string       { return TypeTransportFailed }
func (DTMFReceived) EventType() string          { return TypeDTMFReceived }
func (DTMFSend) EventType() string              { return TypeDTMFSend }
func (e UnknownEvent) EventType() string        { return e.Type }

// NewInputAudioAppend base64-encodes one chunk of input audio in format. A
// PCM chunk must hold whole 16-bit samples, so an odd byte count is refused
// with ErrOddPCMLength; G.711 bytes pass through unchanged.
func NewInputAudioAppend(audio []byte, format AudioFormat) (InputAudioAppend, error) {
	if format.Type == AudioTypePCM && len(audio)%2 != 0 {
		return InputAudioAppend{}, fmt.Errorf("%w: got %d bytes", ErrOddPCMLength, len(audio))
	}
	return InputAudioAppend{Audio: base64.StdEncoding.EncodeToString(audio)}, nil
}

// Bytes decodes the appended audio.
func (e InputAudioAppend) Bytes() ([]byte, error) { return decodeAudio(e.Audio) }

// Bytes decodes the output audio delta.
func (e OutputAudioDelta) Bytes() ([]byte, error) { return decodeAudio(e.Delta) }

func decodeAudio(value string) ([]byte, error) {
	audio, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return nil, fmt.Errorf("openailive: decode base64 audio: %w", err)
	}
	return audio, nil
}
