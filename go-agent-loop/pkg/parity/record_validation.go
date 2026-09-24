package parity

import (
	"encoding/json"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
)

// recordFieldError locates one record validation failure below records[i].
type recordFieldError struct {
	field, reason string
}

func validateRecord(interfaceName string, index int, record transcript.Record) (parsedRecord, error) {
	fail := func(problem *recordFieldError) (parsedRecord, error) {
		return parsedRecord{}, newNormalizationError(interfaceName, fmt.Sprintf("records[%d].%s", index, problem.field), problem.reason)
	}
	if problem := validateRecordEnvelope(record); problem != nil {
		return fail(problem)
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
		return fail(&recordFieldError{field: "payload", reason: err.Error()})
	}
	if kind == "" {
		return fail(&recordFieldError{field: "kind", reason: "is required"})
	}
	if isTransportMechanicKind(kind) {
		return parsedRecord{record: record, kind: kindTransport, raw: raw}, nil
	}
	parsed := parsedRecord{record: record, raw: raw}
	if problem := parsed.parsePayloadFields(kind, fields); problem != nil {
		return fail(problem)
	}
	return parsed, nil
}

func validateRecordEnvelope(record transcript.Record) *recordFieldError {
	switch {
	case record.Version == 0:
		return &recordFieldError{field: "version", reason: "is missing format version"}
	case record.Version != transcript.FormatVersion:
		return &recordFieldError{field: "version", reason: fmt.Sprintf("unsupported format version %d", record.Version)}
	case record.Peer != transcript.PeerClient && record.Peer != transcript.PeerAgent:
		return &recordFieldError{field: "peer", reason: fmt.Sprintf("unknown peer %q", record.Peer)}
	case record.Direction != transcript.DirectionIn && record.Direction != transcript.DirectionOut:
		return &recordFieldError{field: "direction", reason: fmt.Sprintf("unknown direction %q", record.Direction)}
	case !knownStream(record.Stream):
		return &recordFieldError{field: "stream", reason: fmt.Sprintf("unknown stream %q", record.Stream)}
	case len(record.Payload) == 0:
		return &recordFieldError{field: "payload", reason: "is required"}
	}
	return nil
}

type payloadFields = map[string]json.RawMessage

// parsePayloadFields decodes the semantic payload members for one record kind.
// Members are checked in declaration order and the first failure is reported.
func (parsed *parsedRecord) parsePayloadFields(kind string, fields payloadFields) *recordFieldError {
	switch kind {
	case string(kindTurnStart), string(kindTurnEnd):
		parsed.kind = recordKind(kind)
		return firstFieldError(
			assignString(&parsed.id, fields, optionalString, "id"),
			assignString(&parsed.name, fields, optionalString, "role"))
	case string(kindAudioFrame):
		parsed.kind = kindAudioFrame
		var err error
		if parsed.audioBytes, err = audioValue(fields); err != nil {
			return &recordFieldError{field: "payload.bytes", reason: err.Error()}
		}
	case string(kindTranscript):
		parsed.kind = kindTranscript
		var err error
		if parsed.text, _, err = requiredStringValue(fields, "text", "value"); err != nil {
			return &recordFieldError{field: "payload.text", reason: err.Error()}
		}
	case string(kindToolCall):
		parsed.kind = kindToolCall
		parsed.arguments = optionalRawValue(fields, "arguments")
		return firstFieldError(
			assignString(&parsed.id, fields, requiredString, "id"),
			assignString(&parsed.name, fields, optionalString, "name"))
	case string(kindToolResult):
		parsed.kind = kindToolResult
		parsed.semantic = optionalRawValue(fields, "result", "value")
		if problem := assignString(&parsed.id, fields, requiredString, "id"); problem != nil {
			return problem
		}
		if parsed.semantic == nil {
			return &recordFieldError{field: "payload.result", reason: "is required"}
		}
	case string(kindInterrupt), string(kindTerminal):
		parsed.kind = recordKind(kind)
		provenance := optionalString
		if kind == string(kindTerminal) {
			provenance = requiredString
		}
		return firstFieldError(
			assignString(&parsed.reason, fields, requiredString, "reason"),
			assignString(&parsed.provenance, fields, provenance, "provenance"))
	default:
		return &recordFieldError{field: "kind", reason: fmt.Sprintf("unknown record kind %q (unsupported)", kind)}
	}
	return nil
}

// assignString stores one string payload member and reports a failure at
// payload.<name>.
func assignString(target *string, fields payloadFields, read func(payloadFields, ...string) (string, error), name string) *recordFieldError {
	value, err := read(fields, name)
	if err != nil {
		return &recordFieldError{field: "payload." + name, reason: err.Error()}
	}
	*target = value
	return nil
}

func firstFieldError(problems ...*recordFieldError) *recordFieldError {
	for _, problem := range problems {
		if problem != nil {
			return problem
		}
	}
	return nil
}
