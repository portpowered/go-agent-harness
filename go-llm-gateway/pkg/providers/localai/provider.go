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
	// ProviderName is the gateway provider identifier.
	ProviderName = "localai"
	// ModelID is the customer-facing LocalAI realtime model identifier.
	ModelID = "localai/gpt-realtime"
	// WireModel is the model name sent to LocalAI's OpenAI-compatible API.
	WireModel = "gpt-realtime"

	// DefaultRealtimeBaseURL is the compiled LocalAI realtime endpoint before
	// the required model query is added.
	DefaultRealtimeBaseURL = "ws://localhost:8080/v1/realtime"
	// DefaultEndpoint is the fully normalized compiled endpoint.
	DefaultEndpoint = DefaultRealtimeBaseURL + "?model=" + WireModel
	// BaseURLEnv is the environment override used by the gateway composition
	// layer when no invocation or configuration endpoint is supplied.
	BaseURLEnv = "AGENT_MODEL__LOCALAI__BASE_URL"

	// The OpenAI adapter currently uses a non-empty key as an internal gate
	// before it invokes its injectable dialer. This marker never leaves this
	// package: no authorization header is sent to LocalAI.
	openAISessionGate = "localai-session-without-credentials"
)

// Provider implements the gateway's realtime session contracts for LocalAI.
type Provider struct {
	invocationBaseURL string
	configBaseURL     string
	envLookup         func(string) string
	dialer            openai.WebSocketDialer
	logger            logging.Logger
}

// LocalAIProvider is the descriptive name for Provider.
type LocalAIProvider = Provider

var _ providers.SessionProvider = (*Provider)(nil)
var _ providers.CapabilityReporter = (*Provider)(nil)

// New creates a credential-free LocalAI realtime provider.
func New(opts ...Option) *Provider {
	p := &Provider{
		envLookup: os.Getenv,
		dialer:    newDefaultWebSocketDialer(),
		logger:    logging.DummyLogger(),
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// Name returns the stable gateway provider identifier.
func (p *Provider) Name() string { return ProviderName }

// Model returns the customer-facing model identifier owned by this provider.
func (p *Provider) Model() string { return ModelID }

// Capabilities reports the LocalAI behavior proven by this adapter.
func (p *Provider) Capabilities() capabilities.ProviderCapabilities {
	stateless := capabilities.Unsupported("the LocalAI provider implements realtime sessions only")
	return capabilities.ProviderCapabilities{
		Provider: ProviderName,
		Stateless: capabilities.StatelessCapabilities{
			Tools:                  stateless,
			Streaming:              stateless,
			ImageInput:             stateless,
			AudioInput:             stateless,
			AudioOutput:            stateless,
			VideoOutput:            stateless,
			Reasoning:              stateless,
			PromptCaching:          stateless,
			ProviderSpecificConfig: stateless,
		},
		Session: capabilities.SessionCapabilities{
			Sessions:               capabilities.Supported("LocalAI OpenAI-compatible realtime WebSocket sessions are implemented"),
			Tools:                  capabilities.Supported("realtime function tools are serialized by the OpenAI-compatible adapter"),
			AudioInput:             capabilities.Supported("realtime input audio events are supported"),
			AudioOutput:            capabilities.Supported("realtime output audio events are decoded"),
			ProviderSpecificConfig: capabilities.Unsupported("SessionConfig Config is not merged by the LocalAI adapter"),
		},
		Metadata: map[string]string{
			"model":              ModelID,
			"wireModel":          WireModel,
			"credentialRequired": "false",
			"defaultEndpoint":    DefaultEndpoint,
		},
	}
}

// ConnectSession establishes a LocalAI realtime session through the shared
// OpenAI-compatible event translation. The public provider never accepts or
// reads an API credential.
func (p *Provider) ConnectSession(ctx context.Context, config models.SessionConfig) (messages.Session, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("localai: connection cancelled: %w", err)
	}

	model, err := wireModel(config.Model)
	if err != nil {
		return nil, err
	}
	endpoint, err := p.endpoint()
	if err != nil {
		return nil, err
	}

	config.Model = model
	delegate := openai.New(
		openai.WithAPIKey(openAISessionGate),
		openai.WithModel(WireModel),
		openai.WithRealtimeBaseURL(endpoint),
		openai.WithWebSocketDialer(&noAuthDialer{inner: p.dialer}),
		openai.WithLogger(p.logger),
	)
	session, err := delegate.ConnectSession(ctx, config)
	if err == nil {
		return session, nil
	}

	var dialErr *dialError
	if errors.As(err, &dialErr) {
		if errors.Is(dialErr.Err, context.Canceled) || errors.Is(dialErr.Err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("localai: connection cancelled: %w", dialErr.Err)
		}
		p.logger.Error("localai: realtime connection failed", logging.Field{Key: "endpoint", Value: safeEndpoint(endpoint)})
		return nil, NewConnectionError(safeEndpoint(endpoint), dialErr.Err)
	}
	return nil, fmt.Errorf("localai: initialize realtime session at %s: %w", safeEndpoint(endpoint), err)
}

func (p *Provider) endpoint() (string, error) {
	envURL := ""
	if p.envLookup != nil {
		envURL = p.envLookup(BaseURLEnv)
	}
	base := ResolveBaseURL(p.invocationBaseURL, p.configBaseURL, envURL)
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
	switch strings.TrimSpace(model) {
	case "", ModelID, WireModel:
		return WireModel, nil
	default:
		return "", providers.NewUnsupportedRequestError(
			ProviderName,
			"model",
			model,
			[]string{ModelID},
			fmt.Sprintf("localai: unsupported realtime model %q (supported: %q)", model, ModelID),
		)
	}
}

// ResolveBaseURL applies the documented endpoint precedence. Empty values are
// ignored, while a selected non-empty endpoint is otherwise preserved.
func ResolveBaseURL(invocation, configuration, environment string) string {
	for _, candidate := range []string{invocation, configuration, environment, DefaultRealtimeBaseURL} {
		if strings.TrimSpace(candidate) != "" {
			return strings.TrimSpace(candidate)
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
