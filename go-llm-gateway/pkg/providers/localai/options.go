package localai

import (
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/logging"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openai"
)

type Option func(*Provider)

type Config struct {
	BaseURL string `json:"base_url" yaml:"base_url"`
}

func WithBaseURL(baseURL string) Option { return func(p *Provider) { p.invocationBaseURL = baseURL } }
func WithConfig(config Config) Option   { return func(p *Provider) { p.configBaseURL = config.BaseURL } }
func WithEnvironmentBaseURL(baseURL string) Option {
	return func(p *Provider) { p.envLookup = func(string) string { return baseURL } }
}
func WithWebSocketDialer(dialer openai.WebSocketDialer) Option {
	return func(p *Provider) { p.dialer = dialer }
}
func WithLogger(logger logging.Logger) Option {
	return func(p *Provider) { p.logger = logger }
}
