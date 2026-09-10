package evidence

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

type pairedPrefixFailureCase struct {
	name    string
	audio   bool
	failAt  int
	failure func(*os.File, []byte) error
}

func TestDirectoryRecorderRollsBackPairedEvidenceOnAgentWriteFailure(t *testing.T) {
	cases := []pairedPrefixFailureCase{
		{name: "partial transcript", failAt: 4, failure: partialSpoolFailure},
		{name: "zero-byte transcript", failAt: 4, failure: zeroByteSpoolFailure},
		{name: "offset transcript", failAt: 4, failure: noOffsetSpoolFailure},
		{name: "zero-byte audio", audio: true, failAt: 5, failure: zeroByteSpoolFailure},
		{name: "offset audio", audio: true, failAt: 5, failure: noOffsetSpoolFailure},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { runPairedPrefixFailure(t, tc) })
	}
}

func runPairedPrefixFailure(t *testing.T, tc pairedPrefixFailureCase) {
	t.Helper()
	r := newEvidenceRecorder(t)
	r.writeSpool = failingSpoolWriter(tc)
	recordEvidenceText(t, r, "first complete")
	if tc.audio {
		recordFailedAudio(t, r)
	} else {
		recordEvidenceText(t, r, "second failed")
	}
	recordEvidenceTerminal(t, r)
	if err := r.Finalize(t.Context(), nil); err == nil {
		t.Fatal("agent write failure reported complete")
	}
	assertPairedTranscriptPrefix(t, r)
	if tc.audio {
		if _, err := os.Stat(filepath.Join(r.destination, "audio", "out-000.pcm")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("failed audio retained bytes: %v", err)
		}
	}
}

func failingSpoolWriter(tc pairedPrefixFailureCase) func(*os.File, []byte) error {
	writes := 0
	return func(file *os.File, data []byte) error {
		writes++
		if writes == tc.failAt {
			return tc.failure(file, data)
		}
		return writeAll(file, data)
	}
}

func partialSpoolFailure(file *os.File, data []byte) error {
	n := len(data) / 2
	if n == 0 {
		n = 1
	}
	if _, err := file.Write(data[:n]); err != nil {
		return err
	}
	return io.ErrShortWrite
}

func zeroByteSpoolFailure(*os.File, []byte) error { return errors.New("fixture zero-byte agent write") }

func noOffsetSpoolFailure(*os.File, []byte) error { return nil }

func recordFailedAudio(t *testing.T, r *directoryRecorder) {
	t.Helper()
	frame := audio.PCMFrame{Samples: []int16{11, -12}, Format: audio.PCM16DeviceFormat(24000)}
	if err := r.RecordAudio(t.Context(), session.LiveAudioRecord{Direction: session.LiveRecordAgent, Timestamp: evidenceTime(), Frame: frame}); err != nil {
		t.Fatal(err)
	}
}

func assertPairedTranscriptPrefix(t *testing.T, r *directoryRecorder) {
	t.Helper()
	for _, name := range []string{"client.transcript.jsonl", "agent.transcript.jsonl"} {
		lines := bytes.Split(bytes.TrimSpace(readEvidenceFile(t, r, name)), []byte{'\n'})
		if len(lines) != 1 {
			t.Fatalf("%s lines = %d, want one paired prefix", name, len(lines))
		}
		if _, err := transcript.Decode(lines[0]); err != nil {
			t.Fatalf("%s retained invalid JSONL prefix: %v", name, err)
		}
	}
}

// Both streams preserve their own order, but either may run ahead. Enumerate
// every interleaving of two media frames with four normalized response events.
func TestRecordedResponseAudioIsIndependentOfQueueScheduling(t *testing.T) {
	for first := 0; first <= 4; first++ {
		for second := first; second <= 4; second++ {
			t.Run(fmt.Sprintf("audio-at-%d-%d", first, second), func(t *testing.T) { assertRecordedAudioInterleaving(t, first, second) })
		}
	}
}

func assertRecordedAudioInterleaving(t *testing.T, first, second int) {
	t.Helper()
	r := newEvidenceRecorder(t)
	for position := 0; position <= 4; position++ {
		for index, at := range []int{first, second} {
			if at == position {
				recordResponsePCM(t, r, index)
			}
		}
		if position < 4 {
			recordResponseMessage(t, r, position)
		}
	}
	recordEvidenceTerminal(t, r)
	if err := r.Finalize(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	assertResponseAudioIndex(t, r)
}

func recordResponsePCM(t *testing.T, r *directoryRecorder, index int) {
	t.Helper()
	frame := audio.PCMFrame{Samples: []int16{int16(index + 1)}, Format: audio.PCM16DeviceFormat(24000), EndOfResponse: true, PlaybackResponse: audio.PlaybackResponse{ResponseID: fmt.Sprintf("response-%d", index)}}
	if err := r.RecordAudio(t.Context(), session.LiveAudioRecord{Direction: session.LiveRecordAgent, Timestamp: evidenceTime(), Frame: frame}); err != nil {
		t.Fatal(err)
	}
}

func recordResponseMessage(t *testing.T, r *directoryRecorder, position int) {
	t.Helper()
	msg := messages.StreamMessage{Role: messages.RoleAssistant, ResponseID: fmt.Sprintf("response-%d", position/2), Type: messages.StreamTypeMessageEnd}
	if position%2 == 0 {
		msg.Type = messages.StreamTypeTextDelta
		msg.Value = messages.NewTextDeltaValue(fmt.Sprintf("reply-%d", position/2))
	}
	if err := r.RecordMessage(t.Context(), session.LiveRecord{Direction: session.LiveRecordAgent, Timestamp: evidenceTime(), Message: msg}); err != nil {
		t.Fatal(err)
	}
}

func assertResponseAudioIndex(t *testing.T, r *directoryRecorder) {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(string(readEvidenceFile(t, r, "session-log.jsonl"))), "\n")
	if len(lines) != 2 {
		t.Fatalf("recorded turns = %d, want 2: %s", len(lines), lines)
	}
	for index, line := range lines {
		var entry evidenceLogEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatal(err)
		}
		if !entry.Response.Complete || entry.Response.Text != fmt.Sprintf("reply-%d", index) || entry.Response.AudioBytes != 2 || entry.Response.AudioOffsetBytes != uint64(index*2) {
			t.Fatalf("response %d lost its audio association: %+v", index, entry.Response)
		}
	}
}

const testProviderCaptureAvailable = "available"

func TestEvidenceResourceLimitsNormalizeProtectedDefaults(t *testing.T) {
	smaller := recording.ResourceLimits{TranscriptBytes: 1, TranscriptItems: 1, AudioBytes: 1, AudioItems: 1, SidecarBytes: 1, SidecarItems: 1, MetadataBytes: 1, MetadataItems: 1, TerminalBytes: 1, TerminalItems: 1, ProviderBytes: 1, ProviderItems: 1}
	budget, err := newEvidenceResourceBudget(smaller)
	if err != nil || budget.limits != smaller {
		t.Fatalf("smaller limits: budget=%+v err=%v", budget.limits, err)
	}
	larger := recording.ResourceLimits{TranscriptBytes: recording.DefaultTranscriptBytes + 1, TranscriptItems: recording.DefaultTranscriptItems + 1, AudioBytes: recording.DefaultAudioBytes + 1, AudioItems: recording.DefaultAudioItems + 1, SidecarBytes: recording.DefaultSidecarBytes + 1, SidecarItems: recording.DefaultSidecarItems + 1, MetadataBytes: recording.DefaultMetadataBytes + 1, MetadataItems: recording.DefaultMetadataItems + 1, TerminalBytes: recording.DefaultTerminalBytes + 1, TerminalItems: recording.DefaultTerminalItems + 1, ProviderBytes: recording.DefaultProviderBytes + 1, ProviderItems: recording.DefaultProviderItems + 1}
	budget, err = newEvidenceResourceBudget(larger)
	if err != nil || budget.limits.TranscriptBytes != recording.DefaultTranscriptBytes || budget.limits.ProviderItems != recording.DefaultProviderItems {
		t.Fatalf("larger limits: budget=%+v err=%v", budget.limits, err)
	}
	if _, err := newEvidenceResourceBudget(recording.ResourceLimits{TranscriptBytes: -1}); err == nil {
		t.Fatal("negative protected limit was accepted")
	}
}

func TestMinimalRecordingConfigRetainsProviderMarkerAndBoundedTerminal(t *testing.T) {
	config := transcript.RecordingConfig{SessionLog: []byte(strings.Repeat("session-log ", 1024)), Metadata: transcript.RecordingMetadata{Transport: "runtime", Configuration: map[string]string{"provider_capture": testProviderCaptureAvailable, "oversized": strings.Repeat("metadata ", 1024)}}, Terminal: &transcript.RecordingTerminalSummary{Reason: strings.Repeat("reason ", 1024), Classification: strings.Repeat("classification ", 1024), TerminalReason: messages.TerminalReason(strings.Repeat("terminal ", 1024)), TerminalProvenance: messages.TerminalProvenance(strings.Repeat("provenance ", 1024)), OutputState: messages.TerminalOutputState(strings.Repeat("output ", 1024))}}
	minimal := minimalRecordingConfig(config, nil)
	if minimal.Metadata.Transport != "runtime" || minimal.Metadata.Configuration["provider_capture"] != testProviderCaptureAvailable || len(minimal.SessionLog) != 0 || minimal.Corpus != nil {
		t.Fatalf("minimal metadata lost useful bounds/marker: %+v", minimal)
	}
	if minimal.RecordingStatus == nil || minimal.RecordingStatus.State != transcript.RecordingStatusPartial || minimal.Terminal == nil || len(minimal.Terminal.Reason) > recordingTerminalFallbackFieldLimit || len(minimal.Terminal.Classification) > recordingTerminalFallbackFieldLimit {
		t.Fatalf("minimal metadata status/terminal = %+v/%+v", minimal.RecordingStatus, minimal.Terminal)
	}
}

func TestPrepareBundleConfigFallsBackToMinimalMetadata(t *testing.T) {
	r := &directoryRecorder{}
	config := transcript.RecordingConfig{ClientTranscriptPath: "client.transcript.jsonl", AgentTranscriptPath: "agent.transcript.jsonl", Metadata: transcript.RecordingMetadata{Transport: "runtime", Model: strings.Repeat("model ", 2048), Configuration: map[string]string{"provider_capture": testProviderCaptureAvailable, "oversized": strings.Repeat("metadata ", 2048)}}, Terminal: &transcript.RecordingTerminalSummary{Reason: "complete", Classification: "complete", TerminalReason: messages.TerminalReasonProviderAuthoredCompletion, TerminalProvenance: messages.TerminalProvenanceProvider, OutputState: messages.TerminalOutputComplete}}
	config = boundedRecordingConfig(config, nil)
	result := errors.New("manifest metadata admission failed")
	config.RecordingStatus = recordingStatusForError(result, nil)
	fullBytes, err := recordingManifestBytes(config)
	if err != nil {
		t.Fatal(err)
	}
	fallbackBytes, err := recordingManifestBytes(minimalRecordingConfig(config, nil))
	if err != nil || fullBytes <= fallbackBytes {
		t.Fatalf("fallback bytes: full=%d fallback=%d err=%v", fullBytes, fallbackBytes, err)
	}
	r.budget = evidenceResourceBudget{limits: recording.ResourceLimits{MetadataBytes: fallbackBytes, MetadataItems: 1}}
	got, metadataErr, publish := r.prepareBundleConfig(config, result)
	if !publish || metadataErr == nil || got.Metadata.Configuration["provider_capture"] != testProviderCaptureAvailable || got.RecordingStatus == nil || got.RecordingStatus.State != transcript.RecordingStatusPartial {
		t.Fatalf("metadata fallback: publish=%v err=%v config=%+v", publish, metadataErr, got)
	}
}

func TestRecordingManifestCountsConfiguredArtifactSources(t *testing.T) {
	config := transcript.RecordingConfig{ManifestVersion: 2, ClientTranscript: []byte("client"), AgentTranscriptPath: "agent.transcript.jsonl", SessionLog: []byte("log"), InputSegments: [][]byte{{1}}, OutputSegmentPaths: []string{"output.pcm"}, BrowserArtifact: &transcript.BrowserArtifact{Format: transcript.BrowserEventsVersion}, AdditionalArtifacts: []transcript.RecordingArtifact{{Path: "extra.json"}}}
	manifestBytes, err := recordingManifestBytes(config)
	if err != nil || manifestBytes <= 0 {
		t.Fatalf("manifest bytes = %d, err=%v", manifestBytes, err)
	}
	paths := recordingManifestArtifactPlaceholders(config)
	paths = append(paths, recordingManifestArtifactPlaceholders(transcript.RecordingConfig{InputSegmentPaths: []string{"input.pcm"}, OutputSegments: [][]byte{{2}}})...)
	for _, want := range []string{"client.transcript.jsonl", "agent.transcript.jsonl", "session-log.jsonl", "audio/in-000.pcm", "audio/out-000.pcm", transcript.BrowserArtifactDefaultPath, "extra.json"} {
		found := false
		for _, artifact := range paths {
			if artifact.Path == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("manifest artifact placeholders missing %q: %+v", want, paths)
		}
	}
}

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
	recordOverflowAcceptedPrefix(t, r)
	recordOverflowText(t, r)
	recordOverflowAudioAndTerminal(t, r)
	assertOverflowFinalize(t, r)
	assertOverflowSummary(t, r)
	assertOverflowRawTail(t, r)
	assertOverflowBundle(t, r)
}

func recordOverflowAcceptedPrefix(t *testing.T, r *directoryRecorder) {
	t.Helper()
	recordSummaryMessage(t, r, session.LiveRecordClient, messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleUser, Value: messages.NewTextDeltaValue("before")})
	recordSummaryMessage(t, r, session.LiveRecordClient, messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleUser})
	recordSummaryMessage(t, r, session.LiveRecordAgent, messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, ResponseID: "response-before", Value: messages.NewTextDeltaValue("accepted")})
	recordSummaryMessage(t, r, session.LiveRecordAgent, messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, ResponseID: "response-before"})
}

func recordOverflowText(t *testing.T, r *directoryRecorder) {
	t.Helper()
	oversized := strings.Repeat("oversized-summary-", int(directorySummaryMaxBytes/6))
	recordSummaryMessage(t, r, session.LiveRecordAgent, messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, ResponseID: "response-overflow", Value: messages.NewTextDeltaValue(oversized)})
	recordSummaryMessage(t, r, session.LiveRecordAgent, messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, ResponseID: "response-after", Value: messages.NewTextDeltaValue("raw-tail")})
}

func recordSummaryMessage(t *testing.T, r *directoryRecorder, direction session.LiveRecordDirection, message messages.StreamMessage) {
	t.Helper()
	if err := r.RecordMessage(t.Context(), session.LiveRecord{Direction: direction, Timestamp: evidenceTime(), Message: message}); err != nil {
		t.Fatal(err)
	}
}

func recordOverflowAudioAndTerminal(t *testing.T, r *directoryRecorder) {
	t.Helper()
	if err := r.RecordAudio(t.Context(), session.LiveAudioRecord{Direction: session.LiveRecordAgent, Timestamp: evidenceTime(), Frame: audio.PCMFrame{Samples: []int16{11, -12, 13}, Format: audio.PCM16DeviceFormat(24000)}}); err != nil {
		t.Fatal(err)
	}
	recordEvidenceTerminal(t, r)
}

func assertOverflowFinalize(t *testing.T, r *directoryRecorder) {
	t.Helper()
	err := r.Finalize(t.Context(), nil)
	if !errors.Is(err, errConversationSummaryBudget) || !errors.Is(err, io.ErrShortBuffer) {
		t.Fatalf("overflow finalization error = %v", err)
	}
}

func assertOverflowSummary(t *testing.T, r *directoryRecorder) {
	t.Helper()
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
}

func assertOverflowRawTail(t *testing.T, r *directoryRecorder) {
	t.Helper()
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
}

func assertOverflowBundle(t *testing.T, r *directoryRecorder) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(r.destination, "manifest.json")); err != nil {
		t.Fatalf("partial bundle was not published: %v", err)
	}
}

func TestMixedIdentifiedAndLegacyPCMUsesArtifactOffsets(t *testing.T) {
	r := newEvidenceRecorder(t)
	recordResponsePCM(t, r, 0)
	recordResponseMessage(t, r, 0)
	recordResponseMessage(t, r, 1)
	frame := audio.PCMFrame{Samples: []int16{2}, Format: audio.PCM16DeviceFormat(24000)}
	if err := r.RecordAudio(t.Context(), session.LiveAudioRecord{Direction: session.LiveRecordAgent, Timestamp: evidenceTime(), Frame: frame}); err != nil {
		t.Fatal(err)
	}
	recordResponseMessage(t, r, 2)
	recordResponseMessage(t, r, 3)
	recordEvidenceTerminal(t, r)
	if err := r.Finalize(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	assertResponseAudioIndex(t, r)
	if got := readEvidenceFile(t, r, "audio/out-000.pcm"); !bytes.Equal(got, []byte{1, 0, 2, 0}) {
		t.Fatalf("recorded PCM = %v", got)
	}
}
