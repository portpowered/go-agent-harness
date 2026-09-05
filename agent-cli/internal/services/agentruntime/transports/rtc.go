package transports

import (
	"context"
	"errors"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport/rtc"
)

// SessionRuntimeSelection is the opaque, service-owned transport selection
// retained by a runtime plan. Endpoint and source values are intentionally
// strings: concrete signaling, peer, and media implementations stay behind
// the runtime factory and no protocol-specific type crosses the service API.
type SessionRuntimeSelection struct {
	Transport         string
	SignalingEndpoint string
	MediaSource       string
}

var (
	// ErrSessionRTCRuntimeUnavailable identifies a WebRTC selection for which
	// the application has not supplied the protocol-owning runtime factory.
	// A missing factory is an explicit setup failure; it must never fall back
	// to the WebSocket runtime.
	ErrSessionRTCRuntimeUnavailable = errors.New("WebRTC session runtime is unavailable")
	// ErrSessionRTCRuntimeClosed identifies a start attempted after the
	// runtime's caller-owned lifecycle has been closed.
	ErrSessionRTCRuntimeClosed = errors.New("WebRTC session runtime is closed")
	// ErrSessionRTCDataPlaneUnavailable identifies a runtime that completed
	// setup without returning the provider-facing data plane.
	ErrSessionRTCDataPlaneUnavailable = errors.New("WebRTC session data plane is unavailable")
)

// SessionRTCRuntime is the service boundary for one selected WebRTC session.
// Implementations own every resource created by Start and release it from
// Close. The service sees only provider-neutral transport interfaces.
type SessionRTCRuntime interface {
	Start(context.Context) (SessionRTCDataPlane, error)
	Close() error
}

// SessionRTCDataPlane is the provider-facing RTC data connection plus the
// separate media attachment seam. PCM frames never travel through Dial or
// transport.Conn message methods.
type SessionRTCDataPlane interface {
	transport.Dialer
	AttachInboundMedia(context.Context, sharedaudio.InboundMedia) error
	Close() error
}

// SessionRTCRuntimeFactory constructs an inert runtime owner for one exact
// service selection. Construction must not resolve endpoints, open media, or
// start asynchronous work; those effects belong to SessionRTCRuntime.Start.
type SessionRTCRuntimeFactory func(SessionRuntimeSelection) (SessionRTCRuntime, error)

// SessionRTCSignalingResolver resolves one opaque service endpoint through
// the provider-neutral RTC signaling contract.
type SessionRTCSignalingResolver func(context.Context, string) (rtc.Signaling, error)

// SessionRTCDataPlaneFactory creates the provider-facing RTC peer/data path
// after signaling has been resolved. The returned data plane is caller-owned.
type SessionRTCDataPlaneFactory func(context.Context, rtc.Signaling) (SessionRTCDataPlane, error)

// SessionRTCMediaSourceOpener parses and opens one opaque media-source value
// through the existing RTC media-source contract. The returned inbound media
// endpoint is caller-owned by the runtime.
type SessionRTCMediaSourceOpener func(context.Context, string) (sharedaudio.InboundMedia, error)

// SessionRTCComponents are the protocol-neutral dependencies needed by the
// service-owned runtime composition. Concrete signaling, peer, and media
// implementations remain behind these narrow function seams.
type SessionRTCComponents struct {
	ResolveSignaling SessionRTCSignalingResolver
	NewDataPlane     SessionRTCDataPlaneFactory
	OpenMediaSource  SessionRTCMediaSourceOpener
}
