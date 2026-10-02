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
		Turns:        concurrentDefaultTurns(),
		CancelID:     -1,
	})

	if len(run.States) != concurrentDefaultSessions {
		t.Fatalf("completed sessions: got %d, want %d", len(run.States), concurrentDefaultSessions)
	}

	for _, state := range run.States {

		t.Run(state.Token, func(t *testing.T) {
			AssertSessionLifecycle(t, state.Deltas)

			if state.MessageEndCount != len(concurrentDefaultTurns()) {
				t.Fatalf("session %s completed turns: got %d, want %d", state.Token, state.MessageEndCount, len(concurrentDefaultTurns()))
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
// worker returns from queueing it. Here the provider receives each input at
// once and respond completes the turn synchronously — the worst-case
// interleaving, forced deterministically. The walker must still credit every
// turn on the tick after its send tick; a walker that samples its completion
// baseline after respond folds the completion into the baseline and never
// advances.
func TestSessionScriptWalkerDetectsTurnCompletedDuringSend(t *testing.T) {
	var received, completions atomic.Int64
	walk := walkScriptedSession(t, sessionScriptOps{
		send:        func(kind concurrentTurnKind) { received.Add(int64(providerInputFrames(kind))) },
		received:    func(concurrentTurnKind) int { return int(received.Load()) },
		respond:     func(concurrentTurnKind) { completions.Add(1) },
		completions: func() int { return int(completions.Load()) },
	})

	if walk.stalledOn >= 0 {
		t.Fatalf("walker stalled on turn %d/%d (awaiting=%t baseline=%d completions=%d): a completion that landed during respond was never credited",
			walk.stalledOn+1, len(walk.plan), walk.progress.awaiting.Load(), walk.progress.baseline.Load(), completions.Load())
	}
	for index, step := range walk.plan {
		if want := step.sendTick + 1; walk.completedAt[index] != want {
			t.Fatalf("turn %d (%s) completed on tick %d, want %d (the tick after its send tick)", index, step.kind, walk.completedAt[index], want)
		}
	}
}

// TestSessionScriptWalkerWithholdsResponseUntilProviderReceivedInput pins the
// cause of the dropped tool calls behind "tool invocation tally: got 0". The
// user's text reaches the engine through the user runner, independently of
// the provider receive buffer. A response queued alongside the request can
// therefore reach the engine first, and the late user text then starts a new
// turn that discards the in-flight response's tool call. The walker must hold
// every scripted response until the provider received the whole turn input,
// however many ticks delivery takes.
func TestSessionScriptWalkerWithholdsResponseUntilProviderReceivedInput(t *testing.T) {
	const deliveryLagTicks = 5
	var received, completions atomic.Int64
	var pending concurrentTurnKind
	var polls int
	awaitingDelivery := false
	walk := walkScriptedSession(t, sessionScriptOps{
		send: func(kind concurrentTurnKind) {
			pending, polls, awaitingDelivery = kind, 0, true
		},
		// The provider receives the turn's input on the walker's
		// deliveryLagTicks-th poll after send; each tick polls once.
		received: func(concurrentTurnKind) int {
			if awaitingDelivery {
				if polls++; polls > deliveryLagTicks {
					received.Add(int64(providerInputFrames(pending)))
					awaitingDelivery = false
				}
			}
			return int(received.Load())
		},
		respond: func(kind concurrentTurnKind) {
			if awaitingDelivery {
				t.Errorf("%s response queued before the provider received the turn's input", kind)
			}
			completions.Add(1)
		},
		completions: func() int { return int(completions.Load()) },
	})

	if walk.stalledOn >= 0 {
		t.Fatalf("walker stalled on turn %d/%d (awaiting=%t responded=%t)", walk.stalledOn+1, len(walk.plan), walk.progress.awaiting.Load(), walk.progress.responded.Load())
	}
	for index, step := range walk.plan {
		if want := step.sendTick + deliveryLagTicks + 1; walk.completedAt[index] != want {
			t.Fatalf("turn %d (%s) completed on tick %d, want %d (the tick after delivery)", index, step.kind, walk.completedAt[index], want)
		}
	}
}

// scriptedWalk is the outcome of driving one script walker to completion.
type scriptedWalk struct {
	plan        []sessionScriptStep
	completedAt []uint64
	progress    *scriptProgress
	stalledOn   int // index of the turn the walker never finished, or -1
}

// walkScriptedSession drives one walker over the default script with ops,
// filling in the session token, open, completed and progress hooks, for a
// bounded number of logical ticks past the last send tick.
func walkScriptedSession(t *testing.T, ops sessionScriptOps) scriptedWalk {
	t.Helper()
	functionalTime := timeharness.New(time.Date(2026, time.August, 23, 9, 0, 0, 0, time.UTC), time.Millisecond)
	defer functionalTime.Close()
	participant, err := functionalTime.Register("walker")
	if err != nil {
		t.Fatalf("register walker: %v", err)
	}

	result := &concurrentSessionResult{ID: 0, Token: concurrentSessionToken(0)}
	plan := sessionScriptPlan(result, concurrentDefaultTurns())
	var completedAt []uint64
	ops.token = result.Token
	ops.open = func() bool { return true }
	ops.completed = func(tick uint64) { completedAt = append(completedAt, tick) }
	ops.progress = &result.progress

	finished := make(chan struct{})
	workerErrors := make(chan error, 1)
	participant.Run(func() {
		defer close(finished)
		walkSessionScript(participant, ops, plan, func() {}, func(err error) { workerErrors <- err })
	})

	// A correct walker needs at most a few ticks per turn past its send
	// tick; this slack is ample.
	lastTick := plan[len(plan)-1].sendTick + 16
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

	walk := scriptedWalk{plan: plan, completedAt: completedAt, progress: &result.progress, stalledOn: -1}
	if next := int(result.progress.nextStep.Load()); next != len(plan) || len(completedAt) != len(plan) {
		walk.stalledOn = next
	}
	return walk
}
