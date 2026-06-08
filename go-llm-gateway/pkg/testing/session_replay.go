package testing

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/portpowered/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-llm-gateway/pkg/gateway"
)

// SessionReplayerOption configures a SessionReplayer.
type SessionReplayerOption func(*SessionReplayer)

// WithReplayContext makes replay delivery stop when ctx is cancelled.
func WithReplayContext(ctx context.Context) SessionReplayerOption {
	return func(r *SessionReplayer) {
		if ctx != nil {
			r.replayCtx = ctx
		}
	}
}

// WithReplayTiming enables real-time delays between events based on their
// recorded timestamps. By default, all events are delivered immediately.
func WithReplayTiming() SessionReplayerOption {
	return func(r *SessionReplayer) { r.useTiming = true }
}

// WithReplayOutboundValidation controls whether Send must match recorded
// client-to-server events before replay advances past them. Validation is
// enabled by default; disable it only for read-only transcript rendering.
func WithReplayOutboundValidation(enabled bool) SessionReplayerOption {
	return func(r *SessionReplayer) { r.validateOutbound = enabled }
}

// SessionReplayer implements messages.Session by replaying server-to-client
// events from a previously recorded session capture file. It is the session-level
// counterpart of ReplayRoundTripper.
//
// Client-to-server events sent via Send are verified against the next expected
// outbound capture record. Replay advances through the ordered capture: inbound
// records are delivered until an outbound record is reached, then replay waits
// for Send to provide that exact event before later inbound records are delivered.
type SessionReplayer struct {
	events    []CapturedSessionEvent
	useTiming bool

	validateOutbound bool
	outbound         *messages.TypedBuffer[messages.StreamMessage]
	done             chan struct{}
	closeOnce        sync.Once

	// sentLog records messages passed to Send (for test inspection).
	sentLog   []messages.StreamMessage
	index     int
	err       error
	closed    bool
	cond      *sync.Cond
	mu        sync.Mutex
	replayCtx context.Context
	cancel    context.CancelFunc
}

var _ messages.Session = (*SessionReplayer)(nil)

// NewSessionReplayer creates a SessionReplayer from a capture file at the given
// path. The file must be a SessionCapture JSON object produced by
// SessionRecorder.FlushToFile. Legacy JSON arrays of CapturedSessionEvent
// objects are accepted for older fixtures.
func NewSessionReplayer(path string, opts ...SessionReplayerOption) (*SessionReplayer, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read session capture file: %w", err)
	}
	return NewSessionReplayerFromBytes(data, opts...)
}

// NewSessionReplayerFromBytes creates a SessionReplayer from raw JSON bytes.
func NewSessionReplayerFromBytes(data []byte, opts ...SessionReplayerOption) (*SessionReplayer, error) {
	events, err := decodeSessionCaptureEvents(data)
	if err != nil {
		return nil, fmt.Errorf("parse session capture: %w", err)
	}

	r := &SessionReplayer{
		events:   events,
		outbound: messages.NewTypedBuffer[messages.StreamMessage](64),
		done:     make(chan struct{}),
	}
	r.validateOutbound = true
	r.cond = sync.NewCond(&r.mu)
	for _, opt := range opts {
		opt(r)
	}
	if r.replayCtx == nil {
		r.replayCtx = context.Background()
	}
	r.replayCtx, r.cancel = context.WithCancel(r.replayCtx)

	go r.replayLoop()

	return r, nil
}

// Send verifies the outbound message against the next expected client-to-server
// capture record. A divergence terminates replay and is available via Err.
func (r *SessionReplayer) Send(ctx context.Context, msg messages.StreamMessage) bool {
	select {
	case <-ctx.Done():
		return false
	default:
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.closed {
		return false
	}
	if r.err != nil {
		return false
	}
	if !r.validateOutbound {
		r.sentLog = append(r.sentLog, msg)
		return true
	}
	if r.index >= len(r.events) {
		r.failLocked(gateway.NewReplayMismatchError("replay completed", string(msg.Type), fmt.Errorf("unexpected outbound event after replay completed")))
		return false
	}

	expected := r.events[r.index]
	if expected.Direction != DirectionClientToServer {
		r.failLocked(gateway.NewReplayMismatchError(
			fmt.Sprintf("%s event %s at sequence %d", expected.Direction, expected.Type, expected.Sequence),
			string(msg.Type),
			fmt.Errorf("got outbound before expected capture event"),
		))
		return false
	}
	if err := compareCapturedStreamMessage(expected, msg); err != nil {
		r.failLocked(gateway.NewReplayMismatchError(
			fmt.Sprintf("outbound payload for %s at sequence %d", expected.Type, expected.Sequence),
			string(msg.Type),
			err,
		))
		return false
	}

	r.sentLog = append(r.sentLog, msg)
	r.index++
	r.cond.Broadcast()
	return true
}

// Receive returns the buffer from which server-to-client events are delivered.
func (r *SessionReplayer) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return r.outbound
}

// Done returns a channel that is closed when all events have been replayed.
func (r *SessionReplayer) Done() <-chan struct{} {
	return r.done
}

// Close terminates the replayer. Safe to call multiple times.
func (r *SessionReplayer) Close() error {
	r.mu.Lock()
	if r.validateOutbound && !r.closed && r.err == nil && r.index < len(r.events) {
		if evt, ok := r.nextExpectedOutboundLocked(); ok {
			r.err = gateway.NewReplayMismatchError(
				fmt.Sprintf("outbound event %s at sequence %d", evt.Type, evt.Sequence),
				"replay close",
				fmt.Errorf("session replay closed before expected outbound event"),
			)
		}
	}
	r.mu.Unlock()
	r.cancel()
	r.close()
	return nil
}

// SentLog returns a copy of messages that were passed to Send (for test assertions).
func (r *SessionReplayer) SentLog() []messages.StreamMessage {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]messages.StreamMessage, len(r.sentLog))
	copy(out, r.sentLog)
	return out
}

// Err returns a replay divergence or omitted-outbound error, if one occurred.
func (r *SessionReplayer) Err() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

func (r *SessionReplayer) replayLoop() {
	defer r.close()

	var lastTimestamp int64
	for {
		r.mu.Lock()
		for r.validateOutbound && !r.closed && r.err == nil && r.index < len(r.events) && r.events[r.index].Direction == DirectionClientToServer {
			r.cond.Wait()
		}
		if r.closed || r.err != nil || r.index >= len(r.events) {
			r.mu.Unlock()
			return
		}
		evt := r.events[r.index]
		r.index++
		r.mu.Unlock()

		if r.useTiming && evt.TimestampMs > lastTimestamp {
			delay := time.Duration(evt.TimestampMs-lastTimestamp) * time.Millisecond
			select {
			case <-r.done:
				return
			case <-r.replayCtx.Done():
				return
			case <-time.After(delay):
			}
		}
		lastTimestamp = evt.TimestampMs

		if evt.Direction != DirectionServerToClient {
			continue
		}

		msg, err := deserializeStreamMessage(evt)
		if err != nil {
			fmt.Fprintf(os.Stderr, "session replayer: skipping event (type=%s): %v\n", evt.Type, err)
			continue
		}

		// Write to outbound buffer; if done is closed, stop.
		select {
		case <-r.done:
			return
		default:
			if !r.outbound.Write(r.replayCtx, msg) && r.replayCtx.Err() != nil {
				return
			}
		}
	}
}

func (r *SessionReplayer) failLocked(err error) {
	if r.err == nil {
		r.err = err
	}
	r.closeOnce.Do(func() {
		r.closed = true
		close(r.done)
	})
	r.cond.Broadcast()
}

func (r *SessionReplayer) close() {
	r.closeOnce.Do(func() {
		r.cancel()
		r.mu.Lock()
		r.closed = true
		close(r.done)
		r.mu.Unlock()
	})
	r.cond.Broadcast()
}

func (r *SessionReplayer) nextExpectedOutboundLocked() (CapturedSessionEvent, bool) {
	for i := r.index; i < len(r.events); i++ {
		if r.events[i].Direction == DirectionClientToServer {
			return r.events[i], true
		}
	}
	return CapturedSessionEvent{}, false
}

// deserializeStreamMessage converts a CapturedSessionEvent back into a
// StreamMessage using the type-aware UnmarshalStreamMessage helper.
func deserializeStreamMessage(evt CapturedSessionEvent) (messages.StreamMessage, error) {
	payload := evt.Payload
	if len(payload) == 0 {
		payload = evt.Data
	}
	if len(payload) == 0 {
		return messages.StreamMessage{}, fmt.Errorf("missing payload")
	}
	if evt.PayloadType != "" && evt.PayloadType != SessionPayloadTypeStreamMessage {
		return messages.StreamMessage{}, fmt.Errorf("unsupported payload type: %s", evt.PayloadType)
	}
	return UnmarshalStreamMessage(payload)
}

func compareCapturedStreamMessage(expected CapturedSessionEvent, actual messages.StreamMessage) error {
	if expected.Type != "" && expected.Type != string(actual.Type) {
		return fmt.Errorf("expected outbound type %s, got %s", expected.Type, actual.Type)
	}
	expectedPayload := expected.Payload
	if len(expectedPayload) == 0 {
		expectedPayload = expected.Data
	}
	if len(expectedPayload) == 0 {
		return fmt.Errorf("expected outbound event %s is missing payload", expected.Type)
	}
	if expected.PayloadType != "" && expected.PayloadType != SessionPayloadTypeStreamMessage {
		return fmt.Errorf("expected outbound event %s has unsupported payload type %s", expected.Type, expected.PayloadType)
	}

	actualPayload, err := MarshalStreamMessage(actual)
	if err != nil {
		return fmt.Errorf("marshal outbound event %s: %w", actual.Type, err)
	}
	if !jsonEqual(expectedPayload, actualPayload) {
		return fmt.Errorf("expected outbound payload for %s does not match sent payload type %s", expected.Type, actual.Type)
	}
	return nil
}

func jsonEqual(a, b []byte) bool {
	var av any
	var bv any
	if err := json.Unmarshal(a, &av); err != nil {
		return bytes.Equal(a, b)
	}
	if err := json.Unmarshal(b, &bv); err != nil {
		return bytes.Equal(a, b)
	}
	aj, err := json.Marshal(av)
	if err != nil {
		return bytes.Equal(a, b)
	}
	bj, err := json.Marshal(bv)
	if err != nil {
		return bytes.Equal(a, b)
	}
	return bytes.Equal(aj, bj)
}

func decodeSessionCaptureEvents(data []byte) ([]CapturedSessionEvent, error) {
	var capture SessionCapture
	if err := json.Unmarshal(data, &capture); err == nil && capture.Version != 0 {
		return capture.Records, nil
	}

	var events []CapturedSessionEvent
	if err := json.Unmarshal(data, &events); err != nil {
		return nil, err
	}
	return events, nil
}
