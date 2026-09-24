package admission

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomevidence"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/gateway"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
)

// object is the deliberately narrow JSON shape used while admitting a room
// bundle. Keeping decoding helpers here leaves loader.go focused on the
// ordered admission pipeline rather than field conversion details.
type object map[string]json.RawMessage

func decodeObject(data []byte) (object, error) {
	var value object
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, err
	}
	if value == nil {
		return nil, fmt.Errorf("expected JSON object")
	}
	return value, nil
}

func decodeObjectField(value object, name string) (object, error) {
	raw, ok := value[name]
	if !ok {
		return nil, fmt.Errorf("field is missing")
	}
	return decodeObject(raw)
}

func stringValue(value object, name string) string {
	var result string
	if raw, ok := value[name]; ok {
		if err := json.Unmarshal(raw, &result); err != nil {
			return ""
		}
	}
	return result
}

func boolean(value object, name string) (bool, bool) {
	var result bool
	raw, ok := value[name]
	if !ok || json.Unmarshal(raw, &result) != nil {
		return false, false
	}
	return result, true
}

func integer(value object, name string) (int, bool) {
	var result int
	raw, ok := value[name]
	if !ok || json.Unmarshal(raw, &result) != nil {
		return 0, false
	}
	return result, true
}

func integer64(value object, name string) (int64, bool) {
	var result int64
	raw, ok := value[name]
	if !ok || json.Unmarshal(raw, &result) != nil {
		return 0, false
	}
	return result, true
}

func timestamp(value object, name string) (time.Time, error) {
	text := strings.TrimSpace(stringValue(value, name))
	if text == "" {
		return time.Time{}, fmt.Errorf("timestamp is missing")
	}
	result, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		return time.Time{}, err
	}
	return result, nil
}

func mismatch(field string, err error) error {
	cause := fmt.Errorf("%w: %w", rooms.ErrInvalidReplayBundle, err)
	return &roomevidence.BundleError{Kind: roomevidence.BundleMismatch, Field: field, Err: &classifiedAdmissionCause{
		cause:           cause,
		classifications: []error{roomevidence.ErrInvalidRoomReplayBundle, gateway.ErrReplayMismatch, providers.ErrReplayMismatch},
	}}
}

func incomplete(field string, err error) error {
	cause := fmt.Errorf("%w: %w", rooms.ErrReplayBundleIncomplete, err)
	return &roomevidence.BundleError{Kind: roomevidence.BundleIncomplete, Field: field, Err: &classifiedAdmissionCause{
		cause:           cause,
		classifications: []error{roomevidence.ErrRoomReplayBundleIncomplete, gateway.ErrReplayIncomplete, providers.ErrReplayIncomplete},
	}}
}

type classifiedAdmissionCause struct {
	cause           error
	classifications []error
}

func (e *classifiedAdmissionCause) Error() string {
	if e == nil || e.cause == nil {
		return "<nil>"
	}
	return e.cause.Error()
}

func (e *classifiedAdmissionCause) Unwrap() []error {
	if e == nil {
		return nil
	}
	errorsToUnwrap := make([]error, 0, len(e.classifications)+1)
	if e.cause != nil {
		errorsToUnwrap = append(errorsToUnwrap, e.cause)
	}
	return append(errorsToUnwrap, e.classifications...)
}

func normalizeParticipantKind(kind rooms.ParticipantKind) rooms.ParticipantKind {
	switch value := rooms.ParticipantKind(strings.ToLower(strings.TrimSpace(string(kind)))); value {
	case "", rooms.ParticipantKindAgent:
		return rooms.ParticipantKindAgent
	case rooms.ParticipantKindHuman, rooms.ParticipantKindCustomer:
		return rooms.ParticipantKindHuman
	default:
		return value
	}
}
