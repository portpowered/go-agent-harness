package openailive

import (
	"fmt"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func heldDelegation(id string, offsetMS int64) DelegationCreated {
	return DelegationCreated{OffsetMS: offsetMS, Delegation: DelegationInfo{ID: id, Target: DelegationClient}}
}

func reportedIDs(t *testing.T, msgs []messages.StreamMessage) []string {
	t.Helper()
	ids := make([]string, 0, len(msgs))
	for _, msg := range msgs {
		value, ok := msg.Value.(*messages.DelegationCreatedValue)
		if !ok {
			t.Fatalf("message %s carries %T, want a delegation", msg.Type, msg.Value)
		}
		ids = append(ids, value.ID)
	}
	return ids
}

// Held delegations fall due at their own settle deadlines, earliest first,
// and an empty user fragment still advances the transcript that covers them.
func TestDelegationTrackerReleasesHeldDelegationsByDeadlineAndCoverage(t *testing.T) {
	const settle = 400 * time.Millisecond
	tracker := newDelegationTracker(settle, DefaultSegmentGap)
	start := time.Unix(0, 0)
	if out := tracker.created(start, heldDelegation("late_offset", 5000)); out != nil {
		t.Fatalf("created = %v, want the delegation held", out)
	}
	if out := tracker.created(start.Add(100*time.Millisecond), heldDelegation("early_offset", 1000)); out != nil {
		t.Fatalf("created = %v, want the delegation held", out)
	}
	if next, held := tracker.nextDeadline(); !held || !next.Equal(start.Add(settle)) {
		t.Fatalf("nextDeadline = %v %t, want the first delegation's deadline", next, held)
	}

	// An empty fragment adds no text but proves the timeline reached 1200 ms.
	got := reportedIDs(t, tracker.transcript(messages.RoleUser, TranscriptDelta{StartMS: 900, EndMS: 1200}))
	if len(got) != 1 || got[0] != "early_offset" {
		t.Fatalf("released by coverage = %v, want early_offset", got)
	}
	if got := reportedIDs(t, tracker.due(start.Add(settle-time.Millisecond))); len(got) != 0 {
		t.Fatalf("released before the deadline = %v, want none", got)
	}
	if got := reportedIDs(t, tracker.due(start.Add(settle))); len(got) != 1 || got[0] != "late_offset" {
		t.Fatalf("released at the deadline = %v, want late_offset", got)
	}
	if _, held := tracker.nextDeadline(); held {
		t.Fatal("a delegation is still held after both were reported")
	}
}

// The transcript ring keeps the most recent spans and starts a new span when
// the speaker changes or a span would grow past its cap.
func TestDelegationTrackerRingKeepsRecentSpans(t *testing.T) {
	tracker := newDelegationTracker(time.Second, DefaultSegmentGap)
	for i := range transcriptRingSize + 3 {
		speaker := messages.RoleUser
		if i%2 == 1 {
			speaker = messages.RoleAssistant
		}
		at := int64(i) * 100
		tracker.transcript(speaker, TranscriptDelta{Delta: fmt.Sprintf("span %d", i), StartMS: at, EndMS: at + 50})
	}
	if len(tracker.ring) != transcriptRingSize || tracker.ring[0].Text != "span 3" {
		t.Fatalf("ring holds %d spans from %q, want the last %d", len(tracker.ring), tracker.ring[0].Text, transcriptRingSize)
	}
	long := make([]byte, maxSpanText)
	for i := range long {
		long[i] = 'a'
	}
	last := tracker.ring[len(tracker.ring)-1]
	tracker.transcript(last.Speaker, TranscriptDelta{Delta: string(long), StartMS: last.EndMS, EndMS: last.EndMS + 10})
	if got := tracker.ring[len(tracker.ring)-1]; got.Text != string(long) {
		t.Fatalf("an over-cap continuation joined the previous span (%d bytes)", len(got.Text))
	}
}
