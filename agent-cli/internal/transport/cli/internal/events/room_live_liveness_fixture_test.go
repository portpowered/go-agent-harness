package events

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeRooms "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	runtimeRoomWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	runtimeSessionWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/wire"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

type roomLiveLivenessFixture struct {
	classification string
	clock          *platformclock.Deterministic
	provider       map[string]*roomLiveSession
	roomService    runtimeRooms.Service
	manifest       runtimeRooms.Manifest
	destination    string
	factoryReady   chan struct{}
	broker         *Broker
	sink           *releasePeerOnLiveness
	allReader      *sseReader
	peerReader     *sseReader
	resultChannel  chan roomLiveResult
	releaseError   error

	diagnosticMu             sync.Mutex
	completedTurnDiagnostics int
	livenessDiagnostic       *runtimeRooms.RoomDiagnosticRecord
}

func newRoomLiveLivenessFixture(t *testing.T, timeoutCase bool) *roomLiveLivenessFixture {
	t.Helper()
	classification, silentEvents := roomLiveLivenessEvents(timeoutCase)
	fixture := &roomLiveLivenessFixture{
		classification: classification,
		clock:          platformclock.NewDeterministic(time.Unix(1700000000, 0), time.Millisecond),
		provider: map[string]*roomLiveSession{
			roomLiveSilentParticipant: newRoomLiveSession(silentEvents...),
			"peer":                    newRoomLiveSession(roomLiveMessage(messages.StreamTypeSessionOpen, messages.NewSessionOpenValue("peer-session", "audio_inference"))),
		},
		factoryReady:  make(chan struct{}, 2),
		resultChannel: make(chan roomLiveResult, 1),
	}
	fixture.manifest = roomLiveLivenessManifest()
	fixture.roomService = runtimeRoomWire.NewService(runtimeRoomWire.Dependencies{
		Live:  fixture.liveService(t),
		Clock: fixture.clock,
	})
	fixture.destination = filepath.Join(t.TempDir(), "evidence")
	fixture.broker = fixture.newBroker(t)
	fixture.openStreams(t)
	fixture.sink = fixture.newSink()
	for _, participant := range fixture.manifest.Participants {
		fixture.broker.PublishRoomEvent(EventParticipantJoined, participant.ID)
	}
	return fixture
}

func roomLiveLivenessEvents(timeoutCase bool) (string, []messages.StreamMessage) {
	classification := roomLiveEmptyResponseClassification
	events := []messages.StreamMessage{
		roomLiveMessage(messages.StreamTypeSessionOpen, messages.NewSessionOpenValue("silent-session", "audio_inference")),
	}
	if timeoutCase {
		return roomLiveTimeoutClassification, append(events,
			roomLiveMessageWithResponse(messages.StreamTypeMessageStart, "response-timeout", messages.NewMessageStartValue()),
		)
	}
	events = append(events,
		roomLiveMessageWithResponse(messages.StreamTypeMessageStart, "response-empty", messages.NewMessageStartValue()),
		roomLiveMessageWithResponse(messages.StreamTypeMessageEnd, "response-empty", messages.NewMessageEndValueWithTerminal(
			messages.TokenUsage{}, messages.TerminalReasonPartialOutput, messages.TerminalProvenanceProvider, messages.TerminalOutputNone,
		)),
	)
	return classification, events
}

func roomLiveLivenessManifest() runtimeRooms.Manifest {
	return runtimeRooms.Manifest{
		SchemaVersion: runtimeRooms.SchemaVersion,
		Room:          runtimeRooms.Room{Interactive: true, MaxTurns: 1},
		Participants: []runtimeRooms.Participant{
			{Kind: runtimeRooms.ParticipantKindAgent, ID: roomLiveSilentParticipant, SystemPrompt: "answer", OpeningPrompt: "start", Provider: "fixture", Model: "fixture", APIKeyEnv: "SILENT_KEY", Tools: []string{}},
			{Kind: runtimeRooms.ParticipantKindAgent, ID: "peer", SystemPrompt: "answer", OpeningPrompt: "start", Provider: "fixture", Model: "fixture", APIKeyEnv: "PEER_KEY", Tools: []string{}},
		},
	}
}

func (f *roomLiveLivenessFixture) liveService(t *testing.T) session.LiveService {
	t.Helper()
	return runtimeSessionWire.NewLiveService(runtimeSessionWire.LiveDependencies{
		InferencerFactory: func(_ context.Context, request session.LiveRequest) (messages.SessionInferencer, error) {
			participant := f.provider[request.ParticipantID]
			if participant == nil {
				return nil, errors.New("unknown room fixture participant")
			}
			if request.Replay.OutputCapturePath == "" {
				return nil, errors.New("room recorder did not provide a capture path")
			}
			if err := os.WriteFile(request.Replay.OutputCapturePath, []byte("[]\n"), 0o600); err != nil {
				return nil, err
			}
			f.factoryReady <- struct{}{}
			return roomLiveInferencer{session: participant}, nil
		},
		Clock: f.clock.Now, Scheduler: f.clock,
	})
}

func (f *roomLiveLivenessFixture) newBroker(t *testing.T) *Broker {
	t.Helper()
	broker, err := New([]string{roomLiveSilentParticipant, "peer"}, Options{Now: f.clock.Now})
	if err != nil {
		t.Fatalf("New broker: %v", err)
	}
	t.Cleanup(func() {
		if err := broker.Close(); err != nil {
			t.Errorf("broker.Close(): %v", err)
		}
	})
	return broker
}

func (f *roomLiveLivenessFixture) openStreams(t *testing.T) {
	t.Helper()
	server := newTestEventServer(t, f.broker)
	t.Cleanup(server.Close)
	allResponse, allReader := openSSE(t, server, "/events")
	f.allReader = allReader
	t.Cleanup(func() {
		if err := allResponse.Body.Close(); err != nil {
			t.Errorf("all-events response Close(): %v", err)
		}
	})
	peerResponse, peerReader := openSSE(t, server, "/events?participant=peer")
	f.peerReader = peerReader
	t.Cleanup(func() {
		if err := peerResponse.Body.Close(); err != nil {
			t.Errorf("peer-events response Close(): %v", err)
		}
	})
}

func (f *roomLiveLivenessFixture) newSink() *releasePeerOnLiveness {
	return &releasePeerOnLiveness{
		delegate: NewLiveSink(f.broker),
		release: func() {
			if err := f.provider["peer"].Close(); err != nil {
				f.diagnosticMu.Lock()
				f.releaseError = err
				f.diagnosticMu.Unlock()
			}
		},
		observed: make(chan struct{}),
		started:  make(chan struct{}),
	}
}

func (f *roomLiveLivenessFixture) startRoom() {
	go func() {
		result, runErr := f.roomService.Run(context.Background(), io.Discard, runtimeRooms.RoomRunOptions{
			Manifest:    f.manifest,
			OutputDir:   f.destination,
			EventSink:   f.sink,
			AudioFormat: runtimeRooms.AudioFormat{},
			OnDiagnostic: func(_ string, record runtimeRooms.RoomDiagnosticRecord) {
				f.recordDiagnostic(record)
			},
			OnParticipantTerminated: func(value runtimeRooms.RoomParticipantResult) {
				f.broker.PublishRoomEvent(EventParticipantTerminated, value.ParticipantID, string(value.TerminationReason))
			},
		})
		f.broker.PublishRoomEvent(EventRunTerminated, RoomParticipantID, string(result.TerminationReason))
		f.resultChannel <- roomLiveResult{value: result, err: runErr}
	}()
}

func (f *roomLiveLivenessFixture) recordDiagnostic(record runtimeRooms.RoomDiagnosticRecord) {
	f.diagnosticMu.Lock()
	defer f.diagnosticMu.Unlock()
	if record.Event == "live_liveness_fault" {
		copy := runtimeRooms.RoomDiagnosticRecord{Event: record.Event, At: record.At, Fields: map[string]string{}}
		for key, value := range record.Fields {
			copy.Fields[key] = value
		}
		f.livenessDiagnostic = &copy
	}
	if record.Event == "live_turn_completed" {
		f.completedTurnDiagnostics++
	}
}

func (f *roomLiveLivenessFixture) advanceTimeout(t *testing.T) {
	t.Helper()
	for range f.provider {
		select {
		case <-f.factoryReady:
		case <-time.After(3 * time.Second):
			t.Fatal("room did not admit all live participants")
		}
	}
	select {
	case <-f.sink.started:
	case <-time.After(3 * time.Second):
		t.Fatal("room did not observe the stalled provider response")
	}
	f.clock.AdvanceBy(10*time.Second + time.Millisecond)
}

func (f *roomLiveLivenessFixture) waitForLiveness(t *testing.T) {
	t.Helper()
	select {
	case <-f.sink.observed:
	case outcome := <-f.resultChannel:
		t.Fatalf("room returned before liveness projection: result=%+v err=%v", outcome.value, outcome.err)
	case <-time.After(3 * time.Second):
		t.Fatal("room did not publish liveness through the live service")
	}
}

func (f *roomLiveLivenessFixture) assertPeerFault(t *testing.T) {
	t.Helper()
	for {
		payload := f.peerReader.next(t)
		if sseString(t, payload, "event") != EventParticipantLivenessFault {
			continue
		}
		if got := sseString(t, payload, "participant_id"); got != roomLiveSilentParticipant {
			t.Fatalf("peer-filtered fault participant = %q, want silent", got)
		}
		if got := sseString(t, payload, "reason"); got != f.classification {
			t.Fatalf("peer-filtered fault reason = %q, want %q", got, f.classification)
		}
		return
	}
}

type roomLiveStreamResult struct {
	events          []map[string]json.RawMessage
	faultIndex      int
	terminatedIndex int
	faultCount      int
}

func (f *roomLiveLivenessFixture) readAllStream(t *testing.T) roomLiveStreamResult {
	t.Helper()
	result := roomLiveStreamResult{faultIndex: -1, terminatedIndex: -1}
	for {
		payload := f.allReader.next(t)
		result.events = append(result.events, payload)
		if sseString(t, payload, "type") != EventRoom {
			continue
		}
		switch sseString(t, payload, "event") {
		case EventParticipantLivenessFault:
			result.faultCount++
			result.faultIndex = len(result.events) - 1
		case EventParticipantTerminated:
			if sseString(t, payload, "participant_id") == roomLiveSilentParticipant {
				result.terminatedIndex = len(result.events) - 1
			}
		case EventRunTerminated:
			return result
		}
	}
}

func (f *roomLiveLivenessFixture) assertStream(t *testing.T, stream roomLiveStreamResult) {
	t.Helper()
	if stream.faultCount != 1 || stream.faultIndex < 0 || stream.terminatedIndex < 0 || stream.faultIndex >= stream.terminatedIndex {
		t.Fatalf("room stream liveness ordering fault_count=%d fault_index=%d terminated_index=%d events=%v", stream.faultCount, stream.faultIndex, stream.terminatedIndex, stream.events)
	}
	joined, terminated := lifecycleParticipants(t, stream.events)
	for _, participantID := range []string{roomLiveSilentParticipant, "peer"} {
		if !joined[participantID] || !terminated[participantID] {
			t.Fatalf("participant %q lifecycle events joined=%v terminated=%v; events=%v", participantID, joined[participantID], terminated[participantID], stream.events)
		}
	}
}

func lifecycleParticipants(t *testing.T, events []map[string]json.RawMessage) (map[string]bool, map[string]bool) {
	t.Helper()
	joined, terminated := map[string]bool{}, map[string]bool{}
	for _, payload := range events {
		if sseString(t, payload, "type") != EventRoom {
			continue
		}
		participantID := sseString(t, payload, "participant_id")
		switch sseString(t, payload, "event") {
		case EventParticipantJoined:
			joined[participantID] = true
		case EventParticipantTerminated:
			terminated[participantID] = true
		case EventRunTerminated:
			if participantID != RoomParticipantID {
				t.Fatalf("run terminal participant = %q, want room", participantID)
			}
		}
	}
	return joined, terminated
}

func (f *roomLiveLivenessFixture) assertResult(t *testing.T) {
	t.Helper()
	select {
	case outcome := <-f.resultChannel:
		if outcome.err != nil {
			t.Fatalf("room run: %v", outcome.err)
		}
		f.assertSilentResult(t, outcome.value)
		f.assertPeerResult(t, outcome.value)
		f.assertDiagnostics(t)
		f.diagnosticMu.Lock()
		releaseErr := f.releaseError
		f.diagnosticMu.Unlock()
		if releaseErr != nil {
			t.Fatalf("peer release: %v", releaseErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("room did not finish after peer release")
	}
}

const (
	roomLiveSilentParticipant           = "silent"
	roomLiveEmptyResponseClassification = "silent_provider_empty_response"
	roomLiveTimeoutClassification       = "silent_provider_timeout"
)

func (f *roomLiveLivenessFixture) assertSilentResult(t *testing.T, result runtimeRooms.RoomResult) {
	t.Helper()
	if result.Reason != runtimeRooms.RoomTerminationStopped {
		t.Fatalf("room reason = %q, want stopped after the peer completes", result.Reason)
	}
	participant := result.Participants[roomLiveSilentParticipant]
	if participant.TerminationReason != runtimeRooms.ParticipantTerminationError || participant.Classification != f.classification {
		t.Fatalf("silent participant result = %+v", participant)
	}
	if participant.TerminalReason != string(messages.TerminalReasonTerminalFailure) || participant.TerminalProvenance != string(messages.TerminalProvenanceSession) || participant.OutputState != string(messages.TerminalOutputNone) {
		t.Fatalf("silent participant terminal metadata = (%q, %q, %q)", participant.TerminalReason, participant.TerminalProvenance, participant.OutputState)
	}
	if participant.TurnsCompleted != 0 || !strings.Contains(participant.Error, f.classification) {
		t.Fatalf("silent participant accounting = turns:%d error:%q", participant.TurnsCompleted, participant.Error)
	}
}

func (f *roomLiveLivenessFixture) assertPeerResult(t *testing.T, result runtimeRooms.RoomResult) {
	t.Helper()
	if peer := result.Participants["peer"]; peer.Classification == f.classification {
		t.Fatalf("peer inherited silent participant classification: %+v", peer)
	}
}

func (f *roomLiveLivenessFixture) assertDiagnostics(t *testing.T) {
	t.Helper()
	f.diagnosticMu.Lock()
	turnDiagnostics, diagnostic := f.completedTurnDiagnostics, f.livenessDiagnostic
	f.diagnosticMu.Unlock()
	if turnDiagnostics != 0 {
		t.Fatalf("silent %s produced completed-turn diagnostics: %d", f.classification, turnDiagnostics)
	}
	if diagnostic == nil || diagnostic.Fields["classification"] != f.classification || diagnostic.Fields["terminal_reason"] != string(messages.TerminalReasonTerminalFailure) || diagnostic.Fields["terminal_provenance"] != string(messages.TerminalProvenanceSession) || diagnostic.Fields["output_state"] != string(messages.TerminalOutputNone) {
		t.Fatalf("silent liveness diagnostic = %+v", diagnostic)
	}
}

func (f *roomLiveLivenessFixture) assertEvidence(t *testing.T) {
	t.Helper()
	manifestValue := readRoomJSON(t, filepath.Join(f.destination, runtimeRooms.RoomReplayBundleManifestPath))
	participants, ok := manifestValue["participants"].(map[string]any)
	if !ok {
		t.Fatalf("manifest participants = %T (%v)", manifestValue["participants"], manifestValue["participants"])
	}
	silent, ok := participants[roomLiveSilentParticipant].(map[string]any)
	if !ok || silent["classification"] != f.classification {
		t.Fatalf("manifest silent participant = %v", participants[roomLiveSilentParticipant])
	}
	f.assertTimeline(t)
}

func (f *roomLiveLivenessFixture) assertTimeline(t *testing.T) {
	t.Helper()
	path := filepath.Join(f.destination, runtimeRooms.RoomEvidenceTimelinePath)
	timelineFile, err := os.Open(path)
	if err != nil {
		t.Fatalf("open room timeline: %v", err)
	}
	defer func() {
		if err := timelineFile.Close(); err != nil {
			t.Errorf("room timeline Close(): %v", err)
		}
	}()
	scanner := bufio.NewScanner(timelineFile)
	timelineFault, timelineTerminated := -1, -1
	for index := 0; scanner.Scan(); index++ {
		var entry struct {
			Event         string            `json:"event"`
			ParticipantID string            `json:"participant_id"`
			Fields        map[string]string `json:"fields"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			t.Fatalf("decode timeline entry: %v", err)
		}
		if entry.ParticipantID != roomLiveSilentParticipant {
			continue
		}
		if entry.Event == "live_liveness_fault" && entry.Fields["classification"] == f.classification {
			timelineFault = index
		}
		if entry.Event == EventParticipantTerminated {
			timelineTerminated = index
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan room timeline: %v", err)
	}
	if timelineFault < 0 || timelineTerminated < 0 || timelineFault >= timelineTerminated {
		t.Fatalf("timeline liveness ordering fault=%d terminated=%d", timelineFault, timelineTerminated)
	}
}
