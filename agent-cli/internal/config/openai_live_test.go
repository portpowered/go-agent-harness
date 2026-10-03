package config

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openai/chatgptauth"
)

// signedInConfigDir returns a config directory with a ChatGPT login.
func signedInConfigDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := chatgptauth.NewFileStore(ChatGPTAuthStorePath(dir)).Save(chatgptauth.Credential{
		AccessToken: "token", RefreshToken: "refresh", AccountID: "acct", ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	return dir
}

// The credential order of openai-live: with no model the ChatGPT sign-in
// picks gpt-live-1-codex and an API key gpt-live-1; an explicit model
// narrows the order to the credential it accepts and fails fast without it.
func TestOpenAILiveCredentialOrder(t *testing.T) {
	signedIn, empty := signedInConfigDir(t), t.TempDir()
	if got := DefaultOpenAILiveModel(signedIn); got != OpenAILiveChatGPTModel {
		t.Fatalf("default with a ChatGPT login = %q", got)
	}
	for _, dir := range []string{empty, ""} {
		if got := DefaultOpenAILiveModel(dir); got != OpenAILiveAPIKeyModel {
			t.Fatalf("default without a login (%q) = %q", dir, got)
		}
	}

	codex, err := ResolveOpenAILiveCredential(OpenAILiveChatGPTModel, "sk-ignored", signedIn)
	if err != nil || codex.APIKey != "" || codex.ChatGPTAuthPath != ChatGPTAuthStorePath(signedIn) {
		t.Fatalf("codex with a login = %+v, %v; want the store and no API key", codex, err)
	}
	if _, err := ResolveOpenAILiveCredential(OpenAILiveChatGPTModel, "sk-test", empty); err == nil || !strings.Contains(err.Error(), "yui auth chatgpt") {
		t.Fatalf("codex without a login = %v, want the sign-in instruction", err)
	}
	live, err := ResolveOpenAILiveCredential(OpenAILiveAPIKeyModel, "sk-test", signedIn)
	if err != nil || live.APIKey != "sk-test" || live.ChatGPTAuthPath != "" {
		t.Fatalf("gpt-live-1 with a key = %+v, %v", live, err)
	}
	_, err = ResolveOpenAILiveCredential(OpenAILiveAPIKeyModel, "", signedIn)
	if err == nil || !strings.Contains(err.Error(), "requires an OpenAI API key") || !strings.Contains(err.Error(), "your ChatGPT sign-in runs gpt-live-1-codex") {
		t.Fatalf("gpt-live-1 with only a login = %v, want the API-key refusal pointing at the codex model", err)
	}
	_, err = ResolveOpenAILiveCredential(OpenAILiveAPIKeyModel, " ", empty)
	if err == nil || !strings.Contains(err.Error(), "AGENT_MODEL__OPENAI__API_KEY") || !strings.Contains(err.Error(), "run `yui auth chatgpt`") {
		t.Fatalf("gpt-live-1 with nothing = %v, want both ways to sign in", err)
	}
}

// A store that exists but is unusable still counts as a sign-in, so the
// session reports the problem instead of silently falling back to a key.
func TestAnUnusableChatGPTStoreCountsAsSignedIn(t *testing.T) {
	dir := signedInConfigDir(t)
	if err := os.Chmod(ChatGPTAuthStorePath(dir), 0o644); err != nil {
		t.Fatal(err)
	}
	if !ChatGPTSignedIn(dir) {
		t.Fatal("a world-readable store does not count as a sign-in")
	}
}
