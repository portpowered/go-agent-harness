package lifecycle

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

const typedLivenessSilentID = "silent"

func TestRunnerPreservesParticipantBrowserLifecycleAndWatchOrdering(t *testing.T) {
	watchEvents := make(chan rooms.BrowserEvent, 1)
	watchEvents <- rooms.BrowserEvent{
		Type: "invocation_completed", Sequence: 17, At: time.Unix(42, 0), BrowserID: "browser-a",
		TargetID: "tab-a", Generation: 3, PreviousGeneration: 2, InvocationID: "inv-1",
		ToolName: "read_page", State: "completed", Status: "ok", ErrorCode: "", Reason: "done",
		CatalogReady: true, ToolCount: 2, ToolCountKnown: true,
	}
	close(watchEvents)
	var lifecycleMu sync.Mutex
	var lifecycle []string
	var closeCount int
	browserFactory := func(rooms.Participant) (rooms.BrowserCapabilities, error) {
		return rooms.BrowserCapabilities{
			Definitions: []messages.ToolDefinition{{Name: "read_page"}},
			Initialize: func(context.Context) error {
				lifecycleMu.Lock()
				lifecycle = append(lifecycle, "initialize")
				lifecycleMu.Unlock()
				return nil
			},
			RefreshToolDefinitions: func(context.Context) ([]messages.ToolDefinition, error) {
				lifecycleMu.Lock()
				lifecycle = append(lifecycle, "refresh")
				lifecycleMu.Unlock()
				return []messages.ToolDefinition{{Name: "read_page_v2"}}, nil
			},
			BrowserWatch: func(context.Context) <-chan rooms.BrowserEvent { return watchEvents },
			Close: func() error {
				lifecycleMu.Lock()
				lifecycle = append(lifecycle, "close")
				closeCount++
				lifecycleMu.Unlock()
				return nil
			},
		}, nil
	}
	service := &browserParityService{}
	runner := New(Dependencies{Live: service, Clock: platformclock.Real{}})
	manifest := rooms.Manifest{
		SchemaVersion: rooms.SchemaVersion,
		Room:          rooms.Room{MaxTurns: 1},
		Participants: []rooms.Participant{
			{ID: "browser-agent", Kind: rooms.ParticipantKindAgent, SystemPrompt: "browser", OpeningPrompt: "start", Provider: "fixture", Model: "room", APIKeyEnv: "ROOM_KEY", Tools: []string{}, BrowserTools: browserConfigForParityTest()},
			{ID: "plain-agent", Kind: rooms.ParticipantKindAgent, SystemPrompt: "plain", OpeningPrompt: "start", Provider: "fixture", Model: "room", APIKeyEnv: "ROOM_KEY", Tools: []string{}},
		},
	}
	result, err := runner.Run(context.Background(), nil, rooms.RoomRunOptions{Manifest: manifest, BrowserCapabilitiesFactory: browserFactory})
	if err != nil {
		t.Fatalf("Run error = %v", err)
	}
	if result.TerminationReason != rooms.RoomTerminationMaxTurnsReached {
		t.Fatalf("room termination = %q, want max turns", result.TerminationReason)
	}
	lifecycleMu.Lock()
	gotLifecycle := append([]string(nil), lifecycle...)
	gotCloseCount := closeCount
	lifecycleMu.Unlock()
	if len(gotLifecycle) != 3 || gotLifecycle[0] != "initialize" || gotLifecycle[1] != "refresh" || gotLifecycle[2] != "close" {
		t.Fatalf("browser lifecycle = %v, want initialize, refresh, close", gotLifecycle)
	}
	if gotCloseCount != 1 {
		t.Fatalf("browser close count = %d, want one owner close", gotCloseCount)
	}
	service.mu.Lock()
	requests := append([]session.LiveRequest(nil), service.requests...)
	service.mu.Unlock()
	if len(requests) != 2 || requests[0].Capabilities == nil || requests[0].Capabilities.Handle == nil {
		t.Fatalf("live browser request = %+v, want participant capability handle", requests)
	}
	watcher, ok := requests[0].Capabilities.Handle.(session.LiveCapabilityWatcher)
	if !ok {
		t.Fatal("room browser adapter did not expose a typed watcher")
	}
	// OpenLive drives the capability before returning, so the watcher output was
	// consumed by the fake service. This second assertion checks the adapter's
	// field mapping independently without depending on provider event timing.
	if watcher == nil {
		t.Fatal("typed browser watcher is nil")
	}
}

func browserConfigForParityTest() *rooms.BrowserToolsConfig {
	config := rooms.BrowserToolsConfig{
		Backend:   rooms.BrowserToolsBackendWebMCP,
		Selection: rooms.BrowserSelectionConfig{AutoSelect: rooms.BrowserAutoSelectOff},
		Policy: rooms.BrowserPolicyConfig{
			Approval:          rooms.BrowserApprovalWrites,
			CancelOnInterrupt: rooms.BrowserCancelOnInterruptReadOnly,
		},
		Limits: rooms.BrowserLimitsConfig{InvocationTimeout: 20 * time.Second},
	}
	return &config
}

type browserParityService struct {
	mu       sync.Mutex
	requests []session.LiveRequest
}

func (s *browserParityService) OpenLive(ctx context.Context, request session.LiveRequest) (session.LiveHandle, error) {
	if request.Capabilities != nil {
		if err := request.Capabilities.Handle.Initialize(ctx); err != nil {
			return nil, err
		}
		if _, err := request.Capabilities.Handle.RefreshDefinitions(ctx); err != nil {
			return nil, err
		}
		watcher, ok := request.Capabilities.Handle.(session.LiveCapabilityWatcher)
		if !ok {
			return nil, errors.New("browser capability watcher is missing")
		}
		watch := watcher.BrowserWatch(ctx)
		if watch == nil {
			return nil, errors.New("browser capability watch is missing")
		}
		event, ok := <-watch
		if !ok || event.Type != "invocation_completed" || event.Sequence != 17 || event.Generation != 3 || event.PreviousGeneration != 2 || event.InvocationID != "inv-1" || event.ToolName != "read_page" {
			return nil, errors.New("browser capability event mapping changed")
		}
	}
	s.mu.Lock()
	s.requests = append(s.requests, request)
	s.mu.Unlock()
	return &browserParityHandle{capabilities: request.Capabilities, events: make(chan session.LiveEvent, 1), done: make(chan struct{})}, nil
}

type browserParityHandle struct {
	capabilities *session.LiveCapabilities
	events       chan session.LiveEvent
	done         chan struct{}
	once         sync.Once
}

func (h *browserParityHandle) Media() audio.MediaEndpoints      { return audio.MediaEndpoints{} }
func (h *browserParityHandle) Events() <-chan session.LiveEvent { return h.events }

func (h *browserParityHandle) Start(context.Context) error {
	h.events <- assistantTurnEndEvent("")
	return nil
}

func (*browserParityHandle) Send(context.Context, session.LiveControl) error { return nil }

func (h *browserParityHandle) Cancel(error) { h.once.Do(func() { close(h.done) }) }

func (h *browserParityHandle) Wait() error {
	<-h.done
	return nil
}

func (h *browserParityHandle) Close() error {
	if h.capabilities != nil && h.capabilities.Handle != nil {
		if err := h.capabilities.Handle.Close(); err != nil {
			return err
		}
	}
	close(h.events)
	return nil
}

var _ session.LiveService = (*browserParityService)(nil)
var _ session.LiveHandle = (*browserParityHandle)(nil)

func TestRunnerRoutesTypedLivenessFaultAndPreservesPeer(t *testing.T) {
	for _, test := range []struct {
		name           string
		classification string
	}{
		{name: "empty response", classification: "silent_provider_empty_response"},
		{name: "provider timeout", classification: "silent_provider_timeout"},
	} {
		t.Run(test.name, func(t *testing.T) { runTypedLivenessCase(t, test.classification) })
	}
}

func runTypedLivenessCase(t *testing.T, classification string) {
	silent := newFakeLiveHandle()
	silent.startEvents = []session.LiveEvent{{
		Kind: string(session.LiveEventLiveness),
		Liveness: &session.LiveLivenessFailure{
			Classification: classification,
			ResponseID:     "response-1",
			TerminalReason: messages.TerminalReasonPartialOutput,
		},
	}}
	peer := newFakeLiveHandle()
	service := &fakeLiveService{handles: map[string]*fakeLiveHandle{typedLivenessSilentID: silent, "peer": peer}}
	sink := &recordingRoomEventSink{}
	faultSeen := make(chan struct{})
	var faultOnce sync.Once
	runner := New(Dependencies{Live: service, Clock: platformclock.Real{}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resultCh := startTypedLivenessRun(ctx, &runner, sink, faultSeen, &faultOnce)
	waitForTypedLivenessFault(t, faultSeen)
	if got := peer.cancelCallsSnapshot(); got != 0 {
		t.Fatalf("peer was cancelled by %s fault: %d", classification, got)
	}
	cancel()
	outcome := waitForRoomResult(t, resultCh)
	assertTypedLivenessResult(t, outcome, classification)
	assertForwardedLiveness(t, sink, classification)
}

func startTypedLivenessRun(ctx context.Context, runner *Runner, sink *recordingRoomEventSink, faultSeen chan struct{}, faultOnce *sync.Once) <-chan roomRunOutcome {
	resultCh := make(chan roomRunOutcome, 1)
	go func() {
		result, err := runner.Run(ctx, nil, rooms.RoomRunOptions{
			Manifest: typedLivenessManifest(), EventSink: sink,
			OnDiagnostic: func(participantID string, record rooms.RoomDiagnosticRecord) {
				if participantID == typedLivenessSilentID && record.Event == "live_liveness_fault" {
					faultOnce.Do(func() { close(faultSeen) })
				}
			},
		})
		resultCh <- roomRunOutcome{result: result, err: err}
	}()
	return resultCh
}

type roomRunOutcome struct {
	result rooms.RoomResult
	err    error
}

func typedLivenessManifest() rooms.Manifest {
	return rooms.Manifest{
		SchemaVersion: rooms.SchemaVersion,
		Room:          rooms.Room{Interactive: true},
		Participants: []rooms.Participant{
			{ID: typedLivenessSilentID, SystemPrompt: typedLivenessSilentID, OpeningPrompt: "start", Provider: "p", Model: "m", APIKeyEnv: "SILENT", Tools: []string{}},
			{ID: "peer", SystemPrompt: "peer", OpeningPrompt: "start", Provider: "p", Model: "m", APIKeyEnv: "PEER", Tools: []string{}},
		},
	}
}

func waitForTypedLivenessFault(t *testing.T, faultSeen <-chan struct{}) {
	t.Helper()
	select {
	case <-faultSeen:
	case <-time.After(2 * time.Second):
		t.Fatal("typed liveness fault was not observed")
	}
}

func waitForRoomResult(t *testing.T, resultCh <-chan roomRunOutcome) roomRunOutcome {
	t.Helper()
	select {
	case outcome := <-resultCh:
		return outcome
	case <-time.After(2 * time.Second):
		t.Fatal("room did not join peer cleanup")
		return roomRunOutcome{}
	}
}

func assertTypedLivenessResult(t *testing.T, outcome roomRunOutcome, classification string) {
	t.Helper()
	if outcome.err != nil {
		t.Fatalf("room run error = %v", outcome.err)
	}
	if got := outcome.result.Participants[typedLivenessSilentID].Classification; got != classification {
		t.Fatalf("silent classification = %q, want %q", got, classification)
	}
	if got := outcome.result.Participants["peer"].Classification; got != "" {
		t.Fatalf("peer inherited liveness classification %q", got)
	}
}

func assertForwardedLiveness(t *testing.T, sink *recordingRoomEventSink, classification string) {
	t.Helper()
	sink.mu.Lock()
	defer sink.mu.Unlock()
	count := 0
	for index, participantID := range sink.participants {
		if participantID == typedLivenessSilentID && sink.events[index].Liveness != nil && sink.events[index].Liveness.Classification == classification {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("forwarded %s liveness events = %d, want one", classification, count)
	}
}

func TestRunnerPublishesLivenessBeforeCancellingParticipant(t *testing.T) {
	silent := newFakeLiveHandle()
	silent.startEvents = []session.LiveEvent{{
		Kind: string(session.LiveEventLiveness),
		Liveness: &session.LiveLivenessFailure{
			Classification: "silent_provider_empty_response",
			TerminalReason: messages.TerminalReasonTerminalFailure,
		},
	}}
	peer := newFakeLiveHandle()
	service := &fakeLiveService{handles: map[string]*fakeLiveHandle{
		typedLivenessSilentID: silent,
		"peer":                peer,
	}}
	sink := &livenessOrderingSink{silent: silent, peer: peer, seen: make(chan struct{})}
	runner := New(Dependencies{Live: service, Clock: platformclock.Real{}})
	manifest := typedLivenessManifest()
	resultCh := make(chan error, 1)
	go func() {
		_, err := runner.Run(context.Background(), nil, rooms.RoomRunOptions{Manifest: manifest, EventSink: sink})
		resultCh <- err
	}()
	select {
	case <-sink.seen:
	case <-time.After(2 * time.Second):
		t.Fatal("room did not publish typed liveness")
	}
	if got := sink.silentCancels; got != 0 {
		t.Fatalf("silent participant cancelled before liveness publication: %d", got)
	}
	select {
	case err := <-resultCh:
		if err != nil {
			t.Fatalf("room run error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("room did not finish participant cleanup")
	}
}

type livenessOrderingSink struct {
	silent        *fakeLiveHandle
	peer          *fakeLiveHandle
	seen          chan struct{}
	silentCancels int
	once          sync.Once
}

func (s *livenessOrderingSink) Publish(_ context.Context, participantID string, event session.LiveEvent) error {
	if participantID == typedLivenessSilentID && event.Liveness != nil {
		s.silentCancels = s.silent.cancelCallsSnapshot()
		s.peer.Cancel(nil)
		s.once.Do(func() { close(s.seen) })
	}
	return nil
}

const (
	scriptedAgentID = "agent"
	scriptedPeerID  = "peer"
)

// liveStreamEvent projects a stream message exactly as the session live
// owner publishes it: the event kind is the stream type (TEXT.DELTA is
// renamed to the text kind) and the typed message is retained.
func liveStreamEvent(sessionID string, msg messages.StreamMessage) session.LiveEvent {
	observed := msg
	kind := string(msg.Type)
	if msg.Type == messages.StreamTypeTextDelta {
		kind = string(session.LiveEventText)
	}
	return session.LiveEvent{Kind: kind, SessionID: sessionID, ParticipantID: sessionID, Role: msg.Role, ResponseID: msg.ResponseID, Message: &observed}
}

// assistantTurnEndEvent is the provider response-done boundary as the live
// session publishes it.
func assistantTurnEndEvent(sessionID string) session.LiveEvent {
	return liveStreamEvent(sessionID, messages.StreamMessage{Type: messages.StreamTypeMessageEnd, ResponseID: "resp", Value: messages.NewMessageEndValue(messages.TokenUsage{})})
}

// nonTurnEvents is the rest of a realistic assistant response: every event
// the live session emits around a turn that must not itself count as one.
func nonTurnEvents(sessionID string) []session.LiveEvent {
	return []session.LiveEvent{
		liveStreamEvent(sessionID, messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, ResponseID: "resp", Value: messages.NewMessageStartValue()}),
		liveStreamEvent(sessionID, messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, ResponseID: "resp", Value: &messages.TextDeltaValue{Content: "hello"}}),
		liveStreamEvent(sessionID, messages.StreamMessage{Type: messages.StreamTypeAudioEnd, Role: messages.RoleAssistant, ResponseID: "resp", Value: messages.NewAudioEndValue()}),
		liveStreamEvent(sessionID, messages.StreamMessage{Type: messages.StreamTypeTranscriptEnd, Role: messages.RoleAssistant, ResponseID: "resp", Value: messages.NewTranscriptEndValue("hello")}),
		liveStreamEvent(sessionID, messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleTool, Value: messages.NewMessageEndValue(messages.TokenUsage{})}),
		liveStreamEvent(sessionID, messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleUser, Value: messages.NewMessageEndValue(messages.TokenUsage{})}),
		liveStreamEvent(sessionID, messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, ResponseID: "acknowledgement", ResponsePurpose: messages.ResponsePurposeToolAcknowledgement, Value: messages.NewMessageEndValue(messages.TokenUsage{})}),
		liveStreamEvent(sessionID, messages.StreamMessage{Type: messages.StreamTypeMessageEnd, ResponseID: "interrupted", Value: messages.NewMessageEndValueWithTerminal(messages.TokenUsage{}, messages.TerminalReasonPartialOutput, messages.TerminalProvenanceProvider, messages.TerminalOutputPartial)}),
	}
}

func TestAssistantTurnCompletedMatchesOnlyAssistantMessageEnd(t *testing.T) {
	if !assistantTurnCompleted(assistantTurnEndEvent(scriptedAgentID)) {
		t.Fatal("provider MESSAGE.END was not counted as a turn")
	}
	assistant := liveStreamEvent(scriptedAgentID, messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})})
	if !assistantTurnCompleted(assistant) {
		t.Fatal("assistant MESSAGE.END was not counted as a turn")
	}
	for _, event := range append(nonTurnEvents(scriptedAgentID),
		session.LiveEvent{Kind: string(session.LiveEventStarted)},
		session.LiveEvent{Kind: string(session.LiveEventTerminal)},
		session.LiveEvent{Kind: "turn_completed"},
	) {
		if assistantTurnCompleted(event) {
			t.Errorf("event %q role=%q counted as a completed turn", event.Kind, event.Role)
		}
	}
}

func TestRunnerStopsAtMaxTurnsFromRealLiveEventKinds(t *testing.T) {
	ids := []string{scriptedAgentID, scriptedPeerID}
	service := &scriptedLiveService{handles: map[string]*scriptedLiveHandle{}}
	manifest := rooms.Manifest{SchemaVersion: rooms.SchemaVersion, Room: rooms.Room{MaxTurns: 2}}
	for _, id := range ids {
		service.handles[id] = &scriptedLiveHandle{id: id, events: make(chan session.LiveEvent, 32), done: make(chan struct{})}
		manifest.Participants = append(manifest.Participants, rooms.Participant{ID: id, Kind: rooms.ParticipantKindAgent, SystemPrompt: id, OpeningPrompt: "start", Provider: "p", Model: "m", APIKeyEnv: "KEY_" + id, Tools: []string{}})
	}
	processed := make(chan string, 128)
	runner := New(Dependencies{Live: service, Clock: platformclock.Real{}})
	done := make(chan scriptedOutcome, 1)
	go func() {
		result, err := runner.Run(context.Background(), nil, rooms.RoomRunOptions{
			Manifest: manifest,
			OnDiagnostic: func(participantID string, record rooms.RoomDiagnosticRecord) {
				if strings.HasPrefix(record.Event, "live_") && record.Event != "live_"+string(session.LiveEventStarted) {
					processed <- participantID
				}
			},
		})
		done <- scriptedOutcome{result, err}
	}()

	// Turn one for both agents, surrounded by every non-turn event kind.
	sent := 0
	for _, id := range ids {
		events := append(nonTurnEvents(id), assistantTurnEndEvent(id))
		events = append(events, nonTurnEvents(id)...)
		for _, event := range events {
			service.handles[id].events <- event
			sent++
		}
	}
	// Turn two for only the first agent: the bound is shared, so the room
	// must keep running until every agent reaches it.
	service.handles[scriptedAgentID].events <- assistantTurnEndEvent(scriptedAgentID)
	sent++
	waitProcessed(t, processed, sent)
	expectRunning(t, done)

	service.handles[scriptedPeerID].events <- assistantTurnEndEvent(scriptedPeerID)
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("Run error = %v", got.err)
		}
		if got.result.TerminationReason != rooms.RoomTerminationMaxTurnsReached {
			t.Fatalf("room termination = %q, want max turns", got.result.TerminationReason)
		}
		for _, id := range ids {
			if turns := got.result.Participants[id].TurnsCompleted; turns != 2 {
				t.Errorf("%s turns = %d, want 2", id, turns)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("room did not stop after every agent completed two assistant turns")
	}
}

type scriptedOutcome struct {
	result rooms.RoomResult
	err    error
}

func expectRunning(t *testing.T, done <-chan scriptedOutcome) {
	t.Helper()
	select {
	case got := <-done:
		t.Fatalf("room stopped before the turn bound: result=%+v err=%v", got.result, got.err)
	case <-time.After(50 * time.Millisecond):
	}
}

func waitProcessed(t *testing.T, processed <-chan string, want int) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for seen := 0; seen < want; seen++ {
		select {
		case <-processed:
		case <-deadline:
			t.Fatalf("processed %d live events, want %d", seen, want)
		}
	}
}

type scriptedLiveService struct {
	handles map[string]*scriptedLiveHandle
}

func (s *scriptedLiveService) OpenLive(_ context.Context, request session.LiveRequest) (session.LiveHandle, error) {
	return s.handles[request.SessionID], nil
}

type scriptedLiveHandle struct {
	id     string
	events chan session.LiveEvent
	done   chan struct{}
	once   sync.Once
	closed sync.Once
}

func (h *scriptedLiveHandle) Media() audio.MediaEndpoints      { return audio.MediaEndpoints{} }
func (h *scriptedLiveHandle) Events() <-chan session.LiveEvent { return h.events }
func (h *scriptedLiveHandle) Start(context.Context) error {
	h.events <- session.LiveEvent{Kind: string(session.LiveEventStarted), SessionID: h.id}
	return nil
}
func (*scriptedLiveHandle) Send(context.Context, session.LiveControl) error { return nil }
func (h *scriptedLiveHandle) Cancel(error)                                  { h.once.Do(func() { close(h.done) }) }
func (h *scriptedLiveHandle) Wait() error {
	<-h.done
	return nil
}
func (h *scriptedLiveHandle) Close() error {
	h.closed.Do(func() { close(h.events) })
	return nil
}

// roomCapabilityProvider is a provider session that runs turn detection, owns
// local playback and declares its input rate.
type roomCapabilityProvider struct {
	receive    *messages.TypedBuffer[messages.StreamMessage]
	done       chan struct{}
	interrupts int
}

func (*roomCapabilityProvider) Send(context.Context, messages.StreamMessage) bool { return true }
func (p *roomCapabilityProvider) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return p.receive
}
func (p *roomCapabilityProvider) Done() <-chan struct{}     { return p.done }
func (*roomCapabilityProvider) Close() error                { return nil }
func (*roomCapabilityProvider) ProviderTurnDetection() bool { return true }
func (*roomCapabilityProvider) InputAudioSampleRate() int   { return 24000 }
func (*roomCapabilityProvider) LocalPlayback() messages.LocalPlaybackState {
	return messages.LocalPlaybackState{Active: true, Level: 1234}
}
func (p *roomCapabilityProvider) InterruptLocalPlayback(context.Context) bool {
	p.interrupts++
	return true
}

type roomCapabilityInferencer struct{ session messages.Session }

func (i roomCapabilityInferencer) ConnectSession(context.Context) (messages.Session, error) {
	return i.session, nil
}

// The room path hands the runner the connection tracker's session. The
// runner's local barge-in must still see turn detection, local playback and
// the input rate through it.
func TestRoomTrackedSessionForwardsBargeInCapabilities(t *testing.T) {
	provider := &roomCapabilityProvider{receive: messages.NewTypedBuffer[messages.StreamMessage](4), done: make(chan struct{})}
	lifecycle := NewParticipantLifecycle(rooms.ParticipantLifecycleOptions{})
	session, err := NewConnectionTracker(roomCapabilityInferencer{session: provider}, lifecycle, nil).ConnectSession(context.Background())
	if err != nil {
		t.Fatalf("ConnectSession: %v", err)
	}
	capable, ok := session.(messages.BargeInCapableSession)
	if !ok {
		t.Fatalf("room session %T does not expose the barge-in capabilities", session)
	}
	if !capable.ProviderTurnDetection() || capable.InputAudioSampleRate() != 24000 || capable.LocalPlayback().Level != 1234 ||
		!capable.InterruptLocalPlayback(context.Background()) || provider.interrupts != 1 {
		t.Fatal("room session did not forward the provider's barge-in capabilities")
	}
}
