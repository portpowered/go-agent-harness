package livehost

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/livedelegation"
)

func signedInConfigDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := config.ChatGPTAuthStorePath(dir)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create auth directory: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{}`), 0o600); err != nil {
		t.Fatalf("write auth store: %v", err)
	}
	return dir
}

// The delegation backend of a GPT-Live session defaults to openai-chatgpt
// on a ChatGPT login (the account's default model unless one is
// configured), otherwise to model.provider, falling back to openai for a
// voice-only provider; session.delegation overrides the provider, model and
// limits. Raw keys become credential references.
func TestDelegationPolicyPicksTheBackendForGPTLive(t *testing.T) {
	signedIn := signedInConfigDir(t)
	reference := func(key string) string { return "ref:" + key }
	withChatGPTModel := openAIConfig(config.ProviderOpenAI)
	withChatGPTModel.Model.OpenAIChatGPT = &config.ChatGPTConfig{Model: "gpt-chosen", BaseURL: "https://chatgpt.test/codex"}
	configured := openAIConfig(config.ProviderOpenAI)
	configured.Model.OpenRouter = &config.OpenAIConfig{Model: "router-default", APIKey: "sk-router", BaseURL: "https://router.test"}
	configured.Session = &config.SessionConfig{Delegation: &config.SessionDelegationConfig{
		Provider: "OpenRouter", Model: "router-chosen", MaxConcurrency: 1, MaxTurns: 4, MaxDurationSeconds: 30, MaxTokens: 5000,
	}}
	liveOnly := config.Config{Model: config.ModelConfig{Provider: config.ProviderOpenAILive}}

	for name, tc := range map[string]struct {
		cfg       config.Config
		configDir string
		want      livedelegation.Policy
	}{
		"ChatGPT login, account default model": {
			cfg: openAIConfig(config.ProviderOpenAI), configDir: signedIn,
			want: livedelegation.Policy{Backend: livedelegation.Backend{Provider: config.ProviderOpenAIChatGPT, ChatGPTAuthPath: config.ChatGPTAuthStorePath(signedIn)}},
		},
		"ChatGPT login, configured model": {
			cfg: withChatGPTModel, configDir: signedIn,
			want: livedelegation.Policy{Backend: livedelegation.Backend{Provider: config.ProviderOpenAIChatGPT, Model: "gpt-chosen", BaseURL: "https://chatgpt.test/codex", ChatGPTAuthPath: config.ChatGPTAuthStorePath(signedIn)}},
		},
		"no login, configured text provider": {
			cfg: openAIConfig(config.ProviderOpenAI), configDir: t.TempDir(),
			want: livedelegation.Policy{Backend: livedelegation.Backend{Provider: config.ProviderOpenAI, Model: "gpt-realtime-2.1", BaseURL: "https://api.openai.com/v1", CredentialReference: "ref:sk-openai"}},
		},
		"no login, voice-only provider uses the voice key": {
			cfg: liveOnly, configDir: t.TempDir(),
			want: livedelegation.Policy{Backend: livedelegation.Backend{Provider: config.ProviderOpenAI, CredentialReference: "ref:sk-voice"}},
		},
		"session.delegation overrides": {
			cfg: configured, configDir: signedIn,
			want: livedelegation.Policy{
				Backend: livedelegation.Backend{Provider: config.ProviderOpenRouter, Model: "router-chosen", BaseURL: "https://router.test", CredentialReference: "ref:sk-router"},
				Limits:  livedelegation.Limits{Concurrency: 1, MaxTurns: 4, MaxDuration: 30 * time.Second, MaxTokens: 5000},
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := tc.cfg
			got := delegationPolicy(delegationInputs{
				loaded: &cfg, configDir: tc.configDir, voiceProvider: config.ProviderOpenAILive, voiceAPIKey: "sk-voice",
				credentialReference: reference,
			})
			if got == nil || *got != tc.want {
				t.Fatalf("policy = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestDelegationPolicyIsOnlyForGPTLive(t *testing.T) {
	cfg := openAIConfig(config.ProviderOpenAI)
	if got := delegationPolicy(delegationInputs{loaded: &cfg, configDir: signedInConfigDir(t), voiceProvider: config.ProviderOpenAI}); got != nil {
		t.Fatalf("policy for openai realtime = %+v, want none", got)
	}
}
