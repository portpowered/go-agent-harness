package openai

import (
	"encoding/json"
	"testing"

	"github.com/portpowered/go-llm-gateway/pkg/providers"
)

func TestApplyInferenceRequestOptions_Defaults(t *testing.T) {
	req := chatRequest{Model: "gpt-4o"}
	applyInferenceRequestOptions(&req, providers.InferenceRequest{})

	if req.MaxTokens != nil {
		t.Error("MaxTokens should be nil when not set")
	}
	if req.Temperature != nil {
		t.Error("Temperature should be nil when not set")
	}
	if req.Stop != nil {
		t.Error("Stop should be nil when not set")
	}
}

func TestApplyInferenceRequestOptions_MaxTokens(t *testing.T) {
	req := chatRequest{Model: "gpt-4o"}
	max := 1024
	applyInferenceRequestOptions(&req, providers.InferenceRequest{MaxTokens: &max})

	if req.MaxTokens == nil || *req.MaxTokens != 1024 {
		t.Errorf("MaxTokens: got %v, want 1024", req.MaxTokens)
	}
}

func TestApplyInferenceRequestOptions_Temperature(t *testing.T) {
	req := chatRequest{Model: "gpt-4o"}
	temp := 0.5
	applyInferenceRequestOptions(&req, providers.InferenceRequest{Temperature: &temp})

	if req.Temperature == nil || *req.Temperature != 0.5 {
		t.Errorf("Temperature: got %v, want 0.5", req.Temperature)
	}
}

func TestApplyInferenceRequestOptions_StopSequences_Single(t *testing.T) {
	req := chatRequest{Model: "gpt-4o"}
	applyInferenceRequestOptions(&req, providers.InferenceRequest{
		StopSequences: []string{"\n"},
	})

	stop, ok := req.Stop.(string)
	if !ok || stop != "\n" {
		t.Errorf("Stop should be string %q, got %T %v", "\n", req.Stop, req.Stop)
	}
}

func TestApplyInferenceRequestOptions_StopSequences_Multiple(t *testing.T) {
	req := chatRequest{Model: "gpt-4o"}
	applyInferenceRequestOptions(&req, providers.InferenceRequest{
		StopSequences: []string{"\n", "END", "---"},
	})

	stops, ok := req.Stop.([]string)
	if !ok {
		t.Fatalf("Stop should be []string, got %T", req.Stop)
	}
	if len(stops) != 3 {
		t.Fatalf("OfStringArray: got len=%d, want 3", len(stops))
	}
	if stops[0] != "\n" || stops[1] != "END" || stops[2] != "---" {
		t.Errorf("Stop: got %v", stops)
	}
}

func TestApplyInferenceRequestOptions_FrequencyPenalty(t *testing.T) {
	req := chatRequest{Model: "gpt-4o"}
	penalty := 1.5
	applyInferenceRequestOptions(&req, providers.InferenceRequest{FrequencyPenalty: &penalty})

	if req.FrequencyPenalty == nil || *req.FrequencyPenalty != 1.5 {
		t.Errorf("FrequencyPenalty: got %v, want 1.5", req.FrequencyPenalty)
	}
}

func TestApplyInferenceRequestOptions_FrequencyPenaltyNil(t *testing.T) {
	req := chatRequest{Model: "gpt-4o"}
	applyInferenceRequestOptions(&req, providers.InferenceRequest{})

	if req.FrequencyPenalty != nil {
		t.Errorf("FrequencyPenalty should be nil when not set, got %v", req.FrequencyPenalty)
	}
}

func TestFrequencyPenaltyAppearsInJSON(t *testing.T) {
	penalty := 1.5
	req := chatRequest{
		Model:            "gpt-4o",
		Messages:         []requestMsg{{Role: "user", Content: strContent("hello")}},
		FrequencyPenalty: &penalty,
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	fp, ok := raw["frequency_penalty"]
	if !ok {
		t.Fatal("frequency_penalty missing from JSON payload")
	}
	if fp.(float64) != 1.5 {
		t.Errorf("frequency_penalty: got %v, want 1.5", fp)
	}
}

func TestFrequencyPenaltyOmittedFromJSONWhenNil(t *testing.T) {
	req := chatRequest{
		Model:    "gpt-4o",
		Messages: []requestMsg{{Role: "user", Content: strContent("hello")}},
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if _, ok := raw["frequency_penalty"]; ok {
		t.Error("frequency_penalty should be omitted from JSON when nil")
	}
}

func TestApplyInferenceRequestOptions_ThinkingIgnored(t *testing.T) {
	// OpenAI provider ignores Thinking; other options should still apply.
	req := chatRequest{Model: "gpt-4o"}
	max := 512
	applyInferenceRequestOptions(&req, providers.InferenceRequest{
		MaxTokens: &max,
		Thinking:  &providers.ThinkingConfig{Mode: providers.ThinkingEnabled, BudgetTokens: 4096},
	})

	if req.MaxTokens == nil || *req.MaxTokens != 512 {
		t.Errorf("MaxTokens should be set regardless of Thinking, got %v", req.MaxTokens)
	}
}
