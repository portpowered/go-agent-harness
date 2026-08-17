package rtc

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

func TestPeerS4Lifecycle(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		cause := errors.New("timeout")
		p := peer(func(context.Context, int) (Conn, error) { return nil, cause }, 1)
		terminal(t, p, p.Connect(context.Background()), cause, 1)
		path(t, p, StateIdle, StateConnecting, StateTerminalFailure)
	})
	t.Run("peer gone", func(t *testing.T) {
		var open atomic.Int32
		first, second := newConn(&open, nil), newConn(&open, nil)
		p := peer(func(_ context.Context, n int) (Conn, error) {
			if n == 1 {
				return first, nil
			}
			return second, nil
		}, 2)
		must(t, p.Connect(context.Background()))
		must(t, p.PeerLost(nil))
		must(t, p.Wait(context.Background()))
		path(t, p, StateIdle, StateConnecting, StateConnected, StateReconnecting, StateConnected)
		d := p.config.Dialer.(*fake)
		check(t, d.dials.Load() == 2 && p.Attempts() == 1 && first.closes.Load() == 1, "dials/attempts/first close = %d/%d/%d", d.dials.Load(), p.Attempts(), first.closes.Load())
		must(t, p.Close())
		check(t, second.closes.Load() == 1 && open.Load() == 0, "second close/open = %d/%d", second.closes.Load(), open.Load())
	})
	t.Run("connect teardown", func(t *testing.T) {
		started := make(chan struct{})
		p := peer(func(ctx context.Context, _ int) (Conn, error) { close(started); <-ctx.Done(); return nil, ctx.Err() }, 3)
		connect, closed := make(chan error, 1), make(chan error, 1)
		go func() { connect <- p.Connect(context.Background()) }()
		<-started
		go func() { closed <- p.Close() }()
		must(t, awaitErr(t, closed))
		check(t, errors.Is(awaitErr(t, connect), ErrPeerClosed), "connect was not closed")
		path(t, p, StateIdle, StateConnecting, StateClosed)
	})
	t.Run("double teardown", func(t *testing.T) {
		conn := newConn(nil, nil)
		p := peer(func(context.Context, int) (Conn, error) { return conn, nil }, 1)
		must(t, p.Connect(context.Background()))
		must(t, p.Close())
		must(t, p.Close())
		check(t, conn.closes.Load() == 1 && p.State() == StateClosed, "close/state = %d/%s", conn.closes.Load(), p.State())
	})
}

func TestPeerS4CloseCancelsPendingRetry(t *testing.T) {
	started := make(chan struct{})
	d := &fake{fn: func(_ context.Context, n int) (Conn, error) {
		if n == 1 {
			return newConn(nil, nil), nil
		}
		return nil, errors.New("unavailable")
	}}
	p := NewPeer(PeerConfig{Dialer: d, Retry: RetryPolicy{MaxAttempts: 4, Backoff: time.Hour, MaxBackoff: time.Hour, Wait: func(ctx context.Context, _ time.Duration) error { close(started); <-ctx.Done(); return ctx.Err() }}})
	must(t, p.Connect(context.Background()))
	must(t, p.PeerLost(nil))
	<-started
	must(t, p.Close())
	check(t, d.dials.Load() == 2 && p.Attempts() == 1 && p.State() == StateClosed, "dials/attempts/state = %d/%d/%s", d.dials.Load(), p.Attempts(), p.State())
}

func TestPeerRetryExhaustionPreservesCause(t *testing.T) {
	cause := errors.New("permanently unavailable")
	var delay atomic.Int64
	d := &fake{fn: func(context.Context, int) (Conn, error) { return nil, cause }}
	p := NewPeer(PeerConfig{Dialer: d, Retry: RetryPolicy{MaxAttempts: 4, Backoff: time.Hour, MaxBackoff: time.Millisecond, Wait: func(_ context.Context, d time.Duration) error { delay.Store(int64(d)); return nil }}})
	terminal(t, p, p.Connect(context.Background()), cause, 4)
	check(t, d.dials.Load() == 4 && time.Duration(delay.Load()) <= time.Millisecond, "dials/delay = %d/%s", d.dials.Load(), time.Duration(delay.Load()))
	must(t, p.Close())
}

func TestPeerPublicErrorAndWaitOutcomes(t *testing.T) {
	t.Run("idle and closed", func(t *testing.T) {
		p := NewPeer(PeerConfig{})
		must(t, p.Wait(context.Background()))
		must(t, p.Close())
		check(t, errors.Is(p.Wait(context.Background()), ErrPeerClosed), "closed wait did not preserve state")
	})
	t.Run("terminal", func(t *testing.T) {
		cause := errors.New("terminal")
		p := peer(func(context.Context, int) (Conn, error) { return nil, cause }, 1)
		err := p.Connect(context.Background())
		terminal(t, p, err, cause, 1)
		waitErr := p.Wait(context.Background())
		check(t, errors.Is(waitErr, cause), "terminal wait lost cause: %v", waitErr)
		must(t, p.Close())
	})
	t.Run("wait cancellation", func(t *testing.T) {
		started := make(chan struct{})
		p := peer(func(ctx context.Context, _ int) (Conn, error) { close(started); <-ctx.Done(); return nil, ctx.Err() }, 1)
		connected := make(chan error, 1)
		go func() { connected <- p.Connect(context.Background()) }()
		<-started
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		check(t, errors.Is(p.Wait(ctx), context.Canceled), "wait did not honor cancellation")
		must(t, p.Close())
		check(t, errors.Is(<-connected, ErrPeerClosed), "connect did not close")
	})
}

func TestPeerBackoffCancellationAndZeroDelay(t *testing.T) {
	t.Run("zero delay retries", func(t *testing.T) {
		cause := errors.New("unavailable")
		d := &fake{fn: func(context.Context, int) (Conn, error) { return nil, cause }}
		p := NewPeer(PeerConfig{Dialer: d, Retry: RetryPolicy{MaxAttempts: 2}})
		terminal(t, p, p.Connect(context.Background()), cause, 2)
		check(t, d.dials.Load() == 2, "dial count = %d", d.dials.Load())
		must(t, p.Close())
	})
	t.Run("timer cancellation", func(t *testing.T) {
		started := make(chan struct{})
		cause := errors.New("unavailable")
		d := &fake{fn: func(_ context.Context, n int) (Conn, error) {
			if n == 1 {
				close(started)
			}
			return nil, cause
		}}
		p := NewPeer(PeerConfig{Dialer: d, Retry: RetryPolicy{MaxAttempts: 2, Backoff: time.Hour, MaxBackoff: time.Hour}})
		connected := make(chan error, 1)
		go func() { connected <- p.Connect(context.Background()) }()
		<-started
		must(t, p.Close())
		check(t, errors.Is(<-connected, ErrPeerClosed), "connect did not close")
		check(t, d.dials.Load() == 1, "dial count after cancellation = %d", d.dials.Load())
	})
}

func TestPeerS8ConcurrentConnectCloseAndReads(t *testing.T) {
	started, conn := make(chan struct{}), newConn(nil, nil)
	p := peer(func(ctx context.Context, _ int) (Conn, error) { close(started); <-ctx.Done(); return conn, nil }, 1)
	a, b := make(chan error, 1), make(chan error, 1)
	go func() { a <- p.Connect(context.Background()) }()
	<-started
	go func() { b <- p.Connect(context.Background()) }()
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		for range 1200 {
			switch p.State() {
			case StateIdle, StateConnecting, StateConnected, StateReconnecting, StateTerminalFailure, StateClosed:
			default:
				t.Errorf("illegal state %q", p.State())
			}
			_ = p.Transitions()
		}
	}()
	closed := make(chan error, 1)
	go func() { closed <- p.Close() }()
	<-readDone
	must(t, awaitErr(t, closed))
	for _, ch := range []<-chan error{a, b} {
		check(t, errors.Is(awaitErr(t, ch), ErrPeerClosed), "connect did not close")
	}
	check(t, p.State() == StateClosed && conn.closes.Load() == 1, "state/close = %s/%d", p.State(), conn.closes.Load())
}

func TestPeerS9OneHundredConnectTeardownCycles(t *testing.T) {
	baseG, open := runtime.NumGoroutine(), new(atomic.Int32)
	baseOpen := open.Load()
	var created, closed atomic.Int32
	for range 100 {
		p := peer(func(context.Context, int) (Conn, error) { created.Add(1); return newConn(open, &closed), nil }, 1)
		must(t, p.Connect(context.Background()))
		must(t, p.Close())
	}
	check(t, created.Load() == 100 && closed.Load() == 100 && open.Load() == baseOpen, "created/closed/open = %d/%d/%d", created.Load(), closed.Load(), open.Load())
	settle(t, 500*time.Millisecond, func() bool { return runtime.NumGoroutine() <= baseG+2 })
}

type fake struct {
	fn            func(context.Context, int) (Conn, error)
	dials, closes atomic.Int32
	open, closed  *atomic.Int32
}

func (f *fake) DialContext(ctx context.Context, _ string, _ map[string]string) (Conn, error) {
	return f.fn(ctx, int(f.dials.Add(1)))
}
func peer(fn func(context.Context, int) (Conn, error), max int) *Peer {
	return NewPeer(PeerConfig{Dialer: &fake{fn: fn}, Retry: RetryPolicy{MaxAttempts: max}})
}
func newConn(open, closed *atomic.Int32) *fake {
	if open != nil {
		open.Add(1)
	}
	return &fake{open: open, closed: closed}
}
func (*fake) ReadMessage() (int, []byte, error) { return 0, nil, nil }
func (*fake) WriteMessage(int, []byte) error    { return nil }
func (f *fake) Close() error {
	f.closes.Add(1)
	if f.open != nil {
		f.open.Add(-1)
	}
	if f.closed != nil {
		f.closed.Add(1)
	}
	return nil
}
func must(t *testing.T, err error) { check(t, err == nil, "unexpected error: %v", err) }
func terminal(t *testing.T, p *Peer, err, cause error, attempts int) {
	var typed *TerminalError
	check(t, errors.Is(err, cause) && errors.Is(err, ErrPeerTerminalFailure) && errors.As(err, &typed) && typed.Attempts == attempts && p.State() == StateTerminalFailure, "error/state = %v/%s", err, p.State())
	check(t, errors.Is(p.Err(), cause), "peer error lost cause: %v", p.Err())
	check(t, typed.Error() == fmt.Sprintf("rtc peer terminal failure after %d attempts: %v", attempts, typed.Cause), "terminal error string = %q", typed.Error())
}
func path(t *testing.T, p *Peer, want ...State) {
	got := p.Transitions()
	check(t, len(got) == len(want)-1, "transitions = %#v", got)
	for i, tr := range got {
		check(t, tr.From == want[i] && tr.To == want[i+1], "transition %d = %s -> %s", i, tr.From, tr.To)
	}
}
func check(t *testing.T, ok bool, format string, args ...any) {
	if !ok {
		t.Fatalf(format, args...)
	}
}
func awaitErr(t *testing.T, ch <-chan error) error {
	select {
	case err := <-ch:
		return err
	case <-time.After(time.Second):
		t.Fatal("operation did not finish promptly")
		return nil
	}
}
func settle(t *testing.T, timeout time.Duration, f func() bool) {
	deadline := time.Now().Add(timeout)
	for !f() {
		if time.Now().After(deadline) {
			t.Fatal("condition did not settle")
		}
		runtime.Gosched()
	}
}
