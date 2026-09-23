package support

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomreplay"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/gateway"
)

type JSONObject map[string]json.RawMessage

func Object(raw json.RawMessage) (JSONObject, error) {
	var object JSONObject
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, err
	}
	if object == nil {
		return nil, errors.New("expected JSON object")
	}
	return object, nil
}

func FirstString(primary, fallback JSONObject, names ...string) (string, bool, error) {
	for _, object := range []JSONObject{primary, fallback} {
		if object == nil {
			continue
		}
		for _, name := range names {
			if raw, present := object[name]; present {
				value, ok := String(raw)
				if !ok {
					return "", true, errors.New("expected string")
				}
				return strings.TrimSpace(value), true, nil
			}
		}
	}
	return "", false, errors.New("missing string")
}

func String(raw json.RawMessage) (string, bool) {
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return "", false
	}
	return value, true
}

func ErrOrDefault(err, fallback error) error {
	if err != nil {
		return err
	}
	return fallback
}

func Join(errs ...error) error { return errors.Join(errs...) }

func BundleError(kind roomreplay.RoomReplayBundleErrorKind, field, artifact, expected, actual string, cause error) error {
	if kind == "" {
		kind = roomreplay.RoomReplayBundleMismatch
	}
	var replayCause error
	if kind == roomreplay.RoomReplayBundleIncomplete {
		replayCause = gateway.NewReplayIncompleteError(expected, actual, cause)
	} else {
		replayCause = gateway.NewReplayMismatchError(expected, actual, cause)
	}
	return &roomreplay.RoomReplayBundleError{
		Kind: kind, Field: field, Artifact: artifact, Expected: expected, Actual: actual, Err: replayCause,
	}
}
