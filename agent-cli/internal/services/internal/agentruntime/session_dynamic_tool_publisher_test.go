package agentruntime

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	webmcpTestkit "github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/testkit"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestSessionDynamicToolPublisher_ReplacesDefinitionsInOneRunningSession(t *testing.T) {
	base := []messages.ToolDefinition{
		dynamicPublisherTestDefinition("static_tool", "static"),
		dynamicPublisherTestDefinition("webmcp_select_tab", "stable"),
	}
	pageA := []messages.ToolDefinition{dynamicPublisherTestDefinition("cube_state", "cube A")}
	pageB := []messages.ToolDefinition{
		dynamicPublisherTestDefinition("create_document", "document B"),
		dynamicPublisherTestDefinition("get_document", "read document B"),
	}
	current := append([]messages.ToolDefinition(nil), pageA...)
	var currentMu sync.Mutex
	refreshStarted := make(chan struct{}, 8)
	refresh := func(ctx context.Context) ([]messages.ToolDefinition, error) {
		select {
		case refreshStarted <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		currentMu.Lock()
		defer currentMu.Unlock()
		return append([]messages.ToolDefinition(nil), current...), nil
	}

	events := make(chan webmcp.BrokerEvent, 8)
	watchStopped := make(chan struct{})
	var watchOnce sync.Once
	watch := func(ctx context.Context) <-chan webmcp.BrokerEvent {
		go func() {
			<-ctx.Done()
			watchOnce.Do(func() { close(watchStopped) })
		}()
		return events
	}

	session := newDynamicPublisherTestSession()
	inferencer := &dynamicPublisherTestInferencer{session: session}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	runErr := make(chan error, 1)
	go func() {
		runErr <- runTestAgentLoopSession(ctx, io.Discard, inferencer, sessionLoopOptions{
			WaitForClose: true, audioService: newTestAudioIOService(),
			ToolExecutor:             &messages.DefaultToolExecutor{},
			ToolDefinitions:          append(append([]messages.ToolDefinition(nil), base...), pageA...),
			ToolDefinitionBase:       base,
			RefreshToolDefinitions:   refresh,
			BrowserWatch:             watch,
			AdvertiseToolDefinitions: true,
		})
	}()

	// The first refresh is released only after SESSION.OPEN has reached the
	// service loop. It establishes the catalog-A baseline without emitting a
	// redundant update.
	waitForDynamicPublisherRefresh(t, ctx, refreshStarted)
	bootstrap := readDynamicPublisherUpdate(t, ctx, session)
	wantBootstrap := mergeSessionToolDefinitionBase(base, pageA)
	if !reflect.DeepEqual(bootstrap, wantBootstrap) {
		t.Fatalf("bootstrap provider tools = %#v, want %#v", bootstrap, wantBootstrap)
	}

	currentMu.Lock()
	current = append([]messages.ToolDefinition(nil), pageB...)
	currentMu.Unlock()
	events <- webmcp.BrokerEvent{Type: webmcp.BrokerEventSelected, Sequence: 1}
	waitForDynamicPublisherRefresh(t, ctx, refreshStarted)
	gotB := readDynamicPublisherUpdate(t, ctx, session)
	wantB := mergeSessionToolDefinitionBase(base, pageB)
	if !reflect.DeepEqual(gotB, wantB) {
		t.Fatalf("A-to-B provider tools = %#v, want %#v", gotB, wantB)
	}

	currentMu.Lock()
	current = append([]messages.ToolDefinition(nil), pageA...)
	currentMu.Unlock()
	events <- webmcp.BrokerEvent{Type: webmcp.BrokerEventGenerationChanged, Sequence: 2}
	waitForDynamicPublisherRefresh(t, ctx, refreshStarted)
	gotA := readDynamicPublisherUpdate(t, ctx, session)
	wantA := mergeSessionToolDefinitionBase(base, pageA)
	if !reflect.DeepEqual(gotA, wantA) {
		t.Fatalf("B-to-A provider tools = %#v, want %#v", gotA, wantA)
	}

	currentMu.Lock()
	current = nil
	currentMu.Unlock()
	events <- webmcp.BrokerEvent{Type: webmcp.BrokerEventCatalogChanged, Sequence: 3}
	waitForDynamicPublisherRefresh(t, ctx, refreshStarted)
	gotEmpty := readDynamicPublisherUpdate(t, ctx, session)
	if !reflect.DeepEqual(gotEmpty, base) {
		t.Fatalf("empty ready catalog provider tools = %#v, want stable base %#v", gotEmpty, base)
	}
	if inferencer.connected != 1 {
		t.Fatalf("provider connections = %d, want one persistent connection", inferencer.connected)
	}

	cancel()
	select {
	case <-watchStopped:
	case <-time.After(time.Second):
		t.Fatal("dynamic publisher did not stop its independent broker watch")
	}
	select {
	case <-runErr:
	case <-time.After(time.Second):
		t.Fatal("session loop did not stop after cancellation")
	}
}

func TestSessionDynamicToolPublisher_CoalescesSelectionCatalogBurst(t *testing.T) {
	base := []messages.ToolDefinition{
		dynamicPublisherTestDefinition("static_tool", "static"),
		dynamicPublisherTestDefinition("webmcp_select_tab", "stable"),
	}
	pageA := []messages.ToolDefinition{dynamicPublisherTestDefinition("cube_state", "cube A")}
	pageB := []messages.ToolDefinition{dynamicPublisherTestDefinition("create_document", "document B")}

	var refreshMu sync.Mutex
	refreshCalls := 0
	refreshCall := make(chan int, 8)
	refresh := func(ctx context.Context) ([]messages.ToolDefinition, error) {
		refreshMu.Lock()
		refreshCalls++
		call := refreshCalls
		refreshMu.Unlock()
		select {
		case refreshCall <- call:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if call == 1 {
			return append([]messages.ToolDefinition(nil), pageA...), nil
		}
		return append([]messages.ToolDefinition(nil), pageB...), nil
	}

	events := make(chan webmcp.BrokerEvent, 8)
	watch := func(context.Context) <-chan webmcp.BrokerEvent { return events }
	clock := webmcpTestkit.NewFakeClock(time.Unix(0, 0).UTC())
	timerFactory := &dynamicPublisherTimerFactory{clock: clock, created: make(chan struct{}, 2)}
	session := newDynamicPublisherTestSession()
	inferencer := &dynamicPublisherTestInferencer{session: session}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	runErr := make(chan error, 1)
	go func() {
		runErr <- runTestAgentLoopSession(ctx, io.Discard, inferencer, sessionLoopOptions{
			WaitForClose: true, audioService: newTestAudioIOService(),
			ToolExecutor:             &messages.DefaultToolExecutor{},
			ToolDefinitions:          append(append([]messages.ToolDefinition(nil), base...), pageA...),
			ToolDefinitionBase:       base,
			RefreshToolDefinitions:   refresh,
			BrowserWatch:             watch,
			PublicationTimerFactory:  timerFactory,
			AdvertiseToolDefinitions: true,
		})
	}()

	select {
	case got := <-refreshCall:
		if got != 1 {
			t.Fatalf("initial refresh call = %d, want one", got)
		}
	case <-ctx.Done():
		t.Fatalf("timed out waiting for initial refresh: %v", ctx.Err())
	}
	_ = readDynamicPublisherUpdate(t, ctx, session)

	// The three notifications represent one selected target/catalog
	// generation. They are queued as a burst before the settle boundary and
	// must cause one refresh and one provider update for the final surface.
	events <- webmcp.BrokerEvent{Type: webmcp.BrokerEventSelected, BrowserID: "browser", TargetID: "tab-b", Generation: 2, Sequence: 1}
	events <- webmcp.BrokerEvent{Type: webmcp.BrokerEventGenerationChanged, BrowserID: "browser", TargetID: "tab-b", Generation: 2, Sequence: 2}
	events <- webmcp.BrokerEvent{Type: webmcp.BrokerEventCatalogChanged, BrowserID: "browser", TargetID: "tab-b", Generation: 2, Sequence: 3}
	select {
	case <-timerFactory.created:
	case <-ctx.Done():
		t.Fatalf("timed out waiting for burst settle boundary: %v", ctx.Err())
	}
	clock.Advance(sessionDynamicToolPublicationSettleWindow)

	select {
	case got := <-refreshCall:
		if got != 2 {
			t.Fatalf("burst refresh call = %d, want second overall call", got)
		}
	case <-ctx.Done():
		t.Fatalf("timed out waiting for burst refresh: %v", ctx.Err())
	}
	got := readDynamicPublisherUpdate(t, ctx, session)
	want := mergeSessionToolDefinitionBase(base, pageB)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("burst provider tools = %#v, want %#v", got, want)
	}
	refreshMu.Lock()
	gotRefreshCalls := refreshCalls
	refreshMu.Unlock()
	if gotRefreshCalls != 2 {
		t.Fatalf("refresh calls = %d, want initial plus one burst refresh", gotRefreshCalls)
	}

	cancel()
	select {
	case <-runErr:
	case <-time.After(time.Second):
		t.Fatal("session loop did not stop after cancellation")
	}
}

type dynamicPublisherTimerFactory struct {
	clock   *webmcpTestkit.FakeClock
	created chan struct{}
}

func (f *dynamicPublisherTimerFactory) NewTimer(duration time.Duration) webmcp.Timer {
	timer := f.clock.NewTimer(duration)
	select {
	case f.created <- struct{}{}:
	default:
	}
	return timer
}

func TestSessionDynamicToolPublisher_HermeticCatalogSwitchExecutesCurrentSurface(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	scenario := newDynamicPublisherCatalogSwitch(t, ctx, cancel)
	scenario.assertBootstrap(t, ctx)
	scenario.switchToB(t, ctx)
	scenario.switchBackToA(t, ctx)
	scenario.replaceCatalog(t, ctx)
	scenario.replaceGeneration(t, ctx)
	scenario.assertPersistentSession(t, ctx)
	scenario.stop(t)
}

func TestSessionDynamicToolPublisher_NoOpRefreshKeepsLastSuccessfulState(t *testing.T) {
	base := []messages.ToolDefinition{dynamicPublisherTestDefinition("stable_tool", "stable")}
	page := []messages.ToolDefinition{dynamicPublisherTestDefinition("page_tool", "page")}
	publisher := newSessionDynamicToolPublisher(
		base,
		append(append([]messages.ToolDefinition(nil), base...), page...),
		func(context.Context) <-chan webmcp.BrokerEvent { return make(chan webmcp.BrokerEvent) },
		func(context.Context) ([]messages.ToolDefinition, error) {
			return append([]messages.ToolDefinition(nil), page...), nil
		},
	)
	if !publisher.consumeEvent(webmcp.BrokerEvent{Type: webmcp.BrokerEventCatalogChanged, Sequence: 9}) {
		t.Fatal("first sequenced catalog event was not accepted")
	}
	if publisher.consumeEvent(webmcp.BrokerEvent{Type: webmcp.BrokerEventCatalogChanged, Sequence: 9}) {
		t.Fatal("duplicate sequenced catalog event triggered a second refresh")
	}

	if err := publisher.refreshAndPublish(context.Background(), nil, "duplicate_event"); err != nil {
		t.Fatalf("no-op refresh = %v, want nil", err)
	}
	state := publisher.stateSnapshot()
	want := mergeSessionToolDefinitionBase(base, page)
	if !reflect.DeepEqual(state.LastSuccessfulDefinitions, want) {
		t.Fatalf("last successful definitions = %#v, want %#v", state.LastSuccessfulDefinitions, want)
	}
	if state.PublicationCount != 0 {
		t.Fatalf("publication count = %d, want zero for unchanged canonical tools", state.PublicationCount)
	}
}

func TestSessionToolDefinitionDigestIncludesCompleteParameterSchema(t *testing.T) {
	first := dynamicPublisherTestDefinition("page_tool", "page")
	first.ParameterSchema = json.RawMessage(`{"type":"object","properties":{"moves":{"type":"array","items":{"type":"string"}}}}`)
	second := first
	second.ParameterSchema = json.RawMessage(`{"type":"object","properties":{"moves":{"type":"array","items":{"type":"integer"}}}}`)

	firstDigest, err := sessionToolDefinitionDigest([]messages.ToolDefinition{first})
	if err != nil {
		t.Fatalf("digest first definition: %v", err)
	}
	secondDigest, err := sessionToolDefinitionDigest([]messages.ToolDefinition{second})
	if err != nil {
		t.Fatalf("digest second definition: %v", err)
	}
	if firstDigest == secondDigest {
		t.Fatalf("schema-only definition change did not change digest: %s", firstDigest)
	}
}

func TestSessionDynamicToolPublisher_RejectsStaleGenerationNotifications(t *testing.T) {
	base := []messages.ToolDefinition{dynamicPublisherTestDefinition("stable_tool", "stable")}
	page := []messages.ToolDefinition{dynamicPublisherTestDefinition("page_tool", "page")}
	publisher := newSessionDynamicToolPublisher(
		base,
		append(append([]messages.ToolDefinition(nil), base...), page...),
		func(context.Context) <-chan webmcp.BrokerEvent { return make(chan webmcp.BrokerEvent) },
		func(context.Context) ([]messages.ToolDefinition, error) {
			return page, nil
		},
	)
	definitions := mergeSessionToolDefinitionBase(base, page)
	digest, err := sessionToolDefinitionDigest(definitions)
	if err != nil {
		t.Fatalf("published definition digest: %v", err)
	}
	published := sessionDynamicToolPublicationEvent{
		browserID:  "browser",
		targetID:   "tab",
		generation: 4,
		sequence:   10,
	}
	publisher.commitSuccessfulPublication(published, true, definitions, digest, true)

	if publisher.consumeEvent(webmcp.BrokerEvent{
		Type:       webmcp.BrokerEventCatalogChanged,
		BrowserID:  published.browserID,
		TargetID:   published.targetID,
		Generation: 3,
		Sequence:   11,
	}) {
		t.Fatal("stale generation notification was accepted")
	}
	if publisher.hasPending {
		t.Fatal("stale generation notification left pending publication work")
	}
	state := publisher.stateSnapshot()
	if state.LastSuccessfulGeneration != published.generation || state.LastSuccessfulEventSequence != published.sequence {
		t.Fatalf("stale notification advanced last successful state = %#v", state)
	}
	if state.LatestEventSequence != 11 {
		t.Fatalf("latest event sequence = %d, want 11 after observing stale event", state.LatestEventSequence)
	}

	if !publisher.consumeEvent(webmcp.BrokerEvent{
		Type:       webmcp.BrokerEventGenerationChanged,
		BrowserID:  published.browserID,
		TargetID:   published.targetID,
		Generation: 5,
		Sequence:   12,
	}) {
		t.Fatal("newer generation notification was not accepted")
	}
	if publisher.consumeEvent(webmcp.BrokerEvent{
		Type:       webmcp.BrokerEventCatalogChanged,
		BrowserID:  published.browserID,
		TargetID:   published.targetID,
		Generation: 4,
		Sequence:   13,
	}) {
		t.Fatal("out-of-order older generation replaced newer pending work")
	}
	if publisher.pending.generation != 5 {
		t.Fatalf("pending generation = %d, want newer generation 5", publisher.pending.generation)
	}
}

func TestSessionDynamicToolPublisher_RefreshFailureRetainsLastSuccessfulState(t *testing.T) {
	base := []messages.ToolDefinition{dynamicPublisherTestDefinition("stable_tool", "stable")}
	pageA := []messages.ToolDefinition{dynamicPublisherTestDefinition("page_a", "page A")}
	pageB := []messages.ToolDefinition{dynamicPublisherTestDefinition("page_b", "page B")}
	refreshErr := errors.New("catalog transport unavailable")
	publisher := newSessionDynamicToolPublisher(
		base,
		append(append([]messages.ToolDefinition(nil), base...), pageA...),
		func(context.Context) <-chan webmcp.BrokerEvent { return make(chan webmcp.BrokerEvent) },
		func(context.Context) ([]messages.ToolDefinition, error) {
			return pageB, refreshErr
		},
	)
	publisher.consumeEvent(webmcp.BrokerEvent{
		Type:       webmcp.BrokerEventCatalogChanged,
		BrowserID:  "browser",
		TargetID:   "tab",
		Generation: 2,
		Sequence:   7,
	})

	err := publisher.refreshAndPublish(context.Background(), nil, "broker_event")
	if !errors.Is(err, ErrSessionDynamicToolPublication) || !errors.Is(err, refreshErr) {
		t.Fatalf("refresh error = %v, want publication and catalog causes", err)
	}
	state := publisher.stateSnapshot()
	want := mergeSessionToolDefinitionBase(base, pageA)
	if !reflect.DeepEqual(state.LastSuccessfulDefinitions, want) {
		t.Fatalf("failed refresh advanced definitions = %#v, want %#v", state.LastSuccessfulDefinitions, want)
	}
	if state.LatestEventSequence != 7 {
		t.Fatalf("latest event sequence = %d, want 7", state.LatestEventSequence)
	}
	if state.LastSuccessfulGeneration != 0 || state.LastSuccessfulEventSequence != 0 {
		t.Fatalf("failed refresh advanced successful generation state = %#v", state)
	}
	if !publisher.hasPending {
		t.Fatal("failed refresh discarded pending generation before successful delivery")
	}
	if state.Lifecycle != SessionDynamicToolPublicationFailed || state.Err == nil {
		t.Fatalf("failure state = %#v, want bounded failed lifecycle", state)
	}
	if len(err.Error()) > 420 {
		t.Fatalf("failure diagnostic length = %d, want bounded output", len(err.Error()))
	}
}
