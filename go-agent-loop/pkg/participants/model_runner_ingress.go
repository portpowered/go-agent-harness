package participants

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// Session ingress: the single ordered queue through which user-side audio,
// control events, and complete messages reach a session runner.
//
// Admission is FIFO across every input kind, so a later audio frame can never
// overtake the MESSAGE.END that delimits the preceding turn. The one
// exception is a RESPONSE.CANCEL with no control input queued ahead of it: it
// takes a small priority lane and overtakes queued bulk audio, so an
// interrupt is not delayed behind seconds of microphone frames.

// SessionIngressError is a constant sentinel error for session ingress
// admission; being a constant, it cannot be reassigned. Match it with
// errors.Is.
type SessionIngressError string

func (e SessionIngressError) Error() string { return string(e) }

const (
	// ErrSessionInputQueueFull reports that the session ingress is at
	// capacity. Returning this bounded admission result keeps callers such as
	// tool result forwarding from waiting on a provider or an unbounded queue.
	ErrSessionInputQueueFull SessionIngressError = "session input queue is full"
	// ErrSessionClosed reports that a waiting session admission was abandoned
	// because the session runner stopped and will never drain the ingress again.
	ErrSessionClosed SessionIngressError = "session runner stopped; input was not admitted"
	// ErrNotSessionMode reports session input sent to a turn-based runner.
	ErrNotSessionMode SessionIngressError = "model runner is not in session mode"
	// errNilAdmissionContext reports a waiting admission without a context,
	// which could never be cancelled.
	errNilAdmissionContext SessionIngressError = "waiting session admission requires a context"
)

const (
	sessionIngressCapacity    = 72
	sessionCancelLaneCapacity = 4 // a full lane falls back to the ordered ingress
)

type sessionInputKind uint8

const (
	sessionInputAudio sessionInputKind = iota + 1
	sessionInputEvent
	sessionInputMessage
)

// SessionInput is one user-side input for a session runner. Build it with
// SessionAudio, SessionEvent, or SessionMessage.
type SessionInput struct {
	kind            sessionInputKind
	audio           messages.SessionAudioInput
	event           messages.StreamMessage
	message         messages.Message
	requestResponse bool
}

// SessionAudio is raw PCM. Contentful frames may barge in on an active
// response according to policy; silence always passes through. The runner
// retains the admitted slice.
func SessionAudio(pcm []byte, policy messages.SessionAudioInputPolicy) SessionInput {
	return SessionInput{kind: sessionInputAudio, audio: messages.SessionAudioInput{PCM: pcm, InterruptionPolicy: policy}}
}

// SessionEvent is a pre-built control-plane event (MESSAGE.END,
// RESPONSE.CREATE, RESPONSE.CANCEL, TOOLCALL.END, SESSION.UPDATE, ...)
// forwarded to the provider session under the runner's ordering rules.
func SessionEvent(msg messages.StreamMessage) SessionInput {
	return SessionInput{kind: sessionInputEvent, event: msg}
}

// SessionMessage is one complete user message for providers with a
// complete-message path (for example a multimodal opening turn).
// requestResponse selects whether the provider responds immediately or waits
// for a later audio commit.
func SessionMessage(msg messages.Message, requestResponse bool) SessionInput {
	return SessionInput{kind: sessionInputMessage, message: msg, requestResponse: requestResponse}
}

// SessionAdmission selects what EnqueueSessionInput does when the ingress is full.
type SessionAdmission uint8

const (
	// SessionAdmitOrFail returns ErrSessionInputQueueFull instead of waiting.
	// A nil context means "no cancellation".
	SessionAdmitOrFail SessionAdmission = iota
	// SessionAdmitWaiting waits for capacity, context cancellation, or runner
	// shutdown (ErrSessionClosed). A context is required.
	SessionAdmitWaiting
)

// SessionMode reports whether the runner drives a persistent provider session.
func (r *ModelRunner) SessionMode() bool { return r != nil && r.ingress != nil }

// EnqueueSessionInput admits one input to the session runner's ordered
// ingress. Inputs are forwarded to the provider in admission order. Admission
// takes a FIFO lock, so even SessionAdmitOrFail can wait behind a
// SessionAdmitWaiting admission parked on a full ingress until that admission
// is drained, cancelled, or released by runner shutdown. A RESPONSE.CANCEL
// with no control input queued ahead of it bypasses both and overtakes queued
// audio.
func (r *ModelRunner) EnqueueSessionInput(ctx context.Context, input SessionInput, admission SessionAdmission) error {
	if !r.SessionMode() {
		return ErrNotSessionMode
	}
	return r.ingress.admit(ctx, input, admission)
}

// sessionIngress owns every path from callers into the session runner.
type sessionIngress struct {
	// mu establishes the FIFO admission boundary across input kinds.
	mu      sync.Mutex
	ordered chan SessionInput
	// priority carries RESPONSE.CANCEL ahead of queued audio.
	priority chan messages.StreamMessage
	// queuedControls counts non-audio inputs admitted (or being admitted) to
	// ordered but not yet consumed. A cancel never overtakes one of them.
	queuedControls atomic.Int64
	stop           sessionIngressStop
	toolEvents     pendingToolEvents
}

func newSessionIngress() *sessionIngress {
	return &sessionIngress{
		ordered:  make(chan SessionInput, sessionIngressCapacity),
		priority: make(chan messages.StreamMessage, sessionCancelLaneCapacity),
	}
}

func (q *sessionIngress) admit(ctx context.Context, input SessionInput, admission SessionAdmission) error {
	if ctx == nil && admission == SessionAdmitWaiting {
		return errNilAdmissionContext
	}
	if input.kind == sessionInputEvent && q.admitPriorityCancel(input.event) {
		return nil
	}
	control := input.kind != sessionInputAudio
	if control {
		q.queuedControls.Add(1)
	}
	q.mu.Lock()
	err := q.push(ctx, input, admission)
	q.mu.Unlock()
	if err != nil && control {
		q.queuedControls.Add(-1)
	}
	return err
}

func (q *sessionIngress) admitPriorityCancel(msg messages.StreamMessage) bool {
	if msg.Type != messages.StreamTypeResponseCancel || q.queuedControls.Load() > 0 || q.stop.stopped() {
		return false
	}
	select {
	case q.priority <- msg:
		return true
	default:
		return false
	}
}

// push enqueues input under the admission lock. A nil ctx (allowed only for
// SessionAdmitOrFail) is never cancelled.
func (q *sessionIngress) push(ctx context.Context, input SessionInput, admission SessionAdmission) error {
	var cancelled <-chan struct{} // nil: never ready
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
		cancelled = ctx.Done()
	}
	toolEvent := input.kind == sessionInputEvent && isSessionToolEvent(input.event)
	if toolEvent {
		q.toolEvents.queued()
	}
	err := q.send(ctx, cancelled, input, admission)
	if err != nil && toolEvent {
		q.toolEvents.consumed()
	}
	return err
}

func (q *sessionIngress) send(ctx context.Context, cancelled <-chan struct{}, input SessionInput, admission SessionAdmission) error {
	if admission == SessionAdmitWaiting {
		if q.stop.stopped() {
			return ErrSessionClosed
		}
		select {
		case q.ordered <- input:
			return nil
		case <-cancelled:
			return ctx.Err()
		case <-q.stop.done():
			return ErrSessionClosed
		}
	}
	select {
	case q.ordered <- input:
		return nil
	case <-cancelled:
		return ctx.Err()
	default:
		return ErrSessionInputQueueFull
	}
}

// consumed records that the runner dequeued input from the ordered queue.
func (q *sessionIngress) consumed(input SessionInput) {
	if input.kind != sessionInputAudio {
		q.queuedControls.Add(-1)
	}
}

func isSessionToolEvent(msg messages.StreamMessage) bool {
	return msg.Type == messages.StreamTypeToolCallEnd || msg.Type == messages.StreamTypeResponseCreate
}

// pendingToolEvents counts tool-result boundary events accepted into the
// ingress but not yet forwarded by the runner. The coordinator can enqueue
// the follow-up inference request immediately after the forwarder returns,
// so the count closes the race where that request would otherwise send a
// bare RESPONSE.CREATE before the queued TOOLCALL.END and continuation.
type pendingToolEvents struct {
	mu    sync.Mutex
	count int
}

func (p *pendingToolEvents) queued() {
	p.mu.Lock()
	p.count++
	p.mu.Unlock()
}

func (p *pendingToolEvents) consumed() {
	p.mu.Lock()
	if p.count > 0 {
		p.count--
	}
	p.mu.Unlock()
}

func (p *pendingToolEvents) pending() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.count > 0
}

// sessionIngressStop is closed when the session runner returns, so admissions
// parked on a full ingress are released instead of waiting for a consumer that
// no longer exists. A later runSession on the same runner re-arms it.
type sessionIngressStop struct {
	mu     sync.Mutex
	ch     chan struct{}
	closed bool
}

func (s *sessionIngressStop) done() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ch == nil {
		s.ch = make(chan struct{})
	}
	return s.ch
}

func (s *sessionIngressStop) start() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		s.ch, s.closed = make(chan struct{}), false
	}
}

func (s *sessionIngressStop) stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ch == nil {
		s.ch = make(chan struct{})
	}
	if !s.closed {
		close(s.ch)
		s.closed = true
	}
}

func (s *sessionIngressStop) stopped() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}
