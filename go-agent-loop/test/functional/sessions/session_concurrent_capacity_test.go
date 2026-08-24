package sessions

import (
	"fmt"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
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
