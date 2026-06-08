// Package capabilities defines the public provider feature contract for
// go-llm-gateway.
//
// CapabilityStateUnknown is the default for providers that do not publish
// explicit capabilities. Unknown means the gateway has no local proof of
// support and consumers must not treat it as supported.
package capabilities

import "fmt"

// CapabilityState describes whether a provider can satisfy a feature locally.
type CapabilityState string

const (
	// CapabilityStateUnknown means the provider has not made an explicit support
	// claim. This is the default and must not be interpreted as support.
	CapabilityStateUnknown CapabilityState = "unknown"
	// CapabilityStateSupported means the provider contract explicitly supports
	// the feature.
	CapabilityStateSupported CapabilityState = "supported"
	// CapabilityStateUnsupported means the provider contract explicitly does not
	// support the feature.
	CapabilityStateUnsupported CapabilityState = "unsupported"
)

// IsKnown reports whether the state is explicitly supported or unsupported.
func (s CapabilityState) IsKnown() bool {
	return s == CapabilityStateSupported || s == CapabilityStateUnsupported
}

// IsSupported reports whether the state is an explicit support claim.
func (s CapabilityState) IsSupported() bool {
	return s == CapabilityStateSupported
}

// FeatureCapability describes one provider feature. Detail is intended for
// stable human-readable context such as "not implemented by this wrapper";
// provider-specific machine-readable fields belong in Metadata.
type FeatureCapability struct {
	State    CapabilityState   `json:"state"`
	Detail   string            `json:"detail,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// Supported returns an explicit supported capability.
func Supported(detail string) FeatureCapability {
	return FeatureCapability{State: CapabilityStateSupported, Detail: detail}
}

// Unsupported returns an explicit unsupported capability.
func Unsupported(detail string) FeatureCapability {
	return FeatureCapability{State: CapabilityStateUnsupported, Detail: detail}
}

// Unknown returns a capability with no support claim.
func Unknown(detail string) FeatureCapability {
	return FeatureCapability{State: CapabilityStateUnknown, Detail: detail}
}

// IsSupported reports whether this feature is explicitly supported.
func (c FeatureCapability) IsSupported() bool {
	return c.State.IsSupported()
}

// IsKnown reports whether this feature has an explicit supported or unsupported
// state.
func (c FeatureCapability) IsKnown() bool {
	return c.State.IsKnown()
}

// StatelessCapabilities describes request/response provider behavior.
type StatelessCapabilities struct {
	Tools                  FeatureCapability `json:"tools"`
	Streaming              FeatureCapability `json:"streaming"`
	ImageInput             FeatureCapability `json:"imageInput"`
	AudioInput             FeatureCapability `json:"audioInput"`
	AudioOutput            FeatureCapability `json:"audioOutput"`
	VideoOutput            FeatureCapability `json:"videoOutput"`
	Reasoning              FeatureCapability `json:"reasoning"`
	PromptCaching          FeatureCapability `json:"promptCaching"`
	ProviderSpecificConfig FeatureCapability `json:"providerSpecificConfig"`
}

// SessionCapabilities describes persistent bidirectional session behavior.
type SessionCapabilities struct {
	Sessions               FeatureCapability `json:"sessions"`
	Tools                  FeatureCapability `json:"tools"`
	AudioInput             FeatureCapability `json:"audioInput"`
	AudioOutput            FeatureCapability `json:"audioOutput"`
	ProviderSpecificConfig FeatureCapability `json:"providerSpecificConfig"`
}

// ProviderCapabilities is the public capability contract for one provider.
type ProviderCapabilities struct {
	Provider  string                `json:"provider"`
	Stateless StatelessCapabilities `json:"stateless"`
	Session   SessionCapabilities   `json:"session"`
	Metadata  map[string]string     `json:"metadata,omitempty"`
}

// Feature identifies a capability-gated provider behavior.
type Feature string

const (
	FeatureSessions               Feature = "sessions"
	FeatureTools                  Feature = "tools"
	FeatureStreaming              Feature = "streaming"
	FeatureImageInput             Feature = "image_input"
	FeatureAudioInput             Feature = "audio_input"
	FeatureAudioOutput            Feature = "audio_output"
	FeatureVideoOutput            Feature = "video_output"
	FeatureReasoning              Feature = "reasoning"
	FeaturePromptCaching          Feature = "prompt_caching"
	FeatureProviderSpecificConfig Feature = "provider_specific_config"
)

const (
	RequestedModeStateless       = "stateless"
	RequestedModeStatelessStream = "stateless_stream"
	RequestedModeSession         = "session"
)

// UnsupportedFeatureError reports a deterministic local request/capability
// mismatch before a provider call is attempted.
type UnsupportedFeatureError struct {
	Provider      string            `json:"provider"`
	Feature       Feature           `json:"feature"`
	RequestedMode string            `json:"requestedMode"`
	Capability    FeatureCapability `json:"capability"`
}

func (e *UnsupportedFeatureError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("provider %q does not support %s for %s requests: capability state is %q", e.Provider, e.Feature, e.RequestedMode, e.Capability.State)
}

// UnknownProviderCapabilities returns the documented fallback for providers
// that do not implement explicit capability reporting.
func UnknownProviderCapabilities(provider string) ProviderCapabilities {
	unknown := Unknown("provider has not published explicit capability metadata")
	return ProviderCapabilities{
		Provider: provider,
		Stateless: StatelessCapabilities{
			Tools:                  unknown,
			Streaming:              unknown,
			ImageInput:             unknown,
			AudioInput:             unknown,
			AudioOutput:            unknown,
			VideoOutput:            unknown,
			Reasoning:              unknown,
			PromptCaching:          unknown,
			ProviderSpecificConfig: unknown,
		},
		Session: SessionCapabilities{
			Sessions:               unknown,
			Tools:                  unknown,
			AudioInput:             unknown,
			AudioOutput:            unknown,
			ProviderSpecificConfig: unknown,
		},
	}
}
