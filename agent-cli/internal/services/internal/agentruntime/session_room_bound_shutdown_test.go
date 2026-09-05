package agentruntime

import (
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
)

type roomBoundStreamObservation struct {
	participantID string
	message       messages.StreamMessage
}

func TestRoomTrackedSessionBlocksNewWorkAfterAdmission(t *testing.T) {
	admissionClosed := make(chan struct{})
	inner := newRoomTestSession()
	tracked := &roomTrackedSession{Session: inner, admissionClosed: admissionClosed}

	if !tracked.Send(context.Background(), messages.StreamMessage{Type: messages.StreamTypeAudioDelta}) {
		t.Fatal("pre-bound audio was not delivered")
	}
	close(admissionClosed)

	for _, messageType := range []messages.StreamMessageType{
		messages.StreamTypeAudioDelta,
		messages.StreamTypeMessageEnd,
		messages.StreamTypeToolCallEnd,
		messages.StreamTypeResponseCreate,
		messages.StreamTypeSessionUpdate,
	} {
		outcome := tracked.SendWithOutcome(context.Background(), messages.StreamMessage{Type: messageType})
		if outcome.Status != messages.SessionSendCancelled {
			t.Fatalf("post-bound %s status = %q, want %q", messageType, outcome.Status, messages.SessionSendCancelled)
		}
	}
	if !tracked.Send(context.Background(), messages.StreamMessage{Type: messages.StreamTypeResponseCancel}) {
		t.Fatal("response cancellation was not delivered after admission closed")
	}
	if !tracked.Send(context.Background(), messages.StreamMessage{Type: messages.StreamTypeSessionClose}) {
		t.Fatal("session close was not delivered after admission closed")
	}
	if got := inner.sentCountSnapshot(); got != 3 {
		t.Fatalf("underlying session received %d messages, want pre-bound audio plus cancel and close", got)
	}
}

func TestRunRoom_MaxTurnsDrainsResponseAlreadyInFlight(t *testing.T) {
	const (
		activeID = "active"
		peerID   = "peer"
	)
	inferencers := map[string]*roomTestInferencer{
		activeID: {events: []messages.StreamMessage{roomTestSessionOpen(activeID)}},
		peerID:   {events: []messages.StreamMessage{roomTestSessionOpen(peerID)}},
	}
	opts, _ := newRoomTestRunOptions([]string{activeID, peerID}, inferencers)
	opts.Manifest.Room.MaxTurns = 1
	opts.BoundShutdownGrace = 100 * time.Millisecond

	ready := make(chan string, 2)
	bound := make(chan RoomTerminationReason, 1)
	stream := make(chan roomBoundStreamObservation, 32)
	diagnostics := make(chan SessionDiagnosticRecord, 32)
	opts.OnParticipantReady = func(result RoomParticipantReady) { ready <- result.ParticipantID }
	opts.onRoomBoundShutdown = func(reason RoomTerminationReason) { bound <- reason }
	opts.onParticipantStream = func(participantID string, message messages.StreamMessage) {
		stream <- roomBoundStreamObservation{participantID: participantID, message: message}
	}
	opts.OnDiagnostic = func(_ string, record SessionDiagnosticRecord) { diagnostics <- record }

	outcome := runRoomBoundTest(t, opts)
	for range []int{0, 1} {
		select {
		case <-ready:
		case <-time.After(time.Second):
			t.Fatal("room participants did not become ready")
		}
	}
	activeSession := waitRoomBoundTestSession(t, inferencers[activeID])
	peerSession := waitRoomBoundTestSession(t, inferencers[peerID])

	// The active participant has already completed one turn and is midway
	// through its next response when the peer completes its first turn. That
	// peer terminal event is what reaches the room max-turn bound.
	writeRoomBoundTestEvents(t, activeSession, roomTestResponse("active first"))
	awaitRoomBoundTestMessage(t, stream, activeID, messages.StreamTypeMessageEnd)
	writeRoomBoundTestEvents(t, activeSession, []messages.StreamMessage{
		{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()},
	})
	awaitRoomBoundTestMessage(t, stream, activeID, messages.StreamTypeMessageStart)
	writeRoomBoundTestEvents(t, peerSession, roomTestResponse("peer first"))
	awaitRoomBoundTestMessage(t, stream, peerID, messages.StreamTypeMessageEnd)
	select {
	case reason := <-bound:
		if reason != RoomTerminationMaxTurnsReached {
			t.Fatalf("bound reason = %q, want %q", reason, RoomTerminationMaxTurnsReached)
		}
	case <-time.After(time.Second):
		t.Fatal("max-turn bound did not close admission")
	}

	// The response that was active before the bound is allowed to finish in
	// grace. Its terminal event must not be mistaken for a new room failure.
	writeRoomBoundTestEvents(t, activeSession, []messages.StreamMessage{
		{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("active final")},
		roomTestMessageEnd(),
	})
	awaitRoomBoundTestMessage(t, stream, activeID, messages.StreamTypeMessageEnd)

	result, err := awaitRoomBoundTestResult(t, outcome)
	if err != nil {
		t.Fatalf("max-turn bound drain: %v", err)
	}
	if result.Reason != RoomTerminationMaxTurnsReached {
		t.Fatalf("room reason = %q, want %q", result.Reason, RoomTerminationMaxTurnsReached)
	}
	for _, id := range []string{activeID, peerID} {
		participant := result.Participants[id]
		if participant.Reason != ParticipantTerminationEnded || participant.Error != "" {
			t.Fatalf("participant %q = %+v, want ended with empty error", id, participant)
		}
	}
	active := result.Participants[activeID]
	if active.TerminationTrigger != ParticipantTerminationTriggerMaxTurnsReachedMidResponse || active.TerminationDisposition != ParticipantTerminationDispositionCompletedDuringGrace || active.Classification != "" || active.TerminalReason != string(messages.TerminalReasonProviderAuthoredCompletion) || active.TerminalProvenance != string(messages.TerminalProvenanceProvider) || active.OutputState != string(messages.TerminalOutputComplete) {
		t.Fatalf("active bound completion metadata = %+v", active)
	}
	peer := result.Participants[peerID]
	if peer.TerminationTrigger != ParticipantTerminationTriggerMaxTurnsReached || peer.TerminationDisposition != ParticipantTerminationDispositionCompleted || peer.TerminalReason != string(messages.TerminalReasonProviderAuthoredCompletion) || peer.TerminalProvenance != string(messages.TerminalProvenanceProvider) || peer.OutputState != string(messages.TerminalOutputComplete) {
		t.Fatalf("peer bound completion metadata = %+v", peer)
	}
	for {
		select {
		case record := <-diagnostics:
			if record.Event == SessionDiagnosticEventFailure {
				t.Fatalf("room-bound drain emitted session failure: %+v", record)
			}
		default:
			return
		}
	}
}

func TestRunRoom_BoundGraceExpiryCancelsActiveResponseCleanly(t *testing.T) {
	tests := []struct {
		name       string
		bound      func(*RoomRunOptions)
		wantReason RoomTerminationReason
	}{
		{
			name: "turn",
			bound: func(opts *RoomRunOptions) {
				opts.Manifest.Room.MaxTurns = 1
			},
			wantReason: RoomTerminationMaxTurnsReached,
		},
		{
			name: "duration",
			bound: func(opts *RoomRunOptions) {
				opts.Manifest.Room.MaxDuration = 150 * time.Millisecond
			},
			wantReason: RoomTerminationMaxDurationReached,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			const activeID = "active"
			const peerID = "peer"
			inferencers := map[string]*roomTestInferencer{
				activeID: {events: []messages.StreamMessage{roomTestSessionOpen(activeID)}},
				peerID:   {events: []messages.StreamMessage{roomTestSessionOpen(peerID)}},
			}
			opts, _ := newRoomTestRunOptions([]string{activeID, peerID}, inferencers)
			opts.OutputDir = filepath.Join(t.TempDir(), "room-run")
			opts.BoundShutdownGrace = 40 * time.Millisecond
			testCase.bound(&opts)

			ready := make(chan string, 2)
			bound := make(chan RoomTerminationReason, 1)
			stream := make(chan roomBoundStreamObservation, 16)
			diagnostics := make(chan struct {
				participantID string
				record        SessionDiagnosticRecord
			}, 128)
			opts.OnParticipantReady = func(result RoomParticipantReady) { ready <- result.ParticipantID }
			opts.onRoomBoundShutdown = func(reason RoomTerminationReason) { bound <- reason }
			opts.onParticipantStream = func(participantID string, message messages.StreamMessage) {
				stream <- roomBoundStreamObservation{participantID: participantID, message: message}
			}
			opts.OnDiagnostic = func(participantID string, record SessionDiagnosticRecord) {
				diagnostics <- struct {
					participantID string
					record        SessionDiagnosticRecord
				}{participantID: participantID, record: record}
			}

			outcome := runRoomBoundTest(t, opts)
			for range []int{0, 1} {
				select {
				case <-ready:
				case <-time.After(time.Second):
					t.Fatal("room participants did not become ready")
				}
			}
			activeSession := waitRoomBoundTestSession(t, inferencers[activeID])
			peerSession := waitRoomBoundTestSession(t, inferencers[peerID])
			if testCase.name == "turn" {
				writeRoomBoundTestEvents(t, activeSession, roomTestResponse("active first"))
				awaitRoomBoundTestMessage(t, stream, activeID, messages.StreamTypeMessageEnd)
			}
			// Ensure the response is observable before waiting for either bound.
			writeRoomBoundTestEvents(t, activeSession, []messages.StreamMessage{
				{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()},
			})
			awaitRoomBoundTestMessage(t, stream, activeID, messages.StreamTypeMessageStart)
			if testCase.name == "turn" {
				writeRoomBoundTestEvents(t, peerSession, roomTestResponse("peer first"))
				awaitRoomBoundTestMessage(t, stream, peerID, messages.StreamTypeMessageEnd)
			}

			select {
			case reason := <-bound:
				if reason != testCase.wantReason {
					t.Fatalf("bound reason = %q, want %q", reason, testCase.wantReason)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("room bound did not close admission")
			}

			result, err := awaitRoomBoundTestResult(t, outcome)
			if err != nil {
				t.Fatalf("bound cancellation: %v", err)
			}
			if got := activeSession.sentTypeCountSnapshot(messages.StreamTypeResponseCancel); got != 1 {
				t.Fatalf("active response cancellations = %d, want exactly one", got)
			}
			if got := peerSession.sentTypeCountSnapshot(messages.StreamTypeResponseCancel); got != 0 {
				t.Fatalf("peer response cancellations = %d, want none", got)
			}
			if result.Reason != testCase.wantReason {
				t.Fatalf("room reason = %q, want %q", result.Reason, testCase.wantReason)
			}
			assertRoomParticipantTerminalManifestMatches(t, opts.OutputDir, result)
			for _, id := range []string{activeID, peerID} {
				participant := result.Participants[id]
				if participant.Reason != ParticipantTerminationEnded || participant.Error != "" {
					t.Fatalf("participant %q = %+v, want ended with empty error", id, participant)
				}
			}
			boundDiagnostics := make(map[string]SessionDiagnosticRecord, 2)
			for len(boundDiagnostics) < 2 {
				select {
				case diagnostic := <-diagnostics:
					if diagnostic.record.Event == SessionDiagnosticEventRoomBound {
						boundDiagnostics[diagnostic.participantID] = diagnostic.record
					}
				case <-time.After(time.Second):
					t.Fatalf("room-bound diagnostics = %v, want one per participant", boundDiagnostics)
				}
			}
			for _, id := range []string{activeID, peerID} {
				diagnostic, ok := boundDiagnostics[id]
				participant := result.Participants[id]
				if !ok {
					t.Fatalf("room-bound diagnostic for %q = %+v", id, diagnostic)
				}
				for field, want := range participantTerminalFields(participant) {
					if got := diagnostic.Fields[field]; got != want {
						t.Fatalf("room-bound diagnostic for %q field %q = %q, want %q (fields=%v)", id, field, got, want, diagnostic.Fields)
					}
				}
			}
			for _, id := range []string{activeID, peerID} {
				participant := result.Participants[id]
				if id == activeID {
					wantTrigger := ParticipantTerminationTriggerMaxDurationReachedMidResponse
					if testCase.name == "turn" {
						wantTrigger = ParticipantTerminationTriggerMaxTurnsReachedMidResponse
					}
					if participant.TerminationTrigger != wantTrigger || participant.TerminationDisposition != ParticipantTerminationDispositionCancelledAfterGrace || participant.Classification != RoomBoundCancelledClassification || participant.TerminalReason != string(messages.TerminalReasonCancellation) || participant.TerminalProvenance != string(messages.TerminalProvenanceRoom) {
						t.Fatalf("participant %q cancellation metadata = %+v", id, participant)
					}
					if participant.OutputState != string(messages.TerminalOutputNone) && participant.OutputState != string(messages.TerminalOutputPartial) {
						t.Fatalf("participant %q cancellation output state = %q, want none or partial", id, participant.OutputState)
					}
					continue
				}
				wantTrigger := ParticipantTerminationTriggerMaxDurationReached
				if testCase.name == "turn" {
					wantTrigger = ParticipantTerminationTriggerMaxTurnsReached
				}
				if participant.TerminationTrigger != wantTrigger || participant.TerminationDisposition != ParticipantTerminationDispositionCompleted {
					t.Fatalf("participant %q bound metadata = %+v", id, participant)
				}
			}
		})
	}
}

func TestRunRoom_BoundGraceProviderFailureRemainsAuthoritative(t *testing.T) {
	const (
		activeID = "active"
		peerID   = "peer"
		secret   = "secret-active"
	)
	inferencers := map[string]*roomTestInferencer{
		activeID: {events: []messages.StreamMessage{roomTestSessionOpen(activeID)}},
		peerID:   {events: []messages.StreamMessage{roomTestSessionOpen(peerID)}},
	}
	opts, _ := newRoomTestRunOptions([]string{activeID, peerID}, inferencers)
	opts.OutputDir = filepath.Join(t.TempDir(), "room-run")
	opts.Manifest.Room.MaxTurns = 1
	opts.BoundShutdownGrace = 250 * time.Millisecond
	ready := make(chan string, 2)
	bound := make(chan RoomTerminationReason, 1)
	stream := make(chan roomBoundStreamObservation, 16)
	diagnostics := make(chan SessionDiagnosticRecord, 32)
	opts.OnParticipantReady = func(result RoomParticipantReady) { ready <- result.ParticipantID }
	opts.onRoomBoundShutdown = func(reason RoomTerminationReason) {
		bound <- reason
		sessions := inferencers[activeID].sessionsSnapshot()
		if len(sessions) != 1 {
			return
		}
		failure := messages.NewErrorValueWithTerminal(
			"provider failed with "+secret,
			providers.ErrorClassTransport,
			messages.TerminalReasonTerminalFailure,
			messages.TerminalProvenanceProvider,
			messages.TerminalOutputPartial,
		)
		failure.Code = "provider_mid_response_failure"
		_ = sessions[0].receive.Write(context.Background(), messages.StreamMessage{Type: messages.StreamTypeError, Value: failure})
	}
	opts.onParticipantStream = func(participantID string, message messages.StreamMessage) {
		stream <- roomBoundStreamObservation{participantID: participantID, message: message}
	}
	opts.OnDiagnostic = func(_ string, record SessionDiagnosticRecord) { diagnostics <- record }

	outcome := runRoomBoundTest(t, opts)
	for range []int{0, 1} {
		select {
		case <-ready:
		case <-time.After(time.Second):
			t.Fatal("room participants did not become ready")
		}
	}
	activeSession := waitRoomBoundTestSession(t, inferencers[activeID])
	peerSession := waitRoomBoundTestSession(t, inferencers[peerID])
	writeRoomBoundTestEvents(t, activeSession, roomTestResponse("active first"))
	awaitRoomBoundTestMessage(t, stream, activeID, messages.StreamTypeMessageEnd)
	writeRoomBoundTestEvents(t, activeSession, []messages.StreamMessage{
		{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()},
	})
	awaitRoomBoundTestMessage(t, stream, activeID, messages.StreamTypeMessageStart)
	writeRoomBoundTestEvents(t, peerSession, roomTestResponse("peer first"))
	awaitRoomBoundTestMessage(t, stream, peerID, messages.StreamTypeMessageEnd)
	select {
	case reason := <-bound:
		if reason != RoomTerminationMaxTurnsReached {
			t.Fatalf("bound reason = %q, want %q", reason, RoomTerminationMaxTurnsReached)
		}
	case <-time.After(time.Second):
		t.Fatal("max-turn bound did not start grace")
	}

	result, err := awaitRoomBoundTestResult(t, outcome)
	if err == nil {
		t.Fatal("provider failure during grace returned nil room error")
	}
	if result.TerminationReason != RoomTerminationFailed {
		t.Fatalf("room reason = %q, want %q", result.TerminationReason, RoomTerminationFailed)
	}
	failed := result.Participants[activeID]
	if failed.Reason != ParticipantTerminationError || failed.Error == "" || failed.TerminationTrigger != ParticipantTerminationTriggerSessionFailure || failed.TerminationDisposition != ParticipantTerminationDispositionFailed || failed.Classification != providers.ErrorClassTransport || failed.TerminalReason != string(messages.TerminalReasonTerminalFailure) || failed.TerminalProvenance != string(messages.TerminalProvenanceProvider) || failed.OutputState != string(messages.TerminalOutputPartial) {
		t.Fatalf("failure during grace participant = %+v", failed)
	}
	if strings.Contains(failed.Error, secret) || strings.Contains(result.Error, secret) {
		t.Fatalf("provider secret leaked in result: participant=%q room=%q", failed.Error, result.Error)
	}
	manifestData := readRoomEvidenceFile(t, filepath.Join(opts.OutputDir, RoomEvidenceManifestPath))
	if strings.Contains(string(manifestData), secret) {
		t.Fatalf("provider secret leaked in run manifest: %s", manifestData)
	}
	var manifest roomEvidenceManifest
	if decodeErr := json.Unmarshal(manifestData, &manifest); decodeErr != nil {
		t.Fatalf("decode run manifest: %v", decodeErr)
	}
	manifestFailure := manifest.Participants[activeID]
	if manifestFailure.TerminationReason != failed.TerminationReason || manifestFailure.TerminationTrigger != failed.TerminationTrigger || manifestFailure.TerminationDisposition != failed.TerminationDisposition || manifestFailure.Classification != failed.Classification || manifestFailure.TerminalReason != failed.TerminalReason || manifestFailure.TerminalProvenance != failed.TerminalProvenance || manifestFailure.OutputState != failed.OutputState || manifestFailure.Error != failed.Error {
		t.Fatalf("manifest failure = %+v, result = %+v", manifestFailure, failed)
	}
	failureCount := 0
	for {
		select {
		case record := <-diagnostics:
			if record.Event == SessionDiagnosticEventFailure {
				failureCount++
			}
		default:
			if failureCount != 1 {
				t.Fatalf("session failure diagnostics = %d, want exactly one", failureCount)
			}
			return
		}
	}
}

func runRoomBoundTest(t *testing.T, opts RoomRunOptions) <-chan roomTestRunOutcome {
	t.Helper()
	outcome := make(chan roomTestRunOutcome, 1)
	go func() {
		result, err := RunRoomWithResult(context.Background(), io.Discard, opts)
		outcome <- roomTestRunOutcome{result: result, err: err}
	}()
	return outcome
}

func waitRoomBoundTestSession(t *testing.T, inferencer *roomTestInferencer) *roomTestSession {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for {
		sessions := inferencer.sessionsSnapshot()
		if len(sessions) == 1 {
			return sessions[0]
		}
		select {
		case <-deadline.C:
			t.Fatalf("room test session count = %d, want one", len(sessions))
		default:
			time.Sleep(time.Millisecond)
		}
	}
}

func writeRoomBoundTestEvents(t *testing.T, session *roomTestSession, events []messages.StreamMessage) {
	t.Helper()
	for _, event := range events {
		if !session.receive.Write(context.Background(), event) {
			t.Fatalf("write room test event %s", event.Type)
		}
	}
}

func awaitRoomBoundTestMessage(t *testing.T, events <-chan roomBoundStreamObservation, participantID string, messageType messages.StreamMessageType) {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for {
		select {
		case event := <-events:
			if event.participantID == participantID && event.message.Type == messageType {
				return
			}
		case <-deadline.C:
			t.Fatalf("did not observe %s for participant %q", messageType, participantID)
		}
	}
}

func awaitRoomBoundTestResult(t *testing.T, outcome <-chan roomTestRunOutcome) (RoomResult, error) {
	t.Helper()
	select {
	case result := <-outcome:
		return result.result, result.err
	case <-time.After(2 * time.Second):
		t.Fatal("room did not terminate after bound grace")
		return RoomResult{}, nil
	}
}
