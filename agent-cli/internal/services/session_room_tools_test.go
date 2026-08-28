package services

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/inference"
)

func TestNewDefaultRoomParticipantToolCapabilitiesFactorySelectsOnlyGrantedTools(t *testing.T) {
	factory, err := newDefaultRoomParticipantToolCapabilitiesFactory(t.TempDir())
	if err != nil {
		t.Fatalf("newDefaultRoomParticipantToolCapabilitiesFactory: %v", err)
	}

	capabilities, err := factory(room.Participant{ID: "alpha", Tools: []string{"exec"}})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if capabilities.Executor == nil {
		t.Fatal("factory returned a nil executor")
	}
	if len(capabilities.Definitions) != 1 || capabilities.Definitions[0].Name != "exec" {
		t.Fatalf("definitions = %#v, want exactly exec", capabilities.Definitions)
	}

	response, err := capabilities.Executor.Execute(context.Background(), messages.ToolCall{
		ID:        "exec-call",
		Name:      "exec",
		Arguments: `{"command":"printf room-tool-selection"}`,
	})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if response.Content != "room-tool-selection" {
		t.Fatalf("exec output = %q, want room-tool-selection", response.Content)
	}

	_, err = capabilities.Executor.Execute(context.Background(), messages.ToolCall{
		ID:   "ungranted-call",
		Name: "read_file",
	})
	if !errors.Is(err, tools.ErrToolNotFound) {
		t.Fatalf("ungranted read_file error = %v, want tools.ErrToolNotFound", err)
	}
}

func TestNewEmptyToolRegistryStartsEmptyAndAcceptsFirstTool(t *testing.T) {
	registry := tools.NewEmptyToolRegistry()
	if got := registry.Count(); got != 0 {
		t.Fatalf("empty registry count = %d, want 0", got)
	}
	if err := registry.Register(tools.NewExecTool("", false)); err != nil {
		t.Fatalf("register first tool: %v", err)
	}
	if got := registry.Count(); got != 1 {
		t.Fatalf("registry count after first registration = %d, want 1", got)
	}
}

func TestBuildRoomParticipantPlans_UsesIsolatedRequestedCapabilities(t *testing.T) {
	ids := []string{"alpha", "beta"}
	inferencers := map[string]*roomTestInferencer{
		"alpha": {},
		"beta":  {},
	}
	opts, _ := newRoomTestRunOptions(ids, inferencers)
	opts.Manifest.Participants[0].Tools = []string{"alpha_tool"}
	opts.Manifest.Participants[1].Tools = []string{"beta_tool"}
	opts.Manifest.Participants[0].Voice = "alloy"
	opts.Manifest.Participants[1].Voice = "ash"
	var capabilityCalls []string
	opts.ToolCapabilitiesFactory = func(participant room.Participant) (RoomParticipantToolCapabilities, error) {
		capabilityCalls = append(capabilityCalls, participant.ID)
		return RoomParticipantToolCapabilities{
			Executor: roomScopedToolExecutor{participantID: participant.ID},
			Definitions: []messages.ToolDefinition{{
				Name:        participant.ID + "_tool",
				Description: "participant-scoped test tool",
			}},
		}, nil
	}

	plans, _, err := buildRoomParticipantPlans(opts, room.ValidationOptions{LookupCredential: opts.CredentialLookup})
	if err != nil {
		t.Fatalf("buildRoomParticipantPlans: %v", err)
	}
	if got := strings.Join(capabilityCalls, ","); got != "alpha,beta" {
		t.Fatalf("capability factory calls = %q, want alpha,beta", got)
	}
	if len(plans) != len(ids) {
		t.Fatalf("plans = %d, want %d", len(plans), len(ids))
	}

	for index, plan := range plans {
		participantID := ids[index]
		if plan.options.ToolExecutor == nil {
			t.Fatalf("participant %q has nil tool executor", participantID)
		}
		if len(plan.options.ToolDefinitions) != 1 || plan.options.ToolDefinitions[0].Name != participantID+"_tool" {
			t.Fatalf("participant %q definitions = %#v, want only its requested tool", participantID, plan.options.ToolDefinitions)
		}
		wantVoice := map[string]string{"alpha": "alloy", "beta": "ash"}[participantID]
		if plan.options.Voice != wantVoice {
			t.Fatalf("participant %q voice = %q, want %q", participantID, plan.options.Voice, wantVoice)
		}
		response, executeErr := plan.options.ToolExecutor.Execute(context.Background(), messages.ToolCall{
			ID:   "call-" + participantID,
			Name: participantID + "_tool",
		})
		if executeErr != nil {
			t.Fatalf("participant %q tool execution: %v", participantID, executeErr)
		}
		if response.Content != participantID {
			t.Fatalf("participant %q tool result = %q, want isolated result", participantID, response.Content)
		}
		otherID := ids[1-index]
		if _, executeErr := plan.options.ToolExecutor.Execute(context.Background(), messages.ToolCall{Name: otherID + "_tool"}); executeErr == nil {
			t.Fatalf("participant %q executor accepted %q", participantID, otherID+"_tool")
		}
		if plan.options.ToolExecutor == plans[1-index].options.ToolExecutor {
			t.Fatalf("participants %q and %q share a tool executor", participantID, otherID)
		}
	}
}

func TestBuildRoomParticipantPlans_EmptyToolsDoNotConstructCapabilities(t *testing.T) {
	opts, _ := newRoomTestRunOptions([]string{"alpha", "beta"}, map[string]*roomTestInferencer{
		"alpha": {},
		"beta":  {},
	})
	opts.ToolCapabilitiesFactory = func(room.Participant) (RoomParticipantToolCapabilities, error) {
		t.Fatal("tool capability factory called for an explicit empty tools list")
		return RoomParticipantToolCapabilities{}, nil
	}

	plans, _, err := buildRoomParticipantPlans(opts, room.ValidationOptions{LookupCredential: opts.CredentialLookup})
	if err != nil {
		t.Fatalf("buildRoomParticipantPlans: %v", err)
	}
	for _, plan := range plans {
		if plan.options.ToolExecutor != nil || len(plan.options.ToolDefinitions) != 0 {
			t.Fatalf("participant %q has tools despite tools: []: executor=%T definitions=%#v", plan.manifest.ID, plan.options.ToolExecutor, plan.options.ToolDefinitions)
		}
	}
}

func TestBuildRoomParticipantPlans_IsolatesConcurrentCapabilityUse(t *testing.T) {
	ids := []string{"alpha", "beta"}
	opts, _ := newRoomTestRunOptions(ids, map[string]*roomTestInferencer{
		"alpha": {},
		"beta":  {},
	})
	for index, participant := range opts.Manifest.Participants {
		participant.Tools = []string{ids[index] + "_tool"}
		participant.Voice = []string{"alloy", "ash"}[index]
		opts.Manifest.Participants[index] = participant
	}
	opts.ToolCapabilitiesFactory = func(participant room.Participant) (RoomParticipantToolCapabilities, error) {
		return RoomParticipantToolCapabilities{
			Executor: roomScopedToolExecutor{participantID: participant.ID},
			Definitions: []messages.ToolDefinition{{
				Name: participant.ID + "_tool",
			}},
		}, nil
	}

	plans, _, err := buildRoomParticipantPlans(opts, room.ValidationOptions{LookupCredential: opts.CredentialLookup})
	if err != nil {
		t.Fatalf("buildRoomParticipantPlans: %v", err)
	}

	var waitGroup sync.WaitGroup
	errs := make(chan error, len(plans))
	for index, plan := range plans {
		waitGroup.Add(1)
		go func(index int, plan *roomParticipantPlan) {
			defer waitGroup.Done()
			wantID := ids[index]
			wantVoice := []string{"alloy", "ash"}[index]
			for callIndex := 0; callIndex < 64; callIndex++ {
				if plan.options.Voice != wantVoice {
					errs <- fmt.Errorf("participant %q observed voice %q, want %q", wantID, plan.options.Voice, wantVoice)
					return
				}
				response, executeErr := plan.options.ToolExecutor.Execute(context.Background(), messages.ToolCall{
					ID:   fmt.Sprintf("call-%d", callIndex),
					Name: wantID + "_tool",
				})
				if executeErr != nil {
					errs <- fmt.Errorf("participant %q call %d: %w", wantID, callIndex, executeErr)
					return
				}
				if response.Content != wantID {
					errs <- fmt.Errorf("participant %q observed result %q", wantID, response.Content)
					return
				}
			}
		}(index, plan)
	}
	waitGroup.Wait()
	close(errs)
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
}

func TestBuildRoomParticipantPlans_RejectsCapabilityMismatchBeforeSessionConstruction(t *testing.T) {
	opts, factoryCalls := newRoomTestRunOptions([]string{"alpha", "beta"}, map[string]*roomTestInferencer{
		"alpha": {},
		"beta":  {},
	})
	opts.Manifest.Participants[0].Tools = []string{"alpha_tool"}
	opts.ToolCapabilitiesFactory = func(participant room.Participant) (RoomParticipantToolCapabilities, error) {
		return RoomParticipantToolCapabilities{
			Executor:    roomScopedToolExecutor{participantID: participant.ID},
			Definitions: []messages.ToolDefinition{{Name: "unexpected_tool"}},
		}, nil
	}

	_, _, err := buildRoomParticipantPlans(opts, room.ValidationOptions{LookupCredential: opts.CredentialLookup})
	if err == nil {
		t.Fatal("capability mismatch returned nil error")
	}
	if !errors.Is(err, ErrRoomParticipantToolMismatch) {
		t.Fatalf("error = %v, want ErrRoomParticipantToolMismatch", err)
	}
	if !strings.Contains(err.Error(), `room participant "alpha"`) || !strings.Contains(err.Error(), "unexpected_tool") {
		t.Fatalf("error = %v, want participant and unexpected tool", err)
	}
	if len(factoryCalls) != 0 {
		t.Fatalf("session factory was called %d times after capability mismatch", len(factoryCalls))
	}
}

func TestBuildRoomParticipantPlans_OmittedVoicePreservesProviderDefault(t *testing.T) {
	opts, factoryCalls := newRoomTestRunOptions([]string{"alpha", "beta"}, map[string]*roomTestInferencer{
		"alpha": {},
		"beta":  {},
	})

	plans, _, err := buildRoomParticipantPlans(opts, room.ValidationOptions{LookupCredential: opts.CredentialLookup})
	if err != nil {
		t.Fatalf("buildRoomParticipantPlans: %v", err)
	}
	if len(factoryCalls) != 2 {
		t.Fatalf("session factory calls = %d, want one per participant", len(factoryCalls))
	}
	for _, plan := range plans {
		if plan.options.Voice != "" {
			t.Fatalf("participant %q voice = %q, want provider default", plan.manifest.ID, plan.options.Voice)
		}
	}
}

func TestBuildRoomParticipantPlans_PropagatesOpeningPromptToSession(t *testing.T) {
	opts, factoryCalls := newRoomTestRunOptions([]string{"alpha", "beta"}, map[string]*roomTestInferencer{
		"alpha": {},
		"beta":  {},
	})
	opts.Manifest.Participants[0].OpeningPrompt = "Start the bounded room conversation."

	plans, _, err := buildRoomParticipantPlans(opts, room.ValidationOptions{LookupCredential: opts.CredentialLookup})
	if err != nil {
		t.Fatalf("buildRoomParticipantPlans: %v", err)
	}
	if got := factoryCalls["alpha"].Prompt; got != "Start the bounded room conversation." {
		t.Fatalf("alpha session prompt = %q, want opening prompt", got)
	}
	if got := factoryCalls["beta"].Prompt; got != "" {
		t.Fatalf("beta session prompt = %q, want no implicit prompt", got)
	}
	if plans[0].options.Prompt != factoryCalls["alpha"].Prompt || plans[1].options.Prompt != factoryCalls["beta"].Prompt {
		t.Fatalf("plan prompt values diverged from factory options: plans=%q/%q factory=%q/%q", plans[0].options.Prompt, plans[1].options.Prompt, factoryCalls["alpha"].Prompt, factoryCalls["beta"].Prompt)
	}
}

func TestNewLiveSessionInferencerCarriesToolDefinitionsToProviderRequest(t *testing.T) {
	definition := messages.ToolDefinition{
		Name:        "participant_tool",
		Description: "a participant-only tool",
		Parameters:  []messages.ToolParameter{{Name: "input", Type: "string", Required: true}},
	}
	for _, provider := range []string{"openai", "grok"} {
		t.Run(provider, func(t *testing.T) {
			inferencer, _, err := NewLiveSessionInferencer(SessionRunOptions{
				Provider:        provider,
				Model:           map[string]string{"openai": openAIRealtimeDefaultModel, "grok": "grok-session-model"}[provider],
				APIKey:          "room-test-key",
				BaseURL:         "ws://room.test/realtime",
				ConfigDir:       t.TempDir(),
				ToolDefinitions: []messages.ToolDefinition{definition},
			}, "participant instructions")
			if err != nil {
				t.Fatalf("NewLiveSessionInferencer: %v", err)
			}
			requested, ok := inferencer.(interface {
				Request() inference.SessionRequest
			})
			if !ok {
				t.Fatalf("inferencer type %T does not expose its session request", inferencer)
			}
			tools := requested.Request().Config.Tools
			if len(tools) != 1 || tools[0].Name != definition.Name || tools[0].Description != definition.Description {
				t.Fatalf("provider tools = %#v, want participant definition", tools)
			}
			if len(tools[0].Parameters) != 1 || tools[0].Parameters[0].Name != "input" {
				t.Fatalf("provider tool parameters = %#v, want copied parameters", tools[0].Parameters)
			}
		})
	}
}

type roomScopedToolExecutor struct {
	participantID string
}

func (e roomScopedToolExecutor) Execute(_ context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	wantName := e.participantID + "_tool"
	if call.Name != wantName {
		return messages.ToolCallResponse{}, fmt.Errorf("tool %q is not available to participant %q", call.Name, e.participantID)
	}
	return messages.ToolCallResponse{ToolCallID: call.ID, Content: e.participantID}, nil
}
