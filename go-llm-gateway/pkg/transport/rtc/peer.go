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
func (e *TerminalError) Unwrap() error        { return e.Cause }
func (e *TerminalError) Is(target error) bool { return target == ErrPeerTerminalFailure }

// ContextDialer is required because Close must cancel and join an in-flight dial.
type ContextDialer interface {
	DialContext(context.Context, string, map[string]string) (Conn, error)
}

type RetryPolicy struct {
	MaxAttempts         int
	Backoff, MaxBackoff time.Duration
	Wait                func(context.Context, time.Duration) error
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
	ctx    context.Context
	old    Conn
	err    error
}
type Peer struct {
	config      PeerConfig
	mu          sync.RWMutex
	state       State
	attempts    int
	terminalErr error
	conn        Conn
	history     []Transition
	op          *operation
	once        sync.Once
	closeErr    error
}

func NewPeer(c PeerConfig) *Peer {
	r := c.Retry
	if r.MaxAttempts <= 0 {
		r.MaxAttempts = 3
	}
	if r.MaxBackoff <= 0 {
		r.MaxBackoff = time.Second
	}
	c.Headers = maps.Clone(c.Headers)
	c.Retry = r
	return &Peer{config: c, state: StateIdle}
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
	op, run, err := p.begin(false, nil)
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
	err = p.run(op.ctx)
	stop()
	p.finish(op, err)
	return err
}

func (p *Peer) PeerLost(cause error) error {
	if cause == nil {
		cause = ErrPeerLost
	}
	op, run, err := p.begin(true, cause)
	if err != nil {
		return err
	}
	if !run {
		return nil
	}
	_ = closeConn(op.old)
	go func() { p.finish(op, p.run(op.ctx)) }()
	return nil
}

func (p *Peer) begin(reconnect bool, cause error) (*operation, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.state == StateClosed {
		return nil, false, ErrPeerClosed
	}
	if p.state == StateTerminalFailure {
		return nil, false, p.terminalErr
	}
	if p.op != nil {
		if reconnect && p.state != StateReconnecting {
			return nil, false, ErrPeerNotConnected
		}
		return p.op, false, nil
	}
	if reconnect {
		if p.state != StateConnected {
			return nil, false, ErrPeerNotConnected
		}
		old := p.conn
		p.conn, p.attempts = nil, 0
		p.transitionLocked(StateReconnecting, cause, 0)
		op := p.newOperationLocked()
		op.old = old
		return op, true, nil
	}
	if p.state == StateConnected {
		return nil, false, nil
	}
	p.attempts, p.terminalErr = 0, nil
	p.transitionLocked(StateConnecting, nil, 0)
	return p.newOperationLocked(), true, nil
}

func (p *Peer) newOperationLocked() *operation {
	ctx, cancel := context.WithCancel(context.Background())
	op := &operation{done: make(chan struct{}), cancel: cancel, ctx: ctx}
	p.op = op
	return op
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
	p.once.Do(func() {
		p.mu.Lock()
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
	for attempt := 1; attempt <= p.config.Retry.MaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return p.cancelled(err, attempt-1)
		}
		p.mu.Lock()
		if p.state == StateClosed {
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
		_ = closeConn(conn)
		last = err
		if p.isClosed() {
			return ErrPeerClosed
		}
		if err = ctx.Err(); err != nil {
			return p.cancelled(err, attempt)
		}
		if attempt == p.config.Retry.MaxAttempts {
			return p.terminal(last, attempt, true)
		}
		if err = p.backoff(ctx); err != nil {
			if p.isClosed() {
				return ErrPeerClosed
			}
			return p.cancelled(err, attempt)
		}
	}
	return p.terminal(last, p.config.Retry.MaxAttempts, true)
}
func (p *Peer) dial(ctx context.Context) (Conn, error) {
	if p.config.Dialer == nil {
		return nil, ErrNoDialer
	}
	return p.config.Dialer.DialContext(ctx, p.config.Endpoint, maps.Clone(p.config.Headers))
}
func (p *Peer) backoff(ctx context.Context) error {
	delay := p.config.Retry.Backoff
	if delay <= 0 {
		return nil
	}
	if delay > p.config.Retry.MaxBackoff {
		delay = p.config.Retry.MaxBackoff
	}
	if p.config.Retry.Wait != nil {
		return p.config.Retry.Wait(ctx, delay)
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
	if p.state == StateClosed {
		p.mu.Unlock()
		_ = closeConn(conn)
		return ErrPeerClosed
	}
	old := p.conn
	p.conn, p.terminalErr = conn, nil
	p.transitionLocked(StateConnected, nil, attempt)
	p.mu.Unlock()
	_ = closeConn(old)
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
	if p.state == StateClosed {
		p.mu.Unlock()
		return ErrPeerClosed
	}
	p.terminalErr = err
	conn := p.conn
	p.conn = nil
	p.transitionLocked(StateTerminalFailure, err, attempts)
	p.mu.Unlock()
	_ = closeConn(conn)
	return err
}
func (p *Peer) finish(op *operation, err error) {
	op.cancel()
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
func (p *Peer) isClosed() bool { p.mu.RLock(); defer p.mu.RUnlock(); return p.state == StateClosed }
func closeConn(conn Conn) error {
	if conn == nil {
		return nil
	}
	return conn.Close()
}
