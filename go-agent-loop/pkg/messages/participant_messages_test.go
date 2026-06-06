package messages

import (
	"testing"
)

func intPtr(v int) *int             { return &v }
func float64Ptr(v float64) *float64 { return &v }

func TestNewInferenceRequest_WithDefaults(t *testing.T) {
	defaults := &InferenceDefaults{
		MaxTokens:     intPtr(512),
		Temperature:   float64Ptr(0.3),
		StopSequences: []string{"STOP"},
	}
	req := NewInferenceRequest(nil, nil, 5, defaults)
	if req.LoopPassID != 5 {
		t.Errorf("LoopPassID: got %d, want 5", req.LoopPassID)
	}
	if req.MaxTokens == nil || *req.MaxTokens != 512 {
		t.Errorf("MaxTokens: got %v, want 512", req.MaxTokens)
	}
	if req.Temperature == nil || *req.Temperature != 0.3 {
		t.Errorf("Temperature: got %v, want 0.3", req.Temperature)
	}
	if len(req.StopSequences) != 1 || req.StopSequences[0] != "STOP" {
		t.Errorf("StopSequences: got %v, want [STOP]", req.StopSequences)
	}
}

func TestNewInferenceRequest_NilDefaults(t *testing.T) {
	req := NewInferenceRequest(nil, nil, 1, nil)
	if req.MaxTokens != nil {
		t.Errorf("MaxTokens should be nil, got %v", *req.MaxTokens)
	}
	if req.Temperature != nil {
		t.Errorf("Temperature should be nil, got %v", *req.Temperature)
	}
	if req.StopSequences != nil {
		t.Errorf("StopSequences should be nil, got %v", req.StopSequences)
	}
}

func TestNewInferenceRequest_PartialDefaults(t *testing.T) {
	defaults := &InferenceDefaults{
		MaxTokens: intPtr(1024),
		// Temperature and StopSequences left nil.
	}
	req := NewInferenceRequest(nil, nil, 0, defaults)
	if req.MaxTokens == nil || *req.MaxTokens != 1024 {
		t.Errorf("MaxTokens: got %v, want 1024", req.MaxTokens)
	}
	if req.Temperature != nil {
		t.Errorf("Temperature should be nil, got %v", *req.Temperature)
	}
	if req.StopSequences != nil {
		t.Errorf("StopSequences should be nil, got %v", req.StopSequences)
	}
}

func TestNewInferenceRequest_PreservesMessagesAndTools(t *testing.T) {
	msgs := []Message{NewTextMessage(RoleUser, "hello")}
	tools := []ToolDefinition{{Name: "test_tool"}}
	defaults := &InferenceDefaults{MaxTokens: intPtr(100)}

	req := NewInferenceRequest(msgs, tools, 3, defaults)
	if len(req.Messages) != 1 || req.Messages[0].TextContent() != "hello" {
		t.Errorf("Messages not preserved: got %v", req.Messages)
	}
	if len(req.Tools) != 1 || req.Tools[0].Name != "test_tool" {
		t.Errorf("Tools not preserved: got %v", req.Tools)
	}
	if req.LoopPassID != 3 {
		t.Errorf("LoopPassID: got %d, want 3", req.LoopPassID)
	}
}
