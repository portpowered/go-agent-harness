package clitest

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"testing"
	"testing/synctest"
	"time"
)

func TestStreamConnDrainsBufferedBytesBeforeEOF(t *testing.T) {
	client, server := newStreamConnPair()
	if _, err := client.Write([]byte("hello")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	data, err := io.ReadAll(server)
	if err != nil || string(data) != "hello" {
		t.Fatalf("read after peer close = %q, %v; want buffered bytes then EOF", data, err)
	}
}

// A write racing the peer's close is accepted, as a kernel accepts it before
// a reset, instead of failing the way net.Pipe does.
func TestStreamConnDropsWritesAfterPeerClose(t *testing.T) {
	client, server := newStreamConnPair()
	if err := server.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if n, err := client.Write([]byte("late")); n != 4 || err != nil {
		t.Fatalf("write after peer close = %d, %v; want accepted", n, err)
	}
	if _, err := client.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("read after peer close = %v, want EOF", err)
	}
}

func TestStreamConnRejectsUseAfterLocalClose(t *testing.T) {
	client, _ := newStreamConnPair()
	if err := client.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := client.Read(make([]byte, 1)); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("read after close = %v, want net.ErrClosed", err)
	}
	if _, err := client.Write([]byte("x")); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("write after close = %v, want net.ErrClosed", err)
	}
}

func TestStreamConnDeadlinesUseTheVirtualClock(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client, server := newStreamConnPair()
		if err := server.SetReadDeadline(time.Now().Add(time.Hour)); err != nil {
			t.Fatalf("set read deadline: %v", err)
		}
		start := time.Now()
		if _, err := server.Read(make([]byte, 1)); !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatalf("read past deadline = %v, want os.ErrDeadlineExceeded", err)
		}
		if waited := time.Since(start); waited != time.Hour {
			t.Fatalf("read waited %s, want the one-hour virtual deadline", waited)
		}
		if err := client.SetDeadline(time.Now()); err != nil {
			t.Fatalf("set deadline: %v", err)
		}
		if _, err := client.Write([]byte("x")); !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatalf("write past deadline = %v, want os.ErrDeadlineExceeded", err)
		}
		if err := client.SetWriteDeadline(time.Time{}); err != nil {
			t.Fatalf("clear write deadline: %v", err)
		}
		if _, err := client.Write([]byte("x")); err != nil {
			t.Fatalf("write without deadline: %v", err)
		}
	})
}

func TestPipeListenerStopsAcceptingAndDialingWhenClosed(t *testing.T) {
	listener := NewPipeListener()
	if listener.Addr().Network() != pipeNetwork || listener.Addr().String() != pipeNetwork {
		t.Fatalf("listener address = %v", listener.Addr())
	}
	if err := listener.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	if _, err := listener.Accept(); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("accept after close = %v, want net.ErrClosed", err)
	}
	if _, err := listener.DialContext(context.Background(), "tcp", "provider:443"); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("dial after close = %v, want net.ErrClosed", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewPipeListener().DialContext(ctx, "tcp", "provider:443"); !errors.Is(err, context.Canceled) {
		t.Fatalf("dial with cancelled context = %v, want context.Canceled", err)
	}
}
