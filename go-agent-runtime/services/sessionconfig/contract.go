// Package sessionconfig defines the provider-neutral policy contract for
// realtime session admission and selection.
//
// Hosts supply already-loaded defaults and an immutable provider model catalog
// at the composition boundary. The service does not read configuration,
// environment variables, flags, credentials, devices, or network resources.
package sessionconfig

import (
	"errors"
	"fmt"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
)

const (
	ProviderOpenAI = "openai"
	ProviderGrok   = "grok"

	TransportWebSocket = "ws"
	TransportWebRTC    = "webrtc"

	ReplayTimingImmediate = "immediate"
	ReplayTimingRecorded  = "recorded"

	OpenAIRealtimeDefaultModel   = providers.OpenAIRealtimeDefaultModel
	OpenAIRealtimeReasoningModel = providers.OpenAIRealtime21Model
	OpenAIRealtimeBaseURL        = "wss://api.openai.com/v1/realtime"
	OpenAIRealtimeAPIKeyEnv      = "AGENT_MODEL__OPENAI__API_KEY"
	ConfigFileName               = "config.yaml"
)

type sentinelError string

func (e sentinelError) Error() string { return string(e) }

const (
	// ErrInvalidSessionRuntimeSelection identifies a malformed or incompatible
	// transport/signaling/media selection at the service boundary.
	ErrInvalidSessionRuntimeSelection sentinelError = "invalid session runtime selection"
	// ErrInvalidSessionTransport identifies a transport value the service does
	// not know how to dispatch.
	ErrInvalidSessionTransport sentinelError = "invalid session transport"
	// ErrSessionSignalingRequiresWebRTC identifies signaling supplied for the
	// unchanged WebSocket runtime.
	ErrSessionSignalingRequiresWebRTC sentinelError = "session signaling requires WebRTC transport"
	// ErrSessionMediaSourceRequiresWebRTC identifies a media source supplied for
	// the unchanged WebSocket runtime.
	ErrSessionMediaSourceRequiresWebRTC sentinelError = "session media source requires WebRTC transport"
	// ErrSessionWebRTCRequiresSignaling identifies a WebRTC request without an
	// endpoint for the signaling exchange.
	ErrSessionWebRTCRequiresSignaling sentinelError = "WebRTC session transport requires signaling"
	// ErrSessionWebRTCRequiresMediaSource identifies a WebRTC request without a
	// selected external media source.
	ErrSessionWebRTCRequiresMediaSource sentinelError = "WebRTC session transport requires media source"
	// ErrSessionRuntimeSelectionConflict identifies two aliases carrying
	// different signaling endpoint values.
	ErrSessionRuntimeSelectionConflict sentinelError = "conflicting session signaling endpoints"
	// ErrOpenAIRealtimeAPIKeyMissing classifies a live OpenAI credential failure.
	ErrOpenAIRealtimeAPIKeyMissing sentinelError = "openai realtime api key is missing"
	// ErrInvalidOpenAIRealtimeVoice identifies a voice outside the documented
	// OpenAI Realtime voice registry.
	ErrInvalidOpenAIRealtimeVoice sentinelError = "invalid OpenAI Realtime voice"
)

// RuntimeSelection is the canonical transport selection consumed by runtime
// planners. Transport is normalized to ws or webrtc; endpoint and media values
// retain the exact caller-provided strings.
type RuntimeSelection struct {
	Transport         string
	SignalingEndpoint string
	MediaSource       string
}

// SessionRuntimeSelection is a descriptive compatibility name for
// RuntimeSelection.
type SessionRuntimeSelection = RuntimeSelection

// RuntimeSelectionError reports every option field that made a runtime
// selection invalid while preserving both the stable class and specific cause.
type RuntimeSelectionError struct {
	Fields []string
	Err    error
}

// SessionRuntimeSelectionError is the long-form compatibility name for
// RuntimeSelectionError.
type SessionRuntimeSelectionError = RuntimeSelectionError

func (e *RuntimeSelectionError) Error() string {
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

func (e *RuntimeSelectionError) Unwrap() error {
	if e == nil {
		return nil
	}
	return errors.Join(ErrInvalidSessionRuntimeSelection, e.Err)
}

// InvalidOpenAIRealtimeVoiceError reports a value outside the documented
// built-in OpenAI Realtime voice set.
type InvalidOpenAIRealtimeVoiceError struct {
	Voice           string
	SupportedVoices []string
}

func (e *InvalidOpenAIRealtimeVoiceError) Error() string {
	if e == nil {
		return ErrInvalidOpenAIRealtimeVoice.Error()
	}
	return fmt.Sprintf("invalid OpenAI Realtime voice %q; supported voices: %s", e.Voice, strings.Join(e.SupportedVoices, ", "))
}

func (e *InvalidOpenAIRealtimeVoiceError) Unwrap() error {
	if e == nil {
		return nil
	}
	return ErrInvalidOpenAIRealtimeVoice
}

// ProviderConfig is a value snapshot of the credential and model fields a
// realtime provider needs. The service never reads or writes credentials; the
// host decides how to obtain them before building a Request.
type ProviderConfig struct {
	Model           string
	APIKey          string
	BaseURL         string
	ReasoningEffort string
}

// RuntimeProviderConfig is a descriptive compatibility name for
// ProviderConfig.
type RuntimeProviderConfig = ProviderConfig

// SessionDefaults contains persisted session-specific defaults. A nil pointer
// in Defaults.Session means the session block was absent, which is distinct
// from a present block whose fields are empty.
type SessionDefaults struct {
	Provider        string
	Model           string
	ReasoningEffort string
	Transport       string
}

// Defaults is the host-supplied, already-loaded configuration snapshot. It is
// deliberately smaller than a CLI configuration tree and contains no file,
// flag, environment, terminal, device, or provider implementation details.
type Defaults struct {
	Provider string
	Session  *SessionDefaults
	OpenAI   *ProviderConfig
	Grok     *ProviderConfig
}

// Request contains the request-scoped values needed for pure session policy.
// ProviderProvided/ModelProvided preserve the distinction between omitted and
// explicit command values; in particular an explicitly empty model is rejected
// instead of silently selecting a default.
type Request struct {
	Provider         string
	ProviderProvided bool
	Model            string
	ModelProvided    bool
	APIKey           string
	BaseURL          string
	ReasoningEffort  string
	Voice            string

	RecordPath   string
	ReplayPath   string
	ReplayTiming string

	SessionInferencer messages.SessionInferencer

	Transport         string
	Signaling         string
	SignalingEndpoint string
	MediaSource       string

	Defaults Defaults
}

// Service owns pure session option validation and provider/runtime selection.
// Implementations are inert after construction and hold only an immutable
// model-catalog dependency.
type Service interface {
	Validate(Request) error
	ValidateCapture(Request) error
	ResolveRuntimeSelection(Request) (RuntimeSelection, error)
	ResolveProvider(Request) string
	ResolveOpenAIRealtimeConfig(Request) (ProviderConfig, error)
	ResolveGrokConfig(Request) (ProviderConfig, error)
	NormalizeReplayTiming(string) string
	ValidateOpenAIRealtimeVoice(string) error
	ValidateOpenAIRealtimeReasoningEffort(string) error
	SupportedOpenAIRealtimeVoices() []string
	CloneTurnDetection(*models.TurnDetectionConfig) *models.TurnDetectionConfig
	OpenAIRealtimeURL(ProviderConfig) string
	// ResolveOpenAI and ResolveGrok are concise aliases for embedders.
	ResolveOpenAI(Request) (ProviderConfig, error)
	ResolveGrok(Request) (ProviderConfig, error)
}
