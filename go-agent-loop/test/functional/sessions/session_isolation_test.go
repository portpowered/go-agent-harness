package sessions

import (
	"bytes"
	"fmt"
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

// ---------------------------------------------------------------------------
// Isolation checker
// ---------------------------------------------------------------------------

// isolationFinding describes one detected cross-session contamination.
type isolationFinding struct {
	Owner        string // session whose capture was scanned
	ForeignToken string // another session's marker found in the capture
	Where        string // location of the offending record
	Snippet      string // printable snippet of the offending payload
}

func (f isolationFinding) String() string {
	return fmt.Sprintf("session %q capture contains foreign marker %q at %s (payload %q)", f.Owner, f.ForeignToken, f.Where, f.Snippet)
}

// containsSessionMarker reports whether payload contains token as a whole
// marker: an occurrence whose neighboring bytes are not themselves marker
// characters. Plain substring matching misfires at large session counts where
// one token is a prefix of another ("sess-10" inside "sess-100"), which would
// fabricate leakage findings during the ceiling ramp.
func containsSessionMarker(payload []byte, token string) bool {
	if len(token) == 0 {
		return false
	}
	isMarkerByte := func(b byte) bool {
		return b == '-' || b == '_' ||
			(b >= '0' && b <= '9') ||
			(b >= 'A' && b <= 'Z') ||
			(b >= 'a' && b <= 'z')
	}
	for start := 0; start+len(token) <= len(payload); start++ {
		if !bytes.Equal(payload[start:start+len(token)], []byte(token)) {
			continue
		}
		beforeOK := start == 0 || !isMarkerByte(payload[start-1])
		end := start + len(token)
		afterOK := end == len(payload) || !isMarkerByte(payload[end])
		if beforeOK && afterOK {
			return true
		}
	}
	return false
}

// checkRecordsIsolation scans one session's captured records for foreign
// session tokens.
func checkRecordsIsolation(ownerToken string, foreignTokens []string, records []transcript.Record) []isolationFinding {
	findings := []isolationFinding{}
	for idx, record := range records {
		for _, foreign := range foreignTokens {
			if foreign != ownerToken && containsSessionMarker(record.Payload, foreign) {
				findings = append(findings, isolationFinding{
					Owner:        ownerToken,
					ForeignToken: foreign,
					Where:        fmt.Sprintf("record[%d] peer=%s dir=%s stream=%s", idx, record.Peer, record.Direction, record.Stream),
					Snippet:      printableSnippet(record.Payload),
				})
			}
		}
	}
	return findings
}

// checkDeltasIsolation scans one session's collected delta stream. Values are
// reduced to comparable bytes through the same projection the capture sink
// uses, plus the marshaled message for structured values such as tool calls.
func checkDeltasIsolation(ownerToken string, foreignTokens []string, deltas []messages.StreamMessage) []isolationFinding {
	findings := []isolationFinding{}
	for idx, delta := range deltas {
		payload := streamPayload(delta)
		if len(payload) == 0 || bytes.Equal(payload, []byte(delta.Type)) {
			payload = marshalPayload(delta, []byte(delta.Type))
		}
		for _, foreign := range foreignTokens {
			if foreign != ownerToken && containsSessionMarker(payload, foreign) {
				findings = append(findings, isolationFinding{
					Owner:        ownerToken,
					ForeignToken: foreign,
					Where:        fmt.Sprintf("delta[%d] type=%s role=%s", idx, delta.Type, delta.Role),
					Snippet:      printableSnippet(payload),
				})
			}
		}
	}
	return findings
}

// checkSessionIsolation runs both projections for one session and fails t with
// named findings when any foreign marker appears.
func checkSessionIsolation(t *testing.T, ownerToken string, tokens []string, records []transcript.Record, deltas []messages.StreamMessage) {
	t.Helper()
	foreign := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if token != ownerToken {
			foreign = append(foreign, token)
		}
	}
	findings := checkRecordsIsolation(ownerToken, foreign, records)
	findings = append(findings, checkDeltasIsolation(ownerToken, foreign, deltas)...)
	if len(findings) != 0 {
		rendered := make([]string, 0, len(findings))
		for _, finding := range findings {
			rendered = append(rendered, finding.String())
		}
		t.Fatalf("cross-session leakage detected in session %q:\n%s", ownerToken, strings.Join(rendered, "\n"))
	}
}

func printableSnippet(payload []byte) string {
	const maxSnippet = 96
	end := len(payload)
	if end > maxSnippet {
		end = maxSnippet
	}
	snippet := bytes.Map(func(r rune) rune {
		if r >= 32 && r < 127 {
			return r
		}
		return '.'
	}, payload[:end])
	return string(snippet)
}
