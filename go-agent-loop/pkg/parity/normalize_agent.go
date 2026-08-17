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

const defaultAgentInterface = "agent"

// NormalizeAgent reduces JSONL bytes or decoded records to the shared parity
// projection. JSON input is decoded with presence-aware raw fields before it
// is converted to the shared transcript contract.
//
// The agent boundary describes direction from the provider's viewpoint. An
// inbound agent observation is therefore mapped to the shared outbound
// orientation, and an outbound observation is mapped to shared inbound. The
// common reducer then owns all semantic projection rules and all-or-nothing
// validation.
func NormalizeAgent(interfaceName string, input any) (Projection, error) {
	if strings.TrimSpace(interfaceName) == "" {
		interfaceName = defaultAgentInterface
	}

	records, err := decodeAgentInput(interfaceName, input)
	if err != nil {
		return Projection{}, err
	}
	if len(records) == 0 {
		return Projection{}, newNormalizationError(interfaceName, "records", "is required")
	}
	return Normalize(interfaceName, records)
}

// NormalizeAgentTranscript is a convenience alias for NormalizeAgent.
func NormalizeAgentTranscript(interfaceName string, input any) (Projection, error) {
	return NormalizeAgent(interfaceName, input)
}

// NormalizeAgentJSONL is the typed JSONL convenience form of NormalizeAgent.
func NormalizeAgentJSONL(interfaceName string, data []byte) (Projection, error) {
	return NormalizeAgent(interfaceName, data)
}

// NormalizeAgentRecords is the typed form for callers that already decoded
// records through the transcript package. Raw JSONL callers should prefer
// NormalizeAgent so missing fields cannot be confused with zero values.
func NormalizeAgentRecords(interfaceName string, records []transcript.Record) (Projection, error) {
	return NormalizeAgent(interfaceName, records)
}

func decodeAgentInput(interfaceName string, input any) ([]transcript.Record, error) {
	switch value := input.(type) {
	case []byte:
		return decodeAgentJSON(interfaceName, value)
	case transcript.Record:
		return validateAgentRecords(interfaceName, []transcript.Record{value})
	case []transcript.Record:
		return validateAgentRecords(interfaceName, value)
	case nil:
		return nil, newNormalizationError(interfaceName, "records", "is required")
	default:
		return nil, newNormalizationError(interfaceName, "input", fmt.Sprintf("unsupported transcript input %T", input))
	}
}

func decodeAgentJSON(interfaceName string, data []byte) ([]transcript.Record, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, newNormalizationError(interfaceName, "records", "is required")
	}

	lines := bytes.Split(trimmed, []byte{'\n'})
	records := make([]transcript.Record, 0, len(lines))
	for index, line := range lines {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			return nil, newNormalizationError(interfaceName, fmt.Sprintf("records[%d]", index), "must not contain an empty JSONL record")
		}
		record, err := decodeAgentRecord(interfaceName, index, line)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, nil
}

func decodeAgentRecord(interfaceName string, index int, data []byte) (transcript.Record, error) {
	field := func(name string) string { return fmt.Sprintf("records[%d].%s", index, name) }
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return transcript.Record{}, newNormalizationError(interfaceName, field("record"), fmt.Sprintf("must be a JSON object: %v", err))
	}
	if fields == nil {
		return transcript.Record{}, newNormalizationError(interfaceName, field("record"), "must be a JSON object")
	}

	version, err := requiredAgentInt(fields, "v")
	if err != nil {
		return transcript.Record{}, newNormalizationError(interfaceName, field("version"), err.Error())
	}
	if version == 0 {
		return transcript.Record{}, newNormalizationError(interfaceName, field("version"), "is missing format version")
	}
	if version != transcript.FormatVersion {
		return transcript.Record{}, newNormalizationError(interfaceName, field("version"), fmt.Sprintf("unsupported format version %d", version))
	}

	tick, err := requiredAgentUint(fields, "tick")
	if err != nil {
		return transcript.Record{}, newNormalizationError(interfaceName, field("tick"), err.Error())
	}
	timestamp, err := requiredAgentString(fields, "t")
	if err != nil {
		return transcript.Record{}, newNormalizationError(interfaceName, field("timestamp"), err.Error())
	}
	peer, err := requiredAgentString(fields, "peer")
	if err != nil {
		return transcript.Record{}, newNormalizationError(interfaceName, field("peer"), err.Error())
	}
	if transcript.Peer(peer) != transcript.PeerAgent {
		return transcript.Record{}, newNormalizationError(interfaceName, field("peer"), fmt.Sprintf("must be %q for an agent transcript", transcript.PeerAgent))
	}
	direction, err := requiredAgentString(fields, "dir")
	if err != nil {
		return transcript.Record{}, newNormalizationError(interfaceName, field("direction"), err.Error())
	}
	semanticDirection, err := agentSemanticDirection(transcript.Direction(direction))
	if err != nil {
		return transcript.Record{}, newNormalizationError(interfaceName, field("direction"), err.Error())
	}
	streamValue, err := requiredAgentString(fields, "stream")
	if err != nil {
		return transcript.Record{}, newNormalizationError(interfaceName, field("stream"), err.Error())
	}
	stream := transcript.Stream(streamValue)
	if !knownStream(stream) {
		return transcript.Record{}, newNormalizationError(interfaceName, field("stream"), fmt.Sprintf("unknown stream %q", stream))
	}
	payloadText, err := requiredAgentString(fields, "payload")
	if err != nil {
		return transcript.Record{}, newNormalizationError(interfaceName, field("payload"), err.Error())
	}
	payload, err := base64.StdEncoding.DecodeString(payloadText)
	if err != nil {
		return transcript.Record{}, newNormalizationError(interfaceName, field("payload"), "must be valid base64")
	}
	if len(payload) == 0 {
		return transcript.Record{}, newNormalizationError(interfaceName, field("payload"), "must not be empty")
	}

	return transcript.Record{
		Version:   version,
		Tick:      tick,
		Timestamp: timestamp, // validated but intentionally excluded from Projection
		Peer:      transcript.PeerAgent,
		Direction: semanticDirection,
		Stream:    stream,
		Payload:   clone(payload),
	}, nil
}

func validateAgentRecords(interfaceName string, source []transcript.Record) ([]transcript.Record, error) {
	records := make([]transcript.Record, 0, len(source))
	for index, sourceRecord := range source {
		field := func(name string) string { return fmt.Sprintf("records[%d].%s", index, name) }
		if sourceRecord.Version == 0 {
			return nil, newNormalizationError(interfaceName, field("version"), "is missing format version")
		}
		if sourceRecord.Version != transcript.FormatVersion {
			return nil, newNormalizationError(interfaceName, field("version"), fmt.Sprintf("unsupported format version %d", sourceRecord.Version))
		}
		if sourceRecord.Peer != transcript.PeerAgent {
			return nil, newNormalizationError(interfaceName, field("peer"), fmt.Sprintf("must be %q for an agent transcript", transcript.PeerAgent))
		}
		semanticDirection, err := agentSemanticDirection(sourceRecord.Direction)
		if err != nil {
			return nil, newNormalizationError(interfaceName, field("direction"), err.Error())
		}
		if !knownStream(sourceRecord.Stream) {
			return nil, newNormalizationError(interfaceName, field("stream"), fmt.Sprintf("unknown stream %q", sourceRecord.Stream))
		}
		if len(sourceRecord.Payload) == 0 {
			return nil, newNormalizationError(interfaceName, field("payload"), "must not be empty")
		}
		record := sourceRecord
		record.Direction = semanticDirection
		record.Payload = clone(sourceRecord.Payload)
		records = append(records, record)
	}
	return records, nil
}

func agentSemanticDirection(direction transcript.Direction) (transcript.Direction, error) {
	switch direction {
	case transcript.DirectionIn:
		return transcript.DirectionOut, nil
	case transcript.DirectionOut:
		return transcript.DirectionIn, nil
	default:
		return "", fmt.Errorf("unknown direction %q", direction)
	}
}

func requiredAgentRaw(fields map[string]json.RawMessage, name string) (json.RawMessage, error) {
	raw, ok := fields[name]
	if !ok {
		return nil, errors.New("is required")
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, errors.New("must not be null")
	}
	return raw, nil
}

func requiredAgentString(fields map[string]json.RawMessage, name string) (string, error) {
	raw, err := requiredAgentRaw(fields, name)
	if err != nil {
		return "", err
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", errors.New("must be a string")
	}
	return value, nil
}

func requiredAgentInt(fields map[string]json.RawMessage, name string) (int, error) {
	raw, err := requiredAgentRaw(fields, name)
	if err != nil {
		return 0, err
	}
	var value int
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, errors.New("must be an integer")
	}
	return value, nil
}

func requiredAgentUint(fields map[string]json.RawMessage, name string) (uint64, error) {
	raw, err := requiredAgentRaw(fields, name)
	if err != nil {
		return 0, err
	}
	var value uint64
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, errors.New("must be a non-negative integer")
	}
	return value, nil
}
