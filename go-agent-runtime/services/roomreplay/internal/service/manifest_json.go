package service

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
)

type roomReplayJSONObject map[string]json.RawMessage

func errOrDefault(err, fallback error) error {
	if err != nil {
		return err
	}
	return fallback
}

func mustMarshal(value any) json.RawMessage {
	data, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	return data
}

func roomReplayObject(raw json.RawMessage) (roomReplayJSONObject, error) {
	var object roomReplayJSONObject
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, err
	}
	if object == nil {
		return nil, errors.New("expected JSON object")
	}
	return object, nil
}

func roomReplayRawField(object roomReplayJSONObject, names ...string) (json.RawMessage, bool) {
	for _, name := range names {
		if raw, ok := object[name]; ok {
			return raw, true
		}
	}
	return nil, false
}

func roomReplayStringField(object roomReplayJSONObject, names ...string) (string, bool, error) {
	raw, ok := roomReplayRawField(object, names...)
	if !ok {
		return "", false, nil
	}
	value, ok := decodeRoomReplayString(raw)
	if !ok {
		return "", true, errors.New("expected string")
	}
	return strings.TrimSpace(value), true, nil
}

func roomReplayBoolField(object roomReplayJSONObject, name string) (bool, bool, error) {
	raw, ok := roomReplayRawField(object, name)
	if !ok {
		return false, false, nil
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, true, err
	}
	return value, true, nil
}

func roomReplayIntField(object roomReplayJSONObject, name string) (int, bool, error) {
	raw, ok := roomReplayRawField(object, name)
	if !ok {
		return 0, false, nil
	}
	var number json.Number
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&number); err != nil {
		return 0, true, err
	}
	value, err := strconv.Atoi(number.String())
	if err != nil {
		return 0, true, err
	}
	return value, true, nil
}

func firstRoomReplayIntField(primary, fallback roomReplayJSONObject, names ...string) (int, bool, error) {
	for _, object := range []roomReplayJSONObject{primary, fallback} {
		if object == nil {
			continue
		}
		for _, name := range names {
			if value, present, err := roomReplayIntField(object, name); present {
				return value, true, err
			}
		}
	}
	return 0, false, errors.New("missing integer")
}

func firstRoomReplayStringField(primary, fallback roomReplayJSONObject, names ...string) (string, bool, error) {
	for _, object := range []roomReplayJSONObject{primary, fallback} {
		if object == nil {
			continue
		}
		for _, name := range names {
			if value, present, err := roomReplayStringField(object, name); present {
				return value, true, err
			}
		}
	}
	return "", false, errors.New("missing string")
}

func roomReplayTimeField(object roomReplayJSONObject, name string) (time.Time, bool, error) {
	raw, ok := roomReplayRawField(object, name)
	if !ok {
		return time.Time{}, false, nil
	}
	value, ok := decodeRoomReplayString(raw)
	if !ok {
		return time.Time{}, true, errors.New("expected RFC3339 string")
	}
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil {
		return time.Time{}, true, err
	}
	return parsed.UTC(), true, nil
}

func decodeRoomReplayString(raw json.RawMessage) (string, bool) {
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return "", false
	}
	return value, true
}

func int64Pointer(value int) *int64 {
	converted := int64(value)
	return &converted
}

func normalizeRoomReplayDigest(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	return strings.TrimPrefix(value, "sha256:")
}
