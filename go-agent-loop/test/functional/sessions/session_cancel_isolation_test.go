package sessions

import (
	"bytes"
	"fmt"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
)

// TestCancellingOneMidRunSessionLeavesOthersUndisturbed proves partial failure
// degrades per session only: with eight sessions mid-run, cancelling one part
// way through its script lets every surviving session complete its full script
// with captures identical in content and order to an uninterrupted reference
// run of the same scripts.
func TestCancellingOneMidRunSessionLeavesOthersUndisturbed(t *testing.T) {
	baselineGoroutines := runtime.NumGoroutine()

	reference := runConcurrentSessions(t, concurrentDriverOptions{
		SessionCount: concurrentDefaultSessions,
		Turns:        concurrentDefaultTurns,
		CancelID:     -1,
	})
	cancelled := runConcurrentSessions(t, concurrentDriverOptions{
		SessionCount: concurrentDefaultSessions,
		Turns:        concurrentDefaultTurns,
		CancelID:     0,
		CancelAfter:  1,
	})

	if cancelled.Cancelled == nil {
		t.Fatalf("cancel run produced no cancelled session state")
	}
	victim := *cancelled.Cancelled
	if victim.ID != 0 || !victim.Cancelled {
		t.Fatalf("cancelled state mislabeled: id=%d cancelled=%v", victim.ID, victim.Cancelled)
	}
	if len(victim.Records) == 0 {
		t.Fatalf("cancelled session %s lost its captured prefix", victim.Token)
	}
	if first := victim.Deltas[0]; first.Type != messages.StreamTypeSessionOpen {
		t.Fatalf("cancelled session %s first delta %q, want SESSION.OPEN", victim.Token, first.Type)
	}

	referenceByID := map[int]concurrentSessionState{}
	for _, state := range reference.States {
		referenceByID[state.ID] = state
	}
	survivorCount := 0
	for _, state := range cancelled.States {
		if !state.Cancelled && state.ID == 0 {
			t.Fatalf("cancelled session %s reappeared among survivors", state.Token)
		}
		want := referenceByID[state.ID]
		if diff := compareRecordBuckets(scriptRecordBuckets(want.Records), scriptRecordBuckets(state.Records)); diff != "" {
			t.Fatalf("survivor %s captured records diverge from reference: %s\nreference path tails:\n%s\ncancelled-run path tails:\n%s",
				state.Token, diff, renderPathTails(recordPathBuckets(want.Records)), renderPathTails(recordPathBuckets(state.Records)))
		}
		gotDeltas := scriptDeltaKeys(state.Deltas)
		wantDeltas := scriptDeltaKeys(want.Deltas)
		if fmt.Sprint(gotDeltas) != fmt.Sprint(wantDeltas) {
			t.Fatalf("survivor %s delta order diverges from reference:\n  expected: %v\n    actual: %v",
				state.Token, summarizeKeys(wantDeltas), summarizeKeys(gotDeltas))
		}
		// Both runs must still terminate through the full lifecycle even
		// though the post-script flush between the last scripted MESSAGE.END
		// and teardown is engine scheduling.
		for _, stateDeltas := range [][]messages.StreamMessage{state.Deltas, want.Deltas} {
			n := len(stateDeltas)
			if n < 2 || stateDeltas[n-2].Type != messages.StreamTypeSessionClose || stateDeltas[n-1].Type != messages.StreamTypeLoopEnd {
				t.Fatalf("survivor %s run did not terminate with SESSION.CLOSE + LOOP.END: last types %v", state.Token, deltaTypesTail(stateDeltas, 3))
			}
		}
		if state.MessageEndCount != want.MessageEndCount {
			t.Fatalf("survivor %s turn count diverges from reference: got %d, want %d", state.Token, state.MessageEndCount, want.MessageEndCount)
		}
		if fmt.Sprint(state.ToolCalls) != fmt.Sprint(want.ToolCalls) {
			t.Fatalf("survivor %s tool tally diverges from reference: got %v, want %v", state.Token, state.ToolCalls, want.ToolCalls)
		}
		survivorCount++
	}
	if survivorCount != concurrentDefaultSessions-1 {
		t.Fatalf("surviving sessions: got %d, want %d", survivorCount, concurrentDefaultSessions-1)
	}

	// S9 leak check: goroutine counts return to baseline within a settle
	// tolerance after both runs tear down. The settle loop runs outside any
	// tick generation, so it never substitutes for logical synchronization.
	const settleDeadline = 10 * time.Second
	const allowedResidual = 4
	deadline := time.Now().Add(settleDeadline)
	final := runtime.NumGoroutine()
	for final > baselineGoroutines+allowedResidual && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
		final = runtime.NumGoroutine()
	}
	if final > baselineGoroutines+allowedResidual {
		t.Fatalf("goroutines after cancellation runs: baseline=%d final=%d residual exceeds allowance %d", baselineGoroutines, final, allowedResidual)
	}
}

// ephemeralIDPattern matches infrastructure-generated identifiers minted
// fresh on every run: random stream IDs (tool-msg-<hex>, tool-call-<hex>)
// and per-loop GlobalIndex sequence numbers whose assignment order depends
// on goroutine scheduling around session startup. They are naming and
// bookkeeping, not conversation content, so both order-key projections
// normalize them before comparing a cancelled run against its reference.
var ephemeralIDPattern = regexp.MustCompile(`tool-(msg|call)-[0-9a-f]{6,}|"GlobalIndex":\d+|"LoopPassID":\d+`)

// stabilizePayload normalizes ephemeral identifiers in a raw payload.
func stabilizePayload(payload []byte) []byte {
	return ephemeralIDPattern.ReplaceAll(payload, []byte("NORMALIZED"))
}

// recordPathBuckets groups stabilized payload keys by capture path
// (peer/direction/stream). Within a single path, crossings are FIFO, so each
// bucket sequence must match the reference exactly. Relative order across
// different capture paths depends on goroutine scheduling and is therefore
// not compared.
func recordPathBuckets(records []transcript.Record) map[string][]string {
	buckets := map[string][]string{}
	for _, record := range records {
		path := fmt.Sprintf("%s/%s/%s", record.Peer, record.Direction, record.Stream)
		buckets[path] = append(buckets[path], fmt.Sprintf("%x", stabilizePayload(record.Payload)))
	}
	return buckets
}

// compareRecordBuckets requires both runs to have traversed identical paths
// with identical in-path sequences, naming the path and position of any
// divergence.
func compareRecordBuckets(want, got map[string][]string) string {
	for path, wantKeys := range want {
		gotKeys := got[path]
		if len(wantKeys) != len(gotKeys) {
			return fmt.Sprintf("path %s has %d crossings, reference has %d", path, len(gotKeys), len(wantKeys))
		}
		for i := range wantKeys {
			if wantKeys[i] != gotKeys[i] {
				return fmt.Sprintf("path %s diverges at [%d]:\n    expected: %s\n    actual:   %s",
					path, i, summarizeKey(wantKeys[i]), summarizeKey(gotKeys[i]))
			}
		}
	}
	for path := range got {
		if _, ok := want[path]; !ok {
			return fmt.Sprintf("unexpected capture path %s (%d crossings)", path, len(got[path]))
		}
	}
	return ""
}

// scriptRecordBuckets buckets only the script-deterministic prefix of a
// session's captures: everything up to and including the last assistant
// MESSAGE.END. Content past that point (the executed-tool-result message and
// the SESSION.CLOSE/LOOP.END lifecycle records) is produced by shutdown
// flushing whose landing relative to Stop is engine-internal scheduling, not
// conversation state; tool execution itself is asserted at the executor
// boundary via the tool-call tally below, and lifecycle completion via the
// delta-order comparison.
func scriptRecordBuckets(records []transcript.Record) map[string][]string {
	lastScript := -1
	for i, record := range records {
		if bytes.Contains(record.Payload, []byte(`"Role":"assistant"`)) && bytes.Contains(record.Payload, []byte(`"Type":"MESSAGE.END"`)) {
			lastScript = i
		}
	}
	if lastScript >= 0 {
		records = records[:lastScript+1]
	}
	return recordPathBuckets(records)
}

func summarizeKey(key string) string {
	const maxPayload = 640
	parts := strings.Split(key, "/")
	payload := parts[len(parts)-1]
	if len(payload) > maxPayload {
		payload = payload[:maxPayload] + "..."
	}
	return fmt.Sprintf("%s payload=%s", strings.Join(parts[:len(parts)-1], "/"), payload)
}

// renderPathTails renders the last few stabilized keys of every capture path
// so a bucket divergence shows both sides' actual sequences.
func renderPathTails(buckets map[string][]string) string {
	paths := make([]string, 0, len(buckets))
	for path := range buckets {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	var b strings.Builder
	for _, path := range paths {
		keys := buckets[path]
		fmt.Fprintf(&b, "  %s (%d crossings)\n", path, len(keys))
		start := len(keys) - 6
		if start < 0 {
			start = 0
		}
		for i := start; i < len(keys); i++ {
			fmt.Fprintf(&b, "    [%d] %s\n", i, summarizeKey(keys[i]))
		}
	}
	return b.String()
}

// deltaOrderKeys reduces deltas to their content-and-order identity.
func deltaOrderKeys(deltas []messages.StreamMessage) []string {
	keys := make([]string, 0, len(deltas))
	for _, delta := range deltas {
		payload := streamPayload(delta)
		if len(payload) == 0 || string(payload) == string(delta.Type) {
			payload = marshalPayload(delta, []byte(delta.Type))
		}
		normalized := stabilizePayload(payload)
		keys = append(keys, fmt.Sprintf("%s/%s/%x", delta.Type, delta.Role, normalized))
	}
	return keys
}

// scriptDeltaKeys reduces the script-deterministic prefix of a delta stream
// (through the last assistant MESSAGE.END) to order keys, applying the same
// rationale as scriptRecordBuckets.
func scriptDeltaKeys(deltas []messages.StreamMessage) []string {
	lastScript := -1
	for i, delta := range deltas {
		if delta.Type == messages.StreamTypeMessageEnd && delta.Role == messages.RoleAssistant {
			lastScript = i
		}
	}
	if lastScript >= 0 {
		deltas = deltas[:lastScript+1]
	}
	return deltaOrderKeys(deltas)
}

// deltaTypesTail renders the trailing delta types for failure messages.
func deltaTypesTail(deltas []messages.StreamMessage, n int) []messages.StreamMessageType {
	if len(deltas) > n {
		deltas = deltas[len(deltas)-n:]
	}
	types := make([]messages.StreamMessageType, 0, len(deltas))
	for _, delta := range deltas {
		types = append(types, delta.Type)
	}
	return types
}

// summarizeKeys renders at most the head of an ordered key slice for failure
// messages so divergence is readable without flooding the log.
func summarizeKeys(keys []string) string {
	const maxShown = 12
	if len(keys) <= maxShown {
		return fmt.Sprint(keys)
	}
	return fmt.Sprintf("%v ...(%d total)", keys[:maxShown], len(keys))
}
