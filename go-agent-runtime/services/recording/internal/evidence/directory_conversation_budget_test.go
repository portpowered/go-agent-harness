package evidence

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

func TestConversationSummarySnapshotReplacementReaccountsRetainedBytes(t *testing.T) {
	conversation := newEvidenceConversation()
	conversation.appendTranscript(true, "item-1", "partial", false)
	first := conversation.budget.bytes
	conversation.appendTranscript(true, "item-1", "corrected full transcript", true)
	second := conversation.budget.bytes
	if got := conversation.turn.inputText.String(); got != "corrected full transcript" {
		t.Fatalf("snapshot text = %q", got)
	}
	if second <= first {
		t.Fatalf("replacement did not charge larger snapshot: first=%d second=%d", first, second)
	}
	conversation.appendTranscript(true, "item-1", "ok", true)
	if got := conversation.turn.inputText.String(); got != "ok" {
		t.Fatalf("small replacement text = %q", got)
	}
	if got := conversation.budget.bytes; got >= second {
		t.Fatalf("replacement did not release old snapshot charge: second=%d final=%d", second, got)
	}
}

func TestConversationSummaryItemLimitStopsOnlyProjection(t *testing.T) {
	conversation := newEvidenceConversation()
	for index := 0; index < directorySummaryMaxItems+100; index++ {
		conversation.appendText(true, "x")
		if conversation.summaryFull {
			break
		}
	}
	if !conversation.summaryFull {
		t.Fatal("summary item limit was not latched")
	}
	if !errors.Is(conversation.summaryErr, errConversationSummaryItems) || !errors.Is(conversation.summaryErr, errConversationSummaryBudget) {
		t.Fatalf("summary overflow error = %v", conversation.summaryErr)
	}
	if conversation.budget.items > directorySummaryMaxItems || conversation.budget.bytes > directorySummaryMaxBytes {
		t.Fatalf("budget exceeded after rejection: %+v", conversation.budget)
	}
}

func TestConversationSummaryOverflowPublishesPartialAndContinuesRawEvidence(t *testing.T) {
	r := newEvidenceRecorder(t)
	send := func(direction session.LiveRecordDirection, message messages.StreamMessage) {
		t.Helper()
		if err := r.RecordMessage(t.Context(), session.LiveRecord{Direction: direction, Timestamp: evidenceTime(), Message: message}); err != nil {
			t.Fatal(err)
		}
	}

	send(session.LiveRecordClient, messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleUser, Value: messages.NewTextDeltaValue("before")})
	send(session.LiveRecordClient, messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleUser})
	send(session.LiveRecordAgent, messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, ResponseID: "response-before", Value: messages.NewTextDeltaValue("accepted")})
	send(session.LiveRecordAgent, messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, ResponseID: "response-before"})

	oversized := strings.Repeat("oversized-summary-", int(directorySummaryMaxBytes/6))
	send(session.LiveRecordAgent, messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, ResponseID: "response-overflow", Value: messages.NewTextDeltaValue(oversized)})
	send(session.LiveRecordAgent, messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, ResponseID: "response-after", Value: messages.NewTextDeltaValue("raw-tail")})
	if err := r.RecordAudio(t.Context(), session.LiveAudioRecord{Direction: session.LiveRecordAgent, Timestamp: evidenceTime(), Frame: sharedaudio.PCMFrame{Samples: []int16{11, -12, 13}, Format: sharedaudio.PCM16DeviceFormat(24000)}}); err != nil {
		t.Fatal(err)
	}
	recordEvidenceTerminal(t, r)
	err := r.Finalize(t.Context(), nil)
	if !errors.Is(err, errConversationSummaryBudget) || !errors.Is(err, io.ErrShortBuffer) {
		t.Fatalf("overflow finalization error = %v", err)
	}
	manifest := evidenceManifest(t, r)
	if manifest.RecordingStatus == nil || manifest.RecordingStatus.State != transcript.RecordingStatusPartial {
		t.Fatalf("overflow status = %+v", manifest.RecordingStatus)
	}
	var entries []evidenceLogEntry
	for _, line := range bytes.Split(bytes.TrimSpace(readEvidenceFile(t, r, "session-log.jsonl")), []byte{'\n'}) {
		var entry evidenceLogEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, entry)
	}
	if len(entries) < 1 || entries[0].Input.Text != "before" || entries[0].Response.Text != "accepted" {
		t.Fatalf("partial accepted summary = %+v", entries)
	}
	foundRawTail := false
	for _, line := range bytes.Split(readEvidenceFile(t, r, "agent.transcript.jsonl"), []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		record, err := transcript.Decode(line)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(record.Payload, []byte("raw-tail")) {
			foundRawTail = true
		}
	}
	if !foundRawTail {
		t.Fatal("raw transcript after summary overflow was lost")
	}
	if got := readEvidenceFile(t, r, filepath.Join("audio", "out-000.pcm")); len(got) != 6 {
		t.Fatalf("raw PCM after summary overflow length = %d", len(got))
	}
	if _, err := os.Stat(filepath.Join(r.destination, "manifest.json")); err != nil {
		t.Fatalf("partial bundle was not published: %v", err)
	}
}
