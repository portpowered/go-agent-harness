// Package bareadmission resolves the immutable policy used to admit a bare
// live session. Hosts supply already-loaded configuration and environment
// snapshots; this package never discovers files, environment, devices, or
// providers on its own.
package bareadmission

import (
	"context"
	"fmt"
	"strings"
)

const (
	ProviderOpenAI = "openai"
	ProviderGrok   = "grok"

	TransportWebSocket = "ws"
	TransportWebRTC    = "webrtc"

	DefaultOpenAIModel        = "gpt-realtime-2.1-mini"
	DefaultTranscriptionModel = "gpt-live-transcribe"
)

type errorCode string

func (e errorCode) Error() string { return string(e) }

const (
	// ErrUnsupportedProvider classifies a provider that the bare-session
	// policy cannot admit.
	ErrUnsupportedProvider errorCode = "unsupported bare live session provider"
	// ErrCredentialMissing classifies a missing credential discovered before
	// any host-owned provider or device acquisition.
	ErrCredentialMissing errorCode = "bare live session credential is missing"
	// ErrInvalidTransport classifies a transport outside the two supported
	// bare-session dispatch choices.
	ErrInvalidTransport errorCode = "invalid session transport"

	// These errors are deliberately local to the provider-neutral contract. The
	// CLI compatibility adapter translates them to its historical provider
	// error types at the host edge.
	ErrModelCatalogRequired     errorCode = "provider model catalog is required"
	ErrUnsupportedRealtimeModel errorCode = "unsupported realtime model"
)

// UnsupportedRealtimeModelError is the stable provider-neutral typed
// model-admission error returned by the policy service.
type UnsupportedRealtimeModelError struct {
	Provider        string
	Model           string
	SupportedModels []string
}

func (e *UnsupportedRealtimeModelError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%s model %q is not realtime-capable for agent session; supported realtime models: %s", e.Provider, e.Model, strings.Join(e.SupportedModels, ", "))
}

func (e *UnsupportedRealtimeModelError) Unwrap() error {
	if e == nil {
		return nil
	}
	return ErrUnsupportedRealtimeModel
}

// CredentialError is the redacted, actionable missing-credential failure.
// ConfigPath is descriptive provenance only; no credential material is kept.
type CredentialError struct {
	Provider   string
	ConfigPath string
}

func (e *CredentialError) Error() string {
	if e == nil {
		return ErrCredentialMissing.Error()
	}
	provider := strings.ToLower(strings.TrimSpace(e.Provider))
	if provider == "" {
		provider = ProviderOpenAI
	}
	if provider == ProviderOpenAI {
		return fmt.Sprintf(
			"openai realtime api key is missing: bare live session requires an OpenAI API key; set OPENAI_API_KEY or AGENT_MODEL__OPENAI__API_KEY, pass --api-key, or configure model.openai.api_key in %s",
			e.ConfigPath,
		)
	}
	return fmt.Sprintf(
		"%s API key is required for bare live session (set AGENT_MODEL__%s__API_KEY, pass --api-key, or configure model.%s.api_key in %s)",
		provider, strings.ToUpper(provider), provider, e.ConfigPath,
	)
}

func (e *CredentialError) Unwrap() error { return ErrCredentialMissing }

// InvalidTransportError reports the normalized transport rejected by policy.
type InvalidTransportError struct{ Transport string }

func (e *InvalidTransportError) Error() string {
	if e == nil {
		return ErrInvalidTransport.Error()
	}
	return fmt.Sprintf("%s: %q (want %q or %q)", ErrInvalidTransport, e.Transport, TransportWebSocket, TransportWebRTC)
}

func (e *InvalidTransportError) Unwrap() error { return ErrInvalidTransport }

// Model is one provider-neutral catalog entry. The service only uses the
// provider, ID, and immutable capability snapshot during admission.
type Model struct {
	Provider                string
	ID                      string
	SupportsAudio           bool
	SupportsImageInput      bool
	SupportsFunctionCalling bool
	SupportsReasoning       bool
}

// ModelCatalog is the value-shaped catalog supplied by a host. The service
// copies Models before resolving, so later caller mutation cannot affect a
// request or a repeated resolution.
type ModelCatalog struct{ Models []Model }

// VADConfig is the provider-neutral snapshot of the persisted VAD policy.
type VADConfig struct {
	Enabled           *bool
	Type              string
	Threshold         float64
	PrefixPaddingMs   int
	SilenceDurationMs int
	CreateResponse    *bool
	InterruptResponse *bool
	Eagerness         string
}

// TranscriptionConfig is the provider-neutral snapshot of customer-audio
// transcription policy.
type TranscriptionConfig struct {
	Enabled *bool
	Model   string
}

// SessionConfig is the already-loaded session-specific configuration. It is
// intentionally independent of CLI config types and contains no host handles.
type SessionConfig struct {
	Provider           string
	Model              string
	Transport          string
	InputDevice        string
	OutputDevice       string
	VAD                *VADConfig
	InputTranscription *TranscriptionConfig
}

// ProviderConfig is a snapshot of the provider settings selected by the host.
type ProviderConfig struct {
	Model   string
	APIKey  string
	BaseURL string
}

// ConfigSnapshot contains the already-loaded, immutable host configuration.
// A nil Config is a deliberate input and fails closed only when policy needs
// a missing dependency; it never causes filesystem or environment access.
type ConfigSnapshot struct {
	Path          string
	ModelProvider string
	OpenAI        *ProviderConfig
	Grok          *ProviderConfig
	Session       *SessionConfig
}

// DeviceSelection carries selectors and command presence bits only. Opening
// or acquiring a device remains outside this service.
type DeviceSelection struct {
	InputDevice   string
	OutputDevice  string
	InputPresent  bool
	OutputPresent bool
}

// Request is the complete explicit input to one bare-session admission. The
// service does not infer values from process state, files, flags, or globals.
type Request struct {
	Provider                string
	Model                   string
	ModelProvided           bool
	APIKey                  string
	BaseURL                 string
	Transport               string
	TransportProvided       bool
	NoInputTranscription    bool
	EnvironmentOpenAIAPIKey string
	Config                  *ConfigSnapshot
	Catalog                 *ModelCatalog
	Device                  DeviceSelection
}

// TurnDetection is the immutable resolved provider policy returned to the
// host adapter. Pointer booleans preserve explicit false values.
type TurnDetection struct {
	Type              string
	Threshold         float64
	PrefixPaddingMs   int
	SilenceDurationMs int
	CreateResponse    *bool
	InterruptResponse *bool
	Eagerness         string
}

// Transcription is the immutable resolved customer-audio policy.
type Transcription struct {
	Enabled bool
	Model   string
}

// Result is the complete resolved policy. It contains no acquired provider or
// device object and owns all pointer values returned to the caller.
type Result struct {
	BareLive                bool
	Provider                string
	Model                   string
	APIKey                  string
	BaseURL                 string
	Transport               string
	TurnDetection           *TurnDetection
	InputAudioTranscription *Transcription
	Device                  DeviceSelection
}

// Service resolves one request without external acquisition.
type Service interface {
	Resolve(context.Context, Request) (Result, error)
}
