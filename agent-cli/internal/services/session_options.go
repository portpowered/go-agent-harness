// This file contains session option types, validation, configuration resolution, and provider construction for the session command.
package services

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/metrics"
	platformclock "github.com/portpowered/go-agent-harness/go-agent-loop/pkg/platform/clock"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/gateway"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/inference"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/grok"
	oaiprovider "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openai"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

const (
	sessionProviderGrok   = config.ProviderGrok
	sessionProviderOpenAI = config.ProviderOpenAI
	openAIRealtimeModel   = openAIRealtimeDefaultModel
	openAIRealtimeBaseURL = "wss://api.openai.com/v1/realtime"
)

// SessionRunOptions contains the user-facing agent session command options.
type SessionRunOptions struct {
	RecordPath        string
	ReplayPath        string
	Provider          string
	Model             string
	ModelProvided     bool
	APIKey            string
	BaseURL           string
	ConfigDir         string
	Prompt            string
	SessionInferencer messages.SessionInferencer
	WebSocketDialer   transport.Dialer

	// ToolExecutor optionally injects the composed session tool executor.
	// When nil, duplex loop construction stays byte-for-byte identical to the
	// no-tools behavior; provider tool calls keep reaching the loop default
	// and fail exactly as they did before this field existed.
	ToolExecutor messages.ToolExecutor
	// ToolDefinitions is the config-filtered tool surface advertised to the
	// session provider and the duplex agent loop. It must be derived from the
	// same config snapshot as ToolExecutor.
	ToolDefinitions []messages.ToolDefinition
	// LoadedConfig is the config snapshot used to derive session capabilities.
	// When present, provider resolution reuses it instead of loading config a
	// second time during runtime planning.
	LoadedConfig *config.Config

	// ToolExecutionTimeout overrides the per-invocation session tool adapter
	// deadline for hermetic tests. Zero selects defaultSessionToolExecutionTimeout;
	// production plans never set it, so live behavior is unchanged.
	ToolExecutionTimeout time.Duration

	// Clock stamps runtime observations. A nil clock uses the host clock. The
	// generated CLI supplies the composed clock so replay and recording
	// observers can correlate events across command instances.
	Clock platformclock.Source
	// RuntimeObserver receives clock-stamped audio, turn, and terminal events
	// from the session command. Nil keeps the runtime observationally silent.
	RuntimeObserver SessionRuntimeObserver

	// Diagnostics optionally receives one canonical structured record per
	// terminal failure plus per-turn and tool-call records. Nil keeps runtime
	// behavior byte-for-byte unchanged.
	Diagnostics SessionDiagnosticSink
	// MetricsRecorder optionally receives per-direction stream observations.
	MetricsRecorder metrics.Recorder
	// StreamObserver optionally receives every session stream delta after it
	// crosses the session loop boundary. Nil keeps runtime behavior unchanged.
	StreamObserver SessionStreamObserver
	// AudioInputs schedules user audio injections through the loop's existing
	// audio-input seam, attributed to specific turns.
	AudioInputs []ScheduledAudioInput
	// WaitForClose keeps the replay session loop running across multiple
	// completed turns until an explicit SESSION.CLOSE arrives instead of
	// stopping at the first completed turn. Defaults to false, which preserves
	// the existing single-turn stop behavior byte-for-byte.
	WaitForClose bool
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
	if opts.LoadedConfig != nil {
		return opts.LoadedConfig.Model.Provider
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
	loadedCfg := opts.LoadedConfig
	if loadedCfg == nil {
		storage, err := config.NewDefaultConfigStorage(opts.ConfigDir)
		if err != nil {
			return config.GrokConfig{}, fmt.Errorf("failed to initialize config: %w", err)
		}
		loadedCfg, err = storage.Load()
		if err != nil {
			return config.GrokConfig{}, fmt.Errorf("failed to load config: %w", err)
		}
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
	if opts.ModelProvided && opts.Model == "" {
		return config.OpenAIConfig{}, unsupportedOpenAIRealtimeModelError(opts.Model)
	}

	loadedCfg := opts.LoadedConfig
	if loadedCfg == nil {
		storage, err := config.NewDefaultConfigStorage(opts.ConfigDir)
		if err != nil {
			return config.OpenAIConfig{}, fmt.Errorf("failed to initialize config: %w", err)
		}
		loadedCfg, err = storage.Load()
		if err != nil {
			return config.OpenAIConfig{}, fmt.Errorf("failed to load config: %w", err)
		}
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
	if !opts.ModelProvided && opts.Model == "" && loadedCfg.Model.OpenAI == nil {
		active.Model = openAIRealtimeModel
	}
	if strings.TrimSpace(active.APIKey) == "" {
		return config.OpenAIConfig{}, fmt.Errorf("OpenAI API key is required for live realtime session mode (set AGENT_MODEL__OPENAI__API_KEY, pass --api-key, or configure model.openai.api_key in %s)", config.ConfigFileName)
	}
	if strings.TrimSpace(active.Model) == "" {
		if active.Model == "" && !opts.ModelProvided && opts.Model == "" {
			active.Model = openAIRealtimeModel
		} else {
			return config.OpenAIConfig{}, unsupportedOpenAIRealtimeModelError(active.Model)
		}
	}
	if !isOpenAIRealtimeModel(active.Model) {
		return config.OpenAIConfig{}, unsupportedOpenAIRealtimeModelError(active.Model)
	}
	return *active, nil
}

func isOpenAIRealtimeModel(model string) bool {
	_, ok := LookupOpenAIRealtimeModel(model)
	return ok
}

// NewGrokSessionInferencer builds the session-capable Grok realtime inferencer.
func NewGrokSessionInferencer(sessionCfg config.GrokConfig) (messages.SessionInferencer, error) {
	return NewGrokSessionInferencerWithOptions(sessionCfg)
}

// NewGrokSessionInferencerWithOptions builds the session-capable Grok realtime inferencer.
func NewGrokSessionInferencerWithOptions(sessionCfg config.GrokConfig, opts ...grok.Option) (messages.SessionInferencer, error) {
	return NewGrokSessionInferencerWithToolsAndOptions(sessionCfg, nil, opts...)
}

// NewGrokSessionInferencerWithToolsAndOptions builds a Grok realtime
// inferencer with the selected tool definitions in its initial session
// configuration.
func NewGrokSessionInferencerWithToolsAndOptions(sessionCfg config.GrokConfig, toolDefinitions []messages.ToolDefinition, opts ...grok.Option) (messages.SessionInferencer, error) {
	providerOpts := []grok.Option{grok.WithAPIKey(sessionCfg.APIKey)}
	if strings.TrimSpace(sessionCfg.BaseURL) != "" {
		providerOpts = append(providerOpts, grok.WithBaseURL(sessionCfg.BaseURL))
	}
	providerOpts = append(providerOpts, opts...)
	sessionGateway, err := gateway.NewSessionGateway(gateway.WithSessionProvider(grok.New(providerOpts...)))
	if err != nil {
		return nil, fmt.Errorf("create Grok session gateway: %w", err)
	}
	inferenceOpts := []inference.SessionOption{inference.WithSessionModel(sessionCfg.Model)}
	if len(toolDefinitions) > 0 {
		inferenceOpts = append(inferenceOpts, inference.WithSessionTools(toolDefinitions))
	}
	return inference.NewSessionGatewayInferencer(sessionGateway, inferenceOpts...), nil
}

// NewOpenAIRealtimeSessionInferencer builds the session-capable OpenAI realtime inferencer.
func NewOpenAIRealtimeSessionInferencer(sessionCfg config.OpenAIConfig) (messages.SessionInferencer, error) {
	return NewOpenAIRealtimeSessionInferencerWithOptions(sessionCfg)
}

// NewOpenAIRealtimeSessionInferencerWithOptions builds the OpenAI realtime inferencer.
func NewOpenAIRealtimeSessionInferencerWithOptions(sessionCfg config.OpenAIConfig, opts ...oaiprovider.Option) (messages.SessionInferencer, error) {
	return NewOpenAIRealtimeSessionInferencerWithToolsAndOptions(sessionCfg, nil, opts...)
}

// NewOpenAIRealtimeSessionInferencerWithToolsAndOptions builds an OpenAI
// realtime inferencer with the selected tool definitions in its initial
// session configuration.
func NewOpenAIRealtimeSessionInferencerWithToolsAndOptions(sessionCfg config.OpenAIConfig, toolDefinitions []messages.ToolDefinition, opts ...oaiprovider.Option) (messages.SessionInferencer, error) {
	if !isOpenAIRealtimeModel(sessionCfg.Model) {
		return nil, unsupportedOpenAIRealtimeModelError(sessionCfg.Model)
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
	inferenceOpts := []inference.SessionOption{inference.WithSessionModel(sessionCfg.Model)}
	if len(toolDefinitions) > 0 {
		inferenceOpts = append(inferenceOpts, inference.WithSessionTools(toolDefinitions))
	}
	return inference.NewSessionGatewayInferencer(sessionGateway, inferenceOpts...), nil
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
