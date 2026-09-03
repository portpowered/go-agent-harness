package rtc

import (
	"context"
	"errors"
	"io"
	"sync"
)

const (
	// DefaultSessionMediaFrameSamples is the 30 ms PCM frame size used by the
	// realtime session media adapters at 16 kHz.
	DefaultSessionMediaFrameSamples = 480
	// DefaultSessionMediaSampleRate retains the original low-level SessionMedia
	// contract for callers that do not supply a negotiated provider rate.
	DefaultSessionMediaSampleRate = 16000
	sessionMediaFrameMillis       = 30
	// Five minutes of queued 30 ms frames is a defensive memory ceiling, not
	// a latency target. Normal playback starts immediately and drains this
	// backlog concurrently. Crossing the ceiling fails explicitly instead of
	// corrupting audio by dropping an arbitrary part of the response.
	sessionMediaMaxQueuedFrames = 10_000
)

var (
	// ErrSessionMediaClosed indicates that a session-owned media endpoint has
	// been closed as part of session teardown.
	ErrSessionMediaClosed = errors.New("RTC session media is closed")
	// ErrSessionMediaEmptyFrame indicates that an outbound media frame had no
	// samples to send.
	ErrSessionMediaEmptyFrame = errors.New("RTC session media frame is empty")
	// ErrSessionMediaNoWriter indicates that no provider media writer was
	// supplied when the session media adapter was created.
	ErrSessionMediaNoWriter = errors.New("RTC session media outbound writer is unavailable")
	// ErrSessionMediaInboundBacklog indicates that a provider delivered more
	// than the defensive five-minute lossless playback backlog can retain.
	ErrSessionMediaInboundBacklog = errors.New("RTC session media inbound backlog limit exceeded")
)

// SessionMediaWriter sends one normalized PCM frame through a provider-owned
// realtime session.
type SessionMediaWriter func(context.Context, PCMFrame) error

// SessionMedia adapts a provider's event-oriented audio stream to the
// frame-oriented RTC media interfaces. The provider owns this object and
// exposes its endpoints through MediaSession.
type SessionMedia struct {
	inbound  *sessionInboundMedia
	outbound *sessionOutboundMedia
}

// NewSessionMedia creates a provider-owned media adapter. Inbound samples are
// assembled into DefaultSessionMediaFrameSamples-sized frames.
func NewSessionMedia(writer SessionMediaWriter) *SessionMedia {
	return &SessionMedia{
		inbound:  newSessionInboundMedia(DefaultSessionMediaFrameSamples, DefaultSessionMediaSampleRate, true),
		outbound: newSessionOutboundMedia(writer),
	}
}

// NewSessionMediaAtRate creates a provider-owned media adapter whose inbound
// frame cadence is 30 ms at sampleRate. A non-positive rate retains the 16 kHz
// compatibility default. Unlike the legacy constructor, a response-final
// partial frame remains exact rather than being padded with additional audio.
func NewSessionMediaAtRate(writer SessionMediaWriter, sampleRate int) *SessionMedia {
	if sampleRate <= 0 {
		sampleRate = DefaultSessionMediaSampleRate
	}
	frameSamples := sampleRate * sessionMediaFrameMillis / 1000
	if frameSamples <= 0 {
		frameSamples = DefaultSessionMediaFrameSamples
	}
	return &SessionMedia{
		inbound:  newSessionInboundMedia(frameSamples, sampleRate, false),
		outbound: newSessionOutboundMedia(writer),
	}
}

// Endpoints returns the media endpoints owned by the session.
func (m *SessionMedia) Endpoints() MediaEndpoints {
	if m == nil {
		return MediaEndpoints{}
	}
	return MediaEndpoints{
		Inbound:  m.inbound,
		Outbound: m.outbound,
	}
}

// PushInbound appends PCM samples received from the provider. Complete frames
// become available to the Inbound endpoint in lossless FIFO order. Providers
// can deliver an entire response faster than a physical device can play it,
// so this queue must retain that response backlog rather than silently
// discard old audio before device-owned pacing can apply.
func (m *SessionMedia) PushInbound(samples []int16) error {
	if m == nil || m.inbound == nil {
		return ErrSessionMediaClosed
	}
	return m.inbound.push(samples)
}

// StartInboundResponse associates following inbound PCM with one provider
// response. Repeated calls for the same response are ignored.
func (m *SessionMedia) StartInboundResponse(response PlaybackResponse) {
	if m == nil || m.inbound == nil || response.ItemID == "" {
		return
	}
	m.inbound.startResponse(response)
}

// InterruptInbound discards PCM that has not reached the device and asks the
// clocked playback controller to discard its native queue. The returned
// boundary is suitable for a provider conversation-item truncation event.
func (m *SessionMedia) InterruptInbound() (PlaybackInterruption, bool) {
	if m == nil || m.inbound == nil {
		return PlaybackInterruption{}, false
	}
	return m.inbound.interrupt()
}

// FlushInbound emits a final zero-padded frame for samples remaining at the
// end of a provider audio response.
func (m *SessionMedia) FlushInbound() error {
	if m == nil || m.inbound == nil {
		return ErrSessionMediaClosed
	}
	return m.inbound.flush()
}

// FailInbound makes the next Inbound.ReadFrame return err after queued frames
// have been consumed.
func (m *SessionMedia) FailInbound(err error) {
	if m == nil || m.inbound == nil {
		return
	}
	m.inbound.fail(err)
}

// Close closes both media directions. It does not close the provider session
// itself; the owning provider session performs that operation.
func (m *SessionMedia) Close() error {
	if m == nil {
		return nil
	}
	if m.inbound != nil {
		m.inbound.close()
	}
	if m.outbound != nil {
		m.outbound.close()
	}
	return nil
}

type sessionOutboundMedia struct {
	writer    SessionMediaWriter
	done      chan struct{}
	closeOnce sync.Once
}

func newSessionOutboundMedia(writer SessionMediaWriter) *sessionOutboundMedia {
	return &sessionOutboundMedia{
		writer: writer,
		done:   make(chan struct{}),
	}
}

func (m *sessionOutboundMedia) WriteFrame(ctx context.Context, frame PCMFrame) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(frame.Samples) == 0 {
		return ErrSessionMediaEmptyFrame
	}
	select {
	case <-m.done:
		return ErrSessionMediaClosed
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if m.writer == nil {
		return ErrSessionMediaNoWriter
	}

	// Do not let a provider retain or mutate the caller's frame buffer.
	samples := append([]int16(nil), frame.Samples...)
	return m.writer(ctx, PCMFrame{Samples: samples})
}

func (m *sessionOutboundMedia) Close() error {
	m.close()
	return nil
}

func (m *sessionOutboundMedia) close() {
	m.closeOnce.Do(func() {
		close(m.done)
	})
}

type sessionInboundMedia struct {
	mu           sync.Mutex
	frameSamples int
	sampleRate   int
	padPartial   bool
	pending      []int16
	frames       []PCMFrame
	terminal     error
	closed       bool
	done         chan struct{}
	wake         chan struct{}
	closeOnce    sync.Once
	response     PlaybackResponse
	// responseSamples retains provider-rate PCM by response. Network delivery
	// can open later responses while an earlier response is still reaching the
	// physical device, so one mutable counter cannot cap the audible response's
	// truncation cursor correctly.
	responseSamples map[PlaybackResponse]uint64
	// playbackResponse is the response whose first FIFO frame was most recently
	// dequeued for the device. It deliberately trails response when the provider
	// sends a tool continuation faster than real-time playback.
	playbackResponse PlaybackResponse
	interrupted      PlaybackResponse
	discarding       bool
	controller       PlaybackController
}

func newSessionInboundMedia(frameSamples, sampleRate int, padPartial bool) *sessionInboundMedia {
	return &sessionInboundMedia{
		frameSamples:    frameSamples,
		sampleRate:      sampleRate,
		padPartial:      padPartial,
		done:            make(chan struct{}),
		wake:            make(chan struct{}, 1),
		responseSamples: make(map[PlaybackResponse]uint64),
	}
}

func (m *sessionInboundMedia) ReadFrame(ctx context.Context) (PCMFrame, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		m.mu.Lock()
		if len(m.frames) > 0 {
			frame := m.frames[0]
			m.frames[0] = PCMFrame{}
			m.frames = m.frames[1:]
			if len(frame.Samples) > 0 {
				m.activatePlaybackLocked(frame.PlaybackResponse)
			}
			m.mu.Unlock()
			return frame, nil
		}
		if m.terminal != nil {
			err := m.terminal
			m.mu.Unlock()
			return PCMFrame{}, err
		}
		if m.closed {
			m.mu.Unlock()
			return PCMFrame{}, ErrSessionMediaClosed
		}
		m.mu.Unlock()

		select {
		case <-m.done:
			// Re-check the queue so frames published immediately before close
			// remain observable to the consumer.
		case <-m.wake:
		case <-ctx.Done():
			return PCMFrame{}, ctx.Err()
		}
	}
}

func (m *sessionInboundMedia) SetPlaybackController(controller PlaybackController) {
	m.mu.Lock()
	m.controller = controller
	if controller != nil {
		if m.playbackResponse.ItemID != "" {
			controller.StartPlayback(m.playbackResponse)
		} else {
			m.activateQueuedPlaybackLocked()
		}
	}
	m.mu.Unlock()
}

// activateQueuedPlaybackLocked synchronously identifies the audible FIFO head
// as soon as it exists. This makes a server-VAD event immediately following an
// audio delta deterministic even when the device pump goroutine has not yet
// been scheduled. A later ingress response cannot replace this identity; its
// frames remain behind the current response until ReadFrame reaches them.
func (m *sessionInboundMedia) activateQueuedPlaybackLocked() {
	if m.playbackResponse.ItemID != "" {
		return
	}
	for _, frame := range m.frames {
		if len(frame.Samples) > 0 && frame.PlaybackResponse.ItemID != "" {
			m.activatePlaybackLocked(frame.PlaybackResponse)
			return
		}
	}
}

func (m *sessionInboundMedia) activatePlaybackLocked(response PlaybackResponse) {
	if response.ItemID == "" || response == m.playbackResponse {
		return
	}
	previous := m.playbackResponse
	m.playbackResponse = response
	if previous.ItemID != "" {
		delete(m.responseSamples, previous)
	}
	if m.controller != nil {
		m.controller.StartPlayback(response)
	}
}

func (m *sessionInboundMedia) startResponse(response PlaybackResponse) {
	m.mu.Lock()
	if m.discarding && m.interrupted == response {
		m.mu.Unlock()
		return
	}
	if m.closed || m.response == response {
		m.mu.Unlock()
		return
	}
	m.discarding = false
	m.interrupted = PlaybackResponse{}
	m.response = response
	if m.responseSamples == nil {
		m.responseSamples = make(map[PlaybackResponse]uint64)
	}
	m.responseSamples[response] = 0
	m.mu.Unlock()
}

func (m *sessionInboundMedia) interrupt() (PlaybackInterruption, bool) {
	m.mu.Lock()
	response := m.playbackResponse
	responseSamples := m.responseSamples[response]
	ingressResponse := m.response
	controller := m.controller
	for index := range m.frames {
		m.frames[index] = PCMFrame{}
	}
	m.frames = nil
	m.pending = nil
	m.response = PlaybackResponse{}
	m.playbackResponse = PlaybackResponse{}
	m.responseSamples = make(map[PlaybackResponse]uint64)
	// Discard late deltas from the newest ingress response in this cancelled
	// chain. The audible response can be older when tool continuations have
	// already arrived and queued behind it.
	m.interrupted = ingressResponse
	m.discarding = ingressResponse.ItemID != ""
	if controller == nil || response.ItemID == "" {
		m.mu.Unlock()
		m.notify()
		return PlaybackInterruption{}, false
	}
	audioEndMS, ok := controller.InterruptPlayback(response)
	if audioEndMS < 0 {
		audioEndMS = 0
	}
	if m.sampleRate > 0 {
		availableMS := int(responseSamples * 1000 / uint64(m.sampleRate))
		if audioEndMS > availableMS {
			audioEndMS = availableMS
		}
	}
	m.mu.Unlock()
	m.notify()
	if !ok {
		return PlaybackInterruption{}, false
	}
	return PlaybackInterruption{PlaybackResponse: response, AudioEndMS: audioEndMS}, true
}

func (m *sessionInboundMedia) push(samples []int16) error {
	if len(samples) == 0 {
		return nil
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return ErrSessionMediaClosed
	}
	if m.terminal != nil {
		err := m.terminal
		m.mu.Unlock()
		return err
	}
	if m.discarding {
		m.mu.Unlock()
		return nil
	}
	maximumInt := int(^uint(0) >> 1)
	if len(samples) > maximumInt-len(m.pending) {
		m.mu.Unlock()
		return ErrSessionMediaInboundBacklog
	}
	completeFrames := (len(m.pending) + len(samples)) / m.frameSamples
	availableFrames := sessionMediaMaxQueuedFrames - len(m.frames)
	if availableFrames < 0 || completeFrames > availableFrames {
		m.mu.Unlock()
		return ErrSessionMediaInboundBacklog
	}
	if m.response.ItemID != "" {
		m.responseSamples[m.response] += uint64(len(samples))
	}
	m.pending = append(m.pending, samples...)
	m.appendCompleteFramesLocked()
	m.activateQueuedPlaybackLocked()
	m.mu.Unlock()
	m.notify()
	return nil
}

func (m *sessionInboundMedia) flush() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return ErrSessionMediaClosed
	}
	if m.terminal != nil {
		err := m.terminal
		m.mu.Unlock()
		return err
	}
	if m.discarding {
		m.mu.Unlock()
		return nil
	}
	needsBoundary := len(m.pending) > 0 || !m.padPartial
	if needsBoundary && len(m.frames) >= sessionMediaMaxQueuedFrames {
		m.mu.Unlock()
		return ErrSessionMediaInboundBacklog
	}
	m.appendResponseBoundaryLocked(true)
	m.activateQueuedPlaybackLocked()
	m.mu.Unlock()
	m.notify()
	return nil
}

func (m *sessionInboundMedia) fail(err error) {
	if err == nil {
		err = io.EOF
	}
	m.mu.Lock()
	if m.terminal == nil && !m.closed {
		m.appendResponseBoundaryLocked(false)
		m.activateQueuedPlaybackLocked()
		m.terminal = err
	}
	m.mu.Unlock()
	m.notify()
}

func (m *sessionInboundMedia) appendResponseBoundaryLocked(includeEmpty bool) {
	if len(m.pending) > 0 {
		if len(m.frames) >= sessionMediaMaxQueuedFrames {
			// The terminal error set by fail remains observable after existing
			// audio drains; do not exceed the defensive allocation ceiling.
			m.pending = nil
			return
		}
		sampleCount := len(m.pending)
		if m.padPartial {
			sampleCount = m.frameSamples
		}
		samples := make([]int16, sampleCount)
		copy(samples, m.pending)
		m.frames = append(m.frames, PCMFrame{Samples: samples, EndOfResponse: true, PlaybackResponse: m.response})
		m.pending = nil
	} else if includeEmpty && !m.padPartial {
		// A complete frame may already have been consumed before the provider's
		// done event arrives. Publish an explicit zero-sample boundary so a sink
		// can flush any rate-conversion remainder without inventing audio.
		m.frames = append(m.frames, PCMFrame{EndOfResponse: true, PlaybackResponse: m.response})
	}
}

func (m *sessionInboundMedia) Close() error {
	m.close()
	return nil
}

func (m *sessionInboundMedia) close() {
	m.closeOnce.Do(func() {
		m.mu.Lock()
		m.closed = true
		m.mu.Unlock()
		close(m.done)
		m.notify()
	})
}

func (m *sessionInboundMedia) appendCompleteFramesLocked() {
	for len(m.pending) >= m.frameSamples {
		samples := make([]int16, m.frameSamples)
		copy(samples, m.pending[:m.frameSamples])
		m.frames = append(m.frames, PCMFrame{Samples: samples, PlaybackResponse: m.response})
		m.pending = m.pending[m.frameSamples:]
	}
}

func (m *sessionInboundMedia) notify() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}
