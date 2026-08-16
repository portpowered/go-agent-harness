// This file contains session option types, validation, configuration resolution, and provider construction for the session command.
package services

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/gateway"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/inference"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/grok"
	oaiprovider "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openai"
)

const (
	sessionProviderGrok   = config.ProviderGrok
	sessionProviderOpenAI = config.ProviderOpenAI
	openAIRealtimeModel   = "gpt-realtime"
	openAIRealtimeBaseURL = "wss://api.openai.com/v1/realtime"
)

// SessionRunOptions contains the user-facing agent session command options.
type SessionRunOptions struct {
	RecordPath string
	ReplayPath string
	Provider   string
	Model      string
	APIKey     string
	BaseURL    string
	ConfigDir  string
	Prompt     string

	SessionInferencer messages.SessionInferencer
	WebSocketDialer   grok.WebSocketDialer
}

func validateSessionRunOptions(opts SessionRunOptions) error {
	if opts.RecordPath == "" && opts.ReplayPath == "" {
		return fmt.Errorf("agent session requires --record <file>.json or --replay <file>.json")
	}
	if opts.RecordPath != "" && opts.ReplayPath != "" {
		return fmt.Errorf("agent session does not support --record and --replay together; choose one capture mode")
	}
	if opts.RecordPath != "" && !isJSONCapturePath(opts.RecordPath) {
		return fmt.Errorf("--record path %q must end with .json", opts.RecordPath)
	}
	if opts.ReplayPath != "" && !isJSONCapturePath(opts.ReplayPath) {
		return fmt.Errorf("--replay path %q must end with .json", opts.ReplayPath)
	}
	return nil
}

func isJSONCapturePath(path string) bool {
	return strings.EqualFold(filepath.Ext(path), ".json")
}

func validateInjectedLiveSession(opts SessionRunOptions) error {
	switch strings.ToLower(effectiveSessionProvider(opts)) {
	case sessionProviderOpenAI:
		_, err := resolveOpenAIRealtimeSessionConfig(opts)
		return err
	case sessionProviderGrok:
		_, err := resolveGrokSessionConfig(opts)
		return err
	default:
		return fmt.Errorf("--record supports session providers %q and %q; got %q", sessionProviderGrok, sessionProviderOpenAI, effectiveSessionProvider(opts))
	}
}

func effectiveSessionProvider(opts SessionRunOptions) string {
	if strings.TrimSpace(opts.Provider) != "" {
		return opts.Provider
	}
	storage, err := config.NewDefaultConfigStorage(opts.ConfigDir)
	if err != nil {
		return ""
	}
	loadedCfg, err := storage.Load()
	if err != nil {
		return ""
	}
	return loadedCfg.Model.Provider
}

func resolveGrokSessionConfig(opts SessionRunOptions) (config.GrokConfig, error) {
	storage, err := config.NewDefaultConfigStorage(opts.ConfigDir)
	if err != nil {
		return config.GrokConfig{}, fmt.Errorf("failed to initialize config: %w", err)
	}

	loadedCfg, err := storage.Load()
	if err != nil {
		return config.GrokConfig{}, fmt.Errorf("failed to load config: %w", err)
	}
	if strings.TrimSpace(opts.Provider) == "" && !strings.EqualFold(loadedCfg.Model.Provider, sessionProviderGrok) {
		return config.GrokConfig{}, fmt.Errorf("--record requires --provider grok for live session inference")
	}

	effective := loadedCfg.ApplyOverrides(opts.APIKey, opts.Model, opts.Provider, opts.BaseURL)
	if strings.TrimSpace(effective.Model.Provider) == "" {
		return config.GrokConfig{}, fmt.Errorf("--record requires --provider grok for live session inference")
	}
	if !strings.EqualFold(effective.Model.Provider, sessionProviderGrok) {
		return config.GrokConfig{}, fmt.Errorf("--record supports provider %q only; got %q", sessionProviderGrok, effective.Model.Provider)
	}
	if err := effective.ValidateGrokSession(); err != nil {
		return config.GrokConfig{}, err
	}
	active, err := effective.ActiveGrokConfig()
	if err != nil {
		return config.GrokConfig{}, err
	}
	return *active, nil
}

func resolveOpenAIRealtimeSessionConfig(opts SessionRunOptions) (config.OpenAIConfig, error) {
	storage, err := config.NewDefaultConfigStorage(opts.ConfigDir)
	if err != nil {
		return config.OpenAIConfig{}, fmt.Errorf("failed to initialize config: %w", err)
	}

	loadedCfg, err := storage.Load()
	if err != nil {
		return config.OpenAIConfig{}, fmt.Errorf("failed to load config: %w", err)
	}
	if strings.TrimSpace(opts.Provider) == "" && !strings.EqualFold(loadedCfg.Model.Provider, sessionProviderOpenAI) {
		return config.OpenAIConfig{}, fmt.Errorf("--record requires --provider openai for OpenAI realtime session inference")
	}

	effective := loadedCfg.ApplyOverrides(opts.APIKey, opts.Model, opts.Provider, opts.BaseURL)
	if strings.TrimSpace(effective.Model.Provider) == "" {
		return config.OpenAIConfig{}, fmt.Errorf("--record requires --provider openai for OpenAI realtime session inference")
	}
	if !strings.EqualFold(effective.Model.Provider, sessionProviderOpenAI) {
		return config.OpenAIConfig{}, fmt.Errorf("--record supports provider %q only for OpenAI realtime sessions; got %q", sessionProviderOpenAI, effective.Model.Provider)
	}
	active, err := effective.ActiveOpenAIConfig()
	if err != nil {
		return config.OpenAIConfig{}, err
	}
	if strings.TrimSpace(active.APIKey) == "" {
		return config.OpenAIConfig{}, fmt.Errorf("OpenAI API key is required for live realtime session mode (set AGENT_MODEL__OPENAI__API_KEY, pass --api-key, or configure model.openai.api_key in %s)", config.ConfigFileName)
	}
	if strings.TrimSpace(active.Model) == "" {
		return config.OpenAIConfig{}, fmt.Errorf("OpenAI realtime model is required for live session mode (set AGENT_MODEL__OPENAI__MODEL, pass --model, or configure model.openai.model in %s)", config.ConfigFileName)
	}
	if !isOpenAIRealtimeModel(active.Model) {
		return config.OpenAIConfig{}, fmt.Errorf("OpenAI model %q is not realtime-capable for agent session; use %q or a supported realtime model alias", active.Model, openAIRealtimeModel)
	}
	return *active, nil
}

func isOpenAIRealtimeModel(model string) bool {
	return strings.EqualFold(strings.TrimSpace(model), openAIRealtimeModel)
}

// NewGrokSessionInferencer builds the session-capable Grok realtime inferencer.
func NewGrokSessionInferencer(sessionCfg config.GrokConfig) (messages.SessionInferencer, error) {
	return NewGrokSessionInferencerWithOptions(sessionCfg)
}

// NewGrokSessionInferencerWithOptions builds the session-capable Grok realtime inferencer.
func NewGrokSessionInferencerWithOptions(sessionCfg config.GrokConfig, opts ...grok.Option) (messages.SessionInferencer, error) {
	providerOpts := []grok.Option{grok.WithAPIKey(sessionCfg.APIKey)}
	if strings.TrimSpace(sessionCfg.BaseURL) != "" {
		providerOpts = append(providerOpts, grok.WithBaseURL(sessionCfg.BaseURL))
	}
	providerOpts = append(providerOpts, opts...)
	sessionGateway, err := gateway.NewSessionGateway(gateway.WithSessionProvider(grok.New(providerOpts...)))
	if err != nil {
		return nil, fmt.Errorf("create Grok session gateway: %w", err)
	}
	return inference.NewSessionGatewayInferencer(
		sessionGateway,
		inference.WithSessionModel(sessionCfg.Model),
	), nil
}

// NewOpenAIRealtimeSessionInferencer builds the session-capable OpenAI realtime inferencer.
func NewOpenAIRealtimeSessionInferencer(sessionCfg config.OpenAIConfig) (messages.SessionInferencer, error) {
	return NewOpenAIRealtimeSessionInferencerWithOptions(sessionCfg)
}

// NewOpenAIRealtimeSessionInferencerWithOptions builds the OpenAI realtime inferencer.
func NewOpenAIRealtimeSessionInferencerWithOptions(sessionCfg config.OpenAIConfig, opts ...oaiprovider.Option) (messages.SessionInferencer, error) {
	if !isOpenAIRealtimeModel(sessionCfg.Model) {
		return nil, fmt.Errorf("OpenAI model %q is not realtime-capable for agent session; use %q or a supported realtime model alias", sessionCfg.Model, openAIRealtimeModel)
	}
	providerOpts := []oaiprovider.Option{
		oaiprovider.WithAPIKey(sessionCfg.APIKey),
		oaiprovider.WithModel(sessionCfg.Model),
		oaiprovider.WithRealtimeBaseURL(openAIRealtimeURL(sessionCfg)),
	}
	providerOpts = append(providerOpts, opts...)
	sessionGateway, err := gateway.NewSessionGateway(gateway.WithSessionProvider(oaiprovider.New(providerOpts...)))
	if err != nil {
		return nil, fmt.Errorf("create OpenAI realtime session gateway: %w", err)
	}
	return inference.NewSessionGatewayInferencer(
		sessionGateway,
		inference.WithSessionModel(sessionCfg.Model),
	), nil
}

func openAIRealtimeURL(sessionCfg config.OpenAIConfig) string {
	base := strings.TrimSpace(sessionCfg.BaseURL)
	if base == "" {
		base = openAIRealtimeBaseURL
	}
	parsed, err := url.Parse(base)
	if err != nil {
		return base
	}
	query := parsed.Query()
	if query.Get("model") == "" {
		query.Set("model", sessionCfg.Model)
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

type openAIWebSocketDialerAdapter struct {
	inner grok.WebSocketDialer
}

var _ oaiprovider.WebSocketDialer = (*openAIWebSocketDialerAdapter)(nil)

func newOpenAIWebSocketDialerAdapter(inner grok.WebSocketDialer) *openAIWebSocketDialerAdapter {
	return &openAIWebSocketDialerAdapter{inner: inner}
}

func (d *openAIWebSocketDialerAdapter) Dial(url string, headers map[string]string) (oaiprovider.WebSocketConn, error) {
	conn, err := d.inner.Dial(url, headers)
	if err != nil {
		return nil, err
	}
	return &openAIWebSocketConnAdapter{inner: conn}, nil
}

type openAIWebSocketConnAdapter struct {
	inner grok.WebSocketConn
}

var _ oaiprovider.WebSocketConn = (*openAIWebSocketConnAdapter)(nil)

func (c *openAIWebSocketConnAdapter) ReadMessage() (int, []byte, error) {
	return c.inner.ReadMessage()
}

func (c *openAIWebSocketConnAdapter) WriteMessage(messageType int, data []byte) error {
	return c.inner.WriteMessage(messageType, data)
}

func (c *openAIWebSocketConnAdapter) Close() error {
	return c.inner.Close()
}
