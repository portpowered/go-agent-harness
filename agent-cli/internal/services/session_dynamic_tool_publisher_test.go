package services

import (
	"context"
	"errors"
	"io"
	"reflect"
	"sync"
	"testing"
	"time"

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
