//go:build sessioncapacityramp

package sessions

import (
	"fmt"
	"runtime"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// This file is an ON-DEMAND measurement, not part of any default suite. It is
// excluded from ordinary builds by the sessioncapacityramp build tag so every
// PR-tier test scope stays inside its time budget. Reproduce with:
//
//	go test ./test/functional/sessions -tags=sessioncapacityramp \
//	    -run '^TestLongConcurrencyCeilingRamp$' -v -count=1
//
// The measured results are recorded in docs/architecture/concurrent-session-capacity.md.
const (
	// rampStartSessions is the contract floor from story 001; the ramp begins
	// where the required proofs already hold.
	rampStartSessions = 8
	// rampMaxSessions caps the search so a pathological environment cannot
	// spin the ramp forever; reaching the cap reports "ceiling >= cap".
	rampMaxSessions = 128
)

// resourceSample is one point-in-time observation of process-level counters.
// Samples are taken synchronously on the coordinating goroutine BETWEEN rungs,
// never concurrently with a run, and are reported only — they never pace or
// gate anything.
type resourceSample struct {
	goroutines    int
	heapAllocByte uint64
	heapSysByte   uint64
}

func sampleResources() resourceSample {
	runtime.GC()
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	return resourceSample{
		goroutines:    runtime.NumGoroutine(),
		heapAllocByte: stats.HeapAlloc,
		heapSysByte:   stats.HeapSys,
	}
}

// TestLongConcurrencyCeilingRamp ramps the session count upward until a rung
// fails to complete cleanly and reports the highest fully-clean session count.
// Correctness per rung is judged exactly like the required proofs: deterministic
// tick scheduling drives every run and each surviving session must finish its
// full three-turn script (completed-turn invariants, lifecycle, zero leakage).
func TestLongConcurrencyCeilingRamp(t *testing.T) {
	highestClean := 0
	firstFailing := 0

	for n := rampStartSessions; n <= rampMaxSessions && firstFailing == 0; n *= 2 {
		started := time.Now()

		clean := t.Run(fmt.Sprintf("ramp-%03d", n), func(st *testing.T) {
			run := runConcurrentSessions(st, concurrentDriverOptions{
				SessionCount: n,
				Turns:        concurrentDefaultTurns,
				CancelID:     -1,
			})
			assertEverySessionCompletedScript(st, n, run)
		})
		sample := sampleResources()
		elapsed := time.Since(started)
		if !clean {
			firstFailing = n
			t.Logf("RAMP rung sessions=%d result=FAILED wall=%s goroutines=%d heap_alloc_kib=%d heap_sys_mib=%d",
				n, elapsed.Round(time.Millisecond), sample.goroutines, sample.heapAllocByte/1024, sample.heapSysByte/(1024*1024))
			break
		}
		highestClean = n
		t.Logf("RAMP rung sessions=%d result=clean wall=%s goroutines=%d heap_alloc_kib=%d heap_sys_mib=%d",
			n, elapsed.Round(time.Millisecond), sample.goroutines, sample.heapAllocByte/1024, sample.heapSysByte/(1024*1024))
	}

	if highestClean == 0 {
		t.Fatalf("no rung completed cleanly, not even the %d-session contract floor; the environment cannot establish a capacity baseline (see failing rung output above)", rampStartSessions)
	}
	if firstFailing == 0 {
		t.Logf("CEILING highest_fully_clean_sessions=%d (search capped at %d; ceiling is at least the cap)", highestClean, rampMaxSessions)
		return
	}
	t.Logf("CEILING highest_fully_clean_sessions=%d first_failing_rung=%d (failure signature in rung ramp-%03d output above)",
		highestClean, firstFailing, firstFailing)
}

// assertEverySessionCompletedScript enforces the completed-turn invariants for
// one clean rung: every session finished all three scripted turns, ended on
// LOOP.END, and leaked nothing to any other session.
func assertEverySessionCompletedScript(t *testing.T, sessionCount int, run *concurrentRun) {
	t.Helper()
	if len(run.States) != sessionCount {
		t.Fatalf("surviving sessions: got %d, want %d", len(run.States), sessionCount)
	}
	tokens := concurrentAllTokens(sessionCount)
	for _, state := range run.States {
		checkSessionIsolation(t, state.Token, tokens, state.Records, state.Deltas)
		if state.MessageEndCount != len(concurrentDefaultTurns) {
			t.Fatalf("session %s completed turns: got %d, want %d", state.Token, state.MessageEndCount, len(concurrentDefaultTurns))
		}
		last := state.Deltas[len(state.Deltas)-1]
		if last.Type != messages.StreamTypeLoopEnd {
			t.Fatalf("session %s lifecycle: last delta %q, want LOOP.END", state.Token, last.Type)
		}
	}
}
