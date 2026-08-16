// Package parity defines the transport-independent facts retained from a
// speech-to-speech transcript for later comparison.
package parity

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
)

// RecordKind is the semantic kind carried by a transcript record payload.
// Payloads may use either kind or type; the reducer maps supported spellings
// to these canonical values before reducing them.
type RecordKind string

const (
	RecordKindTurnStart  RecordKind = "turn.start"
	RecordKindTurnEnd    RecordKind = "turn.end"
	RecordKindAudioFrame RecordKind = "audio.frame"
	RecordKindTranscript RecordKind = "transcript"
	RecordKindToolCall   RecordKind = "tool.call"
	RecordKindToolResult RecordKind = "tool.result"
	RecordKindInterrupt  RecordKind = "interrupt"
	RecordKindTerminal   RecordKind = "terminal"
	RecordKindIgnored    RecordKind = "ignored"
)

// Projection is the complete, ordered, transport-independent speech-to-speech
// evidence retained by normalization. A successful reduction returns all
// slices initialized, including when the corresponding fact is absent.
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

// Turn and TurnFact are descriptive aliases for callers that prefer shorter
// or fact-oriented names.
type Turn = TurnBoundary
type TurnFact = TurnBoundary

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

// Transcript is a descriptive alias for TranscriptFact.
type Transcript = TranscriptFact

// ToolCallFact correlates a call and its result by ID. A recording may end
// while a call is pending, so either payload may be absent. Arguments and
// Result retain the semantic value bytes; the CallPayload and ResultPayload
// fields retain each complete source payload exactly.
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

// ToolCall is a descriptive alias for ToolCallFact.
type ToolCall = ToolCallFact

// InterruptionFact records an ordered semantic interruption point.
type InterruptionFact struct {
	Order      int    `json:"order"`
	Tick       uint64 `json:"tick"`
	Reason     string `json:"reason"`
	Provenance string `json:"provenance,omitempty"`
	Payload    []byte `json:"payload"`
}

// Interruption is a descriptive alias for InterruptionFact.
type Interruption = InterruptionFact

// TerminalOutcome records the terminal reason and the layer that authored it.
type TerminalOutcome struct {
	Tick       uint64 `json:"tick"`
	Reason     string `json:"reason"`
	Provenance string `json:"provenance"`
	Payload    []byte `json:"payload"`
}

// Terminal is a descriptive alias for TerminalOutcome.
type Terminal = TerminalOutcome

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

// Reduce is an equivalent verb for Normalize.
func Reduce(interfaceName string, records []transcript.Record) (Projection, error) {
	return Normalize(interfaceName, records)
}

// NormalizeRecords is an explicit name for callers normalizing a record list.
func NormalizeRecords(interfaceName string, records []transcript.Record) (Projection, error) {
	return Normalize(interfaceName, records)
}

// Project uses record-first argument order for callers that treat projection
// as the primary operation.
func Project(records []transcript.Record, interfaceName string) (Projection, error) {
	return Normalize(interfaceName, records)
}

type parsedRecord struct {
	record transcript.Record
	kind   RecordKind
	fields map[string]json.RawMessage
	raw    []byte
	// audioBytes is populated for audio frames after payload validation.
	audioBytes []byte
	// semanticValue is the exact value bytes for transcript, tool, and terminal
	// fields that have a value separate from the complete raw payload.
	semanticValue []byte
	id            string
	name          string
	text          string
	reason        string
	provenance    string
	arguments     []byte
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
	if len(bytes.TrimSpace(record.Payload)) == 0 {
		return parsedRecord{}, newNormalizationError(interfaceName, field("payload"), "is required")
	}

	raw := clone(record.Payload)
	fields, kindText, err := decodePayload(raw)
	if err != nil {
		// A raw audio record is the one captured form whose payload is not a
		// JSON envelope. Its stream identifies the audio evidence; all other
		// non-JSON payloads are malformed record content.
		if isAudioStream(record.Stream) && !json.Valid(raw) {
			return parsedRecord{record: record, kind: RecordKindAudioFrame, raw: raw, audioBytes: raw}, nil
		}
		return parsedRecord{}, newNormalizationError(interfaceName, field("payload"), err.Error())
	}
	kind, ok := canonicalKind(kindText, fields)
	if !ok {
		if kindText == "" {
			return parsedRecord{}, newNormalizationError(interfaceName, field("kind"), "is required")
		}
		return parsedRecord{}, newNormalizationError(interfaceName, field("kind"), fmt.Sprintf("unknown record kind %q", kindText))
	}
	parsed := parsedRecord{record: record, kind: kind, fields: fields, raw: raw}
	if kind == RecordKindIgnored {
		return parsed, nil
	}

	// The timestamp is derived wall-clock arrival data. Logical Tick is the
	// deterministic sequencing fact and is retained instead of Timestamp.
	// Peer, Direction, and Stream identify recorder viewpoint or transport;
	// they are validated above and intentionally do not enter the projection.

	switch kind {
	case RecordKindTurnStart, RecordKindTurnEnd:
		parsed.id, err = optionalString(fields, "id", "turn_id", "turnId")
		if err != nil {
			return parsedRecord{}, newNormalizationError(interfaceName, field("payload.id"), err.Error())
		}
		parsed.name, err = optionalString(fields, "role", "speaker", "actor")
		if err != nil {
			return parsedRecord{}, newNormalizationError(interfaceName, field("payload.role"), err.Error())
		}
	case RecordKindAudioFrame:
		parsed.audioBytes, err = audioValue(fields)
		if err != nil {
			return parsedRecord{}, newNormalizationError(interfaceName, field("payload.audio"), err.Error())
		}
	case RecordKindTranscript:
		parsed.text, parsed.semanticValue, err = requiredStringValue(fields, "text", "content", "transcript", "value")
		if err != nil {
			return parsedRecord{}, newNormalizationError(interfaceName, field("payload.text"), err.Error())
		}
	case RecordKindToolCall:
		parsed.id, err = requiredString(fields, "id", "tool_call_id", "toolCallId", "call_id")
		if err != nil {
			return parsedRecord{}, newNormalizationError(interfaceName, field("payload.id"), err.Error())
		}
		parsed.name, err = optionalString(fields, "name", "tool_name", "toolName")
		if err != nil {
			return parsedRecord{}, newNormalizationError(interfaceName, field("payload.name"), err.Error())
		}
		parsed.arguments = semanticField(fields, "arguments", "args", "input", "parameters")
	case RecordKindToolResult:
		parsed.id, err = requiredString(fields, "id", "tool_call_id", "toolCallId", "call_id")
		if err != nil {
			return parsedRecord{}, newNormalizationError(interfaceName, field("payload.id"), err.Error())
		}
		parsed.semanticValue = semanticField(fields, "result", "output", "content", "value")
		if parsed.semanticValue == nil {
			return parsedRecord{}, newNormalizationError(interfaceName, field("payload.result"), "is required")
		}
	case RecordKindInterrupt:
		parsed.reason, err = requiredString(fields, "reason", "interruption_reason", "interrupt_reason")
		if err != nil {
			return parsedRecord{}, newNormalizationError(interfaceName, field("payload.reason"), err.Error())
		}
		parsed.provenance, err = optionalString(fields, "provenance", "interrupt_provenance", "source")
		if err != nil {
			return parsedRecord{}, newNormalizationError(interfaceName, field("payload.provenance"), err.Error())
		}
	case RecordKindTerminal:
		parsed.reason, err = requiredString(fields, "reason", "terminal_reason", "terminalReason")
		if err != nil {
			return parsedRecord{}, newNormalizationError(interfaceName, field("payload.reason"), err.Error())
		}
		parsed.provenance, err = requiredString(fields, "provenance", "terminal_provenance", "terminalProvenance", "source")
		if err != nil {
			return parsedRecord{}, newNormalizationError(interfaceName, field("payload.provenance"), err.Error())
		}
	}
	return parsed, nil
}

func reduceRecord(projection *Projection, toolSlots map[string][]int, index int, record parsedRecord) error {
	switch record.kind {
	case RecordKindIgnored:
		// Transport framing and derived lifecycle chatter do not describe a
		// comparable conversation fact; their mechanics are intentionally
		// omitted here, while every semantic record is retained below.
		return nil
	case RecordKindTurnStart, RecordKindTurnEnd:
		boundary := "start"
		if record.kind == RecordKindTurnEnd {
			boundary = "end"
		}
		projection.Turns = append(projection.Turns, TurnBoundary{
			Order: len(projection.Turns) + 1, Tick: record.record.Tick, Kind: string(record.kind), Boundary: boundary,
			ID: record.id, Role: record.name, Payload: clone(record.raw),
		})
	case RecordKindAudioFrame:
		projection.Audio.Frames = append(projection.Audio.Frames, AudioFrame{
			Tick: record.record.Tick, Bytes: clone(record.audioBytes), Payload: clone(record.raw),
		})
		projection.Audio.FrameCount++
		projection.Audio.TotalBytes += len(record.audioBytes)
	case RecordKindTranscript:
		projection.Transcripts = append(projection.Transcripts, TranscriptFact{
			Order: len(projection.Transcripts) + 1, Tick: record.record.Tick, Text: record.text, Payload: clone(record.raw),
		})
	case RecordKindToolCall:
		toolIndex := findToolSlot(toolSlots, projection.ToolCalls, record.id, false)
		if toolIndex < 0 {
			projection.ToolCalls = append(projection.ToolCalls, ToolCallFact{Order: len(projection.ToolCalls) + 1, ID: record.id})
			toolIndex = len(projection.ToolCalls) - 1
		}
		tool := &projection.ToolCalls[toolIndex]
		tool.Name, tool.Arguments, tool.CallTick, tool.CallPayload = record.name, clone(record.arguments), record.record.Tick, clone(record.raw)
		toolSlots[record.id] = appendIfMissing(toolSlots[record.id], toolIndex)
	case RecordKindToolResult:
		toolIndex := findToolSlot(toolSlots, projection.ToolCalls, record.id, true)
		if toolIndex < 0 {
			projection.ToolCalls = append(projection.ToolCalls, ToolCallFact{Order: len(projection.ToolCalls) + 1, ID: record.id})
			toolIndex = len(projection.ToolCalls) - 1
		}
		tool := &projection.ToolCalls[toolIndex]
		tool.Result, tool.ResultTick, tool.ResultPayload = clone(record.semanticValue), record.record.Tick, clone(record.raw)
		toolSlots[record.id] = appendIfMissing(toolSlots[record.id], toolIndex)
	case RecordKindInterrupt:
		projection.Interruptions = append(projection.Interruptions, InterruptionFact{
			Order: len(projection.Interruptions) + 1, Tick: record.record.Tick, Reason: record.reason,
			Provenance: record.provenance, Payload: clone(record.raw),
		})
	case RecordKindTerminal:
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
		if index >= len(calls) {
			continue
		}
		call := calls[index]
		if lookingForResult {
			if call.ResultPayload == nil {
				return index
			}
		} else if call.CallPayload == nil {
			return index
		}
	}
	return -1
}

func appendIfMissing(values []int, value int) []int {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func decodePayload(raw []byte) (map[string]json.RawMessage, string, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, "", fmt.Errorf("must be valid JSON object: %w", err)
	}
	if fields == nil {
		return nil, "", errors.New("must be a JSON object")
	}
	kind, present, err := firstString(fields, "kind", "record_kind", "recordKind", "event_type", "eventType")
	if err != nil {
		return nil, "", fmt.Errorf("kind %s", err)
	}
	if !present {
		kind, present, err = firstString(fields, "type")
		if err != nil {
			return nil, "", fmt.Errorf("kind %s", err)
		}
	}
	if nested, ok := objectField(fields, "value", "event", "payload"); ok {
		for key, value := range nested {
			if _, exists := fields[key]; !exists {
				fields[key] = value
			}
		}
		if !present || isGenericKind(kind) {
			kind, _, err = firstString(nested, "kind", "record_kind", "recordKind", "event_type", "eventType", "type")
			if err != nil {
				return nil, "", fmt.Errorf("kind %s", err)
			}
		}
	}
	return fields, kind, nil
}

func isGenericKind(kind string) bool {
	normalized := strings.ToLower(strings.TrimSpace(kind))
	return normalized == "event" || normalized == "stream.event" || normalized == "stream_event"
}

func canonicalKind(value string, fields map[string]json.RawMessage) (RecordKind, bool) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	normalized = strings.NewReplacer("_", ".", "-", ".", "/", ".", " ", ".").Replace(normalized)
	switch normalized {
	case "turn.start", "turn.begin", "turn.started", "turn.open", "message.start", "message.begin", "message.started", "message.open":
		return RecordKindTurnStart, true
	case "turn.end", "turn.finish", "turn.finished", "turn.complete", "turn.completed":
		return RecordKindTurnEnd, true
	case "message.end", "message.finish", "message.finished":
		if hasField(fields, "reason", "terminal.reason", "terminal_reason", "terminalReason", "provenance", "terminal_provenance", "terminalProvenance") {
			return RecordKindTerminal, true
		}
		return RecordKindTurnEnd, true
	case "audio.frame", "audio", "audio.delta", "audio.chunk", "audio.packet", "audio.data", "delta.audio", "delta.audio.frame":
		return RecordKindAudioFrame, true
	case "transcript", "transcript.delta", "transcript.final", "transcript.text", "text", "text.delta", "text.final", "delta.text":
		return RecordKindTranscript, true
	case "tool.call", "tool.use", "toolcall", "toolcall.end", "tool.use.end":
		return RecordKindToolCall, true
	case "tool.result", "tool.response", "tool_result", "toolresult":
		return RecordKindToolResult, true
	case "interrupt", "interruption", "response.cancel", "response.canceled", "cancel":
		return RecordKindInterrupt, true
	case "terminal", "session.close", "session.closed", "loop.end", "error":
		return RecordKindTerminal, true
	case "audio.start", "audio.end", "text.start", "text.end", "toolcall.start", "toolcall.delta", "message.delta",
		"session.open", "session.created", "session.updated", "session.update", "usage.info", "pong", "ping",
		"vad.speech.started", "vad.speech.stopped", "image.start", "image.delta", "image.end", "video.start", "video.delta", "video.end":
		return RecordKindIgnored, true
	default:
		return "", false
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

func firstString(fields map[string]json.RawMessage, names ...string) (string, bool, error) {
	for _, name := range names {
		raw, ok := fields[name]
		if !ok {
			continue
		}
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return "", true, errors.New("must be a string")
		}
		return value, true, nil
	}
	return "", false, nil
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
	for _, name := range names {
		raw, ok := fields[name]
		if !ok {
			continue
		}
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return "", nil, errors.New("must be a string")
		}
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return "", nil, errors.New("is required")
		}
		return value, clone(raw), nil
	}
	return "", nil, errors.New("is required")
}

func semanticField(fields map[string]json.RawMessage, names ...string) []byte {
	for _, name := range names {
		if raw, ok := fields[name]; ok {
			return semanticBytes(raw)
		}
	}
	return nil
}

func semanticBytes(raw json.RawMessage) []byte {
	var value string
	if json.Unmarshal(raw, &value) == nil {
		return []byte(value)
	}
	return clone(raw)
}

func audioValue(fields map[string]json.RawMessage) ([]byte, error) {
	for _, name := range []string{"bytes", "audio", "content", "data", "payload"} {
		if raw, ok := fields[name]; ok {
			return decodeAudioBytes(raw)
		}
	}
	return nil, errors.New("is required")
}

func decodeAudioBytes(raw json.RawMessage) ([]byte, error) {
	var array []byte
	if len(raw) > 0 && raw[0] == '[' {
		if err := json.Unmarshal(raw, &array); err != nil {
			return nil, errors.New("must contain byte values")
		}
		return clone(array), nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, errors.New("must contain bytes or a base64 string")
	}
	if decoded, err := base64.StdEncoding.DecodeString(value); err == nil {
		return decoded, nil
	}
	return []byte(value), nil
}

func objectField(fields map[string]json.RawMessage, names ...string) (map[string]json.RawMessage, bool) {
	for _, name := range names {
		if raw, ok := fields[name]; ok {
			var nested map[string]json.RawMessage
			if json.Unmarshal(raw, &nested) == nil && nested != nil {
				return nested, true
			}
		}
	}
	return nil, false
}

func hasField(fields map[string]json.RawMessage, names ...string) bool {
	for _, name := range names {
		if _, ok := fields[name]; ok {
			return true
		}
	}
	return false
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
