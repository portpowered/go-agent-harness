package livehost

import (
	"context"

	runtimeAudio "github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio"
)

// OpenAudioInput is the stateless host-to-service seam for an admitted audio
// input request. Processing state and ownership remain in runtimeAudio.
func OpenAudioInput(ctx context.Context, service runtimeAudio.Service, request runtimeAudio.InputRequest) (runtimeAudio.Input, error) {
	return service.OpenInput(ctx, request)
}

// OpenAudioOutput is the stateless host-to-service seam for an admitted audio
// output request. Processing state and ownership remain in runtimeAudio.
func OpenAudioOutput(ctx context.Context, service runtimeAudio.Service, request runtimeAudio.OutputRequest) (runtimeAudio.Output, error) {
	return service.OpenOutput(ctx, request)
}
