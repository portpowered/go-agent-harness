// This file contains session option types, validation, configuration resolution, and provider construction for the session command.
package services

import (
	"errors"
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
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/grok"
	oaiprovider "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openai"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

const (
	sessionProviderGrok   = config.ProviderGrok
	sessionProviderOpenAI = config.ProviderOpenAI
	openAIRealtimeModel   = openAIRealtimeDefaultModel
	openAIRealtimeBaseURL = "wss://api.openai.com/v1/realtime"

	// SessionTransportWebSocket is the unchanged session transport default.
	SessionTransportWebSocket = "ws"
	// SessionTransportWebRTC selects the service-owned WebRTC runtime.
	SessionTransportWebRTC = "webrtc"
)

var (
	// ErrInvalidSessionRuntimeSelection identifies a malformed or incompatible
	// transport/signaling/media selection at the service boundary.
	ErrInvalidSessionRuntimeSelection = errors.New("invalid session runtime selection")
	// ErrInvalidSessionTransport identifies a transport value the service does
	// not know how to dispatch.
	ErrInvalidSessionTransport = errors.New("invalid session transport")
	// ErrSessionSignalingRequiresWebRTC identifies signaling supplied for the
	// unchanged WebSocket runtime.
	ErrSessionSignalingRequiresWebRTC = errors.New("session signaling requires WebRTC transport")
	// ErrSessionMediaSourceRequiresWebRTC identifies a media source supplied for
	// the unchanged WebSocket runtime.
	ErrSessionMediaSourceRequiresWebRTC = errors.New("session media source requires WebRTC transport")
	// ErrSessionWebRTCRequiresSignaling identifies a WebRTC request without an
	// endpoint for the signaling exchange.
	ErrSessionWebRTCRequiresSignaling = errors.New("WebRTC session transport requires signaling")
	// ErrSessionWebRTCRequiresMediaSource identifies a WebRTC request without a
	// selected external media source.
	ErrSessionWebRTCRequiresMediaSource = errors.New("WebRTC session transport requires media source")
	// ErrSessionRuntimeSelectionConflict identifies two aliases carrying
	// different signaling endpoint values.
	ErrSessionRuntimeSelectionConflict = errors.New("conflicting session signaling endpoints")
)

// SessionRuntimeSelection is the opaque, service-owned transport selection
// retained by a runtime plan. Endpoint and source values are intentionally
// strings: concrete signaling, peer, and media implementations stay behind
// the runtime factory and no protocol-specific type crosses the service API.
type SessionRuntimeSelection struct {
	Transport         string
	SignalingEndpoint string
	MediaSource       string
}

// SessionRuntimeSelectionError reports all fields that made a selection
// invalid while preserving a stable sentinel and the more specific cause.
// Fields contain service option names without the leading command-line dashes.
type SessionRuntimeSelectionError struct {
	Fields []string
	Err    error
}

func (e *SessionRuntimeSelectionError) Error() string {
	if e == nil {
		return ErrInvalidSessionRuntimeSelection.Error()
	}
	if len(e.Fields) == 0 {
		return fmt.Sprintf("%s: %v", ErrInvalidSessionRuntimeSelection, e.Err)
	}
	if e.Err == nil {
		return fmt.Sprintf("%s (%s)", ErrInvalidSessionRuntimeSelection, strings.Join(e.Fields, ", "))
	}
	return fmt.Sprintf("%s (%s): %v", ErrInvalidSessionRuntimeSelection, strings.Join(e.Fields, ", "), e.Err)
}

func (e *SessionRuntimeSelectionError) Unwrap() error {
	if e == nil {
		return nil
	}
	return errors.Join(ErrInvalidSessionRuntimeSelection, e.Err)
}

// SessionRunOptions contains the user-facing agent session command options.
type SessionRunOptions struct {
	RecordPath    string
	ReplayPath    string
	Provider      string
	Model         string
	ModelProvided bool
	APIKey        string
	BaseURL       string
	ConfigDir     string
	Prompt        string
	// Voice selects the optional OpenAI Realtime audio output voice for this
	// invocation. The empty value preserves the provider default. It is kept
	// on the session options rather than package state so concurrent sessions
	// retain independent configuration.
	Voice             string
	SessionInferencer messages.SessionInferencer
	WebSocketDialer   transport.Dialer
	// RTCRuntimeFactory optionally supplies the service-owned WebRTC runtime
	// constructor. A nil value keeps the WebSocket path unchanged; selecting
	// WebRTC without a factory returns an explicit setup error rather than
	// silently falling back to WebSocket.
	RTCRuntimeFactory SessionRTCRuntimeFactory

	// Transport selects the live session runtime. Empty preserves the existing
	// WebSocket default. The value is retained as supplied in the option
	// contract only long enough for case/space-insensitive validation; plans
	// store the canonical ws or webrtc value.
	Transport string
	// Signaling is the selected opaque signaling endpoint. It is consumed by
	// the WebRTC runtime only; it must remain empty for the WebSocket runtime.
	Signaling string
	// SignalingEndpoint is the descriptive alias used by non-CLI callers. When
	// both signaling fields are supplied they must carry the same value.
	SignalingEndpoint string
	// MediaSource is the selected opaque external media-source identity. It is
	// consumed by the WebRTC runtime only; it must remain empty for WebSocket.
	MediaSource string
	// RTCDeviceBinding carries optional registry-backed local audio selectors.
	// The runtime opens these devices only after planning succeeds and before
	// provider/peer setup begins.
	RTCDeviceBinding RTCDeviceBindingRequest

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
	// from the session command. The terminal event carries the production-owned
	// session-cumulative token totals and complete metrics snapshot. Nil keeps
	// the runtime observationally silent.
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
	// SessionUpdatedTimeout overrides the bounded wait for the initial
	// SESSION.UPDATED acknowledgement in deterministic callers. Zero selects
	// the production timeout.
	SessionUpdatedTimeout time.Duration
	// WaitForClose keeps the replay session loop running across multiple
	// completed turns until an explicit SESSION.CLOSE arrives instead of
	// stopping at the first completed turn. Defaults to false, which preserves
	// the existing single-turn stop behavior byte-for-byte.
	WaitForClose bool

	// sessionImageCapabilities is resolved once by the entry point that owns
	// an initial --image turn and reused when the read_image tool is bound.
	// Keeping it private prevents callers from bypassing the capability
	// resolver while allowing all session wrappers to share one snapshot.
	sessionImageCapabilities *SessionImageCapabilities
}

func validateSessionRunOptions(opts SessionRunOptions) error {
	if err := ValidateOpenAIRealtimeVoice(opts.Voice); err != nil {
		return err
	}
	if _, err := resolveSessionRuntimeSelection(opts); err != nil {
		return err
	}
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

func resolveSessionRuntimeSelection(opts SessionRunOptions) (SessionRuntimeSelection, error) {
	transportValue := strings.ToLower(strings.TrimSpace(opts.Transport))
	if transportValue == "" {
		transportValue = SessionTransportWebSocket
	}
	if transportValue != SessionTransportWebSocket && transportValue != SessionTransportWebRTC {
		return SessionRuntimeSelection{}, sessionRuntimeSelectionError(
			[]string{"transport"},
			fmt.Errorf("%w: %q (want %q or %q)", ErrInvalidSessionTransport, opts.Transport, SessionTransportWebSocket, SessionTransportWebRTC),
		)
	}

	signaling := opts.Signaling
	if opts.SignalingEndpoint != "" {
		if signaling != "" && signaling != opts.SignalingEndpoint {
			return SessionRuntimeSelection{}, sessionRuntimeSelectionError(
				[]string{"signaling", "signaling-endpoint"},
				ErrSessionRuntimeSelectionConflict,
			)
		}
		signaling = opts.SignalingEndpoint
	}
	selection := SessionRuntimeSelection{
		Transport:         transportValue,
		SignalingEndpoint: signaling,
		MediaSource:       opts.MediaSource,
	}

	var fields []string
	var causes []error
	if transportValue == SessionTransportWebSocket {
		if strings.TrimSpace(signaling) != "" {
			fields = append(fields, "transport", "signaling")
			causes = append(causes, ErrSessionSignalingRequiresWebRTC)
		}
		if strings.TrimSpace(opts.MediaSource) != "" {
			fields = append(fields, "transport", "media-source")
			causes = append(causes, ErrSessionMediaSourceRequiresWebRTC)
		}
	} else {
		if strings.TrimSpace(signaling) == "" {
			fields = append(fields, "transport", "signaling")
			causes = append(causes, ErrSessionWebRTCRequiresSignaling)
		}
		if strings.TrimSpace(opts.MediaSource) == "" {
			fields = append(fields, "transport", "media-source")
			causes = append(causes, ErrSessionWebRTCRequiresMediaSource)
		}
	}
	if len(causes) != 0 {
		return SessionRuntimeSelection{}, sessionRuntimeSelectionError(uniqueSessionSelectionFields(fields), errors.Join(causes...))
	}
	return selection, nil
}

func sessionRuntimeSelectionError(fields []string, err error) error {
	return &SessionRuntimeSelectionError{Fields: append([]string(nil), fields...), Err: err}
}

func uniqueSessionSelectionFields(fields []string) []string {
	seen := make(map[string]struct{}, len(fields))
	unique := make([]string, 0, len(fields))
	for _, field := range fields {
		if _, ok := seen[field]; ok {
			continue
		}
		seen[field] = struct{}{}
		unique = append(unique, field)
	}
	return unique
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
	return newOpenAIRealtimeSessionInferencerWithVoiceAndToolsAndOptions(sessionCfg, "", toolDefinitions, opts...)
}

func newOpenAIRealtimeSessionInferencerWithVoiceAndToolsAndOptions(sessionCfg config.OpenAIConfig, voice string, toolDefinitions []messages.ToolDefinition, opts ...oaiprovider.Option) (messages.SessionInferencer, error) {
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
	if voice != "" {
		inferenceOpts = append(inferenceOpts, inference.WithSessionVoice(voice))
	}
	if len(toolDefinitions) > 0 {
		inferenceOpts = append(inferenceOpts, inference.WithSessionTools(toolDefinitions))
	}
	return inference.NewSessionGatewayInferencer(sessionGateway, inferenceOpts...), nil
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
		config.Voice = opts.Voice
		config.Tools = append([]messages.ToolDefinition(nil), opts.ToolDefinitions...)
		providerOpts := []oaiprovider.Option{
			oaiprovider.WithAPIKey(sessionCfg.APIKey),
			oaiprovider.WithModel(sessionCfg.Model),
			oaiprovider.WithRealtimeBaseURL(openAIRealtimeURL(sessionCfg)),
		}
		if opts.WebSocketDialer != nil {
			providerOpts = append(providerOpts, oaiprovider.WithWebSocketDialer(opts.WebSocketDialer))
		} else {
			providerOpts = append(providerOpts, oaiprovider.WithWebSocketDialer(oaiprovider.NewDefaultWebSocketDialer()))
		}
		providerGateway, err := gateway.NewSessionGateway(gateway.WithSessionProvider(oaiprovider.New(providerOpts...)))
		if err != nil {
			return nil, "", fmt.Errorf("create OpenAI realtime session gateway: %w", err)
		}
		return inference.NewSessionGatewayInferencer(providerGateway, inference.WithSessionRequest(inference.SessionRequest{Config: config})), model, nil
	case sessionProviderGrok:
		sessionCfg, err := resolveGrokSessionConfig(opts)
		if err != nil {
			return nil, "", err
		}
		model = sessionCfg.Model
		config = deviceProbeSessionConfig(model, instructions, models.AudioFormatPCM16, models.AudioFormatPCM16)
		config.Tools = append([]messages.ToolDefinition(nil), opts.ToolDefinitions...)
		providerOpts := []grok.Option{grok.WithAPIKey(sessionCfg.APIKey)}
		if strings.TrimSpace(sessionCfg.BaseURL) != "" {
			providerOpts = append(providerOpts, grok.WithBaseURL(sessionCfg.BaseURL))
		}
		if opts.WebSocketDialer != nil {
			providerOpts = append(providerOpts, grok.WithWebSocketDialer(opts.WebSocketDialer))
		} else {
			providerOpts = append(providerOpts, grok.WithWebSocketDialer(grok.NewDefaultWebSocketDialer()))
		}
		providerGateway, err := gateway.NewSessionGateway(gateway.WithSessionProvider(grok.New(providerOpts...)))
		if err != nil {
			return nil, "", fmt.Errorf("create Grok realtime session gateway: %w", err)
		}
		return inference.NewSessionGatewayInferencer(providerGateway, inference.WithSessionRequest(inference.SessionRequest{Config: config})), model, nil
	default:
		return nil, "", fmt.Errorf("--devices real supports realtime providers %q and %q; got %q", sessionProviderOpenAI, sessionProviderGrok, providerName)
	}
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
