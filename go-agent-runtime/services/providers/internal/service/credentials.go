package service

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openai/chatgptauth"
)

// Explicit endpoints may implement anonymous local inference. The built-in
// hosted endpoints require credentials before any network operation begins.
func requiresCredential(endpoint string) bool {
	if strings.TrimSpace(endpoint) == "" {
		return true
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return false
	}
	switch strings.ToLower(parsed.Hostname()) {
	case "api.openai.com", "api.x.ai", "openrouter.ai":
		return true
	default:
		return false
	}
}

func validateSessionCredential(cfg providers.SessionConfig, provider, model string) error {
	if provider == providers.OpenAILiveProvider && model == providers.OpenAILiveCodexModel {
		return validateChatGPTLogin(cfg)
	}
	if strings.TrimSpace(cfg.APIKey) != "" || strings.TrimSpace(cfg.ReplayPath) != "" {
		return nil
	}
	endpoint := cfg.RealtimeURL
	if strings.TrimSpace(endpoint) == "" {
		endpoint = cfg.BaseURL
	}
	if !requiresCredential(endpoint) {
		return nil
	}
	if provider == providers.OpenAILiveProvider {
		// gpt-live-1 accepts only an OpenAI API key; a ChatGPT sign-in is not
		// a credential for it.
		return fmt.Errorf("%s %s requires an OpenAI API key; a ChatGPT sign-in is not accepted for it (use --model %s with `yui auth chatgpt`)", provider, model, providers.OpenAILiveCodexModel)
	}
	return fmt.Errorf("%s realtime api key is missing", provider)
}

// validateChatGPTLogin requires the ChatGPT auth store for gpt-live-1-codex,
// which takes no API key, before any network operation. The route has no
// provider capture, so record and replay are refused too.
func validateChatGPTLogin(cfg providers.SessionConfig) error {
	label := providers.OpenAILiveProvider + " " + providers.OpenAILiveCodexModel
	if strings.TrimSpace(cfg.ReplayPath) != "" || strings.TrimSpace(cfg.RecordPath) != "" {
		return fmt.Errorf("%s does not support provider capture record or replay yet", label)
	}
	path := strings.TrimSpace(cfg.ChatGPTAuthPath)
	if path == "" {
		return fmt.Errorf("%s runs on a ChatGPT sign-in: %w", label, chatgptauth.ErrNotLoggedIn)
	}
	if _, err := chatgptauth.NewFileStore(path).Load(); err != nil {
		return fmt.Errorf("%s runs on a ChatGPT sign-in: %w", label, err)
	}
	return nil
}
