package mediagate

import (
	"context"
	"errors"
	"testing"
	"time"

	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

// blockingInbound models a provider reader that only unblocks when its
// bridge context is cancelled. It deliberately does not use the caller's
// context so the test can distinguish invocation cancellation from explicit
// media teardown.
type blockingInbound struct {
	started chan struct{}
	exited  chan struct{}
}

func newBlockingInbound() *blockingInbound {
	return &blockingInbound{started: make(chan struct{}), exited: make(chan struct{})}
}

func (b *blockingInbound) ReadFrame(ctx context.Context) (sharedaudio.PCMFrame, error) {
	select {
	case <-b.started:
	default:
		close(b.started)
	}
	<-ctx.Done()
	select {
	case <-b.exited:
	default:
		close(b.exited)
	}
	return sharedaudio.PCMFrame{}, ctx.Err()
}

func (b *blockingInbound) Close() error { return nil }

func TestCloseCancelsBridgeAfterInvocationContextCancellation(t *testing.T) {
	invocationCtx, cancelInvocation := context.WithCancel(context.Background())
	defer cancelInvocation()

	provider := newBlockingInbound()
	gate := New(nil)
	gate.Attach(invocationCtx, sharedaudio.MediaEndpoints{Inbound: provider})

	select {
	case <-provider.started:
	case <-time.After(time.Second):
		t.Fatal("media bridge did not start")
	}
	cancelInvocation()

	// Attach intentionally owns a context independent of a graceful run
	// cancellation so provider output can drain. The bridge must therefore
	// remain alive until the gate's explicit Close boundary.
	drainCtx, cancelDrain := context.WithTimeout(context.Background(), 25*time.Millisecond)
	err := gate.DrainInbound(drainCtx)
	cancelDrain()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("DrainInbound() after invocation cancellation = %v, want deadline", err)
	}
	select {
	case <-provider.exited:
		t.Fatal("media bridge exited with invocation context")
	default:
	}

	closed := make(chan struct{})
	closeErrs := make(chan error, 1)
	go func() {
		closeErrs <- gate.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Gate.Close() did not join the media bridge")
	}
	if err := <-closeErrs; err != nil {
		t.Fatalf("Gate.Close() = %v, want nil", err)
	}
	select {
	case <-provider.exited:
	case <-time.After(time.Second):
		t.Fatal("media bridge did not observe explicit close")
	}

	if err := gate.Close(); err != nil {
		t.Fatalf("second Gate.Close() = %v, want nil", err)
	}
}

type closeErrorOutbound struct{ err error }

func (*closeErrorOutbound) WriteFrame(context.Context, sharedaudio.PCMFrame) error { return nil }

func (o *closeErrorOutbound) Close() error { return o.err }

func TestClosePreservesEndpointCloseErrorAcrossCalls(t *testing.T) {
	want := errors.New("speaker close failed")
	gate := New(nil)
	gate.Attach(context.Background(), sharedaudio.MediaEndpoints{Outbound: &closeErrorOutbound{err: want}})

	if err := gate.Close(); !errors.Is(err, want) {
		t.Fatalf("first Gate.Close() = %v, want %v", err, want)
	}
	if err := gate.Close(); !errors.Is(err, want) {
		t.Fatalf("second Gate.Close() = %v, want %v", err, want)
	}
}
