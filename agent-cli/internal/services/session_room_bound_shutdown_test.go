package services

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
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
			opts.BoundShutdownGrace = 40 * time.Millisecond
			testCase.bound(&opts)

			ready := make(chan string, 2)
			bound := make(chan RoomTerminationReason, 1)
			stream := make(chan roomBoundStreamObservation, 16)
			opts.OnParticipantReady = func(result RoomParticipantReady) { ready <- result.ParticipantID }
			opts.onRoomBoundShutdown = func(reason RoomTerminationReason) { bound <- reason }
			opts.onParticipantStream = func(participantID string, message messages.StreamMessage) {
				stream <- roomBoundStreamObservation{participantID: participantID, message: message}
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
			if result.Reason != testCase.wantReason {
				t.Fatalf("room reason = %q, want %q", result.Reason, testCase.wantReason)
			}
			for _, id := range []string{activeID, peerID} {
				participant := result.Participants[id]
				if participant.Reason != ParticipantTerminationEnded || participant.Error != "" {
					t.Fatalf("participant %q = %+v, want ended with empty error", id, participant)
				}
			}
		})
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
