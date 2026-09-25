package agentruntime

import (
	"context"
	"encoding/json"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"

	webmcpTestkit "github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/testkit"
	webmcpTools "github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/tools"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
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

// dynamicPublisherTimerFactory drives the settle boundary from a fake clock
// and reports every arm (creation or reset), so a test can fire the boundary
// only after the publisher has consumed each queued event.
type dynamicPublisherTimerFactory struct {
	clock *webmcpTestkit.FakeClock
	armed chan struct{}
}

func (f *dynamicPublisherTimerFactory) NewTimer(duration time.Duration) sessionturn.Timer {
	timer := armSignalingTimer{Timer: f.clock.NewTimer(duration), armed: f.armed}
	timer.signal()
	return timer
}

type armSignalingTimer struct {
	webmcp.Timer
	armed chan<- struct{}
}

func (t armSignalingTimer) Reset(duration time.Duration) bool {
	active := t.Timer.Reset(duration)
	t.signal()
	return active
}

func (t armSignalingTimer) signal() { t.armed <- struct{}{} }

// waitForDynamicPublisherArms blocks until the settle timer was armed count
// times.
func waitForDynamicPublisherArms(t *testing.T, ctx context.Context, armed <-chan struct{}, count int) {
	t.Helper()
	for range count {
		select {
		case <-armed:
		case <-ctx.Done():
			t.Fatalf("timed out waiting for settle boundary arm: %v", ctx.Err())
		}
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
	want := newTurnService().MergeToolDefinitions(base, page)
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
