//go:build darwin && cgo && !nomicrophone

package devices

// NewPlatformDeviceRegistry returns the host's lazy CoreAudio registry.
// Device enumeration and native handles are acquired only when the caller
// invokes a registry operation.
func NewPlatformDeviceRegistry() DeviceRegistry { return NewCoreAudioDeviceRegistry() }

// CoreAudio flag values (CoreAudioBaseTypes.h, AUComponent.h).
const (
	audioFormatFlagIsSignedInteger       = 0x4
	audioFormatFlagIsPacked              = 0x8
	audioUnitRenderActionOutputIsSilence = 1 << 4
	bitsPerByte                          = 8
)

// VoiceProcessingProvider reports whether a device endpoint is backed by a
// native duplex voice-processing graph rather than the portable fallback.
type VoiceProcessingProvider interface {
	VoiceProcessingActive() bool
}

var _ VoiceProcessingProvider = (*voiceProcessingEndpoint)(nil)
