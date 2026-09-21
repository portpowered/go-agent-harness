package sessionadapter

import (
	"context"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/internal/live/mediagate"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

type MediaRequirements interface {
	SatisfiedBy(sharedaudio.MediaEndpoints) bool
}

type CaptureMedia func(messages.Session, sharedaudio.MediaSession, bool) sharedaudio.MediaEndpoints

type CaptureOptions struct {
	Inner             messages.SessionInferencer
	Media             *mediagate.Gate
	Continuous        bool
	FlushOutbound     bool
	Requirements      MediaRequirements
	CaptureMedia      CaptureMedia
	OnDispatch        func(messages.StreamMessage)
	OnToolResult      func(string, string, bool) func()
	OnContinuation    func() func()
	OnOpeningAdmitted func()
	OnProviderDone    func(error)
	OnMediaAttached   func(bool)
}

// CapturingInferencer attaches provider media after session setup.
type CapturingInferencer struct {
	inner             messages.SessionInferencer
	media             *mediagate.Gate
	continuous        bool
	flushOutbound     bool
	requirements      MediaRequirements
	captureMedia      CaptureMedia
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

func NewCapturingInferencer(options CaptureOptions) *CapturingInferencer {
	return &CapturingInferencer{
		inner: options.Inner, media: options.Media, continuous: options.Continuous,
		flushOutbound: options.FlushOutbound, requirements: options.Requirements,
		captureMedia: options.CaptureMedia, onDispatch: options.OnDispatch,
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
		endpoints := providerMedia.RTCMedia()
		if i.captureMedia != nil {
			endpoints = i.captureMedia(s, providerMedia, i.continuous)
		}
		mediaAttached = i.requirements == nil || i.requirements.SatisfiedBy(endpoints)
		i.media.Attach(ctx, endpoints)
	}
	if i.onMediaAttached != nil {
		i.onMediaAttached(mediaAttached)
	}
	if !mediaAttached {
		i.media.Fail(mediagate.ErrMediaUnavailable)
	}
	if done := s.Done(); done != nil && i.onProviderDone != nil {
		go func() {
			<-done
			i.onProviderDone(i.TerminalError())
		}()
	}
	return newOrderedSession(s, i.media, i.flushOutbound, i.onDispatch, i.onToolResult, i.onContinuation, i.onOpeningAdmitted), nil
}

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

func (i *CapturingInferencer) TerminalError() error {
	if i == nil {
		return nil
	}
	i.captureMu.Lock()
	connected := i.connectedSession
	i.captureMu.Unlock()
	if provider, ok := connected.(interface{ TerminalError() error }); ok {
		return provider.TerminalError()
	}
	return nil
}
