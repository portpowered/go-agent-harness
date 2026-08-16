package rtc

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// State is a peer's observable lifecycle state.
type State string

const (
	StateIdle            State = "idle"
	StateNew                   = StateIdle
	StateConnecting      State = "connecting"
	StateConnected       State = "connected"
	StateReconnecting    State = "reconnecting"
	StateTerminalFailure State = "terminal-failure"
	StateFailed                = StateTerminalFailure
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
	ErrNoDialer            = errors.New("rtc peer has no dialer")
	ErrNilConnection       = errors.New("rtc dialer returned a nil connection")
)

// TerminalError retains the final cause and exact attempt count.
type TerminalError struct {
	Cause    error
	Attempts int
}

func (e *TerminalError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("rtc peer terminal failure after %d attempts: %v", e.Attempts, e.Cause)
}
func (e *TerminalError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}
func (e *TerminalError) Is(target error) bool {
	return target == ErrPeerTerminalFailure || target == ErrTerminalFailure || target == ErrRetryExhausted
}

// ContextDialer is an optional cancellation-aware extension to Dialer.
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
	Dialer   Dialer
	Endpoint string
	Headers  map[string]string
	Retry    RetryPolicy
}

type Transition struct {
	From    State
	To      State
	Attempt int
	Cause   error
	Error   error
}

type operation struct {
	done   chan struct{}
	cancel context.CancelFunc
	result error
}

// Peer owns one connection and all of its retry, cancellation, and teardown work.
type Peer struct {
	mu sync.Mutex

	state       State
	attempts    int
	terminalErr error
	conn        Conn
	history     []Transition

	dialer   Dialer
	endpoint string
	headers  map[string]string
	policy   RetryPolicy

	ctx    context.Context
	cancel context.CancelFunc
	op     *operation

	closed    bool
	closeDone chan struct{}
	closeErr  error
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

func (p *Peer) State() State     { p.mu.Lock(); defer p.mu.Unlock(); return p.state }
func (p *Peer) Err() error       { p.mu.Lock(); defer p.mu.Unlock(); return p.terminalErr }
func (p *Peer) Attempts() int    { p.mu.Lock(); defer p.mu.Unlock(); return p.attempts }
func (p *Peer) Connection() Conn { p.mu.Lock(); defer p.mu.Unlock(); return p.conn }
func (p *Peer) Transitions() []Transition {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]Transition(nil), p.history...)
}

func (p *Peer) Connect(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return ErrPeerClosed
	}
	if p.state == StateConnected {
		p.mu.Unlock()
		return nil
	}
	if p.state == StateTerminalFailure {
		err := p.terminalErr
		p.mu.Unlock()
		return err
	}
	if p.op != nil {
		op := p.op
		p.mu.Unlock()
		return waitOperation(ctx, op)
	}
	p.attempts, p.terminalErr = 0, nil
	p.transitionLocked(StateConnecting, nil, 0)
	opCtx, cancel := context.WithCancel(p.ctx)
	op := &operation{done: make(chan struct{}), cancel: cancel}
	p.op = op
	p.mu.Unlock()
	stop := context.AfterFunc(ctx, cancel)
	err := p.runAttempts(opCtx)
	stop()
	cancel()
	p.finishOperation(op, err)
	return err
}

func (p *Peer) PeerLost(cause error) error { return p.startReconnect(context.Background(), cause) }

func (p *Peer) startReconnect(ctx context.Context, cause error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if cause == nil {
		cause = ErrPeerLost
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return ErrPeerClosed
	}
	if p.state == StateTerminalFailure {
		err := p.terminalErr
		p.mu.Unlock()
		return err
	}
	if p.state == StateReconnecting && p.op != nil {
		p.mu.Unlock()
		return nil
	}
	if p.state != StateConnected {
		p.mu.Unlock()
		return ErrPeerNotConnected
	}
	conn := p.conn
	p.conn, p.attempts = nil, 0
	p.transitionLocked(StateReconnecting, cause, 0)
	opCtx, cancel := context.WithCancel(p.ctx)
	op := &operation{done: make(chan struct{}), cancel: cancel}
	p.op = op
	p.mu.Unlock()
	closeConnection(conn)
	stop := context.AfterFunc(ctx, cancel)
	go func() {
		err := p.runAttempts(opCtx)
		stop()
		cancel()
		p.finishOperation(op, err)
	}()
	return nil
}

func (p *Peer) Wait(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	p.mu.Lock()
	op, state, err := p.op, p.state, p.terminalErr
	p.mu.Unlock()
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
	p.mu.Lock()
	if p.closed {
		done, err := p.closeDone, p.closeErr
		p.mu.Unlock()
		if done != nil {
			<-done
			p.mu.Lock()
			err = p.closeErr
			p.mu.Unlock()
		}
		return err
	}
	p.closed = true
	p.cancel()
	op := p.op
	if op != nil {
		op.cancel()
	}
	conn := p.conn
	p.conn, p.closeDone = nil, make(chan struct{})
	p.transitionLocked(StateClosed, nil, p.attempts)
	done := p.closeDone
	p.mu.Unlock()
	closeErr := closeConnection(conn)
	if op != nil {
		<-op.done
	}
	p.mu.Lock()
	p.closeErr = closeErr
	close(done)
	p.mu.Unlock()
	return closeErr
}

func (p *Peer) runAttempts(ctx context.Context) error {
	var last error
	for attempt := 1; attempt <= p.policy.MaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			if p.isClosed() {
				return ErrPeerClosed
			}
			return p.terminal(err, attempt-1)
		}
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			return ErrPeerClosed
		}
		p.attempts = attempt
		p.mu.Unlock()
		conn, err := p.dialContext(ctx)
		if err == nil && conn == nil {
			err = ErrNilConnection
		}
		if err == nil {
			return p.accept(conn, attempt)
		}
		closeConnection(conn)
		last = err
		if p.isClosed() {
			return ErrPeerClosed
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return p.terminal(ctxErr, attempt)
		}
		if attempt == p.policy.MaxAttempts {
			return p.terminal(last, attempt)
		}
		if err := p.waitBackoff(ctx); err != nil {
			if p.isClosed() {
				return ErrPeerClosed
			}
			return p.terminal(err, attempt)
		}
	}
	return p.terminal(last, p.policy.MaxAttempts)
}

func (p *Peer) dialContext(ctx context.Context) (Conn, error) {
	if d, ok := p.dialer.(ContextDialer); ok {
		return d.DialContext(ctx, p.endpoint, cloneHeaders(p.headers))
	}
	if p.dialer == nil {
		return nil, ErrNoDialer
	}
	result := make(chan struct {
		conn Conn
		err  error
	})
	go func() {
		select {
		case <-ctx.Done():
			return
		default:
		}
		conn, err := p.dialer.Dial(p.endpoint, cloneHeaders(p.headers))
		select {
		case result <- struct {
			conn Conn
			err  error
		}{conn, err}:
		case <-ctx.Done():
			closeConnection(conn)
		}
	}()
	select {
	case result := <-result:
		return result.conn, result.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (p *Peer) waitBackoff(ctx context.Context) error {
	delay := p.policy.Backoff
	if delay <= 0 {
		return nil
	}
	if p.policy.MaxBackoff > 0 && delay > p.policy.MaxBackoff {
		delay = p.policy.MaxBackoff
	}
	if p.policy.Wait != nil {
		return p.policy.Wait(ctx, delay)
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *Peer) accept(conn Conn, attempt int) error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		closeConnection(conn)
		return ErrPeerClosed
	}
	old := p.conn
	p.conn, p.terminalErr = conn, nil
	p.transitionLocked(StateConnected, nil, attempt)
	p.mu.Unlock()
	closeConnection(old)
	return nil
}

func (p *Peer) terminal(cause error, attempts int) error {
	if cause == nil {
		cause = ErrRetryExhausted
	}
	terminal := &TerminalError{Cause: cause, Attempts: attempts}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return ErrPeerClosed
	}
	p.terminalErr = terminal
	conn := p.conn
	p.conn = nil
	p.transitionLocked(StateTerminalFailure, terminal, attempts)
	p.mu.Unlock()
	closeConnection(conn)
	return terminal
}

func (p *Peer) finishOperation(op *operation, err error) {
	p.mu.Lock()
	if p.op == op {
		p.op = nil
	}
	op.result = err
	close(op.done)
	p.mu.Unlock()
}

func waitOperation(ctx context.Context, op *operation) error {
	select {
	case <-op.done:
		return op.result
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

func (p *Peer) isClosed() bool { p.mu.Lock(); defer p.mu.Unlock(); return p.closed }

func closeConnection(conn Conn) error {
	if conn == nil {
		return nil
	}
	return conn.Close()
}

func cloneHeaders(headers map[string]string) map[string]string {
	if headers == nil {
		return nil
	}
	result := make(map[string]string, len(headers))
	for key, value := range headers {
		result[key] = value
	}
	return result
}
