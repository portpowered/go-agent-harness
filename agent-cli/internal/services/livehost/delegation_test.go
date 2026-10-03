package livehost

import (
	"os"
	"path/filepath"
	"strings"
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
// limits. A Realtime or GPT-Live model is never inherited as the text model:
// without session.delegation.model the backend is reported unconfigured. The
// voice key is reused only on the voice key's own endpoint. Raw keys become
// credential references.
func TestDelegationPolicyPicksTheBackendForGPTLive(t *testing.T) {
	signedIn := signedInConfigDir(t)
	reference := func(key string) string { return "ref:" + key }
	withChatGPTModel := openAIConfig(config.ProviderOpenAI)
	withChatGPTModel.Model.OpenAIChatGPT = &config.ChatGPTConfig{Model: "gpt-chosen", BaseURL: "https://chatgpt.test/codex"}
	textModel := openAIConfig(config.ProviderOpenAI)
	textModel.Model.OpenAI.Model = "gpt-4.1"
	delegationModel := openAIConfig(config.ProviderOpenAI)
	delegationModel.Session = &config.SessionConfig{Delegation: &config.SessionDelegationConfig{Model: "gpt-4.1-mini"}}
	configured := openAIConfig(config.ProviderOpenAI)
	configured.Model.OpenRouter = &config.OpenAIConfig{Model: "router-default", APIKey: "sk-router", BaseURL: "https://router.test"}
	configured.Session = &config.SessionConfig{Delegation: &config.SessionDelegationConfig{
		Provider: "OpenRouter", Model: "router-chosen", MaxConcurrency: 1, MaxTurns: 4, MaxDurationSeconds: 30, MaxTokens: 5000,
	}}
	liveOnly := config.Config{Model: config.ModelConfig{Provider: config.ProviderOpenAILive}, Session: &config.SessionConfig{
		Delegation: &config.SessionDelegationConfig{Model: "gpt-4.1-mini"},
	}}
	const unconfigured = "no text model for delegation provider openai: set session.delegation.model (model.openai.model is unset or a voice model)"

	for name, tc := range map[string]struct {
		cfg          config.Config
		configDir    string
		voiceBaseURL string
		want         livedelegation.Policy
	}{
		"ChatGPT login, account default model": {
			cfg: openAIConfig(config.ProviderOpenAI), configDir: signedIn,
			want: livedelegation.Policy{Backend: livedelegation.Backend{Provider: config.ProviderOpenAIChatGPT, ChatGPTAuthPath: config.ChatGPTAuthStorePath(signedIn)}},
		},
		"ChatGPT login, configured model": {
			cfg: withChatGPTModel, configDir: signedIn,
			want: livedelegation.Policy{Backend: livedelegation.Backend{Provider: config.ProviderOpenAIChatGPT, Model: "gpt-chosen", BaseURL: "https://chatgpt.test/codex", ChatGPTAuthPath: config.ChatGPTAuthStorePath(signedIn)}},
		},
		"no login, a realtime model is never inherited": {
			cfg: openAIConfig(config.ProviderOpenAI), configDir: t.TempDir(),
			want: livedelegation.Policy{Backend: livedelegation.Backend{Provider: config.ProviderOpenAI, BaseURL: "https://api.openai.com/v1", CredentialReference: "ref:sk-openai", Unconfigured: unconfigured}},
		},
		"no login, a text model is inherited": {
			cfg: textModel, configDir: t.TempDir(),
			want: livedelegation.Policy{Backend: livedelegation.Backend{Provider: config.ProviderOpenAI, Model: "gpt-4.1", BaseURL: "https://api.openai.com/v1", CredentialReference: "ref:sk-openai"}},
		},
		"no login, session.delegation.model": {
			cfg: delegationModel, configDir: t.TempDir(),
			want: livedelegation.Policy{Backend: livedelegation.Backend{Provider: config.ProviderOpenAI, Model: "gpt-4.1-mini", BaseURL: "https://api.openai.com/v1", CredentialReference: "ref:sk-openai"}},
		},
		"voice-only provider reuses the voice key on its endpoint": {
			cfg: liveOnly, configDir: t.TempDir(), voiceBaseURL: "https://api.openai.com/v1/",
			want: livedelegation.Policy{Backend: livedelegation.Backend{Provider: config.ProviderOpenAI, Model: "gpt-4.1-mini", CredentialReference: "ref:sk-voice"}},
		},
		"a custom voice endpoint's key never reaches the backend": {
			cfg: liveOnly, configDir: t.TempDir(), voiceBaseURL: "https://voice-proxy.example/v1",
			want: livedelegation.Policy{Backend: livedelegation.Backend{Provider: config.ProviderOpenAI, Model: "gpt-4.1-mini"}},
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
				voiceBaseURL: tc.voiceBaseURL, credentialReference: reference,
			})
			if got == nil || *got != tc.want {
				t.Fatalf("policy = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// GPT-Live sessions learn how delegated work comes back; the codex route
// also learns its channels. Other providers' instructions are unchanged.
func TestDelegationGuidanceIsAddedToGPTLiveInstructions(t *testing.T) {
	live := withDelegationGuidance("Be brief.\n", config.ProviderOpenAILive, "gpt-live-1")
	if !strings.HasPrefix(live, "Be brief.\n\n## Delegated work\n") || !strings.Contains(live, "Do not guess the result while waiting.") || strings.Contains(live, "Speakable") {
		t.Fatalf("gpt-live-1 instructions = %q", live)
	}
	codex := withDelegationGuidance("", config.ProviderOpenAILive, config.OpenAILiveChatGPTModel)
	if !strings.HasPrefix(codex, "## Delegated work") || !strings.Contains(codex, "Commentary is silent background") || !strings.Contains(codex, "never read it aloud") || !strings.Contains(codex, "Speakable is the answer to deliver") {
		t.Fatalf("codex instructions = %q", codex)
	}
	if got := withDelegationGuidance("Be brief.", config.ProviderOpenAI, "gpt-realtime"); got != "Be brief." {
		t.Fatalf("openai instructions = %q, want them unchanged", got)
	}
}

func TestDelegationPolicyIsOnlyForGPTLive(t *testing.T) {
	cfg := openAIConfig(config.ProviderOpenAI)
	if got := delegationPolicy(delegationInputs{loaded: &cfg, configDir: signedInConfigDir(t), voiceProvider: config.ProviderOpenAI}); got != nil {
		t.Fatalf("policy for openai realtime = %+v, want none", got)
	}
}
