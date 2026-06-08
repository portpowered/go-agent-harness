package providers

import "github.com/portpowered/go-llm-gateway/pkg/models"

// CapabilityState describes whether a provider wrapper can locally claim a
// feature is supported. Unknown is used when support depends on provider model,
// endpoint, or runtime behavior that the wrapper cannot prove statically.
type CapabilityState string

const (
	CapabilitySupported   CapabilityState = "supported"
	CapabilityUnsupported CapabilityState = "unsupported"
	CapabilityUnknown     CapabilityState = "unknown"
)

// Capability reports a single provider feature state and the reason for that
// state when the answer is unsupported or unknown.
type Capability struct {
	State     CapabilityState `json:"state"`
	Rationale string          `json:"rationale,omitempty"`
}

// StatelessCapabilities describes features on request/response and stream
// inference paths.
type StatelessCapabilities struct {
	Inference       Capability `json:"inference"`
	Streaming       Capability `json:"streaming"`
	Tools           Capability `json:"tools"`
	ImageInput      Capability `json:"imageInput"`
	AudioInput      Capability `json:"audioInput"`
	VideoInput      Capability `json:"videoInput"`
	VideoOutput     Capability `json:"videoOutput"`
	Reasoning       Capability `json:"reasoning"`
	PromptCaching   Capability `json:"promptCaching"`
	ProviderOptions Capability `json:"providerOptions"`
}

// SessionCapabilities describes features on persistent bidirectional session
// paths.
type SessionCapabilities struct {
	Sessions           Capability                        `json:"sessions"`
	Tools              Capability                        `json:"tools"`
	TextModality       Capability                        `json:"textModality"`
	AudioModality      Capability                        `json:"audioModality"`
	InputAudioFormats  map[models.AudioFormat]Capability `json:"inputAudioFormats,omitempty"`
	OutputAudioFormats map[models.AudioFormat]Capability `json:"outputAudioFormats,omitempty"`
	TurnDetection      Capability                        `json:"turnDetection"`
	ProviderOptions    Capability                        `json:"providerOptions"`
}

// ProviderCapabilities is the public capability report for one provider family.
type ProviderCapabilities struct {
	Provider  string                `json:"provider"`
	Stateless StatelessCapabilities `json:"stateless"`
	Session   *SessionCapabilities  `json:"session,omitempty"`
}

// CapabilityReporter is implemented by providers that expose a static
// provider-family capability report.
type CapabilityReporter interface {
	Capabilities() ProviderCapabilities
}

func Supported() Capability {
	return Capability{State: CapabilitySupported}
}

func Unsupported(rationale string) Capability {
	return Capability{State: CapabilityUnsupported, Rationale: rationale}
}

func Unknown(rationale string) Capability {
	return Capability{State: CapabilityUnknown, Rationale: rationale}
}

func UnsupportedStatelessCapabilities(rationale string) StatelessCapabilities {
	unsupported := Unsupported(rationale)
	return StatelessCapabilities{
		Inference:       unsupported,
		Streaming:       unsupported,
		Tools:           unsupported,
		ImageInput:      unsupported,
		AudioInput:      unsupported,
		VideoInput:      unsupported,
		VideoOutput:     unsupported,
		Reasoning:       unsupported,
		PromptCaching:   unsupported,
		ProviderOptions: unsupported,
	}
}

func UnsupportedSessionCapabilities(rationale string) SessionCapabilities {
	unsupported := Unsupported(rationale)
	return SessionCapabilities{
		Sessions:           unsupported,
		Tools:              unsupported,
		TextModality:       unsupported,
		AudioModality:      unsupported,
		InputAudioFormats:  unsupportedAudioFormats(rationale),
		OutputAudioFormats: unsupportedAudioFormats(rationale),
		TurnDetection:      unsupported,
		ProviderOptions:    unsupported,
	}
}

func SessionCapabilitiesPtr(capabilities SessionCapabilities) *SessionCapabilities {
	return &capabilities
}

func RealtimeAudioFormats() map[models.AudioFormat]Capability {
	return map[models.AudioFormat]Capability{
		models.AudioFormatPCM16:    Supported(),
		models.AudioFormatG711Ulaw: Supported(),
		models.AudioFormatG711Alaw: Supported(),
	}
}

func unsupportedAudioFormats(rationale string) map[models.AudioFormat]Capability {
	return map[models.AudioFormat]Capability{
		models.AudioFormatPCM16:    Unsupported(rationale),
		models.AudioFormatG711Ulaw: Unsupported(rationale),
		models.AudioFormatG711Alaw: Unsupported(rationale),
	}
}
