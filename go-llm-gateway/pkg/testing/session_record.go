package testing

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// SessionRecorder wraps a messages.Session and records all sent and received
// events for later serialisation. It is the session-level counterpart of
// RecordRoundTripper.
//
// The recorder is thread-safe: Send and Receive may be called from different
// goroutines concurrently.
type SessionRecorder struct {
	inner    messages.Session
	events   []CapturedSessionEvent
	mu       sync.Mutex
	startAt  time.Time
	capture  SessionCapture
	sequence int
	relayCtx context.Context
	cancel   context.CancelFunc

	// inbound is a wrapped TypedBuffer that intercepts reads from the inner
	// session's Receive buffer and records each message.
	inbound *recordingBuffer
}

var _ messages.Session = (*SessionRecorder)(nil)
var _ messages.SessionResponseRequester = (*SessionRecorder)(nil)
var _ messages.SessionResponseCapability = (*SessionRecorder)(nil)

// SessionRecorderOption configures metadata on a SessionRecorder capture.
type SessionRecorderOption func(*SessionRecorder)

// WithSessionCaptureProvider records non-sensitive provider metadata in the capture envelope.
func WithSessionCaptureProvider(name, model string) SessionRecorderOption {
	return func(r *SessionRecorder) {
		r.capture.Provider = SessionProviderMetadata{Name: name, Model: model}
	}
}

// WithSessionCaptureID records a non-sensitive provider session identifier in the capture envelope.
func WithSessionCaptureID(id string) SessionRecorderOption {
	return func(r *SessionRecorder) {
		r.capture.Session.ID = id
	}
}

// WithSessionRelayContext makes inbound relay writes stop when ctx is cancelled.
func WithSessionRelayContext(ctx context.Context) SessionRecorderOption {
	return func(r *SessionRecorder) {
		if ctx != nil {
			r.relayCtx = ctx
		}
	}
}

// NewSessionRecorder creates a SessionRecorder that wraps inner and records
// every event that passes through Send and Receive.
func NewSessionRecorder(inner messages.Session, opts ...SessionRecorderOption) *SessionRecorder {
	startAt := time.Now().UTC()
	r := &SessionRecorder{
		inner:   inner,
		events:  make([]CapturedSessionEvent, 0),
		startAt: startAt,
		capture: SessionCapture{
			Version: SessionCaptureVersion,
			Session: SessionMetadata{
				StartedAtUTC: startAt.Format(time.RFC3339Nano),
			},
			Records: make([]CapturedSessionEvent, 0),
		},
	}
	for _, opt := range opts {
		opt(r)
	}
	if r.relayCtx == nil {
		r.relayCtx = context.Background()
	}
	r.relayCtx, r.cancel = context.WithCancel(r.relayCtx)
	r.inbound = newRecordingBuffer(inner.Receive(), r)
	return r
}

// Send forwards the message to the inner session and records it as a
// client-to-server event.
func (r *SessionRecorder) Send(ctx context.Context, msg messages.StreamMessage) bool {
	return r.SendWithOutcome(ctx, msg).OK()
}

// SendWithOutcome forwards the message to the inner session and preserves
// typed send outcomes when the wrapped session exposes them.
func (r *SessionRecorder) SendWithOutcome(ctx context.Context, msg messages.StreamMessage) messages.SessionSendOutcome {
	r.recordMessage(DirectionClientToServer, msg)
	return messages.SendSessionWithOutcome(ctx, r.inner, msg)
}

// RequestResponse forwards the optional explicit response capability while
// recording its stream-level control event. A replay-backed inner session does
// not expose the capability, so it remains compatible with older captures.
func (r *SessionRecorder) RequestResponse(ctx context.Context) messages.SessionSendOutcome {
	if !messages.SupportsSessionResponseRequests(r.inner) {
		return messages.SessionSendOutcome{Status: messages.SessionSendTerminalFailure}
	}
	return r.SendWithOutcome(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeResponseCreate,
		Value: messages.NewResponseCreateValue(),
	})
}

func (r *SessionRecorder) SupportsResponseRequests() bool {
	return messages.SupportsSessionResponseRequests(r.inner)
}

// Receive returns a TypedBuffer whose reads are intercepted so that every
// server-to-client message is recorded.
func (r *SessionRecorder) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return r.inbound.buf
}

// Done delegates to the inner session.
func (r *SessionRecorder) Done() <-chan struct{} {
	return r.inner.Done()
}

// Close delegates to the inner session.
func (r *SessionRecorder) Close() error {
	r.cancel()
	return r.inner.Close()
}

// FlushToFile writes all recorded events as a JSON envelope to the given path.
func (r *SessionRecorder) FlushToFile(path string) error {
	capture := r.Capture()

	data, err := json.MarshalIndent(capture, "", "  ")
	if err != nil {
		return fmt.Errorf("encode session captures: %w", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write session capture file: %w", err)
	}
	return nil
}

// Capture returns a copy of the complete capture envelope.
func (r *SessionRecorder) Capture() SessionCapture {
	r.mu.Lock()
	defer r.mu.Unlock()

	events := make([]CapturedSessionEvent, len(r.events))
	copy(events, r.events)

	capture := r.capture
	capture.Records = events
	return capture
}

// Events returns a copy of the recorded events (for inspection in tests).
func (r *SessionRecorder) Events() []CapturedSessionEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]CapturedSessionEvent, len(r.events))
	copy(out, r.events)
	return out
}

func (r *SessionRecorder) recordMessage(dir SessionEventDirection, msg messages.StreamMessage) {
	data, err := MarshalStreamMessage(msg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "session recorder: failed to marshal %s event: %v\n", msg.Type, err)
		return
	}
	r.mu.Lock()
	r.sequence++
	r.events = append(r.events, CapturedSessionEvent{
		Sequence:    r.sequence,
		Direction:   dir,
		TimestampMs: time.Since(r.startAt).Milliseconds(),
		Type:        string(msg.Type),
		PayloadType: SessionPayloadTypeStreamMessage,
		Payload:     data,
	})
	r.mu.Unlock()
}

// recordingBuffer proxies reads from an inner TypedBuffer and records each
// message via the SessionRecorder. Because TypedBuffer is channel-backed and
// we cannot intercept channel reads, we use a relay goroutine that reads from
// the inner buffer and writes to a new buffer, recording along the way.
type recordingBuffer struct {
	buf *messages.TypedBuffer[messages.StreamMessage]
}

func newRecordingBuffer(inner *messages.TypedBuffer[messages.StreamMessage], rec *SessionRecorder) *recordingBuffer {
	// Create a relay buffer with the same capacity.
	relay := messages.NewTypedBuffer[messages.StreamMessage](inner.Cap())

	// Relay goroutine: read from the inner channel, record, and forward.
	// Watches the inner session's Done() channel to terminate when the session
	// ends, preventing goroutine leaks (TypedBuffer channels are never closed).
	go func() {
		for {
			select {
			case msg := <-inner.Chan():
				rec.recordMessage(DirectionServerToClient, msg)
				relay.Write(rec.relayCtx, msg)
			case <-rec.inner.Done():
				rec.cancel()
				return
			case <-rec.relayCtx.Done():
				return
			}
		}
	}()

	return &recordingBuffer{buf: relay}
}
