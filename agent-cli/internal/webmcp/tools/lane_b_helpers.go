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

func firstToolSetOptions(options []ToolSetOptions) ToolSetOptions {
	if len(options) == 0 {
		return ToolSetOptions{}
	}
	return options[0]
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
	value, _ := values[name].(string)
	return value
}

func laneBBoolValue(values map[string]any, name string) bool {
	value, _ := values[name].(bool)
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
