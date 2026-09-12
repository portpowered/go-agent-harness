// This file contains session option types, validation, configuration resolution, and provider construction for the session command.
package agentruntime

import rtcontract "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentruntime/transports"

import sessioncontract "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/metrics"
	runtimeproviders "github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionconfig"
	sessionconfigwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionconfig/wire"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/observability"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/gateway"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/inference"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/grok"
	oaiprovider "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openai"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

const (
	sessionProviderGrok   = config.ProviderGrok
	sessionProviderOpenAI = config.ProviderOpenAI
	openAIRealtimeModel   = openAIRealtimeDefaultModel
	openAIRealtimeBaseURL = sessionconfig.OpenAIRealtimeBaseURL

	// SessionTransportWebSocket is the unchanged session transport default.
	SessionTransportWebSocket = "ws"
	// SessionTransportWebRTC selects the service-owned WebRTC runtime.
	SessionTransportWebRTC = "webrtc"
	// SessionOpenAIAPIKeyEnv is the canonical environment key used by the
	// shared config loader for OpenAI realtime sessions.
	SessionOpenAIAPIKeyEnv = sessionconfig.OpenAIRealtimeAPIKeyEnv
)

var (
	// ErrSessionAudioInTurnBargeRequiresSequence identifies an opt-in barge-in
	// request that does not provide the repeated-turn sequence it controls.
	ErrSessionAudioInTurnBargeRequiresSequence = sessioncontract.ErrSessionAudioInTurnBargeRequiresSequence
	// ErrInvalidSessionRuntimeSelection identifies a malformed or incompatible
	// transport/signaling/media selection at the service boundary.
	ErrInvalidSessionRuntimeSelection = sessionconfig.ErrInvalidSessionRuntimeSelection
	// ErrInvalidSessionTransport identifies a transport value the service does
	// not know how to dispatch.
	ErrInvalidSessionTransport = sessionconfig.ErrInvalidSessionTransport
	// ErrSessionSignalingRequiresWebRTC identifies signaling supplied for the
	// unchanged WebSocket runtime.
	ErrSessionSignalingRequiresWebRTC = sessionconfig.ErrSessionSignalingRequiresWebRTC
	// ErrSessionMediaSourceRequiresWebRTC identifies a media source supplied for
	// the unchanged WebSocket runtime.
	ErrSessionMediaSourceRequiresWebRTC = sessionconfig.ErrSessionMediaSourceRequiresWebRTC
	// ErrSessionWebRTCRequiresSignaling identifies a WebRTC request without an
	// endpoint for the signaling exchange.
	ErrSessionWebRTCRequiresSignaling = sessionconfig.ErrSessionWebRTCRequiresSignaling
	// ErrSessionWebRTCRequiresMediaSource identifies a WebRTC request without a
	// selected external media source.
	ErrSessionWebRTCRequiresMediaSource = sessionconfig.ErrSessionWebRTCRequiresMediaSource
	// ErrSessionRuntimeSelectionConflict identifies two aliases carrying
	// different signaling endpoint values.
	ErrSessionRuntimeSelectionConflict = sessionconfig.ErrSessionRuntimeSelectionConflict
	// ErrOpenAIRealtimeAPIKeyMissing classifies the preflight error returned
	// when an OpenAI realtime session has no credential. Callers that do not
	// expose an --api-key flag (for example `room run`) should catch this
	// with errors.Is and substitute a remedy their command actually accepts
	// instead of surfacing the --api-key wording below.
	ErrOpenAIRealtimeAPIKeyMissing = sessionconfig.ErrOpenAIRealtimeAPIKeyMissing
)

type SessionRuntimeSelection = rtcontract.SessionRuntimeSelection

type SessionAudioInTurnBargeError = sessioncontract.SessionAudioInTurnBargeError

// ScheduledAudioDispatchPolicy is the explicit policy selected for a finite
// repeated audio-turn sequence. The zero value is intentionally not a policy;
// planning always normalizes it to completion-gated behavior.
type ScheduledAudioDispatchPolicy string

const (
	// ScheduledAudioDispatchCompletionGated preserves ordinary serialized
	// --audio-in-turn behavior.
	ScheduledAudioDispatchCompletionGated ScheduledAudioDispatchPolicy = "completion-gated"
	// ScheduledAudioDispatchActiveResponse releases later scheduled turns when
	// their identified prior response is active and non-terminal.
	ScheduledAudioDispatchActiveResponse ScheduledAudioDispatchPolicy = "active-response"
)

func scheduledAudioDispatchPolicyForOptions(opts SessionRunOptions) ScheduledAudioDispatchPolicy {
	if opts.AudioInTurnBarge {
		return ScheduledAudioDispatchActiveResponse
	}
	return ScheduledAudioDispatchCompletionGated
}

// SessionRuntimeSelectionError reports all fields that made a selection
// invalid while preserving a stable sentinel and the more specific cause.
// Fields contain service option names without the leading command-line dashes.
type SessionRuntimeSelectionError = sessionconfig.RuntimeSelectionError

// SessionRunOptions contains the user-facing agent session command options.
type SessionRunOptions struct {
	// Composition-owned runtime and provider capability dependencies.
	runtimeFactory                sessionRuntimeFactory
	ModelCatalog                  runtimeproviders.ModelCatalog
	RecordPath                    string
	ReplayPath                    string
	ReplayTiming                  string
	roomReplay                    bool
	Provider                      string
	ProviderProvided              bool
	Model                         string
	ModelProvided                 bool
	NoInputTranscription          bool
	InputAudioTranscription       *models.InputAudioTranscriptionConfig
	APIKey                        string
	BaseURL                       string
	ConfigDir                     string
	WorkDir                       string
	AllowPaths                    []string
	FilesystemPolicy              *tools.FilesystemPolicy
	Prompt                        string
	PromptProvided                bool
	Voice                         string
	ReasoningEffort               string
	SessionInferencer             messages.SessionInferencer
	AudioOutputRequested          bool
	WebSocketDialer               transport.Dialer
	RecordSessionCapturePath      string
	RTCRuntimeFactory             SessionRTCRuntimeFactory
	Transport                     string
	TransportProvided             bool
	BareLive                      bool
	TurnDetection                 *models.TurnDetectionConfig
	Signaling                     string
	SignalingEndpoint             string
	MediaSource                   string
	RTCDeviceBinding              RTCDeviceBindingRequest
	ToolExecutor                  messages.ToolExecutor
	ToolDefinitions               []messages.ToolDefinition
	ToolDefinitionBase            []messages.ToolDefinition
	RefreshToolDefinitions        func(context.Context) ([]messages.ToolDefinition, error)
	BrowserWatch                  func(context.Context) <-chan webmcp.BrokerEvent
	BrowserEventWatch             func(context.Context) <-chan webmcp.BrowserEvent
	BrowserCapabilityState        webmcp.BrowserCapabilityState
	BrowserToolsEnabled           bool
	BrowserToolsInteractive       bool
	LoadedConfig                  *config.Config
	InteractiveToolPolicy         *InteractiveToolPolicy
	CapabilityClose               func() error
	capabilityCoordinator         SessionCapabilityCoordinator
	CancellationIntent            *SessionCancellationIntent
	ToolExecutionTimeout          time.Duration
	Clock                         platformclock.Source
	LivenessClock                 SessionLivenessClock
	RuntimeObserver               SessionRuntimeObserver
	Diagnostics                   SessionDiagnosticSink
	ToolDiagnostics               SessionToolDiagnosticSink
	MetricsRecorder               metrics.Recorder
	Observability                 observability.Dependencies
	StreamObserver                SessionStreamObserver
	AudioInputs                   []ScheduledAudioInput
	AudioInTurnBarge              bool
	AudioInterruptions            <-chan ScheduledAudioInput
	ClientOwnsAudioTurnBoundaries bool
	SessionUpdatedTimeout         time.Duration
	WaitForClose                  bool
	sessionImageCapabilities      *SessionImageCapabilities
	recordingClaim                *sessionRecordingClaim
	recordingDirectoryClaim       *sessionRecordingDirectoryClaim
}

func newSessionConfigService(opts SessionRunOptions) sessionconfig.Service {
	return sessionconfigwire.NewService(sessionconfigwire.Dependencies{ModelCatalog: opts.ModelCatalog})
}

func sessionConfigRequest(opts SessionRunOptions, defaults sessionconfig.Defaults) sessionconfig.Request {
	return sessionconfig.Request{
		Provider:          opts.Provider,
		ProviderProvided:  opts.ProviderProvided,
		Model:             opts.Model,
		ModelProvided:     opts.ModelProvided,
		APIKey:            opts.APIKey,
		BaseURL:           opts.BaseURL,
		ReasoningEffort:   opts.ReasoningEffort,
		Voice:             opts.Voice,
		RecordPath:        opts.RecordPath,
		ReplayPath:        opts.ReplayPath,
		ReplayTiming:      opts.ReplayTiming,
		SessionInferencer: opts.SessionInferencer,
		Transport:         opts.Transport,
		Signaling:         opts.Signaling,
		SignalingEndpoint: opts.SignalingEndpoint,
		MediaSource:       opts.MediaSource,
		Defaults:          defaults,
	}
}

func sessionConfigDefaults(cfg *config.Config) sessionconfig.Defaults {
	var defaults sessionconfig.Defaults
	if cfg == nil {
		return defaults
	}
	defaults.Provider = cfg.Model.Provider
	if cfg.Session != nil {
		defaults.Session = &sessionconfig.SessionDefaults{
			Provider:        cfg.Session.Provider,
			Model:           cfg.Session.Model,
			ReasoningEffort: cfg.Session.ReasoningEffort,
			Transport:       cfg.Session.Transport,
		}
	}
	if cfg.Model.OpenAI != nil {
		defaults.OpenAI = &sessionconfig.ProviderConfig{
			Model:           cfg.Model.OpenAI.Model,
			APIKey:          cfg.Model.OpenAI.APIKey,
			BaseURL:         cfg.Model.OpenAI.BaseURL,
			ReasoningEffort: cfg.Model.OpenAI.ReasoningEffort,
		}
	}
	if cfg.Model.Grok != nil {
		defaults.Grok = &sessionconfig.ProviderConfig{
			Model:   cfg.Model.Grok.Model,
			APIKey:  cfg.Model.Grok.APIKey,
			BaseURL: cfg.Model.Grok.BaseURL,
		}
	}
	return defaults
}

func loadSessionConfigForResolution(opts SessionRunOptions) (*config.Config, error) {
	if opts.LoadedConfig != nil {
		return opts.LoadedConfig, nil
	}
	storage, err := config.NewDefaultConfigStorage(opts.ConfigDir)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize config: %w", err)
	}
	loadedCfg, err := storage.Load()
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}
	return loadedCfg, nil
}

// Deprecated: callers should use the public sessionconfig service; this keeps
// the CLI's early capture-only preflight ordering intact.
func validateSessionCaptureOptions(opts SessionRunOptions) error {
	return newSessionConfigService(opts).ValidateCapture(sessionConfigRequest(opts, sessionconfig.Defaults{}))
}

// Deprecated: the reusable policy lives in sessionconfig; this is the CLI
// compatibility adapter that preserves the historical voice error identity.
func validateSessionRunOptions(opts SessionRunOptions) error {
	return adaptSessionConfigError(newSessionConfigService(opts).Validate(sessionConfigRequest(opts, sessionconfig.Defaults{})))
}

func adaptSessionConfigError(err error) error {
	var voiceErr *sessionconfig.InvalidOpenAIRealtimeVoiceError
	if !errors.As(err, &voiceErr) {
		return err
	}
	return &sessioncontract.InvalidOpenAIRealtimeVoiceError{
		Voice:           voiceErr.Voice,
		SupportedVoices: append([]string(nil), voiceErr.SupportedVoices...),
	}
}

const (
	sessionReplayTimingImmediate = sessionconfig.ReplayTimingImmediate
	sessionReplayTimingRecorded  = sessionconfig.ReplayTimingRecorded
)

// Deprecated: replay timing normalization is owned by sessionconfig.Service.
func normalizedSessionReplayTiming(value string) string {
	return newSessionConfigService(SessionRunOptions{}).NormalizeReplayTiming(value)
}

// Deprecated: runtime planners still consume the legacy transport value type;
// selection decisions are made by sessionconfig.Service.
func resolveSessionRuntimeSelection(opts SessionRunOptions) (SessionRuntimeSelection, error) {
	selection, err := newSessionConfigService(opts).ResolveRuntimeSelection(sessionConfigRequest(opts, sessionconfig.Defaults{}))
	if err != nil {
		return SessionRuntimeSelection{}, err
	}
	return SessionRuntimeSelection{
		Transport:         selection.Transport,
		SignalingEndpoint: selection.SignalingEndpoint,
		MediaSource:       selection.MediaSource,
	}, nil
}

func validateInjectedLiveSession(opts SessionRunOptions) error {
	provider := effectiveSessionProvider(opts)
	opts.Provider = provider
	if provider == "" {
		return missingSessionProviderError()
	}

	switch provider {
	case sessionProviderOpenAI:
		_, err := resolveOpenAIRealtimeSessionConfig(opts)
		return err
	case sessionProviderGrok:
		_, err := resolveGrokSessionConfig(opts)
		return err
	default:
		return unsupportedRealtimeSessionProviderError(provider)
	}
}

func unsupportedRealtimeSessionProviderError(provider string) error {
	return fmt.Errorf("unsupported realtime session provider %q; supported providers are %q and %q", provider, sessionProviderOpenAI, sessionProviderGrok)
}

func missingSessionProviderError() error {
	return fmt.Errorf("--record requires --provider %s or --provider %s for live session inference", sessionProviderGrok, sessionProviderOpenAI)
}

// Deprecated: this edge adapter loads the host config snapshot and delegates
// provider selection to sessionconfig.Service.
func effectiveSessionProvider(opts SessionRunOptions) string {
	if strings.TrimSpace(opts.Provider) != "" {
		return resolveRealtimeSessionProvider(opts, nil)
	}
	if opts.LoadedConfig != nil {
		return resolveRealtimeSessionProvider(opts, opts.LoadedConfig)
	}
	storage, err := config.NewDefaultConfigStorage(opts.ConfigDir)
	if err != nil {
		return resolveRealtimeSessionProvider(opts, nil)
	}
	loadedCfg, err := storage.Load()
	if err != nil {
		return resolveRealtimeSessionProvider(opts, nil)
	}
	return resolveRealtimeSessionProvider(opts, loadedCfg)
}

// resolveRealtimeSessionProvider applies the one provider policy shared by
// every live session path. A command-line provider and the session-specific
// persisted provider are explicit choices; the ordinary model provider is
// used only when it is itself realtime-capable. One-shot defaults such as
// openrouter therefore fall through to OpenAI without changing ask or chat.
// Provider values are normalized at this boundary so downstream config
// overrides and runtime planners see the same canonical identity.
//
// Deprecated: provider selection is owned by sessionconfig.Service.
func resolveRealtimeSessionProvider(opts SessionRunOptions, cfg *config.Config) string {
	return newSessionConfigService(opts).ResolveProvider(sessionConfigRequest(opts, sessionConfigDefaults(cfg)))
}

// Deprecated: this converts the public provider snapshot to the CLI config
// type required by the concrete Grok gateway constructor.
func resolveGrokSessionConfig(opts SessionRunOptions) (config.GrokConfig, error) {
	loadedCfg, err := loadSessionConfigForResolution(opts)
	if err != nil {
		return config.GrokConfig{}, err
	}
	resolved, err := newSessionConfigService(opts).ResolveGrokConfig(sessionConfigRequest(opts, sessionConfigDefaults(loadedCfg)))
	if err != nil {
		return config.GrokConfig{}, err
	}
	return config.GrokConfig{Model: resolved.Model, APIKey: resolved.APIKey, BaseURL: resolved.BaseURL}, nil
}

// Deprecated: this converts the public provider snapshot to the CLI config
// type required by the concrete OpenAI gateway constructor.
func resolveOpenAIRealtimeSessionConfig(opts SessionRunOptions) (config.OpenAIConfig, error) {
	loadedCfg, err := loadSessionConfigForResolution(opts)
	if err != nil {
		return config.OpenAIConfig{}, err
	}
	resolved, err := newSessionConfigService(opts).ResolveOpenAIRealtimeConfig(sessionConfigRequest(opts, sessionConfigDefaults(loadedCfg)))
	if err != nil {
		return config.OpenAIConfig{}, err
	}
	return config.OpenAIConfig{Model: resolved.Model, APIKey: resolved.APIKey, BaseURL: resolved.BaseURL, ReasoningEffort: resolved.ReasoningEffort}, nil
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
	return newOpenAIRealtimeSessionInferencerWithVoiceAndToolsAndOptions(sessionCfg, "", toolDefinitions, opts...)
}

func newOpenAIRealtimeSessionInferencerWithVoiceAndToolsAndOptions(sessionCfg config.OpenAIConfig, voice string, toolDefinitions []messages.ToolDefinition, opts ...oaiprovider.Option) (messages.SessionInferencer, error) {
	return newOpenAIRealtimeSessionInferencerWithVoiceAndToolsAndInputAudioTranscriptionAndOptions(sessionCfg, voice, toolDefinitions, models.InputAudioTranscriptionConfig{
		Enabled: true,
		Model:   models.DefaultInputAudioTranscriptionModel,
	}, opts...)
}

func newOpenAIRealtimeSessionInferencerWithVoiceAndToolsAndInputAudioTranscriptionAndOptions(sessionCfg config.OpenAIConfig, voice string, toolDefinitions []messages.ToolDefinition, inputAudioTranscription models.InputAudioTranscriptionConfig, opts ...oaiprovider.Option) (messages.SessionInferencer, error) {
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
	inferenceOpts = append(inferenceOpts, inference.WithSessionInputAudioTranscription(inputAudioTranscription))
	if voice != "" {
		inferenceOpts = append(inferenceOpts, inference.WithSessionVoice(voice))
	}
	if len(toolDefinitions) > 0 {
		inferenceOpts = append(inferenceOpts, inference.WithSessionTools(toolDefinitions))
	}
	return inference.NewSessionGatewayInferencer(sessionGateway, inferenceOpts...), nil
}

// SessionInferencerCaptureFlusher is implemented by the inferencer
// NewLiveSessionInferencer returns when SessionRunOptions.RecordSessionCapturePath
// is set: its live websocket traffic is being recorded, and FlushCapture
// persists everything captured so far to that path. A caller should call
// FlushCapture once the session this inferencer produced has fully closed,
// so the persisted capture reflects the complete exchange rather than a
// still-in-progress one.
type SessionInferencerCaptureFlusher interface {
	FlushCapture() error
}

// sessionInferencerWithCaptureFlush adapts a *gwtesting.RecordingWebSocketDialer
// (which records raw websocket traffic, not messages.SessionInferencer calls)
// into the SessionInferencerCaptureFlusher a caller can type-assert for
// without depending on the concrete recorder type.
type sessionInferencerWithCaptureFlush struct {
	messages.SessionInferencer
	path     string
	recorder *gwtesting.RecordingWebSocketDialer
}

func (w *sessionInferencerWithCaptureFlush) FlushCapture() error {
	return w.recorder.FlushToFile(w.path)
}

// resolveSessionWebSocketDialer picks the dialer NewLiveSessionInferencer's
// provider construction should use: the caller-injected dialer (or the
// provider's real default when none was injected), optionally wrapped with a
// raw-traffic recorder when SessionRunOptions.RecordSessionCapturePath is
// set. The returned recorder is nil unless recording was requested.
func resolveSessionWebSocketDialer(opts SessionRunOptions, providerName, model string, newDefaultDialer func() transport.Dialer) (transport.Dialer, *gwtesting.RecordingWebSocketDialer) {
	dialer := opts.WebSocketDialer
	if dialer == nil {
		dialer = newDefaultDialer()
	}
	if strings.TrimSpace(opts.RecordSessionCapturePath) == "" {
		return dialer, nil
	}
	recorder := gwtesting.NewRecordingWebSocketDialer(dialer, providerName, model)
	return recorder, recorder
}

// wrapSessionInferencerCaptureFlush leaves inferencer unchanged when recorder
// is nil (recording was not requested), and otherwise wraps it so a caller
// can type-assert for SessionInferencerCaptureFlusher and flush the capture
// once the session this inferencer produced has closed.
func wrapSessionInferencerCaptureFlush(inferencer messages.SessionInferencer, recorder *gwtesting.RecordingWebSocketDialer, path string) messages.SessionInferencer {
	if recorder == nil {
		return inferencer
	}
	return &sessionInferencerWithCaptureFlush{SessionInferencer: inferencer, path: path, recorder: recorder}
}

// NewLiveSessionInferencer builds the audio-capable realtime session used by
// device-tier probes. Unlike the ordinary session constructors, this helper
// supplies the provider's audio formats and rates in the initial request so a
// device bridge can send and receive PCM without relying on a later control
// message to change the wire contract.
func NewLiveSessionInferencer(opts SessionRunOptions, instructions string) (messages.SessionInferencer, string, error) {
	providerName := strings.ToLower(strings.TrimSpace(effectiveSessionProvider(opts)))
	if providerName == "" {
		return nil, "", fmt.Errorf("--devices real requires a realtime session provider; pass --provider openai or --provider grok")
	}
	opts.Provider = providerName
	instructions = composeSessionInstructions(opts, instructions)

	var (
		model  string
		config models.SessionConfig
	)
	switch providerName {
	case sessionProviderOpenAI:
		sessionCfg, err := resolveOpenAIRealtimeSessionConfig(opts)
		if err != nil {
			return nil, "", err
		}
		model = sessionCfg.Model
		config = deviceProbeSessionConfig(model, instructions, models.AudioFormatPCM16, models.AudioFormatPCM16)
		inputAudioTranscription := resolveInputAudioTranscriptionPolicy(opts, providerName, true)
		if opts.InputAudioTranscription != nil {
			inputAudioTranscription = *opts.InputAudioTranscription
		}
		config.InputAudioTranscription = &inputAudioTranscription
		config.TurnDetection = cloneSessionTurnDetection(opts.TurnDetection)
		config.Voice = opts.Voice
		config.ReasoningEffort = sessionCfg.ReasoningEffort
		config.Tools = append([]messages.ToolDefinition(nil), opts.ToolDefinitions...)
		dialer, recorder := resolveSessionWebSocketDialer(opts, providerName, model, func() transport.Dialer { return oaiprovider.NewDefaultWebSocketDialer() })
		providerOpts := []oaiprovider.Option{
			oaiprovider.WithAPIKey(sessionCfg.APIKey),
			oaiprovider.WithModel(sessionCfg.Model),
			oaiprovider.WithRealtimeBaseURL(openAIRealtimeURL(sessionCfg)),
			oaiprovider.WithWebSocketDialer(dialer),
		}
		providerGateway, err := gateway.NewSessionGateway(gateway.WithSessionProvider(oaiprovider.New(providerOpts...)))
		if err != nil {
			return nil, "", fmt.Errorf("create OpenAI realtime session gateway: %w", err)
		}
		inferencer := inference.NewSessionGatewayInferencer(providerGateway, inference.WithSessionRequest(inference.SessionRequest{Config: config}))
		return wrapSessionInferencerCaptureFlush(inferencer, recorder, opts.RecordSessionCapturePath), model, nil
	case sessionProviderGrok:
		sessionCfg, err := resolveGrokSessionConfig(opts)
		if err != nil {
			return nil, "", err
		}
		model = sessionCfg.Model
		config = deviceProbeSessionConfig(model, instructions, models.AudioFormatPCM16, models.AudioFormatPCM16)
		config.TurnDetection = cloneSessionTurnDetection(opts.TurnDetection)
		inputAudioTranscription := resolveInputAudioTranscriptionPolicy(opts, providerName, true)
		if opts.InputAudioTranscription != nil {
			inputAudioTranscription = *opts.InputAudioTranscription
		}
		config.InputAudioTranscription = &inputAudioTranscription
		config.Tools = append([]messages.ToolDefinition(nil), opts.ToolDefinitions...)
		dialer, recorder := resolveSessionWebSocketDialer(opts, providerName, model, func() transport.Dialer { return grok.NewDefaultWebSocketDialer() })
		providerOpts := []grok.Option{grok.WithAPIKey(sessionCfg.APIKey), grok.WithWebSocketDialer(dialer)}
		if strings.TrimSpace(sessionCfg.BaseURL) != "" {
			providerOpts = append(providerOpts, grok.WithBaseURL(sessionCfg.BaseURL))
		}
		providerGateway, err := gateway.NewSessionGateway(gateway.WithSessionProvider(grok.New(providerOpts...)))
		if err != nil {
			return nil, "", fmt.Errorf("create Grok realtime session gateway: %w", err)
		}
		inferencer := inference.NewSessionGatewayInferencer(providerGateway, inference.WithSessionRequest(inference.SessionRequest{Config: config}))
		return wrapSessionInferencerCaptureFlush(inferencer, recorder, opts.RecordSessionCapturePath), model, nil
	default:
		return nil, "", fmt.Errorf("--devices real supports realtime providers %q and %q; got %q", sessionProviderOpenAI, sessionProviderGrok, providerName)
	}
}

// Deprecated: sessionconfig.Service owns request-scoped cloning.
func cloneSessionTurnDetection(policy *models.TurnDetectionConfig) *models.TurnDetectionConfig {
	return newSessionConfigService(SessionRunOptions{}).CloneTurnDetection(policy)
}

func deviceProbeSessionConfig(model, instructions string, input, output models.AudioFormat) models.SessionConfig {
	return models.SessionConfig{
		Model:                 model,
		Modalities:            []models.SessionModality{models.SessionModalityAudio},
		Instructions:          instructions,
		InputAudioFormat:      input,
		OutputAudioFormat:     output,
		InputAudioSampleRate:  models.SampleRate24000,
		OutputAudioSampleRate: models.SampleRate24000,
	}
}

// Deprecated: sessionconfig.Service owns endpoint normalization; this adapts
// the result to the provider constructor's config type.
func openAIRealtimeURL(sessionCfg config.OpenAIConfig) string {
	return newSessionConfigService(SessionRunOptions{}).OpenAIRealtimeURL(sessionconfig.ProviderConfig{Model: sessionCfg.Model, BaseURL: sessionCfg.BaseURL})
}
