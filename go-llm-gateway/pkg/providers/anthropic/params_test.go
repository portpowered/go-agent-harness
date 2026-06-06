package anthropic

import (
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/packages/param"
	"github.com/portpowered/go-llm-gateway/pkg/providers"
)

func TestApplyInferenceRequestOptions_Defaults(t *testing.T) {
	params := anthropic.MessageNewParams{
		Model:    anthropic.Model("claude-3-5-sonnet"),
		Messages: []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock("Hi"))},
	}
	applyInferenceRequestOptions(&params, providers.InferenceRequest{})

	if params.MaxTokens != defaultMaxTokens {
		t.Errorf("MaxTokens: got %d, want %d", params.MaxTokens, defaultMaxTokens)
	}
	if param.IsOmitted(params.Temperature) == false {
		t.Error("Temperature should be omitted when not set")
	}
	if len(params.StopSequences) != 0 {
		t.Errorf("StopSequences: got %v, want empty", params.StopSequences)
	}
	// When req.Thinking is nil we do not set params.Thinking (zero value)
	if params.Thinking.OfDisabled != nil || params.Thinking.OfEnabled != nil || params.Thinking.OfAdaptive != nil {
		t.Error("Thinking should be unset when req.Thinking is nil")
	}
	// When req.CacheControl is nil we do not set params.CacheControl
	if params.CacheControl.Type != "" {
		t.Error("CacheControl should be unset when req.CacheControl is nil")
	}
}

func TestApplyInferenceRequestOptions_MaxTokens(t *testing.T) {
	params := anthropic.MessageNewParams{
		Model:    anthropic.Model("claude-3-5-sonnet"),
		Messages: []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock("Hi"))},
	}
	max := 2048
	applyInferenceRequestOptions(&params, providers.InferenceRequest{MaxTokens: &max})

	if params.MaxTokens != 2048 {
		t.Errorf("MaxTokens: got %d, want 2048", params.MaxTokens)
	}
}

func TestApplyInferenceRequestOptions_Temperature(t *testing.T) {
	params := anthropic.MessageNewParams{
		Model:    anthropic.Model("claude-3-5-sonnet"),
		Messages: []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock("Hi"))},
	}
	temp := 0.7
	applyInferenceRequestOptions(&params, providers.InferenceRequest{Temperature: &temp})

	if !params.Temperature.Valid() || params.Temperature.Value != 0.7 {
		t.Errorf("Temperature: got valid=%v value=%v, want valid=true value=0.7", params.Temperature.Valid(), params.Temperature.Value)
	}
}

func TestApplyInferenceRequestOptions_StopSequences(t *testing.T) {
	params := anthropic.MessageNewParams{
		Model:    anthropic.Model("claude-3-5-sonnet"),
		Messages: []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock("Hi"))},
	}
	applyInferenceRequestOptions(&params, providers.InferenceRequest{
		StopSequences: []string{"\n", "END"},
	})

	if len(params.StopSequences) != 2 || params.StopSequences[0] != "\n" || params.StopSequences[1] != "END" {
		t.Errorf("StopSequences: got %v", params.StopSequences)
	}
}

func TestApplyInferenceRequestOptions_ThinkingEnabled(t *testing.T) {
	params := anthropic.MessageNewParams{
		Model:    anthropic.Model("claude-3-5-sonnet"),
		Messages: []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock("Hi"))},
	}
	applyInferenceRequestOptions(&params, providers.InferenceRequest{
		Thinking: &providers.ThinkingConfig{Mode: providers.ThinkingEnabled, BudgetTokens: 4096},
	})

	if params.Thinking.OfEnabled == nil {
		t.Fatal("Thinking should be OfEnabled")
	}
	if params.Thinking.OfEnabled.BudgetTokens != 4096 {
		t.Errorf("BudgetTokens: got %d, want 4096", params.Thinking.OfEnabled.BudgetTokens)
	}
}

func TestApplyInferenceRequestOptions_ThinkingAdaptive(t *testing.T) {
	params := anthropic.MessageNewParams{
		Model:    anthropic.Model("claude-3-5-sonnet"),
		Messages: []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock("Hi"))},
	}
	applyInferenceRequestOptions(&params, providers.InferenceRequest{
		Thinking: &providers.ThinkingConfig{Mode: providers.ThinkingAdaptive},
	})

	if params.Thinking.OfAdaptive == nil {
		t.Fatal("Thinking should be OfAdaptive")
	}
}

func TestApplyInferenceRequestOptions_ThinkingDisabled(t *testing.T) {
	params := anthropic.MessageNewParams{
		Model:    anthropic.Model("claude-3-5-sonnet"),
		Messages: []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock("Hi"))},
	}
	applyInferenceRequestOptions(&params, providers.InferenceRequest{
		Thinking: &providers.ThinkingConfig{Mode: providers.ThinkingOff},
	})

	if params.Thinking.OfDisabled == nil {
		t.Fatal("Thinking should be OfDisabled")
	}
}

func TestThinkingConfigToAnthropic_EnabledClampsMinBudget(t *testing.T) {
	u := ThinkingConfigToAnthropic(providers.ThinkingConfig{
		Mode:         providers.ThinkingEnabled,
		BudgetTokens: 100, // below 1024
	})
	if u.OfEnabled == nil {
		t.Fatal("expected OfEnabled")
	}
	if u.OfEnabled.BudgetTokens != 1024 {
		t.Errorf("BudgetTokens should be clamped to 1024, got %d", u.OfEnabled.BudgetTokens)
	}
}

func TestThinkingConfigToAnthropic_EnabledPreservesBudget(t *testing.T) {
	u := ThinkingConfigToAnthropic(providers.ThinkingConfig{
		Mode:         providers.ThinkingEnabled,
		BudgetTokens: 2048,
	})
	if u.OfEnabled == nil {
		t.Fatal("expected OfEnabled")
	}
	if u.OfEnabled.BudgetTokens != 2048 {
		t.Errorf("BudgetTokens: got %d, want 2048", u.OfEnabled.BudgetTokens)
	}
}

func TestApplyInferenceRequestOptions_CacheControl_InMemory(t *testing.T) {
	params := anthropic.MessageNewParams{
		Model:    anthropic.Model("claude-3-5-sonnet"),
		Messages: []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock("Hi"))},
	}
	applyInferenceRequestOptions(&params, providers.InferenceRequest{
		CacheControl: &providers.CacheControlConfig{CacheRetentionPolicy: providers.CacheRetentionInMemory},
	})

	if params.CacheControl.TTL != anthropic.CacheControlEphemeralTTLTTL5m {
		t.Errorf("CacheControl.TTL (in_memory): got %q, want 5m", params.CacheControl.TTL)
	}
}

func TestApplyInferenceRequestOptions_CacheControl_24h(t *testing.T) {
	params := anthropic.MessageNewParams{
		Model:    anthropic.Model("claude-3-5-sonnet"),
		Messages: []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock("Hi"))},
	}
	applyInferenceRequestOptions(&params, providers.InferenceRequest{
		CacheControl: &providers.CacheControlConfig{CacheRetentionPolicy: providers.CacheRetention24h},
	})

	if params.CacheControl.TTL != anthropic.CacheControlEphemeralTTLTTL1h {
		t.Errorf("CacheControl.TTL (24h): got %q, want 1h", params.CacheControl.TTL)
	}
}

func TestApplyInferenceRequestOptions_CacheControl_EmptyPolicyDefaultsToInMemory(t *testing.T) {
	params := anthropic.MessageNewParams{
		Model:    anthropic.Model("claude-3-5-sonnet"),
		Messages: []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock("Hi"))},
	}
	applyInferenceRequestOptions(&params, providers.InferenceRequest{
		CacheControl: &providers.CacheControlConfig{CacheRetentionPolicy: ""},
	})

	if params.CacheControl.TTL != anthropic.CacheControlEphemeralTTLTTL5m {
		t.Errorf("CacheControl.TTL (empty policy): got %q, want 5m", params.CacheControl.TTL)
	}
}
