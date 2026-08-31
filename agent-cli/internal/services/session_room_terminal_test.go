package services

import (
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
)

func TestRunRoom_FailureTerminalEvidenceIsAuthoritative(t *testing.T) {
	const (
		failedID = "failed"
		peerID   = "peer"
		secret   = "room-provider-secret"
	)
	failure := messages.NewErrorValueWithTerminal(
		"raw provider detail "+secret,
		providers.ErrorClassTransport,
		messages.TerminalReasonTerminalFailure,
		messages.TerminalProvenanceProvider,
		messages.TerminalOutputPartial,
	)
	failure.Code = "provider_transport_failure"
	inferencers := map[string]*roomTestInferencer{
		failedID: {events: []messages.StreamMessage{
			roomTestSessionOpen(failedID),
			{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()},
			{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("partial output")},
			{Type: messages.StreamTypeError, Value: failure},
		}},
		peerID: {events: []messages.StreamMessage{roomTestSessionOpen(peerID)}},
	}
	opts, _ := newRoomTestRunOptions([]string{failedID, peerID}, inferencers)
	opts.CredentialLookup = func(string) (string, bool) { return secret, true }
	opts.OutputDir = filepath.Join(t.TempDir(), "failed-room")
	diagnostics := make(chan struct {
		participantID string
		record        SessionDiagnosticRecord
	}, 128)
	opts.OnDiagnostic = func(participantID string, record SessionDiagnosticRecord) {
		diagnostics <- struct {
			participantID string
			record        SessionDiagnosticRecord
		}{participantID: participantID, record: record}
	}

	result, err := RunRoomWithResult(context.Background(), io.Discard, opts)
	if err == nil {
		t.Fatal("provider failure returned nil room error")
	}
	if result.TerminationReason != RoomTerminationFailed {
		t.Fatalf("room reason = %q, want %q", result.TerminationReason, RoomTerminationFailed)
	}
	failed := result.Participants[failedID]
	if failed.Reason != ParticipantTerminationError || failed.TerminationTrigger != ParticipantTerminationTriggerSessionFailure || failed.TerminationDisposition != ParticipantTerminationDispositionFailed || failed.Classification != providers.ErrorClassTransport || failed.TerminalReason != messages.TerminalReasonTerminalFailure || failed.TerminalProvenance != messages.TerminalProvenanceProvider || failed.OutputState != messages.TerminalOutputPartial {
		t.Fatalf("failed participant terminal evidence = %+v", failed)
	}
	if strings.Contains(failed.Error, secret) || strings.Contains(result.Error, secret) {
		t.Fatalf("provider secret leaked in result: participant=%q room=%q", failed.Error, result.Error)
	}
	peer := result.Participants[peerID]
	if peer.Reason != ParticipantTerminationEnded || peer.Error != "" {
		t.Fatalf("peer result = %+v, want clean coordinator cancellation", peer)
	}

	failureRecords := make([]struct {
		participantID string
		record        SessionDiagnosticRecord
	}, 0, 1)
	for {
		select {
		case diagnostic := <-diagnostics:
			if diagnostic.record.Event == SessionDiagnosticEventFailure {
				failureRecords = append(failureRecords, diagnostic)
			}
		default:
			if len(failureRecords) != 1 || failureRecords[0].participantID != failedID {
				t.Fatalf("session failure diagnostics = %+v, want exactly one for %q", failureRecords, failedID)
			}
			goto diagnosticsDrained
		}
	}

diagnosticsDrained:
	manifestData := readRoomEvidenceFile(t, filepath.Join(opts.OutputDir, RoomEvidenceManifestPath))
	if strings.Contains(string(manifestData), secret) {
		t.Fatalf("run manifest leaked provider secret: %s", manifestData)
	}
	var manifest roomEvidenceManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatalf("decode run manifest: %v", err)
	}
	manifestParticipant := manifest.Participants[failedID]
	if manifestParticipant.TerminationReason != failed.TerminationReason || manifestParticipant.Reason != failed.Reason || manifestParticipant.TerminationTrigger != failed.TerminationTrigger || manifestParticipant.TerminationDisposition != failed.TerminationDisposition || manifestParticipant.Classification != failed.Classification || manifestParticipant.TerminalReason != failed.TerminalReason || manifestParticipant.TerminalProvenance != failed.TerminalProvenance || manifestParticipant.OutputState != failed.OutputState || manifestParticipant.CompletedTurns != failed.TurnsCompleted || manifestParticipant.Connected != failed.Connected || manifestParticipant.Error != failed.Error {
		t.Fatalf("manifest failed participant = %+v, result = %+v", manifestParticipant, failed)
	}

	timeline := readRoomEvidenceJSONLLines(t, filepath.Join(opts.OutputDir, RoomEvidenceTimelinePath))
	var terminated *roomTimelineEntry
	providerErrorSeen := false
	for _, line := range timeline {
		var entry roomTimelineEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			t.Fatalf("decode room timeline entry: %v", err)
		}
		if strings.Contains(string(line), secret) {
			t.Fatalf("room timeline leaked provider secret: %s", line)
		}
		if entry.Event == "provider_error" {
			providerErrorSeen = true
			if _, hasRawMessage := entry.Fields["message"]; hasRawMessage {
				t.Fatalf("provider error timeline retained raw message: %+v", entry)
			}
		}
		if entry.Event == "participant_terminated" && entry.Participant == failedID {
			copy := entry
			terminated = &copy
		}
	}
	if !providerErrorSeen || terminated == nil {
		t.Fatalf("timeline missing provider failure evidence: provider_error=%v terminated=%v", providerErrorSeen, terminated)
	}
	for key, want := range participantTerminalFields(failed) {
		if got := terminated.Fields[key]; got != want {
			t.Fatalf("timeline participant_terminated field %q = %q, want %q (fields=%v)", key, got, want, terminated.Fields)
		}
	}

	failedDiagnostics := readRoomEvidenceJSONLLines(t, filepath.Join(opts.OutputDir, manifestParticipant.Artifacts.Diagnostics))
	failureCount := 0
	for _, line := range failedDiagnostics {
		var record selfPlayDiagnosticLine
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatalf("decode participant diagnostic: %v", err)
		}
		if record.Event == SessionDiagnosticEventFailure {
			failureCount++
		}
	}
	if failureCount != 1 {
		t.Fatalf("participant session_failure records = %d, want exactly one", failureCount)
	}
}

func TestRoomParticipantLifecycle_BoundFirstRejectsLateFailure(t *testing.T) {
	lifecycle := &roomParticipantLifecycle{}
	lifecycle.observe(messages.StreamMessage{
		Type: messages.StreamTypeMessageStart,
		Role: messages.RoleAssistant,
	})
	lifecycle.markCoordinatorStopping(true, RoomTerminationMaxDurationReached)
	lifecycle.markBoundCancellation()

	accepted := lifecycle.observeTerminal(sessionTerminalObservation{
		Classification:     providers.ErrorClassTransport,
		TerminalReason:     string(messages.TerminalReasonTerminalFailure),
		TerminalProvenance: string(messages.TerminalProvenanceProvider),
		OutputState:        string(messages.TerminalOutputNone),
		Err:                context.DeadlineExceeded,
		Failure:            true,
	})
	if accepted {
		t.Fatal("late provider failure replaced bound cancellation")
	}
	observation := lifecycle.terminalObservationSnapshot()
	if observation.terminationTrigger != ParticipantTerminationTriggerMaxDurationReachedMidResponse || observation.terminationDisposition != ParticipantTerminationDispositionCancelledAfterGrace || observation.classification != RoomBoundCancelledClassification || observation.terminalReason != string(messages.TerminalReasonCancellation) || observation.terminalProvenance != string(messages.TerminalProvenanceRoom) {
		t.Fatalf("bound-first observation = %+v", observation)
	}
}

func TestRoomParticipantLifecycle_BoundCancellationSendsResponseCancelOnce(t *testing.T) {
	admissionClosed := make(chan struct{})
	close(admissionClosed)
	inner := newRoomTestSession()
	lifecycle := &roomParticipantLifecycle{admissionClosed: admissionClosed}
	tracked := &roomTrackedSession{Session: inner, lifecycle: lifecycle, admissionClosed: admissionClosed}
	lifecycle.setOwnedSession(tracked)
	lifecycle.observe(messages.StreamMessage{
		Type:       messages.StreamTypeMessageStart,
		Role:       messages.RoleAssistant,
		ResponseID: "response-active",
		Value:      messages.NewMessageStartValue(),
	})
	lifecycle.markCoordinatorStopping(true, RoomTerminationMaxTurnsReached)
	lifecycle.markBoundCancellation()
	lifecycle.cancelActiveResponse()
	lifecycle.cancelActiveResponse()

	if got := inner.sentTypeCountSnapshot(messages.StreamTypeResponseCancel); got != 1 {
		t.Fatalf("response cancellations = %d, want exactly one", got)
	}
	inner.mu.Lock()
	sent := append([]messages.StreamMessage(nil), inner.sent...)
	inner.mu.Unlock()
	if len(sent) != 1 || sent[0].Value == nil {
		t.Fatalf("sent cancellation messages = %+v, want one valued RESPONSE.CANCEL", sent)
	}
	if _, ok := sent[0].Value.(*messages.ResponseCancelValue); !ok {
		t.Fatalf("RESPONSE.CANCEL value = %T, want *messages.ResponseCancelValue", sent[0].Value)
	}
}

func TestRoomParticipantLifecycle_BoundCancellationRejectsLateCompletion(t *testing.T) {
	lifecycle := &roomParticipantLifecycle{}
	lifecycle.observe(messages.StreamMessage{
		Type:       messages.StreamTypeMessageStart,
		Role:       messages.RoleAssistant,
		ResponseID: "response-active",
		Value:      messages.NewMessageStartValue(),
	})
	lifecycle.markCoordinatorStopping(true, RoomTerminationMaxTurnsReached)
	lifecycle.markBoundCancellation()

	if lifecycle.observeTerminal(sessionTerminalObservation{
		ResponseID:         "response-active",
		TerminalReason:     string(messages.TerminalReasonProviderAuthoredCompletion),
		TerminalProvenance: string(messages.TerminalProvenanceProvider),
		OutputState:        string(messages.TerminalOutputComplete),
	}) {
		t.Fatal("late provider completion replaced bound cancellation")
	}
	observation := lifecycle.terminalObservationSnapshot()
	if observation.terminationTrigger != ParticipantTerminationTriggerMaxTurnsReachedMidResponse || observation.terminationDisposition != ParticipantTerminationDispositionCancelledAfterGrace || observation.classification != RoomBoundCancelledClassification || observation.terminalReason != string(messages.TerminalReasonCancellation) || observation.terminalProvenance != string(messages.TerminalProvenanceRoom) {
		t.Fatalf("bound cancellation after late completion = %+v", observation)
	}
}

func TestRoomParticipantLifecycle_BoundAdmissionAllowsOnlyExistingToolContinuation(t *testing.T) {
	admissionClosed := make(chan struct{})
	inner := newRoomTestSession()
	lifecycle := &roomParticipantLifecycle{admissionClosed: admissionClosed}
	tracked := &roomTrackedSession{Session: inner, lifecycle: lifecycle, admissionClosed: admissionClosed}

	lifecycle.observe(messages.StreamMessage{
		Type:       messages.StreamTypeMessageStart,
		Role:       messages.RoleAssistant,
		ResponseID: "response-tool",
		Value:      messages.NewMessageStartValue(),
	})
	lifecycle.observe(messages.StreamMessage{
		Type:       messages.StreamTypeToolCallEnd,
		Role:       messages.RoleAssistant,
		ToolCallId: "call-existing",
		Value:      messages.NewToolCallEndValue("call-existing", "lookup", "{}"),
	})
	lifecycle.markCoordinatorStopping(true, RoomTerminationMaxTurnsReached)
	close(admissionClosed)

	if !tracked.Send(context.Background(), messages.StreamMessage{
		Type:       messages.StreamTypeToolCallEnd,
		ToolCallId: "call-existing",
		Value:      messages.NewToolCallEndValue("call-existing", "lookup", "result"),
	}) {
		t.Fatal("existing tool result was not admitted during bound grace")
	}
	if !tracked.Send(context.Background(), messages.StreamMessage{
		Type:  messages.StreamTypeResponseCreate,
		Value: messages.NewResponseCreateValue(),
	}) {
		t.Fatal("existing tool continuation was not admitted during bound grace")
	}
	if tracked.Send(context.Background(), messages.StreamMessage{
		Type:       messages.StreamTypeToolCallEnd,
		ToolCallId: "call-new",
		Value:      messages.NewToolCallEndValue("call-new", "lookup", "result"),
	}) {
		t.Fatal("new tool result crossed the bound admission boundary")
	}
	if tracked.Send(context.Background(), messages.StreamMessage{Type: messages.StreamTypeAudioDelta}) {
		t.Fatal("new audio crossed the bound admission boundary")
	}

	lifecycle.markBoundCancellation()
	if tracked.Send(context.Background(), messages.StreamMessage{Type: messages.StreamTypeResponseCreate}) {
		t.Fatal("tool continuation crossed the force-cancellation boundary")
	}
	if got := inner.sentCountSnapshot(); got != 2 {
		t.Fatalf("underlying session received %d messages, want tool result and continuation", got)
	}
}

func TestRoomParticipantLifecycle_BoundTerminalAdmissionClosesAfterGraceCompletion(t *testing.T) {
	lifecycle := &roomParticipantLifecycle{}
	lifecycle.observe(messages.StreamMessage{
		Type:       messages.StreamTypeMessageStart,
		Role:       messages.RoleAssistant,
		ResponseID: "response-active",
		Value:      messages.NewMessageStartValue(),
	})
	lifecycle.markCoordinatorStopping(true, RoomTerminationMaxDurationReached)
	if !lifecycle.admitResponseTerminal() {
		t.Fatal("active response terminal was not admitted during grace")
	}
	if !lifecycle.observeTerminal(sessionTerminalObservation{
		ResponseID:         "response-active",
		TerminalReason:     string(messages.TerminalReasonProviderAuthoredCompletion),
		TerminalProvenance: string(messages.TerminalProvenanceProvider),
		OutputState:        string(messages.TerminalOutputComplete),
	}) {
		t.Fatal("active response terminal was not recorded")
	}
	if lifecycle.admitResponseTerminal() {
		t.Fatal("a second response terminal crossed the bound after completion")
	}
	observation := lifecycle.terminalObservationSnapshot()
	if observation.terminationTrigger != ParticipantTerminationTriggerMaxDurationReachedMidResponse || observation.terminationDisposition != ParticipantTerminationDispositionCompletedDuringGrace || observation.terminalReason != string(messages.TerminalReasonProviderAuthoredCompletion) || observation.terminalProvenance != string(messages.TerminalProvenanceProvider) || observation.outputState != string(messages.TerminalOutputComplete) {
		t.Fatalf("bound completion observation = %+v", observation)
	}
}

func TestRoomParticipantLifecycle_FailureFirstSurvivesLaterBound(t *testing.T) {
	lifecycle := &roomParticipantLifecycle{}
	if !lifecycle.observeTerminal(sessionTerminalObservation{
		Classification:     providers.ErrorClassTransport,
		TerminalReason:     string(messages.TerminalReasonTerminalFailure),
		TerminalProvenance: string(messages.TerminalProvenanceProvider),
		OutputState:        string(messages.TerminalOutputNone),
		Err:                context.DeadlineExceeded,
		Failure:            true,
	}) {
		t.Fatal("initial provider failure was not accepted")
	}
	lifecycle.markCoordinatorStopping(true, RoomTerminationMaxTurnsReached)
	observation := lifecycle.terminalObservationSnapshot()
	if observation.terminationTrigger != ParticipantTerminationTriggerSessionFailure || observation.terminationDisposition != ParticipantTerminationDispositionFailed || observation.classification != providers.ErrorClassTransport || observation.terminalReason != string(messages.TerminalReasonTerminalFailure) || observation.terminalProvenance != string(messages.TerminalProvenanceProvider) {
		t.Fatalf("failure-first observation = %+v", observation)
	}
}

func TestRoomCoordinator_FailureDuringGracePromotesRoomCause(t *testing.T) {
	const participantID = "failed"
	coordinator := newRoomCoordinator(nil, 0, time.Second, nil, nil)
	runtime := &roomParticipantRuntime{
		plan: &roomParticipantPlan{manifest: room.Participant{ID: participantID}},
		lifecycle: &roomParticipantLifecycle{
			stateChanged:    coordinator.progress,
			admissionClosed: coordinator.admissionDone(),
		},
	}
	coordinator.addParticipant(runtime)
	coordinator.beginBoundShutdown(RoomTerminationMaxDurationReached, nil)

	if !runtime.lifecycle.observeTerminal(sessionTerminalObservation{
		Classification:     providers.ErrorClassTransport,
		TerminalReason:     string(messages.TerminalReasonTerminalFailure),
		TerminalProvenance: string(messages.TerminalProvenanceProvider),
		OutputState:        string(messages.TerminalOutputPartial),
		Err:                context.DeadlineExceeded,
		Failure:            true,
	}) {
		t.Fatal("provider failure during grace was not accepted")
	}
	coordinator.fail(roomParticipantFailure(participantID, context.DeadlineExceeded, nil))

	if got := coordinator.reasonSnapshot(); got != RoomTerminationFailed {
		t.Fatalf("room reason = %q, want %q", got, RoomTerminationFailed)
	}
	if coordinator.bound {
		t.Fatal("bound shutdown remained authoritative after a pre-cancellation failure")
	}
	select {
	case <-coordinator.done:
	default:
		t.Fatal("failure promotion did not stop the room")
	}
}

func assertRoomParticipantTerminalManifestMatches(t *testing.T, outputDir string, result RoomResult) {
	t.Helper()
	manifestData := readRoomEvidenceFile(t, filepath.Join(outputDir, RoomEvidenceManifestPath))
	var manifest roomEvidenceManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatalf("decode room terminal manifest: %v", err)
	}
	for id, participant := range result.Participants {
		manifestParticipant, ok := manifest.Participants[id]
		if !ok {
			t.Fatalf("terminal manifest missing participant %q", id)
		}
		if manifestParticipant.TerminationReason != participant.TerminationReason || manifestParticipant.Reason != participant.Reason || manifestParticipant.TerminationTrigger != participant.TerminationTrigger || manifestParticipant.TerminationDisposition != participant.TerminationDisposition || manifestParticipant.Classification != participant.Classification || manifestParticipant.TerminalReason != participant.TerminalReason || manifestParticipant.TerminalProvenance != participant.TerminalProvenance || manifestParticipant.OutputState != participant.OutputState || manifestParticipant.CompletedTurns != participant.TurnsCompleted || manifestParticipant.Connected != participant.Connected || manifestParticipant.Error != participant.Error {
			t.Fatalf("terminal manifest participant %q = %+v, result = %+v", id, manifestParticipant, participant)
		}
	}

	terminated := make(map[string]map[string]string, len(result.Participants))
	bound := make(map[string]map[string]string, len(result.Participants))
	for _, line := range readRoomEvidenceJSONLLines(t, filepath.Join(outputDir, RoomEvidenceTimelinePath)) {
		var entry roomTimelineEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			t.Fatalf("decode room terminal timeline: %v", err)
		}
		switch entry.Event {
		case "participant_terminated":
			terminated[entry.Participant] = entry.Fields
		case "room_bound_shutdown":
			bound[entry.Participant] = entry.Fields
		}
	}
	for id, participant := range result.Participants {
		want := participantTerminalFields(participant)
		for event, fields := range map[string]map[string]string{"participant_terminated": terminated[id], "room_bound_shutdown": bound[id]} {
			if fields == nil {
				t.Fatalf("terminal timeline missing %s for participant %q", event, id)
			}
			for key, wantValue := range want {
				if got := fields[key]; got != wantValue {
					t.Fatalf("terminal timeline %s %q field %q = %q, want %q", event, id, key, got, wantValue)
				}
			}
		}
	}
}
