package agentruntime

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	webmcpTools "github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/tools"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

type dynamicPublisherCatalogSwitch struct {
	pageA, pageB, pageAChanged, pageAGenerationChanged webmcp.ToolCatalogSnapshot
	base                                               []messages.ToolDefinition
	initialPageDefinitions                             []messages.ToolDefinition
	broker                                             *dynamicPublisherCatalogBroker
	toolSet                                            *webmcpTools.BrokerToolSet
	session                                            *dynamicPublisherTestSession
	inferencer                                         *dynamicPublisherTestInferencer
	refreshStarted                                     chan struct{}
	runErr                                             chan error
	cancel                                             context.CancelFunc
}

func newDynamicPublisherCatalogSwitch(t *testing.T, ctx context.Context, cancel context.CancelFunc) *dynamicPublisherCatalogSwitch {
	t.Helper()
	pageA, pageB, pageAChanged, pageAGenerationChanged := dynamicPublisherSwitchCatalogs()
	broker := newDynamicPublisherCatalogBroker(pageA)
	toolSet := webmcpTools.NewBrokerToolSet(broker)
	base := dynamicPublisherStableDefinitions(toolSet)
	toolSet.SetReservedToolNames(dynamicPublisherDefinitionNames(base))
	initialPageDefinitions, err := toolSet.PageToolDefinitionsWithError(ctx)
	if err != nil {
		t.Fatalf("initial page definitions: %v", err)
	}
	initial := append(append([]messages.ToolDefinition(nil), base...), initialPageDefinitions...)
	refreshStarted := make(chan struct{}, 8)
	refresh := dynamicPublisherCatalogRefresh(toolSet, base, refreshStarted)
	session := newDynamicPublisherTestSession()
	inferencer := &dynamicPublisherTestInferencer{session: session}
	runErr := make(chan error, 1)
	go func() {
		runErr <- runTestAgentLoopSession(ctx, io.Discard, inferencer, sessionLoopOptions{
			WaitForClose: true, audioService: newTestAudioIOService(),
			ToolExecutor: toolSet.Executor(), ToolDefinitions: initial,
			ToolDefinitionBase: base, RefreshToolDefinitions: refresh,
			BrowserWatch: broker.Watch, AdvertiseToolDefinitions: true,
		})
	}()
	return &dynamicPublisherCatalogSwitch{
		pageA: pageA, pageB: pageB, pageAChanged: pageAChanged,
		pageAGenerationChanged: pageAGenerationChanged, base: base,
		initialPageDefinitions: initialPageDefinitions, broker: broker,
		toolSet: toolSet, session: session, inferencer: inferencer,
		refreshStarted: refreshStarted, runErr: runErr, cancel: cancel,
	}
}

func dynamicPublisherSwitchCatalogs() (webmcp.ToolCatalogSnapshot, webmcp.ToolCatalogSnapshot, webmcp.ToolCatalogSnapshot, webmcp.ToolCatalogSnapshot) {
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
	return pageA, pageB, pageAChanged, pageAGenerationChanged
}

func dynamicPublisherStableDefinitions(toolSet *webmcpTools.BrokerToolSet) []messages.ToolDefinition {
	static := []messages.ToolDefinition{dynamicPublisherTestDefinition("static_tool", "static")}
	return append(append([]messages.ToolDefinition(nil), static...), toolSet.Definitions()...)
}

func dynamicPublisherDefinitionNames(definitions []messages.ToolDefinition) []string {
	names := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		names = append(names, definition.Name)
	}
	return names
}

func dynamicPublisherCatalogRefresh(toolSet *webmcpTools.BrokerToolSet, base []messages.ToolDefinition, started chan<- struct{}) func(context.Context) ([]messages.ToolDefinition, error) {
	return func(ctx context.Context) ([]messages.ToolDefinition, error) {
		select {
		case started <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		pageDefinitions, err := toolSet.PageToolDefinitionsWithError(ctx)
		if err != nil {
			return nil, err
		}
		return append(append([]messages.ToolDefinition(nil), base...), pageDefinitions...), nil
	}
}

func (s *dynamicPublisherCatalogSwitch) assertBootstrap(t *testing.T, ctx context.Context) {
	t.Helper()
	waitForDynamicPublisherRefresh(t, ctx, s.refreshStarted)
	bootstrap := readDynamicPublisherUpdate(t, ctx, s.session)
	assertDynamicPublisherSurface(t, bootstrap, s.base, s.initialPageDefinitions, "bootstrap A")
}

func (s *dynamicPublisherCatalogSwitch) switchToB(t *testing.T, ctx context.Context) {
	t.Helper()
	s.broker.SetCatalog(s.pageB)
	s.broker.events <- webmcp.BrokerEvent{Type: webmcp.BrokerEventSelected, Sequence: 1}
	waitForDynamicPublisherRefresh(t, ctx, s.refreshStarted)
	got := readDynamicPublisherUpdate(t, ctx, s.session)
	pageDefinitions := pageDefinitionsFromCatalog(t, s.toolSet, ctx, s.pageB)
	assertDynamicPublisherSurface(t, got, s.base, pageDefinitions, "A-to-B")
	assertDynamicPublisherDefinition(t, got, "create_document", "create document B", 2)
	s.assertStaleCallRecovery(t, ctx)
	s.assertBInvocations(t, ctx)
}

func (s *dynamicPublisherCatalogSwitch) assertStaleCallRecovery(t *testing.T, ctx context.Context) {
	t.Helper()
	invocationsBefore := s.broker.InvocationCount()
	response := executeDynamicPublisherPageCall(t, ctx, s.toolSet.Executor(), "stale-a", "cube_state", `{}`)
	if response.OK || response.Error == nil || response.Error.Code != string(webmcp.ErrorStaleToolRef) {
		t.Fatalf("stale A-only response = %#v, want stale guidance envelope", response)
	}
	if !strings.Contains(response.Error.Message, "create_document") || !strings.Contains(response.Error.Message, webmcp.ListToolsToolName) {
		t.Fatalf("stale guidance = %q, want current catalog and stable recovery path", response.Error.Message)
	}
	if got := s.broker.InvocationCount(); got != invocationsBefore {
		t.Fatalf("stale A-only invocation count = %d, want unchanged at %d", got, invocationsBefore)
	}
}

func (s *dynamicPublisherCatalogSwitch) assertBInvocations(t *testing.T, ctx context.Context) {
	t.Helper()
	created := executeDynamicPublisherPageCall(t, ctx, s.toolSet.Executor(), "create-b", "create_document", `{"title":"switch-proof","content":"catalog B"}`)
	if !created.OK {
		t.Fatalf("newly advertised B tool response = %#v, want success", created)
	}
	last := s.broker.LastInvocation()
	if last.ToolRef != pageToolRef(s.pageB, "create_document") || string(last.Input) != `{"title":"switch-proof","content":"catalog B"}` {
		t.Fatalf("B invocation = %#v, want current create_document ref and input", last)
	}
	shared := executeDynamicPublisherPageCall(t, ctx, s.toolSet.Executor(), "shared-b", "shared_action", `{"value":"B"}`)
	if !shared.OK || s.broker.LastInvocation().ToolRef != pageToolRef(s.pageB, "shared_action") {
		t.Fatalf("same-name B invocation = envelope=%#v invocation=%#v, want B ref", shared, s.broker.LastInvocation())
	}
}

func (s *dynamicPublisherCatalogSwitch) switchBackToA(t *testing.T, ctx context.Context) {
	t.Helper()
	s.broker.SetCatalog(s.pageA)
	s.broker.events <- webmcp.BrokerEvent{Type: webmcp.BrokerEventGenerationChanged, Sequence: 2}
	waitForDynamicPublisherRefresh(t, ctx, s.refreshStarted)
	got := readDynamicPublisherUpdate(t, ctx, s.session)
	pageDefinitions := pageDefinitionsFromCatalog(t, s.toolSet, ctx, s.pageA)
	assertDynamicPublisherSurface(t, got, s.base, pageDefinitions, "B-to-A")
	shared := executeDynamicPublisherPageCall(t, ctx, s.toolSet.Executor(), "shared-a", "shared_action", `{"value":"A"}`)
	if !shared.OK || s.broker.LastInvocation().ToolRef != pageToolRef(s.pageA, "shared_action") {
		t.Fatalf("same-name A invocation = envelope=%#v invocation=%#v, want A ref", shared, s.broker.LastInvocation())
	}
}

func (s *dynamicPublisherCatalogSwitch) replaceCatalog(t *testing.T, ctx context.Context) {
	t.Helper()
	s.broker.SetCatalog(s.pageAChanged)
	s.broker.events <- webmcp.BrokerEvent{Type: webmcp.BrokerEventCatalogChanged, Sequence: 3}
	waitForDynamicPublisherRefresh(t, ctx, s.refreshStarted)
	got := readDynamicPublisherUpdate(t, ctx, s.session)
	pageDefinitions := pageDefinitionsFromCatalog(t, s.toolSet, ctx, s.pageAChanged)
	assertDynamicPublisherSurface(t, got, s.base, pageDefinitions, "catalog add-remove")
	assertDynamicPublisherAbsent(t, got, "cube_state", "catalog add-remove")
	assertDynamicPublisherPresent(t, got, "read_cube_history", "catalog add-remove")
}

func (s *dynamicPublisherCatalogSwitch) replaceGeneration(t *testing.T, ctx context.Context) {
	t.Helper()
	s.broker.SetCatalog(s.pageAGenerationChanged)
	s.broker.events <- webmcp.BrokerEvent{Type: webmcp.BrokerEventGenerationChanged, Sequence: 4}
	waitForDynamicPublisherRefresh(t, ctx, s.refreshStarted)
	got := readDynamicPublisherUpdate(t, ctx, s.session)
	pageDefinitions := pageDefinitionsFromCatalog(t, s.toolSet, ctx, s.pageAGenerationChanged)
	assertDynamicPublisherSurface(t, got, s.base, pageDefinitions, "generation replacement")
	assertDynamicPublisherDefinition(t, got, "read_cube_history", "read cube history A3", 1)
}

func (s *dynamicPublisherCatalogSwitch) assertPersistentSession(t *testing.T, ctx context.Context) {
	t.Helper()
	s.broker.events <- webmcp.BrokerEvent{Type: webmcp.BrokerEventGenerationChanged, Sequence: 5}
	waitForDynamicPublisherRefresh(t, ctx, s.refreshStarted)
	select {
	case msg := <-s.session.sent:
		t.Fatalf("duplicate canonical event emitted provider message %#v", msg)
	default:
	}
	if s.broker.InvocationCount() != 3 {
		t.Fatalf("successful page invocation count = %d, want stale guidance excluded and three current calls", s.broker.InvocationCount())
	}
	if s.inferencer.connected != 1 {
		t.Fatalf("provider connections = %d, want one persistent connection", s.inferencer.connected)
	}
}

func (s *dynamicPublisherCatalogSwitch) stop(t *testing.T) {
	t.Helper()
	s.cancel()
	select {
	case <-s.broker.watchStopped:
	case <-time.After(time.Second):
		t.Fatal("publisher did not cancel its independent broker watch")
	}
	select {
	case <-s.runErr:
	case <-time.After(time.Second):
		t.Fatal("session loop did not stop after cancellation")
	}
}
