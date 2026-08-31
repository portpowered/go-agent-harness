// This file contains session option types, validation, configuration resolution, and provider construction for the session command.
package services

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/metrics"
	platformclock "github.com/portpowered/go-agent-harness/go-agent-loop/pkg/platform/clock"
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
	openAIRealtimeBaseURL = "wss://api.openai.com/v1/realtime"

	// SessionTransportWebSocket is the unchanged session transport default.
	SessionTransportWebSocket = "ws"
	// SessionTransportWebRTC selects the service-owned WebRTC runtime.
	SessionTransportWebRTC = "webrtc"
	// SessionOpenAIAPIKeyEnv is the canonical environment key used by the
	// shared config loader for OpenAI realtime sessions.
	SessionOpenAIAPIKeyEnv = "AGENT_MODEL__OPENAI__API_KEY"
)

var (
	// ErrSessionAudioInTurnBargeRequiresSequence identifies an opt-in barge-in
	// request that does not provide the repeated-turn sequence it controls.
	ErrSessionAudioInTurnBargeRequiresSequence = errors.New("--audio-in-turn-barge requires at least two --audio-in-turn values")
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
	// ErrOpenAIRealtimeAPIKeyMissing classifies the preflight error returned
	// when an OpenAI realtime session has no credential. Callers that do not
	// expose an --api-key flag (for example `room run`) should catch this
	// with errors.Is and substitute a remedy their command actually accepts
	// instead of surfacing the --api-key wording below.
	ErrOpenAIRealtimeAPIKeyMissing = errors.New("openai realtime api key is missing")
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

// SessionAudioInTurnBargeError reports an invalid --audio-in-turn-barge
// cardinality before session setup or provider connection.
type SessionAudioInTurnBargeError struct {
	TurnCount int
}

func (e *SessionAudioInTurnBargeError) Error() string {
	if e == nil {
		return ErrSessionAudioInTurnBargeRequiresSequence.Error()
	}
	return fmt.Sprintf("%s; got %d", ErrSessionAudioInTurnBargeRequiresSequence, e.TurnCount)
}

func (e *SessionAudioInTurnBargeError) Unwrap() error {
	return ErrSessionAudioInTurnBargeRequiresSequence
}

// ValidateSessionAudioInTurnBarge validates the explicit scheduled-turn
// policy before any provider or capability setup. The ordinary one-turn and
// multi-turn paths remain valid when the opt-in is omitted.
func ValidateSessionAudioInTurnBarge(enabled bool, turnCount int) error {
	if !enabled || turnCount >= 2 {
		return nil
	}
	if turnCount < 0 {
		turnCount = 0
	}
	return &SessionAudioInTurnBargeError{TurnCount: turnCount}
}

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
	RecordPath string
	ReplayPath string
	// roomReplay marks a session that is part of a room-owned replay. Room
	// orchestration supplies audio frames from the shared timeline, so the
	// single-session replay planner must not auto-reconstruct client audio
	// turns from the provider capture.
	roomReplay bool
	Provider   string
	// ProviderProvided distinguishes an explicit provider flag from the
	// command's empty/default value when resolving persisted bare-session
	// settings.
	ProviderProvided bool
	Model            string
	ModelProvided    bool
	// NoInputTranscription explicitly disables the live OpenAI customer-audio
	// transcription default. It has no effect on replay, whose recorded
	// session.update remains authoritative.
	NoInputTranscription bool
	// InputAudioTranscription is the resolved request-scoped transcription
	// policy. Nil preserves the existing mode-specific policy resolution.
	InputAudioTranscription *models.InputAudioTranscriptionConfig
	APIKey                  string
	BaseURL                 string
	ConfigDir               string
	Prompt                  string
	// PromptProvided distinguishes an explicitly supplied empty prompt from an
	// omitted prompt. Replay uses the distinction to opt into capture-derived
	// prompt planning only when the caller did not provide a prompt.
	PromptProvided bool
	// Voice selects the optional OpenAI Realtime audio output voice for this
	// invocation. The empty value preserves the provider default. It is kept
	// on the session options rather than package state so concurrent sessions
	// retain independent configuration.
	Voice             string
	SessionInferencer messages.SessionInferencer
	WebSocketDialer   transport.Dialer
	// RecordSessionCapturePath, when non-empty, makes NewLiveSessionInferencer
	// wrap the resolved websocket dialer (WebSocketDialer, or the provider's
	// real default dialer when that is unset) with a raw-traffic recorder,
	// and makes the returned inferencer additionally implement
	// SessionInferencerCaptureFlusher. It is unrelated to RecordPath/the
	// solo `agent session run --record` path: this seam exists so a caller
	// that constructs a session through NewLiveSessionInferencer directly
	// (the room runtime's default participant factory) can capture the same
	// kind of raw provider session capture that path produces.
	RecordSessionCapturePath string
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
	// TransportProvided distinguishes the pflag default from an explicit
	// transport selection so persisted bare-session transport can be honored.
	TransportProvided bool
	// BareLive marks the resolved zero/alternate-free live-device request. It
	// is intentionally separate from BrowserToolsEnabled and capture modes.
	BareLive bool
	// TurnDetection is the resolved server-side VAD policy for a bare live
	// session. Nil preserves existing non-bare behavior.
	TurnDetection *models.TurnDetectionConfig
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
	// ToolDefinitionBase is the immutable static and stable broker surface
	// retained by the live dynamic-tool publisher. Callers that do not provide
	// a separate base retain compatibility; the publisher falls back to the
	// initial ToolDefinitions snapshot.
	ToolDefinitionBase []messages.ToolDefinition
	// RefreshToolDefinitions returns the complete current session tool surface,
	// including static, stable broker, and current first-class page tools. Its
	// error is kept explicit so a failed catalog read cannot advance provider
	// alignment.
	RefreshToolDefinitions func(context.Context) ([]messages.ToolDefinition, error)
	// BrowserWatch supplies an independent subscription to semantic broker
	// selection/catalog/generation events for this session.
	BrowserWatch func(context.Context) <-chan webmcp.BrokerEvent
	// BrowserEventWatch supplies the adapter-owned semantic browser events used
	// only by the optional recording observer. It never owns session delivery.
	BrowserEventWatch func(context.Context) <-chan webmcp.BrowserEvent
	// BrowserCapabilityState is the session-owned browser state used to compose
	// model-facing selection grounding. It must not be inferred from the
	// presence or absence of dynamic page definitions.
	BrowserCapabilityState webmcp.BrowserCapabilityState
	// BrowserToolsEnabled records the resolved browser capability admission.
	// It allows an explicitly activated live session to run without requiring
	// the legacy provider-recording flag while keeping ordinary sessions on
	// their existing validation path.
	BrowserToolsEnabled bool
	// LoadedConfig is the config snapshot used to derive session capabilities.
	// When present, provider resolution reuses it instead of loading config a
	// second time during runtime planning.
	LoadedConfig *config.Config
	// InteractiveToolPolicy optionally supplies an already-resolved policy
	// snapshot. When nil, runtime planning resolves one from LoadedConfig, an
	// existing ConfigDir file, or the documented defaults before provider
	// construction.
	InteractiveToolPolicy *InteractiveToolPolicy

	// CapabilityClose is the optional cleanup hook transferred from the CLI
	// session capability factory. The service wraps it in one shared
	// SessionCapabilityCoordinator so planning, runtime, and nested wrappers
	// cannot close the same capability more than once.
	CapabilityClose       func() error
	capabilityCoordinator *SessionCapabilityCoordinator

	// CancellationIntent carries the CLI-owned, run-scoped SIGINT marker into
	// terminal accounting. A nil value preserves ordinary caller-cancellation
	// behavior for service callers that do not own OS signal handling.
	CancellationIntent *SessionCancellationIntent

	// ToolExecutionTimeout overrides the per-invocation session tool adapter
	// deadline for hermetic tests. Zero selects the class-specific interactive
	// policy budget.
	ToolExecutionTimeout time.Duration

	// Clock stamps runtime observations. A nil clock uses the host clock. The
	// generated CLI supplies the composed clock so replay and recording
	// observers can correlate events across command instances.
	Clock platformclock.Source
	// LivenessClock supplies participant-owned watchdog timers. Nil derives a
	// timer clock from Clock when possible, otherwise the session uses the host
	// clock. Deterministic callers can inject this seam without changing the
	// runtime timestamp source.
	LivenessClock SessionLivenessClock
	// RuntimeObserver receives clock-stamped audio, turn, and terminal events
	// from the session command. The terminal event carries the production-owned
	// session-cumulative token totals and complete metrics snapshot. Nil keeps
	// the runtime observationally silent.
	RuntimeObserver SessionRuntimeObserver

	// Diagnostics optionally receives one canonical structured record per
	// terminal failure plus per-turn and tool-call records. Nil keeps runtime
	// behavior byte-for-byte unchanged.
	Diagnostics SessionDiagnosticSink
	// ToolDiagnostics optionally receives the original typed error for each
	// session tool failure. It is an operator-only channel; the provider sees
	// the session adapter's customer-safe projection instead.
	ToolDiagnostics SessionToolDiagnosticSink
	// MetricsRecorder optionally receives per-direction stream observations.
	MetricsRecorder metrics.Recorder
	// StreamObserver optionally receives every session stream delta after it
	// crosses the session loop boundary. Nil keeps runtime behavior unchanged.
	StreamObserver SessionStreamObserver
	// AudioInputs schedules user audio injections through the loop's existing
	// audio-input seam, attributed to specific turns.
	AudioInputs []ScheduledAudioInput
	// AudioInTurnBarge selects the explicit active-response dispatch policy for
	// repeated --audio-in-turn inputs. False preserves the completion-gated
	// serialized policy.
	AudioInTurnBarge bool
	// AudioInterruptions delivers event-driven customer audio through the same
	// duplex loop as scheduled inputs. The browser conversation runner uses it
	// for overlap audio that must be admitted only after an in-flight browser
	// invocation is observed; it is intentionally not a second audio loop.
	AudioInterruptions <-chan ScheduledAudioInput
	// ClientOwnsAudioTurnBoundaries requests an explicit client-owned realtime
	// audio turn contract for a finite --audio-in source. The source sends the
	// MESSAGE.END boundary itself; provider VAD must not auto-commit the same
	// buffer before that boundary arrives.
	ClientOwnsAudioTurnBoundaries bool
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

	// recordingClaim is acquired before provider construction and shared by
	// nested session wrappers. It is intentionally private; command callers
	// select the destination through RecordPath and do not manage sidecars.
	recordingClaim *sessionRecordingClaim
	// recordingDirectoryClaim is acquired before provider/media setup and shared
	// by nested directory-recording wrappers through finalization.
	recordingDirectoryClaim *sessionRecordingDirectoryClaim
}

func validateSessionRunOptions(opts SessionRunOptions) error {
	if err := ValidateOpenAIRealtimeVoice(opts.Voice); err != nil {
		return err
	}
	if _, err := resolveSessionRuntimeSelection(opts); err != nil {
		return err
	}
	if opts.RecordPath == "" && opts.ReplayPath == "" && !opts.BrowserToolsEnabled && !opts.BareLive {
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
	// Validate the complete capture before any caller-owned audio source,
	// derived artifact sink, provider plan, or replay session can be created.
	// Injected sessions are an explicit low-level test seam and do not use the
	// path-based replay contract.
	if opts.ReplayPath != "" && opts.SessionInferencer == nil {
		if _, err := gwtesting.LoadSessionCaptureForReplay(opts.ReplayPath); err != nil {
			return fmt.Errorf("replay session capture %s: %w", opts.ReplayPath, err)
		}
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
	provider := strings.ToLower(strings.TrimSpace(effectiveSessionProvider(opts)))
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
		return fmt.Errorf("--record supports session providers %q and %q; got %q", sessionProviderGrok, sessionProviderOpenAI, effectiveSessionProvider(opts))
	}
}

func missingSessionProviderError() error {
	return fmt.Errorf("--record requires --provider %s or --provider %s for live session inference", sessionProviderGrok, sessionProviderOpenAI)
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
		return config.GrokConfig{}, missingSessionProviderError()
	}

	effective := loadedCfg.ApplyOverrides(opts.APIKey, opts.Model, opts.Provider, opts.BaseURL)
	if strings.TrimSpace(effective.Model.Provider) == "" {
		return config.GrokConfig{}, missingSessionProviderError()
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
		return config.OpenAIConfig{}, missingSessionProviderError()
	}

	effective := loadedCfg.ApplyOverrides(opts.APIKey, opts.Model, opts.Provider, opts.BaseURL)
	if strings.TrimSpace(effective.Model.Provider) == "" {
		return config.OpenAIConfig{}, missingSessionProviderError()
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
		return config.OpenAIConfig{}, fmt.Errorf("%w: OpenAI API key is required for live realtime session mode (set %s, pass --api-key, or configure model.openai.api_key in %s)", ErrOpenAIRealtimeAPIKeyMissing, SessionOpenAIAPIKeyEnv, config.ConfigFileName)
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
	return newOpenAIRealtimeSessionInferencerWithVoiceAndToolsAndInputAudioTranscriptionAndOptions(sessionCfg, voice, toolDefinitions, models.InputAudioTranscriptionConfig{
		Enabled: true,
		Model:   models.DefaultInputAudioTranscriptionModel,
	}, opts...)
}

func newOpenAIRealtimeSessionInferencerWithVoiceAndToolsAndInputAudioTranscriptionAndOptions(sessionCfg config.OpenAIConfig, voice string, toolDefinitions []messages.ToolDefinition, inputAudioTranscription models.InputAudioTranscriptionConfig, opts ...oaiprovider.Option) (messages.SessionInferencer, error) {
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

func cloneSessionTurnDetection(policy *models.TurnDetectionConfig) *models.TurnDetectionConfig {
	if policy == nil {
		return nil
	}
	copy := *policy
	if policy.CreateResponse != nil {
		createResponse := *policy.CreateResponse
		copy.CreateResponse = &createResponse
	}
	return &copy
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
