package file

import (
	"context"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

// filePlayback is a direction-only compatibility handle. Audio processing,
// response boundaries and sink ownership are implemented by audioio.
type filePlayback struct{ output audioio.Output }

func newPlayback(ctx context.Context, request devices.FileOutput, providerRate int, service audioio.Service) (*filePlayback, error) {
	if request.Sink == nil {
		return nil, fmt.Errorf("%w: finite playback sink is nil", devices.ErrInvalidRequest)
	}
	if request.SampleRate < 0 || providerRate < 0 {
		return nil, fmt.Errorf("%w: audio sample rates must not be negative", devices.ErrInvalidRequest)
	}
	if service == nil {
		return nil, fmt.Errorf("%w: audio service is unavailable", devices.ErrUnavailable)
	}
	output, err := service.OpenOutput(ctx, audioio.OutputRequest{
		Sink: request.Sink, SinkRate: request.SampleRate, ProviderRate: providerRate,
		Continuous: request.Continuous,
	})
	if err != nil {
		return nil, err
	}
	return &filePlayback{output: output}, nil
}

func (p *filePlayback) Pump(ctx context.Context, inbound sharedaudio.InboundMedia) error {
	if p == nil || p.output == nil {
		return fmt.Errorf("%w: finite playback is unavailable", devices.ErrUnavailable)
	}
	return p.output.Pump(ctx, inbound)
}

func (p *filePlayback) Close() error {
	if p == nil || p.output == nil {
		return nil
	}
	return p.output.Close()
}

var _ devices.Playback = (*filePlayback)(nil)
