package openailive

import (
	"encoding/json"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/internal/livesession"
)

// Model1 is the only GPT-Live model and snapshot.
const Model1 = "gpt-live-1"

// DefaultEndpoint is the primary GPT-Live WebSocket. It takes no query
// parameters; the model goes in session.start.
const DefaultEndpoint = "wss://api.openai.com/v1/live/sessions"

// Audio format types for SessionAudio.Format. One format applies to both
// input and output for the whole session.
const (
	AudioTypePCM  = livesession.AudioTypePCM // mono signed 16-bit little-endian PCM
	AudioTypePCMU = "audio/pcmu"             // G.711 mu-law, one byte per sample
	AudioTypePCMA = "audio/pcma"             // G.711 A-law, one byte per sample
)

// Sample rates GPT-Live accepts. PCM takes 16 or 24 kHz; G.711 takes 8 kHz.
const (
	RatePCM24k = 24000
	RatePCM16k = 16000
	RateG711   = 8000
)

// Delegation modes. Client delegation is the default when delegation is
// omitted or null.
const (
	DelegationClient    = "client"
	DelegationResponses = "responses"
)

// Session close reasons carried by session.closed.
const (
	CloseReasonCloseRequested = livesession.CloseReasonCloseRequested
	CloseReasonExpired        = livesession.CloseReasonExpired
	CloseReasonContent        = livesession.CloseReasonContent
	CloseReasonRemoteHangup   = livesession.CloseReasonRemoteHangup
	CloseReasonConnectionLost = livesession.CloseReasonConnectionLost
)

// SessionStatusActive is the status of a running SessionResource.
const SessionStatusActive = "active"

// Error types and the documented error codes. invalid_request_error is the
// only documented error type (call_error appears only in transport.failed),
// and the code list is incomplete; callers must handle unknown types and
// unknown or empty codes.
const (
	ErrorTypeInvalidRequest = "invalid_request_error"
	ErrorTypeCall           = "call_error"

	CodeUnknownParameter     = "unknown_parameter"
	CodeImmutableFieldUpdate = "immutable_field_update"
	CodeInvalidAudio         = "invalid_audio"
	CodeDecisionAlreadyMade  = "decision_already_made"
)

// SessionConfig is the strict session object of session.start. The server
// rejects unknown fields with unknown_parameter, so it has no field for
// turn detection, transcription, top-level tools or modalities.
type SessionConfig struct {
	Model        string        `json:"model"`
	Instructions string        `json:"instructions,omitempty"`
	Audio        *SessionAudio `json:"audio,omitempty"`
	Client       *ClientConfig `json:"client,omitempty"`
	Delegation   *Delegation   `json:"delegation,omitempty"`
	Input        []InitialItem `json:"input,omitzero"`
	Store        *bool         `json:"store,omitempty"`
}

// SessionResource is the resolved session the server reports in
// session.started, session.updated and session.closed: the SessionConfig
// plus its id, expiry (Unix seconds) and status. Azure omits status.
type SessionResource struct {
	ID        string `json:"id"`
	Status    string `json:"status,omitempty"`
	ExpiresAt int64  `json:"expires_at,omitempty"`
	SessionConfig
}

// SessionAudio holds the primary-WebSocket audio format and the output voice.
type SessionAudio struct {
	Format *AudioFormat `json:"format,omitempty"`
	Output *AudioOutput `json:"output,omitempty"`
}

// AudioFormat is one of the four documented formats, for example
// {"type":"audio/pcm","rate":24000}.
type AudioFormat struct {
	Type string `json:"type"`
	Rate int    `json:"rate"`
}

// AudioOutput selects the output voice. The server default is marin.
type AudioOutput struct {
	Voice Voice `json:"voice,omitzero"`
}

// Voice is a built-in voice name or a custom voice id. It encodes as a JSON
// string when ID is empty and as {"id": ID} otherwise.
type Voice struct {
	Name string
	ID   string
}

type customVoice struct {
	ID string `json:"id"`
}

// MarshalJSON encodes a named voice as a string and a custom voice as {id}.
func (v Voice) MarshalJSON() ([]byte, error) {
	if v.ID != "" {
		return json.Marshal(customVoice{ID: v.ID})
	}
	return json.Marshal(v.Name)
}

// UnmarshalJSON accepts either a voice name string or a {id} object.
func (v *Voice) UnmarshalJSON(data []byte) error {
	var name string
	if err := json.Unmarshal(data, &name); err == nil {
		*v = Voice{Name: name}
		return nil
	}
	var custom customVoice
	if err := json.Unmarshal(data, &custom); err != nil {
		return err
	}
	*v = Voice{ID: custom.ID}
	return nil
}

// ClientConfig holds WebRTC frontend data-channel permissions. The allowed
// lists are "all" or arrays, so they stay raw JSON.
type ClientConfig struct {
	DataChannel *DataChannelConfig `json:"data_channel,omitempty"`
}

// DataChannelConfig restricts the events a WebRTC frontend may exchange.
type DataChannelConfig struct {
	AllowedClientEvents json.RawMessage `json:"allowed_client_events,omitempty"`
	AllowedServerEvents json.RawMessage `json:"allowed_server_events,omitempty"`
}

// Delegation selects who does backend work: the client application
// ({"type":"client"}) or a hosted Responses model.
type Delegation struct {
	Type      string                     `json:"type"`
	Responses *ResponsesDelegationConfig `json:"responses,omitempty"`
}

// ResponsesDelegationConfig configures Responses delegation. Model is
// required at creation; session.update may send any subset.
type ResponsesDelegationConfig struct {
	Model             string          `json:"model,omitempty"`
	Instructions      *string         `json:"instructions,omitempty"`
	MaxOutputTokens   *int            `json:"max_output_tokens,omitempty"`
	ParallelToolCalls *bool           `json:"parallel_tool_calls,omitempty"`
	Reasoning         *Reasoning      `json:"reasoning,omitempty"`
	ServiceTier       string          `json:"service_tier,omitempty"`
	Text              *TextConfig     `json:"text,omitempty"`
	ToolChoice        json.RawMessage `json:"tool_choice,omitempty"`
	Tools             []Tool          `json:"tools,omitzero"`
}

// Reasoning configures the Responses backend's reasoning.
type Reasoning struct {
	Effort  string `json:"effort,omitempty"`
	Summary string `json:"summary,omitempty"`
}

// TextConfig configures the Responses backend's text verbosity.
type TextConfig struct {
	Verbosity string `json:"verbosity,omitempty"`
}

// Tool is a Responses delegation tool: a function tool or {"type":"web_search"}.
type Tool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name,omitempty"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Strict      *bool           `json:"strict,omitempty"`
}

// InitialItem is one text-only startup history message. Each message has
// exactly one text part.
type InitialItem struct {
	Type    string        `json:"type,omitempty"`
	ID      string        `json:"id,omitempty"`
	Role    string        `json:"role"`
	Status  string        `json:"status,omitempty"`
	Content []ContentPart `json:"content"`
}

// ContentPart is the text part of an InitialItem.
type ContentPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// Error is the body of an error event and of transport.failed.
type Error struct {
	Type          string  `json:"type"`
	Code          string  `json:"code,omitempty"`
	Message       string  `json:"message"`
	Param         *string `json:"param,omitempty"`
	ClientEventID string  `json:"client_event_id,omitempty"`
}

// Usage is the cumulative voice duration of a session, in seconds. It is a
// snapshot: never sum successive values.
type Usage struct {
	Seconds float64 `json:"seconds"`
}

// ContextWindow reports how full the Live context window is.
type ContextWindow struct {
	UsageRatio float64 `json:"usage_ratio"`
}

// DelegationInfo is the metadata of session.delegation.created. It carries no
// task text.
type DelegationInfo struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	Target     string `json:"target"`
	ResponseID string `json:"response_id,omitempty"`
}
