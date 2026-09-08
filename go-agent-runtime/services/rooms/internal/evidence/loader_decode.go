package evidence

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
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
	return &rooms.RoomReplayBundleError{Kind: rooms.RoomReplayBundleMismatch, Field: field, Err: fmt.Errorf("%w: %w", rooms.ErrInvalidReplayBundle, err)}
}

func incomplete(field string, err error) error {
	return &rooms.RoomReplayBundleError{Kind: rooms.RoomReplayBundleIncomplete, Field: field, Err: fmt.Errorf("%w: %w", rooms.ErrReplayBundleIncomplete, err)}
}
