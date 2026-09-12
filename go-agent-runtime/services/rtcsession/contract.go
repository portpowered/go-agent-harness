// Package rtcsession owns the host-neutral lifecycle boundary for one live RTC
// session. Provider selection and artifact policy stay at the host edge; this
// package receives only opaque endpoint selections and injected components.
package rtcsession

import (
	"context"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport/rtc"
)

// SessionRuntimeSelection is the opaque selection for one RTC invocation.
// Concrete signaling, peer, and media implementations remain behind the
// injected component seams.
type SessionRuntimeSelection struct {
	Transport         string
	SignalingEndpoint string
	MediaSource       string
}

type sessionRTCError string

func (e sessionRTCError) Error() string { return string(e) }

const (
	// ErrSessionRTCRuntimeUnavailable identifies an incomplete RTC composition.
	ErrSessionRTCRuntimeUnavailable sessionRTCError = "WebRTC session runtime is unavailable"
	// ErrSessionRTCRuntimeClosed identifies a start attempted after Close.
	ErrSessionRTCRuntimeClosed sessionRTCError = "WebRTC session runtime is closed"
	// ErrSessionRTCDataPlaneUnavailable identifies a missing data plane.
	ErrSessionRTCDataPlaneUnavailable sessionRTCError = "WebRTC session data plane is unavailable"
)

// SessionRTCRuntimeError adds phase context while preserving the original
// signaling, peer, media, provider, or context error for errors.Is/errors.As.
type SessionRTCRuntimeError struct {
	Phase string
	Err   error
}

func (e *SessionRTCRuntimeError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Phase == "" {
		return "WebRTC session runtime: " + errorText(e.Err)
	}
	if e.Err == nil {
		return "WebRTC session runtime " + e.Phase
	}
	return "WebRTC session runtime " + e.Phase + ": " + e.Err.Error()
}

func (e *SessionRTCRuntimeError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func errorText(err error) string {
	if err == nil {
		return "<nil>"
	}
	return err.Error()
}

// SessionRTCRuntime owns the resources acquired for one selected session.
// Construction is inert; Start performs lazy signaling, data-plane, and media
// acquisition, and Close is bounded and idempotent.
type SessionRTCRuntime interface {
	Start(context.Context) (SessionRTCDataPlane, error)
	Close() error
}

// SessionRTCDataPlane is the provider-facing RTC data connection and media
// attachment seam. PCM frames do not travel through transport.Conn messages.
type SessionRTCDataPlane interface {
	transport.Dialer
	AttachInboundMedia(context.Context, sharedaudio.InboundMedia) error
	Close() error
}

// SessionRTCRuntimeFactory constructs an inert runtime for one selection.
type SessionRTCRuntimeFactory func(SessionRuntimeSelection) (SessionRTCRuntime, error)

// SessionRTCSignalingResolver resolves an opaque endpoint through the
// provider-neutral signaling contract.
type SessionRTCSignalingResolver func(context.Context, string) (rtc.Signaling, error)

// SessionRTCDataPlaneFactory creates the provider-facing RTC peer/data path.
type SessionRTCDataPlaneFactory func(context.Context, rtc.Signaling) (SessionRTCDataPlane, error)

// SessionRTCMediaSourceOpener opens one opaque inbound media source.
type SessionRTCMediaSourceOpener func(context.Context, string) (sharedaudio.InboundMedia, error)

// SessionRTCComponents are the explicit dependencies needed to compose one
// runtime. The service does not resolve configuration or read environment
// state.
type SessionRTCComponents struct {
	ResolveSignaling SessionRTCSignalingResolver
	NewDataPlane     SessionRTCDataPlaneFactory
	OpenMediaSource  SessionRTCMediaSourceOpener
}

// Inferencer is a provider session wrapper that owns the RTC runtime together
// with the provider session and exposes an idempotent finalizer to the host.
type Inferencer interface {
	messages.SessionInferencer
	CloseSession() error
}

// Service composes RTC runtime owners and their provider-session adapters.
// Implementations are private; applications receive this contract from the
// dedicated Wire graph.
type Service interface {
	NewRuntime(SessionRuntimeSelection) (SessionRTCRuntime, error)
	WrapInferencer(SessionRTCRuntime, messages.SessionInferencer) Inferencer
	NewLazyDialer(SessionRTCRuntime) transport.Dialer
}
