// Package transcript defines the versioned, binary-safe frame format used by
// both-side speech-to-speech recordings.
//
// A payload is opaque. The codec only base64-encodes it for JSON transport and
// never parses, normalizes, or re-encodes the bytes it was given.
package transcript

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// FormatVersion is the transcript record schema version. Consumers must read
// it before interpreting a record; changing it is a breaking format change.
const FormatVersion = 1

// Peer identifies which side authored a frame.
type Peer string

const (
	// PeerClient authored the frame as the speech-to-speech client.
	PeerClient Peer = "client"
	// PeerAgent authored the frame as the speech-to-speech agent.
	PeerAgent Peer = "agent"
)

// Direction is relative to the process making the recording, not to the
// protocol. In means received by the recorder and out means sent by it.
type Direction string

const (
	// DirectionIn means the recording process received the frame.
	DirectionIn Direction = "in"
	// DirectionOut means the recording process sent the frame.
	DirectionOut Direction = "out"
)

// Stream identifies the channel on which a frame was observed.
type Stream string

const (
	StreamWS        Stream = "ws"
	StreamRTCAudio  Stream = "rtc-audio"
	StreamRTCData   Stream = "rtc-data"
	StreamDeviceIn  Stream = "device-in"
	StreamDeviceOut Stream = "device-out"
	// StreamRuntimeMessage contains normalized agent-session observations, not raw transport frames.
	StreamRuntimeMessage Stream = "runtime-message"
	// StreamRuntimeAudio indexes PCM observed at a session media port.
	StreamRuntimeAudio Stream = "runtime-audio"
	// StreamRuntimeEvent preserves session/tool lifecycle observations and error text.
	StreamRuntimeEvent Stream = "runtime-event"

	// StreamWebSocket is a descriptive alias for StreamWS.
	StreamWebSocket = StreamWS
)

var (
	// ErrMissingVersion identifies a record with no usable format version.
	ErrMissingVersion = errors.New("transcript: missing format version")
	// ErrUnsupportedVersion identifies a nonzero version this package cannot read.
	ErrUnsupportedVersion = errors.New("transcript: unsupported format version")

	// Descriptive aliases for callers that name the field explicitly.
	ErrMissingFormatVersion     = ErrMissingVersion
	ErrUnsupportedFormatVersion = ErrUnsupportedVersion
)

// Record is one versioned transcript frame. Timestamp is the stable UTC
// representation derived from Tick by the caller's logical clock. Payload is
// opaque bytes; its JSON representation is a base64 string.
type Record struct {
	Version   int       `json:"v"`
	Tick      uint64    `json:"tick"`
	Timestamp string    `json:"t"`
	Peer      Peer      `json:"peer"`
	Direction Direction `json:"dir"`
	Stream    Stream    `json:"stream"`
	Payload   []byte    `json:"payload"`
}

// NewRecord creates a version-1 record and copies payload so the record owns
// its bytes. Timestamps are normalized to UTC with RFC3339Nano precision.
func NewRecord(tick uint64, timestamp time.Time, peer Peer, direction Direction, stream Stream, payload []byte) Record {
	return Record{
		Version:   FormatVersion,
		Tick:      tick,
		Timestamp: timestamp.UTC().Format(time.RFC3339Nano),
		Peer:      peer,
		Direction: direction,
		Stream:    stream,
		Payload:   append([]byte(nil), payload...),
	}
}

// recordJSON is deliberately separate from Record so payload is always
// emitted as a JSON string, including for a nil or empty byte slice.
type recordJSON struct {
	Version   int       `json:"v"`
	Tick      uint64    `json:"tick"`
	Timestamp string    `json:"t"`
	Peer      Peer      `json:"peer"`
	Direction Direction `json:"dir"`
	Stream    Stream    `json:"stream"`
	Payload   string    `json:"payload"`
}

// MarshalJSON emits the pinned field order and binary-safe payload encoding.
func (r Record) MarshalJSON() ([]byte, error) {
	return json.Marshal(recordJSON{
		Version:   r.Version,
		Tick:      r.Tick,
		Timestamp: r.Timestamp,
		Peer:      r.Peer,
		Direction: r.Direction,
		Stream:    r.Stream,
		Payload:   base64.StdEncoding.EncodeToString(r.Payload),
	})
}

// UnmarshalJSON validates the format version before interpreting the rest of
// the record. Unknown top-level fields are ignored, while payload bytes remain
// opaque because only base64 decoding is performed.
func (r *Record) UnmarshalJSON(data []byte) error {
	if r == nil {
		return errors.New("transcript: unmarshal into nil record")
	}

	var version struct {
		Version *int `json:"v"`
	}
	if err := json.Unmarshal(data, &version); err != nil {
		return fmt.Errorf("transcript: read format version: %w", err)
	}
	if version.Version == nil || *version.Version == 0 {
		return ErrMissingVersion
	}
	if *version.Version != FormatVersion {
		return fmt.Errorf("%w: got %d, want %d", ErrUnsupportedVersion, *version.Version, FormatVersion)
	}

	var encoded struct {
		recordJSON
		Payload *string `json:"payload"`
	}
	if err := json.Unmarshal(data, &encoded); err != nil {
		return fmt.Errorf("transcript: decode record: %w", err)
	}
	if encoded.Payload == nil {
		return errors.New("transcript: missing payload")
	}
	payload, err := base64.StdEncoding.DecodeString(*encoded.Payload)
	if err != nil {
		return fmt.Errorf("transcript: decode payload: %w", err)
	}

	*r = Record{
		Version:   encoded.Version,
		Tick:      encoded.Tick,
		Timestamp: encoded.Timestamp,
		Peer:      encoded.Peer,
		Direction: encoded.Direction,
		Stream:    encoded.Stream,
		Payload:   payload,
	}
	return nil
}

// Encode returns one newline-delimited JSON record.
func Encode(record Record) ([]byte, error) {
	encoded, err := json.Marshal(record)
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

// Decode reads one JSONL record. Surrounding JSON whitespace, including its
// terminating newline, is accepted; a second non-whitespace value is not.
func Decode(line []byte) (Record, error) {
	var record Record
	if err := json.Unmarshal(line, &record); err != nil {
		return Record{}, err
	}
	return record, nil
}
