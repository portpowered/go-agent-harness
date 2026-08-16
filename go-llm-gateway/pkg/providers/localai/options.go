package localai

import "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/logging"

// Option configures a LocalAI provider.
type Option func(*Provider)

// Config is the LocalAI portion of a loaded configuration file.
type Config struct {
	BaseURL string `json:"base_url" yaml:"base_url"`
}

// WithBaseURL supplies the highest-precedence, per-invocation endpoint.
func WithBaseURL(baseURL string) Option {
	return func(p *Provider) { p.invocationBaseURL = baseURL }
}

// WithConfig supplies the endpoint loaded from configuration.
func WithConfig(config Config) Option {
	return func(p *Provider) { p.configBaseURL = config.BaseURL }
}

// WithConfigBaseURL supplies the endpoint loaded from configuration.
func WithConfigBaseURL(baseURL string) Option {
	return func(p *Provider) { p.configBaseURL = baseURL }
}

// WithEnvironmentLookup replaces the environment lookup function. It is
// useful for composition seams and deterministic tests; the default is
// os.Getenv.
func WithEnvironmentLookup(lookup func(string) string) Option {
	return func(p *Provider) { p.envLookup = lookup }
}

// WithEnvironmentBaseURL supplies a deterministic environment-level value.
func WithEnvironmentBaseURL(baseURL string) Option {
	return WithEnvironmentLookup(func(string) string { return baseURL })
}

// WithWebSocketDialer injects the OpenAI-compatible realtime dialer.
func WithWebSocketDialer(d WebSocketDialer) Option {
	return func(p *Provider) {
		if d != nil {
			p.dialer = d
		}
	}
}

// WithLogger sets the provider logger. LocalAI logs endpoint metadata only.
func WithLogger(logger logging.Logger) Option {
	return func(p *Provider) {
		if logger != nil {
			p.logger = logger
		}
	}
}
