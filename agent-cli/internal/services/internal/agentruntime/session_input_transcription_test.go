package agentruntime

import (
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/inference"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
)

func TestResolveInputAudioTranscriptionPolicyIsRequestScoped(t *testing.T) {
	cases := []struct {
		name       string
		provider   string
		audioInput bool
		noInput    bool
		replay     string
		want       models.InputAudioTranscriptionConfig
	}{
		{
			name:       "OpenAI audio defaults enabled",
			provider:   config.ProviderOpenAI,
			audioInput: true,
			want: models.InputAudioTranscriptionConfig{
				Enabled: true,
				Model:   models.DefaultInputAudioTranscriptionModel,
			},
		},
		{
			name:       "explicit opt-out",
			provider:   config.ProviderOpenAI,
			audioInput: true,
			noInput:    true,
		},
		{
			name:     "text-only",
			provider: config.ProviderOpenAI,
		},
		{
			name:       "Grok audio",
			provider:   config.ProviderGrok,
			audioInput: true,
		},
		{
			name:       "replay",
			provider:   config.ProviderOpenAI,
			audioInput: true,
			replay:     "historical.session.json",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := resolveInputAudioTranscriptionPolicy(SessionRunOptions{ModelCatalog: testModelCatalog(),
				NoInputTranscription: testCase.noInput,
				ReplayPath:           testCase.replay,
			}, testCase.provider, testCase.audioInput)
			if got != testCase.want {
				t.Fatalf("resolved policy = %#v, want %#v", got, testCase.want)
			}
		})
	}
}

func TestOpenAIRealtimeSessionBuilderCarriesResolvedInputAudioTranscriptionPolicy(t *testing.T) {
	policy := models.InputAudioTranscriptionConfig{Enabled: true, Model: models.DefaultInputAudioTranscriptionModel}
	inferencer, err := buildOpenAIRealtimeSessionInferencerWithToolsAndInputAudioTranscription(
		config.OpenAIConfig{Model: openAIRealtimeDefaultModel},
		"",
		&stubRuntimeDialer{id: "input-transcription-test"},
		[]messages.ToolDefinition{{Name: "lookup"}},
		policy,
	)
	if err != nil {
		t.Fatalf("build OpenAI realtime session: %v", err)
	}
	requested, ok := inferencer.(interface {
		Request() inference.SessionRequest
	})
	if !ok {
		t.Fatalf("inferencer type %T does not expose its request", inferencer)
	}
	got := requested.Request().Config.InputAudioTranscription
	if got == nil || *got != policy {
		t.Fatalf("session request policy = %#v, want %#v", got, policy)
	}
}
