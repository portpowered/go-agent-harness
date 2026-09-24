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
	sentLog []messages.StreamMessage
	index   int
	err     error
	outcome SessionReplayOutcome
	closed  bool
	cond    *sync.Cond
	// outboundValidationIndex is independent from the chronological replay
	// cursor. Send validates the next client record synchronously, while the
	// replay pump later consumes that admission at its chronological position.
	// This lets the pump publish a long server run without making the caller
	// wait for the bounded receive buffer to drain.
	outboundValidationIndex int
	validatedOutbound       int
	mu                      sync.Mutex
	replayCtx               context.Context
	cancel                  context.CancelFunc
}

const maxPendingReplayOutbound = 64

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
	if r.closed {
		r.mu.Unlock()
		return messages.SessionSendOutcome{Status: messages.SessionSendClosed}
	}
	if r.err != nil {
		err := r.err
		r.mu.Unlock()
		return messages.SessionSendOutcome{Status: messages.SessionSendTerminalFailure, Err: err}
	}
	if !r.validateOutbound {
		r.sentLog = append(r.sentLog, msg)
		r.mu.Unlock()
		return messages.SessionSendOutcome{Status: messages.SessionSendSucceeded}
	}
	if r.validatedOutbound >= maxPendingReplayOutbound {
		r.mu.Unlock()
		return messages.SessionSendOutcome{Status: messages.SessionSendBufferFull}
	}
	expectedIndex, ok := r.nextValidationOutboundLocked()
	if !ok {
		err := newReplayMismatchError("replay completed", string(msg.Type), fmt.Errorf("unexpected outbound event after replay completed"))
		r.failLocked(SessionReplayDiverged, err)
		r.mu.Unlock()
		return messages.SessionSendOutcome{Status: messages.SessionSendTerminalFailure, Err: err}
	}
	expected := r.events[expectedIndex]
	if err := compareCapturedStreamMessage(expected, msg); err != nil {
		mismatchErr := newReplayMismatchError(
			replayEventDescription(expected.Sequence, expected.Type),
			replayEventDescription(expected.Sequence, string(msg.Type)),
			err,
		)
		r.failLocked(SessionReplayDiverged, mismatchErr)
		r.mu.Unlock()
		return messages.SessionSendOutcome{Status: messages.SessionSendTerminalFailure, Err: mismatchErr}
	}

	r.sentLog = append(r.sentLog, msg)
	r.outboundValidationIndex = expectedIndex + 1
	r.validatedOutbound++
	r.cond.Broadcast()
	r.mu.Unlock()
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
		evt, eventIndex, deliver, stop := r.nextReplayEvent()
		if stop {
			return
		}
		if !deliver {
			continue
		}
		if !r.waitReplayTiming(evt.TimestampMs, lastTimestamp) {
			return
		}
		lastTimestamp = evt.TimestampMs
		if !r.deliverReplayEvent(evt, eventIndex) {
			return
		}
	}
}

// nextReplayEvent waits for the chronological cursor to reach a deliverable
// server record. Client records consume their Send admission and are skipped
// (deliver=false); stop reports that replay has finished or been cancelled.
func (r *SessionReplayer) nextReplayEvent() (evt CapturedSessionEvent, eventIndex int, deliver, stop bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for r.awaitingOutboundAdmissionLocked() {
		r.cond.Wait()
	}
	if !r.closed && r.err == nil && r.replayCtx.Err() != nil {
		r.setOutcomeLocked(SessionReplayCancelled, r.replayCtx.Err())
		return evt, 0, false, true
	}
	if r.closed || r.err != nil || r.index >= len(r.events) {
		if r.err == nil && r.index >= len(r.events) && r.validatedOutbound == 0 {
			r.setOutcomeLocked(SessionReplayCompleted, nil)
		}
		return evt, 0, false, true
	}
	evt, eventIndex = r.events[r.index], r.index
	if evt.Direction != DirectionServerToClient {
		// With validation enabled, Send has already checked this record and
		// reserved one admission. Consume it in capture order. Validation is
		// disabled for transcript rendering, so client records are skipped.
		if r.validateOutbound {
			r.validatedOutbound--
		}
		r.index++
		r.cond.Broadcast()
		return evt, eventIndex, false, false
	}
	return evt, eventIndex, true, false
}

// awaitingOutboundAdmissionLocked reports whether the cursor is parked on a
// client record that Send has not yet validated and admitted.
func (r *SessionReplayer) awaitingOutboundAdmissionLocked() bool {
	if !r.validateOutbound || r.closed || r.err != nil || r.replayCtx.Err() != nil {
		return false
	}
	return r.index < len(r.events) && r.events[r.index].Direction == DirectionClientToServer && r.validatedOutbound == 0
}

// waitReplayTiming sleeps for the captured inter-event delay when timing is
// enabled; it returns false when replay closed or was cancelled meanwhile.
func (r *SessionReplayer) waitReplayTiming(timestampMs, lastTimestamp int64) bool {
	if !r.useTiming || timestampMs <= lastTimestamp {
		return true
	}
	select {
	case <-r.done:
		return false
	case <-r.replayCtx.Done():
		r.mu.Lock()
		r.setOutcomeLocked(SessionReplayCancelled, r.replayCtx.Err())
		r.mu.Unlock()
		return false
	case <-time.After(time.Duration(timestampMs-lastTimestamp) * time.Millisecond):
		return true
	}
}

// deliverReplayEvent publishes one server record and advances the cursor; it
// returns false when delivery stopped because replay closed or was cancelled.
func (r *SessionReplayer) deliverReplayEvent(evt CapturedSessionEvent, eventIndex int) bool {
	msg, err := deserializeStreamMessage(evt)
	if err != nil {
		fmt.Fprintf(os.Stderr, "session replayer: skipping event (type=%s): %v\n", evt.Type, err)
		r.advanceReplayCursor(evt, eventIndex)
		return true
	}

	// Write to the bounded outbound buffer with cancellation-aware
	// backpressure. A dropped server event would falsely advance the
	// capture cursor and let a later client send diverge. Client admission is
	// tracked separately, so this wait cannot strand the sending goroutine.
	outcome := r.outbound.WriteWaitContextOrDone(r.replayCtx, r.done, msg)
	if !outcome.OK() {
		r.mu.Lock()
		if r.replayCtx.Err() != nil {
			r.setOutcomeLocked(SessionReplayCancelled, r.replayCtx.Err())
		}
		r.mu.Unlock()
		return false
	}
	// The replay loop owns the chronological index. Advance only after the
	// normalized message is in the public buffer, so chronological
	// publication cannot advance beyond an undelivered server event.
	r.advanceReplayCursor(evt, eventIndex)
	return true
}

func (r *SessionReplayer) advanceReplayCursor(evt CapturedSessionEvent, eventIndex int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.closed && r.err == nil && r.index == eventIndex && r.events[r.index].Sequence == evt.Sequence {
		r.index++
		r.cond.Broadcast()
	}
}

// nextValidationOutboundLocked finds the next client record from the
// independent validation cursor. Server records between client messages are
// intentionally skipped here; the chronological replay cursor still blocks
// later server delivery until the corresponding client admission is consumed.
func (r *SessionReplayer) nextValidationOutboundLocked() (int, bool) {
	for i := r.outboundValidationIndex; i < len(r.events); i++ {
		if r.events[i].Direction == DirectionClientToServer {
			return i, true
		}
	}
	return 0, false
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
	for i := r.outboundValidationIndex; i < len(r.events); i++ {
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
