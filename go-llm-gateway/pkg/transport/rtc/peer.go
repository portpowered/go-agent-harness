package rtc

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sync"
	"time"
)

type State string

const (
	StateIdle            State = "idle"
	StateNew             State = StateIdle
	StateConnecting      State = "connecting"
	StateConnected       State = "connected"
	StateReconnecting    State = "reconnecting"
	StateTerminalFailure State = "terminal-failure"
	StateFailed          State = StateTerminalFailure
	StateClosed          State = "closed"
)

type PeerState = State

var (
	ErrPeerClosed          = errors.New("rtc peer is closed")
	ErrPeerNotConnected    = errors.New("rtc peer is not connected")
	ErrPeerLost            = errors.New("rtc peer connection was lost")
	ErrPeerTerminalFailure = errors.New("rtc peer reached terminal failure")
	ErrTerminalFailure     = ErrPeerTerminalFailure
	ErrRetryExhausted      = errors.New("rtc peer retry attempts exhausted")
	ErrNoDialer            = errors.New("rtc peer has no cancellation-aware dialer")
	ErrNilConnection       = errors.New("rtc dialer returned a nil connection")
)

type TerminalError struct {
	Cause    error
	Attempts int
}

func (e *TerminalError) Error() string {
	return fmt.Sprintf("rtc peer terminal failure after %d attempts: %v", e.Attempts, e.Cause)
}
func (e *TerminalError) Unwrap() error { return e.Cause }
func (e *TerminalError) Is(target error) bool {
	return target == ErrPeerTerminalFailure || target == ErrTerminalFailure
}

// ContextDialer is required so Close can cancel an in-flight dial without a worker leak.
type ContextDialer interface {
	DialContext(context.Context, string, map[string]string) (Conn, error)
}

type RetryPolicy struct {
	MaxAttempts int
	Backoff     time.Duration
	MaxBackoff  time.Duration
	Wait        func(context.Context, time.Duration) error
}
type PeerConfig struct {
	Dialer   ContextDialer
	Endpoint string
	Headers  map[string]string
	Retry    RetryPolicy
}
type Transition struct {
	From, To     State
	Attempt      int
	Cause, Error error
}
type operation struct {
	done   chan struct{}
	cancel context.CancelFunc
	err    error
}
type Peer struct {
	mu          sync.RWMutex
	state       State
	attempts    int
	terminalErr error
	conn        Conn
	history     []Transition
	dialer      ContextDialer
	endpoint    string
	headers     map[string]string
	policy      RetryPolicy
	ctx         context.Context
	cancel      context.CancelFunc
	op          *operation
	closed      bool
	closeOnce   sync.Once
	closeErr    error
}

func NewPeer(config PeerConfig) *Peer {
	policy := config.Retry
	if policy.MaxAttempts <= 0 {
		policy.MaxAttempts = 3
	}
	if policy.MaxBackoff <= 0 {
		policy.MaxBackoff = time.Second
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Peer{state: StateIdle, dialer: config.Dialer, endpoint: config.Endpoint, headers: cloneHeaders(config.Headers), policy: policy, ctx: ctx, cancel: cancel}
}
func (p *Peer) State() State     { p.mu.RLock(); defer p.mu.RUnlock(); return p.state }
func (p *Peer) Err() error       { p.mu.RLock(); defer p.mu.RUnlock(); return p.terminalErr }
func (p *Peer) Attempts() int    { p.mu.RLock(); defer p.mu.RUnlock(); return p.attempts }
func (p *Peer) Connection() Conn { p.mu.RLock(); defer p.mu.RUnlock(); return p.conn }
func (p *Peer) Transitions() []Transition {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return append([]Transition(nil), p.history...)
}

func (p *Peer) Connect(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	op, runCtx, _, run, err := p.start(false, nil)
	if err != nil {
		return err
	}
	if !run {
		if op == nil {
			return nil
		}
		return waitOperation(ctx, op)
	}
	stop := context.AfterFunc(ctx, op.cancel)
	err = p.run(runCtx)
	stop()
	op.cancel()
	p.finish(op, err)
	return err
}

func (p *Peer) PeerLost(cause error) error {
	if cause == nil {
		cause = ErrPeerLost
	}
	op, runCtx, old, run, err := p.start(true, cause)
	if err != nil {
		return err
	}
	closeConn(old)
	if run {
		go func() { p.finish(op, p.run(runCtx)); op.cancel() }()
	}
	return nil
}

func (p *Peer) start(reconnect bool, cause error) (*operation, context.Context, Conn, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, nil, nil, false, ErrPeerClosed
	}
	if p.state == StateTerminalFailure {
		return nil, nil, nil, false, p.terminalErr
	}
	if p.op != nil {
		if reconnect && p.state != StateReconnecting {
			return nil, nil, nil, false, ErrPeerNotConnected
		}
		return p.op, nil, nil, false, nil
	}
	if reconnect {
		if p.state != StateConnected {
			return nil, nil, nil, false, ErrPeerNotConnected
		}
		old := p.conn
		p.conn = nil
		p.attempts = 0
		p.transitionLocked(StateReconnecting, cause, 0)
		op, opCtx := p.newOperationLocked()
		return op, opCtx, old, true, nil
	}
	if p.state == StateConnected {
		return nil, nil, nil, false, nil
	}
	p.attempts, p.terminalErr = 0, nil
	p.transitionLocked(StateConnecting, nil, 0)
	op, opCtx := p.newOperationLocked()
	return op, opCtx, nil, true, nil
}

func (p *Peer) newOperationLocked() (*operation, context.Context) {
	opCtx, cancel := context.WithCancel(p.ctx)
	op := &operation{done: make(chan struct{}), cancel: cancel}
	p.op = op
	return op, opCtx
}

func (p *Peer) Wait(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	p.mu.RLock()
	op, state, err := p.op, p.state, p.terminalErr
	p.mu.RUnlock()
	if op != nil {
		return waitOperation(ctx, op)
	}
	if state == StateTerminalFailure {
		return err
	}
	if state == StateClosed {
		return ErrPeerClosed
	}
	return nil
}

func (p *Peer) Close() error {
	p.closeOnce.Do(func() {
		p.mu.Lock()
		p.closed = true
		p.cancel()
		op := p.op
		if op != nil {
			op.cancel()
		}
		conn := p.conn
		p.conn = nil
		p.transitionLocked(StateClosed, nil, p.attempts)
		p.mu.Unlock()
		p.closeErr = closeConn(conn)
		if op != nil {
			<-op.done
		}
	})
	return p.closeErr
}

func (p *Peer) run(ctx context.Context) error {
	var last error
	for attempt := 1; attempt <= p.policy.MaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return p.cancelled(err, attempt-1)
		}
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			return ErrPeerClosed
		}
		p.attempts = attempt
		p.mu.Unlock()
		conn, err := p.dial(ctx)
		if err == nil && conn == nil {
			err = ErrNilConnection
		}
		if err == nil {
			return p.accept(conn, attempt)
		}
		closeConn(conn)
		last = err
		if p.isClosed() {
			return ErrPeerClosed
		}
		if err = ctx.Err(); err != nil {
			return p.cancelled(err, attempt)
		}
		if attempt == p.policy.MaxAttempts {
			return p.terminal(last, attempt, true)
		}
		if err = p.backoff(ctx); err != nil {
			if p.isClosed() {
				return ErrPeerClosed
			}
			return p.cancelled(err, attempt)
		}
	}
	return p.terminal(last, p.policy.MaxAttempts, true)
}
func (p *Peer) dial(ctx context.Context) (Conn, error) {
	if p.dialer == nil {
		return nil, ErrNoDialer
	}
	return p.dialer.DialContext(ctx, p.endpoint, cloneHeaders(p.headers))
}
func (p *Peer) backoff(ctx context.Context) error {
	delay := p.policy.Backoff
	if delay <= 0 {
		return nil
	}
	if delay > p.policy.MaxBackoff {
		delay = p.policy.MaxBackoff
	}
	if p.policy.Wait != nil {
		return p.policy.Wait(ctx, delay)
	}
	t := time.NewTimer(delay)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (p *Peer) accept(conn Conn, attempt int) error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		closeConn(conn)
		return ErrPeerClosed
	}
	old := p.conn
	p.conn, p.terminalErr = conn, nil
	p.transitionLocked(StateConnected, nil, attempt)
	p.mu.Unlock()
	closeConn(old)
	return nil
}
func (p *Peer) cancelled(cause error, attempts int) error {
	if p.isClosed() {
		return ErrPeerClosed
	}
	return p.terminal(cause, attempts, false)
}
func (p *Peer) terminal(cause error, attempts int, exhausted bool) error {
	if cause == nil {
		cause = ErrRetryExhausted
	}
	if exhausted {
		cause = errors.Join(ErrRetryExhausted, cause)
	}
	err := &TerminalError{Cause: cause, Attempts: attempts}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return ErrPeerClosed
	}
	p.terminalErr = err
	conn := p.conn
	p.conn = nil
	p.transitionLocked(StateTerminalFailure, err, attempts)
	p.mu.Unlock()
	closeConn(conn)
	return err
}
func (p *Peer) finish(op *operation, err error) {
	p.mu.Lock()
	if p.op == op {
		p.op = nil
	}
	op.err = err
	close(op.done)
	p.mu.Unlock()
}
func waitOperation(ctx context.Context, op *operation) error {
	select {
	case <-op.done:
		return op.err
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (p *Peer) transitionLocked(to State, cause error, attempt int) {
	if p.state == to {
		return
	}
	p.history = append(p.history, Transition{From: p.state, To: to, Attempt: attempt, Cause: cause, Error: cause})
	p.state = to
}
func (p *Peer) isClosed() bool { p.mu.RLock(); defer p.mu.RUnlock(); return p.closed }
func closeConn(conn Conn) error {
	if conn == nil {
		return nil
	}
	return conn.Close()
}
func cloneHeaders(headers map[string]string) map[string]string {
	return maps.Clone(headers)
}
