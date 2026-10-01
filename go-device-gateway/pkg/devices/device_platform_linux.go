//go:build linux && cgo && !nomicrophone

package devices

import "github.com/gen2brain/malgo"

// NewPlatformDeviceRegistry returns the host's lazy audio-device registry.
// Device enumeration and native handles are acquired only when the caller
// invokes a registry operation.
func NewPlatformDeviceRegistry() DeviceRegistry { return NewDeviceRegistry() }

// linuxBackend pairs a malgo backend with its device-ID prefix.
type linuxBackend struct {
	backend malgo.Backend
	name    string
}

// linuxBackends lists the Linux backends in preference order: PulseAudio,
// then ALSA for directions PulseAudio does not provide.
func linuxBackends() [2]linuxBackend {
	return [...]linuxBackend{
		{backend: malgo.BackendPulseaudio, name: linuxPulseBackend},
		{backend: malgo.BackendAlsa, name: linuxAlsaBackend},
	}
}
