package services

import (
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
)

func TestPlanSessionRuntimeScheduledAudioDispatchPolicy(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		barge      bool
		wantPolicy ScheduledAudioDispatchPolicy
	}{
		{
			name:       "default completion gated",
			wantPolicy: ScheduledAudioDispatchCompletionGated,
		},
		{
			name:       "explicit active response",
			barge:      true,
			wantPolicy: ScheduledAudioDispatchActiveResponse,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			plan, err := planSessionRuntimeWithFactory(SessionRunOptions{
				Provider:          sessionProviderOpenAI,
				Model:             "gpt-realtime",
				APIKey:            "test-key",
				LoadedConfig:      &config.Config{Model: config.ModelConfig{Provider: sessionProviderOpenAI, OpenAI: &config.OpenAIConfig{Model: "gpt-realtime", APIKey: "test-key"}}},
				SessionInferencer: &scriptedSessionInferencer{},
				AudioInTurnBarge:  testCase.barge,
				AudioInputs:       []ScheduledAudioInput{{AfterCompletedTurns: 0, PCM: []byte{1}, EndOfTurn: true}, {AfterCompletedTurns: 1, PCM: []byte{2}, EndOfTurn: true}},
			}, sessionRuntimeFactory{})
			if err != nil {
				t.Fatalf("plan session runtime: %v", err)
			}
			if plan.scheduledAudioDispatch != testCase.wantPolicy {
				t.Fatalf("plan scheduled audio policy = %q, want %q", plan.scheduledAudioDispatch, testCase.wantPolicy)
			}
			if plan.loop.ScheduledAudioDispatch != testCase.wantPolicy {
				t.Fatalf("loop scheduled audio policy = %q, want %q", plan.loop.ScheduledAudioDispatch, testCase.wantPolicy)
			}
		})
	}
}

func TestValidateSessionAudioInTurnBargePreservesOrdinarySequences(t *testing.T) {
	for _, turnCount := range []int{0, 1, 2, 3} {
		if err := ValidateSessionAudioInTurnBarge(false, turnCount); err != nil {
			t.Fatalf("default policy with %d turns: %v", turnCount, err)
		}
	}
	if err := ValidateSessionAudioInTurnBarge(true, 2); err != nil {
		t.Fatalf("two-turn barge-in sequence: %v", err)
	}
}
