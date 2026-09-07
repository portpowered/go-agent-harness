package audio

import "context"

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
	// Format and lineage describe the samples at this boundary. Legacy
	// adapters may omit Format until their negotiated format is attached.
	Format      DeviceFormat
	StreamID    string
	Epoch       uint64
	Sequence    uint64
	StartSample uint64
	// EndOfResponse marks the provider audio-response boundary. Samples may
	// be empty when the preceding frame ended exactly on the media cadence.
	EndOfResponse bool
	// PlaybackResponse is populated by event-oriented provider adapters. It
	// lets a device sink reject a frame already read across a concurrent
	// server-VAD interruption without affecting ordinary RTP media.
	PlaybackResponse PlaybackResponse
}

// PlaybackResponse identifies one provider audio content part whose samples
// are being rendered by a local device. ItemID and ContentIndex are the
// coordinates required by Realtime providers when unplayed audio must be
// truncated from conversation history after a server-VAD interruption.
type PlaybackResponse struct {
	ResponseID   string
	ItemID       string
	ContentIndex int
}

// PlaybackInterruption is the device-observed playout boundary for one
// interrupted response. AudioEndMS is measured from samples actually consumed
// by the local device, not samples received or queued by the transport.
type PlaybackInterruption struct {
	PlaybackResponse
	AudioEndMS int
}

// PlaybackController is implemented by a clocked local playback sink. A
// provider-owned media adapter uses it to open a response playout interval and
// atomically stop that interval when server-side VAD reports user speech.
type PlaybackController interface {
	StartPlayback(PlaybackResponse)
	InterruptPlayback(PlaybackResponse) (audioEndMS int, ok bool)
}

// ActivePlaybackController is the lossless device-clock extension used when
// more than one provider response can be queued ahead of the speaker. The
// media reader's latest dequeued response may be newer than the response the
// device callback is actually rendering; implementations return the audible
// response identity together with its exact consumed duration.
type ActivePlaybackController interface {
	PlaybackController
	InterruptActivePlayback() (PlaybackInterruption, bool)
}

// PlaybackControlledInbound is the optional control seam implemented by
// provider media adapters that support device-clocked interruption. Ordinary
// RTP and file-backed inbound media need not implement it.
type PlaybackControlledInbound interface {
	InboundMedia
	SetPlaybackController(PlaybackController)
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

// MediaEndpoints are the caller-owned PCM endpoints exposed by an RTC session
// owner. The session owner creates and closes these endpoints; a local device
// runtime may only use them for the duration of the owning session.
type MediaEndpoints struct {
	Inbound  InboundMedia
	Outbound OutboundMedia
}

// MediaSession is an optional capability implemented by an RTC session owner.
// It lives in the shared transport package so provider implementations can
// expose their real tracks without importing an agent-cli internal package.
type MediaSession interface {
	RTCMedia() MediaEndpoints
}

// MediaSessionOptions selects optional provider-media behavior for a live
// session owner. InboundContinuous is intended for a raw streaming sink that
// must observe each provider delta promptly; ordinary RTC playback keeps the
// negotiated frame cadence and should leave it false.
type MediaSessionOptions struct {
	InboundContinuous bool
}

// ConfigurableMediaSession is an optional extension of MediaSession. Providers
// that can choose their inbound framing implement it without forcing every
// embedded session or test double to grow a provider-specific method.
type ConfigurableMediaSession interface {
	MediaSession
	RTCMediaWithOptions(MediaSessionOptions) MediaEndpoints
}

// NewSessionMediaAtRateWithOptions creates provider-owned media with an
// explicit inbound framing policy. Continuous inbound mode emits each
// currently available provider delta as a frame while retaining the response
// boundary as a separate empty marker when needed.
func NewSessionMediaAtRateWithOptions(writer SessionMediaWriter, sampleRate int, options MediaSessionOptions) *SessionMedia {
	media := NewSessionMediaAtRate(writer, sampleRate)
	media.inbound.emitAvailable = options.InboundContinuous
	return media
}

func (m *sessionInboundMedia) appendAvailableFrameLocked() {
	if len(m.pending) == 0 {
		return
	}
	samples := append([]int16(nil), m.pending...)
	m.frames = append(m.frames, PCMFrame{Samples: samples, PlaybackResponse: m.response})
	m.pending = nil
}

func (m *sessionInboundMedia) inboundFramesNeeded(incoming int) int {
	pending := len(m.pending) + incoming
	frames := pending / m.frameSamples
	if m.emitAvailable && pending%m.frameSamples != 0 {
		frames++
	}
	return frames
}

func (m *sessionInboundMedia) appendInboundFramesLocked() {
	m.appendCompleteFramesLocked()
	if m.emitAvailable {
		m.appendAvailableFrameLocked()
	}
}
