package services

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
)

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
