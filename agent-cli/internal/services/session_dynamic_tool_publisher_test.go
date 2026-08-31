package services

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	webmcpTools "github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/tools"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

type dynamicPublisherTestSession struct {
	recv      *messages.TypedBuffer[messages.StreamMessage]
	sent      chan messages.StreamMessage
	done      chan struct{}
	closeOnce sync.Once
}

func newDynamicPublisherTestSession() *dynamicPublisherTestSession {
	return &dynamicPublisherTestSession{
		recv: messages.NewTypedBuffer[messages.StreamMessage](16),
		sent: make(chan messages.StreamMessage, 16),
		done: make(chan struct{}),
	}
}

func (s *dynamicPublisherTestSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	select {
	case s.sent <- msg:
		return true
	case <-ctx.Done():
		return false
	}
}

func (s *dynamicPublisherTestSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.recv
}

func (s *dynamicPublisherTestSession) Done() <-chan struct{} { return s.done }

func (s *dynamicPublisherTestSession) Close() error {
	s.closeOnce.Do(func() { close(s.done) })
	return nil
}

type dynamicPublisherTestInferencer struct {
	session   *dynamicPublisherTestSession
	connected int
}

func (i *dynamicPublisherTestInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	i.connected++
	if !i.session.recv.Write(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeSessionOpen,
		Value: messages.NewSessionOpenValue("dynamic-publisher-session", "test"),
	}) {
		return nil, ctx.Err()
	}
	if !i.session.recv.Write(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeSessionCreated,
		Value: messages.NewSessionCreatedValue("dynamic-publisher-session", "test"),
	}) {
		return nil, ctx.Err()
	}
	return i.session, nil
}

func dynamicPublisherTestDefinition(name, description string) messages.ToolDefinition {
	return messages.ToolDefinition{
		Name:        name,
		Description: description,
		Parameters: []messages.ToolParameter{{
			Name:        "value",
			Type:        "string",
			Description: "value for the test tool",
			Required:    true,
		}},
		ParametersClosed: true,
	}
}

func waitForDynamicPublisherRefresh(t *testing.T, ctx context.Context, refreshStarted <-chan struct{}) {
	t.Helper()
	select {
	case <-refreshStarted:
	case <-ctx.Done():
		t.Fatalf("timed out waiting for catalog refresh: %v", ctx.Err())
	}
}

func readDynamicPublisherUpdate(t *testing.T, ctx context.Context, session *dynamicPublisherTestSession) []messages.ToolDefinition {
	t.Helper()
	select {
	case msg := <-session.sent:
		if msg.Type != messages.StreamTypeSessionUpdate {
			t.Fatalf("provider message type = %s, want %s", msg.Type, messages.StreamTypeSessionUpdate)
		}
		value, ok := msg.Value.(*messages.SessionUpdateValue)
		if !ok || value == nil {
			t.Fatalf("provider message value = %T, want *messages.SessionUpdateValue", msg.Value)
		}
		return value.Tools
	case <-ctx.Done():
		t.Fatalf("timed out waiting for provider session.update: %v", ctx.Err())
		return nil
	}
}

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

func TestSessionDynamicToolPublisher_HermeticCatalogSwitchExecutesCurrentSurface(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	pageA := dynamicPublisherCatalog(
		dynamicPublisherPageTool("cube_state", "cube state A", "AAAAAAAAAAAAAAAAAAAAAA", `{"type":"object","properties":{},"additionalProperties":false}`),
		dynamicPublisherPageTool("shared_action", "shared action A", "AQAAAAAAAAAAAAAAAAAAAA", `{"type":"object","properties":{"value":{"type":"string"}},"required":["value"],"additionalProperties":false}`),
	)
	pageB := dynamicPublisherCatalogAt(2,
		dynamicPublisherPageTool("create_document", "create document B", "BAAAAAAAAAAAAAAAAAAAAA", `{"type":"object","properties":{"title":{"type":"string","description":"Document title."},"content":{"type":"string","description":"Document body."}},"required":["title","content"],"additionalProperties":false}`),
		dynamicPublisherPageTool("get_document", "get document B", "BQAAAAAAAAAAAAAAAAAAAA", `{"type":"object","properties":{"title":{"type":"string"}},"required":["title"],"additionalProperties":false}`),
		dynamicPublisherPageTool("shared_action", "shared action B", "BQAAAAAAAAAAAAAAAAAAAB", `{"type":"object","properties":{"value":{"type":"string"}},"required":["value"],"additionalProperties":false}`),
	)
	pageAChanged := dynamicPublisherCatalogAt(3,
		dynamicPublisherPageTool("shared_action", "shared action A2", "CQAAAAAAAAAAAAAAAAAAAA", `{"type":"object","properties":{"value":{"type":"string"}},"required":["value"],"additionalProperties":false}`),
		dynamicPublisherPageTool("read_cube_history", "read cube history A2", "CQAAAAAAAAAAAAAAAAAAAB", `{"type":"object","properties":{},"additionalProperties":false}`),
	)
	pageAGenerationChanged := dynamicPublisherCatalogAt(4,
		dynamicPublisherPageTool("read_cube_history", "read cube history A3", "DQAAAAAAAAAAAAAAAAAAAA", `{"type":"object","properties":{"limit":{"type":"number","description":"Maximum entries."}},"required":["limit"],"additionalProperties":false}`),
		dynamicPublisherPageTool("shared_action", "shared action A3", "DQAAAAAAAAAAAAAAAAAAAB", `{"type":"object","properties":{"value":{"type":"string","description":"Generation four value."}},"required":["value"],"additionalProperties":false}`),
	)

	broker := newDynamicPublisherCatalogBroker(pageA)
	toolSet := webmcpTools.NewBrokerToolSet(broker)
	static := []messages.ToolDefinition{dynamicPublisherTestDefinition("static_tool", "static")}
	base := append(append([]messages.ToolDefinition(nil), static...), toolSet.Definitions()...)
	reservedNames := make([]string, 0, len(base))
	for _, definition := range base {
		reservedNames = append(reservedNames, definition.Name)
	}
	toolSet.SetReservedToolNames(reservedNames)
	initialPageDefinitions, err := toolSet.PageToolDefinitionsWithError(ctx)
	if err != nil {
		t.Fatalf("initial page definitions: %v", err)
	}
	initialDefinitions := append(append([]messages.ToolDefinition(nil), base...), initialPageDefinitions...)
	refreshStarted := make(chan struct{}, 8)
	refresh := func(refreshContext context.Context) ([]messages.ToolDefinition, error) {
		select {
		case refreshStarted <- struct{}{}:
		case <-refreshContext.Done():
			return nil, refreshContext.Err()
		}
		pageDefinitions, refreshErr := toolSet.PageToolDefinitionsWithError(refreshContext)
		if refreshErr != nil {
			return nil, refreshErr
		}
		return append(append([]messages.ToolDefinition(nil), base...), pageDefinitions...), nil
	}

	session := newDynamicPublisherTestSession()
	inferencer := &dynamicPublisherTestInferencer{session: session}
	runErr := make(chan error, 1)
	go func() {
		runErr <- runAgentLoopSession(ctx, io.Discard, inferencer, sessionLoopOptions{
			WaitForClose:             true,
			ToolExecutor:             toolSet.Executor(),
			ToolDefinitions:          initialDefinitions,
			ToolDefinitionBase:       base,
			RefreshToolDefinitions:   refresh,
			BrowserWatch:             broker.Watch,
			AdvertiseToolDefinitions: true,
		})
	}()

	waitForDynamicPublisherRefresh(t, ctx, refreshStarted)
	bootstrap := readDynamicPublisherUpdate(t, ctx, session)
	assertDynamicPublisherSurface(t, bootstrap, base, initialPageDefinitions, "bootstrap A")

	broker.SetCatalog(pageB)
	broker.events <- webmcp.BrokerEvent{Type: webmcp.BrokerEventSelected, Sequence: 1}
	waitForDynamicPublisherRefresh(t, ctx, refreshStarted)
	gotB := readDynamicPublisherUpdate(t, ctx, session)
	pageBDefinitions := pageDefinitionsFromCatalog(t, toolSet, ctx, pageB)
	assertDynamicPublisherSurface(t, gotB, base, pageBDefinitions, "A-to-B")
	assertDynamicPublisherDefinition(t, gotB, "create_document", "create document B", 2)

	invocationsBeforeStale := broker.InvocationCount()
	staleResponse := executeDynamicPublisherPageCall(t, toolSet.Executor(), "stale-a", "cube_state", `{}`)
	if staleResponse.OK || staleResponse.Error == nil || staleResponse.Error.Code != string(webmcp.ErrorStaleToolRef) {
		t.Fatalf("stale A-only response = %#v, want stale guidance envelope", staleResponse)
	}
	if !strings.Contains(staleResponse.Error.Message, "create_document") || !strings.Contains(staleResponse.Error.Message, webmcp.ListToolsToolName) {
		t.Fatalf("stale guidance = %q, want current catalog and stable recovery path", staleResponse.Error.Message)
	}
	if got := broker.InvocationCount(); got != invocationsBeforeStale {
		t.Fatalf("stale A-only invocation count = %d, want unchanged at %d", got, invocationsBeforeStale)
	}

	created := executeDynamicPublisherPageCall(t, toolSet.Executor(), "create-b", "create_document", `{"title":"switch-proof","content":"catalog B"}`)
	if !created.OK {
		t.Fatalf("newly advertised B tool response = %#v, want success", created)
	}
	lastInvocation := broker.LastInvocation()
	if lastInvocation.ToolRef != pageToolRef(pageB, "create_document") || string(lastInvocation.Input) != `{"title":"switch-proof","content":"catalog B"}` {
		t.Fatalf("B invocation = %#v, want current create_document ref and input", lastInvocation)
	}

	sharedB := executeDynamicPublisherPageCall(t, toolSet.Executor(), "shared-b", "shared_action", `{"value":"B"}`)
	if !sharedB.OK || broker.LastInvocation().ToolRef != pageToolRef(pageB, "shared_action") {
		t.Fatalf("same-name B invocation = envelope=%#v invocation=%#v, want B ref", sharedB, broker.LastInvocation())
	}

	broker.SetCatalog(pageA)
	broker.events <- webmcp.BrokerEvent{Type: webmcp.BrokerEventGenerationChanged, Sequence: 2}
	waitForDynamicPublisherRefresh(t, ctx, refreshStarted)
	gotA := readDynamicPublisherUpdate(t, ctx, session)
	pageADefinitions := pageDefinitionsFromCatalog(t, toolSet, ctx, pageA)
	assertDynamicPublisherSurface(t, gotA, base, pageADefinitions, "B-to-A")
	sharedA := executeDynamicPublisherPageCall(t, toolSet.Executor(), "shared-a", "shared_action", `{"value":"A"}`)
	if !sharedA.OK || broker.LastInvocation().ToolRef != pageToolRef(pageA, "shared_action") {
		t.Fatalf("same-name A invocation = envelope=%#v invocation=%#v, want A ref", sharedA, broker.LastInvocation())
	}

	broker.SetCatalog(pageAChanged)
	broker.events <- webmcp.BrokerEvent{Type: webmcp.BrokerEventCatalogChanged, Sequence: 3}
	waitForDynamicPublisherRefresh(t, ctx, refreshStarted)
	gotAChanged := readDynamicPublisherUpdate(t, ctx, session)
	pageAChangedDefinitions := pageDefinitionsFromCatalog(t, toolSet, ctx, pageAChanged)
	assertDynamicPublisherSurface(t, gotAChanged, base, pageAChangedDefinitions, "catalog add-remove")
	assertDynamicPublisherAbsent(t, gotAChanged, "cube_state", "catalog add-remove")
	assertDynamicPublisherPresent(t, gotAChanged, "read_cube_history", "catalog add-remove")

	broker.SetCatalog(pageAGenerationChanged)
	broker.events <- webmcp.BrokerEvent{Type: webmcp.BrokerEventGenerationChanged, Sequence: 4}
	waitForDynamicPublisherRefresh(t, ctx, refreshStarted)
	gotGenerationChanged := readDynamicPublisherUpdate(t, ctx, session)
	pageAGenerationDefinitions := pageDefinitionsFromCatalog(t, toolSet, ctx, pageAGenerationChanged)
	assertDynamicPublisherSurface(t, gotGenerationChanged, base, pageAGenerationDefinitions, "generation replacement")
	assertDynamicPublisherDefinition(t, gotGenerationChanged, "read_cube_history", "read cube history A3", 1)

	broker.events <- webmcp.BrokerEvent{Type: webmcp.BrokerEventGenerationChanged, Sequence: 5}
	waitForDynamicPublisherRefresh(t, ctx, refreshStarted)
	select {
	case msg := <-session.sent:
		t.Fatalf("duplicate canonical event emitted provider message %#v", msg)
	default:
	}
	if broker.InvocationCount() != 3 {
		t.Fatalf("successful page invocation count = %d, want stale guidance excluded and three current calls", broker.InvocationCount())
	}
	if inferencer.connected != 1 {
		t.Fatalf("provider connections = %d, want one persistent connection", inferencer.connected)
	}

	cancel()
	select {
	case <-broker.watchStopped:
	case <-time.After(time.Second):
		t.Fatal("publisher did not cancel its independent broker watch")
	}
	select {
	case <-runErr:
	case <-time.After(time.Second):
		t.Fatal("session loop did not stop after cancellation")
	}
}

type dynamicPublisherCatalogBroker struct {
	mu           sync.Mutex
	catalog      webmcp.ToolCatalogSnapshot
	events       chan webmcp.BrokerEvent
	invocations  []webmcp.InvokeRequest
	invocationID uint64
	watchStopped chan struct{}
	watchOnce    sync.Once
}

func newDynamicPublisherCatalogBroker(catalog webmcp.ToolCatalogSnapshot) *dynamicPublisherCatalogBroker {
	return &dynamicPublisherCatalogBroker{
		catalog:      cloneDynamicPublisherCatalog(catalog),
		events:       make(chan webmcp.BrokerEvent, 8),
		watchStopped: make(chan struct{}),
	}
}

func (b *dynamicPublisherCatalogBroker) Discover(context.Context, webmcp.DiscoverOptions) ([]webmcp.BrowserCandidate, error) {
	return nil, nil
}

func (b *dynamicPublisherCatalogBroker) ListTargets(context.Context, webmcp.BrowserSelector) ([]webmcp.Target, error) {
	return nil, nil
}

func (b *dynamicPublisherCatalogBroker) Select(_ context.Context, _ webmcp.TargetSelector) (webmcp.PageContext, error) {
	return webmcp.PageContext{}, nil
}

func (b *dynamicPublisherCatalogBroker) Selected(context.Context) (webmcp.PageContext, error) {
	return webmcp.PageContext{}, nil
}

func (b *dynamicPublisherCatalogBroker) ListTools(context.Context, webmcp.ListToolsOptions) (webmcp.ToolCatalogSnapshot, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return cloneDynamicPublisherCatalog(b.catalog), nil
}

func (b *dynamicPublisherCatalogBroker) Invoke(_ context.Context, request webmcp.InvokeRequest) (webmcp.InvokeResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.invocations = append(b.invocations, request)
	b.invocationID++
	return webmcp.InvokeResult{
		InvocationID: webmcp.InvocationID("dynamic-invocation-" + string(rune('0'+b.invocationID))),
		State:        webmcp.InvocationCompleted,
		Output:       json.RawMessage(`{"accepted":true}`),
	}, nil
}

func (b *dynamicPublisherCatalogBroker) Cancel(context.Context, webmcp.CancelRequest) error {
	return nil
}

func (b *dynamicPublisherCatalogBroker) Watch(ctx context.Context) <-chan webmcp.BrokerEvent {
	b.watchOnce.Do(func() {
		go func() {
			<-ctx.Done()
			close(b.watchStopped)
		}()
	})
	return b.events
}

func (b *dynamicPublisherCatalogBroker) Close() error {
	return nil
}

func (b *dynamicPublisherCatalogBroker) SetCatalog(catalog webmcp.ToolCatalogSnapshot) {
	b.mu.Lock()
	b.catalog = cloneDynamicPublisherCatalog(catalog)
	b.mu.Unlock()
}

func (b *dynamicPublisherCatalogBroker) InvocationCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.invocations)
}

func (b *dynamicPublisherCatalogBroker) LastInvocation() webmcp.InvokeRequest {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.invocations) == 0 {
		return webmcp.InvokeRequest{}
	}
	return b.invocations[len(b.invocations)-1]
}

func dynamicPublisherCatalog(tools ...webmcp.ToolDescriptor) webmcp.ToolCatalogSnapshot {
	return dynamicPublisherCatalogAt(1, tools...)
}

func dynamicPublisherCatalogAt(generation uint64, tools ...webmcp.ToolDescriptor) webmcp.ToolCatalogSnapshot {
	return webmcp.ToolCatalogSnapshot{Generation: generation, Tools: tools}
}

func dynamicPublisherPageTool(name, description, refSuffix, schema string) webmcp.ToolDescriptor {
	return webmcp.ToolDescriptor{
		Ref:         webmcp.ToolRef(webmcp.ToolRefPrefix + refSuffix),
		Name:        name,
		Description: description,
		InputSchema: json.RawMessage(schema),
	}
}

func cloneDynamicPublisherCatalog(catalog webmcp.ToolCatalogSnapshot) webmcp.ToolCatalogSnapshot {
	clone := catalog
	clone.Tools = make([]webmcp.ToolDescriptor, len(catalog.Tools))
	for index, descriptor := range catalog.Tools {
		clone.Tools[index] = descriptor
		clone.Tools[index].InputSchema = append(json.RawMessage(nil), descriptor.InputSchema...)
	}
	return clone
}

func pageDefinitionsFromCatalog(t *testing.T, toolSet *webmcpTools.BrokerToolSet, ctx context.Context, catalog webmcp.ToolCatalogSnapshot) []messages.ToolDefinition {
	t.Helper()
	pageDefinitions, err := toolSet.PageToolDefinitionsWithError(ctx)
	if err != nil {
		t.Fatalf("page definitions for catalog %#v: %v", catalog, err)
	}
	return pageDefinitions
}

func pageToolRef(catalog webmcp.ToolCatalogSnapshot, name string) webmcp.ToolRef {
	for _, descriptor := range catalog.Tools {
		if descriptor.Name == name {
			return descriptor.Ref
		}
	}
	return ""
}

func executeDynamicPublisherPageCall(t *testing.T, executor messages.ToolExecutor, id, name, arguments string) webmcp.ToolResultEnvelope {
	t.Helper()
	response, err := executor.Execute(context.Background(), messages.ToolCall{ID: id, Name: name, Arguments: arguments})
	if err != nil {
		t.Fatalf("execute %s: %v", name, err)
	}
	envelope, err := webmcp.UnmarshalToolResult([]byte(response.Content))
	if err != nil {
		t.Fatalf("decode %s result: %v (%s)", name, err, response.Content)
	}
	return envelope
}

func assertDynamicPublisherSurface(t *testing.T, got, base, page []messages.ToolDefinition, label string) {
	t.Helper()
	want := mergeSessionToolDefinitionBase(base, page)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s surface = %#v, want %#v", label, got, want)
	}
	gotByName := make(map[string]messages.ToolDefinition, len(got))
	for _, definition := range got {
		gotByName[definition.Name] = definition
	}
	for _, definition := range messages.CanonicalToolDefinitions(base) {
		if !reflect.DeepEqual(gotByName[definition.Name], definition) {
			t.Fatalf("%s changed base definition %q = %#v, want %#v", label, definition.Name, gotByName[definition.Name], definition)
		}
	}
}

func assertDynamicPublisherDefinition(t *testing.T, definitions []messages.ToolDefinition, name, description string, parameterCount int) {
	t.Helper()
	for _, definition := range definitions {
		if definition.Name != name {
			continue
		}
		if definition.Description != description || len(definition.Parameters) != parameterCount || !definition.ParametersClosed {
			t.Fatalf("definition %q = %#v, want description %q, %d parameters, closed schema", name, definition, description, parameterCount)
		}
		return
	}
	t.Fatalf("definition %q missing from %#v", name, definitions)
}

func assertDynamicPublisherAbsent(t *testing.T, definitions []messages.ToolDefinition, name, label string) {
	t.Helper()
	for _, definition := range definitions {
		if definition.Name == name {
			t.Fatalf("%s unexpectedly contains %q", label, name)
		}
	}
}

func assertDynamicPublisherPresent(t *testing.T, definitions []messages.ToolDefinition, name, label string) {
	t.Helper()
	for _, definition := range definitions {
		if definition.Name == name {
			return
		}
	}
	t.Fatalf("%s is missing %q", label, name)
}

var _ webmcp.Broker = (*dynamicPublisherCatalogBroker)(nil)

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
	publisher.consumeEvent(webmcp.BrokerEvent{Type: webmcp.BrokerEventCatalogChanged, Sequence: 7})

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
	if state.Lifecycle != SessionDynamicToolPublicationFailed || state.Err == nil {
		t.Fatalf("failure state = %#v, want bounded failed lifecycle", state)
	}
	if len(err.Error()) > 420 {
		t.Fatalf("failure diagnostic length = %d, want bounded output", len(err.Error()))
	}
}
