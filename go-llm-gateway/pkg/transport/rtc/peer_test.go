package rtc

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPeerS4LifecycleErrorsAndTeardown(t *testing.T) {
	t.Run("connect timeout", func(t *testing.T) {
		dialer := &scriptedDialer{plan: func(context.Context, int) (Conn, error) {
			return nil, fmt.Errorf("connect timeout: %w", context.DeadlineExceeded)
		}}
		peer := NewPeer(PeerConfig{Dialer: dialer, Retry: RetryPolicy{MaxAttempts: 1}})
		err := peer.Connect(context.Background())
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Connect error = %v, want deadline identity", err)
		}
		var terminal *TerminalError
		if !errors.As(err, &terminal) || terminal.Attempts != 1 {
			t.Fatalf("Connect error = %v, want one-attempt TerminalError", err)
		}
		if peer.State() != StateTerminalFailure || peer.Attempts() != 1 {
			t.Fatalf("status = (%s, %d), want terminal-failure/1", peer.State(), peer.Attempts())
		}
		assertTransitions(t, peer.Transitions(), StateIdle, StateConnecting, StateTerminalFailure)
	})

	t.Run("peer loss and recovery", func(t *testing.T) {
		var created atomic.Int32
		first := &countedConn{}
		second := &countedConn{}
		dialer := &scriptedDialer{plan: func(_ context.Context, attempt int) (Conn, error) {
			created.Add(1)
			if attempt == 1 {
				return first, nil
			}
			return second, nil
		}}
		peer := NewPeer(PeerConfig{Dialer: dialer, Retry: RetryPolicy{MaxAttempts: 2}})
		if err := peer.Connect(context.Background()); err != nil {
			t.Fatalf("Connect: %v", err)
		}
		cause := errors.New("remote peer disappeared")
		if err := peer.PeerLost(cause); err != nil {
			t.Fatalf("PeerLost: %v", err)
		}
		if err := peer.Wait(context.Background()); err != nil {
			t.Fatalf("Wait after PeerLost: %v", err)
		}
		assertTransitions(t, peer.Transitions(), StateIdle, StateConnecting, StateConnected, StateReconnecting, StateConnected)
		if got := dialer.Attempts(); got != 2 || created.Load() != 2 {
			t.Fatalf("attempts/created = (%d, %d), want (2, 2)", got, created.Load())
		}
		if first.closes.Load() != 1 {
			t.Fatalf("lost connection closes = %d, want 1", first.closes.Load())
		}
		if err := peer.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		if second.closes.Load() != 1 || peer.State() != StateClosed {
			t.Fatalf("remaining close/state = (%d, %s), want (1, closed)", second.closes.Load(), peer.State())
		}
	})

	t.Run("teardown during connect", func(t *testing.T) {
		started := make(chan struct{})
		var once sync.Once
		dialer := &scriptedDialer{plan: func(ctx context.Context, _ int) (Conn, error) {
			once.Do(func() { close(started) })
			<-ctx.Done()
			return nil, ctx.Err()
		}}
		peer := NewPeer(PeerConfig{Dialer: dialer, Retry: RetryPolicy{MaxAttempts: 3}})
		connectDone := make(chan error, 1)
		go func() { connectDone <- peer.Connect(context.Background()) }()
		<-started
		closeDone := make(chan error, 1)
		go func() { closeDone <- peer.Close() }()
		if err := <-closeDone; err != nil {
			t.Fatalf("Close: %v", err)
		}
		if err := <-connectDone; !errors.Is(err, ErrPeerClosed) {
			t.Fatalf("Connect after Close = %v, want ErrPeerClosed", err)
		}
		if peer.State() != StateClosed || peer.Attempts() != 1 {
			t.Fatalf("status = (%s, %d), want closed/1", peer.State(), peer.Attempts())
		}
		assertTransitions(t, peer.Transitions(), StateIdle, StateConnecting, StateClosed)
	})

	t.Run("double teardown", func(t *testing.T) {
		conn := &countedConn{}
		dialer := &scriptedDialer{plan: func(context.Context, int) (Conn, error) { return conn, nil }}
		peer := NewPeer(PeerConfig{Dialer: dialer, Retry: RetryPolicy{MaxAttempts: 1}})
		if err := peer.Connect(context.Background()); err != nil {
			t.Fatalf("Connect: %v", err)
		}
		var wg sync.WaitGroup
		for range 2 {
			wg.Add(1)
			go func() { defer wg.Done(); _ = peer.Close() }()
		}
		wg.Wait()
		if conn.closes.Load() != 1 || peer.State() != StateClosed {
			t.Fatalf("close/state = (%d, %s), want (1, closed)", conn.closes.Load(), peer.State())
		}
	})
}

func TestPeerRetryExhaustionPreservesCause(t *testing.T) {
	cause := &dialFailure{message: "peer is permanently unavailable"}
	dialer := &scriptedDialer{plan: func(context.Context, int) (Conn, error) { return nil, cause }}
	peer := NewPeer(PeerConfig{Dialer: dialer, Retry: RetryPolicy{MaxAttempts: 4, Backoff: 0}})
	err := peer.Connect(context.Background())
	if !errors.Is(err, cause) || !errors.Is(err, ErrRetryExhausted) {
		t.Fatalf("Connect error = %v, want cause and retry-exhausted identity", err)
	}
	var terminal *TerminalError
	if !errors.As(err, &terminal) || terminal.Attempts != 4 {
		t.Fatalf("Connect error = %v, want TerminalError{Attempts: 4}", err)
	}
	if got := dialer.Attempts(); got != 4 || peer.State() != StateTerminalFailure {
		t.Fatalf("attempts/state = (%d, %s), want (4, terminal-failure)", got, peer.State())
	}
	if err := peer.Wait(context.Background()); !errors.Is(err, cause) {
		t.Fatalf("Wait error = %v, want terminal cause", err)
	}
	if err := peer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestPeerS8ConcurrentConnectCloseAndReads(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var startOnce sync.Once
	conn := &countedConn{}
	dialer := &scriptedDialer{plan: func(_ context.Context, _ int) (Conn, error) {
		startOnce.Do(func() { close(started) })
		<-release
		return conn, nil
	}}
	peer := NewPeer(PeerConfig{Dialer: dialer, Retry: RetryPolicy{MaxAttempts: 1}})
	connectDone := make(chan error, 1)
	go func() { connectDone <- peer.Connect(context.Background()) }()
	<-started

	var readers sync.WaitGroup
	for range 12 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for range 100 {
				_ = peer.State()
				_ = peer.Transitions()
			}
		}()
	}
	closeDone := make(chan error, 1)
	go func() { closeDone <- peer.Close() }()
	close(release)
	readers.Wait()
	if err := <-closeDone; err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := <-connectDone; err != nil && !errors.Is(err, ErrPeerClosed) {
		t.Fatalf("Connect after concurrent Close = %v, want nil or ErrPeerClosed", err)
	}
	if peer.State() != StateClosed || conn.closes.Load() != 1 {
		t.Fatalf("final state/close count = (%s, %d), want (closed, 1)", peer.State(), conn.closes.Load())
	}
}

func TestPeerS9OneHundredConnectTeardownCycles(t *testing.T) {
	baseline := runtime.NumGoroutine()
	var created, closed atomic.Int32
	for range 100 {
		dialer := &scriptedDialer{plan: func(context.Context, int) (Conn, error) {
			created.Add(1)
			return &countedConn{total: &closed}, nil
		}}
		peer := NewPeer(PeerConfig{Dialer: dialer, Retry: RetryPolicy{MaxAttempts: 1}})
		if err := peer.Connect(context.Background()); err != nil {
			t.Fatalf("Connect: %v", err)
		}
		if err := peer.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}
	if created.Load() != 100 || closed.Load() != 100 {
		t.Fatalf("created/closed resources = (%d, %d), want (100, 100)", created.Load(), closed.Load())
	}
	waitFor(t, 500*time.Millisecond, func() bool {
		return runtime.NumGoroutine() <= baseline+2
	})
}

type scriptedDialer struct {
	plan     func(context.Context, int) (Conn, error)
	attempts atomic.Int32
}

func (d *scriptedDialer) Dial(string, map[string]string) (Conn, error) {
	return d.DialContext(context.Background(), "", nil)
}

func (d *scriptedDialer) DialContext(ctx context.Context, _ string, _ map[string]string) (Conn, error) {
	attempt := int(d.attempts.Add(1))
	return d.plan(ctx, attempt)
}

func (d *scriptedDialer) Attempts() int { return int(d.attempts.Load()) }

type countedConn struct {
	closes atomic.Int32
	total  *atomic.Int32
}

func (c *countedConn) ReadMessage() (int, []byte, error) { return 0, nil, nil }
func (c *countedConn) WriteMessage(int, []byte) error    { return nil }
func (c *countedConn) Close() error {
	c.closes.Add(1)
	if c.total != nil {
		c.total.Add(1)
	}
	return nil
}

type dialFailure struct{ message string }

func (e *dialFailure) Error() string { return e.message }

func assertTransitions(t *testing.T, got []Transition, want ...State) {
	t.Helper()
	if len(got) != len(want)-1 {
		t.Fatalf("transition count = %d, want %d: %#v", len(got), len(want)-1, got)
	}
	for i, transition := range got {
		if transition.From != want[i] || transition.To != want[i+1] {
			t.Fatalf("transition %d = %s -> %s, want %s -> %s", i, transition.From, transition.To, want[i], want[i+1])
		}
	}
}

func waitFor(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition did not settle before timeout")
		}
		runtime.Gosched()
	}
}
