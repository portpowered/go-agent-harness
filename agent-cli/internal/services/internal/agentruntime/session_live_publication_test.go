package agentruntime

import (
	"context"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	webmcpTestkit "github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/testkit"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	webmcpTools "github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/tools"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
)

// The session loop hosts the service-owned dynamic tool publisher; these tests
// drive it through the CLI broker projection and a running agent loop.

const (
	burstEvents               = 3
	dynamicPublisherTestBound = 10 * time.Second
	dynamicPublisherStopBound = time.Second
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
	ctx, cancel := context.WithTimeout(context.Background(), dynamicPublisherTestBound)
	defer cancel()
	runErr := make(chan error, 1)
	go func() {
		runErr <- runAgentLoopSession(ctx, io.Discard, inferencer, sessionLoopOptions{
			WaitForClose:             true,
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
	wantBootstrap := newTurnService().MergeToolDefinitions(base, pageA)
	if !reflect.DeepEqual(bootstrap, wantBootstrap) {
		t.Fatalf("bootstrap provider tools = %#v, want %#v", bootstrap, wantBootstrap)
	}

	currentMu.Lock()
	current = append([]messages.ToolDefinition(nil), pageB...)
	currentMu.Unlock()
	events <- webmcp.BrokerEvent{Type: webmcp.BrokerEventSelected, Sequence: 1}
	waitForDynamicPublisherRefresh(t, ctx, refreshStarted)
	gotB := readDynamicPublisherUpdate(t, ctx, session)
	wantB := newTurnService().MergeToolDefinitions(base, pageB)
	if !reflect.DeepEqual(gotB, wantB) {
		t.Fatalf("A-to-B provider tools = %#v, want %#v", gotB, wantB)
	}

	currentMu.Lock()
	current = append([]messages.ToolDefinition(nil), pageA...)
	currentMu.Unlock()
	events <- webmcp.BrokerEvent{Type: webmcp.BrokerEventGenerationChanged, Sequence: 2}
	waitForDynamicPublisherRefresh(t, ctx, refreshStarted)
	gotA := readDynamicPublisherUpdate(t, ctx, session)
	wantA := newTurnService().MergeToolDefinitions(base, pageA)
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
	case <-time.After(dynamicPublisherStopBound):
		t.Fatal("dynamic publisher did not stop its independent broker watch")
	}
	select {
	case <-runErr:
	case <-time.After(dynamicPublisherStopBound):
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
	timerFactory := &dynamicPublisherTimerFactory{clock: clock, armed: make(chan struct{}, burstEvents)}
	session := newDynamicPublisherTestSession()
	inferencer := &dynamicPublisherTestInferencer{session: session}
	ctx, cancel := context.WithTimeout(context.Background(), dynamicPublisherTestBound)
	defer cancel()
	runErr := make(chan error, 1)
	go func() {
		runErr <- runAgentLoopSession(ctx, io.Discard, inferencer, sessionLoopOptions{
			WaitForClose:             true,
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
	// generation. Each one re-arms the settle boundary; firing it only after
	// the third arm makes the burst deterministic, and it must cause one
	// refresh and one provider update for the final surface.
	events <- webmcp.BrokerEvent{Type: webmcp.BrokerEventSelected, BrowserID: "browser", TargetID: "tab-b", Generation: 2, Sequence: 1}
	events <- webmcp.BrokerEvent{Type: webmcp.BrokerEventGenerationChanged, BrowserID: "browser", TargetID: "tab-b", Generation: 2, Sequence: 2}
	events <- webmcp.BrokerEvent{Type: webmcp.BrokerEventCatalogChanged, BrowserID: "browser", TargetID: "tab-b", Generation: 2, Sequence: 3}
	waitForDynamicPublisherArms(t, ctx, timerFactory.armed, burstEvents)
	clock.Advance(sessionturn.PublicationSettleWindow)

	select {
	case got := <-refreshCall:
		if got != 2 {
			t.Fatalf("burst refresh call = %d, want second overall call", got)
		}
	case <-ctx.Done():
		t.Fatalf("timed out waiting for burst refresh: %v", ctx.Err())
	}
	got := readDynamicPublisherUpdate(t, ctx, session)
	want := newTurnService().MergeToolDefinitions(base, pageB)
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
	case <-time.After(dynamicPublisherStopBound):
		t.Fatal("session loop did not stop after cancellation")
	}
}

func TestSessionDynamicToolPublisher_HermeticCatalogSwitchExecutesCurrentSurface(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), dynamicPublisherTestBound)
	defer cancel()
	catalogs := hermeticPublisherCatalogs()
	fixture := startHermeticPublisher(t, ctx, catalogs.a)

	gotB := fixture.switchCatalog(catalogs.b, webmcp.BrokerEventSelected, 1, "A-to-B")
	assertDynamicPublisherDefinition(t, gotB, "create_document", "create document B", 2)
	fixture.assertStaleGuidance("cube_state")
	created := fixture.call("create-b", "create_document", `{"title":"switch-proof","content":"catalog B"}`)
	lastInvocation := fixture.broker.LastInvocation()
	if !created.OK || lastInvocation.ToolRef != pageToolRef(catalogs.b, "create_document") || string(lastInvocation.Input) != `{"title":"switch-proof","content":"catalog B"}` {
		t.Fatalf("B invocation = envelope=%#v invocation=%#v, want current create_document ref and input", created, lastInvocation)
	}
	fixture.assertSharedAction("shared-b", `{"value":"B"}`, catalogs.b)

	fixture.switchCatalog(catalogs.a, webmcp.BrokerEventGenerationChanged, 2, "B-to-A")
	fixture.assertSharedAction("shared-a", `{"value":"A"}`, catalogs.a)

	gotAChanged := fixture.switchCatalog(catalogs.aChanged, webmcp.BrokerEventCatalogChanged, 3, "catalog add-remove")
	assertDynamicPublisherAbsent(t, gotAChanged, "cube_state", "catalog add-remove")
	assertDynamicPublisherPresent(t, gotAChanged, "read_cube_history", "catalog add-remove")

	gotGeneration := fixture.switchCatalog(catalogs.aGeneration, webmcp.BrokerEventGenerationChanged, 4, "generation replacement")
	assertDynamicPublisherDefinition(t, gotGeneration, "read_cube_history", "read cube history A3", 1)

	fixture.broker.events <- webmcp.BrokerEvent{Type: webmcp.BrokerEventGenerationChanged, Sequence: 5}
	waitForDynamicPublisherRefresh(t, ctx, fixture.refreshStarted)
	select {
	case msg := <-fixture.session.sent:
		t.Fatalf("duplicate canonical event emitted provider message %#v", msg)
	default:
	}
	if fixture.broker.InvocationCount() != 3 || fixture.inferencer.connected != 1 {
		t.Fatalf("page invocations/connections = %d/%d, want three current calls on one persistent connection", fixture.broker.InvocationCount(), fixture.inferencer.connected)
	}
	cancel()
	fixture.awaitStopped()
}

type hermeticCatalogs struct {
	a, b, aChanged, aGeneration webmcp.ToolCatalogSnapshot
}

func hermeticPublisherCatalogs() hermeticCatalogs {
	const (
		emptySchema = `{"type":"object","properties":{},"additionalProperties":false}`
		valueSchema = `{"type":"object","properties":{"value":{"type":"string"}},"required":["value"],"additionalProperties":false}`
	)
	return hermeticCatalogs{
		a: dynamicPublisherCatalog(
			dynamicPublisherPageTool("cube_state", "cube state A", "AAAAAAAAAAAAAAAAAAAAAA", emptySchema),
			dynamicPublisherPageTool("shared_action", "shared action A", "AQAAAAAAAAAAAAAAAAAAAA", valueSchema),
		),
		b: dynamicPublisherCatalogAt(2,
			dynamicPublisherPageTool("create_document", "create document B", "BAAAAAAAAAAAAAAAAAAAAA", `{"type":"object","properties":{"title":{"type":"string","description":"Document title."},"content":{"type":"string","description":"Document body."}},"required":["title","content"],"additionalProperties":false}`),
			dynamicPublisherPageTool("get_document", "get document B", "BQAAAAAAAAAAAAAAAAAAAA", `{"type":"object","properties":{"title":{"type":"string"}},"required":["title"],"additionalProperties":false}`),
			dynamicPublisherPageTool("shared_action", "shared action B", "BQAAAAAAAAAAAAAAAAAAAB", valueSchema),
		),
		aChanged: dynamicPublisherCatalogAt(3,
			dynamicPublisherPageTool("shared_action", "shared action A2", "CQAAAAAAAAAAAAAAAAAAAA", valueSchema),
			dynamicPublisherPageTool("read_cube_history", "read cube history A2", "CQAAAAAAAAAAAAAAAAAAAB", emptySchema),
		),
		aGeneration: dynamicPublisherCatalogAt(4,
			dynamicPublisherPageTool("read_cube_history", "read cube history A3", "DQAAAAAAAAAAAAAAAAAAAA", `{"type":"object","properties":{"limit":{"type":"number","description":"Maximum entries."}},"required":["limit"],"additionalProperties":false}`),
			dynamicPublisherPageTool("shared_action", "shared action A3", "DQAAAAAAAAAAAAAAAAAAAB", `{"type":"object","properties":{"value":{"type":"string","description":"Generation four value."}},"required":["value"],"additionalProperties":false}`),
		),
	}
}

// hermeticPublisher runs one session loop over a broker-backed page tool set
// and the service-owned dynamic publisher.
type hermeticPublisher struct {
	t              *testing.T
	ctx            context.Context
	broker         *dynamicPublisherCatalogBroker
	toolSet        *webmcpTools.BrokerToolSet
	base           []messages.ToolDefinition
	session        *dynamicPublisherTestSession
	inferencer     *dynamicPublisherTestInferencer
	refreshStarted chan struct{}
	runErr         chan error
}

func startHermeticPublisher(t *testing.T, ctx context.Context, initial webmcp.ToolCatalogSnapshot) *hermeticPublisher {
	t.Helper()
	broker := newDynamicPublisherCatalogBroker(initial)
	toolSet := webmcpTools.NewBrokerToolSet(broker)
	base := append([]messages.ToolDefinition{dynamicPublisherTestDefinition("static_tool", "static")}, toolSet.Definitions()...)
	reservedNames := make([]string, 0, len(base))
	for _, definition := range base {
		reservedNames = append(reservedNames, definition.Name)
	}
	toolSet.SetReservedToolNames(reservedNames)
	initialPage, err := toolSet.PageToolDefinitionsWithError(ctx)
	if err != nil {
		t.Fatalf("initial page definitions: %v", err)
	}
	session := newDynamicPublisherTestSession()
	fixture := &hermeticPublisher{t: t, ctx: ctx, broker: broker, toolSet: toolSet, base: base, session: session,
		inferencer: &dynamicPublisherTestInferencer{session: session}, refreshStarted: make(chan struct{}, 8), runErr: make(chan error, 1)}
	go func() {
		fixture.runErr <- runAgentLoopSession(ctx, io.Discard, fixture.inferencer, sessionLoopOptions{
			WaitForClose: true, ToolExecutor: toolSet.Executor(),
			ToolDefinitions: append(append([]messages.ToolDefinition(nil), base...), initialPage...), ToolDefinitionBase: base,
			RefreshToolDefinitions: fixture.refresh, BrowserWatch: broker.Watch, AdvertiseToolDefinitions: true,
		})
	}()
	waitForDynamicPublisherRefresh(t, ctx, fixture.refreshStarted)
	assertDynamicPublisherSurface(t, readDynamicPublisherUpdate(t, ctx, session), base, initialPage, "bootstrap A")
	return fixture
}

func (f *hermeticPublisher) refresh(ctx context.Context) ([]messages.ToolDefinition, error) {
	select {
	case f.refreshStarted <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	page, err := f.toolSet.PageToolDefinitionsWithError(ctx)
	if err != nil {
		return nil, err
	}
	return append(append([]messages.ToolDefinition(nil), f.base...), page...), nil
}

// switchCatalog changes the page catalog, announces it, and returns the
// published surface after asserting it matches the current page tools.
func (f *hermeticPublisher) switchCatalog(catalog webmcp.ToolCatalogSnapshot, kind webmcp.BrokerEventType, sequence uint64, label string) []messages.ToolDefinition {
	f.t.Helper()
	f.broker.SetCatalog(catalog)
	f.broker.events <- webmcp.BrokerEvent{Type: kind, Sequence: sequence}
	waitForDynamicPublisherRefresh(f.t, f.ctx, f.refreshStarted)
	got := readDynamicPublisherUpdate(f.t, f.ctx, f.session)
	assertDynamicPublisherSurface(f.t, got, f.base, pageDefinitionsFromCatalog(f.t, f.toolSet, f.ctx, catalog), label)
	return got
}

func (f *hermeticPublisher) call(id, name, arguments string) webmcp.ToolResultEnvelope {
	f.t.Helper()
	return executeDynamicPublisherPageCall(f.t, f.toolSet.Executor(), id, name, arguments)
}

func (f *hermeticPublisher) assertStaleGuidance(staleName string) {
	f.t.Helper()
	before := f.broker.InvocationCount()
	stale := f.call("stale-a", staleName, `{}`)
	if stale.OK || stale.Error == nil || stale.Error.Code != string(webmcp.ErrorStaleToolRef) {
		f.t.Fatalf("stale response = %#v, want stale guidance envelope", stale)
	}
	if !strings.Contains(stale.Error.Message, "create_document") || !strings.Contains(stale.Error.Message, webmcp.ListToolsToolName) {
		f.t.Fatalf("stale guidance = %q, want current catalog and stable recovery path", stale.Error.Message)
	}
	if got := f.broker.InvocationCount(); got != before {
		f.t.Fatalf("stale invocation count = %d, want unchanged at %d", got, before)
	}
}

func (f *hermeticPublisher) assertSharedAction(id, arguments string, catalog webmcp.ToolCatalogSnapshot) {
	f.t.Helper()
	shared := f.call(id, "shared_action", arguments)
	if !shared.OK || f.broker.LastInvocation().ToolRef != pageToolRef(catalog, "shared_action") {
		f.t.Fatalf("same-name invocation = envelope=%#v invocation=%#v, want current catalog ref", shared, f.broker.LastInvocation())
	}
}

func (f *hermeticPublisher) awaitStopped() {
	f.t.Helper()
	select {
	case <-f.broker.watchStopped:
	case <-time.After(dynamicPublisherStopBound):
		f.t.Fatal("publisher did not cancel its independent broker watch")
	}
	select {
	case <-f.runErr:
	case <-time.After(dynamicPublisherStopBound):
		f.t.Fatal("session loop did not stop after cancellation")
	}
}
