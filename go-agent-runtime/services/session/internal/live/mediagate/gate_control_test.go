package mediagate

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"

	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

// TestGateBackpressureIsNotReportedAsTransportFailure writes far more frames
// than the bounded queue holds through a slow provider. Every frame must be
// delivered in order and the gate must never surface a queue-full error.
func TestGateBackpressureIsNotReportedAsTransportFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		recorder := &errorRecorder{}
		gate := New(recorder.record)
		target := newGatedOutbound()
		gate.Attach(t.Context(), sharedaudio.MediaEndpoints{Outbound: target})
		requireNoError(t, gate.WaitReady(t.Context()), "WaitReady")
		const frames = mediaQueueCapacity*2 + 5
		writeErr := writeFramesAsync(t.Context(), gate.Endpoints().Outbound, frames)
		synctest.Wait()
		requirePending(t, writeErr, "writer finished while provider was stalled")
		close(target.release)
		requireNoError(t, <-writeErr, "write under backpressure")
		requireNoError(t, gate.Flush(t.Context()), "Flush")
		written, _ := target.snapshot()
		requireOrdered(t, written, frames)
		requireNoError(t, gate.Close(), "Close")
		if _, closed := target.snapshot(); closed != 1 {
			t.Fatalf("provider outbound closed %d times, want 1", closed)
		}
		if errs := recorder.snapshot(); len(errs) != 0 {
			t.Fatalf("reported errors %v, want none", errs)
		}
	})
}

func requireNoError(t *testing.T, err error, what string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s = %v", what, err)
	}
}

// requirePending fails when ch already holds a value: the operation that
// feeds it must still be blocked.
func requirePending[T any](t *testing.T, ch <-chan T, message string) {
	t.Helper()
	select {
	case value := <-ch:
		t.Fatalf("%s (%v)", message, value)
	default:
	}
}

func writeFramesAsync(ctx context.Context, target sharedaudio.OutboundMedia, frames int) <-chan error {
	result := make(chan error, 1)
	go func() {
		for sample := range int16(frames) {
			if err := target.WriteFrame(ctx, pcm(sample)); err != nil {
				result <- err
				return
			}
		}
		result <- nil
	}()
	return result
}

func requireOrdered(t *testing.T, written []int16, frames int) {
	t.Helper()
	if len(written) != frames {
		t.Fatalf("delivered %d frames, want %d", len(written), frames)
	}
	for i, sample := range written {
		if sample != int16(i) {
			t.Fatalf("frame %d = %d, out of order", i, sample)
		}
	}
}

func TestGateProviderWriteFailureIsReportedAndTerminal(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		recorder := &errorRecorder{}
		gate := New(recorder.record)
		target := newGatedOutbound()
		target.err = errTransport
		close(target.release)
		gate.Attach(t.Context(), sharedaudio.MediaEndpoints{Outbound: target})
		if err := gate.Endpoints().Outbound.WriteFrame(t.Context(), pcm(1)); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		// The bridge releases the failed frame's reservation before it
		// fails the port, so nothing is left pending to flush.
		if err := gate.Flush(t.Context()); err != nil {
			t.Fatalf("Flush = %v", err)
		}
		if err := gate.Endpoints().Outbound.WriteFrame(t.Context(), pcm(2)); !errors.Is(err, errTransport) {
			t.Fatalf("WriteFrame after provider failure = %v", err)
		}
		if errs := recorder.snapshot(); len(errs) != 1 || !errors.Is(errs[0], errTransport) {
			t.Fatalf("reported %v, want one transport failure", errs)
		}
		if err := gate.Close(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestGateCloseUnblocksWritersAndJoinsBridge(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		gate := New(nil)
		target := newGatedOutbound()
		gate.Attach(t.Context(), sharedaudio.MediaEndpoints{Outbound: target})
		writeErr := make(chan error, 1)
		go func() {
			for sample := int16(0); ; sample++ {
				if err := gate.Endpoints().Outbound.WriteFrame(t.Context(), pcm(sample)); err != nil {
					writeErr <- err
					return
				}
			}
		}()
		synctest.Wait() // the writer is now blocked on a full queue
		flushErr := make(chan error, 1)
		go func() { flushErr <- gate.Flush(t.Context()) }()
		synctest.Wait()
		if err := gate.Close(); err != nil {
			t.Fatalf("Close = %v", err)
		}
		// Close cancels the bridge's in-flight provider write, so waiters
		// observe either the close or that cancellation.
		if err := <-writeErr; !errors.Is(err, sharedaudio.ErrSessionMediaClosed) && !errors.Is(err, context.Canceled) {
			t.Fatalf("blocked writer after Close = %v", err)
		}
		if err := <-flushErr; !errors.Is(err, sharedaudio.ErrSessionMediaClosed) && !errors.Is(err, context.Canceled) {
			t.Fatalf("Flush after Close = %v", err)
		}
		if err := gate.WaitReady(t.Context()); !errors.Is(err, sharedaudio.ErrSessionMediaClosed) {
			t.Fatalf("WaitReady after Close = %v", err)
		}
	})
}

func TestCancelAckKeepsMarkerUntilProviderConsumesIt(t *testing.T) {
	gate := New(nil)
	id, accepted, err := gate.RegisterAck()
	if err != nil || !IsControlID(id) {
		t.Fatalf("RegisterAck = %q, %v", id, err)
	}
	gate.CancelAck(id)
	if ok := <-accepted; ok {
		t.Fatal("canceled control acknowledged as accepted")
	}
	if present, canceled := gate.ControlState(id); !present || !canceled {
		t.Fatalf("ControlState = %v, %v; want present and canceled", present, canceled)
	}
	if _, err := gate.BeginControl(t.Context(), id); !errors.Is(err, context.Canceled) {
		t.Fatalf("BeginControl after cancel = %v", err)
	}
	// A late provider acknowledgement must not block on the full channel.
	gate.Acknowledge(id, true)
	if present, _ := gate.ControlState(id); present {
		t.Fatal("acknowledged marker still present")
	}
	gate.CancelAck(id) // unknown id is a no-op
}

func TestAbortAckRemovesMarkerImmediately(t *testing.T) {
	gate := New(nil)
	id, accepted, err := gate.RegisterAck()
	if err != nil {
		t.Fatal(err)
	}
	gate.AbortAck(id)
	if ok := <-accepted; ok {
		t.Fatal("aborted control acknowledged as accepted")
	}
	if present, _ := gate.ControlState(id); present {
		t.Fatal("aborted marker still present")
	}
	if _, err := gate.BeginControl(t.Context(), id); !errors.Is(err, sharedaudio.ErrSessionMediaClosed) {
		t.Fatalf("BeginControl after abort = %v", err)
	}
	gate.AbortAck(id)
}

func TestAcknowledgeDeliversAcceptance(t *testing.T) {
	gate := New(nil)
	id, accepted, err := gate.RegisterAck()
	if err != nil {
		t.Fatal(err)
	}
	release, err := gate.BeginControl(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	// Cancelling an active control must not drop its exclusive slot.
	gate.cancelControl(id)
	gate.Acknowledge(id, true)
	if ok := <-accepted; !ok {
		t.Fatal("acknowledgement lost")
	}
	release()
}

func TestControlAckOnNilAndClosedGates(t *testing.T) {
	var nilGate *Gate
	if _, _, err := nilGate.RegisterAck(); !errors.Is(err, ErrMediaUnavailable) {
		t.Fatalf("nil RegisterAck = %v", err)
	}
	nilGate.CancelAck("x")
	nilGate.AbortAck("x")
	nilGate.Acknowledge("x", true)
	nilGate.cancelAcks()
	nilGate.cancelControl("x")
	if present, _ := nilGate.ControlState("x"); present {
		t.Fatal("nil gate reports control")
	}
	gate := New(nil)
	if err := gate.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := gate.RegisterAck(); !errors.Is(err, sharedaudio.ErrSessionMediaClosed) {
		t.Fatalf("RegisterAck after Close = %v", err)
	}
}

// TestControlWaitsForEarlierMediaAndCloseWakesIt proves a control registered
// behind admitted media is held until that media is written, and that Close
// cancels a pending acknowledgement rather than leaving its sender blocked.
func TestControlWaitsForEarlierMediaAndCloseWakesIt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		gate := New(nil)
		target := newGatedOutbound()
		gate.Attach(t.Context(), sharedaudio.MediaEndpoints{Outbound: target})
		requireNoError(t, gate.Endpoints().Outbound.WriteFrame(t.Context(), pcm(1)), "WriteFrame")
		id, accepted, err := gate.RegisterAck()
		requireNoError(t, err, "RegisterAck")
		begun := runAsync(func() error {
			release, err := gate.BeginControl(t.Context(), id)
			if release != nil {
				release()
			}
			return err
		})
		synctest.Wait()
		requirePending(t, begun, "control overtook earlier media")
		close(target.release)
		requireNoError(t, <-begun, "BeginControl after media")
		if written, _ := target.snapshot(); len(written) != 1 {
			t.Fatalf("media written = %v", written)
		}
		_, accepted2, err := gate.RegisterAck()
		requireNoError(t, err, "RegisterAck")
		requireNoError(t, gate.Close(), "Close")
		// Close rejects every control still awaiting its provider dispatch.
		for _, ack := range []<-chan bool{accepted, accepted2} {
			if ok := <-ack; ok {
				t.Fatal("Close accepted a pending control")
			}
		}
	})
}

// runAsync runs operation on its own goroutine and delivers its result.
func runAsync(operation func() error) <-chan error {
	result := make(chan error, 1)
	go func() { result <- operation() }()
	return result
}

type closeErrInbound struct {
	blockingInbound
	err error
}

func (c *closeErrInbound) Close() error { return c.err }

const errEndpointClose testError = "close failed"

func TestAttachWithoutContextClosesEndpoints(t *testing.T) {
	recorder := &errorRecorder{}
	gate := New(recorder.record)
	outbound := newGatedOutbound()
	//nolint:staticcheck // the nil context is the validated input.
	gate.Attach(nil, sharedaudio.MediaEndpoints{Inbound: &closeErrInbound{err: errEndpointClose}, Outbound: outbound})
	if _, closed := outbound.snapshot(); closed != 1 {
		t.Fatalf("outbound closed %d times", closed)
	}
	if errs := recorder.snapshot(); len(errs) != 2 || !errors.Is(errs[1], errEndpointClose) {
		t.Fatalf("reported %v, want context error then close error", errs)
	}
	if err := gate.WaitReady(t.Context()); err == nil {
		t.Fatal("WaitReady after failed attach = nil")
	}
}

func TestAttachAfterCloseClosesEndpoints(t *testing.T) {
	recorder := &errorRecorder{}
	gate := New(recorder.record)
	requireNoError(t, gate.Close(), "Close")
	outbound := newGatedOutbound()
	gate.Attach(t.Context(), sharedaudio.MediaEndpoints{Inbound: &closeErrInbound{err: errEndpointClose}, Outbound: outbound})
	if _, closed := outbound.snapshot(); closed != 1 {
		t.Fatalf("outbound closed %d times", closed)
	}
	if errs := recorder.snapshot(); len(errs) != 1 || !errors.Is(errs[0], errEndpointClose) {
		t.Fatalf("reported %v", errs)
	}
}

func TestAttachWithoutOutboundReportsUnavailable(t *testing.T) {
	gate := New(nil)
	gate.Attach(t.Context(), sharedaudio.MediaEndpoints{})
	if err := gate.WaitReady(t.Context()); !errors.Is(err, ErrMediaUnavailable) {
		t.Fatalf("WaitReady without outbound = %v", err)
	}
	if err := gate.Endpoints().Outbound.WriteFrame(t.Context(), pcm(1)); !errors.Is(err, ErrMediaUnavailable) {
		t.Fatalf("WriteFrame without outbound = %v", err)
	}
	requireNoError(t, gate.DrainInbound(t.Context()), "DrainInbound without inbound")
	requireNoError(t, gate.Close(), "Close")
}

func TestNilGateOperations(t *testing.T) {
	var gate *Gate
	if gate.Endpoints() != (sharedaudio.MediaEndpoints{}) {
		t.Fatal("nil gate endpoints")
	}
	gate.SetFrameObserver(nil)
	gate.observeFrame(FrameInbound, pcm(1))
	gate.Attach(t.Context(), sharedaudio.MediaEndpoints{})
	gate.setPlaybackController(nil)
	if err := gate.WaitReady(t.Context()); !errors.Is(err, ErrMediaUnavailable) {
		t.Fatalf("WaitReady = %v", err)
	}
	if err := gate.DrainInbound(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := gate.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := gate.SealInbound(); err != nil {
		t.Fatal(err)
	}
	if err := gate.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := gate.BeginAutomatic(t.Context()); !errors.Is(err, ErrMediaUnavailable) {
		t.Fatalf("BeginAutomatic = %v", err)
	}
	if _, err := gate.AdmitMedia(); !errors.Is(err, ErrMediaUnavailable) {
		t.Fatalf("AdmitMedia = %v", err)
	}
	if _, err := gate.beginMedia(t.Context(), 1); !errors.Is(err, ErrMediaUnavailable) {
		t.Fatalf("beginMedia = %v", err)
	}
	if _, err := gate.beginControl(t.Context(), "x", nil); !errors.Is(err, ErrMediaUnavailable) {
		t.Fatalf("beginControl = %v", err)
	}
	if err := gate.registerControl("x"); !errors.Is(err, ErrMediaUnavailable) {
		t.Fatalf("registerControl = %v", err)
	}
	gate.releaseControl("x")
	gate.releaseExclusive("")
	gate.releaseMedia(1)
	gate.markMediaDone(1)
	if !gate.isClosed() {
		t.Fatal("nil gate must read as closed")
	}
}

func TestAutomaticSendIsExclusive(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		gate := New(nil)
		release, err := gate.BeginAutomatic(t.Context())
		requireNoError(t, err, "BeginAutomatic")
		second := runAsync(func() error {
			next, err := gate.BeginAutomatic(t.Context())
			if next != nil {
				next()
			}
			return err
		})
		synctest.Wait()
		requirePending(t, second, "second automatic send not exclusive")
		release()
		requireNoError(t, <-second, "second BeginAutomatic")

		release, err = gate.BeginAutomatic(t.Context())
		requireNoError(t, err, "BeginAutomatic")
		defer release()
		ctx, cancel := context.WithCancel(t.Context())
		canceled := runAsync(func() error { _, err := gate.BeginAutomatic(ctx); return err })
		synctest.Wait()
		cancel()
		if err := <-canceled; !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled automatic = %v", err)
		}
	})
}

func TestCloseWakesMediaWaiterAndRejectsAdmission(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		gate := New(nil)
		release, err := gate.BeginAutomatic(t.Context())
		requireNoError(t, err, "BeginAutomatic")
		mediaID, err := gate.AdmitMedia()
		requireNoError(t, err, "AdmitMedia")
		waiter := runAsync(func() error { _, err := gate.beginMedia(t.Context(), mediaID); return err })
		synctest.Wait()
		requireNoError(t, gate.Close(), "Close")
		if err := <-waiter; !errors.Is(err, sharedaudio.ErrSessionMediaClosed) {
			t.Fatalf("media waiter after Close = %v", err)
		}
		release()
		if _, err := gate.BeginAutomatic(t.Context()); !errors.Is(err, sharedaudio.ErrSessionMediaClosed) {
			t.Fatalf("BeginAutomatic after Close = %v", err)
		}
		if _, err := gate.AdmitMedia(); !errors.Is(err, sharedaudio.ErrSessionMediaClosed) {
			t.Fatalf("AdmitMedia after Close = %v", err)
		}
	})
}
