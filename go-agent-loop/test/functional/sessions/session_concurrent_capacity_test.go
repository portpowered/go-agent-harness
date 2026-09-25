package sessions

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/test/functional/timeharness"
)

// TestConcurrentSessionsCompleteScriptedTurns drives eight independent
// agent-loop sessions concurrently, each over its own replay-backed mock
// transport and its own capture sink, all sharing exactly one deterministic
// clock. Every session completes its full three-turn script (text, audio,
// tool) with the correct lifecycle: SESSION.OPEN first, SESSION.CLOSE +
// LOOP.END last.
func TestConcurrentSessionsCompleteScriptedTurns(t *testing.T) {
	run := runConcurrentSessions(t, concurrentDriverOptions{
		SessionCount: concurrentDefaultSessions,
		Turns:        concurrentDefaultTurns,
		CancelID:     -1,
	})

	if len(run.States) != concurrentDefaultSessions {
		t.Fatalf("completed sessions: got %d, want %d", len(run.States), concurrentDefaultSessions)
	}

	for _, state := range run.States {
		state := state
		t.Run(state.Token, func(t *testing.T) {
			AssertSessionLifecycle(t, state.Deltas)

			if state.MessageEndCount != len(concurrentDefaultTurns) {
				t.Fatalf("session %s completed turns: got %d, want %d", state.Token, state.MessageEndCount, len(concurrentDefaultTurns))
			}
			assertAudioChunkSequence(t, state.Token, state.Deltas)
			if len(state.ToolCalls) != 1 {
				t.Fatalf("session %s tool invocation tally: got %d, want 1", state.Token, len(state.ToolCalls))
			}
			call := state.ToolCalls[0]
			if call.Name != concurrentToolName {
				t.Fatalf("session %s tool name: got %q, want %q", state.Token, call.Name, concurrentToolName)
			}
			if !containsSessionMarker([]byte(call.Arguments), state.Token) {
				t.Fatalf("session %s tool arguments do not carry its own marker: %s", state.Token, call.Arguments)
			}
		})
	}
}

// assertAudioChunkSequence verifies the agent-to-client audio chunks carry the
// session's frame sequence 1..N in order.
func assertAudioChunkSequence(t *testing.T, token string, deltas []messages.StreamMessage) {
	t.Helper()
	got := []int{}
	for _, delta := range deltas {
		if delta.Type != messages.StreamTypeAudioDelta {
			continue
		}
		payload := streamPayload(delta)
		if seq, ok := concurrentAudioSeq(payload); ok && containsSessionMarker(payload, token) {
			got = append(got, seq)
		}
	}
	want := make([]int, 0, concurrentAudioChunksPerTurn)
	for seq := 1; seq <= concurrentAudioChunksPerTurn; seq++ {
		want = append(want, seq)
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("session %s audio chunk sequence: got %v, want %v", token, got, want)
	}
}

// TestSessionScriptWalkerDetectsTurnCompletedDuringSend pins the lost-wakeup
// regression behind intermittent "concurrent run did not finish" hangs. The
// scripted provider response is injected straight into the provider receive
// buffer, so under load the engine can emit a turn's MESSAGE.END before the
// worker finishes sending that turn's client inputs. Here send completes the
// turn synchronously — the worst-case interleaving, forced deterministically.
// The walker must still credit every turn on the tick after its send tick; a
// walker that samples its completion baseline after send folds the completion
// into the baseline and never advances.
func TestSessionScriptWalkerDetectsTurnCompletedDuringSend(t *testing.T) {
	functionalTime := timeharness.New(time.Date(2026, time.August, 23, 9, 0, 0, 0, time.UTC), time.Millisecond)
	defer functionalTime.Close()
	participant, err := functionalTime.Register("walker")
	if err != nil {
		t.Fatalf("register walker: %v", err)
	}

	result := &concurrentSessionResult{ID: 0, Token: concurrentSessionToken(0)}
	plan := sessionScriptPlan(result, concurrentDefaultTurns)
	var completions atomic.Int64
	var completedAt []uint64
	ops := sessionScriptOps{
		token:       result.Token,
		open:        func() bool { return true },
		send:        func(concurrentTurnKind) { completions.Add(1) },
		completions: func() int { return int(completions.Load()) },
		completed:   func(tick uint64) { completedAt = append(completedAt, tick) },
		progress:    &result.progress,
	}

	finished := make(chan struct{})
	workerErrors := make(chan error, 1)
	participant.Run(func() {
		defer close(finished)
		walkSessionScript(participant, ops, plan, func() {}, func(err error) { workerErrors <- err })
	})

	// Each turn needs exactly its send tick plus one completion tick; a small
	// slack past the last send tick is ample for a correct walker.
	lastTick := plan[len(plan)-1].sendTick + 8
	for tick := uint64(concurrentOpenTick); tick <= lastTick; tick++ {
		if _, err := functionalTime.AdvanceTo(tick); err != nil {
			t.Fatalf("advance to logical tick %d: %v", tick, err)
		}
		if err := drainWorkerError(workerErrors); err != nil {
			t.Fatalf("logical tick %d: %v", tick, err)
		}
	}
	functionalTime.Close()
	<-finished

	if got := int(result.progress.nextStep.Load()); got != len(plan) {
		t.Fatalf("walker stalled on turn %d/%d (awaiting=%t baseline=%d completions=%d): a completion that landed during send was never credited",
			got+1, len(plan), result.progress.awaiting.Load(), result.progress.baseline.Load(), completions.Load())
	}
	if len(completedAt) != len(plan) {
		t.Fatalf("completed turns: got %d, want %d", len(completedAt), len(plan))
	}
	for index, step := range plan {
		if want := step.sendTick + 1; completedAt[index] != want {
			t.Fatalf("turn %d (%s) completed on tick %d, want %d (the tick after its send tick)", index, step.kind, completedAt[index], want)
		}
	}
}
