package mediagate

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"

	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

// testError is an immutable sentinel for fake transport failures.
type testError string

func (e testError) Error() string { return string(e) }

const errTransport testError = "transport failed"

func pcm(sample int16) sharedaudio.PCMFrame {
	return sharedaudio.PCMFrame{Samples: []int16{sample}}
}

// gatedOutbound is a provider writer that blocks every frame until the test
// releases it, modelling a slow transport that applies backpressure.
type gatedOutbound struct {
	release chan struct{}
	mu      sync.Mutex
	written []int16
	closed  int
	err     error
}

func newGatedOutbound() *gatedOutbound { return &gatedOutbound{release: make(chan struct{})} }

func (o *gatedOutbound) WriteFrame(ctx context.Context, frame sharedaudio.PCMFrame) error {
	select {
	case <-o.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.err != nil {
		return o.err
	}
	o.written = append(o.written, frame.Samples...)
	return nil
}

func (o *gatedOutbound) Close() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.closed++
	return nil
}

func (o *gatedOutbound) snapshot() ([]int16, int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]int16(nil), o.written...), o.closed
}

type errorRecorder struct {
	mu   sync.Mutex
	errs []error
}

func (r *errorRecorder) record(err error) {
	r.mu.Lock()
	r.errs = append(r.errs, err)
	r.mu.Unlock()
}

func (r *errorRecorder) snapshot() []error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]error(nil), r.errs...)
}

func TestOutboundWriteBlocksOnFullQueueAndResumesWhenDrained(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		port := newOutboundPort(2)
		for sample := range int16(2) {
			if err := port.WriteFrame(t.Context(), pcm(sample)); err != nil {
				t.Fatalf("WriteFrame(%d) = %v", sample, err)
			}
		}
		result := make(chan error, 1)
		go func() { result <- port.WriteFrame(t.Context(), pcm(2)) }()
		synctest.Wait()
		select {
		case err := <-result:
			t.Fatalf("WriteFrame on a full queue returned %v; want backpressure", err)
		default:
		}

		for want := range int16(3) {
			frame, _, err := port.next()
			if err != nil || frame.Samples[0] != want {
				t.Fatalf("next() = %v, %v; want sample %d", frame.Samples, err, want)
			}
			port.complete()
		}
		if err := <-result; err != nil {
			t.Fatalf("blocked WriteFrame = %v, want nil after drain", err)
		}
		if err := port.waitDrained(t.Context()); err != nil {
			t.Fatalf("waitDrained = %v", err)
		}
	})
}

func TestOutboundBlockedWriterObservesCloseFailureAndCancel(t *testing.T) {
	cases := []struct {
		name string
		stop func(*outboundPort, context.CancelFunc)
		want error
	}{
		{"close", func(p *outboundPort, _ context.CancelFunc) { p.close() }, sharedaudio.ErrSessionMediaClosed},
		{"failure", func(p *outboundPort, _ context.CancelFunc) { p.fail(errTransport) }, errTransport},
		{"cancel", func(_ *outboundPort, cancel context.CancelFunc) { cancel() }, context.Canceled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				port := newOutboundPort(1)
				if err := port.WriteFrame(t.Context(), pcm(1)); err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				result := make(chan error, 1)
				go func() { result <- port.WriteFrame(ctx, pcm(2)) }()
				synctest.Wait()
				tc.stop(port, cancel)
				if err := <-result; !errors.Is(err, tc.want) {
					t.Fatalf("blocked WriteFrame = %v, want %v", err, tc.want)
				}
				// The failed admission released its reservation; only the
				// queued frame remains pending.
				port.pendingMu.Lock()
				pending := port.pending
				port.pendingMu.Unlock()
				if pending != 1 {
					t.Fatalf("pending = %d, want 1", pending)
				}
			})
		})
	}
}

func TestOutboundWaitDrainedReturnsWhenIdle(t *testing.T) {
	if err := newOutboundPort(1).waitDrained(t.Context()); err != nil {
		t.Fatalf("waitDrained = %v", err)
	}
}

func TestOutboundWaitDrainedWaitsForCompletion(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		port := newOutboundPort(1)
		if err := port.WriteFrame(t.Context(), pcm(1)); err != nil {
			t.Fatal(err)
		}
		result := make(chan error, 1)
		go func() { result <- port.waitDrained(t.Context()) }()
		synctest.Wait()
		select {
		case err := <-result:
			t.Fatalf("waitDrained returned %v before the frame completed", err)
		default:
		}
		if _, _, err := port.next(); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		select {
		case err := <-result:
			t.Fatalf("waitDrained returned %v after dequeue but before completion", err)
		default:
		}
		port.complete()
		if err := <-result; err != nil {
			t.Fatalf("waitDrained = %v", err)
		}
	})
}

func TestOutboundWaitDrainedObservesCloseAndCancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		port := newOutboundPort(1)
		if err := port.WriteFrame(t.Context(), pcm(1)); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if err := port.waitDrained(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("waitDrained(canceled) = %v", err)
		}
		port.fail(errTransport)
		if err := port.waitDrained(t.Context()); !errors.Is(err, errTransport) {
			t.Fatalf("waitDrained(failed) = %v", err)
		}
		<-port.frames // the bridge dequeues the admitted frame
		port.complete()
		// With nothing queued, the bridge observes the failure.
		if _, _, err := port.next(); !errors.Is(err, errTransport) {
			t.Fatalf("next after fail = %v", err)
		}
	})
}

func TestOutboundWriteFrameValidation(t *testing.T) {
	var nilPort *outboundPort
	if err := nilPort.WriteFrame(t.Context(), pcm(1)); !errors.Is(err, ErrMediaUnavailable) {
		t.Fatalf("nil WriteFrame = %v", err)
	}
	if err := nilPort.waitForSpace(t.Context()); !errors.Is(err, ErrMediaUnavailable) {
		t.Fatalf("nil waitForSpace = %v", err)
	}
	if err := nilPort.Close(); err != nil {
		t.Fatalf("nil Close = %v", err)
	}
	nilPort.notifySpace()
	nilPort.fail(errTransport)

	port := newOutboundPort(1)
	//nolint:staticcheck // the nil context is the validated input.
	if err := port.WriteFrame(nil, pcm(1)); err == nil {
		t.Fatal("WriteFrame(nil ctx) = nil")
	}
	//nolint:staticcheck // the nil context is the validated input.
	if err := port.waitForSpace(nil); err == nil {
		t.Fatal("waitForSpace(nil ctx) = nil")
	}
	if err := port.WriteFrame(t.Context(), sharedaudio.PCMFrame{}); !errors.Is(err, sharedaudio.ErrSessionMediaEmptyFrame) {
		t.Fatalf("empty WriteFrame = %v", err)
	}
	huge := sharedaudio.PCMFrame{Samples: make([]int16, maxMediaFrameSamples+1)}
	if err := port.WriteFrame(t.Context(), huge); !errors.Is(err, ErrMediaQueueFull) {
		t.Fatalf("oversized WriteFrame = %v", err)
	}
	if err := port.Close(); err != nil {
		t.Fatal(err)
	}
	if err := port.WriteFrame(t.Context(), pcm(1)); !errors.Is(err, sharedaudio.ErrSessionMediaClosed) {
		t.Fatalf("WriteFrame after Close = %v", err)
	}
	if _, _, err := port.next(); !errors.Is(err, sharedaudio.ErrSessionMediaClosed) {
		t.Fatalf("next after Close = %v", err)
	}
}

func TestInboundPushBlocksOnFullQueueWithoutLosingFrames(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		port := newInboundPort(1)
		if err := port.push(t.Context(), pcm(1), nil); err != nil {
			t.Fatal(err)
		}
		result := make(chan error, 1)
		go func() { result <- port.push(t.Context(), pcm(2), nil) }()
		synctest.Wait()
		select {
		case err := <-result:
			t.Fatalf("push on full queue returned %v; want backpressure", err)
		default:
		}
		for want := int16(1); want <= 2; want++ {
			frame, err := port.readQueuedFrame(t.Context())
			if err != nil || frame.Samples[0] != want {
				t.Fatalf("read = %v, %v; want %d", frame.Samples, err, want)
			}
		}
		if err := <-result; err != nil {
			t.Fatalf("blocked push = %v", err)
		}
	})
}

func TestInboundBlockedPushObservesCloseAndCancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		port := newInboundPort(1)
		if err := port.push(t.Context(), pcm(1), nil); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		result := make(chan error, 1)
		go func() { result <- port.push(ctx, pcm(2), nil) }()
		synctest.Wait()
		cancel()
		if err := <-result; !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled push = %v", err)
		}
		go func() { result <- port.push(t.Context(), pcm(3), nil) }()
		synctest.Wait()
		port.fail(errTransport)
		if err := <-result; !errors.Is(err, errTransport) {
			t.Fatalf("push after failure = %v", err)
		}
		// The admitted frame survives teardown, then the failure surfaces.
		if frame, err := port.readQueuedFrame(t.Context()); err != nil || frame.Samples[0] != 1 {
			t.Fatalf("tail read = %v, %v", frame.Samples, err)
		}
		if _, err := port.readQueuedFrame(t.Context()); !errors.Is(err, errTransport) {
			t.Fatalf("read after tail = %v", err)
		}
	})
}

func TestInboundPortValidation(t *testing.T) {
	var nilPort *inboundPort
	if err := nilPort.push(t.Context(), pcm(1), nil); !errors.Is(err, ErrMediaUnavailable) {
		t.Fatalf("nil push = %v", err)
	}
	if err := nilPort.waitForSpace(t.Context()); !errors.Is(err, ErrMediaUnavailable) {
		t.Fatalf("nil waitForSpace = %v", err)
	}
	if err := nilPort.Close(); err != nil {
		t.Fatal(err)
	}
	nilPort.notifySpace()
	nilPort.fail(errTransport)
	nilPort.SetPlaybackController(nil)
	if nilPort.getPlaybackController() != nil {
		t.Fatal("nil port has a controller")
	}

	port := newInboundPort(1)
	//nolint:staticcheck // the nil context is the validated input.
	if err := port.push(nil, pcm(1), nil); err == nil {
		t.Fatal("push(nil ctx) = nil")
	}
	//nolint:staticcheck // the nil context is the validated input.
	if err := port.waitForSpace(nil); err == nil {
		t.Fatal("waitForSpace(nil ctx) = nil")
	}
	if err := port.waitForSpace(t.Context()); err != nil {
		t.Fatalf("waitForSpace with room = %v", err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := port.push(canceled, pcm(1), nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("push(canceled) = %v", err)
	}
	if _, err := port.readQueuedFrame(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("read(canceled) = %v", err)
	}
	huge := sharedaudio.PCMFrame{Samples: make([]int16, maxMediaFrameSamples+1)}
	if err := port.push(t.Context(), huge, nil); !errors.Is(err, ErrMediaQueueFull) {
		t.Fatalf("oversized push = %v", err)
	}
	// An oversized provider frame is terminal for the port.
	if err := port.push(t.Context(), pcm(1), nil); !errors.Is(err, ErrMediaQueueFull) {
		t.Fatalf("push after oversized frame = %v", err)
	}
	if err := port.Close(); err != nil {
		t.Fatal(err)
	}
	if err := port.operationError(); !errors.Is(err, ErrMediaQueueFull) {
		t.Fatalf("first failure must win, got %v", err)
	}
}
