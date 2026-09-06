package mediagate

import (
	"context"
	"sync"

	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

type inboundPort struct {
	mu                 sync.Mutex
	frames             chan sharedaudio.PCMFrame
	done               chan struct{}
	closeOnce          sync.Once
	err                error
	spaceMu            sync.Mutex
	space              chan struct{}
	controllerMu       sync.Mutex
	playbackController sharedaudio.PlaybackController
	onController       func(sharedaudio.PlaybackController)
}

// SetPlaybackController preserves the optional provider/device interruption
// capability across the pre-Start proxy. A room can register its device clock
// before the provider connects; attach forwards it when the provider endpoint
// becomes available.
func (p *inboundPort) SetPlaybackController(controller sharedaudio.PlaybackController) {
	if p == nil {
		return
	}
	p.controllerMu.Lock()
	p.playbackController = controller
	onController := p.onController
	p.controllerMu.Unlock()
	if onController != nil {
		onController(controller)
	}
}

func (p *inboundPort) getPlaybackController() sharedaudio.PlaybackController {
	if p == nil {
		return nil
	}
	p.controllerMu.Lock()
	defer p.controllerMu.Unlock()
	return p.playbackController
}

func newInboundPort(capacity int) *inboundPort {
	return &inboundPort{frames: make(chan sharedaudio.PCMFrame, capacity), done: make(chan struct{}), space: make(chan struct{})}
}

func (p *inboundPort) ReadFrame(ctx context.Context) (sharedaudio.PCMFrame, error) {
	if ctx == nil {
		return sharedaudio.PCMFrame{}, mediaContextRequired()
	}
	// Prefer already buffered media at teardown so a caller can drain a
	// provider's final frame before observing the terminal operation error.
	select {
	case frame := <-p.frames:
		p.notifySpace()
		return frame, nil
	default:
	}
	select {
	case frame := <-p.frames:
		p.notifySpace()
		return frame, nil
	case <-p.done:
		return sharedaudio.PCMFrame{}, p.operationError()
	case <-ctx.Done():
		return sharedaudio.PCMFrame{}, ctx.Err()
	}
}

func (p *inboundPort) push(ctx context.Context, frame sharedaudio.PCMFrame) error {
	if p == nil {
		return ErrMediaUnavailable
	}
	if ctx == nil {
		return mediaContextRequired()
	}
	if len(frame.Samples) > maxMediaFrameSamples {
		p.fail(ErrMediaQueueFull)
		return ErrMediaQueueFull
	}
	frame.Samples = append([]int16(nil), frame.Samples...)
	for {
		select {
		case <-p.done:
			return p.operationError()
		case <-ctx.Done():
			return ctx.Err()
		case p.frames <- frame:
			return nil
		default:
			if err := p.waitForSpace(ctx); err != nil {
				return err
			}
		}
	}
}

// waitForSpace keeps inbound media lossless while the physical device drains
// the bounded bridge. The provider read loop is allowed to apply transport
// backpressure, but it must never turn a burst into a fabricated queue-full
// terminal error.
func (p *inboundPort) waitForSpace(ctx context.Context) error {
	if p == nil {
		return ErrMediaUnavailable
	}
	if ctx == nil {
		return mediaContextRequired()
	}
	p.spaceMu.Lock()
	if len(p.frames) < cap(p.frames) {
		p.spaceMu.Unlock()
		return nil
	}
	space := p.space
	p.spaceMu.Unlock()
	select {
	case <-space:
		return nil
	case <-p.done:
		return p.operationError()
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *inboundPort) notifySpace() {
	if p == nil {
		return
	}
	p.spaceMu.Lock()
	close(p.space)
	p.space = make(chan struct{})
	p.spaceMu.Unlock()
}

func (p *inboundPort) fail(err error) {
	if p == nil {
		return
	}
	p.mu.Lock()
	if p.err == nil {
		p.err = err
	}
	p.mu.Unlock()
	p.closeOnce.Do(func() { close(p.done) })
}

func (p *inboundPort) close() { p.fail(sharedaudio.ErrSessionMediaClosed) }

func (p *inboundPort) Close() error {
	if p == nil {
		return nil
	}
	p.close()
	return nil
}

func (p *inboundPort) operationError() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return p.err
	}
	return sharedaudio.ErrSessionMediaClosed
}

type outboundPort struct {
	mu        sync.Mutex
	frames    chan outboundFrame
	done      chan struct{}
	closeOnce sync.Once
	err       error
	gate      *Gate
	spaceMu   sync.Mutex
	space     chan struct{}
	pendingMu sync.Mutex
	pending   int
	drained   chan struct{}
}

type outboundFrame struct {
	frame   sharedaudio.PCMFrame
	mediaID uint64
}

func newOutboundPort(capacity int) *outboundPort {
	return &outboundPort{
		frames:  make(chan outboundFrame, capacity),
		done:    make(chan struct{}),
		space:   make(chan struct{}),
		drained: make(chan struct{}),
	}
}

func (p *outboundPort) WriteFrame(ctx context.Context, frame sharedaudio.PCMFrame) error {
	if p == nil {
		return ErrMediaUnavailable
	}
	if ctx == nil {
		return mediaContextRequired()
	}
	if len(frame.Samples) == 0 {
		return sharedaudio.ErrSessionMediaEmptyFrame
	}
	if len(frame.Samples) > maxMediaFrameSamples {
		return ErrMediaQueueFull
	}
	mediaID, err := p.admitMedia()
	if err != nil {
		return err
	}
	frame.Samples = append([]int16(nil), frame.Samples...)
	err = p.enqueue(ctx, outboundFrame{frame: frame, mediaID: mediaID})
	if err != nil && mediaID != 0 {
		p.gate.markMediaDone(mediaID)
	}
	return err
}

func (p *outboundPort) admitMedia() (uint64, error) {
	if p.gate == nil {
		return 0, nil
	}
	return p.gate.AdmitMedia()
}

// enqueue owns the pending reservation until the frame reaches the worker.
// Every unsuccessful admission releases that reservation, including closure.
func (p *outboundPort) enqueue(ctx context.Context, frame outboundFrame) (err error) {
	select {
	case <-p.done:
		return p.operationError()
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	p.pendingMu.Lock()
	if p.pending == 0 {
		p.drained = make(chan struct{})
	}
	p.pending++
	p.pendingMu.Unlock()
	defer func() {
		if err != nil {
			p.complete()
		}
	}()
	for {
		select {
		case p.frames <- frame:
			return nil
		case <-p.done:
			return p.operationError()
		case <-ctx.Done():
			return ctx.Err()
		default:
			if err := p.waitForSpace(ctx); err != nil {
				return err
			}
		}
	}
}

func (p *outboundPort) next() (sharedaudio.PCMFrame, uint64, error) {
	select {
	case frame := <-p.frames:
		p.notifySpace()
		return frame.frame, frame.mediaID, nil
	case <-p.done:
		return sharedaudio.PCMFrame{}, 0, p.operationError()
	}
}

// waitForSpace gives bounded media writers a cancellation-aware admission
// path. Replay and finite sources may produce more frames than the bridge can
// forward in one scheduling turn; returning ErrMediaQueueFull would turn a
// valid capture into a false transport failure. The queue remains bounded and
// the writer wakes when the bridge removes a frame or the gate closes.
func (p *outboundPort) waitForSpace(ctx context.Context) error {
	if p == nil {
		return ErrMediaUnavailable
	}
	if ctx == nil {
		return mediaContextRequired()
	}
	p.spaceMu.Lock()
	if len(p.frames) < cap(p.frames) {
		p.spaceMu.Unlock()
		return nil
	}
	space := p.space
	p.spaceMu.Unlock()
	select {
	case <-space:
		return nil
	case <-p.done:
		return p.operationError()
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *outboundPort) notifySpace() {
	if p == nil {
		return
	}
	p.spaceMu.Lock()
	close(p.space)
	p.space = make(chan struct{})
	p.spaceMu.Unlock()
}

func (p *outboundPort) complete() {
	p.pendingMu.Lock()
	if p.pending > 0 {
		p.pending--
		if p.pending == 0 {
			close(p.drained)
		}
	}
	p.pendingMu.Unlock()
}

func (p *outboundPort) waitDrained(ctx context.Context) error {
	p.pendingMu.Lock()
	if p.pending == 0 {
		p.pendingMu.Unlock()
		return nil
	}
	drained := p.drained
	p.pendingMu.Unlock()
	select {
	case <-drained:
		return nil
	case <-p.done:
		return p.operationError()
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *outboundPort) fail(err error) {
	if p == nil {
		return
	}
	p.mu.Lock()
	if p.err == nil {
		p.err = err
	}
	p.mu.Unlock()
	p.closeOnce.Do(func() { close(p.done) })
}

func (p *outboundPort) close() { p.fail(sharedaudio.ErrSessionMediaClosed) }

func (p *outboundPort) Close() error {
	if p == nil {
		return nil
	}
	p.close()
	return nil
}

func (p *outboundPort) operationError() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return p.err
	}
	return sharedaudio.ErrSessionMediaClosed
}
