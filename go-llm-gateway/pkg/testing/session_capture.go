package testing

import "encoding/json"

const (
	// SessionCaptureVersion is the current on-disk session capture schema version.
	SessionCaptureVersion = 1
	// SessionPayloadTypeStreamMessage identifies payloads encoded as messages.StreamMessage JSON.
	SessionPayloadTypeStreamMessage = "stream_message"
	// SessionPayloadTypeWebSocketMessage identifies raw provider WebSocket JSON messages.
	SessionPayloadTypeWebSocketMessage = "websocket_message"
)

// SessionEventDirection indicates whether an event was sent by the client or received from the server.
type SessionEventDirection string

const (
	// DirectionClientToServer marks events sent from the client to the server.
	DirectionClientToServer SessionEventDirection = "client_to_server"
	// DirectionServerToClient marks events received from the server by the client.
	DirectionServerToClient SessionEventDirection = "server_to_client"
)

// CapturedSessionEvent is a single recorded event from a bidirectional session.
type CapturedSessionEvent struct {
	// Sequence is the logical ordering of this event within the session.
	Sequence int `json:"sequence"`
	// Direction indicates whether the event was sent or received.
	Direction SessionEventDirection `json:"direction"`
	// TimestampMs is the time elapsed since session start, in milliseconds.
	TimestampMs int64 `json:"timestamp_ms"`
	// Type is the session event type (e.g. "session.created", "response.output_text.delta").
	Type string `json:"type"`
	// PayloadType describes how Payload should be decoded.
	PayloadType string `json:"payload_type"`
	// Payload is the serialized event payload.
	Payload json.RawMessage `json:"payload,omitempty"`
	// Data is kept for backwards compatibility with pre-envelope session captures.
	Data json.RawMessage `json:"data,omitempty"`
}

// SessionProviderMetadata describes the provider whose traffic was captured.
type SessionProviderMetadata struct {
	Name  string `json:"name,omitempty"`
	Model string `json:"model,omitempty"`
}

// SessionMetadata describes the captured session without storing credentials.
type SessionMetadata struct {
	ID                string `json:"id,omitempty"`
	StartedAtUTC      string `json:"started_at_utc,omitempty"`
	FixtureProvenance string `json:"fixture_provenance,omitempty"`
}

// SessionCapture is the on-disk envelope for bidirectional session traffic.
type SessionCapture struct {
	Version  int                     `json:"version"`
	Provider SessionProviderMetadata `json:"provider"`
	Session  SessionMetadata         `json:"session"`
	Records  []CapturedSessionEvent  `json:"records"`
}
