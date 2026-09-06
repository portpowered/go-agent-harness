package lifecycle

import (
	"context"
	"errors"
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
	h.events <- session.LiveEvent{Kind: "turn_completed"}
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
	if got := outcome.result.Participants["silent"].Classification; got != classification {
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
		if participantID == "silent" && sink.events[index].Liveness != nil && sink.events[index].Liveness.Classification == classification {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("forwarded %s liveness events = %d, want one", classification, count)
	}
}
