package config

import "strings"

// ConfiguredAPIKeys returns every API key cfg configures: the model provider
// blocks (any of which can back a session or its delegations) and the web
// tools. They are redacted from evidence whichever one a session uses.
func (cfg Config) ConfiguredAPIKeys() []string {
	model := cfg.Model
	keys := []string{cfg.Tools.Web.Brave.APIKey}
	for _, block := range []*OpenAIConfig{model.OpenAI, model.OpenRouter, model.Local} {
		if block != nil {
			keys = append(keys, block.APIKey)
		}
	}
	if model.Claude != nil {
		keys = append(keys, model.Claude.APIKey)
	}
	if model.Fal != nil {
		keys = append(keys, model.Fal.APIKey)
	}
	if model.Grok != nil {
		keys = append(keys, model.Grok.APIKey)
	}
	out := keys[:0]
	for _, key := range keys {
		if strings.TrimSpace(key) != "" {
			out = append(out, key)
		}
	}
	return out
}
