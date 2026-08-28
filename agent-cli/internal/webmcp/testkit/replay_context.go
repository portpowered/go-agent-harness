package testkit

import (
	"bytes"
	"encoding/json"
)

// ReplayJSONSummary describes the shape of a JSON value without retaining any
// page-owned keys or values.
type ReplayJSONSummary struct {
	Present bool
	Kind    string
	Size    int
}

// ReplayOperationSummary is safe typed context for a replay operation. It
// records control-field presence and JSON shape, never the field contents.
type ReplayOperationSummary struct {
	Type            OperationType
	FrameIDPresent  bool
	ToolNamePresent bool
	Input           ReplayJSONSummary
	InvocationID    bool
	URL             bool
}

// ReplayEventSummary is safe typed context for a neutral event. It records
// control-field presence and JSON shape, never event payload contents.
type ReplayEventSummary struct {
	Type         EmittedEventType
	BrowserID    bool
	TargetID     bool
	Generation   bool
	MonotonicMS  bool
	ToolCount    int
	InvocationID bool
	Status       bool
	Output       ReplayJSONSummary
	Error        ReplayJSONSummary
}

func summarizeReplayJSON(raw json.RawMessage) ReplayJSONSummary {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return ReplayJSONSummary{}
	}
	value, err := decodeJSONNumberValue(trimmed)
	if err != nil {
		return ReplayJSONSummary{Present: true, Kind: "invalid"}
	}
	summary := ReplayJSONSummary{Present: true}
	switch typed := value.(type) {
	case nil:
		summary.Kind = "null"
	case bool:
		summary.Kind = "boolean"
	case string:
		summary.Kind = "string"
	case json.Number:
		summary.Kind = "number"
	case []any:
		summary.Kind = "array"
		summary.Size = len(typed)
	case map[string]any:
		summary.Kind = "object"
		summary.Size = len(typed)
	default:
		summary.Kind = "value"
	}
	return summary
}

func summarizeOperationExpectation(value OperationExpectation) ReplayOperationSummary {
	return ReplayOperationSummary{
		Type:            value.Type,
		FrameIDPresent:  value.FrameID != "",
		ToolNamePresent: value.ToolName != "",
		Input:           summarizeReplayJSON(value.Input),
		InvocationID:    value.InvocationID != "",
		URL:             value.URL != "",
	}
}

func summarizeOperationRequest(value OperationRequest) ReplayOperationSummary {
	return ReplayOperationSummary{
		Type:            value.Type,
		FrameIDPresent:  value.FrameID != "",
		ToolNamePresent: value.ToolName != "",
		Input:           summarizeReplayJSON(value.Input),
		InvocationID:    value.InvocationID != "",
		URL:             value.URL != "",
	}
}

func summarizeExpectedEvent(value EmittedEvent) ReplayEventSummary {
	return ReplayEventSummary{
		Type:         value.Type,
		ToolCount:    len(value.Tools),
		InvocationID: value.InvocationID != "",
		Status:       value.Status != "",
		Output:       summarizeReplayJSON(value.Output),
		Error:        summarizeReplayJSON(value.Error),
	}
}

func summarizeFixtureEvent(value FixtureEvent) ReplayEventSummary {
	return ReplayEventSummary{
		Type:         value.Type,
		BrowserID:    value.BrowserID != "",
		TargetID:     value.TargetID != "",
		Generation:   value.Generation != 0,
		MonotonicMS:  value.MonotonicMS != 0,
		ToolCount:    len(value.Tools),
		InvocationID: value.InvocationID != "",
		Status:       value.Status != "",
		Output:       summarizeReplayJSON(value.Output),
		Error:        summarizeReplayJSON(value.Error),
	}
}
