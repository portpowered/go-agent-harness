// Package parity defines the transport-independent facts retained from a
// speech-to-speech transcript for later comparison.
package parity

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
)

// Projection is the complete, ordered, transport-independent speech-to-speech
// evidence retained by normalization. A successful reduction initializes all
// slices, including when the corresponding fact is absent.
type Projection struct {
	Turns         []TurnBoundary     `json:"turns"`
	Audio         AudioSummary       `json:"audio"`
	Transcripts   []TranscriptFact   `json:"transcripts"`
	ToolCalls     []ToolCallFact     `json:"toolCalls"`
	Interruptions []InterruptionFact `json:"interruptions"`
	Terminal      *TerminalOutcome   `json:"terminal,omitempty"`
}

// TurnBoundary records one semantic beginning or end of a turn. Order is the
// one-based position in the source sequence of turn boundaries.
type TurnBoundary struct {
	Order    int    `json:"order"`
	Tick     uint64 `json:"tick"`
	Kind     string `json:"kind"`
	Boundary string `json:"boundary"`
	ID       string `json:"id,omitempty"`
	Role     string `json:"role,omitempty"`
	Payload  []byte `json:"payload"`
}

// AudioSummary contains every retained logical audio frame and its aggregate
// byte count. Frame order is source order; it is never sorted by timestamp.
type AudioSummary struct {
	FrameCount int          `json:"frameCount"`
	TotalBytes int          `json:"totalBytes"`
	Frames     []AudioFrame `json:"frames"`
}

// AudioFrame records the logical tick and decoded audio bytes for one semantic
// frame. Payload is the exact source record payload, retained as evidence.
type AudioFrame struct {
	Tick    uint64 `json:"tick"`
	Bytes   []byte `json:"bytes"`
	Payload []byte `json:"payload"`
}

// TranscriptFact retains transcript text exactly as observed. Payload is the
// untouched source record payload, so unknown fields and repeated JSON values
// remain available to later evidence consumers.
type TranscriptFact struct {
	Order   int    `json:"order"`
	Tick    uint64 `json:"tick"`
	Text    string `json:"text"`
	Payload []byte `json:"payload"`
}

// ToolCallFact correlates a call and its result by ID. A recording may end
// while a call is pending, so either payload may be absent. Arguments and
// Result contain the exact JSON value bytes from their source fields, while
// CallPayload and ResultPayload retain each complete source payload exactly.
type ToolCallFact struct {
	Order         int    `json:"order"`
	ID            string `json:"id"`
	Name          string `json:"name,omitempty"`
	Arguments     []byte `json:"arguments,omitempty"`
	Result        []byte `json:"result,omitempty"`
	CallTick      uint64 `json:"callTick,omitempty"`
	ResultTick    uint64 `json:"resultTick,omitempty"`
	CallPayload   []byte `json:"callPayload,omitempty"`
	ResultPayload []byte `json:"resultPayload,omitempty"`
}

// InterruptionFact records an ordered semantic interruption point.
type InterruptionFact struct {
	Order      int    `json:"order"`
	Tick       uint64 `json:"tick"`
	Reason     string `json:"reason"`
	Provenance string `json:"provenance,omitempty"`
	Payload    []byte `json:"payload"`
}

// TerminalOutcome records the terminal reason and the layer that authored it.
type TerminalOutcome struct {
	Tick       uint64 `json:"tick"`
	Reason     string `json:"reason"`
	Provenance string `json:"provenance"`
	Payload    []byte `json:"payload"`
}

// ErrNormalization is the sentinel wrapped by every NormalizationError.
var ErrNormalization = errors.New("parity normalization failed")

// NormalizationError identifies the interface, indexed source field, and
// reason for an input that cannot represent the parity contract.
type NormalizationError struct {
	Interface string
	Field     string
	Reason    string
}

func (e *NormalizationError) Error() string {
	return fmt.Sprintf("%s observation %s: %s", e.Interface, e.Field, e.Reason)
}

func (e *NormalizationError) Unwrap() error { return ErrNormalization }

// Normalize validates every record before reducing any of them. It returns no
// partial projection when one record is malformed or unsupported.
func Normalize(interfaceName string, records []transcript.Record) (Projection, error) {
	validated := make([]parsedRecord, 0, len(records))
	for index, record := range records {
		parsed, err := validateRecord(interfaceName, index, record)
		if err != nil {
			return Projection{}, err
		}
		validated = append(validated, parsed)
	}

	projection := emptyProjection()
	toolSlots := make(map[string][]int)
	for index, record := range validated {
		if err := reduceRecord(&projection, toolSlots, index, record); err != nil {
			return Projection{}, withInterface(interfaceName, err)
		}
	}
	return projection, nil
}

type recordKind string

const (
	kindTurnStart  recordKind = "turn.start"
	kindTurnEnd    recordKind = "turn.end"
	kindAudioFrame recordKind = "audio.frame"
	kindTranscript recordKind = "transcript"
	kindToolCall   recordKind = "tool.call"
	kindToolResult recordKind = "tool.result"
	kindInterrupt  recordKind = "interrupt"
	kindTerminal   recordKind = "terminal"
	kindTransport  recordKind = "transport"
)

type parsedRecord struct {
	record     transcript.Record
	kind       recordKind
	raw        []byte
	audioBytes []byte
	semantic   []byte
	id         string
	name       string
	text       string
	reason     string
	provenance string
	arguments  []byte
}

func emptyProjection() Projection {
	return Projection{
		Turns:         make([]TurnBoundary, 0),
		Audio:         AudioSummary{Frames: make([]AudioFrame, 0)},
		Transcripts:   make([]TranscriptFact, 0),
		ToolCalls:     make([]ToolCallFact, 0),
		Interruptions: make([]InterruptionFact, 0),
	}
}

func validateRecord(interfaceName string, index int, record transcript.Record) (parsedRecord, error) {
	field := func(name string) string { return fmt.Sprintf("records[%d].%s", index, name) }
	if record.Version == 0 {
		return parsedRecord{}, newNormalizationError(interfaceName, field("version"), "is missing format version")
	}
	if record.Version != transcript.FormatVersion {
		return parsedRecord{}, newNormalizationError(interfaceName, field("version"), fmt.Sprintf("unsupported format version %d", record.Version))
	}
	if record.Peer != transcript.PeerClient && record.Peer != transcript.PeerAgent {
		return parsedRecord{}, newNormalizationError(interfaceName, field("peer"), fmt.Sprintf("unknown peer %q", record.Peer))
	}
	if record.Direction != transcript.DirectionIn && record.Direction != transcript.DirectionOut {
		return parsedRecord{}, newNormalizationError(interfaceName, field("direction"), fmt.Sprintf("unknown direction %q", record.Direction))
	}
	if !knownStream(record.Stream) {
		return parsedRecord{}, newNormalizationError(interfaceName, field("stream"), fmt.Sprintf("unknown stream %q", record.Stream))
	}
	if len(record.Payload) == 0 {
		return parsedRecord{}, newNormalizationError(interfaceName, field("payload"), "is required")
	}

	// Timestamp is derived wall-clock arrival data. Peer and Direction identify
	// the recorder viewpoint, while Stream identifies a transport channel; all
	// three are validated above but are intentionally absent from Projection.
	raw := clone(record.Payload)
	fields, kind, err := decodePayload(raw)
	if err != nil {
		// Raw audio is the only supported non-JSON form. Its stream identifies the
		// audio evidence, so the bytes and logical tick remain comparable.
		if isAudioStream(record.Stream) && !json.Valid(raw) {
			return parsedRecord{record: record, kind: kindAudioFrame, raw: raw, audioBytes: raw}, nil
		}
		return parsedRecord{}, newNormalizationError(interfaceName, field("payload"), err.Error())
	}
	if kind == "" {
		return parsedRecord{}, newNormalizationError(interfaceName, field("kind"), "is required")
	}
	if isTransportMechanicKind(kind) {
		return parsedRecord{record: record, kind: kindTransport, raw: raw}, nil
	}

	parsed := parsedRecord{record: record, raw: raw}
	switch kind {
	case string(kindTurnStart), string(kindTurnEnd):
		parsed.kind = recordKind(kind)
		parsed.id, err = optionalString(fields, "id")
		if err != nil {
			return parsedRecord{}, newNormalizationError(interfaceName, field("payload.id"), err.Error())
		}
		parsed.name, err = optionalString(fields, "role")
		if err != nil {
			return parsedRecord{}, newNormalizationError(interfaceName, field("payload.role"), err.Error())
		}
	case string(kindAudioFrame):
		parsed.kind = kindAudioFrame
		parsed.audioBytes, err = audioValue(fields)
		if err != nil {
			return parsedRecord{}, newNormalizationError(interfaceName, field("payload.bytes"), err.Error())
		}
	case string(kindTranscript):
		parsed.kind = kindTranscript
		parsed.text, _, err = requiredStringValue(fields, "text", "value")
		if err != nil {
			return parsedRecord{}, newNormalizationError(interfaceName, field("payload.text"), err.Error())
		}
	case string(kindToolCall):
		parsed.kind = kindToolCall
		parsed.id, err = requiredString(fields, "id")
		if err != nil {
			return parsedRecord{}, newNormalizationError(interfaceName, field("payload.id"), err.Error())
		}
		parsed.name, err = optionalString(fields, "name")
		if err != nil {
			return parsedRecord{}, newNormalizationError(interfaceName, field("payload.name"), err.Error())
		}
		parsed.arguments = optionalRawValue(fields, "arguments")
	case string(kindToolResult):
		parsed.kind = kindToolResult
		parsed.id, err = requiredString(fields, "id")
		if err != nil {
			return parsedRecord{}, newNormalizationError(interfaceName, field("payload.id"), err.Error())
		}
		parsed.semantic = optionalRawValue(fields, "result", "value")
		if parsed.semantic == nil {
			return parsedRecord{}, newNormalizationError(interfaceName, field("payload.result"), "is required")
		}
	case string(kindInterrupt):
		parsed.kind = kindInterrupt
		parsed.reason, err = requiredString(fields, "reason")
		if err != nil {
			return parsedRecord{}, newNormalizationError(interfaceName, field("payload.reason"), err.Error())
		}
		parsed.provenance, err = optionalString(fields, "provenance")
		if err != nil {
			return parsedRecord{}, newNormalizationError(interfaceName, field("payload.provenance"), err.Error())
		}
	case string(kindTerminal):
		parsed.kind = kindTerminal
		parsed.reason, err = requiredString(fields, "reason")
		if err != nil {
			return parsedRecord{}, newNormalizationError(interfaceName, field("payload.reason"), err.Error())
		}
		parsed.provenance, err = requiredString(fields, "provenance")
		if err != nil {
			return parsedRecord{}, newNormalizationError(interfaceName, field("payload.provenance"), err.Error())
		}
	default:
		return parsedRecord{}, newNormalizationError(interfaceName, field("kind"), fmt.Sprintf("unknown record kind %q (unsupported)", kind))
	}
	return parsed, nil
}

func reduceRecord(projection *Projection, toolSlots map[string][]int, index int, record parsedRecord) error {
	switch record.kind {
	case kindTransport:
		// These explicitly enumerated records carry only transport identity or
		// packet/frame segmentation. Those mechanics vary between recordings and
		// cannot be compared; no semantic payload is discarded by this case.
		return nil
	case kindTurnStart, kindTurnEnd:
		boundary := "start"
		if record.kind == kindTurnEnd {
			boundary = "end"
		}
		projection.Turns = append(projection.Turns, TurnBoundary{
			Order: len(projection.Turns) + 1, Tick: record.record.Tick, Kind: string(record.kind), Boundary: boundary,
			ID: record.id, Role: record.name, Payload: clone(record.raw),
		})
	case kindAudioFrame:
		projection.Audio.Frames = append(projection.Audio.Frames, AudioFrame{
			Tick: record.record.Tick, Bytes: clone(record.audioBytes), Payload: clone(record.raw),
		})
		projection.Audio.FrameCount++
		projection.Audio.TotalBytes += len(record.audioBytes)
	case kindTranscript:
		projection.Transcripts = append(projection.Transcripts, TranscriptFact{
			Order: len(projection.Transcripts) + 1, Tick: record.record.Tick, Text: record.text, Payload: clone(record.raw),
		})
	case kindToolCall:
		toolIndex := findToolSlot(toolSlots, projection.ToolCalls, record.id, false)
		if toolIndex < 0 {
			projection.ToolCalls = append(projection.ToolCalls, ToolCallFact{Order: len(projection.ToolCalls) + 1, ID: record.id})
			toolIndex = len(projection.ToolCalls) - 1
			toolSlots[record.id] = append(toolSlots[record.id], toolIndex)
		}
		tool := &projection.ToolCalls[toolIndex]
		tool.Name, tool.Arguments, tool.CallTick, tool.CallPayload = record.name, clone(record.arguments), record.record.Tick, clone(record.raw)
	case kindToolResult:
		toolIndex := findToolSlot(toolSlots, projection.ToolCalls, record.id, true)
		if toolIndex < 0 {
			projection.ToolCalls = append(projection.ToolCalls, ToolCallFact{Order: len(projection.ToolCalls) + 1, ID: record.id})
			toolIndex = len(projection.ToolCalls) - 1
			toolSlots[record.id] = append(toolSlots[record.id], toolIndex)
		}
		tool := &projection.ToolCalls[toolIndex]
		tool.Result, tool.ResultTick, tool.ResultPayload = clone(record.semantic), record.record.Tick, clone(record.raw)
	case kindInterrupt:
		projection.Interruptions = append(projection.Interruptions, InterruptionFact{
			Order: len(projection.Interruptions) + 1, Tick: record.record.Tick, Reason: record.reason,
			Provenance: record.provenance, Payload: clone(record.raw),
		})
	case kindTerminal:
		if projection.Terminal != nil {
			return &NormalizationError{Field: fmt.Sprintf("records[%d].kind", index), Reason: "contains more than one terminal outcome"}
		}
		projection.Terminal = &TerminalOutcome{
			Tick: record.record.Tick, Reason: record.reason, Provenance: record.provenance, Payload: clone(record.raw),
		}
	}
	return nil
}

func findToolSlot(slots map[string][]int, calls []ToolCallFact, id string, lookingForResult bool) int {
	for _, index := range slots[id] {
		call := calls[index]
		if lookingForResult && call.ResultPayload == nil {
			return index
		}
		if !lookingForResult && call.CallPayload == nil {
			return index
		}
	}
	return -1
}

func decodePayload(raw []byte) (map[string]json.RawMessage, string, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, "", fmt.Errorf("must be valid JSON object: %w", err)
	}
	if fields == nil {
		return nil, "", errors.New("must be a JSON object")
	}

	kind, present, err := firstString(fields, "kind", "type")
	if err != nil {
		return nil, "", fmt.Errorf("kind %s", err)
	}
	if nested, ok := objectField(fields, "value"); ok {
		for key, value := range nested {
			if _, exists := fields[key]; !exists {
				fields[key] = value
			}
		}
		if !present {
			kind, _, err = firstString(fields, "kind", "type")
			if err != nil {
				return nil, "", fmt.Errorf("kind %s", err)
			}
		}
	}
	return fields, kind, nil
}

// Only these payload kinds are intentionally omitted. Each names concrete
// transport identity or packet/frame segmentation, never a conversation fact.
func isTransportMechanicKind(kind string) bool {
	switch kind {
	case "transport.id", "transport.identifier", "transport.packet", "transport.frame", "transport.segment":
		return true
	default:
		return false
	}
}

func knownStream(stream transcript.Stream) bool {
	switch stream {
	case transcript.StreamWS, transcript.StreamRTCAudio, transcript.StreamRTCData, transcript.StreamDeviceIn, transcript.StreamDeviceOut:
		return true
	default:
		return false
	}
}

func isAudioStream(stream transcript.Stream) bool {
	return stream == transcript.StreamRTCAudio || stream == transcript.StreamDeviceIn || stream == transcript.StreamDeviceOut
}

func firstRaw(fields map[string]json.RawMessage, names ...string) (json.RawMessage, bool) {
	for _, name := range names {
		if raw, ok := fields[name]; ok {
			return raw, true
		}
	}
	return nil, false
}

func firstString(fields map[string]json.RawMessage, names ...string) (string, bool, error) {
	raw, present := firstRaw(fields, names...)
	if !present {
		return "", false, nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", true, errors.New("must be a string")
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", true, errors.New("must be a string")
	}
	return value, true, nil
}

func requiredString(fields map[string]json.RawMessage, names ...string) (string, error) {
	value, present, err := firstString(fields, names...)
	if err != nil {
		return "", err
	}
	if !present || strings.TrimSpace(value) == "" {
		return "", errors.New("is required")
	}
	return value, nil
}

func optionalString(fields map[string]json.RawMessage, names ...string) (string, error) {
	value, present, err := firstString(fields, names...)
	if err != nil {
		return "", err
	}
	if !present {
		return "", nil
	}
	return value, nil
}

func requiredStringValue(fields map[string]json.RawMessage, names ...string) (string, []byte, error) {
	raw, present := firstRaw(fields, names...)
	if !present || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", nil, errors.New("is required")
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", nil, errors.New("must be a string")
	}
	return value, clone(raw), nil
}

func optionalRawValue(fields map[string]json.RawMessage, names ...string) []byte {
	raw, ok := firstRaw(fields, names...)
	if !ok {
		return nil
	}
	return clone(raw)
}

func audioValue(fields map[string]json.RawMessage) ([]byte, error) {
	raw, ok := firstRaw(fields, "bytes")
	if !ok {
		return nil, errors.New("is required")
	}
	return decodeAudioBytes(raw)
}

func decodeAudioBytes(raw json.RawMessage) ([]byte, error) {
	trimmed := bytes.TrimSpace(raw)
	if bytes.Equal(trimmed, []byte("null")) {
		return nil, errors.New("must contain byte values or a base64 string")
	}
	if len(trimmed) > 0 && trimmed[0] == '[' {
		var values []byte
		if err := json.Unmarshal(trimmed, &values); err != nil {
			return nil, errors.New("must contain byte values")
		}
		return clone(values), nil
	}
	var value string
	if err := json.Unmarshal(trimmed, &value); err != nil {
		return nil, errors.New("must contain byte values or a base64 string")
	}
	decoded, err := codec.DecodeBase64WithLimit(value, max(1, len(value)))
	if err != nil {
		return nil, errors.New("must contain byte values or a base64 string")
	}
	return decoded, nil
}

func objectField(fields map[string]json.RawMessage, name string) (map[string]json.RawMessage, bool) {
	raw, ok := fields[name]
	if !ok {
		return nil, false
	}
	var nested map[string]json.RawMessage
	if json.Unmarshal(raw, &nested) != nil || nested == nil {
		return nil, false
	}
	return nested, true
}

func clone(value []byte) []byte {
	if value == nil {
		return nil
	}
	return append([]byte(nil), value...)
}

func newNormalizationError(interfaceName, field, reason string) *NormalizationError {
	return &NormalizationError{Interface: interfaceName, Field: field, Reason: reason}
}

func withInterface(interfaceName string, err error) error {
	var normalizationErr *NormalizationError
	if errors.As(err, &normalizationErr) {
		if normalizationErr.Interface == "" {
			normalizationErr.Interface = interfaceName
		}
		return normalizationErr
	}
	return newNormalizationError(interfaceName, "records", err.Error())
}
