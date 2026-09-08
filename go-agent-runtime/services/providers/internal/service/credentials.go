package service

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
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

func validateSessionCredential(cfg providers.SessionConfig, provider string) error {
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
	return fmt.Errorf("%s realtime api key is missing", provider)
}
