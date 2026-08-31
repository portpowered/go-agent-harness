package testing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/gateway"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
)

// SessionReplayerOption configures a SessionReplayer.
type SessionReplayerOption func(*SessionReplayer)

// SessionReplayStatus is the public terminal state of a session replay.
type SessionReplayStatus string

const (
	// SessionReplayOpen indicates replay has not reached a terminal state.
	SessionReplayOpen SessionReplayStatus = "open"
	// SessionReplayCompleted indicates every captured event was replayed or
	// validated successfully.
	SessionReplayCompleted SessionReplayStatus = "completed"
	// SessionReplayDiverged indicates caller output differed from the next
	// expected capture event.
	SessionReplayDiverged SessionReplayStatus = "diverged"
	// SessionReplayIncomplete indicates replay stopped before an expected
	// capture event was observed.
	SessionReplayIncomplete SessionReplayStatus = "incomplete"
	// SessionReplayCancelled indicates the caller-owned replay context stopped
	// delivery before completion.
	SessionReplayCancelled SessionReplayStatus = "cancelled"
)

// SessionReplayOutcome is an inspectable replay result for test harnesses and
// fixture validators. Err preserves existing replay mismatch classifications,
// while Expected and Actual expose mismatch details without parsing log text.
type SessionReplayOutcome struct {
	Status   SessionReplayStatus
	Err      error
	Expected string
	Actual   string
}

// OK reports whether replay completed without divergence, incompletion, or
// caller cancellation.
func (o SessionReplayOutcome) OK() bool {
	return o.Status == SessionReplayCompleted
}

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
	outcome   SessionReplayOutcome
	closed    bool
	cond      *sync.Cond
	mu        sync.Mutex
	replayCtx context.Context
	cancel    context.CancelFunc
}

var _ messages.Session = (*SessionReplayer)(nil)
var _ messages.SessionSendOutcomeSender = (*SessionReplayer)(nil)

// NewSessionReplayer creates a SessionReplayer from a capture file at the
// given path. Version-2 captures are fully verified; retained version-1
// captures are structurally validated and replayed with reduced-integrity
// guarantees before any replay goroutines are created.
func NewSessionReplayer(path string, opts ...SessionReplayerOption) (*SessionReplayer, error) {
	loaded, err := LoadSessionCaptureForReplay(path)
	if err != nil {
		return nil, err
	}
	return newSessionReplayer(loaded.Capture.Records, opts...), nil
}

// NewSessionReplayerFromBytes creates a SessionReplayer from raw protected
// version-2 capture JSON bytes. It exists for callers that already own the
// capture bytes; legacy bytes require NewSessionReplayerFromLegacyBytes.
func NewSessionReplayerFromBytes(data []byte, opts ...SessionReplayerOption) (*SessionReplayer, error) {
	capture, err := validateSessionCapturePath("", data)
	if err != nil {
		return nil, fmt.Errorf("parse session capture: %w", err)
	}
	return newSessionReplayer(capture.Records, opts...), nil
}

// NewSessionReplayerFromLegacyBytes is an explicit compatibility seam for
// callers that already own legacy bytes. The shipped path-based replay flow
// uses LoadSessionCaptureForReplay instead, so it can validate the source path
// and surface the reduced-integrity warning.
func NewSessionReplayerFromLegacyBytes(data []byte, opts ...SessionReplayerOption) (*SessionReplayer, error) {
	events, err := decodeLegacySessionCaptureEvents(data)
	if err != nil {
		return nil, fmt.Errorf("parse legacy session capture: %w", err)
	}
	return newSessionReplayer(events, opts...), nil
}

func newSessionReplayer(events []CapturedSessionEvent, opts ...SessionReplayerOption) *SessionReplayer {
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

	go r.watchReplayContext()
	go r.replayLoop()

	return r
}

// Send verifies the outbound message against the next expected client-to-server
// capture record. A divergence terminates replay and is available via Err.
func (r *SessionReplayer) Send(ctx context.Context, msg messages.StreamMessage) bool {
	return r.SendWithOutcome(ctx, msg).OK()
}

// SendWithOutcome verifies the outbound message against the next expected
// client-to-server capture record and reports the precise public lifecycle
// outcome.
func (r *SessionReplayer) SendWithOutcome(ctx context.Context, msg messages.StreamMessage) messages.SessionSendOutcome {
	select {
	case <-ctx.Done():
		return sessionSendContextOutcome(ctx)
	default:
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.closed {
		return messages.SessionSendOutcome{Status: messages.SessionSendClosed}
	}
	if r.err != nil {
		return messages.SessionSendOutcome{Status: messages.SessionSendTerminalFailure, Err: r.err}
	}
	if !r.validateOutbound {
		r.sentLog = append(r.sentLog, msg)
		return messages.SessionSendOutcome{Status: messages.SessionSendSucceeded}
	}
	if r.index >= len(r.events) {
		err := newReplayMismatchError("replay completed", string(msg.Type), fmt.Errorf("unexpected outbound event after replay completed"))
		r.failLocked(SessionReplayDiverged, err)
		return messages.SessionSendOutcome{Status: messages.SessionSendTerminalFailure, Err: err}
	}

	expected := r.events[r.index]
	if expected.Direction != DirectionClientToServer {
		err := newReplayMismatchError(
			fmt.Sprintf("%s event %s at sequence %d", expected.Direction, expected.Type, expected.Sequence),
			string(msg.Type),
			fmt.Errorf("got outbound before expected capture event"),
		)
		r.failLocked(SessionReplayDiverged, err)
		return messages.SessionSendOutcome{Status: messages.SessionSendTerminalFailure, Err: err}
	}
	if err := compareCapturedStreamMessage(expected, msg); err != nil {
		mismatchErr := newReplayMismatchError(
			replayEventDescription(expected.Sequence, expected.Type),
			replayEventDescription(expected.Sequence, string(msg.Type)),
			err,
		)
		r.failLocked(SessionReplayDiverged, mismatchErr)
		return messages.SessionSendOutcome{Status: messages.SessionSendTerminalFailure, Err: mismatchErr}
	}

	r.sentLog = append(r.sentLog, msg)
	r.index++
	r.cond.Broadcast()
	return messages.SessionSendOutcome{Status: messages.SessionSendSucceeded}
}

func sessionSendContextOutcome(ctx context.Context) messages.SessionSendOutcome {
	err := ctx.Err()
	if err == context.DeadlineExceeded {
		return messages.SessionSendOutcome{Status: messages.SessionSendTimedOut, Err: err}
	}
	return messages.SessionSendOutcome{Status: messages.SessionSendCancelled, Err: err}
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
			err := newReplayIncompleteError(
				fmt.Sprintf("outbound event %s at sequence %d", evt.Type, evt.Sequence),
				"replay close",
				fmt.Errorf("session replay closed before expected outbound event"),
			)
			r.setOutcomeLocked(SessionReplayIncomplete, err)
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

// Outcome returns the current or terminal replay state. Callers should prefer
// this over inferring replay completion from Done plus Err, because incomplete
// replay and divergent replay both preserve replay mismatch error classes while
// carrying different lifecycle meanings.
func (r *SessionReplayer) Outcome() SessionReplayOutcome {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.outcome.Status != "" {
		return r.outcome
	}
	if r.err != nil {
		return replayOutcomeFromError(SessionReplayDiverged, r.err)
	}
	if r.index >= len(r.events) {
		return SessionReplayOutcome{Status: SessionReplayCompleted}
	}
	if r.closed {
		return SessionReplayOutcome{Status: SessionReplayIncomplete}
	}
	return SessionReplayOutcome{Status: SessionReplayOpen}
}

func (r *SessionReplayer) replayLoop() {
	defer r.close()

	var lastTimestamp int64
	for {
		r.mu.Lock()
		for r.validateOutbound && !r.closed && r.err == nil && r.replayCtx.Err() == nil && r.index < len(r.events) && r.events[r.index].Direction == DirectionClientToServer {
			r.cond.Wait()
		}
		if !r.closed && r.err == nil && r.replayCtx.Err() != nil {
			r.setOutcomeLocked(SessionReplayCancelled, r.replayCtx.Err())
			r.mu.Unlock()
			return
		}
		if r.closed || r.err != nil || r.index >= len(r.events) {
			if r.err == nil && r.index >= len(r.events) {
				r.setOutcomeLocked(SessionReplayCompleted, nil)
			}
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
				r.mu.Lock()
				r.setOutcomeLocked(SessionReplayCancelled, r.replayCtx.Err())
				r.mu.Unlock()
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
				r.mu.Lock()
				r.setOutcomeLocked(SessionReplayCancelled, r.replayCtx.Err())
				r.mu.Unlock()
				return
			}
		}
	}
}

func (r *SessionReplayer) watchReplayContext() {
	<-r.replayCtx.Done()
	r.mu.Lock()
	r.cond.Broadcast()
	r.mu.Unlock()
}

func (r *SessionReplayer) failLocked(status SessionReplayStatus, err error) {
	r.setOutcomeLocked(status, err)
	r.closeOnce.Do(func() {
		r.closed = true
		close(r.done)
	})
	r.cond.Broadcast()
}

func (r *SessionReplayer) setOutcomeLocked(status SessionReplayStatus, err error) {
	if r.err == nil {
		r.err = err
	}
	if r.outcome.Status == "" || r.outcome.Status == SessionReplayOpen {
		r.outcome = replayOutcomeFromError(status, err)
	}
}

func replayOutcomeFromError(status SessionReplayStatus, err error) SessionReplayOutcome {
	outcome := SessionReplayOutcome{Status: status, Err: err}
	var mismatch *gateway.ReplayMismatchError
	if errors.As(err, &mismatch) {
		outcome.Expected = mismatch.Expected
		outcome.Actual = mismatch.Actual
	}
	var incomplete *gateway.ReplayIncompleteError
	if errors.As(err, &incomplete) {
		outcome.Expected = incomplete.Expected
		outcome.Actual = incomplete.Actual
	}
	return outcome
}

func (r *SessionReplayer) close() {
	r.closeOnce.Do(func() {
		r.cancel()
		r.mu.Lock()
		if r.outcome.Status == "" && r.err == nil && r.index >= len(r.events) {
			r.setOutcomeLocked(SessionReplayCompleted, nil)
		}
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

func newReplayMismatchError(expected, actual string, err error) error {
	return errors.Join(
		gateway.NewReplayMismatchError(expected, actual, err),
		providers.ErrReplayMismatch,
	)
}

func newReplayIncompleteError(expected, actual string, err error) error {
	return errors.Join(
		gateway.NewReplayIncompleteError(expected, actual, err),
		providers.ErrReplayIncomplete,
	)
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
	if err := compareReplayPayloads(expectedPayload, actualPayload); err != nil {
		return err
	}
	if expected.Type != "" && expected.Type != string(actual.Type) {
		return fmt.Errorf("expected event type %q, got %q", expected.Type, actual.Type)
	}
	return nil
}

func decodeLegacySessionCaptureEvents(data []byte) ([]CapturedSessionEvent, error) {
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
