package mediagate

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

const mediaQueueCapacity = 128

const liveControlAckPrefix = "go-agent-runtime-live-control-"

// FrameDirection identifies which side of a live media bridge observed a
// frame. The observer is internal to the session service; public recorders see
// the neutral session.LiveAudioRecord contract instead.
type FrameDirection uint8

const (
	FrameInbound FrameDirection = iota + 1
	FrameOutbound
)

type FrameObserver func(FrameDirection, sharedaudio.PCMFrame)

// A provider or device may not place an arbitrarily large byte slice in one
// frame. This cap keeps the bounded queue bounded by bytes as well as frames.
const maxMediaFrameSamples = 48_000

var (
	// ErrMediaUnavailable means the selected provider does not expose a PCM
	// media capability for this live session.
	ErrMediaUnavailable = session.ErrLiveMediaUnavailable
	// ErrMediaQueueFull means a caller attempted to enqueue more media than the
	// bounded session endpoint can retain while the provider is unavailable or
	// temporarily behind.
	ErrMediaQueueFull = errors.New("live media queue is full")
)

func mediaContextRequired() error {
	return errors.New("live media context is required")
}

// Gate is the host-facing media boundary. It owns bounded queues until a
// provider session is connected, then bridges those queues to the provider's
// caller-owned endpoints. No provider callback or unbounded buffer crosses the
// public LiveHandle boundary.
type Gate struct {
	inbound  *inboundPort
	outbound *outboundPort

	mu            sync.Mutex
	closed        bool
	closeErr      error
	closeDone     chan struct{}
	target        sharedaudio.MediaEndpoints
	ready         chan struct{}
	readyOnce     sync.Once
	bridgeCtx     context.Context
	bridgeCancel  context.CancelFunc
	inboundDone   chan struct{}
	inboundOnce   sync.Once
	inboundLinked bool
	onError       func(error)
	frameMu       sync.RWMutex
	onFrame       FrameObserver
	orderMu       sync.Mutex
	orderChanged  chan struct{}
	active        bool
	activeControl string
	mediaNext     uint64
	mediaDone     uint64
	mediaFinished map[uint64]struct{}
	controls      map[string]uint64
	ackMu         sync.Mutex
	acks          map[string]*controlAck
	nextAckID     uint64
	wg            sync.WaitGroup
}

type controlAck struct {
	accepted chan bool
	canceled atomic.Bool
}

func New(onError func(error)) *Gate {
	g := &Gate{
		inbound:       newInboundPort(mediaQueueCapacity),
		outbound:      newOutboundPort(mediaQueueCapacity),
		ready:         make(chan struct{}),
		inboundDone:   make(chan struct{}),
		closeDone:     make(chan struct{}),
		orderChanged:  make(chan struct{}),
		mediaFinished: make(map[uint64]struct{}),
		controls:      make(map[string]uint64),
		acks:          make(map[string]*controlAck),
		onError:       onError,
	}
	g.outbound.gate = g
	g.inbound.onController = g.setPlaybackController
	return g
}

func (g *Gate) Endpoints() sharedaudio.MediaEndpoints {
	if g == nil {
		return sharedaudio.MediaEndpoints{}
	}
	return sharedaudio.MediaEndpoints{Inbound: g.inbound, Outbound: g.outbound}
}

// SetFrameObserver installs a non-blocking observer for successfully bridged
// PCM. It is called before provider attachment by the live invocation owner;
// changing it later is safe and affects subsequent frames only.
func (g *Gate) SetFrameObserver(observer FrameObserver) {
	if g == nil {
		return
	}
	g.frameMu.Lock()
	g.onFrame = observer
	g.frameMu.Unlock()
}

func (g *Gate) observeFrame(direction FrameDirection, frame sharedaudio.PCMFrame) {
	if g == nil {
		return
	}
	g.frameMu.RLock()
	observer := g.onFrame
	g.frameMu.RUnlock()
	if observer == nil {
		return
	}
	frame.Samples = append([]int16(nil), frame.Samples...)
	observer(direction, frame)
}

func (g *Gate) WaitReady(ctx context.Context) error {
	if g == nil {
		return ErrMediaUnavailable
	}
	if ctx == nil {
		return mediaContextRequired()
	}
	select {
	case <-g.ready:
	case <-ctx.Done():
		return ctx.Err()
	}
	g.mu.Lock()
	target := g.target
	closed := g.closed
	g.mu.Unlock()
	if closed {
		return sharedaudio.ErrSessionMediaClosed
	}
	if target.Outbound == nil {
		return ErrMediaUnavailable
	}
	return nil
}

func (g *Gate) Attach(ctx context.Context, target sharedaudio.MediaEndpoints) {
	if g == nil {
		return
	}
	if ctx == nil {
		g.Fail(mediaContextRequired())
		g.report(closeMediaEndpoints(target))
		return
	}
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		g.report(closeMediaEndpoints(target))
		return
	}
	g.target = target
	// The provider session's lifetime owns media admission. Its run context is
	// cancelled during a graceful terminal boundary before the provider media
	// queue has necessarily drained; using that context directly would make the
	// bridge turn a healthy final tail into context.Canceled. Keep the bridge
	// cancellable by this gate while retaining the caller's values, and let
	// Close perform the explicit teardown cancellation for failure paths.
	g.bridgeCtx, g.bridgeCancel = context.WithCancel(context.WithoutCancel(ctx))
	bridgeCtx := g.bridgeCtx
	var inbound, outbound bool
	if target.Inbound != nil {
		g.wg.Add(1)
		g.inboundLinked = true
		inbound = true
	}
	if target.Outbound != nil {
		g.wg.Add(1)
		outbound = true
	}
	g.readyOnce.Do(func() { close(g.ready) })
	g.mu.Unlock()

	if target.Inbound == nil {
		g.inbound.fail(ErrMediaUnavailable)
	} else if inbound {
		if controller := g.inbound.getPlaybackController(); controller != nil {
			if controlled, ok := target.Inbound.(sharedaudio.PlaybackControlledInbound); ok {
				controlled.SetPlaybackController(controller)
			}
		}
		go g.bridgeInbound(bridgeCtx, target.Inbound) //nolint:contextcheck // bridgeCtx deliberately outlives runner cancellation until Gate.Close.
	}
	if target.Outbound == nil {
		g.outbound.fail(ErrMediaUnavailable)
	} else if outbound {
		go g.bridgeOutbound(bridgeCtx, target.Outbound) //nolint:contextcheck // bridgeCtx deliberately outlives runner cancellation until Gate.Close.
	}
}

func (g *Gate) setPlaybackController(controller sharedaudio.PlaybackController) {
	if g == nil {
		return
	}
	g.mu.Lock()
	target := g.target.Inbound
	g.mu.Unlock()
	if controlled, ok := target.(sharedaudio.PlaybackControlledInbound); ok {
		controlled.SetPlaybackController(controller)
	}
}

func (g *Gate) Fail(err error) {
	if err == nil {
		err = ErrMediaUnavailable
	}
	g.inbound.fail(err)
	g.outbound.fail(err)
	g.report(err)
	g.readyOnce.Do(func() { close(g.ready) })
}

func (g *Gate) report(err error) {
	if g == nil || err == nil || errors.Is(err, ErrMediaUnavailable) || errors.Is(err, sharedaudio.ErrSessionMediaClosed) {
		return
	}
	if g.onError != nil {
		g.onError(err)
	}
}

func (g *Gate) bridgeInbound(ctx context.Context, source sharedaudio.InboundMedia) {
	defer g.inboundOnce.Do(func() { close(g.inboundDone) })
	defer g.wg.Done()
	for {
		frame, err := source.ReadFrame(ctx)
		if err != nil {
			g.inbound.fail(err)
			g.report(err)
			return
		}
		if err := g.inbound.push(frame); err != nil {
			g.report(err)
			return
		}
		g.observeFrame(FrameInbound, frame)
	}
}

// DrainInbound waits for the provider-owned inbound bridge to finish copying
// frames that were admitted before normal session completion. It is a
// graceful completion operation; cancellation callers should use Close so the
// bridge is stopped immediately. The provider session is expected to have
// already terminated, so a healthy bridge reaches inboundDone after its
// source exposes all queued frames.
func (g *Gate) DrainInbound(ctx context.Context) error {
	if g == nil {
		return nil
	}
	if ctx == nil {
		return mediaContextRequired()
	}
	g.mu.Lock()
	linked := g.inboundLinked
	g.mu.Unlock()
	if !linked {
		return nil
	}
	select {
	case <-g.inboundDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (g *Gate) bridgeOutbound(ctx context.Context, target sharedaudio.OutboundMedia) {
	defer g.wg.Done()
	for {
		frame, mediaID, err := g.outbound.next()
		if err != nil {
			return
		}
		release, err := g.beginMedia(ctx, mediaID)
		if err != nil {
			g.outbound.complete()
			return
		}
		if err := target.WriteFrame(ctx, frame); err != nil {
			g.outbound.complete()
			release()
			g.outbound.fail(err)
			g.report(err)
			return
		}
		g.observeFrame(FrameOutbound, frame)
		g.outbound.complete()
		release()
	}
}

func (g *Gate) Close() error {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	if g.closed {
		closeDone := g.closeDone
		g.mu.Unlock()
		<-closeDone
		g.mu.Lock()
		closeErr := g.closeErr
		g.mu.Unlock()
		return closeErr
	}
	g.closed = true
	target := g.target
	cancel := g.bridgeCancel
	g.mu.Unlock()

	// Wake provider and media admission waiters before joining bridge workers.
	// Close is a teardown boundary, so no waiter may remain behind a pending
	// control or an unconsumed media barrier.
	g.orderMu.Lock()
	g.notifyOrderLocked()
	g.orderMu.Unlock()

	if cancel != nil {
		cancel()
	}
	// A control sender may be waiting for the provider session's synchronous
	// acknowledgement while teardown closes the provider. Wake it before
	// joining bridge workers so Close remains cancellation-prioritized.
	g.cancelAcks()
	g.inbound.close()
	g.outbound.close()
	closeErr := closeMediaEndpoints(target)
	g.mu.Lock()
	g.closeErr = closeErr
	g.mu.Unlock()
	g.wg.Wait()
	close(g.closeDone)
	return closeErr
}
