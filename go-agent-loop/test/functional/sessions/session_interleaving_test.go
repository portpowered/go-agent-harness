package sessions

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
)

// TestConcurrentSessionsPerEventOrderingUnderInterleaving verifies that
// interleaved, tick-driven scheduling never reorders one session's own
// conversation: audio chunks carry ascending sequence numbers, text and
// transcript deltas follow script order, and each tool call precedes its tool
// result. It also asserts the interleaving is real — client crossings from
// different sessions alternate in the global trace.
func TestConcurrentSessionsPerEventOrderingUnderInterleaving(t *testing.T) {
	run := runConcurrentSessions(t, concurrentDriverOptions{
		SessionCount: concurrentDefaultSessions,
		Turns:        concurrentDefaultTurns,
		CancelID:     -1,
	})

	for _, state := range run.States {
		assertSessionScriptOrder(t, state)
	}
	assertClientCrossingsInterleave(t, run.Trace)
}

// assertSessionScriptOrder compares one session's captured event order against
// its script. Any violation names the session and shows expected vs actual.
func assertSessionScriptOrder(t *testing.T, state concurrentSessionState) {
	t.Helper()
	token := state.Token

	// Agent-to-client deltas must follow the scripted order:
	//   ack text -> audio frames seq 1..N -> heard transcript ->
	//   TOOLCALL.START -> TOOLCALL.END.
	// The script ends each turn at its model MESSAGE.END; executed-tool-result
	// delivery beyond that boundary is asynchronous engine plumbing asserted
	// at the executor boundary in the capacity test.
	var gotOrder []string
	for _, delta := range state.Deltas {
		payload := streamPayload(delta)
		switch {
		case delta.Type == messages.StreamTypeTextDelta && delta.Role == messages.RoleAssistant:
			gotOrder = append(gotOrder, "text-ack")
		case delta.Type == messages.StreamTypeAudioDelta:
			if seq, ok := concurrentAudioSeq(payload); ok {
				gotOrder = append(gotOrder, fmt.Sprintf("audio-%d", seq))
			}
		case delta.Type == messages.StreamTypeTranscriptDelta:
			gotOrder = append(gotOrder, "transcript")
		case delta.Type == messages.StreamTypeToolCallStart:
			gotOrder = append(gotOrder, "tool-start")
		case delta.Type == messages.StreamTypeToolCallEnd:
			gotOrder = append(gotOrder, "tool-end")
		case delta.Type == messages.StreamTypeTextDelta && delta.Role == messages.RoleTool:
			gotOrder = append(gotOrder, "tool-result") // tolerated, not required
		}
	}

	wantOrder := []string{"text-ack"}
	for seq := 1; seq <= concurrentAudioChunksPerTurn; seq++ {
		wantOrder = append(wantOrder, fmt.Sprintf("audio-%d", seq))
	}
	wantOrder = append(wantOrder,
		"transcript",
		"tool-start",
		"tool-end",
	)

	// A trailing tool-result delta is welcome but optional; strip it so the
	// comparison stays on the scripted prefix.
	if n := len(gotOrder); n > 0 && gotOrder[n-1] == "tool-result" {
		gotOrder = gotOrder[:n-1]
	}

	if fmt.Sprint(gotOrder) != fmt.Sprint(wantOrder) {
		t.Fatalf("session %s event ordering violated:\n  expected: %v\n    actual: %v", token, wantOrder, gotOrder)
	}

	// Client-to-provider crossings must follow the script too:
	//   request text -> three sequenced audio frames -> tool trigger text.
	client := []string{}
	for _, record := range state.Records {
		if record.Peer != transcript.PeerClient || record.Direction != transcript.DirectionOut {
			continue
		}
		switch {
		case record.Stream == transcript.StreamRTCAudio:
			if seq, ok := concurrentAudioSeq(record.Payload); ok {
				client = append(client, fmt.Sprintf("audio-%d", seq))
			}
		case bytes.Contains(record.Payload, []byte("requests a text answer")):
			client = append(client, "text-request")
		case bytes.Contains(record.Payload, []byte(concurrentToolName)):
			client = append(client, "tool-trigger")
		}
	}
	wantClient := []string{"text-request"}
	for seq := 1; seq <= concurrentAudioChunksPerTurn; seq++ {
		wantClient = append(wantClient, fmt.Sprintf("audio-%d", seq))
	}
	wantClient = append(wantClient, "tool-trigger")
	if fmt.Sprint(client) != fmt.Sprint(wantClient) {
		t.Fatalf("session %s client crossing order violated:\n  expected: %v\n    actual: %v", token, wantClient, client)
	}
}

// assertClientCrossingsInterleave proves the eight sessions share one global
// trace and that no session monopolizes its ends. Client-input capture is
// asynchronous (the engine drains the audio inbox on its own goroutine), so
// per-entry tick cohorts are not stable; presence and block structure are.
// A scheduling collapse onto one session would put that session's crossings
// at both ends of the trace and fail the boundary checks below.
func assertClientCrossingsInterleave(t *testing.T, trace *concurrentTrace) {
	t.Helper()
	entries := trace.clientEntries()
	seen := map[int]int{}
	transitions := 0
	prev := -1
	for index, entry := range entries {
		seen[entry.Session]++
		if index > 0 && entry.Session != prev {
			transitions++
		}
		prev = entry.Session
	}
	if len(seen) != concurrentDefaultSessions {
		t.Fatalf("global trace spans %d sessions, want %d (observed counts %v)", len(seen), concurrentDefaultSessions, seen)
	}
	const minCrossingsPerSession = 3 // one per turn kind
	for session, count := range seen {
		if count < minCrossingsPerSession {
			t.Fatalf("session %d contributed %d client crossings, want >= %d", session, count, minCrossingsPerSession)
		}
	}

	// A scheduling collapse onto one serialized session produces a trace of
	// per-session blocks and exactly sessions-1 transitions. Real staggered
	// scheduling crosses session boundaries at least once per session.
	if transitions < concurrentDefaultSessions {
		t.Fatalf("global trace shows only %d session transitions across %d entries; sessions are not interleaving (counts %v)",
			transitions, len(entries), seen)
	}
}
