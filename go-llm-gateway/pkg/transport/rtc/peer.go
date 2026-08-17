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
	StateConnecting      State = "connecting"
	StateConnected       State = "connected"
	StateReconnecting    State = "reconnecting"
	StateTerminalFailure State = "terminal-failure"
	StateClosed          State = "closed"
)

var (
	ErrPeerClosed          = errors.New("rtc peer is closed")
	ErrPeerNotConnected    = errors.New("rtc peer is not connected")
	ErrPeerLost            = errors.New("rtc peer connection was lost")
	ErrPeerTerminalFailure = errors.New("rtc peer reached terminal failure")
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

type (
	ContextDialer interface {
		DialContext(context.Context, string, map[string]string) (Conn, error)
	}
	RetryPolicy struct {
		MaxAttempts         int
		Backoff, MaxBackoff time.Duration
		Wait                func(context.Context, time.Duration) error
	}
	PeerConfig struct {
		Dialer   ContextDialer
		Endpoint string
		Headers  map[string]string
		Retry    RetryPolicy
	}
	Transition struct {
		From, To State
		Attempt  int
		Cause    error
	}
	operation struct {
		done   chan struct{}
		cancel context.CancelFunc
		ctx    context.Context
		old    Conn
		err    error
	}
	Peer struct {
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
)

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
func (p *Peer) State() State  { p.mu.RLock(); defer p.mu.RUnlock(); return p.state }
func (p *Peer) Err() error    { p.mu.RLock(); defer p.mu.RUnlock(); return p.terminalErr }
func (p *Peer) Attempts() int { p.mu.RLock(); defer p.mu.RUnlock(); return p.attempts }
func (p *Peer) Transitions() []Transition {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return append([]Transition(nil), p.history...)
}

func (p *Peer) Connect(ctx context.Context) error {
	ctx = contextOrBackground(ctx)
	op, owner, err := p.begin(false, nil, ctx)
	if err != nil || op == nil {
		return err
	}
	if !owner {
		return waitOperation(ctx, op)
	}
	return p.finish(op, p.run(op.ctx))
}
func (p *Peer) PeerLost(cause error) error {
	if cause == nil {
		cause = ErrPeerLost
	}
	op, owner, err := p.begin(true, cause, context.Background())
	if err != nil || !owner {
		return err
	}
	_ = closeConn(op.old)
	go func() { _ = p.finish(op, p.run(op.ctx)) }()
	return nil
}
func (p *Peer) begin(reconnect bool, cause error, parent context.Context) (*operation, bool, error) {
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
	if reconnect && p.state != StateConnected {
		return nil, false, ErrPeerNotConnected
	}
	if !reconnect && p.state == StateConnected {
		return nil, false, nil
	}
	old, next := Conn(nil), StateConnecting
	if reconnect {
		old, p.conn, next = p.conn, nil, StateReconnecting
	}
	p.transitionLocked(next, cause, 0)
	p.attempts, p.terminalErr = 0, nil
	ctx, cancel := context.WithCancel(contextOrBackground(parent))
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
	switch state {
	case StateTerminalFailure:
		return err
	case StateClosed:
		return ErrPeerClosed
	default:
		return nil
	}
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
	max := p.config.Retry.MaxAttempts
	last := error(ErrRetryExhausted)
	for attempt := 1; attempt <= max; attempt++ {
		if err := ctx.Err(); err != nil {
			return p.terminal(err, attempt-1, false)
		}
		p.mu.Lock()
		p.attempts = attempt
		p.mu.Unlock()
		var conn Conn
		var err error
		if p.config.Dialer == nil {
			err = ErrNoDialer
		} else {
			conn, err = p.config.Dialer.DialContext(ctx, p.config.Endpoint, maps.Clone(p.config.Headers))
		}
		if err == nil && ctx.Err() == nil && conn != nil {
			return p.accept(conn, attempt)
		}
		if err == nil {
			err = ctx.Err()
			if err == nil {
				err = ErrNilConnection
			}
		}
		_ = closeConn(conn)
		last = err
		if err = ctx.Err(); err != nil {
			return p.terminal(err, attempt, false)
		}
		if attempt == max {
			return p.terminal(last, attempt, true)
		}
		if err = p.backoff(ctx); err != nil {
			return p.terminal(err, attempt, false)
		}
	}
	return p.terminal(last, max, true)
}
func (p *Peer) backoff(ctx context.Context) error {
	delay := p.config.Retry.Backoff
	if delay > p.config.Retry.MaxBackoff {
		delay = p.config.Retry.MaxBackoff
	}
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
	ctx = contextOrBackground(ctx)
	select {
	case <-op.done:
		return op.err
	case <-ctx.Done():
		return ctx.Err()
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
	p.history = append(p.history, Transition{From: p.state, To: to, Attempt: attempt, Cause: cause})
	p.state = to
}
func closeConn(conn Conn) error {
	if conn == nil {
		return nil
	}
	return conn.Close()
}
