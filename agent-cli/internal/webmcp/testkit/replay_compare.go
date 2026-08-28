package testkit

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
)

func compareReplayOperation(expected OperationExpectation, actual OperationRequest) (string, string) {
	if expected.Type != actual.Type {
		return "type", "operation type differs"
	}
	switch expected.Type {
	case OperationInvokeTool:
		if expected.FrameID != actual.FrameID {
			return "frame_id", "frame ID differs"
		}
		if expected.ToolName != actual.ToolName {
			return "tool_name", "tool name differs"
		}
		actualInput := actual.Input
		if len(actualInput) == 0 {
			actualInput = json.RawMessage(`{}`)
		}
		if difference := replayJSONDifferenceAt(expected.Input, actualInput, "input"); difference != "" {
			return difference, "JSON values differ"
		}
	case OperationCancelTool:
		if expected.InvocationID != actual.InvocationID {
			return "invocation_id", "invocation ID differs"
		}
	case OperationNavigate:
		if expected.URL != actual.URL {
			return "url", "URL differs"
		}
	}
	return "", ""
}

func compareReplayEvent(expected EmittedEvent, actual FixtureEvent, replay *BrowserReplay) (string, string) {
	if expected.Type != actual.Type {
		return "type", "event type differs"
	}
	if actual.BrowserID != "" && actual.BrowserID != replay.browserID {
		return "browser_id", "browser ID differs"
	}
	if actual.TargetID != "" && actual.TargetID != replay.targetID {
		return "target_id", "target ID differs"
	}
	if actual.Generation != 0 && actual.Generation != replay.generation {
		return "generation", "generation differs"
	}
	if actual.MonotonicMS != 0 && actual.MonotonicMS != replay.lastClock {
		return "monotonic_ms", "monotonic time differs"
	}
	switch expected.Type {
	case EmittedToolsAdded:
		if path, difference := compareReplayTools(expected.Tools, actual.Tools); difference != "" {
			return path, difference
		}
	case EmittedToolResponded:
		if expected.InvocationID != actual.InvocationID {
			return "invocation_id", "invocation ID differs"
		}
		if expected.Status != actual.Status {
			return "status", "terminal status differs"
		}
		if difference := replayJSONDifferenceAt(expected.Output, actual.Output, "output"); difference != "" {
			return difference, "terminal output differs"
		}
		if difference := replayJSONDifferenceAt(expected.Error, actual.Error, "error"); difference != "" {
			return difference, "terminal error differs"
		}
	}
	return "", ""
}

func compareReplayTools(expected, actual []ToolDescriptor) (string, string) {
	if len(expected) != len(actual) {
		return "tools", "tool catalog length differs"
	}
	for index := range expected {
		left, right := expected[index], actual[index]
		prefix := fmt.Sprintf("tools[%d]", index)
		if left.Name != right.Name {
			return prefix + ".name", "tool name differs"
		}
		if left.Description != right.Description {
			return prefix + ".description", "tool description differs"
		}
		if left.FrameID != right.FrameID {
			return prefix + ".frame_id", "tool frame ID differs"
		}
		if difference := replayJSONDifferenceAt(left.InputSchema, right.InputSchema, prefix+".input_schema"); difference != "" {
			return difference, "tool input schema differs"
		}
		if difference := replayJSONDifferenceAt(left.Annotations, right.Annotations, prefix+".annotations"); difference != "" {
			return difference, "tool annotations differ"
		}
	}
	return "", ""
}

func replayJSONDifference(left, right json.RawMessage) string {
	return replayJSONDifferenceAt(left, right, "value")
}

func replayJSONDifferenceAt(left, right json.RawMessage, path string) string {
	if len(left) == 0 && len(right) == 0 {
		return ""
	}
	if len(left) == 0 || len(right) == 0 {
		return path
	}
	leftValue, err := decodeJSONNumberValue(left)
	if err != nil {
		return path
	}
	rightValue, err := decodeJSONNumberValue(right)
	if err != nil {
		return path
	}
	return replayValueDifference(leftValue, rightValue, path)
}

func replayValueDifference(left, right any, path string) string {
	switch leftValue := left.(type) {
	case map[string]any:
		rightValue, ok := right.(map[string]any)
		if !ok {
			return path
		}
		keys := make([]string, 0, len(leftValue)+len(rightValue))
		seen := make(map[string]struct{}, len(leftValue)+len(rightValue))
		for key := range leftValue {
			seen[key] = struct{}{}
			keys = append(keys, key)
		}
		for key := range rightValue {
			if _, ok := seen[key]; !ok {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		for _, key := range keys {
			leftChild, leftOK := leftValue[key]
			rightChild, rightOK := rightValue[key]
			childPath := replayJSONFieldPath(path, key)
			if !leftOK || !rightOK {
				return childPath
			}
			if difference := replayValueDifference(leftChild, rightChild, childPath); difference != "" {
				return difference
			}
		}
		return ""
	case []any:
		rightValue, ok := right.([]any)
		if !ok || len(leftValue) != len(rightValue) {
			return path
		}
		for index := range leftValue {
			childPath := fmt.Sprintf("%s[%d]", path, index)
			if difference := replayValueDifference(leftValue[index], rightValue[index], childPath); difference != "" {
				return difference
			}
		}
		return ""
	case json.Number:
		rightValue, ok := right.(json.Number)
		if !ok || leftValue.String() != rightValue.String() {
			return path
		}
		return ""
	default:
		if !reflect.DeepEqual(left, right) {
			return path
		}
		return ""
	}
}

func replayJSONFieldPath(base, key string) string {
	if key == "" {
		return base + `[""]`
	}
	for index, char := range key {
		if !(char == '_' || char == '-' || char == '.' || char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || index > 0 && char >= '0' && char <= '9') {
			return base + "[" + strconv.Quote(key) + "]"
		}
	}
	return base + "." + key
}
