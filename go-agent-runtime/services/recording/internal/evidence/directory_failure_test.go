package evidence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
)

func TestDirectoryRecorderValidatesDestinationAndInjectedClockBeforeAdmission(t *testing.T) {
	for _, kind := range []string{"empty", "file", "populated", "symlink", "no clock"} {
		t.Run(kind, func(t *testing.T) {
			target, source := badEvidenceDestination(t, kind)
			if recorder, err := New(recording.LiveEvidenceOptions{Destination: target}, source); err == nil {
				if cleanupErr := recorder.Finalize(t.Context(), nil); cleanupErr != nil {
					t.Log(cleanupErr)
				}
				t.Fatal("invalid destination or missing clock admitted")
			}
		})
	}
}

func badEvidenceDestination(t *testing.T, kind string) (string, clock.Source) {
	t.Helper()
	directory := t.TempDir()
	target := filepath.Join(directory, "recording")
	var source clock.Source = clock.Real{}
	switch kind {
	case "empty":
		target = ""
	case "file":
		if err := os.WriteFile(target, []byte("customer data"), evidenceFileMode); err != nil {
			t.Fatal(err)
		}
	case "populated":
		if err := os.Mkdir(target, evidenceDirectoryMode); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(target, "keep"), []byte("customer data"), evidenceFileMode); err != nil {
			t.Fatal(err)
		}
	case "symlink":
		if err := os.Symlink(directory, target); err != nil {
			t.Fatal(err)
		}
	case "no clock":
		source = nil
	}
	return target, source
}

func TestDirectoryRecorderUsesCanonicalClockAndKeepsForeignClaim(t *testing.T) {
	source := clock.NewDeterministic(evidenceTime(), time.Millisecond)
	options := recording.LiveEvidenceOptions{Destination: filepath.Join(t.TempDir(), "recording")}
	r, err := newDirectoryRecorder(options, source)
	if err != nil {
		t.Fatal(err)
	}
	if r.options.ClockBase != evidenceTime() || r.options.WallClockStart != evidenceTime() {
		t.Fatalf("clock injection lost: %+v", r.options)
	}
	if _, err := newDirectoryRecorder(options, source); !errors.Is(err, recording.ErrLiveEvidenceClaimed) {
		t.Fatalf("concurrent destination claim = %v", err)
	}
	recordEvidenceText(t, r, "observation")
	recordEvidenceTerminal(t, r)
	if err := os.Remove(r.lockPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.lockPath, []byte("another owner"), evidenceFileMode); err != nil {
		t.Fatal(err)
	}
	if err := r.Finalize(t.Context(), nil); !errors.Is(err, recording.ErrLiveEvidenceClaimed) {
		t.Fatalf("replaced claim error = %v", err)
	}
	data, err := os.ReadFile(r.lockPath)
	if err != nil || string(data) != "another owner" {
		t.Fatalf("finalization removed foreign claim: %s, %v", data, err)
	}
}

func TestDirectoryRecorderUnavailableClockOrFormatAndDroppedEventsRemainPartial(t *testing.T) {
	for _, kind := range []string{"timestamp", "format", "overflow event", "invalid message"} {
		t.Run(kind, func(t *testing.T) {
			r := newEvidenceRecorder(t)
			recordEvidenceText(t, r, "observed")
			recordUnavailableEvidence(t, r, kind)
			recordEvidenceTerminal(t, r)
			if err := r.Finalize(t.Context(), nil); err == nil {
				t.Fatal("missing or dropped evidence claims completeness")
			}
			manifest := evidenceManifest(t, r)
			if manifest.RecordingStatus == nil || manifest.RecordingStatus.State != transcript.RecordingStatusPartial {
				t.Fatal("partial status missing")
			}
		})
	}
}

func recordUnavailableEvidence(t *testing.T, r *directoryRecorder, kind string) {
	t.Helper()
	switch kind {
	case "timestamp":
		if err := r.RecordMessage(t.Context(), session.LiveRecord{Message: messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("unclocked")}}); err != nil {
			t.Fatal(err)
		}
	case "format":
		if err := r.RecordAudio(t.Context(), session.LiveAudioRecord{Timestamp: evidenceTime(), Frame: sharedaudio.PCMFrame{Samples: []int16{1}}}); err != nil {
			t.Fatal(err)
		}
	case "overflow event":
		if err := r.RecordEvent(t.Context(), session.LiveEvent{Timestamp: evidenceTime(), Kind: string(session.LiveEventOverflow), Dropped: 1}); err != nil {
			t.Fatal(err)
		}
	case "invalid message":
		if err := r.RecordMessage(t.Context(), session.LiveRecord{Timestamp: evidenceTime(), Message: messages.StreamMessage{Type: "unsupported", Value: messages.NewTextDeltaValue("unknown")}}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDirectoryRecorderEmptyResponseBoundaryDoesNotInventPCM(t *testing.T) {
	r := newEvidenceRecorder(t)
	frame := sharedaudio.PCMFrame{Format: sharedaudio.PCM16DeviceFormat(24000), EndOfResponse: true}
	if err := r.RecordAudio(t.Context(), session.LiveAudioRecord{Timestamp: evidenceTime(), Frame: frame}); err != nil {
		t.Fatal(err)
	}
	recordEvidenceTerminal(t, r)
	if err := r.Finalize(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(r.destination, "audio", "out-000.pcm")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invented empty/silent PCM: %v", err)
	}
}

func TestDirectoryRecorderRejectsCanceledAdmissionAndConflictingTerminal(t *testing.T) {
	r := newEvidenceRecorder(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := r.RecordMessage(ctx, session.LiveRecord{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("message cancellation = %v", err)
	}
	if err := r.RecordAudio(ctx, session.LiveAudioRecord{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("audio cancellation = %v", err)
	}
	if err := r.RecordEvent(ctx, session.LiveEvent{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("event cancellation = %v", err)
	}
	recordEvidenceTerminal(t, r)
	terminal := messages.NewSessionCloseValueWithTerminal("fixture", "failed", "failed", messages.TerminalReasonTerminalFailure, messages.TerminalProvenanceSession, messages.TerminalOutputPartial)
	if err := r.RecordEvent(t.Context(), session.LiveEvent{Timestamp: evidenceTime(), Terminal: terminal}); err != nil {
		t.Fatal(err)
	}
	if err := r.Finalize(t.Context(), nil); err == nil {
		t.Fatal("conflicting terminal evidence accepted as complete")
	}
}

func TestDirectoryRecorderJoinsProviderCaptureWithoutChangingIntegrity(t *testing.T) {
	r := newEvidenceRecorder(t)
	capture := gatewaytesting.SessionCapture{Version: gatewaytesting.SessionCaptureVersion, Records: []gatewaytesting.CapturedSessionEvent{{
		Sequence: 1, Direction: gatewaytesting.DirectionServerToClient, Type: "session.created", PayloadType: gatewaytesting.SessionPayloadTypeWebSocketMessage, Payload: []byte(`{"type":"session.created"}`),
	}}}
	original, err := json.Marshal(capture)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.ProviderCapturePath(), original, evidenceFileMode); err != nil {
		t.Fatal(err)
	}
	recordEvidenceText(t, r, "observed")
	recordEvidenceTerminal(t, r)
	if err := r.Finalize(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	copied := readEvidenceFile(t, r, "provider.json")
	if !bytes.Equal(copied, original) {
		t.Fatal("portable provider capture bytes changed")
	}
	loaded, err := gatewaytesting.LoadSessionCapture(filepath.Join(r.destination, "provider.json"))
	if err != nil || len(loaded.Records) != 1 {
		t.Fatalf("provider integrity verification = %+v, %v", loaded, err)
	}
	if _, err := os.Stat(r.spool); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("spool retained after publication: %v", err)
	}
}

func TestDirectoryRecorderDoesNotInvalidateProtectedCaptureToRedactIt(t *testing.T) {
	const secret = "fixture-sensitive-value"
	r := newEvidenceRecorder(t)
	r.options.Credentials = []string{secret}
	if err := os.WriteFile(r.ProviderCapturePath(), []byte(`{"protected":"`+secret+`"}`), evidenceFileMode); err != nil {
		t.Fatal(err)
	}
	recordEvidenceText(t, r, "observed")
	recordEvidenceTerminal(t, r)
	if err := r.Finalize(t.Context(), nil); !errors.Is(err, transcript.ErrInvalidRecording) {
		t.Fatalf("protected capture redaction = %v", err)
	}
	if _, err := os.Stat(r.destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalidated or unsafe archive published: %v", err)
	}
}

func TestDirectoryRecorderMalformedProviderSourceIsPartial(t *testing.T) {
	for _, sourceKind := range []string{"missing", "empty", "directory"} {
		r := newEvidenceRecorderWithProviderPath(t)
		if err := os.Remove(r.ProviderCapturePath()); err != nil {
			t.Fatal(err)
		}
		var createErr error
		if sourceKind == "directory" {
			createErr = os.Mkdir(r.ProviderCapturePath(), evidenceDirectoryMode)
		} else if sourceKind == "empty" {
			createErr = os.WriteFile(r.ProviderCapturePath(), nil, evidenceFileMode)
		}
		if createErr != nil {
			t.Fatal(createErr)
		}
		recordEvidenceText(t, r, "observed")
		recordEvidenceTerminal(t, r)
		if err := r.Finalize(t.Context(), nil); err == nil {
			t.Fatal("empty or non-regular provider capture accepted")
		}
		if status := evidenceManifest(t, r).RecordingStatus; status == nil || status.State != transcript.RecordingStatusPartial {
			t.Fatal("unavailable provider source not marked partial")
		}
	}
}

func TestDirectoryRecorderPairsAudioBudgetWithTranscriptBoundary(t *testing.T) {
	root := t.TempDir()
	r, err := newDirectoryRecorder(recording.LiveEvidenceOptions{Destination: filepath.Join(root, "capture"), ClockBase: evidenceTime(), WallClockStart: evidenceTime(), Limits: recording.ResourceLimits{AudioBytes: 2, AudioItems: 1}}, clock.Real{})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.ProviderCapturePath(), []byte(`{"fixture_observation":"session.created"}`), evidenceFileMode); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := r.Finalize(t.Context(), nil); err != nil {
			t.Logf("recording cleanup: %v", err)
		}
	})
	frame := sharedaudio.PCMFrame{Samples: []int16{7}, Format: sharedaudio.PCM16DeviceFormat(24000)}
	for i := 0; i < 2; i++ {
		if err := r.RecordAudio(t.Context(), session.LiveAudioRecord{Direction: session.LiveRecordAgent, Timestamp: evidenceTime(), Frame: frame}); err != nil {
			t.Fatal(err)
		}
	}
	recordEvidenceTerminal(t, r)
	if err := r.Finalize(t.Context(), nil); !errors.Is(err, io.ErrShortBuffer) {
		t.Fatalf("cumulative audio overflow = %v, want short buffer", err)
	}
	if got := readEvidenceFile(t, r, "audio/out-000.pcm"); !bytes.Equal(got, codec.EncodePCM16([]int16{7})) {
		t.Fatalf("audio prefix = %v, want one accepted frame", got)
	}
	if status := evidenceManifest(t, r).RecordingStatus; status == nil || status.State != transcript.RecordingStatusPartial {
		t.Fatalf("audio overflow status = %+v, want partial", status)
	}
}

func TestDirectoryRecorderBoundsOversizedTerminalRuntimeEvent(t *testing.T) {
	const wantMaxEventTextBytes = 2048

	r := newEvidenceRecorder(t)
	terminal := messages.NewSessionCloseValueWithTerminal(
		strings.Repeat("session-", 1024), strings.Repeat("reason-", 1024), strings.Repeat("classification-", 1024),
		messages.TerminalReasonTerminalFailure, messages.TerminalProvenanceSession, messages.TerminalOutputPartial,
	)
	if err := r.RecordEvent(t.Context(), session.LiveEvent{
		Kind:      "oversized.runtime.event",
		Timestamp: evidenceTime(),
		Text:      strings.Repeat("x", directoryEvidenceQueueMaxBytes/2),
		Reason:    strings.Repeat("y", directoryEvidenceQueueMaxBytes/2),
		Terminal:  terminal,
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.Finalize(t.Context(), nil); err != nil {
		t.Fatalf("oversized terminal event = %v", err)
	}
	manifest := evidenceManifest(t, r)
	if manifest.Terminal == nil || manifest.Terminal.OutputState != messages.TerminalOutputPartial {
		t.Fatalf("terminal evidence = %+v, want bounded terminal", manifest.Terminal)
	}
	found := false
	for _, line := range bytes.Split(bytes.TrimSpace(readEvidenceFile(t, r, "agent.transcript.jsonl")), []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		record, err := transcript.Decode(line)
		if err != nil {
			t.Fatal(err)
		}
		if record.Stream != transcript.StreamRuntimeEvent {
			continue
		}
		var payload struct {
			Event struct {
				Text string `json:"text"`
			} `json:"event"`
		}
		if err := json.Unmarshal(record.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if len(payload.Event.Text) != wantMaxEventTextBytes {
			t.Fatalf("runtime event text bytes = %d, want %d", len(payload.Event.Text), wantMaxEventTextBytes)
		}
		found = true
	}
	if !found {
		t.Fatal("bounded runtime event was not preserved")
	}
}

func TestDirectoryRecorderBoundsFallbackTerminalError(t *testing.T) {
	r := newEvidenceRecorder(t)
	recordEvidenceText(t, r, "observed before fallback")
	runErr := errors.New(strings.Repeat("run failure: ", 30000))
	if err := r.Finalize(t.Context(), runErr); err == nil {
		t.Fatal("missing terminal fallback reported complete")
	}
	manifest := evidenceManifest(t, r)
	if manifest.Terminal == nil {
		t.Fatal("fallback terminal was not retained")
	}
	if len(manifest.Terminal.Reason) > 2048 {
		t.Fatalf("fallback terminal reason bytes = %d, want <= 2048", len(manifest.Terminal.Reason))
	}
	if r.budget.terminalBytes <= 0 || r.budget.terminalBytes > recording.DefaultTerminalBytes {
		t.Fatalf("fallback terminal budget = %d, want bounded reservation", r.budget.terminalBytes)
	}
}

func TestSemanticSidecarBoundsOversizedPublicTerminal(t *testing.T) {
	rawPath := filepath.Join(t.TempDir(), "cutoff.session.json")
	r, err := NewSemanticSidecar(rawPath)
	if err != nil {
		t.Fatal(err)
	}
	terminal := messages.NewSessionCloseValueWithTerminal(
		strings.Repeat("session-", 50000), strings.Repeat("reason-", 50000), strings.Repeat("classification-", 50000),
		messages.TerminalReason(strings.Repeat("terminal-", 50000)), messages.TerminalProvenance(strings.Repeat("provenance-", 50000)), messages.TerminalOutputState(strings.Repeat("output-", 50000)),
	)
	if err := r.RecordEvent(t.Context(), session.LiveEvent{Timestamp: evidenceTime(), Terminal: terminal}); err != nil {
		t.Fatal(err)
	}
	if err := r.Finalize(t.Context(), nil); err != nil {
		t.Fatalf("finalize oversized terminal = %v", err)
	}
	sidecarPath := strings.TrimSuffix(rawPath, filepath.Ext(rawPath)) + ".jsonl"
	info, err := os.Stat(sidecarPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > recording.DefaultSidecarBytes {
		t.Fatalf("sidecar bytes = %d, want <= %d", info.Size(), recording.DefaultSidecarBytes)
	}
	data, err := os.ReadFile(sidecarPath)
	if err != nil {
		t.Fatal(err)
	}
	value, ok := decodeDurationSidecarTerminal(t, bytes.TrimSpace(data))
	if !ok || len(value.Reason) > 2048 || len(value.Classification) > 2048 {
		t.Fatalf("bounded sidecar terminal = %+v", value)
	}
}

func TestDirectoryRecorderBoundsManifestMetadata(t *testing.T) {
	r, err := newDirectoryRecorder(recording.LiveEvidenceOptions{
		Destination:   filepath.Join(t.TempDir(), "capture"),
		Provider:      strings.Repeat("provider-", 1<<16),
		Model:         strings.Repeat("model-", 1<<16),
		SessionID:     strings.Repeat("session-", 1<<16),
		ParticipantID: strings.Repeat("participant-", 1<<16),
		ClockBase:     evidenceTime(), WallClockStart: evidenceTime(),
	}, clock.Real{})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.ProviderCapturePath(), []byte(`{"fixture_observation":"session.created"}`), evidenceFileMode); err != nil {
		t.Fatal(err)
	}
	recordEvidenceText(t, r, "metadata remains bounded")
	recordEvidenceTerminal(t, r)
	if err := r.Finalize(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	manifest := evidenceManifest(t, r)
	if len(manifest.Model) > recordingMetadataFieldLimit || len(manifest.Configuration["provider"]) > recordingMetadataFieldLimit || len(manifest.Configuration["session_id"]) > recordingMetadataFieldLimit || len(manifest.Configuration["participant_id"]) > recordingMetadataFieldLimit {
		t.Fatalf("manifest metadata was not bounded: model=%d provider=%d session=%d participant=%d", len(manifest.Model), len(manifest.Configuration["provider"]), len(manifest.Configuration["session_id"]), len(manifest.Configuration["participant_id"]))
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(encoded)+1) > recording.DefaultMetadataBytes {
		t.Fatalf("manifest bytes = %d, want <= %d", len(encoded)+1, recording.DefaultMetadataBytes)
	}
}

func newEvidenceRecorderWithProviderPath(t *testing.T) *directoryRecorder {
	t.Helper()
	root := t.TempDir()
	r, err := newDirectoryRecorder(recording.LiveEvidenceOptions{
		Destination:         filepath.Join(root, "capture"),
		ProviderCapturePath: filepath.Join(root, "provider.json"),
		ClockBase:           evidenceTime(),
		WallClockStart:      evidenceTime(),
	}, clock.Real{})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.ProviderCapturePath(), []byte(`{"fixture_observation":"session.created"}`), evidenceFileMode); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := r.Finalize(t.Context(), nil); err != nil {
			t.Logf("recording cleanup: %v", err)
		}
	})
	return r
}

func TestDirectoryRecorderAllowsAbsentImplicitProviderCapture(t *testing.T) {
	r := newEvidenceRecorder(t)
	if err := os.Remove(r.ProviderCapturePath()); err != nil {
		t.Fatal(err)
	}
	recordEvidenceText(t, r, "semantic-only session")
	recordEvidenceTerminal(t, r)
	if err := r.Finalize(t.Context(), nil); err != nil {
		t.Fatalf("implicit provider capture absence = %v", err)
	}
	manifest := evidenceManifest(t, r)
	if manifest.RecordingStatus != nil {
		t.Fatalf("semantic-only bundle marked partial: %+v", manifest.RecordingStatus)
	}
	if got := manifest.Configuration["provider_capture"]; got != "unavailable" {
		t.Fatalf("provider capture metadata = %q, want unavailable", got)
	}
	if _, err := os.Stat(filepath.Join(r.destination, "provider.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("absent implicit provider capture was fabricated: %v", err)
	}
}

func TestDirectoryRecorderMarksAudioPartialWhenImplicitProviderCaptureIsMissing(t *testing.T) {
	r := newEvidenceRecorder(t)
	if err := os.Remove(r.ProviderCapturePath()); err != nil {
		t.Fatal(err)
	}
	frame := sharedaudio.PCMFrame{Samples: []int16{13, -14, 15}, Format: sharedaudio.PCM16DeviceFormat(24000), EndOfResponse: true}
	if err := r.RecordAudio(t.Context(), session.LiveAudioRecord{Direction: session.LiveRecordAgent, Timestamp: evidenceTime(), Frame: frame}); err != nil {
		t.Fatal(err)
	}
	recordEvidenceTerminal(t, r)
	if err := r.Finalize(t.Context(), nil); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing provider evidence error = %v, want os.ErrNotExist", err)
	}
	manifest := evidenceManifest(t, r)
	if manifest.RecordingStatus == nil || manifest.RecordingStatus.State != transcript.RecordingStatusPartial {
		t.Fatalf("audio bundle without provider evidence was not partial: %+v", manifest.RecordingStatus)
	}
}

func TestConversationTranscriptCompletionReplacesDeltasPerItemAndRole(t *testing.T) {
	var conversation evidenceConversation
	for _, role := range []messages.Role{messages.RoleUser, messages.RoleAssistant} {
		conversation.observe(messages.StreamMessage{Type: messages.StreamTypeTranscriptDelta, Role: role, Value: &messages.TranscriptDeltaValue{ItemID: "first", Text: "partial"}}, false, 1)
		conversation.observe(messages.StreamMessage{Type: messages.StreamTypeTranscriptEnd, Role: role, Value: &messages.TranscriptEndValue{ItemID: "first", FullText: "corrected"}}, false, 2)
		conversation.observe(messages.StreamMessage{Type: messages.StreamTypeTranscriptEnd, Role: role, Value: &messages.TranscriptEndValue{ItemID: "second", FullText: " tail"}}, false, 3)
	}
	if conversation.turn.inputText.String() != "corrected tail" || conversation.turn.responseText.String() != "corrected tail" {
		t.Fatalf("transcript snapshots duplicated or lost: input=%q response=%q", conversation.turn.inputText.String(), conversation.turn.responseText.String())
	}
}

func TestUnprojectableMessagePreservesOriginalAndSubsequentAudio(t *testing.T) {
	r := newEvidenceRecorder(t)
	message := session.LiveRecord{Timestamp: evidenceTime(), Message: messages.StreamMessage{Type: "future-provider-observation", Value: messages.NewTextDeltaValue("preserved payload")}}
	if err := r.RecordMessage(t.Context(), message); err != nil {
		t.Fatal(err)
	}
	frame := sharedaudio.PCMFrame{Samples: []int16{1, -2, 3}, Format: sharedaudio.PCM16DeviceFormat(24000)}
	if err := r.RecordAudio(t.Context(), session.LiveAudioRecord{Direction: session.LiveRecordAgent, Timestamp: evidenceTime(), Frame: frame}); err != nil {
		t.Fatal(err)
	}
	recordEvidenceTerminal(t, r)
	if err := r.Finalize(t.Context(), nil); err == nil {
		t.Fatal("unknown projection reported complete")
	}
	first := bytes.SplitN(readEvidenceFile(t, r, "agent.transcript.jsonl"), []byte("\n"), 2)[0]
	var preserved transcript.Record
	if err := json.Unmarshal(first, &preserved); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(preserved.Payload, []byte("preserved payload")) {
		t.Fatal("original observation lost")
	}
	if got := readEvidenceFile(t, r, "audio/out-000.pcm"); !bytes.Equal(got, []byte{1, 0, 254, 255, 3, 0}) {
		t.Fatalf("later PCM lost: %v", got)
	}
}
