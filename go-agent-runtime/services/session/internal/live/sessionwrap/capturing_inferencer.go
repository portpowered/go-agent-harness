package sessionwrap

import (
	"context"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/internal/live/mediagate"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

// CapturingInferencerOptions configures provider media capture around an
// inferencer while keeping the runtime callbacks owned by the live service.
type CapturingInferencerOptions struct {
	Inner             messages.SessionInferencer
	Media             *mediagate.Gate
	Continuous        bool
	FlushOutbound     bool
	RequireInbound    bool
	RequireOutbound   bool
	OnDispatch        func(messages.StreamMessage)
	OnToolResult      func(string, string, bool) func()
	OnContinuation    func() func()
	OnOpeningAdmitted func()
	OnProviderDone    func(error)
	OnMediaAttached   func(bool)
}

// CapturingInferencer attaches optional provider media after session setup.
type CapturingInferencer struct {
	inner             messages.SessionInferencer
	media             *mediagate.Gate
	continuous        bool
	flushOutbound     bool
	requireInbound    bool
	requireOutbound   bool
	onDispatch        func(messages.StreamMessage)
	onToolResult      func(string, string, bool) func()
	onContinuation    func() func()
	onOpeningAdmitted func()
	onProviderDone    func(error)
	onMediaAttached   func(bool)
	captureMu         sync.Mutex
	captureFlush      func() error
	connectedSession  messages.Session
}

func NewCapturingInferencer(options CapturingInferencerOptions) *CapturingInferencer {
	return &CapturingInferencer{
		inner: options.Inner, media: options.Media, continuous: options.Continuous,
		flushOutbound: options.FlushOutbound, requireInbound: options.RequireInbound,
		requireOutbound: options.RequireOutbound, onDispatch: options.OnDispatch,
		onToolResult: options.OnToolResult, onContinuation: options.OnContinuation,
		onOpeningAdmitted: options.OnOpeningAdmitted, onProviderDone: options.OnProviderDone,
		onMediaAttached: options.OnMediaAttached,
	}
}

func (i *CapturingInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	s, err := i.inner.ConnectSession(ctx)
	if err != nil {
		i.media.Fail(err)
		return nil, err
	}
	if flusher, ok := i.inner.(interface{ FlushCapture() error }); ok {
		i.captureMu.Lock()
		i.captureFlush = flusher.FlushCapture
		i.captureMu.Unlock()
	}
	i.captureMu.Lock()
	i.connectedSession = s
	i.captureMu.Unlock()
	mediaAttached := false
	if providerMedia, ok := s.(sharedaudio.MediaSession); ok {
		endpoints := CaptureMediaEndpoints(s, providerMedia, i.continuous)
		mediaAttached = (!i.requireInbound || endpoints.Inbound != nil) &&
			(!i.requireOutbound || endpoints.Outbound != nil)
		i.media.Attach(ctx, endpoints)
	}
	if i.onMediaAttached != nil {
		i.onMediaAttached(mediaAttached)
	}
	if !mediaAttached {
		i.media.Fail(mediagate.ErrMediaUnavailable)
	}
	// Notify the live owner after the provider cleanup boundary, even if the
	// runner context is already canceled.
	if done := s.Done(); done != nil && i.onProviderDone != nil {
		go func() {
			<-done
			i.onProviderDone(i.TerminalError())
		}()
	}
	return WrapOrderedSession(s, OrderedSessionOptions{
		Media: i.media, FlushOutbound: i.flushOutbound, OnDispatch: i.onDispatch,
		OnToolResult: i.onToolResult, OnContinuation: i.onContinuation,
		OnOpeningAdmitted: i.onOpeningAdmitted,
	}), nil
}

// FlushCapture forwards provider capture finalization after session join.
func (i *CapturingInferencer) FlushCapture() error {
	if i == nil {
		return nil
	}
	i.captureMu.Lock()
	flush := i.captureFlush
	i.captureMu.Unlock()
	if flush == nil {
		return nil
	}
	return flush()
}

// TerminalError reads joined provider state without waiting for Done scheduling.
func (i *CapturingInferencer) TerminalError() error {
	i.captureMu.Lock()
	connected := i.connectedSession
	i.captureMu.Unlock()
	if provider, ok := connected.(interface{ TerminalError() error }); ok {
		return provider.TerminalError()
	}
	return nil
}
