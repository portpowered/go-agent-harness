package rtc

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

type loopbackExchange struct {
	mu                               sync.Mutex
	done                             chan struct{}
	wake                             [2]chan struct{}
	timeout                          time.Duration
	terminal                         bool
	err                              error
	description                      [2]*SessionDescription
	sent, received, complete, waited [2]bool
	candidates                       [2][]ICECandidate
	read                             [2]int
}

// LoopbackEndpoint is one caller-owned endpoint of an isolated in-memory
// signaling pair. It opens no network resources and shares terminal state
// with its paired endpoint.
type LoopbackEndpoint struct {
	exchange *loopbackExchange
	role     SignalingRole
}

var _ Signaling = (*LoopbackEndpoint)(nil)

func normalizeLoopbackConfig(config []SignalingConfig) (SignalingConfig, error) {
	if len(config) > 1 {
		return SignalingConfig{}, fmt.Errorf("%w: at most one config is allowed", ErrInvalidSignalingConfiguration)
	}
	if len(config) == 0 {
		return SignalingConfig{ICEGatheringTimeout: DefaultICEGatheringTimeout}, nil
	}
	timeout, err := config[0].timeout()
	if err != nil {
		return SignalingConfig{}, err
	}
	config[0].ICEGatheringTimeout = timeout
	return config[0], nil
}

// NewLoopbackSignalingPair returns an offerer and answerer. An omitted config
// uses DefaultICEGatheringTimeout; a supplied config must set a positive one.
func NewLoopbackSignalingPair(config ...SignalingConfig) (*LoopbackEndpoint, *LoopbackEndpoint, error) {
	cfg, err := normalizeLoopbackConfig(config)
	if err != nil {
		return nil, nil, err
	}
	x := &loopbackExchange{done: make(chan struct{}), timeout: cfg.ICEGatheringTimeout}
	x.wake[0], x.wake[1] = make(chan struct{}), make(chan struct{})
	offerer := &LoopbackEndpoint{exchange: x, role: OffererRole}
	answerer := &LoopbackEndpoint{exchange: x, role: AnswererRole}
	if cfg.Unreachable {
		x.endLocked(ErrSignalingUnreachable)
	}
	return offerer, answerer, nil
}

// NewLoopbackPair is a short alias for NewLoopbackSignalingPair.
func NewLoopbackPair(config ...SignalingConfig) (*LoopbackEndpoint, *LoopbackEndpoint, error) {
	return NewLoopbackSignalingPair(config...)
}

func (x *loopbackExchange) endLocked(err error) {
	if x.terminal {
		return
	}
	x.terminal, x.err = true, err
	x.description, x.candidates = [2]*SessionDescription{}, [2][]ICECandidate{}
	close(x.done)
	close(x.wake[0])
	close(x.wake[1])
}

func (x *loopbackExchange) signalLocked(role int) {
	close(x.wake[role])
	x.wake[role] = make(chan struct{})
}

func (x *loopbackExchange) operationErrorLocked() error {
	if !x.terminal {
		return nil
	}
	if x.err != nil {
		return x.err
	}
	return ErrSignalingClosed
}

func (x *loopbackExchange) fail(err error) error {
	x.mu.Lock()
	if !x.terminal {
		x.endLocked(err)
	}
	got := x.operationErrorLocked()
	x.mu.Unlock()
	return got
}

func (e *LoopbackEndpoint) index() int {
	if e.role == OffererRole {
		return 0
	}
	return 1
}

func (e *LoopbackEndpoint) prepare(ctx context.Context, role SignalingRole) error {
	if e.role != role {
		return e.exchange.fail(ErrWrongSignalingRole)
	}
	return e.contextFailure(ctx)
}

func (e *LoopbackEndpoint) contextFailure(ctx context.Context) error {
	if ctx != nil && ctx.Err() != nil {
		return e.exchange.fail(fmt.Errorf("signaling context: %w", ctx.Err()))
	}
	return nil
}

func (e *LoopbackEndpoint) Role() SignalingRole   { return e.role }
func (e *LoopbackEndpoint) Done() <-chan struct{} { return e.exchange.done }
func (e *LoopbackEndpoint) Close() error {
	e.exchange.mu.Lock()
	e.exchange.endLocked(ErrSignalingClosed)
	e.exchange.mu.Unlock()
	return nil
}

func (e *LoopbackEndpoint) SendOffer(ctx context.Context, offer SessionDescription) error {
	return e.sendDescription(ctx, offer, OffererRole)
}

func (e *LoopbackEndpoint) SendAnswer(ctx context.Context, answer SessionDescription) error {
	return e.sendDescription(ctx, answer, AnswererRole)
}

func (e *LoopbackEndpoint) sendDescription(ctx context.Context, description SessionDescription, role SignalingRole) error {
	if err := e.prepare(ctx, role); err != nil {
		return err
	}
	x := e.exchange
	x.mu.Lock()
	defer x.mu.Unlock()
	i := e.index()
	if err := x.operationErrorLocked(); err != nil {
		return err
	}
	if i == 1 && !x.received[1] {
		x.endLocked(ErrAnswerBeforeOffer)
		return x.operationErrorLocked()
	}
	if x.sent[i] {
		x.endLocked(ErrInvalidSignalingOrder)
		return x.operationErrorLocked()
	}
	copy := description
	x.description[i], x.sent[i] = &copy, true
	x.signalLocked(1 - i)
	return nil
}

func (e *LoopbackEndpoint) ReceiveOffer(ctx context.Context) (SessionDescription, error) {
	if err := e.prepare(ctx, AnswererRole); err != nil {
		return SessionDescription{}, err
	}
	return e.receiveDescription(ctx, "offer", false)
}

func (e *LoopbackEndpoint) ReceiveAnswer(ctx context.Context) (SessionDescription, error) {
	if err := e.prepare(ctx, OffererRole); err != nil {
		return SessionDescription{}, err
	}
	return e.receiveDescription(ctx, "answer", true)
}

func (e *LoopbackEndpoint) receiveDescription(ctx context.Context, want string, requireOffer bool) (SessionDescription, error) {
	i, remote := e.index(), 1-e.index()
	for {
		x := e.exchange
		x.mu.Lock()
		if err := x.operationErrorLocked(); err != nil {
			x.mu.Unlock()
			return SessionDescription{}, err
		}
		if requireOffer && !x.sent[0] {
			x.endLocked(ErrInvalidSignalingOrder)
			err := x.operationErrorLocked()
			x.mu.Unlock()
			return SessionDescription{}, err
		}
		if x.received[i] {
			x.mu.Unlock()
			return SessionDescription{}, ErrInvalidSignalingOrder
		}
		if x.description[remote] != nil {
			description := *x.description[remote]
			x.received[i] = true
			if !validDescription(description, want) {
				kind := ErrMalformedOffer
				if want == "answer" {
					kind = ErrMalformedAnswer
				}
				x.endLocked(kind)
				err := x.operationErrorLocked()
				x.mu.Unlock()
				return SessionDescription{}, err
			}
			x.mu.Unlock()
			return description, nil
		}
		wake, done := x.wake[i], x.done
		x.mu.Unlock()
		if waitOutcome(ctx, time.Time{}, done, wake) == waitCanceled {
			return SessionDescription{}, e.contextFailure(ctx)
		}
	}
}

func (e *LoopbackEndpoint) SendCandidate(ctx context.Context, candidate ICECandidate) error {
	if err := e.contextFailure(ctx); err != nil {
		return err
	}
	if strings.TrimSpace(candidate.Candidate) == "" {
		return e.exchange.fail(ErrMalformedCandidate)
	}
	x := e.exchange
	x.mu.Lock()
	defer x.mu.Unlock()
	i := e.index()
	if err := x.operationErrorLocked(); err != nil {
		return err
	}
	if i == 1 && !x.received[1] {
		x.endLocked(ErrAnswerBeforeOffer)
		return x.operationErrorLocked()
	}
	if !x.sent[i] || x.complete[i] {
		x.endLocked(ErrInvalidSignalingOrder)
		return x.operationErrorLocked()
	}
	x.candidates[i] = append(x.candidates[i], candidate)
	x.signalLocked(1 - i)
	return nil
}

func (e *LoopbackEndpoint) ReceiveCandidate(ctx context.Context) (ICECandidate, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	i := e.index()
	x := e.exchange
	x.mu.Lock()
	if !x.received[i] {
		x.endLocked(ErrInvalidSignalingOrder)
		err := x.operationErrorLocked()
		x.mu.Unlock()
		return ICECandidate{}, err
	}
	x.mu.Unlock()
	deadline := time.Now().Add(x.timeout)
	for {
		x.mu.Lock()
		remote := 1 - i
		if err := x.operationErrorLocked(); err != nil {
			x.mu.Unlock()
			return ICECandidate{}, err
		}
		if x.read[remote] < len(x.candidates[remote]) {
			candidate := x.candidates[remote][x.read[remote]]
			x.read[remote]++
			x.mu.Unlock()
			return candidate, nil
		}
		if x.complete[remote] {
			if len(x.candidates[remote]) == 0 {
				x.endLocked(ErrNoCandidates)
				err := x.operationErrorLocked()
				x.mu.Unlock()
				return ICECandidate{}, err
			}
			x.mu.Unlock()
			return ICECandidate{}, ErrGatheringComplete
		}
		wake, done := x.wake[i], x.done
		x.mu.Unlock()
		outcome := waitOutcome(ctx, deadline, done, wake)
		if outcome == waitCanceled {
			return ICECandidate{}, e.contextFailure(ctx)
		}
		if outcome == waitTimedOut {
			return ICECandidate{}, x.fail(ErrICEGatheringTimeout)
		}
	}
}

func (e *LoopbackEndpoint) CompleteCandidateGathering(ctx context.Context) error {
	if err := e.contextFailure(ctx); err != nil {
		return err
	}
	x := e.exchange
	x.mu.Lock()
	defer x.mu.Unlock()
	i := e.index()
	if err := x.operationErrorLocked(); err != nil {
		return err
	}
	if i == 1 && !x.received[1] {
		x.endLocked(ErrAnswerBeforeOffer)
		return x.operationErrorLocked()
	}
	if !x.sent[i] || x.complete[i] {
		x.endLocked(ErrInvalidSignalingOrder)
		return x.operationErrorLocked()
	}
	x.complete[i] = true
	x.signalLocked(1 - i)
	return nil
}

func (e *LoopbackEndpoint) WaitCandidateGathering(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	i := e.index()
	deadline := time.Now().Add(e.exchange.timeout)
	for {
		x := e.exchange
		x.mu.Lock()
		if x.terminal {
			err := x.err
			x.mu.Unlock()
			return err
		}
		if x.complete[i] {
			if len(x.candidates[i]) == 0 {
				x.endLocked(ErrNoCandidates)
				err := x.operationErrorLocked()
				x.mu.Unlock()
				return err
			}
			x.waited[i] = true
			x.maybeCompleteLocked()
			terminal, err := x.terminal, x.err
			x.mu.Unlock()
			if terminal {
				return err
			}
			return nil
		}
		wake, done := x.wake[i], x.done
		x.mu.Unlock()
		outcome := waitOutcome(ctx, deadline, done, wake)
		if outcome == waitCanceled {
			return e.contextFailure(ctx)
		}
		if outcome == waitTimedOut {
			return x.fail(ErrICEGatheringTimeout)
		}
	}
}

func (x *loopbackExchange) maybeCompleteLocked() {
	if x.terminal || !x.received[0] || !x.received[1] || !x.complete[0] || !x.complete[1] || !x.waited[0] || !x.waited[1] {
		return
	}
	if len(x.candidates[0]) == 0 || len(x.candidates[1]) == 0 || x.read[0] != len(x.candidates[0]) || x.read[1] != len(x.candidates[1]) {
		return
	}
	x.endLocked(nil)
}

func validDescription(description SessionDescription, want string) bool {
	return description.Type == want && strings.TrimSpace(description.SDP) != ""
}

type waitResult uint8

const (
	waitWoken waitResult = iota
	waitCanceled
	waitTimedOut
)

func waitOutcome(ctx context.Context, deadline time.Time, done, wake <-chan struct{}) waitResult {
	if ctx == nil {
		ctx = context.Background()
	}
	var timer *time.Timer
	var timerC <-chan time.Time
	if !deadline.IsZero() {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return waitTimedOut
		}
		timer = time.NewTimer(remaining)
		timerC = timer.C
		defer timer.Stop()
	}
	select {
	case <-wake:
		return waitWoken
	case <-done:
		return waitWoken
	case <-ctx.Done():
		return waitCanceled
	case <-timerC:
		return waitTimedOut
	}
}
