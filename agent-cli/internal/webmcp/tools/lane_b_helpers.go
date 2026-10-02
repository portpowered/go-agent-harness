package tools

import (
	"encoding/json"
	"strings"
)

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}

func laneBCloneMap(value map[string]any) map[string]any {
	if value == nil {
		return nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return map[string]any{}
	}
	var result map[string]any
	if err := json.Unmarshal(encoded, &result); err != nil {
		return map[string]any{}
	}
	return result
}

func laneBPointerPath(name string) string {
	return "/" + strings.NewReplacer("~", "~0", "/", "~1").Replace(name)
}

func laneBStringValue(values map[string]any, name string) string {
	if values == nil {
		return ""
	}
	value := optionalAs[string](values[name])
	return value
}

func laneBBoolValue(values map[string]any, name string) bool {
	value := optionalAs[bool](values[name])
	return value
}

func laneBBoolValueDefault(values map[string]any, name string, fallback bool) bool {
	if values == nil {
		return fallback
	}
	value, ok := values[name].(bool)
	if !ok {
		return fallback
	}
	return value
}

// optionalAs returns value as T, or T's zero value when value is absent or has
// another shape. Schemas and arguments are decoded JSON, so an optional field
// of the wrong shape is treated exactly like a missing one.
func optionalAs[T any](value any) T {
	if typed, ok := value.(T); ok {
		return typed
	}
	var zero T
	return zero
}

// decodeBestEffort decodes an advisory schema fragment into target. A fragment
// of an unexpected shape keeps whatever fields did decode; callers fall back
// to defaults for the rest.
func decodeBestEffort(raw []byte, target any) {
	if err := json.Unmarshal(raw, target); err != nil {
		return
	}
}

func nonNegativeInt(value any) int {
	switch typed := value.(type) {
	case int:
		if typed >= 0 {
			return typed
		}
	case int64:
		if typed >= 0 && typed <= int64(^uint(0)>>1) {
			return int(typed)
		}
	case float64:
		if typed >= 0 && typed <= float64(^uint(0)>>1) {
			return int(typed)
		}
	}
	return 0
}

func nonNegativeUint(value any) uint64 {
	switch typed := value.(type) {
	case uint64:
		return typed
	case int:
		if typed >= 0 {
			return uint64(typed)
		}
	case float64:
		if typed >= 0 {
			return uint64(typed)
		}
	}
	return 0
}
