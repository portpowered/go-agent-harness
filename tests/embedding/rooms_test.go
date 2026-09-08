package embedding_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	roomswire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	sessionwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/wire"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

type emptyRoomCapture struct{}

func (emptyRoomCapture) Close() error { return nil }
func (emptyRoomCapture) Pump(ctx context.Context, _ audio.OutboundMedia) error {
	<-ctx.Done()
	return ctx.Err()
}

func TestExternalRoomRecordsNoCapturedSamplesTruthfully(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	scheduler := clock.NewDeterministic(time.Unix(123, 0), time.Millisecond)
	host := roomswire.NewService(roomswire.Dependencies{
		Clock: scheduler,
		Media: rooms.MediaFactoryFunc(func(context.Context, rooms.Participant, rooms.AudioFormat) (rooms.MediaPorts, error) {
			return rooms.MediaPorts{Capture: emptyRoomCapture{}}, nil
		}),
	})
	manifest := rooms.Manifest{SchemaVersion: rooms.SchemaVersion, Room: rooms.Room{MaxDuration: time.Second}}
	for _, id := range []string{"alice", "bob"} {
		manifest.Participants = append(manifest.Participants, rooms.Participant{Kind: rooms.ParticipantKindHuman, ID: id, SystemPrompt: "human", InputDevice: "capture", OutputDevice: "playback", Tools: []string{}})
	}
	output := t.TempDir()
	ready := make(chan struct{}, 2)
	finished := make(chan error, 1)
	go func() {
		result, err := host.Run(ctx, nil, rooms.RoomRunOptions{Manifest: manifest, OutputDir: output, OnParticipantReady: func(rooms.RoomParticipantReady) { ready <- struct{}{} }})
		if err == nil && result.RecordingStatus != nil {
			t.Errorf("empty source recording unexpectedly degraded: %+v", result.RecordingStatus)
		}
		finished <- err
	}()
	for range manifest.Participants {
		select {
		case <-ready:
		case <-ctx.Done():
			t.Fatal("room participants were not admitted")
		}
	}
	scheduler.AdvanceBy(time.Second)
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("room did not join after duration bound")
	}
	plan, err := host.LoadReplayPlan(output)
	if err != nil {
		t.Fatalf("load finalized empty recording: %v", err)
	}
	if plan.RoomLatencyPath == "" {
		t.Fatal("new recording omitted its room latency artifact")
	}
	if info, err := os.Stat(plan.RoomLatencyPath); err != nil || info.Size() == 0 {
		t.Fatalf("room latency artifact unavailable or empty: %v", err)
	}
	for _, participant := range manifest.Participants {
		assertEmptyRoomAudio(t, output, participant.ID)
	}
}

func assertEmptyRoomAudio(t *testing.T, output, id string) {
	t.Helper()
	for _, name := range []string{"sent.pcm", "received.pcm"} {
		data, err := os.ReadFile(filepath.Join(output, "participants", id, name))
		if err != nil || len(data) != 0 {
			t.Fatalf("%s/%s bytes=%d err=%v; want no fabricated PCM", id, name, len(data), err)
		}
	}
	file, err := os.Open(filepath.Join(output, "agent-"+id+".wav"))
	if err != nil {
		t.Fatal(err)
	}
	defer closeForTest(t, file)
	layout, err := wavio.Inspect(file)
	if err != nil || layout.DataBytes != 0 {
		t.Fatalf("%s WAV data=%d err=%v; want zero captured samples", id, layout.DataBytes, err)
	}
}

// TestExternalRoomBrowserCapabilitiesStayParticipantLocal moves the old room
// planning assertions to the public service boundary. Each admitted browser
// receives a distinct executor and lifecycle owner; an omitted browser never
// constructs capability state.
func TestExternalRoomBrowserCapabilitiesStayParticipantLocal(t *testing.T) {
	const (
		firstID  = "alpha"
		secondID = "beta"
	)
	service := &embeddedRoomBrowserLive{}
	var mu sync.Mutex
	initCalls := map[string]int{}
	refreshCalls := map[string]int{}
	closeCalls := map[string]int{}
	factory := func(participant rooms.Participant) (rooms.BrowserCapabilities, error) {
		id := participant.ID
		mu.Lock()
		defer mu.Unlock()
		return rooms.BrowserCapabilities{
			Executor:    &embeddedBrowserExecutor{id: id},
			Definitions: []messages.ToolDefinition{{Name: "browser_" + id}},
			BrowserWatch: func(context.Context) <-chan rooms.BrowserEvent {
				events := make(chan rooms.BrowserEvent)
				close(events)
				return events
			},
			Initialize: func(context.Context) error {
				mu.Lock()
				initCalls[id]++
				mu.Unlock()
				return nil
			},
			RefreshToolDefinitions: func(context.Context) ([]messages.ToolDefinition, error) {
				mu.Lock()
				refreshCalls[id]++
				mu.Unlock()
				return []messages.ToolDefinition{{Name: "browser_" + id}}, nil
			},
			Close: func() error {
				mu.Lock()
				closeCalls[id]++
				mu.Unlock()
				return nil
			},
		}, nil
	}
	manifest := embeddedBrowserManifest(firstID, secondID)
	for index := range manifest.Participants {
		manifest.Participants[index].BrowserTools = embeddedBrowserConfig()
	}

	result, err := runEmbeddedBrowserRoom(t, service, manifest, factory)
	if err != nil {
		t.Fatalf("room run: %v", err)
	}
	if result.TerminationReason != rooms.RoomTerminationMaxTurnsReached {
		t.Fatalf("room termination = %q, want max turns", result.TerminationReason)
	}
	requests := service.requestsSnapshot()
	if len(requests) != 2 {
		t.Fatalf("live requests = %d, want two participants", len(requests))
	}
	if requests[0].Capabilities == nil || requests[0].Capabilities.Executor == nil || requests[1].Capabilities == nil || requests[1].Capabilities.Executor == nil {
		t.Fatal("browser participant lost its capability executor")
	}
	if requests[0].Capabilities.Executor == requests[1].Capabilities.Executor {
		t.Fatal("browser capability executor was shared")
	}
	for index, request := range requests {
		if len(request.Capabilities.Definitions) != 1 {
			t.Fatalf("participant %d browser definitions = %v, want one", index, request.Capabilities.Definitions)
		}
		watcher, ok := request.Capabilities.Handle.(session.LiveCapabilityWatcher)
		if !ok || watcher.BrowserWatch(context.Background()) == nil {
			t.Fatalf("participant %d browser watch was not retained", index)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	for _, id := range []string{firstID, secondID} {
		if initCalls[id] != 1 || refreshCalls[id] != 1 || closeCalls[id] != 1 {
			t.Fatalf("participant %q lifecycle initialize=%d refresh=%d close=%d, want one each", id, initCalls[id], refreshCalls[id], closeCalls[id])
		}
	}
}

func TestExternalRoomBrowserOmissionDoesNotConstructCapability(t *testing.T) {
	service := &embeddedRoomBrowserLive{}
	calls := 0
	factory := func(participant rooms.Participant) (rooms.BrowserCapabilities, error) {
		calls++
		return rooms.BrowserCapabilities{
			Executor:    &embeddedBrowserExecutor{id: participant.ID},
			Definitions: []messages.ToolDefinition{{Name: "browser_tool"}},
		}, nil
	}
	manifest := embeddedBrowserManifest("browser", "plain")
	manifest.Participants[0].BrowserTools = embeddedBrowserConfig()
	result, err := runEmbeddedBrowserRoom(t, service, manifest, factory)
	if err != nil {
		t.Fatalf("room run: %v", err)
	}
	if result.TerminationReason != rooms.RoomTerminationMaxTurnsReached {
		t.Fatalf("room termination = %q, want max turns", result.TerminationReason)
	}
	if calls != 1 {
		t.Fatalf("browser capability factory calls = %d, want one", calls)
	}
	requests := service.requestsSnapshot()
	if len(requests) != 2 || requests[0].Capabilities == nil || requests[1].Capabilities != nil {
		t.Fatalf("live capability requests = %+v, want only first participant bound", requests)
	}
}

func TestExternalRoomBrowserConstructionFailureClosesOwner(t *testing.T) {
	service := &embeddedRoomBrowserLive{failParticipant: "beta"}
	var mu sync.Mutex
	closeCalls := 0
	factory := func(participant rooms.Participant) (rooms.BrowserCapabilities, error) {
		return rooms.BrowserCapabilities{
			Executor:    &embeddedBrowserExecutor{id: participant.ID},
			Definitions: []messages.ToolDefinition{{Name: "browser_" + participant.ID}},
			Close: func() error {
				mu.Lock()
				closeCalls++
				mu.Unlock()
				return nil
			},
		}, nil
	}
	manifest := embeddedBrowserManifest("alpha", "beta")
	for index := range manifest.Participants {
		manifest.Participants[index].BrowserTools = embeddedBrowserConfig()
	}
	_, err := runEmbeddedBrowserRoom(t, service, manifest, factory)
	if err == nil {
		t.Fatal("room construction unexpectedly succeeded")
	}
	mu.Lock()
	defer mu.Unlock()
	if closeCalls != 2 {
		t.Fatalf("browser owners closed = %d, want both admitted owners", closeCalls)
	}
}

func TestExternalLiveCapabilitySharedTopologyStaysParticipantLocal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	alphaCapability := newTopologyCapability("alpha")
	betaCapability := newTopologyCapability("beta")
	alphaProvider, betaProvider := newEmbeddedLiveProvider(), newEmbeddedLiveProvider()
	defer closeForTest(t, alphaProvider)
	defer closeForTest(t, betaProvider)
	alpha := openTopologyParticipant(t, ctx, "alpha", alphaProvider, alphaCapability)
	defer closeForTest(t, alpha)
	beta := openTopologyParticipant(t, ctx, "beta", betaProvider, betaCapability)
	defer closeForTest(t, beta)
	assertTopologyCatalog(t, alphaCapability, "queue_alpha", "state_alpha")
	assertTopologyCatalog(t, betaCapability, "queue_beta", "state_beta")
	if alphaCapability == betaCapability {
		t.Fatal("participants share one browser capability owner")
	}

	alphaResult, betaResult, alphaErr, betaErr := executeTopologyCalls(ctx, alphaCapability, betaCapability)
	if alphaErr != nil || betaErr != nil {
		t.Fatalf("concurrent topology calls = alpha=%v beta=%v", alphaErr, betaErr)
	}
	if alphaResult.Content != "alpha:shared" || betaResult.Content != "beta:shared" || alphaResult.ToolCallID == betaResult.ToolCallID {
		t.Fatalf("topology receipts = alpha=%#v beta=%#v, want participant-local shared-target receipts", alphaResult, betaResult)
	}
	assertTopologyInvocationEvent(t, ctx, alpha.Events(), "alpha", "alpha-call")
	assertTopologyInvocationEvent(t, ctx, beta.Events(), "beta", "beta-call")

	alphaCapability.selectTarget("alpha-only")
	alphaUpdated, err := alphaCapability.RefreshDefinitions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertTopologyDefinitionNames(t, alphaUpdated, "alpha_private")
	betaStillShared, err := betaCapability.RefreshDefinitions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertTopologyDefinitionNames(t, betaStillShared, "queue_beta", "state_beta")
	if err := alphaCapability.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := executeTopologyCall(ctx, betaCapability, "beta-after-alpha-close", "state_beta"); err != nil {
		t.Fatalf("beta invocation after alpha close: %v", err)
	}
	if got := alphaCapability.callCount(); got != 1 {
		t.Fatalf("alpha invocation count = %d, want one", got)
	}
	if got := betaCapability.callCount(); got != 2 {
		t.Fatalf("beta invocation count = %d, want two including post-close call", got)
	}
}

func openTopologyParticipant(t *testing.T, ctx context.Context, id string, provider messages.SessionInferencer, capability *topologyCapability) session.LiveHandle {
	t.Helper()
	host := sessionwire.NewLiveService(sessionwire.LiveDependencies{
		InferencerFactory: func(context.Context, session.LiveRequest) (messages.SessionInferencer, error) { return provider, nil },
	})
	handle, err := host.OpenLive(ctx, session.LiveRequest{ParticipantID: id, Capabilities: &session.LiveCapabilities{
		Executor: capability, Definitions: capability.initialDefinitions(), Handle: capability,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := handle.Start(ctx); err != nil {
		closeForTest(t, handle)
		t.Fatal(err)
	}
	select {
	case <-capability.watchStarted:
	case <-ctx.Done():
		closeForTest(t, handle)
		t.Fatal("topology capability watcher did not start")
	}
	return handle
}

func executeTopologyCall(ctx context.Context, capability *topologyCapability, id, name string) (messages.ToolCallResponse, error) {
	return capability.Execute(ctx, messages.ToolCall{ID: id, Name: name})
}

func executeTopologyCalls(ctx context.Context, alpha, beta *topologyCapability) (messages.ToolCallResponse, messages.ToolCallResponse, error, error) {
	type result struct {
		id       string
		response messages.ToolCallResponse
		err      error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	go func() {
		<-start
		response, err := executeTopologyCall(ctx, alpha, "alpha-call", "queue_alpha")
		results <- result{id: "alpha", response: response, err: err}
	}()
	go func() {
		<-start
		response, err := executeTopologyCall(ctx, beta, "beta-call", "state_beta")
		results <- result{id: "beta", response: response, err: err}
	}()
	close(start)
	first, second := <-results, <-results
	if first.id == "beta" {
		first, second = second, first
	}
	return first.response, second.response, first.err, second.err
}

func assertTopologyCatalog(t *testing.T, capability *topologyCapability, names ...string) {
	t.Helper()
	definitions, err := capability.RefreshDefinitions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	assertTopologyDefinitionNames(t, definitions, names...)
}

func assertTopologyDefinitionNames(t *testing.T, definitions []messages.ToolDefinition, names ...string) {
	t.Helper()
	if len(definitions) != len(names) {
		t.Fatalf("topology definitions = %#v, want %v", definitions, names)
	}
	for index, name := range names {
		if definitions[index].Name != name {
			t.Fatalf("topology definition %d = %q, want %q", index, definitions[index].Name, name)
		}
	}
}

func assertTopologyInvocationEvent(t *testing.T, ctx context.Context, events <-chan session.LiveEvent, participantID, invocationID string) {
	t.Helper()
	for {
		select {
		case event := <-events:
			if event.InvocationID == "" {
				continue
			}
			if event.ParticipantID != participantID || event.InvocationID != invocationID || event.TargetID != "shared" || event.State != "completed" {
				t.Fatalf("topology event = %+v, want %s/%s completed on shared target", event, participantID, invocationID)
			}
			return
		case <-ctx.Done():
			t.Fatalf("topology invocation event %s/%s was not delivered", participantID, invocationID)
		}
	}
}

type topologyCapability struct {
	id           string
	watchStarted chan struct{}
	watch        chan session.LiveCapabilityEvent
	mu           sync.Mutex
	target       string
	catalogs     map[string][]messages.ToolDefinition
	calls        []messages.ToolCall
	closed       bool
	closeOnce    sync.Once
}

func newTopologyCapability(id string) *topologyCapability {
	return &topologyCapability{
		id: id, target: "shared", watchStarted: make(chan struct{}), watch: make(chan session.LiveCapabilityEvent, 4),
		catalogs: map[string][]messages.ToolDefinition{
			"shared":     {{Name: "queue_" + id}, {Name: "state_" + id}},
			"alpha-only": {{Name: "alpha_private"}},
		},
	}
}

func (c *topologyCapability) Initialize(context.Context) error { return nil }

func (c *topologyCapability) RefreshDefinitions(ctx context.Context) ([]messages.ToolDefinition, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]messages.ToolDefinition(nil), c.catalogs[c.target]...), nil
}

func (c *topologyCapability) BrowserWatch(context.Context) <-chan session.LiveCapabilityEvent {
	select {
	case <-c.watchStarted:
	default:
		close(c.watchStarted)
	}
	return c.watch
}

func (c *topologyCapability) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	if err := ctx.Err(); err != nil {
		return messages.ToolCallResponse{}, err
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return messages.ToolCallResponse{}, errors.New("topology capability is closed")
	}
	target := c.target
	c.calls = append(c.calls, call)
	c.mu.Unlock()
	c.watch <- session.LiveCapabilityEvent{Type: "invocation", InvocationID: call.ID, TargetID: target, State: "completed"}
	return messages.ToolCallResponse{ToolCallID: call.ID, Name: call.Name, Content: c.id + ":" + target}, nil
}

func (c *topologyCapability) Close() error {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.closed = true
		c.mu.Unlock()
	})
	return nil
}

func (c *topologyCapability) initialDefinitions() []messages.ToolDefinition {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]messages.ToolDefinition(nil), c.catalogs[c.target]...)
}

func (c *topologyCapability) selectTarget(target string) {
	c.mu.Lock()
	c.target = target
	c.mu.Unlock()
	c.watch <- session.LiveCapabilityEvent{Type: "catalog_changed", TargetID: target}
}

func (c *topologyCapability) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.calls)
}

func runEmbeddedBrowserRoom(t *testing.T, live session.LiveService, manifest rooms.Manifest, factory rooms.BrowserCapabilitiesFactory) (rooms.RoomResult, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	service := roomswire.NewService(roomswire.Dependencies{Live: live, Clock: clock.Real{}})
	return service.Run(ctx, nil, rooms.RoomRunOptions{Manifest: manifest, BrowserCapabilitiesFactory: factory})
}

func embeddedBrowserManifest(first, second string) rooms.Manifest {
	return rooms.Manifest{
		SchemaVersion: rooms.SchemaVersion,
		Room:          rooms.Room{MaxTurns: 1},
		Participants: []rooms.Participant{
			{ID: first, Kind: rooms.ParticipantKindAgent, SystemPrompt: first, OpeningPrompt: "start", Provider: "fixture", Model: "room", APIKeyEnv: "ROOM_KEY", Tools: []string{}},
			{ID: second, Kind: rooms.ParticipantKindAgent, SystemPrompt: second, Provider: "fixture", Model: "room", APIKeyEnv: "ROOM_KEY", Tools: []string{}},
		},
	}
}

func embeddedBrowserConfig() *rooms.BrowserToolsConfig {
	return &rooms.BrowserToolsConfig{
		Backend:   rooms.BrowserToolsBackendWebMCP,
		Selection: rooms.BrowserSelectionConfig{AutoSelect: rooms.BrowserAutoSelectOff},
		Policy: rooms.BrowserPolicyConfig{
			Approval:          rooms.BrowserApprovalWrites,
			CancelOnInterrupt: rooms.BrowserCancelOnInterruptReadOnly,
		},
		Limits: rooms.BrowserLimitsConfig{InvocationTimeout: 20 * time.Second},
	}
}

type embeddedBrowserExecutor struct{ id string }

func (e *embeddedBrowserExecutor) Execute(_ context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	if e == nil {
		return messages.ToolCallResponse{}, errors.New("nil browser executor")
	}
	return messages.ToolCallResponse{ToolCallID: call.ID, Name: call.Name, Content: e.id}, nil
}

type embeddedRoomBrowserLive struct {
	mu              sync.Mutex
	requests        []session.LiveRequest
	failParticipant string
}

func (s *embeddedRoomBrowserLive) OpenLive(ctx context.Context, request session.LiveRequest) (session.LiveHandle, error) {
	if request.Capabilities != nil {
		if request.Capabilities.Handle != nil {
			if err := request.Capabilities.Handle.Initialize(ctx); err != nil {
				return nil, err
			}
			if _, err := request.Capabilities.Handle.RefreshDefinitions(ctx); err != nil {
				return nil, err
			}
		}
	}
	s.mu.Lock()
	s.requests = append(s.requests, request)
	fail := request.ParticipantID == s.failParticipant
	s.mu.Unlock()
	if fail {
		return nil, errors.New("provider construction failed")
	}
	return &embeddedRoomBrowserHandle{capability: request.Capabilities, events: make(chan session.LiveEvent, 1), done: make(chan struct{})}, nil
}

func (s *embeddedRoomBrowserLive) requestsSnapshot() []session.LiveRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]session.LiveRequest(nil), s.requests...)
}

type embeddedRoomBrowserHandle struct {
	capability *session.LiveCapabilities
	events     chan session.LiveEvent
	done       chan struct{}
	once       sync.Once
	closeOnce  sync.Once
	closeErr   error
}

func (h *embeddedRoomBrowserHandle) Media() audio.MediaEndpoints      { return audio.MediaEndpoints{} }
func (h *embeddedRoomBrowserHandle) Events() <-chan session.LiveEvent { return h.events }

func (h *embeddedRoomBrowserHandle) Start(context.Context) error {
	h.events <- session.LiveEvent{Kind: "turn_completed"}
	return nil
}

func (*embeddedRoomBrowserHandle) Send(context.Context, session.LiveControl) error { return nil }

func (h *embeddedRoomBrowserHandle) Cancel(error) { h.once.Do(func() { close(h.done) }) }

func (h *embeddedRoomBrowserHandle) Wait() error {
	<-h.done
	return nil
}

func (h *embeddedRoomBrowserHandle) Close() error {
	h.closeOnce.Do(func() {
		if h.capability != nil && h.capability.Handle != nil {
			h.closeErr = h.capability.Handle.Close()
		}
		close(h.events)
	})
	return h.closeErr
}

var _ session.LiveService = (*embeddedRoomBrowserLive)(nil)
var _ session.LiveHandle = (*embeddedRoomBrowserHandle)(nil)
