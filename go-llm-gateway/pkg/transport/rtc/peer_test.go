package rtc

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPeerS4Lifecycle(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(*testing.T)
	}{
		{"connect timeout", s4Timeout}, {"peer gone mid-session", s4Loss},
		{"teardown during connect", s4ConnectClose}, {"double teardown", s4DoubleClose},
	} {
		t.Run(tc.name, tc.run)
	}
}
func s4Timeout(t *testing.T) {
	cause := &dialFailure{"timeout"}
	p := peer(func(context.Context, int) (Conn, error) { return nil, cause }, 1)
	err := p.Connect(context.Background())
	terminal(t, p, err, cause, 1)
	path(t, p, StateIdle, StateConnecting, StateTerminalFailure)
}
func s4Loss(t *testing.T) {
	var open atomic.Int32
	first, second := newConn(&open), newConn(&open)
	d := &scriptDialer{fn: func(_ context.Context, n int) (Conn, error) {
		if n == 1 {
			return first, nil
		}
		return second, nil
	}}
	p := NewPeer(PeerConfig{Dialer: d, Retry: RetryPolicy{MaxAttempts: 2}})
	must(t, p.Connect(nil))
	must(t, p.PeerLost(nil))
	must(t, p.Wait(nil))
	path(t, p, StateIdle, StateConnecting, StateConnected, StateReconnecting, StateConnected)
	if d.calls() != 2 || p.Attempts() != 1 || first.calls.Load() != 1 {
		t.Fatalf("dials/attempts/first close = %d/%d/%d, want 2/1/1", d.calls(), p.Attempts(), first.calls.Load())
	}
	must(t, p.Close())
	if second.calls.Load() != 1 || open.Load() != 0 {
		t.Fatalf("second close/open = %d/%d, want 1/0", second.calls.Load(), open.Load())
	}
}
func s4ConnectClose(t *testing.T) {
	started := make(chan struct{})
	var once sync.Once
	p := peer(func(ctx context.Context, _ int) (Conn, error) {
		once.Do(func() { close(started) })
		<-ctx.Done()
		return nil, ctx.Err()
	}, 3)
	connected := make(chan error, 1)
	go func() { connected <- p.Connect(nil) }()
	<-started
	closed := make(chan error, 1)
	go func() { closed <- p.Close() }()
	must(t, await(t, closed))
	if err := await(t, connected); !errors.Is(err, ErrPeerClosed) {
		t.Fatalf("connect = %v, want ErrPeerClosed", err)
	}
	path(t, p, StateIdle, StateConnecting, StateClosed)
}
func s4DoubleClose(t *testing.T) {
	conn := newConn(nil)
	p := peer(func(context.Context, int) (Conn, error) { return conn, nil }, 1)
	must(t, p.Connect(nil))
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- p.Close() }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		must(t, err)
	}
	if conn.calls.Load() != 1 || p.State() != StateClosed {
		t.Fatalf("close/state = %d/%s, want 1/closed", conn.calls.Load(), p.State())
	}
}

func TestPeerS4CloseCancelsPendingRetry(t *testing.T) {
	started := make(chan struct{})
	var once sync.Once
	d := &scriptDialer{fn: func(_ context.Context, n int) (Conn, error) {
		if n == 1 {
			return newConn(nil), nil
		}
		return nil, errors.New("unavailable")
	}}
	p := NewPeer(PeerConfig{Dialer: d, Retry: RetryPolicy{MaxAttempts: 4, Backoff: time.Hour, MaxBackoff: time.Hour, Wait: func(ctx context.Context, _ time.Duration) error {
		once.Do(func() { close(started) })
		<-ctx.Done()
		return ctx.Err()
	}}})
	must(t, p.Connect(nil))
	must(t, p.PeerLost(nil))
	<-started
	must(t, p.Close())
	if d.calls() != 2 || p.Attempts() != 1 || p.State() != StateClosed {
		t.Fatalf("dials/attempts/state = %d/%d/%s, want 2/1/closed", d.calls(), p.Attempts(), p.State())
	}
}

func TestPeerRetryExhaustionPreservesCause(t *testing.T) {
	cause := &dialFailure{"permanently unavailable"}
	var delay atomic.Int64
	p := NewPeer(PeerConfig{Dialer: &scriptDialer{fn: func(context.Context, int) (Conn, error) { return nil, cause }}, Retry: RetryPolicy{MaxAttempts: 4, Backoff: time.Hour, MaxBackoff: time.Millisecond, Wait: func(_ context.Context, d time.Duration) error { delay.Store(int64(d)); return nil }}})
	err := p.Connect(nil)
	terminal(t, p, err, cause, 4)
	if !errors.Is(err, ErrRetryExhausted) || p.Attempts() != 4 || time.Duration(delay.Load()) > time.Millisecond {
		t.Fatalf("error/attempts/delay = %v/%d/%s", err, p.Attempts(), time.Duration(delay.Load()))
	}
	must(t, p.Close())
}

func TestPeerS8ConcurrentConnectCloseAndReads(t *testing.T) {
	started := make(chan struct{})
	var once sync.Once
	conn := newConn(nil)
	p := peer(func(ctx context.Context, _ int) (Conn, error) {
		once.Do(func() { close(started) })
		<-ctx.Done()
		return conn, nil
	}, 1)
	connected := make(chan error, 1)
	go func() { connected <- p.Connect(nil) }()
	<-started
	var readers sync.WaitGroup
	for range 12 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for range 100 {
				_ = p.State()
				_ = p.Transitions()
			}
		}()
	}
	closed := make(chan error, 1)
	go func() { closed <- p.Close() }()
	readers.Wait()
	must(t, await(t, closed))
	if err := await(t, connected); !errors.Is(err, ErrPeerClosed) {
		t.Fatalf("connect = %v, want ErrPeerClosed", err)
	}
	if p.State() != StateClosed || conn.calls.Load() != 1 {
		t.Fatalf("state/close = %s/%d, want closed/1", p.State(), conn.calls.Load())
	}
}

func TestPeerS9OneHundredConnectTeardownCycles(t *testing.T) {
	baseG, openSockets := runtime.NumGoroutine(), new(atomic.Int32)
	baseSockets := openSockets.Load()
	var created, closed atomic.Int32
	for range 100 {
		p := peer(func(context.Context, int) (Conn, error) { created.Add(1); return tracked(openSockets, &closed), nil }, 1)
		must(t, p.Connect(nil))
		must(t, p.Close())
	}
	if created.Load() != 100 || closed.Load() != 100 || openSockets.Load() != baseSockets {
		t.Fatalf("created/closed/open = %d/%d/%d, want 100/100/%d", created.Load(), closed.Load(), openSockets.Load(), baseSockets)
	}
	settle(t, 500*time.Millisecond, func() bool { return runtime.NumGoroutine() <= baseG+2 })
}

type scriptDialer struct {
	fn func(context.Context, int) (Conn, error)
	n  atomic.Int32
}

func (d *scriptDialer) DialContext(ctx context.Context, _ string, _ map[string]string) (Conn, error) {
	return d.fn(ctx, int(d.n.Add(1)))
}
func (d *scriptDialer) calls() int { return int(d.n.Load()) }
func peer(fn func(context.Context, int) (Conn, error), max int) *Peer {
	return NewPeer(PeerConfig{Dialer: &scriptDialer{fn: fn}, Retry: RetryPolicy{MaxAttempts: max}})
}

type countedConn struct {
	calls        atomic.Int32
	open, closed *atomic.Int32
	once         sync.Once
}

func newConn(open *atomic.Int32) *countedConn {
	if open != nil {
		open.Add(1)
	}
	return &countedConn{open: open}
}
func tracked(open, closed *atomic.Int32) *countedConn {
	c := newConn(open)
	c.closed = closed
	return c
}
func (*countedConn) ReadMessage() (int, []byte, error) { return 0, nil, nil }
func (*countedConn) WriteMessage(int, []byte) error    { return nil }
func (c *countedConn) Close() error {
	c.calls.Add(1)
	c.once.Do(func() {
		if c.open != nil {
			c.open.Add(-1)
		}
		if c.closed != nil {
			c.closed.Add(1)
		}
	})
	return nil
}

type dialFailure struct{ message string }

func (e *dialFailure) Error() string { return e.message }
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func terminal(t *testing.T, p *Peer, err, cause error, attempts int) {
	t.Helper()
	var e *TerminalError
	if !errors.Is(err, cause) || !errors.As(err, &e) || e.Attempts != attempts || p.State() != StateTerminalFailure {
		t.Fatalf("error/state = %v/%s", err, p.State())
	}
}
func path(t *testing.T, p *Peer, want ...State) {
	t.Helper()
	got := p.Transitions()
	if len(got) != len(want)-1 {
		t.Fatalf("transitions = %#v", got)
	}
	for i, tr := range got {
		if tr.From != want[i] || tr.To != want[i+1] {
			t.Fatalf("transition %d = %s -> %s", i, tr.From, tr.To)
		}
	}
}
func await(t *testing.T, ch <-chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(time.Second):
		t.Fatal("operation did not finish promptly")
		return nil
	}
}
func settle(t *testing.T, timeout time.Duration, f func() bool) {
	t.Helper()
	end := time.Now().Add(timeout)
	for !f() {
		if time.Now().After(end) {
			t.Fatal("condition did not settle")
		}
		runtime.Gosched()
	}
}
