package service

import (
	"context"
	"errors"
	"io"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionfinalization"
)

type intentProbe bool

func (i intentProbe) SIGINTReceived() bool { return bool(i) }

type panicIntent struct{}

func (panicIntent) SIGINTReceived() bool { panic("intent panic") }

type contextValueKey struct{}

type causeProbe struct{ cause error }

func (e *causeProbe) Error() string { return "cause boundary" }
func (e *causeProbe) CancellationCause() error {
	if e == nil {
		return nil
	}
	return e.cause
}

type panicCause struct{}

func (panicCause) Error() string { return "panic cause" }
func (panicCause) CancellationCause() error {
	panic("cause panic")
}

type unwrapOne struct{ cause error }

func (e unwrapOne) Error() string { return "wrapped" }
func (e unwrapOne) Unwrap() error { return e.cause }

type unwrapMany struct{ causes []error }

func (e unwrapMany) Error() string   { return "joined" }
func (e unwrapMany) Unwrap() []error { return e.causes }

func TestFinalizerOrdersEveryStageAndPreservesErrorIdentity(t *testing.T) {
	primary := errors.New("primary")
	capErr, sessionErr, deviceErr := errors.New("capabilities"), errors.New("session"), errors.New("device")
	runtimeErr, flushErr, finalizeErr, claimErr := errors.New("runtime"), errors.New("flush"), errors.New("finalize"), errors.New("claim")
	var order []string
	cleanup := func(name string, err error) sessionfinalization.Cleanup {
		return func() error { order = append(order, name); return err }
	}
	req := sessionfinalization.FinalizerRequest{
		CloseCapabilities: cleanup("capabilities", capErr), CloseSession: cleanup("session", sessionErr),
		CloseDevice: cleanup("device", deviceErr), CloseRuntime: cleanup("runtime", runtimeErr),
		FlushCapture:        cleanup("flush", flushErr),
		Finalize:            func(context.Context, io.Writer) error { order = append(order, "finalize"); return finalizeErr },
		ReleaseCaptureClaim: cleanup("claim", claimErr),
		PhaseError:          func(phase string, err error) error { return errors.Join(errors.New(phase), err) },
		RuntimeError:        func(err error) error { return errors.Join(errors.New("runtime envelope"), err) },
	}
	got := New().NewFinalizer(req).Finish(context.Background(), io.Discard, primary)
	for _, want := range []error{primary, capErr, sessionErr, deviceErr, runtimeErr, flushErr, finalizeErr, claimErr} {
		if !errors.Is(got, want) {
			t.Fatalf("finalizer error %v does not preserve %v", got, want)
		}
	}
	if want := []string{"capabilities", "session", "device", "runtime", "flush", "finalize", "claim"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("finalizer order %#v, want %#v", order, want)
	}
}

func TestFinalizerIsOnceOnlyAcrossFinishAndCleanup(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	var nilContext context.Context
	req := sessionfinalization.FinalizerRequest{Finalize: func(context.Context, io.Writer) error {
		mu.Lock()
		calls++
		mu.Unlock()
		return nil
	}}
	f := New().NewFinalizer(req)
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				if err := f.Finish(nilContext, nil, nil); err != nil {
					t.Errorf("concurrent finish = %v", err)
				}
			} else {
				if err := f.Cleanup(nilContext, nil); err != nil {
					t.Errorf("concurrent cleanup = %v", err)
				}
			}
		}(i)
	}
	wg.Wait()
	if calls != 1 {
		t.Fatalf("finalizer callback calls = %d, want one", calls)
	}
	if err := f.Finish(nilContext, nil, errors.New("later primary")); !errors.Is(err, errors.New("later primary")) {
		// A fresh errors.New is intentionally not identity-equal; this branch
		// documents that the cached cleanup does not replace the new primary.
		if err == nil || err.Error() != "later primary" {
			t.Fatalf("later primary = %v", err)
		}
	}
}

func TestFinalizerNormalizesNilInputsAndContinuesAfterPanic(t *testing.T) {
	panicErr := errors.New("after panic")
	var nilContext context.Context
	var gotContext context.Context
	var gotWriter io.Writer
	var order []string
	f := New().NewFinalizer(sessionfinalization.FinalizerRequest{
		CloseSession: func() error { order = append(order, "panic"); panic("boom") },
		CloseRuntime: func() error { order = append(order, "runtime"); return panicErr },
		Finalize: func(ctx context.Context, out io.Writer) error {
			gotContext, gotWriter = ctx, out
			order = append(order, "finalize")
			return nil
		},
	})
	err := f.Finish(nilContext, nil, nil)
	if !errors.Is(err, sessionfinalization.ErrFinalizationPanic) || !errors.Is(err, panicErr) {
		t.Fatalf("panic finalizer error = %v, want panic and later error", err)
	}
	if gotContext == nil || gotWriter == nil {
		t.Fatalf("normalized finalizer inputs = context:%v writer:%v", gotContext, gotWriter)
	}
	if want := []string{"panic", "runtime", "finalize"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("panic finalizer order %#v, want %#v", order, want)
	}
}

func TestFinalizerHandlesDecoratorAndCallbackPanics(t *testing.T) {
	phaseErr := errors.New("phase failure")
	finalizeErr := errors.New("finalize failure")
	var gotContext context.Context
	var nilContext context.Context
	f := New().NewFinalizer(sessionfinalization.FinalizerRequest{
		CloseSession: func() error { return phaseErr },
		FinalizeContext: func(ctx context.Context) context.Context {
			return context.WithValue(ctx, contextValueKey{}, "decorated")
		},
		Finalize: func(ctx context.Context, _ io.Writer) error {
			gotContext = ctx
			return finalizeErr
		},
		PhaseError:   func(string, error) error { panic("phase decorator panic") },
		RuntimeError: func(error) error { panic("runtime decorator panic") },
	})
	err := f.Cleanup(context.Background(), io.Discard)
	if !errors.Is(err, sessionfinalization.ErrFinalizationPanic) {
		t.Fatalf("decorator panic error = %v", err)
	}
	if gotContext == nil || gotContext.Value(contextValueKey{}) != "decorated" {
		t.Fatalf("finalize context = %v", gotContext)
	}

	contextPanic := New().NewFinalizer(sessionfinalization.FinalizerRequest{
		FinalizeContext: func(context.Context) context.Context { panic("context decorator panic") },
		Finalize:        func(ctx context.Context, _ io.Writer) error { return ctx.Err() },
	})
	if err := contextPanic.Cleanup(nilContext, nil); !errors.Is(err, sessionfinalization.ErrFinalizationPanic) {
		t.Fatalf("context decorator panic = %v", err)
	}

	callbackPanic := New().NewFinalizer(sessionfinalization.FinalizerRequest{
		Finalize: func(context.Context, io.Writer) error { panic("finalize callback panic") },
	})
	if err := callbackPanic.Cleanup(context.Background(), io.Discard); !errors.Is(err, sessionfinalization.ErrFinalizationPanic) {
		t.Fatalf("finalize callback panic = %v", err)
	}
}

func TestFinalizerAndTerminationNilReceiversAndPanicBoundaries(t *testing.T) {
	primary := errors.New("primary")
	var nilContext context.Context
	var nilFinalizer *finalizer
	if err := nilFinalizer.Finish(nilContext, nil, primary); !errors.Is(err, primary) {
		t.Fatalf("nil finalizer finish = %v", err)
	}
	if err := nilFinalizer.Cleanup(nilContext, nil); err != nil {
		t.Fatalf("nil finalizer cleanup = %v", err)
	}
	var nilBoundary *terminationBoundary
	if err := nilBoundary.Terminate(primary); !errors.Is(err, primary) {
		t.Fatalf("nil termination = %v", err)
	}

	b := New().NewTerminationBoundary(context.Background(), sessionfinalization.TerminationRequest{
		WaitForStragglers:  func(sessionfinalization.DrainPolicy) error { panic("drain panic") },
		StopOwnedResources: func() error { panic("stop panic") },
		FlushBuffered:      func() error { panic("flush panic") },
	})
	err := b.Terminate(nil)
	if !errors.Is(err, sessionfinalization.ErrFinalizationPanic) {
		t.Fatalf("termination panics = %v", err)
	}
}

func TestTerminationOrdersDrainAndJoinsContext(t *testing.T) {
	primary := errors.New("primary")
	quiesceErr, waitErr, stopErr, flushErr := errors.New("quiesce"), errors.New("wait"), errors.New("stop"), errors.New("flush")
	var order []string
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := New().NewTerminationBoundary(ctx, sessionfinalization.TerminationRequest{
		QuiesceUpstream: func() error { order = append(order, "quiesce"); return quiesceErr },
		WaitForStragglers: func(policy sessionfinalization.DrainPolicy) error {
			if policy != (sessionfinalization.DrainPolicy{QuietPeriod: sessionfinalization.DefaultStragglerDrainQuietPeriod, WallSafety: sessionfinalization.DefaultStragglerDrainWallSafety}) {
				t.Fatalf("drain policy %#v", policy)
			}
			order = append(order, "wait")
			return waitErr
		},
		StopOwnedResources: func() error { order = append(order, "stop"); return stopErr },
		FlushBuffered:      func() error { order = append(order, "flush"); return flushErr },
	})
	cancel()
	err := b.Terminate(primary)
	for _, want := range []error{primary, quiesceErr, waitErr, stopErr, flushErr, context.Canceled} {
		if !errors.Is(err, want) {
			t.Fatalf("termination error %v does not preserve %v", err, want)
		}
	}
	if want := []string{"quiesce", "wait", "stop", "flush"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("termination order %#v, want %#v", order, want)
	}
}

func TestTerminationRequiresDrainAndRunsRemainingStages(t *testing.T) {
	calls := 0
	var nilContext context.Context
	b := New().NewTerminationBoundary(nilContext, sessionfinalization.TerminationRequest{
		StopOwnedResources: func() error { calls++; return nil },
		FlushBuffered:      func() error { calls++; return nil },
	})
	err := b.Terminate(nil)
	if !errors.Is(err, sessionfinalization.ErrMissingStragglerDrain) {
		t.Fatalf("missing drain = %v", err)
	}
	if calls != 2 {
		t.Fatalf("remaining cleanup calls = %d, want two", calls)
	}
}

func TestTerminationIsOnceOnlyAndConvertsPanics(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	b := New().NewTerminationBoundary(context.Background(), sessionfinalization.TerminationRequest{
		QuiesceUpstream:    func() error { panic("quiesce panic") },
		WaitForStragglers:  func(sessionfinalization.DrainPolicy) error { mu.Lock(); calls++; mu.Unlock(); return nil },
		StopOwnedResources: func() error { mu.Lock(); calls++; mu.Unlock(); return nil },
		FlushBuffered:      func() error { mu.Lock(); calls++; mu.Unlock(); return nil },
	})
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := b.Terminate(nil); err == nil {
				t.Error("concurrent termination lost panic")
			}
		}()
	}
	wg.Wait()
	if calls != 3 {
		t.Fatalf("termination callback calls = %d, want three", calls)
	}
	if err := b.Terminate(nil); !errors.Is(err, sessionfinalization.ErrFinalizationPanic) {
		t.Fatalf("cached panic = %v", err)
	}
}

func TestSIGINTClassifierCoversNestedAndMixedErrorTrees(t *testing.T) {
	known := errors.New("known cancellation")
	unknown := errors.New("provider failure")
	options := sessionfinalization.ErrorTreeOptions{CancellationErrors: []error{known}}
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, true},
		{"leaf", known, true},
		{"wrapped", fmtError{cause: known}, true},
		{"joined allowed", unwrapMany{causes: []error{known, fmtError{cause: known}}}, true},
		{"joined mixed", errors.Join(known, unknown), false},
		{"unknown", unknown, false},
		{"deadline", context.DeadlineExceeded, false},
		{"empty multi unwrap", unwrapMany{}, false},
		{"nil unary unwrap", unwrapOne{}, true},
		{"typed cause", &causeProbe{cause: known}, true},
		{"typed nil cause", &causeProbe{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := New().SIGINTErrorOnly(tc.err, options); got != tc.want {
				t.Fatalf("SIGINTErrorOnly(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
	if New().SIGINTCancellationOnly(known, intentProbe(false), options) {
		t.Fatal("false intent classified as SIGINT")
	}
	if !New().SIGINTCancellationOnly(known, intentProbe(true), options) {
		t.Fatal("true intent not classified as SIGINT")
	}
	if New().SIGINTCancellationOnly(known, nil, options) {
		t.Fatal("nil intent classified as SIGINT")
	}
	if New().SIGINTCancellationOnly(known, panicIntent{}, options) {
		t.Fatal("panicking intent classified as SIGINT")
	}
	if New().SIGINTErrorOnly(panicCause{}, options) {
		t.Fatal("panicking cause classified as clean")
	}
}

type fmtError struct{ cause error }

func (e fmtError) Error() string { return "formatted" }
func (e fmtError) Unwrap() error { return e.cause }

func TestSIGINTObserverPrecedenceRequiresExactLoopCancellation(t *testing.T) {
	s := New()
	known := context.Canceled
	for _, tc := range []struct {
		name    string
		failure *sessionfinalization.ObserverFailure
		want    bool
	}{
		{"none", nil, true},
		{"loop cancellation", &sessionfinalization.ObserverFailure{TerminalReason: sessionfinalization.TerminalReasonCancellation, Provenance: sessionfinalization.TerminalProvenanceLoop}, true},
		{"provider cancellation", &sessionfinalization.ObserverFailure{TerminalReason: sessionfinalization.TerminalReasonCancellation, Provenance: "provider"}, false},
		{"loop failure", &sessionfinalization.ObserverFailure{TerminalReason: "terminal_failure", Provenance: sessionfinalization.TerminalProvenanceLoop}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := s.SIGINTCleanForObserver(known, intentProbe(true), tc.failure, sessionfinalization.ErrorTreeOptions{}); got != tc.want {
				t.Fatalf("observer classification = %v, want %v", got, tc.want)
			}
		})
	}
	if s.SIGINTObserverFailureOnly(&sessionfinalization.ObserverFailure{}) {
		t.Fatal("empty observer facts classified clean")
	}
}

func TestContextDeadlineIsRetainedByTermination(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-ctx.Done()
	b := New().NewTerminationBoundary(ctx, sessionfinalization.TerminationRequest{WaitForStragglers: func(sessionfinalization.DrainPolicy) error { return nil }})
	if !errors.Is(b.Terminate(nil), context.DeadlineExceeded) {
		t.Fatal("expired deadline was dropped")
	}
}
