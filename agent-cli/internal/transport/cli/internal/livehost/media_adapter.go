package livehost

import (
	"context"

	runtimeAudio "github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio"
	runtimeDevices "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
)

func OpenAudioInput(ctx context.Context, service runtimeAudio.Service, request runtimeAudio.InputRequest) (runtimeAudio.Input, error) {
	return service.OpenInput(ctx, request)
}

// OpenAudioOutput is the stateless host-to-service seam for an admitted audio
// output request. Processing state and ownership remain in runtimeAudio.
func OpenAudioOutput(ctx context.Context, service runtimeAudio.Service, request runtimeAudio.OutputRequest) (runtimeAudio.Output, error) {
	return service.OpenOutput(ctx, request)
}

func BindRTC(ctx context.Context, service runtimeDevices.Service, request runtimeDevices.RTCBindingRequest) (runtimeDevices.RTCBinding, error) {
	return service.BindRTC(ctx, request)
}
