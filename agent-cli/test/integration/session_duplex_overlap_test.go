package integration

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestSessionCLI_DuplexPCMOverlap(t *testing.T) {
	baselineGoroutines := runtime.NumGoroutine()
	aToB, bToA := v8LoudFrames(t, v8AudioFixturePath(t, "overlap_16k.wav"))
	run := runV8Duplex(t, aToB, bToA, false)
	if err := verifyV8Run(run, map[string][]byte{"A-to-B": aToB, "B-to-A": bToA}); err != nil {
		t.Fatalf("positive v8 duplex proof failed: %v", err)
	}
	assertV8GoroutinesSettled(t, baselineGoroutines, "positive duplex run")
	t.Logf("v8 positive evidence: shared clock base=%s tick_duration=%s overlap_tick=%d final_tick=%d crossings=%d", run.base.Format(time.RFC3339Nano), v8TickDuration, v8OverlapTick, run.finalTick, len(run.crossings))
}

func TestSessionCLI_DuplexPCMOverlapRejectsSilenceControl(t *testing.T) {
	baselineGoroutines := runtime.NumGoroutine()
	aToB, bToA := v8LoudFrames(t, v8AudioFixturePath(t, "overlap_16k.wav"))
	run := runV8Duplex(t, aToB, bToA, true)
	err := verifyV8Run(run, map[string][]byte{"A-to-B": aToB, "B-to-A": bToA})
	if err == nil {
		t.Fatal("silence negative control passed the positive audio verification")
	}
	diagnostic := err.Error()
	if !strings.Contains(diagnostic, "A-to-B") || !strings.Contains(diagnostic, fmt.Sprintf("logical tick %d", v8OverlapTick)) || !strings.Contains(diagnostic, "RMS") || !strings.Contains(diagnostic, "hash=") {
		t.Fatalf("negative control diagnostic lacks direction/tick/hash/RMS details: %v", err)
	}
	assertV8GoroutinesSettled(t, baselineGoroutines, "silence negative control")
	t.Logf("v8 silence negative control rejected as expected: %v", err)
}

func TestSessionCLI_DuplexPCMMultiTurnSchedule(t *testing.T) {
	baselineGoroutines := runtime.NumGoroutine()
	frames := v8LoudFrameSet(t, v8AudioFixturePath(t, "overlap_16k.wav"), v8MultiTurnCount)
	run := runV8MultiTurnDuplex(t, frames, frames)
	if err := verifyV8MultiTurnRun(run, frames, frames); err != nil {
		t.Fatalf("positive v8 multi-turn duplex proof failed: %v", err)
	}
	t.Logf("v8 multi-turn evidence: final_tick=%d crossings=%d A_runtime=%d B_runtime=%d", run.finalTick, len(run.crossings), len(run.harnesses["A"].Runtime), len(run.harnesses["B"].Runtime))
	assertV8GoroutinesSettled(t, baselineGoroutines, "multi-turn duplex run")
}

func TestSessionCLI_DuplexPCMMultiTurnRejectsLaterTurnAudioControl(t *testing.T) {
	baselineGoroutines := runtime.NumGoroutine()
	frames := v8LoudFrameSet(t, v8AudioFixturePath(t, "overlap_16k.wav"), v8MultiTurnCount)
	run := runV8MultiTurnDuplex(t, frames, frames)
	if err := mutateV8ViewPayload(&run, "B/agent", 2, frames[0]); err != nil {
		t.Fatalf("mutate later-turn PCM control: %v", err)
	}
	err := verifyV8MultiTurnRun(run, frames, frames)
	if err == nil {
		t.Fatal("later-turn PCM negative control passed the positive multi-turn verifier")
	}
	diagnostic := err.Error()
	for _, part := range []string{"B/agent", "turn 2", "PCM", "expected hash=", "observed hash="} {
		if !strings.Contains(diagnostic, part) {
			t.Fatalf("later-turn PCM diagnostic lacks %q: %v", part, err)
		}
	}
	assertV8GoroutinesSettled(t, baselineGoroutines, "later-turn PCM negative control")
	t.Logf("v8 later-turn PCM negative control rejected as expected: %v", err)
}

func TestSessionCLI_DuplexPCMMultiTurnRejectsLaterTurnTranscriptControl(t *testing.T) {
	baselineGoroutines := runtime.NumGoroutine()
	frames := v8LoudFrameSet(t, v8AudioFixturePath(t, "overlap_16k.wav"), v8MultiTurnCount)
	run := runV8MultiTurnDuplex(t, frames, frames)
	if err := mutateV8TranscriptMarker(&run, "B", 2, "A transcript turn 2"); err != nil {
		t.Fatalf("mutate later-turn transcript control: %v", err)
	}
	err := verifyV8MultiTurnRun(run, frames, frames)
	if err == nil {
		t.Fatal("later-turn transcript negative control passed the positive multi-turn verifier")
	}
	diagnostic := err.Error()
	for _, part := range []string{"harness B", "turn 2", "transcript", "expected=", "observed="} {
		if !strings.Contains(diagnostic, part) {
			t.Fatalf("later-turn transcript diagnostic lacks %q: %v", part, err)
		}
	}
	assertV8GoroutinesSettled(t, baselineGoroutines, "later-turn transcript negative control")
	t.Logf("v8 later-turn transcript negative control rejected as expected: %v", err)
}

func TestSessionCLI_DuplexPCMMultiTurnRejectsLaterTurnCommitControls(t *testing.T) {
	t.Run("missing commit", func(t *testing.T) {
		baselineGoroutines := runtime.NumGoroutine()
		frames := v8LoudFrameSet(t, v8AudioFixturePath(t, "overlap_16k.wav"), v8MultiTurnCount)
		run := runV8MultiTurnDuplex(t, frames, frames)
		if err := dropV8InputCommit(&run, "A", 2); err != nil {
			t.Fatalf("drop later-turn input commit control: %v", err)
		}
		err := verifyV8MultiTurnRun(run, frames, frames)
		if err == nil {
			t.Fatal("missing later-turn input commit negative control passed the positive multi-turn verifier")
		}
		diagnostic := err.Error()
		for _, part := range []string{"harness A", "B-to-A", "B-turn-2", "input commit", "expected=2", "observed=3"} {
			if !strings.Contains(diagnostic, part) {
				t.Fatalf("missing input commit diagnostic lacks %q: %v", part, err)
			}
		}
		assertV8GoroutinesSettled(t, baselineGoroutines, "missing later-turn input commit negative control")
		t.Logf("v8 missing later-turn input commit negative control rejected as expected: %v", err)
	})

	t.Run("cross-attributed commit", func(t *testing.T) {
		baselineGoroutines := runtime.NumGoroutine()
		frames := v8LoudFrameSet(t, v8AudioFixturePath(t, "overlap_16k.wav"), v8MultiTurnCount)
		run := runV8MultiTurnDuplex(t, frames, frames)
		if err := mutateV8InputCommitPayload(&run, "A", 2, frames[0]); err != nil {
			t.Fatalf("mutate later-turn input commit control: %v", err)
		}
		err := verifyV8MultiTurnRun(run, frames, frames)
		if err == nil {
			t.Fatal("cross-attributed later-turn input commit negative control passed the positive multi-turn verifier")
		}
		diagnostic := err.Error()
		for _, part := range []string{"harness A", "B-to-A", "B-turn-2", "input commit", "expected hash=", "observed hash="} {
			if !strings.Contains(diagnostic, part) {
				t.Fatalf("cross-attributed input commit diagnostic lacks %q: %v", part, err)
			}
		}
		assertV8GoroutinesSettled(t, baselineGoroutines, "cross-attributed later-turn input commit negative control")
		t.Logf("v8 cross-attributed later-turn input commit negative control rejected as expected: %v", err)
	})
}
