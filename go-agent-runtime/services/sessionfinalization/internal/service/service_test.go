package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionfinalization"
)

type emptyCausesError struct{}

func (emptyCausesError) Error() string   { return "empty causes" }
func (emptyCausesError) Unwrap() []error { return nil }
func allowedCancellationError() error    { return errors.New("allowed cancellation") }
func disallowedCancellationError() error { return errors.New("unrelated failure") }
func cancellationFailure() *sessionfinalization.CancellationFailure {
	return &sessionfinalization.CancellationFailure{TerminalReason: "cancellation", Provenance: "loop"}
}

func TestCancellationOnlyClassifiesAllowedCauses(t *testing.T) {
	allowed := allowedCancellationError()
	disallowed := disallowedCancellationError()
	normalizationTarget := errors.New("normalization target")
	wrappedAllowed := fmt.Errorf("wrapped: %w", allowed)
	joinedAllowed := errors.Join(allowed, fmt.Errorf("second: %w", allowed))

	tests := []struct {
		name      string
		request   sessionfinalization.CancellationRequest
		normalize func(error) error
		want      bool
	}{
		{name: "signal missing", request: sessionfinalization.CancellationRequest{Err: allowed}, want: false},
		{name: "nil error", request: sessionfinalization.CancellationRequest{SignalReceived: true}, want: true},
		{name: "direct allowed", request: sessionfinalization.CancellationRequest{SignalReceived: true, Err: allowed, Allowed: []error{allowed}}, want: true},
		{name: "wrapped allowed", request: sessionfinalization.CancellationRequest{SignalReceived: true, Err: wrappedAllowed, Allowed: []error{allowed}}, want: true},
		{name: "joined allowed", request: sessionfinalization.CancellationRequest{SignalReceived: true, Err: joinedAllowed, Allowed: []error{allowed}}, want: true},
		{name: "disallowed leaf", request: sessionfinalization.CancellationRequest{SignalReceived: true, Err: disallowed, Allowed: []error{allowed}}, want: false},
		{name: "empty joined causes", request: sessionfinalization.CancellationRequest{SignalReceived: true, Err: emptyCausesError{}, Allowed: []error{allowed}}, want: false},
		{name: "normalizes to allowed", request: sessionfinalization.CancellationRequest{SignalReceived: true, Err: normalizationTarget, Allowed: []error{allowed}}, normalize: func(error) error { return allowed }, want: true},
		{name: "normalizer returns nil", request: sessionfinalization.CancellationRequest{SignalReceived: true, Err: disallowed, Allowed: []error{allowed}}, normalize: func(error) error { return nil }, want: false},
		{name: "cancellation failure", request: sessionfinalization.CancellationRequest{SignalReceived: true, Err: allowed, Allowed: []error{allowed}, Failure: cancellationFailure()}, want: true},
		{name: "wrong terminal reason", request: sessionfinalization.CancellationRequest{SignalReceived: true, Err: allowed, Allowed: []error{allowed}, Failure: &sessionfinalization.CancellationFailure{TerminalReason: "provider", Provenance: "loop"}}, want: false},
		{name: "wrong provenance", request: sessionfinalization.CancellationRequest{SignalReceived: true, Err: allowed, Allowed: []error{allowed}, Failure: &sessionfinalization.CancellationFailure{TerminalReason: "cancellation", Provenance: "provider"}}, want: false},
	}

	service := New()
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := test.request
			request.Normalize = test.normalize
			if got := service.CancellationOnly(request); got != test.want {
				t.Fatalf("CancellationOnly() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestFinalizerFinishesInDeclaredOrderAndJoinsFailures(t *testing.T) {
	primary := errors.New("primary")
	capErr := errors.New("capabilities")
	bindingErr := errors.New("binding")
	flushErr := errors.New("flush")
	finalizeErr := errors.New("finalize")
	releaseErr := errors.New("release")
	var events []string
	var mu sync.Mutex
	record := func(name string) {
		mu.Lock()
		events = append(events, name)
		mu.Unlock()
	}
	ctx := context.WithValue(context.Background(), testContextKey{}, "marker")
	var gotContext context.Context
	var gotWriter io.Writer
	out := &bytes.Buffer{}
	finalizer := New().New(sessionfinalization.Callbacks{
		CloseCapabilities: func() error { record("capabilities"); return capErr },
		CloseSession:      func() error { record("session"); return nil },
		CloseRuntime:      func() error { record("runtime"); return nil },
		FlushCapture:      func() error { record("flush"); return flushErr },
		Finalize: func(got context.Context, writer io.Writer) error {
			record("finalize")
			gotContext, gotWriter = got, writer
			return finalizeErr
		},
		ReleaseCapture: func() error { record("release"); return releaseErr },
	})
	finalizer.SetDeviceBinding(func() error { record("binding"); return bindingErr })

	got := finalizer.Finish(ctx, out, primary)
	for _, want := range []error{primary, capErr, bindingErr, flushErr, finalizeErr, releaseErr} {
		if !errors.Is(got, want) {
			t.Fatalf("Finish() = %v, missing %v", got, want)
		}
	}
	wantEvents := []string{"capabilities", "session", "binding", "runtime", "flush", "finalize", "release"}
	if !equalStrings(events, wantEvents) {
		t.Fatalf("cleanup order = %v, want %v", events, wantEvents)
	}
	if gotContext != ctx || gotWriter != out {
		t.Fatalf("Finalize received context/writer (%p, %p), want (%p, %p)", gotContext, gotWriter, ctx, out)
	}
}

func TestFinalizerFinishIsOnceAndUsesDiscardForNilWriter(t *testing.T) {
	var calls int
	var finalizeWriter io.Writer
	finalizer := New().New(sessionfinalization.Callbacks{
		CloseSession: func() error { calls++; return nil },
		Finalize: func(_ context.Context, writer io.Writer) error {
			calls++
			finalizeWriter = writer
			return nil
		},
	})
	first := errors.New("first")
	second := errors.New("second")
	if got := finalizer.Finish(context.Background(), nil, first); !errors.Is(got, first) {
		t.Fatalf("first Finish() = %v, missing first cause", got)
	}
	if got := finalizer.Finish(context.Background(), nil, second); !errors.Is(got, second) {
		t.Fatalf("second Finish() = %v, missing second cause", got)
	}
	if calls != 2 {
		t.Fatalf("cleanup callback count = %d, want 2", calls)
	}
	if finalizeWriter == nil {
		t.Fatal("Finalize received nil writer")
	}
}

func TestFinalizerRecoversPanicAndContinuesCleanup(t *testing.T) {
	var events []string
	finalizer := New().New(sessionfinalization.Callbacks{
		CloseCapabilities: func() error { events = append(events, "capabilities"); panic("boom") },
		CloseSession:      func() error { events = append(events, "session"); return nil },
		CloseRuntime:      func() error { events = append(events, "runtime"); return nil },
		ReleaseCapture:    func() error { events = append(events, "release"); return nil },
	})
	err := finalizer.Finish(context.Background(), nil, nil)
	if !errors.Is(err, sessionfinalization.ErrPanic) {
		t.Fatalf("panic error = %v, want ErrPanic", err)
	}
	if !equalStrings(events, []string{"capabilities", "session", "runtime", "release"}) {
		t.Fatalf("cleanup after panic = %v", events)
	}
}

func TestFinalizerNilReceiverAndHelpers(t *testing.T) {
	var finalizer *finalizer
	finalizer.SetDeviceBinding(func() error { return nil })
	primary := errors.New("primary")
	if got := finalizer.Finish(context.Background(), nil, primary); !errors.Is(got, primary) {
		t.Fatalf("nil finalizer Finish() = %v", got)
	}
	if invoke(nil) != nil || wrap("phase", nil) != nil {
		t.Fatal("nil helper input produced an error")
	}
	leaf := errors.New("leaf")
	if !errors.Is(invoke(func() error { return leaf }), leaf) || !errors.Is(wrap("phase", leaf), leaf) {
		t.Fatal("helper did not preserve error identity")
	}
}

type testContextKey struct{}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
