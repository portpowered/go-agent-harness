// Package rtc defines the provider-neutral contract for a WebRTC-backed
// realtime transport.
//
// The data plane reuses package transport, preserving its endpoint, header,
// message, ownership, close, and error semantics. PCM media is a separate,
// frame-oriented seam for inbound and outbound mono signed PCM16 samples; it is
// never encoded as a data message.
//
// This package contains interfaces and values only. Its declarations have no
// network, goroutine, device, track, or protocol-state side effects; downstream
// signaling, peer-connection, and track lanes own those effects.
//
// Dependency decision (2026-08-16): pin Pion v4.2.18 as the pure-Go,
// MIT-licensed in-process library for owned connection/track lanes; retain
// MIT-licensed go2rtc for the later external-media-process source boundary.
// The PR records primary-source maintenance and binary-size evidence. No
// protocol-library concrete type crosses this package boundary.
package rtc

import (
	"context"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

// Dialer is the unchanged provider-neutral data dial contract.
//
// It is an alias rather than a copied method set so an RTC data dialer has the
// exact same endpoint, header ownership, connection ownership, and error
// behavior as transport.Dialer.
type Dialer = transport.Dialer

// Conn is the unchanged provider-neutral data connection contract.
//
// It is an alias rather than a parallel RTC message interface. In particular,
// PCM media must not be sent through these message methods.
type Conn = transport.Conn

// DataDialer names the data side explicitly; it is the same contract as Dialer.
type DataDialer = transport.Dialer

// DataConn names the data side explicitly; it is the same contract as Conn.
type DataConn = transport.Conn

// PCMFrame is one frame of mono, signed 16-bit PCM samples.
//
// Samples are caller-owned on outbound calls and receiver-owned on inbound
// returns. A media implementation must not mutate samples supplied to
// WriteFrame or retain them after WriteFrame returns. A successful ReadFrame
// must return a frame whose sample storage the caller may inspect and reuse;
// the implementation must not mutate or retain that storage after returning.
// Sample rate and framing cadence are negotiated/configured by the owning
// session; they are intentionally not protocol-library concepts in this type.
type PCMFrame struct {
	Samples []int16
}

// MediaEndpoint is the lifecycle seam shared by inbound and outbound media.
//
// Each endpoint returned with a nil error is caller-owned. The caller closes
// that endpoint exactly once when finished. Close releases the implementation's
// resources and preserves any underlying operation-error identity.
type MediaEndpoint interface {
	Close() error
}

// InboundMedia receives PCM frames from the remote peer.
//
// ReadFrame must preserve operation-error identity so errors.Is/errors.As can
// classify failures. A blocked read must observe ctx cancellation or deadline
// expiry and return an error that preserves the corresponding context error.
// The caller owns and closes every successful InboundMedia endpoint.
type InboundMedia interface {
	MediaEndpoint
	ReadFrame(ctx context.Context) (PCMFrame, error)
}

// OutboundMedia sends PCM frames to the remote peer.
//
// WriteFrame must complete the frame operation before returning nil, must not
// mutate frame.Samples, and must not retain the caller's samples after it
// returns. It must preserve operation-error identity and observe ctx
// cancellation or deadline expiry while blocked. The caller owns and closes
// every successful OutboundMedia endpoint.
type OutboundMedia interface {
	MediaEndpoint
	WriteFrame(ctx context.Context, frame PCMFrame) error
}
