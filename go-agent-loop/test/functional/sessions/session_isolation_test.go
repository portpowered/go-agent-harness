package sessions

import (
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
)

// TestConcurrentSessionsZeroCrossSessionLeakage runs the eight-session
// concurrent proof and asserts that every captured record and every delta of
// session k carries only session k's markers. It also verifies each session's
// end state equals its own script's expectation, not another session's.
func TestConcurrentSessionsZeroCrossSessionLeakage(t *testing.T) {
	run := runConcurrentSessions(t, concurrentDriverOptions{
		SessionCount: concurrentDefaultSessions,
		Turns:        concurrentDefaultTurns,
		CancelID:     -1,
	})

	tokens := concurrentAllTokens(concurrentDefaultSessions)
	for _, state := range run.States {
		checkSessionIsolation(t, state.Token, tokens, state.Records, state.Deltas)

		// End-state equals this session's own script, never another's.
		if want := len(concurrentDefaultTurns); state.MessageEndCount != want {
			t.Fatalf("session %s turn count: got %d, want %d", state.Token, state.MessageEndCount, want)
		}
		if len(state.ToolCalls) != 1 {
			t.Fatalf("session %s tool invocation tally: got %d, want 1", state.Token, len(state.ToolCalls))
		}
		call := state.ToolCalls[0]
		args := call.Arguments
		for _, other := range tokens {
			if other != state.Token && containsSessionMarker([]byte(args), other) {
				t.Fatalf("session %s tool call arguments contain foreign marker %q: %s", state.Token, other, args)
			}
		}
		last := state.Deltas[len(state.Deltas)-1]
		if last.Type != messages.StreamTypeLoopEnd {
			t.Fatalf("session %s close status: last delta %q, want LOOP.END", state.Token, last.Type)
		}
	}
}

// TestIsolationCheckerNamesLeakingSessionAndRecord feeds a deliberately
// contaminated capture through the isolation checker and requires the failure
// to name both the leaking session and the offending record. This pins the
// diagnostic contract the negative controls rely on; weakening the checker to
// ignore foreign markers fails this test.
func TestIsolationCheckerNamesLeakingSessionAndRecord(t *testing.T) {
	const ownerID, foreignID = 3, 7
	ownerToken := concurrentSessionToken(ownerID)
	foreignToken := concurrentSessionToken(foreignID)
	allTokens := []string{ownerToken, foreignToken}

	timestamp := time.Date(2026, time.August, 23, 9, 0, 0, 0, time.UTC)
	contaminatedRecords := []transcript.Record{
		transcript.NewRecord(11, timestamp, transcript.PeerAgent, transcript.DirectionIn, transcript.StreamRTCAudio, concurrentAudioFrame(ownerToken, 1)),
		transcript.NewRecord(12, timestamp, transcript.PeerClient, transcript.DirectionOut, transcript.StreamRTCAudio, concurrentAudioFrame(foreignToken, 2)),
	}

	findings := checkRecordsIsolation(ownerToken, allTokens, contaminatedRecords)
	if len(findings) != 1 {
		t.Fatalf("record isolation findings: got %d (%v), want exactly 1", len(findings), findings)
	}
	message := findings[0].String()
	for _, required := range []string{ownerToken, foreignToken, "record[1]", "peer=client"} {
		if !strings.Contains(message, required) {
			t.Fatalf("finding %q does not name %q", message, required)
		}
	}

	contaminatedDeltas := []messages.StreamMessage{
		{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("foreign " + foreignToken + " payload")},
	}
	deltaFindings := checkDeltasIsolation(ownerToken, allTokens, contaminatedDeltas)
	if len(deltaFindings) != 1 {
		t.Fatalf("delta isolation findings: got %d (%v), want exactly 1", len(deltaFindings), deltaFindings)
	}
	if !strings.Contains(deltaFindings[0].String(), ownerToken) || !strings.Contains(deltaFindings[0].String(), foreignToken) {
		t.Fatalf("delta finding %q does not name both sessions", deltaFindings[0].String())
	}

	clean := []transcript.Record{
		transcript.NewRecord(1, timestamp, transcript.PeerClient, transcript.DirectionOut, transcript.StreamRTCAudio, concurrentAudioFrame(ownerToken, 1)),
	}
	if findings := checkRecordsIsolation(ownerToken, allTokens, clean); len(findings) != 0 {
		t.Fatalf("clean capture reported findings: %v", findings)
	}

	// Marker matching is boundary-aware: a foreign token that appears only as
	// a substring of the owner's own longer token ("sess-10" inside
	// "sess-100") is not leakage. The ceiling ramp depends on this at session
	// counts >= 100.
	const rampOwnerID, rampPrefixID = 100, 10
	rampOwner := concurrentSessionToken(rampOwnerID)
	rampTokens := []string{concurrentSessionToken(rampPrefixID), rampOwner}
	rampRecords := []transcript.Record{
		transcript.NewRecord(1, timestamp, transcript.PeerClient, transcript.DirectionOut, transcript.StreamRTCAudio, concurrentAudioFrame(rampOwner, 1)),
	}
	if findings := checkRecordsIsolation(rampOwner, rampTokens, rampRecords); len(findings) != 0 {
		t.Fatalf("prefix-collision scan reported findings: %v", findings)
	}
}
