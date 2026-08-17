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
	if c.Retry.MaxAttempts <= 0 {
		c.Retry.MaxAttempts = 3
	}
	if c.Retry.MaxBackoff <= 0 {
		c.Retry.MaxBackoff = time.Second
	}
	c.Headers = maps.Clone(c.Headers)
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
	op, owner, err := p.start(false, nil, contextOrBackground(ctx))
	if err != nil {
		return err
	}
	if !owner {
		if op == nil {
			return nil
		}
		return waitOperation(ctx, op)
	}
	return p.finish(op, p.run(op.ctx))
}
func (p *Peer) PeerLost(cause error) error {
	if cause == nil {
		cause = ErrPeerLost
	}
	op, owner, err := p.start(true, cause, context.Background())
	if err != nil || !owner {
		return err
	}
	_ = closeConn(op.old)
	go func() { _ = p.finish(op, p.run(op.ctx)) }()
	return nil
}
func (p *Peer) start(reconnect bool, cause error, parent context.Context) (*operation, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch {
	case p.state == StateClosed:
		return nil, false, ErrPeerClosed
	case p.state == StateTerminalFailure:
		return nil, false, p.terminalErr
	case p.op != nil:
		if reconnect && p.state != StateReconnecting {
			return nil, false, ErrPeerNotConnected
		}
		return p.op, false, nil
	case reconnect && p.state != StateConnected:
		return nil, false, ErrPeerNotConnected
	case !reconnect && p.state == StateConnected:
		return nil, false, nil
	}
	old := p.conn
	if reconnect {
		p.conn = nil
		p.transitionLocked(StateReconnecting, cause, 0)
	} else {
		old = nil
		p.transitionLocked(StateConnecting, nil, 0)
	}
	p.attempts, p.terminalErr = 0, nil
	ctx, cancel := context.WithCancel(parent)
	p.op = &operation{done: make(chan struct{}), cancel: cancel, ctx: ctx, old: old}
	return p.op, true, nil
}
func (p *Peer) Wait(ctx context.Context) error {
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
		op, conn := p.op, p.conn
		if op != nil {
			op.cancel()
		}
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
	last := error(ErrRetryExhausted)
	for attempt := 1; attempt <= p.config.Retry.MaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return p.terminal(err, attempt-1, false)
		}
		p.mu.Lock()
		p.attempts = attempt
		p.mu.Unlock()
		conn, err := p.dial(ctx)
		if err == nil {
			if err = ctx.Err(); err == nil && conn == nil {
				err = ErrNilConnection
			}
			if err == nil {
				return p.accept(conn, attempt)
			}
		}
		_ = closeConn(conn)
		last = err
		if err = ctx.Err(); err != nil {
			return p.terminal(err, attempt, false)
		}
		if attempt == p.config.Retry.MaxAttempts {
			return p.terminal(last, attempt, true)
		}
		if err = p.backoff(ctx); err != nil {
			return p.terminal(err, attempt, false)
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
	delay := minDuration(p.config.Retry.Backoff, p.config.Retry.MaxBackoff)
	if delay <= 0 {
		return nil
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
func minDuration(a, b time.Duration) time.Duration {
	if a > b {
		return b
	}
	return a
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
func (p *Peer) terminal(cause error, attempts int, exhausted bool) error {
	if cause == nil {
		cause = ErrRetryExhausted
	}
	if exhausted {
		cause = errors.Join(ErrRetryExhausted, cause)
	}
	err := &TerminalError{Cause: cause, Attempts: attempts}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.state == StateClosed {
		return ErrPeerClosed
	}
	p.terminalErr, p.conn = err, nil
	p.transitionLocked(StateTerminalFailure, err, attempts)
	return err
}
func (p *Peer) finish(op *operation, err error) error {
	op.cancel()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.state == StateClosed {
		err = ErrPeerClosed
	}
	if p.op == op {
		p.op = nil
	}
	op.err = err
	close(op.done)
	return err
}
func waitOperation(ctx context.Context, op *operation) error {
	select {
	case <-op.done:
		return op.err
	case <-contextOrBackground(ctx).Done():
		return contextOrBackground(ctx).Err()
	}
}
func contextOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
func (p *Peer) transitionLocked(to State, cause error, attempt int) {
	if p.state == to {
		return
	}
	p.history = append(p.history, Transition{From: p.state, To: to, Attempt: attempt, Cause: cause, Error: cause})
	p.state = to
}
func closeConn(conn Conn) error {
	if conn == nil {
		return nil
	}
	return conn.Close()
}
