package mediagate

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
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

type closeCountingInbound struct {
	sharedaudio.InboundMedia
	closes atomic.Int32
}

func (i *closeCountingInbound) Close() error {
	i.closes.Add(1)
	return i.InboundMedia.Close()
}

type blockingCloseInbound struct {
	sharedaudio.InboundMedia
	closeStarted chan struct{}
	releaseClose chan struct{}
	closeOnce    sync.Once
	err          error
}

func (i *blockingCloseInbound) Close() error {
	i.closeOnce.Do(func() { close(i.closeStarted) })
	<-i.releaseClose
	return i.err
}

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

func TestSealInboundAllowsLosslessDrainBeforeGateClose(t *testing.T) {
	provider := sharedaudio.NewSessionMediaAtRate(nil, 16000)
	want := make([]int16, sharedaudio.DefaultSessionMediaFrameSamples+1)
	for index := range want {
		want[index] = int16(index + 1)
	}
	if err := provider.PushInbound(want); err != nil {
		t.Fatalf("PushInbound() = %v", err)
	}
	if err := provider.FlushInbound(); err != nil {
		t.Fatalf("FlushInbound() = %v", err)
	}

	gate := New(nil)
	providerEndpoints := provider.Endpoints()
	countingInbound := &closeCountingInbound{InboundMedia: providerEndpoints.Inbound}
	gate.Attach(context.Background(), sharedaudio.MediaEndpoints{
		Inbound:  countingInbound,
		Outbound: providerEndpoints.Outbound,
	})
	if err := gate.SealInbound(); err != nil {
		t.Fatalf("SealInbound() = %v", err)
	}
	drainCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := gate.DrainInbound(drainCtx); err != nil {
		t.Fatalf("DrainInbound() after SealInbound = %v", err)
	}
	frame, err := gate.Endpoints().Inbound.ReadFrame(context.Background())
	if err != nil {
		t.Fatalf("read sealed frame = %v", err)
	}
	if !reflect.DeepEqual(frame.Samples, want[:sharedaudio.DefaultSessionMediaFrameSamples]) {
		t.Fatalf("sealed frame samples differ: got %d samples, want %d", len(frame.Samples), sharedaudio.DefaultSessionMediaFrameSamples)
	}
	tail, err := gate.Endpoints().Inbound.ReadFrame(context.Background())
	if err != nil {
		t.Fatalf("read sealed tail = %v", err)
	}
	if !reflect.DeepEqual(tail.Samples, want[sharedaudio.DefaultSessionMediaFrameSamples:]) || !tail.EndOfResponse {
		t.Fatalf("sealed tail = %#v, want one exact end-of-response sample", tail)
	}
	if err := gate.Close(); err != nil {
		t.Fatalf("Gate.Close() = %v", err)
	}
	if got := countingInbound.closes.Load(); got != 1 {
		t.Fatalf("provider inbound Close calls = %d, want exactly one", got)
	}
}

func TestGateCloseWaitsForConcurrentInboundSeal(t *testing.T) {
	want := errors.New("inbound seal failed")
	provider := sharedaudio.NewSessionMedia(nil)
	inbound := &blockingCloseInbound{
		InboundMedia: provider.Endpoints().Inbound,
		closeStarted: make(chan struct{}),
		releaseClose: make(chan struct{}),
		err:          want,
	}
	gate := New(nil)
	gate.Attach(context.Background(), sharedaudio.MediaEndpoints{Inbound: inbound})

	sealErrs := make(chan error, 1)
	go func() { sealErrs <- gate.SealInbound() }()
	select {
	case <-inbound.closeStarted:
	case <-time.After(time.Second):
		t.Fatal("SealInbound did not start provider close")
	}

	closeErrs := make(chan error, 1)
	go func() { closeErrs <- gate.Close() }()
	select {
	case err := <-closeErrs:
		t.Fatalf("Gate.Close returned before concurrent seal completed: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(inbound.releaseClose)

	if err := <-sealErrs; !errors.Is(err, want) {
		t.Fatalf("SealInbound() = %v, want %v", err, want)
	}
	if err := <-closeErrs; !errors.Is(err, want) {
		t.Fatalf("Gate.Close() = %v, want %v", err, want)
	}
}

func TestSealInboundWaitsForCloseStartedFirst(t *testing.T) {
	want := errors.New("inbound close failed")
	provider := sharedaudio.NewSessionMedia(nil)
	inbound := &blockingCloseInbound{
		InboundMedia: provider.Endpoints().Inbound,
		closeStarted: make(chan struct{}),
		releaseClose: make(chan struct{}),
		err:          want,
	}
	gate := New(nil)
	gate.Attach(context.Background(), sharedaudio.MediaEndpoints{Inbound: inbound})

	closeErrs := make(chan error, 1)
	go func() { closeErrs <- gate.Close() }()
	select {
	case <-inbound.closeStarted:
	case <-time.After(time.Second):
		t.Fatal("Gate.Close did not start provider close")
	}

	sealErrs := make(chan error, 1)
	go func() { sealErrs <- gate.SealInbound() }()
	select {
	case err := <-sealErrs:
		t.Fatalf("SealInbound returned before Gate.Close completed: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(inbound.releaseClose)

	if err := <-closeErrs; !errors.Is(err, want) {
		t.Fatalf("Gate.Close() = %v, want %v", err, want)
	}
	if err := <-sealErrs; !errors.Is(err, want) {
		t.Fatalf("SealInbound() = %v, want %v", err, want)
	}
}
