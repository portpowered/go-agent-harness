package composite

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

type fanoutPlayback struct {
	children []devices.Playback
	queues   []*frameInbox
	control  audio.PlaybackController

	mu      sync.Mutex
	used    bool
	closed  bool
	cancel  context.CancelFunc
	done    chan struct{}
	started chan struct{}
	pumpErr error
}

func newFanoutPlayback(children []devices.Playback) (*fanoutPlayback, error) {
	if len(children) < 2 {
		return nil, fmt.Errorf("%w: playback fan-out needs at least two children", devices.ErrInvalidRequest)
	}
	queues := make([]*frameInbox, len(children))
	for index := range queues {
		queues[index] = newFrameInbox()
	}
	return &fanoutPlayback{
		children: append([]devices.Playback(nil), children...),
		queues:   queues,
		control:  playbackController(children),
		done:     make(chan struct{}),
		started:  make(chan struct{}),
	}, nil
}

func playbackController(children []devices.Playback) audio.PlaybackController {
	for _, child := range children {
		provider, ok := child.(devices.PlaybackControllerProvider)
		if ok {
			if controller := provider.PlaybackController(); controller != nil {
				return controller
			}
		}
	}
	return nil
}

func (p *fanoutPlayback) Pump(ctx context.Context, inbound audio.InboundMedia) (runErr error) {
	if p == nil {
		return fmt.Errorf("%w: playback fan-out is unavailable", devices.ErrUnavailable)
	}
	if ctx == nil {
		return fmt.Errorf("%w: playback context is required", devices.ErrInvalidRequest)
	}
	if inbound == nil {
		return fmt.Errorf("%w: provider inbound media is nil", devices.ErrInvalidRequest)
	}
	runCtx, cancel, err := p.beginPump(ctx)
	if err != nil {
		return err
	}
	defer func() { p.finishPump(runErr) }()
	defer cancel()

	var failures playbackFailures
	workers := p.startChildren(runCtx, &failures, cancel)

	terminal := p.readAndFanOut(runCtx, inbound, &failures, cancel)
	p.closeQueues(terminal, &failures)
	workers.Wait()
	return p.result(terminal, &failures)
}

func (p *fanoutPlayback) startChildren(ctx context.Context, failures *playbackFailures, cancel context.CancelFunc) *sync.WaitGroup {
	var workers sync.WaitGroup
	report := func(err error) {
		if failures.record(err) {
			cancel()
		}
	}
	for index, child := range p.children {
		workers.Add(1)
		go func(index int, child devices.Playback) {
			defer workers.Done()
			if err := child.Pump(ctx, p.queues[index]); err != nil {
				report(err)
			}
		}(index, child)
	}
	return &workers
}

func (p *fanoutPlayback) closeQueues(terminal error, failures *playbackFailures) {
	queueTerminal := terminal
	if err := failures.get(); err != nil {
		queueTerminal = err
	}
	for _, queue := range p.queues {
		queue.closeWithError(queueTerminal)
	}
}

func (p *fanoutPlayback) result(terminal error, failures *playbackFailures) error {
	if err := failures.get(); err != nil {
		if p.isClosed() && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
			return nil
		}
		return err
	}
	if terminal == nil || errors.Is(terminal, io.EOF) || errors.Is(terminal, audio.ErrSessionMediaClosed) {
		return nil
	}
	if p.isClosed() && (errors.Is(terminal, context.Canceled) || errors.Is(terminal, context.DeadlineExceeded)) {
		return nil
	}
	return terminal
}

func (p *fanoutPlayback) readAndFanOut(ctx context.Context, inbound audio.InboundMedia, failures *playbackFailures, cancel context.CancelFunc) error {
	for {
		frame, err := inbound.ReadFrame(ctx)
		if err != nil {
			if cleanPlaybackError(err) {
				return io.EOF
			}
			failures.record(err)
			cancel()
			return err
		}
		if err := p.sendFrame(ctx, frame); err != nil {
			failures.record(err)
			cancel()
			return err
		}
		if err := failures.get(); err != nil {
			return err
		}
	}
}

func (p *fanoutPlayback) sendFrame(ctx context.Context, frame audio.PCMFrame) error {
	for _, queue := range p.queues {
		if err := queue.send(ctx, cloneFrame(frame)); err != nil {
			return err
		}
	}
	return nil
}

func (p *fanoutPlayback) beginPump(ctx context.Context) (context.Context, context.CancelFunc, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, nil, fmt.Errorf("%w: playback fan-out is closed", devices.ErrUnavailable)
	}
	if p.used {
		return nil, nil, errors.New("playback fan-out pump has already been started")
	}
	p.used = true
	runCtx, cancel := context.WithCancel(ctx)
	p.cancel = cancel
	close(p.started)
	return runCtx, cancel, nil
}

func (p *fanoutPlayback) finishPump(err error) {
	p.mu.Lock()
	p.pumpErr = err
	close(p.done)
	p.mu.Unlock()
}

func (p *fanoutPlayback) isClosed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closed
}

func (p *fanoutPlayback) WaitForPump(ctx context.Context) error {
	if p == nil {
		return nil
	}
	if ctx == nil {
		return fmt.Errorf("%w: playback wait context is required", devices.ErrInvalidRequest)
	}
	p.mu.Lock()
	used := p.used
	p.mu.Unlock()
	if !used {
		return nil
	}
	select {
	case <-p.done:
		p.mu.Lock()
		err := p.pumpErr
		p.mu.Unlock()
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *fanoutPlayback) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	if !p.closed {
		p.closed = true
		if p.cancel != nil {
			p.cancel()
		}
	}
	used := p.used
	done := p.done
	p.mu.Unlock()
	for _, queue := range p.queues {
		queue.closeWithError(audio.ErrSessionMediaClosed)
	}
	if used {
		<-done
	}
	return nil
}

func (p *fanoutPlayback) PlaybackController() audio.PlaybackController {
	if p == nil {
		return nil
	}
	return p.control
}

var _ devices.Playback = (*fanoutPlayback)(nil)
var _ devices.PlaybackControllerProvider = (*fanoutPlayback)(nil)
