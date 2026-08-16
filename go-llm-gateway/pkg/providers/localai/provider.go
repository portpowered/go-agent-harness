// Package localai provides a credential-free adapter for LocalAI's
// OpenAI-compatible realtime WebSocket API.
package localai

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/capabilities"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/logging"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openai"
)

const (
	ProviderName           = "localai"
	ModelID                = "localai/gpt-realtime"
	WireModel              = "gpt-realtime"
	DefaultRealtimeBaseURL = "ws://localhost:8080/v1/realtime"
	DefaultEndpoint        = DefaultRealtimeBaseURL + "?model=" + WireModel
	BaseURLEnv             = "AGENT_MODEL__LOCALAI__BASE_URL"
	openAISessionGate      = "localai-session-without-credentials"
)

// Provider implements LocalAI's realtime session contracts.
type Provider struct {
	invocationBaseURL, configBaseURL string
	envLookup                        func(string) string
	dialer                           openai.WebSocketDialer
	logger                           logging.Logger
}

var _ providers.SessionProvider = (*Provider)(nil)
var _ providers.CapabilityReporter = (*Provider)(nil)

func New(opts ...Option) *Provider {
	p := &Provider{envLookup: os.Getenv, dialer: openai.NewDefaultWebSocketDialer(), logger: logging.DummyLogger()}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

func (p *Provider) Name() string  { return ProviderName }
func (p *Provider) Model() string { return ModelID }

func (p *Provider) Capabilities() capabilities.ProviderCapabilities {
	return capabilities.ProviderCapabilities{
		Provider: ProviderName,
		Session: capabilities.SessionCapabilities{
			Sessions:    capabilities.Supported("LocalAI realtime WebSocket sessions are implemented"),
			AudioInput:  capabilities.Supported("realtime input audio events are supported"),
			AudioOutput: capabilities.Supported("realtime output audio events are decoded"),
		},
		Metadata: map[string]string{"model": ModelID, "wireModel": WireModel, "credentialRequired": "false", "defaultEndpoint": DefaultEndpoint},
	}
}

func (p *Provider) ConnectSession(ctx context.Context, config models.SessionConfig) (messages.Session, error) {
	if _, err := wireModel(config.Model); err != nil {
		return nil, err
	}
	endpoint, err := p.endpoint()
	if err != nil {
		return nil, err
	}
	config.Model = WireModel
	session, err := openai.New(
		openai.WithAPIKey(openAISessionGate), openai.WithModel(WireModel),
		openai.WithRealtimeBaseURL(endpoint), openai.WithWebSocketDialer(&noAuthDialer{p.dialer}),
		openai.WithLogger(p.logger),
	).ConnectSession(ctx, config)
	if err == nil {
		return session, nil
	}
	var dialErr *dialError
	if errors.As(err, &dialErr) {
		endpoint = safeEndpoint(endpoint)
		p.logger.Error("localai: realtime connection failed", logging.Field{Key: "endpoint", Value: endpoint})
		return nil, &ConnectionError{Endpoint: endpoint, Err: dialErr}
	}
	return nil, fmt.Errorf("localai: initialize realtime session at %s: %w", safeEndpoint(endpoint), err)
}

func (p *Provider) endpoint() (string, error) {
	environment := ""
	if p.envLookup != nil {
		environment = p.envLookup(BaseURLEnv)
	}
	base := ResolveBaseURL(p.invocationBaseURL, p.configBaseURL, environment)
	parsed, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("localai: invalid endpoint %s: %w", safeEndpoint(base), err)
	}
	if parsed.Scheme != "ws" && parsed.Scheme != "wss" {
		return "", fmt.Errorf("localai: invalid endpoint scheme %q for %s", parsed.Scheme, safeEndpoint(base))
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("localai: invalid endpoint host for %s", safeEndpoint(base))
	}
	query := parsed.Query()
	if query.Get("model") == "" {
		query.Set("model", WireModel)
		parsed.RawQuery = query.Encode()
	}
	return parsed.String(), nil
}

func wireModel(model string) (string, error) {
	model = strings.TrimSpace(model)
	if model == "" || model == ModelID || model == WireModel {
		return WireModel, nil
	}
	return "", providers.NewUnsupportedRequestError(ProviderName, "model", model, []string{ModelID}, fmt.Sprintf("localai: unsupported realtime model %q (supported: %q)", model, ModelID))
}

// ResolveBaseURL applies invocation, configuration, environment, then default precedence.
func ResolveBaseURL(invocation, configuration, environment string) string {
	for _, value := range []string{invocation, configuration, environment, DefaultRealtimeBaseURL} {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return DefaultRealtimeBaseURL
}

func safeEndpoint(endpoint string) string {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return "<invalid>"
	}
	parsed.User = nil
	query := parsed.Query()
	for _, key := range []string{"key", "api_key", "access_token", "token"} {
		query.Del(key)
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}
