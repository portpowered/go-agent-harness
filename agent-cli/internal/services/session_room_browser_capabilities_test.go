package services

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestBuildRoomParticipantPlansComposesIndependentBrowserCapabilities(t *testing.T) {
	ids := []string{"alpha", "beta"}
	inferencers := map[string]*roomTestInferencer{
		"alpha": {},
		"beta":  {},
	}
	opts, _ := newRoomTestRunOptions(ids, inferencers)
	for index := range opts.Manifest.Participants {
		browser := room.DefaultBrowserToolsConfig()
		browser.Connection.CDPURL = "http://127.0.0.1:9222"
		browser.Selection.Browser = "browser-shared"
		browser.Selection.Tab = "tab-shared"
		opts.Manifest.Participants[index].BrowserTools = &browser
	}

	var mu sync.Mutex
	initCalls := make(map[string]int, len(ids))
	closeCalls := make(map[string]int, len(ids))
	opts.BrowserCapabilitiesFactory = func(participant room.Participant) (RoomParticipantBrowserCapabilities, error) {
		id := participant.ID
		executor := &roomBrowserCapabilityTestExecutor{participantID: id}
		definition := messages.ToolDefinition{Name: "browser_" + id, Description: "browser capability"}
		watcher := func(context.Context) <-chan webmcp.BrokerEvent {
			return make(chan webmcp.BrokerEvent)
		}
		return RoomParticipantBrowserCapabilities{
			Executor:           executor,
			Definitions:        []messages.ToolDefinition{definition},
			ToolDefinitionBase: []messages.ToolDefinition{definition},
			BrowserWatch:       watcher,
			Initialize: func(context.Context) error {
				mu.Lock()
				initCalls[id]++
				mu.Unlock()
				return nil
			},
			Close: func() error {
				mu.Lock()
				closeCalls[id]++
				mu.Unlock()
				return nil
			},
		}, nil
	}

	plans, _, err := buildRoomParticipantPlans(opts, room.ValidationOptions{LookupCredential: opts.CredentialLookup})
	if err != nil {
		t.Fatalf("buildRoomParticipantPlans: %v", err)
	}
	if len(plans) != len(ids) {
		t.Fatalf("plans = %d, want %d", len(plans), len(ids))
	}
	for index, plan := range plans {
		id := ids[index]
		if !plan.options.BrowserToolsEnabled {
			t.Fatalf("participant %q browser capability is disabled", id)
		}
		if plan.options.ToolExecutor == nil || len(plan.options.ToolDefinitions) != 1 || plan.options.ToolDefinitions[0].Name != "browser_"+id {
			t.Fatalf("participant %q browser surface = executor %T definitions %#v", id, plan.options.ToolExecutor, plan.options.ToolDefinitions)
		}
		if plan.options.BrowserWatch == nil {
			t.Fatalf("participant %q did not retain its browser watch function", id)
		}
		if plan.capabilityCoordinator == nil {
			t.Fatalf("participant %q has no capability close owner", id)
		}
		if plan.options.CapabilityClose == nil {
			t.Fatalf("participant %q options have no capability close owner", id)
		}
		if plan.options.ToolExecutor == plans[1-index].options.ToolExecutor {
			t.Fatalf("participants %q and %q share a browser executor", id, ids[1-index])
		}
	}
	mu.Lock()
	for _, id := range ids {
		if initCalls[id] != 1 {
			t.Fatalf("participant %q initialize calls = %d, want one", id, initCalls[id])
		}
	}
	mu.Unlock()

	if err := closeRoomParticipantPlanCapabilities(plans); err != nil {
		t.Fatalf("close participant capabilities: %v", err)
	}
	if err := closeRoomParticipantPlanCapabilities(plans); err != nil {
		t.Fatalf("repeat close participant capabilities: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, id := range ids {
		if closeCalls[id] != 1 {
			t.Fatalf("participant %q close calls = %d, want exactly one", id, closeCalls[id])
		}
	}
}

func TestBuildRoomParticipantPlansClosesBrowserCapabilitiesWhenLaterConstructionFails(t *testing.T) {
	ids := []string{"alpha", "beta"}
	inferencers := map[string]*roomTestInferencer{
		"alpha": {},
		"beta":  {},
	}
	opts, _ := newRoomTestRunOptions(ids, inferencers)
	for index := range opts.Manifest.Participants {
		browser := room.DefaultBrowserToolsConfig()
		browser.Connection.CDPURL = "http://127.0.0.1:9222"
		opts.Manifest.Participants[index].BrowserTools = &browser
	}
	closeCalls := make(map[string]int, len(ids))
	opts.BrowserCapabilitiesFactory = func(participant room.Participant) (RoomParticipantBrowserCapabilities, error) {
		id := participant.ID
		return RoomParticipantBrowserCapabilities{
			Executor:    &roomBrowserCapabilityTestExecutor{participantID: id},
			Definitions: []messages.ToolDefinition{{Name: "browser_" + id}},
			Initialize:  func(context.Context) error { return nil },
			Close: func() error {
				closeCalls[id]++
				return nil
			},
		}, nil
	}
	opts.SessionFactory = func(participant room.Participant, _ SessionRunOptions) (messages.SessionInferencer, error) {
		if participant.ID == "beta" {
			return nil, errors.New("provider construction failed")
		}
		return inferencers[participant.ID], nil
	}

	_, _, err := buildRoomParticipantPlans(opts, room.ValidationOptions{LookupCredential: opts.CredentialLookup})
	if err == nil || !strings.Contains(err.Error(), "beta") || !strings.Contains(err.Error(), "provider construction failed") {
		t.Fatalf("build error = %v, want participant-qualified provider failure", err)
	}
	for _, id := range ids {
		if closeCalls[id] != 1 {
			t.Fatalf("participant %q close calls = %d, want exactly one after failed planning", id, closeCalls[id])
		}
	}
}

func TestBuildRoomParticipantPlansDoesNotConstructOmittedBrowserCapabilities(t *testing.T) {
	opts, _ := newRoomTestRunOptions([]string{"browser", "plain"}, map[string]*roomTestInferencer{
		"browser": {},
		"plain":   {},
	})
	browser := room.DefaultBrowserToolsConfig()
	browser.Connection.CDPURL = "http://127.0.0.1:9222"
	opts.Manifest.Participants[0].BrowserTools = &browser
	calls := 0
	opts.BrowserCapabilitiesFactory = func(participant room.Participant) (RoomParticipantBrowserCapabilities, error) {
		calls++
		return RoomParticipantBrowserCapabilities{
			Executor:    &roomBrowserCapabilityTestExecutor{participantID: participant.ID},
			Definitions: []messages.ToolDefinition{{Name: "browser_tool"}},
		}, nil
	}

	plans, _, err := buildRoomParticipantPlans(opts, room.ValidationOptions{LookupCredential: opts.CredentialLookup})
	if err != nil {
		t.Fatalf("buildRoomParticipantPlans: %v", err)
	}
	if calls != 1 {
		t.Fatalf("browser capability factory calls = %d, want one", calls)
	}
	if plans[1].options.BrowserToolsEnabled || plans[1].options.BrowserWatch != nil || plans[1].capabilityCoordinator != nil {
		t.Fatalf("omitted browserTools created browser state: %+v", plans[1].options)
	}
	if err := closeRoomParticipantPlanCapabilities(plans); err != nil {
		t.Fatalf("close participant capabilities: %v", err)
	}
}

type roomBrowserCapabilityTestExecutor struct {
	participantID string
}

func (e *roomBrowserCapabilityTestExecutor) Execute(_ context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	if e == nil {
		return messages.ToolCallResponse{}, fmt.Errorf("nil browser executor")
	}
	return messages.ToolCallResponse{ToolCallID: call.ID, Name: call.Name, Content: e.participantID}, nil
}
