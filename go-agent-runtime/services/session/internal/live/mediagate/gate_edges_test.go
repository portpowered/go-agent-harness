package mediagate

import (
	"context"
	"errors"
	"testing"

	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

func canceledContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	return ctx
}

// A second seal waits for and reports the first seal's outcome instead of
// closing the provider endpoint again.
func TestSealInboundIsIdempotentAndKeepsFirstResult(t *testing.T) {
	sealErr := errors.New("seal failed")
	inbound := &closeCountingInbound{InboundMedia: &closeErrInbound{blockingInbound: *newBlockingInbound(), err: sealErr}}
	gate := New(nil)
	gate.Attach(t.Context(), sharedaudio.MediaEndpoints{Inbound: inbound})
	for range 2 {
		if err := gate.SealInbound(); !errors.Is(err, sealErr) {
			t.Fatalf("SealInbound = %v, want %v", err, sealErr)
		}
	}
	if closes := inbound.closes.Load(); closes != 1 {
		t.Fatalf("provider inbound closed %d times, want 1", closes)
	}
	// The blocked provider read only ends when Close cancels the bridge.
	if err := gate.Close(); !errors.Is(err, sealErr) {
		t.Fatalf("Close = %v, want the seal failure", err)
	}
}

// A control whose barrier precedes unfinished media keeps waiting; the
// sender's cancellation releases the reservation.
func TestBeginControlBehindMediaHonorsCancellation(t *testing.T) {
	gate := New(nil)
	if _, err := gate.AdmitMedia(); err != nil {
		t.Fatal(err)
	}
	id, _, err := gate.RegisterAck()
	requireNoError(t, err, "RegisterAck")
	if _, err := gate.BeginControl(canceledContext(t), id); !errors.Is(err, context.Canceled) {
		t.Fatalf("BeginControl(canceled) = %v", err)
	}
	if present, _ := gate.ControlState(id); !present {
		t.Fatal("a sender's cancellation must leave the acknowledgement for the provider path")
	}
}

// A media frame admitted after a pending control waits behind it, and the
// writer's cancellation finishes that media slot.
func TestBeginMediaBehindControlHonorsCancellation(t *testing.T) {
	gate := New(nil)
	if _, _, err := gate.RegisterAck(); err != nil {
		t.Fatal(err)
	}
	mediaID, err := gate.AdmitMedia()
	requireNoError(t, err, "AdmitMedia")
	if _, err := gate.beginMedia(canceledContext(t), mediaID); !errors.Is(err, context.Canceled) {
		t.Fatalf("beginMedia(canceled) = %v", err)
	}
	gate.orderMu.Lock()
	done := gate.mediaDone
	gate.orderMu.Unlock()
	if done != mediaID {
		t.Fatalf("media done = %d, want %d", done, mediaID)
	}
}

// A control whose ordering reservation disappeared, or that reaches a closed
// gate, is rejected rather than admitted.
func TestBeginControlRejectsLostReservationAndClosedGate(t *testing.T) {
	gate := New(nil)
	id, _, err := gate.RegisterAck()
	requireNoError(t, err, "RegisterAck")
	gate.releaseControl(id)
	if _, err := gate.BeginControl(t.Context(), id); !errors.Is(err, context.Canceled) {
		t.Fatalf("BeginControl without reservation = %v", err)
	}
	requireNoError(t, gate.Close(), "Close")
	if _, err := gate.beginControl(t.Context(), "orphan", &controlAck{accepted: make(chan bool, 1)}); !errors.Is(err, sharedaudio.ErrSessionMediaClosed) {
		t.Fatalf("beginControl after Close = %v", err)
	}
	if err := gate.registerControl("late"); !errors.Is(err, sharedaudio.ErrSessionMediaClosed) {
		t.Fatalf("registerControl after Close = %v", err)
	}
}

func TestRegisterControlInitializesReservations(t *testing.T) {
	gate := New(nil)
	gate.controls = nil
	requireNoError(t, gate.registerControl("first"), "registerControl")
	if _, ok := gate.controls["first"]; !ok {
		t.Fatal("reservation not recorded")
	}
}

func TestGateOperationsRequireContext(t *testing.T) {
	gate := New(nil)
	//nolint:staticcheck // the nil context is the validated input.
	for name, err := range map[string]error{
		"Flush":        gate.Flush(nil),
		"DrainInbound": gate.DrainInbound(nil),
		"WaitReady":    gate.WaitReady(nil),
		"BeginControl": second(gate.beginControl(nil, "x", &controlAck{})),
		"Automatic":    second(gate.BeginAutomatic(nil)),
		"beginMedia":   second(gate.beginMedia(nil, 1)),
	} {
		if err == nil || errors.Is(err, ErrMediaUnavailable) {
			t.Errorf("%s(nil) = %v, want a context error", name, err)
		}
	}
	if err := gate.WaitReady(canceledContext(t)); !errors.Is(err, context.Canceled) {
		t.Fatalf("WaitReady(canceled) = %v", err)
	}
	gate.Fail(nil)
	if err := gate.WaitReady(t.Context()); !errors.Is(err, ErrMediaUnavailable) {
		t.Fatalf("WaitReady after Fail(nil) = %v", err)
	}
}

func second(_ func(), err error) error { return err }

func TestPortsReportClosedWithoutRecordedFailure(t *testing.T) {
	if err := newInboundPort(1).operationError(); !errors.Is(err, sharedaudio.ErrSessionMediaClosed) {
		t.Fatalf("inbound operationError = %v", err)
	}
	if err := newOutboundPort(1).operationError(); !errors.Is(err, sharedaudio.ErrSessionMediaClosed) {
		t.Fatalf("outbound operationError = %v", err)
	}
	if err := newOutboundPort(1).WriteFrame(canceledContext(t), pcm(1)); !errors.Is(err, context.Canceled) {
		t.Fatalf("WriteFrame(canceled) = %v", err)
	}
	gate := New(nil)
	requireNoError(t, gate.Close(), "Close")
	if err := gate.Endpoints().Outbound.WriteFrame(t.Context(), pcm(1)); !errors.Is(err, sharedaudio.ErrSessionMediaClosed) {
		t.Fatalf("WriteFrame after Close = %v", err)
	}
}
