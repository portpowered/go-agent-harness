package agentruntime

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

type roomSSEReader struct {
	scanner *bufio.Scanner
}

func newRoomSSEReader(body io.Reader) *roomSSEReader {
	return &roomSSEReader{scanner: bufio.NewScanner(body)}
}

func (r *roomSSEReader) next(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	for r.scanner.Scan() {
		line := r.scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var payload map[string]json.RawMessage
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &payload); err != nil {
			t.Fatalf("decode SSE payload %q: %v", line, err)
		}
		return payload
	}
	if err := r.scanner.Err(); err != nil {
		t.Fatalf("read SSE payload: %v", err)
	}
	t.Fatal("SSE stream ended before the next event")
	return nil
}

func roomSSEString(t *testing.T, payload map[string]json.RawMessage, field string) string {
	t.Helper()
	var value string
	if err := json.Unmarshal(payload[field], &value); err != nil {
		t.Fatalf("decode SSE %s: %v", field, err)
	}
	return value
}

func TestRoomEventBroker_ServesContractAndFiltersAudio(t *testing.T) {
	const (
		participant = "assistant"
		stamp       = "2026-08-26T23:00:00Z"
	)
	broker, err := NewRoomEventBrokerWithOptions([]string{"customer", participant}, RoomStreamBrokerOptions{
		Now: func() time.Time { return time.Date(2026, 8, 26, 23, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("NewRoomEventBroker: %v", err)
	}
	server := httptest.NewServer(broker)
	defer server.Close()
	defer broker.Close()

	response, err := http.Get(server.URL + "/events")
	if err != nil {
		t.Fatalf("GET /events: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET /events status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	if got := response.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Fatalf("content type = %q, want text/event-stream", got)
	}
	if got := response.Header.Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("access-control-allow-origin = %q, want wildcard for local visualizer", got)
	}
	reader := newRoomSSEReader(response.Body)

	broker.RecordDiagnostic(participant, SessionDiagnosticRecord{
		Event:  SessionDiagnosticEventTurn,
		Fields: map[string]string{fieldTurnIndex: "3", fieldOutputAudioBytes: "48200"},
	})
	broker.ObserveStream(participant, messages.StreamMessage{
		Type:  messages.StreamTypeAudioDelta,
		Value: messages.NewAudioDeltaValue([]byte{1, 2, 3, 4}),
	})
	broker.ObserveStream(participant, messages.StreamMessage{
		Type:  messages.StreamTypeTranscriptDelta,
		Value: messages.NewTranscriptDeltaValue("so when can I expect"),
	})
	broker.ObserveStream(participant, messages.StreamMessage{
		Type:  messages.StreamTypeTranscriptEnd,
		Value: messages.NewTranscriptEndValue("so when can I expect the refund to post?"),
	})
	broker.PublishRoomEvent(RoomStreamEventRunTerminated, "", string(RoomTerminationStopped))

	diagnostic := reader.next(t)
	if roomSSEString(t, diagnostic, "type") != RoomStreamEventTypeDiagnostic || roomSSEString(t, diagnostic, "participant_id") != participant {
		t.Fatalf("diagnostic envelope = %v", diagnostic)
	}
	var fields map[string]string
	if err := json.Unmarshal(diagnostic["fields"], &fields); err != nil {
		t.Fatalf("diagnostic fields: %v", err)
	}
	if fields[fieldTurnIndex] != "3" || roomSSEString(t, diagnostic, "event") != SessionDiagnosticEventTurn {
		t.Fatalf("diagnostic payload = %v fields=%v", diagnostic, fields)
	}
	if roomSSEString(t, diagnostic, "ts") != stamp {
		t.Fatalf("diagnostic timestamp = %q, want %q", roomSSEString(t, diagnostic, "ts"), stamp)
	}

	delta := reader.next(t)
	if roomSSEString(t, delta, "type") != RoomStreamEventTypeTranscriptDelta || roomSSEString(t, delta, "text") != "so when can I expect" {
		t.Fatalf("transcript delta = %v", delta)
	}
	if _, containsAudio := delta["content"]; containsAudio {
		t.Fatalf("transcript delta exposed raw audio: %v", delta)
	}

	transcriptEnd := reader.next(t)
	if roomSSEString(t, transcriptEnd, "type") != RoomStreamEventTypeTranscriptEnd || roomSSEString(t, transcriptEnd, "full_text") != "so when can I expect the refund to post?" {
		t.Fatalf("transcript end = %v", transcriptEnd)
	}

	roomEvent := reader.next(t)
	if roomSSEString(t, roomEvent, "type") != RoomStreamEventTypeRoom || roomSSEString(t, roomEvent, "event") != RoomStreamEventRunTerminated || roomSSEString(t, roomEvent, "participant_id") != RoomStreamRoomParticipantID || roomSSEString(t, roomEvent, "reason") != string(RoomTerminationStopped) {
		t.Fatalf("room event = %v", roomEvent)
	}
}

func TestRoomEventBroker_BroadcastsParticipantLivenessFaultToFilteredPeer(t *testing.T) {
	broker, err := NewRoomEventBroker([]string{"silent", "peer"})
	if err != nil {
		t.Fatalf("NewRoomEventBroker: %v", err)
	}
	defer broker.Close()
	server := httptest.NewServer(broker)
	defer server.Close()

	open := func(path string) (*http.Response, *roomSSEReader) {
		t.Helper()
		requestContext, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		t.Cleanup(cancel)
		request, err := http.NewRequestWithContext(requestContext, http.MethodGet, server.URL+path, nil)
		if err != nil {
			t.Fatalf("create room stream request: %v", err)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		if response.StatusCode != http.StatusOK {
			response.Body.Close()
			t.Fatalf("GET %s status = %d, want %d", path, response.StatusCode, http.StatusOK)
		}
		return response, newRoomSSEReader(response.Body)
	}

	allResponse, allReader := open("/events")
	defer allResponse.Body.Close()
	peerResponse, peerReader := open("/events?participant=peer")
	defer peerResponse.Body.Close()

	broker.PublishParticipantLivenessFault("silent", SessionSilentProviderTimeoutClassification)
	for name, payload := range map[string]map[string]json.RawMessage{
		"unfiltered":    allReader.next(t),
		"peer-filtered": peerReader.next(t),
	} {
		if roomSSEString(t, payload, "type") != RoomStreamEventTypeRoom || roomSSEString(t, payload, "event") != RoomStreamEventParticipantLivenessFault {
			t.Fatalf("%s liveness event = %v", name, payload)
		}
		if roomSSEString(t, payload, "participant_id") != "silent" || roomSSEString(t, payload, "reason") != SessionSilentProviderTimeoutClassification {
			t.Fatalf("%s liveness event attribution = %v", name, payload)
		}
	}
}

func TestRunRoom_EmptyLivenessFaultPrecedesParticipantTermination(t *testing.T) {
	const participantID = "silent"
	ids := []string{"silent", "peer"}
	inferencers := map[string]*roomTestInferencer{
		participantID: {events: []messages.StreamMessage{
			roomTestSessionOpen(participantID),
			{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()},
			{
				Type:       messages.StreamTypeMessageEnd,
				Role:       messages.RoleAssistant,
				ResponseID: "empty-response",
				Value: messages.NewMessageEndValueWithTerminal(
					messages.TokenUsage{},
					messages.TerminalReasonPartialOutput,
					messages.TerminalProvenanceProvider,
					messages.TerminalOutputNone,
				),
			},
		}},
		"peer": {events: []messages.StreamMessage{roomTestSessionOpen("peer")}},
	}
	// The room stream includes the viable sibling as well as the failed
	// participant; the sibling is held open until the fault is observed.
	broker, err := NewRoomEventBroker(ids)
	if err != nil {
		t.Fatalf("NewRoomEventBroker: %v", err)
	}
	defer broker.Close()
	server := httptest.NewServer(broker)
	defer server.Close()
	requestContext, cancelRequest := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelRequest()
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, server.URL+"/events", nil)
	if err != nil {
		t.Fatalf("create room stream request: %v", err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("GET /events: %v", err)
	}
	defer response.Body.Close()
	reader := newRoomSSEReader(response.Body)

	opts, _ := newRoomTestRunOptions(ids, inferencers)
	opts.Manifest.Room.MaxDuration = time.Hour
	opts.OutputDir = filepath.Join(t.TempDir(), "empty-liveness-room")
	opts.Stream = broker
	roomContext, cancelRoom := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelRoom()
	outcome := make(chan roomTestRunOutcome, 1)
	go func() {
		result, runErr := RunRoomWithResult(roomContext, io.Discard, opts)
		outcome <- roomTestRunOutcome{result: result, err: runErr}
	}()

	roomEvents := make([]map[string]json.RawMessage, 0)
	peerReleased := false
	failureDiagnostic := false
	for {
		payload := reader.next(t)
		roomEvents = append(roomEvents, payload)
		if roomSSEString(t, payload, "type") == RoomStreamEventTypeDiagnostic && roomSSEString(t, payload, "participant_id") == participantID && roomSSEString(t, payload, "event") == SessionDiagnosticEventFailure {
			var fields map[string]string
			if err := json.Unmarshal(payload["fields"], &fields); err != nil {
				t.Fatalf("decode empty liveness diagnostic: %v", err)
			}
			if fields[fieldClassification] != SessionSilentProviderEmptyResponseClassification {
				t.Fatalf("empty liveness diagnostic fields = %v", fields)
			}
			failureDiagnostic = true
		}
		if roomSSEString(t, payload, "type") != RoomStreamEventTypeRoom {
			continue
		}
		event := roomSSEString(t, payload, "event")
		if event == RoomStreamEventParticipantLivenessFault && !peerReleased {
			if roomSSEString(t, payload, "participant_id") != participantID || roomSSEString(t, payload, "reason") != SessionSilentProviderEmptyResponseClassification {
				t.Fatalf("empty liveness event = %v", payload)
			}
			peerSessions := inferencers["peer"].sessionsSnapshot()
			if len(peerSessions) != 1 {
				t.Fatalf("peer sessions = %d, want one before releasing the viable sibling", len(peerSessions))
			}
			peerSessions[0].end()
			peerReleased = true
		}
		if event == RoomStreamEventRunTerminated {
			break
		}
	}
	if !peerReleased {
		t.Fatal("room terminated without publishing the empty-response liveness fault")
	}
	if !failureDiagnostic {
		t.Fatal("empty provider room did not publish its typed session failure diagnostic")
	}

	var got roomTestRunOutcome
	select {
	case got = <-outcome:
	case <-time.After(2 * time.Second):
		t.Fatal("empty provider room did not terminate promptly")
	}
	if got.err != nil || got.result.Reason != RoomTerminationStopped {
		t.Fatalf("empty provider room outcome = %+v, err=%v", got.result, got.err)
	}
	participant, ok := got.result.Participants[participantID]
	if !ok {
		t.Fatalf("room result is missing %q: result=%+v err=%v events=%v", participantID, got.result, got.err, roomEvents)
	}
	if participant.Classification != SessionSilentProviderEmptyResponseClassification || participant.TurnsCompleted != 0 {
		t.Fatalf("empty participant result = %+v, want typed zero-turn failure", participant)
	}
	faultIndex := -1
	terminatedIndex := -1
	faultCount := 0
	for index, payload := range roomEvents {
		if roomSSEString(t, payload, "type") != RoomStreamEventTypeRoom {
			continue
		}
		event := roomSSEString(t, payload, "event")
		switch event {
		case RoomStreamEventParticipantLivenessFault:
			faultCount++
			faultIndex = index
			if roomSSEString(t, payload, "participant_id") != participantID || roomSSEString(t, payload, "reason") != SessionSilentProviderEmptyResponseClassification {
				t.Fatalf("empty liveness event = %v", payload)
			}
		case RoomStreamEventParticipantTerminated:
			if roomSSEString(t, payload, "participant_id") == participantID {
				terminatedIndex = index
			}
		}
	}
	if faultCount != 1 || faultIndex < 0 || terminatedIndex < 0 || faultIndex >= terminatedIndex {
		t.Fatalf("room event ordering fault_count=%d fault_index=%d terminated_index=%d events=%v", faultCount, faultIndex, terminatedIndex, roomEvents)
	}

	manifestData := readRoomEvidenceFile(t, filepath.Join(opts.OutputDir, RoomEvidenceManifestPath))
	var manifest roomEvidenceManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatalf("decode empty liveness manifest: %v", err)
	}
	if got := manifest.Participants[participantID].Classification; got != SessionSilentProviderEmptyResponseClassification {
		t.Fatalf("evidence manifest classification = %q, want %q", got, SessionSilentProviderEmptyResponseClassification)
	}
	timeline := readRoomEvidenceJSONLLines(t, filepath.Join(opts.OutputDir, RoomEvidenceTimelinePath))
	timelineFaultIndex := -1
	timelineTerminatedIndex := -1
	for index, line := range timeline {
		var entry roomTimelineEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			t.Fatalf("decode liveness timeline entry: %v", err)
		}
		switch entry.Event {
		case RoomStreamEventParticipantLivenessFault:
			if entry.Participant == participantID && entry.Fields["reason"] == SessionSilentProviderEmptyResponseClassification {
				timelineFaultIndex = index
			}
		case RoomStreamEventParticipantTerminated:
			if entry.Participant == participantID {
				timelineTerminatedIndex = index
			}
		}
	}
	if timelineFaultIndex < 0 || timelineTerminatedIndex < 0 || timelineFaultIndex >= timelineTerminatedIndex {
		t.Fatalf("evidence timeline ordering fault_index=%d terminated_index=%d entries=%v", timelineFaultIndex, timelineTerminatedIndex, timeline)
	}
}

func TestRunRoom_SilentProviderFaultReachesPeerFilteredStream(t *testing.T) {
	ids := []string{"silent", "peer"}
	inferencers := map[string]*roomTestInferencer{
		"silent": {events: []messages.StreamMessage{roomTestSessionOpen("silent")}},
		"peer":   {events: []messages.StreamMessage{roomTestSessionOpen("peer")}},
	}
	peerConnected := make(chan struct{})
	inferencers["peer"].onConnect = func() { close(peerConnected) }
	livenessClock := &livenessTestClock{created: make(chan struct{}, 4)}
	broker, err := NewRoomEventBroker(ids)
	if err != nil {
		t.Fatalf("NewRoomEventBroker: %v", err)
	}
	defer broker.Close()
	server := httptest.NewServer(broker)
	defer server.Close()
	open := func(path string) (*http.Response, *roomSSEReader) {
		t.Helper()
		requestContext, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		t.Cleanup(cancel)
		request, err := http.NewRequestWithContext(requestContext, http.MethodGet, server.URL+path, nil)
		if err != nil {
			t.Fatalf("create room stream request: %v", err)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		if response.StatusCode != http.StatusOK {
			response.Body.Close()
			t.Fatalf("GET %s status = %d, want %d", path, response.StatusCode, http.StatusOK)
		}
		return response, newRoomSSEReader(response.Body)
	}
	allResponse, allReader := open("/events")
	defer allResponse.Body.Close()
	peerResponse, peerReader := open("/events?participant=peer")
	defer peerResponse.Body.Close()

	opts, _ := newRoomTestRunOptions(ids, inferencers)
	opts.Manifest.Room.MaxDuration = time.Hour
	opts.Manifest.Participants[0].OpeningPrompt = "the silent provider needs a response"
	opts.LivenessClock = livenessClock
	opts.Stream = broker
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	outcome := make(chan roomTestRunOutcome, 1)
	go func() {
		result, runErr := RunRoomWithResult(ctx, io.Discard, opts)
		outcome <- roomTestRunOutcome{result: result, err: runErr}
	}()

	select {
	case <-livenessClock.created:
	case <-ctx.Done():
		t.Fatal("silent participant watchdog was not armed")
	}
	select {
	case <-peerConnected:
	case <-ctx.Done():
		t.Fatal("peer participant did not connect")
	}
	if !livenessClock.fireLatest() {
		t.Fatal("silent participant watchdog did not fire")
	}

	var peerFault map[string]json.RawMessage
	for peerFault == nil {
		payload := peerReader.next(t)
		if roomSSEString(t, payload, "type") == RoomStreamEventTypeRoom && roomSSEString(t, payload, "event") == RoomStreamEventParticipantLivenessFault {
			peerFault = payload
		}
	}
	if roomSSEString(t, peerFault, "participant_id") != "silent" || roomSSEString(t, peerFault, "reason") != SessionSilentProviderTimeoutClassification {
		t.Fatalf("peer-filtered fault = %v, want silent timeout", peerFault)
	}
	peerSessions := inferencers["peer"].sessionsSnapshot()
	if len(peerSessions) != 1 {
		t.Fatalf("peer sessions = %d, want one", len(peerSessions))
	}
	if peerSessions[0].closeCallsSnapshot() != 0 {
		t.Fatalf("viable peer was closed by sibling liveness fault: %d close calls", peerSessions[0].closeCallsSnapshot())
	}

	var allEvents []map[string]json.RawMessage
	faultCount := 0
	faultIndex := -1
	silentTerminationIndex := -1
	failureDiagnostic := false
	for {
		payload := allReader.next(t)
		allEvents = append(allEvents, payload)
		if roomSSEString(t, payload, "type") == RoomStreamEventTypeDiagnostic && roomSSEString(t, payload, "participant_id") == "silent" && roomSSEString(t, payload, "event") == SessionDiagnosticEventFailure {
			var fields map[string]string
			if err := json.Unmarshal(payload["fields"], &fields); err != nil {
				t.Fatalf("decode timeout liveness diagnostic: %v", err)
			}
			if fields[fieldClassification] != SessionSilentProviderTimeoutClassification {
				t.Fatalf("timeout liveness diagnostic fields = %v", fields)
			}
			failureDiagnostic = true
		}
		if roomSSEString(t, payload, "type") != RoomStreamEventTypeRoom {
			continue
		}
		event := roomSSEString(t, payload, "event")
		switch event {
		case RoomStreamEventParticipantLivenessFault:
			faultCount++
			faultIndex = len(allEvents) - 1
		case RoomStreamEventParticipantTerminated:
			if roomSSEString(t, payload, "participant_id") == "silent" {
				silentTerminationIndex = len(allEvents) - 1
			}
		}
		if silentTerminationIndex >= 0 {
			// The peer stream has already observed the fault. Keep the viable
			// participant alive until that ordering is also visible on the
			// unfiltered stream.
			break
		}
	}

	peerSessions[0].end()
	for {
		payload := allReader.next(t)
		allEvents = append(allEvents, payload)
		if roomSSEString(t, payload, "type") != RoomStreamEventTypeRoom {
			continue
		}
		event := roomSSEString(t, payload, "event")
		if event == RoomStreamEventParticipantLivenessFault {
			faultCount++
			faultIndex = len(allEvents) - 1
		}
		if event == RoomStreamEventParticipantTerminated && roomSSEString(t, payload, "participant_id") == "silent" {
			silentTerminationIndex = len(allEvents) - 1
		}
		if event == RoomStreamEventRunTerminated {
			break
		}
	}

	select {
	case got := <-outcome:
		if got.err != nil || got.result.Reason != RoomTerminationStopped {
			t.Fatalf("silent provider room outcome = %+v, err=%v", got.result, got.err)
		}
		if got.result.Participants["silent"].Classification != SessionSilentProviderTimeoutClassification {
			t.Fatalf("silent result = %+v, want timeout classification", got.result.Participants["silent"])
		}
		if got.result.Participants["peer"].Classification == SessionSilentProviderTimeoutClassification {
			t.Fatalf("viable peer inherited timeout classification: %+v", got.result.Participants["peer"])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("silent provider room did not terminate after viable peer was released")
	}
	if faultCount != 1 || faultIndex < 0 || silentTerminationIndex < 0 || faultIndex >= silentTerminationIndex {
		t.Fatalf("unfiltered liveness ordering fault_count=%d fault_index=%d silent_termination_index=%d events=%v", faultCount, faultIndex, silentTerminationIndex, allEvents)
	}
	if !failureDiagnostic {
		t.Fatal("silent provider room did not publish its typed session failure diagnostic")
	}
}

func TestRoomEventBroker_UnknownFilterFailsBeforeStreaming(t *testing.T) {
	broker, err := NewRoomEventBroker([]string{"a", "b"})
	if err != nil {
		t.Fatalf("NewRoomEventBroker: %v", err)
	}
	server := httptest.NewServer(broker)
	defer server.Close()
	defer broker.Close()

	response, err := http.Get(server.URL + "/events?participant=missing")
	if err != nil {
		t.Fatalf("GET unknown participant: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown participant status = %d, want %d", response.StatusCode, http.StatusBadRequest)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read unknown participant response: %v", err)
	}
	if !strings.Contains(string(body), `unknown room stream participant: "missing"`) {
		t.Fatalf("unknown participant response = %q", body)
	}
}

func TestRoomEventBroker_IsForwardOnly(t *testing.T) {
	broker, err := NewRoomEventBroker([]string{"a", "b"})
	if err != nil {
		t.Fatalf("NewRoomEventBroker: %v", err)
	}
	server := httptest.NewServer(broker)
	defer server.Close()
	defer broker.Close()

	broker.RecordDiagnostic("a", SessionDiagnosticRecord{Event: "before_connect", Fields: map[string]string{"sequence": "0"}})
	response, err := http.Get(server.URL + "/events?participant=a")
	if err != nil {
		t.Fatalf("GET filtered events: %v", err)
	}
	defer response.Body.Close()
	reader := newRoomSSEReader(response.Body)
	broker.RecordDiagnostic("b", SessionDiagnosticRecord{Event: "wrong_participant"})
	broker.RecordDiagnostic("a", SessionDiagnosticRecord{Event: "after_connect", Fields: map[string]string{"sequence": "1"}})
	payload := reader.next(t)
	if roomSSEString(t, payload, "event") != "after_connect" {
		t.Fatalf("replayed event payload = %v", payload)
	}
}

type blockingRoomResponseWriter struct {
	header    http.Header
	ready     chan struct{}
	started   chan struct{}
	release   chan struct{}
	readyOnce sync.Once
	startOnce sync.Once
}

func newBlockingRoomResponseWriter() *blockingRoomResponseWriter {
	return &blockingRoomResponseWriter{
		header:  make(http.Header),
		ready:   make(chan struct{}),
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (w *blockingRoomResponseWriter) Header() http.Header { return w.header }

func (w *blockingRoomResponseWriter) WriteHeader(int) {}

func (w *blockingRoomResponseWriter) Write(payload []byte) (int, error) {
	w.startOnce.Do(func() { close(w.started) })
	<-w.release
	return len(payload), nil
}

func (w *blockingRoomResponseWriter) Flush() {
	w.readyOnce.Do(func() { close(w.ready) })
}

func TestRoomEventBroker_DropsSlowClientWithoutBlockingPublishers(t *testing.T) {
	broker, err := NewRoomEventBrokerWithOptions([]string{"a"}, RoomStreamBrokerOptions{QueueSize: 1})
	if err != nil {
		t.Fatalf("NewRoomEventBroker: %v", err)
	}
	defer broker.Close()

	requestContext, cancel := context.WithCancel(context.Background())
	defer cancel()
	request := httptest.NewRequest(http.MethodGet, "http://room.test/events", nil).WithContext(requestContext)
	writer := newBlockingRoomResponseWriter()
	handlerDone := make(chan struct{})
	go func() {
		broker.ServeHTTP(writer, request)
		close(handlerDone)
	}()

	select {
	case <-writer.ready:
	case <-time.After(time.Second):
		t.Fatal("slow client handler did not register")
	}
	broker.RecordDiagnostic("a", SessionDiagnosticRecord{Event: "first"})
	select {
	case <-writer.started:
	case <-time.After(time.Second):
		t.Fatal("slow client did not begin its blocked write")
	}

	broker.RecordDiagnostic("a", SessionDiagnosticRecord{Event: "queued"})
	publishDone := make(chan struct{})
	go func() {
		broker.RecordDiagnostic("a", SessionDiagnosticRecord{Event: "overflow"})
		close(publishDone)
	}()
	select {
	case <-publishDone:
	case <-time.After(time.Second):
		t.Fatal("publishing to a slow client blocked the room")
	}

	close(writer.release)
	cancel()
	select {
	case <-handlerDone:
	case <-time.After(time.Second):
		t.Fatal("dropped slow client handler did not settle")
	}
}

func TestRoomEventBroker_ConcurrentParticipantStreamsPreserveOrder(t *testing.T) {
	const count = 64
	broker, err := NewRoomEventBrokerWithOptions([]string{"a", "b"}, RoomStreamBrokerOptions{QueueSize: count * 2})
	if err != nil {
		t.Fatalf("NewRoomEventBroker: %v", err)
	}
	server := httptest.NewServer(broker)
	defer server.Close()
	defer broker.Close()

	response, err := http.Get(server.URL + "/events")
	if err != nil {
		t.Fatalf("GET /events: %v", err)
	}
	defer response.Body.Close()
	reader := newRoomSSEReader(response.Body)

	var publishWG sync.WaitGroup
	for _, participantID := range []string{"a", "b"} {
		participantID := participantID
		publishWG.Add(1)
		go func() {
			defer publishWG.Done()
			for sequence := 0; sequence < count; sequence++ {
				broker.RecordDiagnostic(participantID, SessionDiagnosticRecord{
					Event:  "ordered",
					Fields: map[string]string{"sequence": strconv.Itoa(sequence)},
				})
			}
		}()
	}
	publishWG.Wait()

	sequences := map[string][]int{"a": {}, "b": {}}
	for index := 0; index < count*2; index++ {
		payload := reader.next(t)
		participantID := roomSSEString(t, payload, "participant_id")
		var fields map[string]string
		if err := json.Unmarshal(payload["fields"], &fields); err != nil {
			t.Fatalf("ordered fields: %v", err)
		}
		sequence, err := strconv.Atoi(fields["sequence"])
		if err != nil {
			t.Fatalf("ordered sequence = %v: %v", fields, err)
		}
		sequences[participantID] = append(sequences[participantID], sequence)
	}
	for participantID, got := range sequences {
		if len(got) != count {
			t.Fatalf("participant %s event count = %d, want %d", participantID, len(got), count)
		}
		for index, sequence := range got {
			if sequence != index {
				t.Fatalf("participant %s sequence at %d = %d, want %d; all=%v", participantID, index, sequence, index, got)
			}
		}
	}
}

func TestRunRoom_PublishesParticipantAndTerminalRoomEvents(t *testing.T) {
	ids := []string{"a", "b"}
	inferencers := make(map[string]*roomTestInferencer, 2)
	for _, id := range []string{"a", "b"} {
		events := []messages.StreamMessage{roomTestSessionOpen(id)}
		events = append(events, roomTestResponse("room stream response")...)
		inferencers[id] = &roomTestInferencer{events: events}
	}
	broker, err := NewRoomEventBroker(ids)
	if err != nil {
		t.Fatalf("NewRoomEventBroker: %v", err)
	}
	server := httptest.NewServer(broker)
	defer server.Close()
	defer broker.Close()
	response, err := http.Get(server.URL + "/events")
	if err != nil {
		t.Fatalf("GET /events: %v", err)
	}
	defer response.Body.Close()
	reader := newRoomSSEReader(response.Body)

	opts, _ := newRoomTestRunOptions(ids, inferencers)
	opts.Manifest.Room.MaxTurns = 1
	opts.Stream = broker
	outcome := make(chan roomTestRunOutcome, 1)
	go func() {
		result, runErr := RunRoomWithResult(context.Background(), io.Discard, opts)
		outcome <- roomTestRunOutcome{result: result, err: runErr}
	}()

	joined := make(map[string]bool)
	terminated := make(map[string]bool)
	diagnostics := make(map[string]bool)
	var terminal map[string]json.RawMessage
	for terminal == nil {
		payload := reader.next(t)
		switch roomSSEString(t, payload, "type") {
		case RoomStreamEventTypeRoom:
			event := roomSSEString(t, payload, "event")
			participantID := roomSSEString(t, payload, "participant_id")
			switch event {
			case RoomStreamEventParticipantJoined:
				joined[participantID] = true
			case RoomStreamEventParticipantTerminated:
				terminated[participantID] = true
			case RoomStreamEventRunTerminated:
				terminal = payload
			}
		case RoomStreamEventTypeDiagnostic:
			if roomSSEString(t, payload, "event") == SessionDiagnosticEventTurn {
				diagnostics[roomSSEString(t, payload, "participant_id")] = true
			}
		}
	}

	if roomSSEString(t, terminal, "participant_id") != RoomStreamRoomParticipantID || roomSSEString(t, terminal, "reason") != string(RoomTerminationMaxTurnsReached) {
		t.Fatalf("terminal room event = %v", terminal)
	}
	for _, participantID := range ids {
		if !joined[participantID] || !terminated[participantID] || !diagnostics[participantID] {
			t.Fatalf("participant %s events joined=%v terminated=%v diagnostic=%v", participantID, joined[participantID], terminated[participantID], diagnostics[participantID])
		}
	}
	select {
	case result := <-outcome:
		if result.err != nil {
			t.Fatalf("RunRoomWithResult: %v", result.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("room did not finish after terminal stream event")
	}
}

func TestRoomEventBroker_RejectsReservedRoomParticipant(t *testing.T) {
	_, err := NewRoomEventBroker([]string{"room", "participant"})
	if err == nil || !strings.Contains(err.Error(), RoomStreamRoomParticipantID) {
		t.Fatalf("reserved participant error = %v", err)
	}
}

func TestRoomEventBroker_DropsOnlySlowClientWhenQueueIsFull(t *testing.T) {
	broker, err := NewRoomEventBrokerWithOptions([]string{"a"}, RoomStreamBrokerOptions{QueueSize: 1})
	if err != nil {
		t.Fatalf("NewRoomEventBroker: %v", err)
	}

	request := httptest.NewRequest(http.MethodGet, "http://room.test/events", nil)
	requestContext, cancel := context.WithCancel(request.Context())
	defer cancel()
	request = request.WithContext(requestContext)
	writer := newBlockingRoomSSEWriter()
	handlerDone := make(chan struct{})
	go func() {
		broker.ServeHTTP(writer, request)
		close(handlerDone)
	}()

	select {
	case <-writer.ready:
	case <-time.After(time.Second):
		t.Fatal("SSE handler did not register the client")
	}
	broker.RecordDiagnostic("a", SessionDiagnosticRecord{Event: "first"})
	select {
	case <-writer.writeStarted:
	case <-time.After(time.Second):
		t.Fatal("SSE handler did not begin writing the first event")
	}

	// The handler is blocked in its writer. One event can wait in its bounded
	// queue; the next one disconnects only this slow client and returns to the
	// publisher immediately.
	broker.RecordDiagnostic("a", SessionDiagnosticRecord{Event: "queued"})
	broker.RecordDiagnostic("a", SessionDiagnosticRecord{Event: "overflow"})

	deadline := time.Now().Add(time.Second)
	for {
		broker.mu.Lock()
		clients := len(broker.clients)
		broker.mu.Unlock()
		if clients == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("slow client remained registered after queue overflow")
		}
		time.Sleep(time.Millisecond)
	}
	close(writer.unblock)
	cancel()
	select {
	case <-handlerDone:
	case <-time.After(time.Second):
		t.Fatal("slow SSE handler did not terminate")
	}
}

type blockingRoomSSEWriter struct {
	header       http.Header
	ready        chan struct{}
	writeStarted chan struct{}
	unblock      chan struct{}
	readyOnce    sync.Once
	writeOnce    sync.Once
}

func newBlockingRoomSSEWriter() *blockingRoomSSEWriter {
	return &blockingRoomSSEWriter{
		header:       make(http.Header),
		ready:        make(chan struct{}),
		writeStarted: make(chan struct{}, 1),
		unblock:      make(chan struct{}),
	}
}

func (w *blockingRoomSSEWriter) Header() http.Header { return w.header }

func (w *blockingRoomSSEWriter) WriteHeader(_ int) {}

func (w *blockingRoomSSEWriter) Write(payload []byte) (int, error) {
	w.writeOnce.Do(func() { w.writeStarted <- struct{}{} })
	<-w.unblock
	return len(payload), nil
}

func (w *blockingRoomSSEWriter) Flush() {
	w.readyOnce.Do(func() { close(w.ready) })
}

var _ SessionDiagnosticSink = RoomParticipantEventSink{}
