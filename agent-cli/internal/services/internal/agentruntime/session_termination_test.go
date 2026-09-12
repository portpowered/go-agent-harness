package agentruntime

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	functional "github.com/portpowered/go-agent-harness/go-agent-loop/test/functional"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

// noopStragglerDrain satisfies the boundary's mandatory straggler-drain
// callback for tests that are not exercising drain behavior themselves.
func noopStragglerDrain(sessionStragglerDrainPolicy) error { return nil }

func TestSessionTerminationBoundaryRendersBothDrainPhasesAndPreservesErrors(t *testing.T) {
	var rendered bytes.Buffer
	primaryErr := errors.New("initiating terminal failure")
	quiesceErr := errors.New("upstream quiesce failed")
	waitErr := errors.New("straggler wait failed")
	stopErr := errors.New("owned resource stop failed")
	flushErr := errors.New("buffered flush failed")

	boundary := sessionTerminationBoundary{
		quiesceUpstream: func() error {
			rendered.WriteString("upstream producers quiesced\n")
			return quiesceErr
		},
		waitForStragglers: func(policy sessionStragglerDrainPolicy) error {
			if policy.quietPeriod != sessionStragglerDrainQuietPeriod {
				t.Fatalf("straggler quiet period = %s, want %s", policy.quietPeriod, sessionStragglerDrainQuietPeriod)
			}
			rendered.WriteString("delayed accepted provider delta\n")
			return waitErr
		},
		stopOwnedResources: func() error {
			rendered.WriteString("owned resources stopped\n")
			return stopErr
		},
		flushBuffered: func() error {
			rendered.WriteString("already-buffered provider delta\n")
			return flushErr
		},
	}

	gotErr := boundary.terminate(primaryErr)
	if gotErr == nil {
		t.Fatal("termination returned nil error")
	}
	for _, wantErr := range []error{primaryErr, quiesceErr, waitErr, stopErr, flushErr} {
		if !errors.Is(gotErr, wantErr) {
			t.Fatalf("termination error = %v, want errors.Is(..., %v)", gotErr, wantErr)
		}
	}

	output := rendered.String()
	for _, want := range []string{"delayed accepted provider delta", "already-buffered provider delta"} {
		if !strings.Contains(output, want) {
			t.Fatalf("rendered termination output = %q, missing %q", output, want)
		}
	}
	if strings.Index(output, "upstream producers quiesced") > strings.Index(output, "delayed accepted provider delta") ||
		strings.Index(output, "delayed accepted provider delta") > strings.Index(output, "owned resources stopped") ||
		strings.Index(output, "owned resources stopped") > strings.Index(output, "already-buffered provider delta") {
		t.Fatalf("termination output order = %q, want quiesce, wait, stop, flush", output)
	}
}

func TestSessionTerminationBoundaryRunsCleanupOnlyOnce(t *testing.T) {
	var quiesceCalls, waitCalls, stopCalls, flushCalls int
	boundary := sessionTerminationBoundary{
		quiesceUpstream: func() error {
			quiesceCalls++
			return nil
		},
		waitForStragglers: func(sessionStragglerDrainPolicy) error {
			waitCalls++
			return nil
		},
		stopOwnedResources: func() error {
			stopCalls++
			return nil
		},
		flushBuffered: func() error {
			flushCalls++
			return nil
		},
	}

	if first := boundary.terminate(nil); first != nil {
		t.Fatalf("first termination error = %v", first)
	}
	if second := boundary.terminate(errors.New("ignored second cause")); second != nil {
		t.Fatalf("second termination error = %v, want cached clean result", second)
	}
	if quiesceCalls != 1 || waitCalls != 1 || stopCalls != 1 || flushCalls != 1 {
		t.Fatalf("cleanup calls = quiesce:%d wait:%d stop:%d flush:%d, want one each", quiesceCalls, waitCalls, stopCalls, flushCalls)
	}
}

func TestSessionStragglerDrainRejectsZeroPolicy(t *testing.T) {
	if err := waitForSessionLoopStragglers(nil, nil, sessionStragglerDrainPolicy{}, nil); !errors.Is(err, errInvalidSessionStragglerDrainPolicy) {
		t.Fatalf("zero straggler policy error = %v, want %v", err, errInvalidSessionStragglerDrainPolicy)
	}
}

func TestSessionStragglerDrainFrozenClockHasWallSafety(t *testing.T) {
	inf := functional.NewMockSessionInferencer()
	t.Cleanup(inf.Close)
	loop, err := agentloop.New(agentloop.WithSessionInferencer(inf))
	if err != nil {
		t.Fatalf("create session loop: %v", err)
	}
	clock := platformclock.NewDeterministic(time.Unix(0, 0).UTC(), time.Second)
	start := time.Now()
	if err := waitForSessionLoopStragglersWithContext(context.Background(), io.Discard, loop, defaultSessionStragglerDrainPolicy, nil, clock); err != nil {
		t.Fatalf("frozen-clock straggler drain: %v", err)
	}
	if elapsed := time.Since(start); elapsed >= time.Second {
		t.Fatalf("frozen-clock straggler drain took %s, want wall safety under 1s", elapsed)
	}
}

func TestSessionStragglerDrainCancellationWinsImmediately(t *testing.T) {
	inf := functional.NewMockSessionInferencer()
	t.Cleanup(inf.Close)
	loop, err := agentloop.New(agentloop.WithSessionInferencer(inf))
	if err != nil {
		t.Fatalf("create session loop: %v", err)
	}
	clock := platformclock.NewDeterministic(time.Unix(0, 0).UTC(), time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if err := waitForSessionLoopStragglersWithContext(ctx, io.Discard, loop, defaultSessionStragglerDrainPolicy, nil, clock); err != nil {
		t.Fatalf("cancelled straggler drain: %v", err)
	}
	if elapsed := time.Since(start); elapsed >= 100*time.Millisecond {
		t.Fatalf("cancelled straggler drain took %s, want immediate return", elapsed)
	}
}

func TestSessionTerminationBoundaryRejectsMissingStragglerDrain(t *testing.T) {
	var stopCalls, flushCalls int
	boundary := sessionTerminationBoundary{
		stopOwnedResources: func() error {
			stopCalls++
			return nil
		},
		flushBuffered: func() error {
			flushCalls++
			return nil
		},
	}

	err := boundary.terminate(nil)
	if !errors.Is(err, errMissingSessionStragglerDrain) {
		t.Fatalf("missing straggler drain error = %v, want %v", err, errMissingSessionStragglerDrain)
	}
	if stopCalls != 1 || flushCalls != 1 {
		t.Fatalf("cleanup calls = stop:%d flush:%d, want one each after configuration error", stopCalls, flushCalls)
	}
}

// TestSessionTerminationBoundaryPreservesCallerCancellationOnAnyExitPath pins
// the intermittent-failure root cause: the live and duration session loops'
// select statements have a dozen distinct terminal exit paths, and any one of
// them can win a race against <-ctx.Done() when the caller cancels the run
// context. Before this boundary unconditionally joined ctx.Err() itself, only
// the three call sites that happened to wrap sessionRunTerminationError
// preserved the caller's cancellation; every other exit path (including one a
// blocked tool executor's own cancellation-triggered event could select
// instead of ctx.Done() landing directly) silently dropped it whenever its
// "clean" result (primary == nil, as terminate(nil) is called throughout the
// loop) won that race. That produced exactly the CI symptom this test fixes:
// TestSessionUnresolvedToolResultTerminalPathsFailWithStableDiagnostic's
// caller_cancellation and caller_deadline subtests failed roughly one run in
// twenty-five, only inside the full test/integration package, never in
// isolation — because the race needs a second ready channel, which isolation
// starves. This test forces the exact condition deterministically: a boundary
// whose ctx is already cancelled (or past its deadline) before terminate is
// ever called, mimicking the "clean" branch winning the race outright.
func TestSessionTerminationBoundaryPreservesCallerCancellationOnAnyExitPath(t *testing.T) {
	t.Run("cancelled context survives a clean terminate(nil)", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		boundary := sessionTerminationBoundary{ctx: ctx, waitForStragglers: noopStragglerDrain}

		err := boundary.terminate(nil)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("terminate(nil) with a cancelled ctx = %v, want errors.Is(..., context.Canceled)", err)
		}
	})

	t.Run("expired deadline survives a clean terminate(nil)", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 0)
		defer cancel()
		<-ctx.Done()
		boundary := sessionTerminationBoundary{ctx: ctx, waitForStragglers: noopStragglerDrain}

		err := boundary.terminate(nil)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("terminate(nil) with an expired deadline = %v, want errors.Is(..., context.DeadlineExceeded)", err)
		}
	})

	t.Run("cancellation is joined alongside an unrelated primary error", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		primaryErr := errors.New("unresolved tool result")
		boundary := sessionTerminationBoundary{ctx: ctx, waitForStragglers: noopStragglerDrain}

		err := boundary.terminate(primaryErr)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("terminate(primary) with a cancelled ctx = %v, want errors.Is(..., context.Canceled)", err)
		}
		if !errors.Is(err, primaryErr) {
			t.Fatalf("terminate(primary) with a cancelled ctx = %v, want errors.Is(..., primaryErr)", err)
		}
	})

	t.Run("a live context is never joined into a clean result", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		boundary := sessionTerminationBoundary{ctx: ctx, waitForStragglers: noopStragglerDrain}

		if err := boundary.terminate(nil); err != nil {
			t.Fatalf("terminate(nil) with a live ctx = %v, want nil", err)
		}
	})
}

func TestSessionTerminationBoundaryWaitRemainsBoundedWhenOutputStops(t *testing.T) {
	started := time.Now()
	var rendered bytes.Buffer

	boundary := sessionTerminationBoundary{
		waitForStragglers: func(policy sessionStragglerDrainPolicy) error {
			timer := time.NewTimer(policy.quietPeriod)
			defer timer.Stop()
			<-timer.C
			rendered.WriteString("bounded wait completed\n")
			return nil
		},
		stopOwnedResources: func() error {
			rendered.WriteString("stop completed\n")
			return nil
		},
		flushBuffered: func() error {
			rendered.WriteString("flush completed\n")
			return nil
		},
	}

	if err := boundary.terminate(nil); err != nil {
		t.Fatalf("termination error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("bounded termination took %s, want under 250ms", elapsed)
	}
	if !strings.HasPrefix(rendered.String(), "bounded wait completed\n") {
		t.Fatalf("rendered termination output = %q, want bounded wait first", rendered.String())
	}
}
