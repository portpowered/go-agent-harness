package services

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

type roomBrowserPublicationFixture struct {
	mu sync.Mutex

	current      []messages.ToolDefinition
	refreshCalls int
	closeCalls   int

	ready       chan struct{}
	readyOnce   sync.Once
	watch       chan webmcp.BrokerEvent
	watchOnce   sync.Once
	watchOpened chan struct{}
}

func TestRunRoomPublishesParticipantLocalBrowserDefinitions(t *testing.T) {
	ids := []string{"alpha", "beta"}
	inferencers := make(map[string]*roomTestInferencer, len(ids))
	fixtures := make(map[string]*roomBrowserPublicationFixture, len(ids))
	initialPages := make(map[string][]messages.ToolDefinition, len(ids))
	updatedPages := make(map[string][]messages.ToolDefinition, len(ids))
	for _, id := range ids {
		inferencers[id] = &roomTestInferencer{events: []messages.StreamMessage{
			roomTestSessionOpen(id),
			{Type: messages.StreamTypeSessionCreated, Value: messages.NewSessionCreatedValue(id, "room-test")},
		}}
		fixtures[id] = &roomBrowserPublicationFixture{
			ready:       make(chan struct{}),
			watch:       make(chan webmcp.BrokerEvent, 4),
			watchOpened: make(chan struct{}),
		}
		initialPages[id] = []messages.ToolDefinition{{
			Name:        "get_" + id + "_state",
			Description: "Initial " + id + " page state.",
		}}
		updatedPages[id] = []messages.ToolDefinition{{
			Name:        "get_" + id + "_state_v2",
			Description: "Refreshed " + id + " page state.",
		}}
	}

	opts, factoryCalls := newRoomTestRunOptions(ids, inferencers)
	stable := []messages.ToolDefinition{{Name: webmcp.ListTabsToolName, Description: "List tabs."}}
	for index := range opts.Manifest.Participants {
		browser := room.DefaultBrowserToolsConfig()
		browser.Connection.CDPURL = "http://127.0.0.1:9222"
		opts.Manifest.Participants[index].BrowserTools = &browser
	}
	opts.BrowserCapabilitiesFactory = func(participant room.Participant) (RoomParticipantBrowserCapabilities, error) {
		id := participant.ID
		fixture := fixtures[id]
		initial := append(append([]messages.ToolDefinition(nil), stable...), initialPages[id]...)
		fixture.mu.Lock()
		fixture.current = append([]messages.ToolDefinition(nil), initial...)
		fixture.mu.Unlock()
		return RoomParticipantBrowserCapabilities{
			Executor:           &roomBrowserCapabilityTestExecutor{participantID: id},
			Definitions:        append([]messages.ToolDefinition(nil), initial...),
			ToolDefinitionBase: append([]messages.ToolDefinition(nil), stable...),
			RefreshToolDefinitions: func(ctx context.Context) ([]messages.ToolDefinition, error) {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				fixture.mu.Lock()
				fixture.refreshCalls++
				if fixture.refreshCalls >= 2 {
					fixture.readyOnce.Do(func() { close(fixture.ready) })
				}
				current := append([]messages.ToolDefinition(nil), fixture.current...)
				fixture.mu.Unlock()
				return current, nil
			},
			BrowserWatch: func(context.Context) <-chan webmcp.BrokerEvent {
				fixture.watchOnce.Do(func() { close(fixture.watchOpened) })
				return fixture.watch
			},
			Close: func() error {
				fixture.mu.Lock()
				fixture.closeCalls++
				fixture.mu.Unlock()
				return nil
			},
		}, nil
	}

	opened := make(chan string, len(ids))
	opts.onParticipantSessionOpen = func(id string) { opened <- id }
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	outcome := make(chan roomTestRunOutcome, 1)
	go func() {
		result, err := RunRoomWithResult(ctx, io.Discard, opts)
		outcome <- roomTestRunOutcome{result: result, err: err}
	}()

	seenOpened := make(map[string]struct{}, len(ids))
	for len(seenOpened) < len(ids) {
		select {
		case id := <-opened:
			seenOpened[id] = struct{}{}
		case <-ctx.Done():
			t.Fatalf("session-open observations = %v: %v", seenOpened, ctx.Err())
		}
	}
	for _, id := range ids {
		select {
		case <-fixtures[id].watchOpened:
		case <-ctx.Done():
			t.Fatalf("participant %q browser watch did not start: %v", id, ctx.Err())
		}
		select {
		case <-fixtures[id].ready:
		case <-ctx.Done():
			t.Fatalf("participant %q browser publisher did not reach session-ready refresh: %v", id, ctx.Err())
		}
	}

	for _, id := range ids {
		options := factoryCalls[id]
		assertRoomBrowserDefinitionNames(t, options.ToolDefinitions, "webmcp_list_tabs", "get_"+id+"_state")
		assertRoomBrowserDefinitionNames(t, options.ToolDefinitionBase, "webmcp_list_tabs")
		for _, other := range ids {
			if other == id {
				continue
			}
			if roomDefinitionNamesContain(options.ToolDefinitions, "get_"+other+"_state") {
				t.Fatalf("participant %q initial definitions leaked participant %q: %v", id, other, options.ToolDefinitions)
			}
		}
	}

	sessions := make(map[string]*roomTestSession, len(ids))
	for _, id := range ids {
		participantSessions := inferencers[id].sessionsSnapshot()
		if len(participantSessions) != 1 {
			t.Fatalf("participant %q sessions = %d, want one", id, len(participantSessions))
		}
		sessions[id] = participantSessions[0]
	}

	setRoomBrowserPublicationCurrent(fixtures["alpha"], append(append([]messages.ToolDefinition(nil), stable...), updatedPages["alpha"]...))
	fixtures["alpha"].watch <- webmcp.BrokerEvent{Type: webmcp.BrokerEventCatalogChanged, Sequence: 1}
	alphaUpdate := readRoomBrowserSessionUpdate(t, ctx, sessions["alpha"])
	assertRoomBrowserDefinitionNames(t, alphaUpdate, "webmcp_list_tabs", "get_alpha_state_v2")
	if roomDefinitionNamesContain(alphaUpdate, "get_beta_state_v2") {
		t.Fatalf("alpha refresh advertised beta page tool: %v", alphaUpdate)
	}
	if got := sessions["beta"].sentCountSnapshot(); got != 0 {
		t.Fatalf("beta received %d outbound messages after alpha-only refresh, want none", got)
	}

	setRoomBrowserPublicationCurrent(fixtures["beta"], append(append([]messages.ToolDefinition(nil), stable...), updatedPages["beta"]...))
	fixtures["beta"].watch <- webmcp.BrokerEvent{Type: webmcp.BrokerEventGenerationChanged, Sequence: 1}
	betaUpdate := readRoomBrowserSessionUpdate(t, ctx, sessions["beta"])
	assertRoomBrowserDefinitionNames(t, betaUpdate, "webmcp_list_tabs", "get_beta_state_v2")
	if roomDefinitionNamesContain(betaUpdate, "get_alpha_state_v2") {
		t.Fatalf("beta refresh advertised alpha page tool: %v", betaUpdate)
	}
	if got := sessions["alpha"].sentCountSnapshot(); got != 1 {
		t.Fatalf("alpha received %d outbound messages after beta-only refresh, want one", got)
	}

	cancel()
	select {
	case got := <-outcome:
		if got.err != nil {
			t.Fatalf("room cancellation: %v", got.err)
		}
		if got.result.Reason != RoomTerminationStopped {
			t.Fatalf("room reason = %q, want %q", got.result.Reason, RoomTerminationStopped)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("room did not terminate after publication assertions")
	}
	for _, id := range ids {
		fixtures[id].mu.Lock()
		closeCalls := fixtures[id].closeCalls
		fixtures[id].mu.Unlock()
		if closeCalls != 1 {
			t.Fatalf("participant %q browser close calls = %d, want exactly one", id, closeCalls)
		}
	}
}

func setRoomBrowserPublicationCurrent(fixture *roomBrowserPublicationFixture, definitions []messages.ToolDefinition) {
	fixture.mu.Lock()
	fixture.current = append([]messages.ToolDefinition(nil), definitions...)
	fixture.mu.Unlock()
}

func readRoomBrowserSessionUpdate(t *testing.T, ctx context.Context, session *roomTestSession) []messages.ToolDefinition {
	t.Helper()
	msg, ok := session.nextSent(ctx)
	if !ok {
		t.Fatalf("timed out waiting for participant browser definition update: %v", ctx.Err())
	}
	if msg.Type != messages.StreamTypeSessionUpdate {
		t.Fatalf("provider message type = %s, want %s", msg.Type, messages.StreamTypeSessionUpdate)
	}
	value, ok := msg.Value.(*messages.SessionUpdateValue)
	if !ok || value == nil {
		t.Fatalf("provider message value = %T, want *messages.SessionUpdateValue", msg.Value)
	}
	return value.Tools
}

func assertRoomBrowserDefinitionNames(t *testing.T, definitions []messages.ToolDefinition, names ...string) {
	t.Helper()
	if len(definitions) != len(names) {
		t.Fatalf("definition names = %v, want exactly %v", roomDefinitionNames(definitions), names)
	}
	for _, name := range names {
		if !roomDefinitionNamesContain(definitions, name) {
			t.Fatalf("definition names = %v, missing %q", roomDefinitionNames(definitions), name)
		}
	}
}

func roomDefinitionNames(definitions []messages.ToolDefinition) []string {
	names := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		names = append(names, definition.Name)
	}
	return names
}

func roomDefinitionNamesContain(definitions []messages.ToolDefinition, name string) bool {
	for _, definition := range definitions {
		if strings.TrimSpace(definition.Name) == name {
			return true
		}
	}
	return false
}
