package file

import (
	"context"
	"fmt"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

// filePlayback is a direction-only compatibility handle. Audio processing,
// response boundaries and sink ownership are implemented by audioio.
type filePlayback struct {
	output audioio.Output

	mu      sync.Mutex
	used    bool
	done    chan struct{}
	pumpErr error
}

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
	return &filePlayback{output: output, done: make(chan struct{})}, nil
}

func (p *filePlayback) Pump(ctx context.Context, inbound sharedaudio.InboundMedia) (runErr error) {
	if p == nil || p.output == nil {
		return fmt.Errorf("%w: finite playback is unavailable", devices.ErrUnavailable)
	}
	p.mu.Lock()
	if p.used {
		p.mu.Unlock()
		return fmt.Errorf("%w: finite playback pump has already been started", devices.ErrInvalidRequest)
	}
	p.used = true
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		p.pumpErr = runErr
		close(p.done)
		p.mu.Unlock()
	}()
	return p.output.Pump(ctx, inbound)
}

// WaitForPump lets the live invocation drain provider-owned playback before
// closing the finite sink. Without this boundary, a normal provider terminal
// can close the file before its final inbound frame is consumed.
func (p *filePlayback) WaitForPump(ctx context.Context) error {
	if p == nil {
		return nil
	}
	if ctx == nil {
		return fmt.Errorf("%w: playback wait context is required", devices.ErrInvalidRequest)
	}
	p.mu.Lock()
	used := p.used
	done := p.done
	p.mu.Unlock()
	if !used {
		return nil
	}
	select {
	case <-done:
		p.mu.Lock()
		err := p.pumpErr
		p.mu.Unlock()
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *filePlayback) Close() error {
	if p == nil || p.output == nil {
		return nil
	}
	return p.output.Close()
}

var _ devices.Playback = (*filePlayback)(nil)
