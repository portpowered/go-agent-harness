package testing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

var _ messages.BargeInCapableSession = (*SessionRecorder)(nil)

// SessionRecorder wraps a messages.Session and records all sent and received
// events for later serialisation. It is the session-level counterpart of
// RecordRoundTripper.
//
// The recorder is thread-safe: Send and Receive may be called from different
// goroutines concurrently.
type SessionRecorder struct {
	messages.SessionCapabilities
	barrier  messages.RelayBarrier
	inner    messages.Session
	events   []CapturedSessionEvent
	mu       sync.Mutex
	startAt  time.Time
	clock    clock.Source
	capture  SessionCapture
	sequence int
	// stop ends the inbound relay when the recorder closes; relayDone closes
	// when the relay has stopped.
	stop      chan struct{}
	stopOnce  sync.Once
	relayDone chan struct{}

	// inbound is a wrapped TypedBuffer that intercepts reads from the inner
	// session's Receive buffer and records each message.
	inbound *recordingBuffer
}

type recordingWebSocketConn struct {
	inner    transport.Conn
	recorder *RecordingWebSocketDialer
}

var _ transport.Conn = (*recordingWebSocketConn)(nil)

func (c *recordingWebSocketConn) ReadMessage() (int, []byte, error) {
	c.recorder.captureMu.RLock()
	defer c.recorder.captureMu.RUnlock()

	messageType, payload, err := c.inner.ReadMessage()
	if err == nil {
		sequence := c.recorder.recordMessage(DirectionServerToClient, payload)
		c.recorder.commitMessage(sequence)
	}
	return messageType, payload, err
}

func (c *recordingWebSocketConn) WriteMessage(messageType int, payload []byte) error {
	c.recorder.captureMu.RLock()
	defer c.recorder.captureMu.RUnlock()

	// Reserve the outbound event before invoking the wrapped connection. A
	// hermetic provider may synchronously enqueue a response while processing
	// this write; recording after the call lets that response appear before
	// its causal client event in the capture.
	sequence := c.recorder.recordMessage(DirectionClientToServer, payload)
	if err := c.inner.WriteMessage(messageType, payload); err != nil {
		c.recorder.discardMessage(sequence)
		return err
	}
	c.recorder.commitMessage(sequence)
	return nil
}

func (c *recordingWebSocketConn) Close() error { return c.inner.Close() }

var _ messages.Session = (*SessionRecorder)(nil)
var _ messages.SessionResponseRequester = (*SessionRecorder)(nil)
var _ messages.SessionResponseCapability = (*SessionRecorder)(nil)
var _ messages.SessionOutboundFlusher = (*SessionRecorder)(nil)

// SessionRecorderOption configures metadata on a SessionRecorder capture.
type SessionRecorderOption func(*SessionRecorder)

// WithSessionCaptureClock gives stream evidence the application's time domain.
func WithSessionCaptureClock(source clock.Source) SessionRecorderOption {
	return func(r *SessionRecorder) {
		if source != nil {
			r.clock = source
		}
	}
}

// WithReplayClock uses the same injected domain for replay timestamps and waits.
func WithReplayClock(source clock.TimerSource) ReplayWebSocketDialerOption {
	return func(d *ReplayWebSocketDialer) {
		if source != nil {
			d.clock = source
		}
	}
}

func captureClock(sources []clock.Source) clock.Source {
	if len(sources) > 0 && sources[0] != nil {
		return sources[0]
	}
	return clock.Real{}
}

func newCaptureEnvelope(startedAt time.Time) SessionCapture {
	return SessionCapture{
		Version: SessionCaptureVersion,
		Session: SessionMetadata{StartedAtUTC: startedAt.UTC().Format(time.RFC3339Nano)},
		Records: make([]CapturedSessionEvent, 0),
	}
}

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

// NewSessionRecorder creates a SessionRecorder that wraps inner and records
// every event that passes through Send and Receive. The inbound relay stops
// when ctx ends, inner is done or the recorder closes.
func NewSessionRecorder(ctx context.Context, inner messages.Session, opts ...SessionRecorderOption) (*SessionRecorder, error) {
	if ctx == nil {
		return nil, errors.New("session recorder context is required")
	}
	if inner == nil {
		return nil, errors.New("session recorder requires a session")
	}
	r := &SessionRecorder{
		SessionCapabilities: messages.SessionCapabilities{Wrapped: inner},
		inner:               inner,
		events:              make([]CapturedSessionEvent, 0),
		clock:               clock.Real{},
		stop:                make(chan struct{}),
		relayDone:           make(chan struct{}),
	}
	for _, opt := range opts {
		opt(r)
	}
	r.startAt = r.clock.Now()
	r.capture.Version = SessionCaptureVersion
	r.capture.Session.StartedAtUTC = r.startAt.UTC().Format(time.RFC3339Nano)
	r.capture.Records = make([]CapturedSessionEvent, 0)
	r.inbound = newRecordingBuffer(ctx, inner.Receive(), r)
	return r, nil
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
	if !r.SupportsResponseRequests() {
		return messages.SessionSendOutcome{Status: messages.SessionSendTerminalFailure}
	}
	return r.SendWithOutcome(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeResponseCreate,
		Value: messages.NewResponseCreateValue(),
	})
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
	closeErr := r.inner.Close()
	// Let the wrapped session publish its terminal state before stopping the
	// inbound relay. A duration-bounded caller relies on that final event to
	// distinguish an intentional cutoff from cancellation.
	r.stopOnce.Do(func() { close(r.stop) })
	return closeErr
}

// FlushToFile writes all recorded events as a JSON envelope to the given path.
func (r *SessionRecorder) FlushToFile(path string) error {
	capture := r.Capture()

	data, err := json.MarshalIndent(capture, "", "  ")
	if err != nil {
		return fmt.Errorf("encode session captures: %w", err)
	}
	if err := os.WriteFile(path, data, legacyCaptureFileMode); err != nil {
		return fmt.Errorf("write session capture file: %w", err)
	}
	return nil
}

// Capture returns a copy of the complete capture envelope.
func (r *SessionRecorder) Capture() SessionCapture {
	r.mu.Lock()
	defer r.mu.Unlock()

	capture := r.capture
	capture.Records = cloneCapturedEvents(r.events)
	// Capture is part of the public recording result, so expose the same
	// protected envelope that FlushToFile writes. The recorder only emits JSON
	// payloads; an unexpected serialization failure is surfaced by FlushToFile.
	if sealed, err := SealSessionCapture(capture); err == nil {
		capture = sealed
	}
	return capture
}

// Events returns a copy of the recorded events (for inspection in tests).
func (r *SessionRecorder) Events() []CapturedSessionEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return cloneCapturedEvents(r.events)
}

func cloneCapturedEvents(events []CapturedSessionEvent) []CapturedSessionEvent {
	out := make([]CapturedSessionEvent, len(events))
	for index, event := range events {
		out[index] = cloneCapturedEvent(event)
	}
	return out
}

func cloneCapturedEvent(event CapturedSessionEvent) CapturedSessionEvent {
	event.Payload = append(json.RawMessage(nil), event.Payload...)
	event.Data = append(json.RawMessage(nil), event.Data...)
	return event
}

func validateSessionCaptureRecords(path string, capture SessionCapture) error {
	previousSequence := 0
	for index, record := range capture.Records {
		if err := validateSessionCaptureRecord(path, index, record, previousSequence); err != nil {
			return err
		}
		previousSequence = record.Sequence
	}
	return nil
}

func validateSessionCaptureRecord(path string, index int, record CapturedSessionEvent, previousSequence int) error {
	fieldPrefix := fmt.Sprintf("/records/%d", index)
	if record.Sequence <= 0 {
		return newSessionCaptureValidationError(path, SessionCaptureErrorClassStructure, fieldPrefix+"/sequence", record.Sequence, "", "positive integer", fmt.Sprintf("%d", record.Sequence), ErrSessionCaptureStructure)
	}
	if record.Sequence <= previousSequence {
		return newSessionCaptureValidationError(path, SessionCaptureErrorClassStructure, fieldPrefix+"/sequence", record.Sequence, "", fmt.Sprintf("greater than %d", previousSequence), fmt.Sprintf("%d", record.Sequence), ErrSessionCaptureStructure)
	}
	if record.Direction != DirectionClientToServer && record.Direction != DirectionServerToClient {
		return newSessionCaptureValidationError(path, SessionCaptureErrorClassStructure, fieldPrefix+"/direction", record.Sequence, "", "client_to_server or server_to_client", string(record.Direction), ErrSessionCaptureStructure)
	}
	if record.TimestampMs < 0 {
		return newSessionCaptureValidationError(path, SessionCaptureErrorClassStructure, fieldPrefix+"/timestamp_ms", record.Sequence, "", "non-negative integer", fmt.Sprintf("%d", record.TimestampMs), ErrSessionCaptureStructure)
	}
	if strings.TrimSpace(record.Type) == "" {
		return newSessionCaptureValidationError(path, SessionCaptureErrorClassStructure, fieldPrefix+"/type", record.Sequence, "", "non-empty string", "missing", ErrSessionCaptureStructure)
	}
	if record.PayloadType != SessionPayloadTypeStreamMessage && record.PayloadType != SessionPayloadTypeWebSocketMessage {
		return newSessionCaptureValidationError(path, SessionCaptureErrorClassStructure, fieldPrefix+"/payload_type", record.Sequence, "", SessionPayloadTypeStreamMessage+" or "+SessionPayloadTypeWebSocketMessage, record.PayloadType, ErrSessionCaptureStructure)
	}
	payload := eventPayload(record)
	if len(bytes.TrimSpace(payload)) == 0 {
		return newSessionCaptureValidationError(path, SessionCaptureErrorClassStructure, fieldPrefix+"/payload", record.Sequence, "", "non-empty JSON value", "missing", ErrSessionCaptureStructure)
	}
	if !json.Valid(payload) {
		return newSessionCaptureValidationError(path, SessionCaptureErrorClassStructure, fieldPrefix+"/payload", record.Sequence, "", "valid JSON value", "invalid JSON", ErrSessionCaptureStructure)
	}
	if captureJSONType(payload) == jsonNullLiteral {
		return newSessionCaptureValidationError(path, SessionCaptureErrorClassStructure, fieldPrefix+"/payload", record.Sequence, "", "non-null JSON value", jsonNullLiteral, ErrSessionCaptureStructure)
	}
	return nil
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
		TimestampMs: r.clock.Now().Sub(r.startAt).Milliseconds(),
		Type:        string(msg.Type),
		PayloadType: SessionPayloadTypeStreamMessage,
		Payload:     data,
	})
	r.mu.Unlock()
}

// SyncReceive returns once every provider message queued before the call has
// been recorded and relayed into Receive.
func (r *SessionRecorder) SyncReceive(ctx context.Context) {
	r.SessionCapabilities.SyncReceive(ctx)
	r.barrier.Await(ctx, r.relayDone)
}
