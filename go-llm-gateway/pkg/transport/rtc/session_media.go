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
	sessionMediaQueueLimit          = 256
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
		inbound:  newSessionInboundMedia(DefaultSessionMediaFrameSamples),
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

// PushInbound appends PCM samples received from the provider. Complete
// frames become available to the Inbound endpoint; a bounded queue drops the
// oldest frames when no consumer is attached.
func (m *SessionMedia) PushInbound(samples []int16) error {
	if m == nil || m.inbound == nil {
		return ErrSessionMediaClosed
	}
	return m.inbound.push(samples)
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
	pending      []int16
	frames       []PCMFrame
	terminal     error
	closed       bool
	done         chan struct{}
	wake         chan struct{}
	closeOnce    sync.Once
}

func newSessionInboundMedia(frameSamples int) *sessionInboundMedia {
	return &sessionInboundMedia{
		frameSamples: frameSamples,
		done:         make(chan struct{}),
		wake:         make(chan struct{}, 1),
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
	m.pending = append(m.pending, samples...)
	m.appendCompleteFramesLocked()
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
	if len(m.pending) > 0 {
		samples := make([]int16, m.frameSamples)
		copy(samples, m.pending)
		m.frames = append(m.frames, PCMFrame{Samples: samples})
		m.pending = nil
		m.trimQueueLocked()
	}
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
		m.terminal = err
	}
	m.mu.Unlock()
	m.notify()
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
		m.frames = append(m.frames, PCMFrame{Samples: samples})
		m.pending = m.pending[m.frameSamples:]
	}
	m.trimQueueLocked()
}

func (m *sessionInboundMedia) trimQueueLocked() {
	if len(m.frames) <= sessionMediaQueueLimit {
		return
	}
	first := len(m.frames) - sessionMediaQueueLimit
	copy(m.frames, m.frames[first:])
	m.frames = m.frames[:sessionMediaQueueLimit]
}

func (m *sessionInboundMedia) notify() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}
