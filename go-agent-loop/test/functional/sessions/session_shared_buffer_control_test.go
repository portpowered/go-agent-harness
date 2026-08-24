package sessions

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/test/functional/timeharness"
)

// TestSharedCaptureBufferAliasingFailsIsolationCheck constructs the aliased
// configuration the negative control requires: two sessions deliberately share
// one capture sink/buffer, so both sessions' crossings interleave in a single
// collector exactly as they would if a multi-session runtime silently reused a
// buffer. Feeding that collector through the same isolation checker used by the
// green run must report contamination naming BOTH sessions.
//
// The control is deterministic and self-proving: if the checker were weakened
// to ignore shared-buffer contamination it would return no findings and this
// test would fail.
func TestSharedCaptureBufferAliasingFailsIsolationCheck(t *testing.T) {
	functionalTime := timeharness.New(time.Date(2026, time.August, 23, 10, 0, 0, 0, time.UTC), time.Millisecond)
	defer functionalTime.Close()
	sharedClock := functionalTime.Clock()

	const aliasedCount = 2
	// The deliberate defect: ONE collector handed to every session.
	aliasedCollector := NewSessionTranscript()
	trace := newConcurrentTrace()

	results := make([]*concurrentSessionResult, aliasedCount)
	for id := range results {
		token := concurrentSessionToken(id)
		sink := &tracedSink{session: id, token: token, collector: aliasedCollector, trace: trace}
		inf := NewMockSessionInferencer()
		tool := NewMockToolExecutor().AddResult(concurrentToolName, "result-for-"+token)
		scenario := NewSessionScenarioWithConfig(t, inf, tool, SessionScenarioOptions{
			Clock:   sharedClock,
			Capture: sink,
		}, agentloop.WithTickRate(concurrentEngineTickRate))
		results[id] = &concurrentSessionResult{
			ID:         id,
			Token:      token,
			Scenario:   scenario,
			Inferencer: inf,
			Tool:       tool,
			Collector:  sink.collector,
		}
		scenario.Start()
	}

	live := int64(aliasedCount)
	var workers sync.WaitGroup
	workers.Add(aliasedCount)
	workerErrors := make(chan error, aliasedCount)
	for _, result := range results {
		result := result
		participant, err := functionalTime.Register("aliased-" + result.Token)
		if err != nil {
			t.Fatalf("register %s: %v", result.Token, err)
		}
		participant.Run(func() {
			defer workers.Done()
			defer atomic.AddInt64(&live, -1)
			runSessionScript(participant, result, concurrentDefaultTurns, func() {}, func(err error) { workerErrors <- err })
		})
	}

	tick := uint64(concurrentOpenTick)
	deadline := time.Now().Add(concurrentRunBudget)
	for atomic.LoadInt64(&live) > 0 {
		if time.Now().After(deadline) {
			t.Fatalf("aliased run did not finish within %v", concurrentRunBudget)
		}
		if _, err := functionalTime.AdvanceTo(tick); err != nil {
			t.Fatalf("advance to logical tick %d: %v", tick, err)
		}
		tick++
		if err := drainWorkerError(workerErrors); err != nil {
			t.Fatalf("logical tick %d: %v", tick-1, err)
		}
	}
	workers.Wait()
	if err := drainWorkerError(workerErrors); err != nil {
		t.Fatalf("session worker: %v", err)
	}
	for _, result := range results {
		if err := result.Scenario.Stop(10 * time.Second); err != nil {
			t.Fatalf("stop session %s: %v", result.Token, err)
		}
	}

	// The shared buffer now holds crossings of both sessions. Scanning the one
	// merged capture under each owner must report contamination in both
	// directions, naming both sessions and the offending records.
	tokens := concurrentAllTokens(aliasedCount)
	merged := aliasedCollector.Records()
	findingsA := checkRecordsIsolation(results[0].Token, tokens, merged)
	findingsB := checkRecordsIsolation(results[1].Token, tokens, merged)
	assertFindingPair(t, findingsA, findingsB, results[0].Token, results[1].Token)

	// The positive direction still holds: an isolated run passes the same
	// checker unchanged. This is the control's proof that the failure above is
	// caused by sharing, not by a checker that fails everything.
	isolated := runConcurrentSessions(t, concurrentDriverOptions{
		SessionCount: aliasedCount,
		Turns:        concurrentDefaultTurns,
		CancelID:     -1,
	})
	for _, state := range isolated.States {
		checkSessionIsolation(t, state.Token, tokens, state.Records, state.Deltas)
	}
}

// assertFindingPair requires the aliased scans to name both sessions in both
// directions with concrete record locations.
func assertFindingPair(t *testing.T, findingsA, findingsB []isolationFinding, tokenA, tokenB string) {
	t.Helper()
	if len(findingsA) == 0 || len(findingsB) == 0 {
		t.Fatalf("isolation checker missed the aliased buffer: findings(A)=%d findings(B)=%d", len(findingsA), len(findingsB))
	}
	messageA := renderFindings(findingsA)
	messageB := renderFindings(findingsB)
	if !strings.Contains(messageA, tokenB) {
		t.Fatalf("scan of %q did not name contaminating session %q:\n%s", tokenA, tokenB, messageA)
	}
	if !strings.Contains(messageB, tokenA) {
		t.Fatalf("scan of %q did not name contaminating session %q:\n%s", tokenB, tokenA, messageB)
	}
	if !strings.Contains(messageA, "record[") {
		t.Fatalf("findings do not identify offending records:\n%s", messageA)
	}
}

func renderFindings(findings []isolationFinding) string {
	rendered := make([]string, 0, len(findings))
	for _, finding := range findings {
		rendered = append(rendered, finding.String())
	}
	return strings.Join(rendered, "\n")
}
