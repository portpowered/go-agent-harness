package engine

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

const (
	messageReplayBufferSize = 64
	messageReplayPendingMax = 64
)

type messageReplayStatus string

const (
	messageReplayOpen       messageReplayStatus = "open"
	messageReplayComplete   messageReplayStatus = "completed"
	messageReplayDiverged   messageReplayStatus = "diverged"
	messageReplayIncomplete messageReplayStatus = "incomplete"
	messageReplayCancelled  messageReplayStatus = "cancelled"
)

// MessageSession replays a captured bidirectional StreamMessage sequence.
// Its cursors, outbound admission, cancellation and completion state belong to
// the replay service, which is the only production constructor.
type MessageSession struct {
	events           []gatewaytesting.CapturedSessionEvent
	validateOutbound bool
	outbound         *messages.TypedBuffer[messages.StreamMessage]
	done             chan struct{}
	closeOnce        sync.Once
	index            int
	outboundIndex    int
	validatedPending int
	err              error
	status           messageReplayStatus
	closed           bool
	cond             *sync.Cond
	mu               sync.Mutex
	ctx              context.Context
	cancel           context.CancelFunc
}

var _ messages.Session = (*MessageSession)(nil)
var _ messages.SessionSendOutcomeSender = (*MessageSession)(nil)

// NewMessageSession takes an owned copy of the capture sequence and starts a
// bounded replay. validateOutbound must remain true for provider sessions; the
// read-only CaptureReplay contract disables it because it only drains inbound
// messages.
func NewMessageSession(events []gatewaytesting.CapturedSessionEvent, ctx context.Context, validateOutbound bool) *MessageSession {
	if ctx == nil {
		ctx = context.Background()
	}
	owned := cloneCaptureEvents(events)
	r := &MessageSession{
		events:           owned,
		validateOutbound: validateOutbound,
		outbound:         messages.NewTypedBuffer[messages.StreamMessage](messageReplayBufferSize),
		done:             make(chan struct{}),
	}
	r.cond = sync.NewCond(&r.mu)
	r.ctx, r.cancel = context.WithCancel(ctx)
	go r.watchContext()
	go r.replayLoop()
	return r
}

func (r *MessageSession) Send(ctx context.Context, message messages.StreamMessage) bool {
	return r.SendWithOutcome(ctx, message).OK()
}

func (r *MessageSession) SendWithOutcome(ctx context.Context, message messages.StreamMessage) messages.SessionSendOutcome {
	if ctx == nil {
		return messages.SessionSendOutcome{Status: messages.SessionSendTerminalFailure, Err: errors.New("replay send requires a context")}
	}
	select {
	case <-ctx.Done():
		return sendContextOutcome(ctx)
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
		r.mu.Unlock()
		return messages.SessionSendOutcome{Status: messages.SessionSendSucceeded}
	}
	if r.validatedPending >= messageReplayPendingMax {
		r.mu.Unlock()
		return messages.SessionSendOutcome{Status: messages.SessionSendBufferFull}
	}
	expectedIndex, ok := r.nextOutboundIndexLocked()
	if !ok {
		err := replayMismatch("replay completed", string(message.Type), errors.New("unexpected outbound event after replay completed"))
		r.failLocked(messageReplayDiverged, err)
		r.mu.Unlock()
		return messages.SessionSendOutcome{Status: messages.SessionSendTerminalFailure, Err: err}
	}
	expected := r.events[expectedIndex]
	if err := compareStreamEvent(expected, message); err != nil {
		mismatch := replayMismatch(
			eventDescription(expected.Sequence, expected.Type),
			eventDescription(expected.Sequence, string(message.Type)),
			err,
		)
		r.failLocked(messageReplayDiverged, mismatch)
		r.mu.Unlock()
		return messages.SessionSendOutcome{Status: messages.SessionSendTerminalFailure, Err: mismatch}
	}
	r.outboundIndex = expectedIndex + 1
	r.validatedPending++
	r.cond.Broadcast()
	r.mu.Unlock()
	return messages.SessionSendOutcome{Status: messages.SessionSendSucceeded}
}

func (r *MessageSession) Receive() *messages.TypedBuffer[messages.StreamMessage] { return r.outbound }

func (r *MessageSession) Done() <-chan struct{} { return r.done }

func (r *MessageSession) Err() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

func (r *MessageSession) Close() error {
	r.mu.Lock()
	if r.validateOutbound && !r.closed && r.err == nil && r.index < len(r.events) {
		if event, ok := r.nextExpectedOutboundLocked(); ok {
			err := replayIncomplete(
				outboundEventDescription(event),
				"replay close",
				errors.New("session replay closed before expected outbound event"),
			)
			r.setOutcomeLocked(messageReplayIncomplete, err)
		}
	}
	r.mu.Unlock()
	r.cancel()
	r.close()
	return nil
}

func (r *MessageSession) replayLoop() {
	defer r.close()
	for {
		event, eventIndex, ok := r.nextReplayEvent()
		if !ok {
			return
		}
		if event.Direction != gatewaytesting.DirectionServerToClient {
			r.advanceOutboundEvent()
			continue
		}
		if !r.deliverReplayEvent(event, eventIndex) {
			return
		}
	}
}

func (r *MessageSession) nextReplayEvent() (gatewaytesting.CapturedSessionEvent, int, bool) {
	for {
		r.mu.Lock()
		if r.awaitingOutboundLocked() {
			r.cond.Wait()
			r.mu.Unlock()
			continue
		}
		if !r.closed && r.err == nil && r.ctx.Err() != nil {
			r.setOutcomeLocked(messageReplayCancelled, r.ctx.Err())
		}
		if r.closed || r.err != nil || r.index >= len(r.events) {
			r.finishMessageReplayLocked()
			r.mu.Unlock()
			return gatewaytesting.CapturedSessionEvent{}, 0, false
		}
		event, index := r.events[r.index], r.index
		r.mu.Unlock()
		return event, index, true
	}
}

func (r *MessageSession) awaitingOutboundLocked() bool {
	return r.validateOutbound && !r.closed && r.err == nil && r.ctx.Err() == nil && r.index < len(r.events) && r.events[r.index].Direction == gatewaytesting.DirectionClientToServer && r.validatedPending == 0
}

func (r *MessageSession) finishMessageReplayLocked() {
	if r.err == nil && r.index >= len(r.events) && r.validatedPending == 0 {
		r.setOutcomeLocked(messageReplayComplete, nil)
	}
}

func (r *MessageSession) advanceOutboundEvent() {
	r.mu.Lock()
	if !r.closed && r.err == nil && r.index < len(r.events) {
		if r.validateOutbound {
			r.validatedPending--
		}
		r.index++
		r.cond.Broadcast()
	}
	r.mu.Unlock()
}

func (r *MessageSession) deliverReplayEvent(event gatewaytesting.CapturedSessionEvent, eventIndex int) bool {
	message, err := decodeStreamEvent(event)
	if err != nil {
		r.mu.Lock()
		if !r.closed && r.err == nil && r.index == eventIndex {
			r.failLocked(messageReplayDiverged, fmt.Errorf("decode replay event at sequence %d: %w", event.Sequence, err))
		}
		r.mu.Unlock()
		return false
	}
	if write := r.outbound.WriteWaitContextOrDone(r.ctx, r.done, message); !write.OK() {
		r.mu.Lock()
		if r.ctx.Err() != nil {
			r.setOutcomeLocked(messageReplayCancelled, r.ctx.Err())
		}
		r.mu.Unlock()
		return false
	}
	r.mu.Lock()
	if !r.closed && r.err == nil && r.index == eventIndex {
		r.index++
		r.cond.Broadcast()
	}
	r.mu.Unlock()
	return true
}

func (r *MessageSession) nextOutboundIndexLocked() (int, bool) {
	for index := r.outboundIndex; index < len(r.events); index++ {
		if r.events[index].Direction == gatewaytesting.DirectionClientToServer {
			return index, true
		}
	}
	return 0, false
}

func (r *MessageSession) nextExpectedOutboundLocked() (gatewaytesting.CapturedSessionEvent, bool) {
	index, ok := r.nextOutboundIndexLocked()
	if !ok {
		return gatewaytesting.CapturedSessionEvent{}, false
	}
	return r.events[index], true
}

func (r *MessageSession) watchContext() {
	<-r.ctx.Done()
	r.mu.Lock()
	r.cond.Broadcast()
	r.mu.Unlock()
}

func (r *MessageSession) failLocked(status messageReplayStatus, err error) {
	r.setOutcomeLocked(status, err)
	r.cancel()
	r.closeOnce.Do(func() {
		r.closed = true
		close(r.done)
	})
	r.cond.Broadcast()
}

func (r *MessageSession) setOutcomeLocked(status messageReplayStatus, err error) {
	if r.err == nil {
		r.err = err
	}
	if r.status == "" || r.status == messageReplayOpen {
		r.status = status
	}
}

func (r *MessageSession) close() {
	r.closeOnce.Do(func() {
		r.cancel()
		r.mu.Lock()
		if r.status == "" && r.err == nil && r.index >= len(r.events) {
			r.setOutcomeLocked(messageReplayComplete, nil)
		}
		r.closed = true
		close(r.done)
		r.mu.Unlock()
	})
	r.cond.Broadcast()
}
