package file

import (
	"context"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

// fileCapture is a direction-only compatibility handle. Audio ownership is
// in audioio; devices/file only maps the service-owned stream to the existing
// devices.Capture contract.
type fileCapture struct{ input audioio.Input }

func newCapture(ctx context.Context, request devices.FileInput, providerRate int, service audioio.Service) (*fileCapture, error) {
	if request.Source == nil {
		return nil, fmt.Errorf("%w: finite capture source is nil", devices.ErrInvalidRequest)
	}
	if request.SampleRate < 0 || providerRate < 0 {
		return nil, fmt.Errorf("%w: audio sample rates must not be negative", devices.ErrInvalidRequest)
	}
	if request.Pace && request.Scheduler == nil {
		return nil, fmt.Errorf("%w: finite input pacing requires a scheduler", devices.ErrInvalidRequest)
	}
	if service == nil {
		return nil, fmt.Errorf("%w: audio service is unavailable", devices.ErrUnavailable)
	}
	input, err := service.OpenInput(ctx, audioio.InputRequest{
		Source: request.Source, SourceRate: request.SampleRate, ProviderRate: providerRate,
		Pace: request.Pace, Continuous: request.Continuous, OnTurnBoundary: request.OnTurnBoundary,
		Scheduler: request.Scheduler,
	})
	if err != nil {
		return nil, err
	}
	return &fileCapture{input: input}, nil
}

func (c *fileCapture) Pump(ctx context.Context, outbound sharedaudio.OutboundMedia) error {
	if c == nil || c.input == nil {
		return fmt.Errorf("%w: finite capture is unavailable", devices.ErrUnavailable)
	}
	return c.input.Pump(ctx, outbound)
}

func (c *fileCapture) Close() error {
	if c == nil || c.input == nil {
		return nil
	}
	return c.input.Close()
}

var _ devices.Capture = (*fileCapture)(nil)
