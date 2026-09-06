package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeTools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/inference"
)

func TestBuildRoomParticipantPlans_LoadedManifestWiresExactParticipantToolContracts(t *testing.T) {
	lookupCredential := func(name string) (string, bool) {
		if name == "ROOM_OPENAI_KEY" {
			return "room-test-key", true
		}
		return "", false
	}
	manifestPath := filepath.Join(t.TempDir(), "room.json")
	manifestData := []byte(fmt.Sprintf(`{
  "schema_version": 1,
  "room": {"max_turns": 2},
  "participants": [
    {
      "id": "customer",
      "system_prompt": "Ask the assistant to perform the room proof.",
      "opening_prompt": "Ask the assistant to perform the room proof.",
      "provider": "openai",
      "model": %q,
      "api_key_env": "ROOM_OPENAI_KEY",
      "voice": "marin",
      "tools": []
    },
    {
      "id": "assistant",
      "system_prompt": "Use exec for the requested room proof, then confirm it aloud.",
      "provider": "openai",
      "model": %q,
      "api_key_env": "ROOM_OPENAI_KEY",
      "voice": "cedar",
      "tools": ["exec"]
    }
  ]
}`, openAIRealtimeDefaultModel, openAIRealtimeDefaultModel))
	if err := os.WriteFile(manifestPath, manifestData, 0o600); err != nil {
		t.Fatalf("write room manifest: %v", err)
	}
	manifest, err := room.ReadManifest(manifestPath, room.ValidationOptions{LookupCredential: lookupCredential})
	if err != nil {
		t.Fatalf("read room manifest: %v", err)
	}

	requests := make(map[string]inference.SessionRequest, len(manifest.Participants))
	configDir := t.TempDir()
	opts := RoomRunOptions{
		Manifest: manifest, ModelCatalog: testModelCatalog(),
		CredentialLookup: lookupCredential,
		ConfigDir:        configDir,
		BaseURL:          "ws://room.test/realtime",
		SessionFactory: func(participant room.Participant, options SessionRunOptions) (messages.SessionInferencer, error) {
			inferencer, _, factoryErr := NewLiveSessionInferencer(options, participant.SystemPrompt)
			if factoryErr != nil {
				return nil, factoryErr
			}
			requester, ok := inferencer.(interface {
				Request() inference.SessionRequest
			})
			if !ok {
				return nil, fmt.Errorf("participant %q inferencer %T does not expose its request", participant.ID, inferencer)
			}
			requests[participant.ID] = requester.Request()
			return inferencer, nil
		},
	}

	plans, _, err := buildRoomParticipantPlans(opts, room.ValidationOptions{LookupCredential: lookupCredential})
	if err != nil {
		t.Fatalf("buildRoomParticipantPlans: %v", err)
	}
	if len(plans) != 2 || len(requests) != 2 {
		t.Fatalf("plans/requests = %d/%d, want two independently composed participants", len(plans), len(requests))
	}

	customer := plans[0]
	if customer.manifest.ID != "customer" {
		t.Fatalf("first participant = %q, want customer", customer.manifest.ID)
	}
	if customer.options.ToolExecutor != nil || len(customer.options.ToolDefinitions) != 0 {
		t.Fatalf("customer capabilities = executor %T definitions %#v, want no usable executor or definitions", customer.options.ToolExecutor, customer.options.ToolDefinitions)
	}
	customerRequest := requests["customer"]
	if customerRequest.Config.Voice != "marin" || len(customerRequest.Config.Tools) != 0 {
		t.Fatalf("customer provider request = voice %q tools %#v, want marin and zero tools", customerRequest.Config.Voice, customerRequest.Config.Tools)
	}

	assistant := plans[1]
	if assistant.manifest.ID != "assistant" {
		t.Fatalf("second participant = %q, want assistant", assistant.manifest.ID)
	}
	if assistant.options.ToolExecutor == nil {
		t.Fatal("assistant has no participant-local executor")
	}
	if len(assistant.options.ToolDefinitions) != 1 {
		t.Fatalf("assistant definitions = %#v, want exactly one definition", assistant.options.ToolDefinitions)
	}
	assertCompleteExecDefinition(t, assistant.options.ToolDefinitions[0])
	assistantRequest := requests["assistant"]
	if assistantRequest.Config.Voice != "cedar" || len(assistantRequest.Config.Tools) != 1 {
		t.Fatalf("assistant provider request = voice %q tools %#v, want cedar and exactly one tool", assistantRequest.Config.Voice, assistantRequest.Config.Tools)
	}
	assertCompleteExecDefinition(t, assistantRequest.Config.Tools[0])

	response, executeErr := assistant.options.ToolExecutor.Execute(context.Background(), messages.ToolCall{
		ID:        "room-proof-call",
		Name:      "exec",
		Arguments: `{"command":"printf ROOMPROOF"}`,
	})
	if executeErr != nil {
		t.Fatalf("assistant exec call: %v", executeErr)
	}
	if response.ToolCallID != "room-proof-call" || response.Content != "ROOMPROOF" {
		t.Fatalf("assistant exec response = %#v, want correlated ROOMPROOF result", response)
	}
	if _, executeErr := assistant.options.ToolExecutor.Execute(context.Background(), messages.ToolCall{
		ID:   "ungranted-call",
		Name: "read_file",
	}); !errors.Is(executeErr, runtimeTools.ErrToolNotFound) {
		t.Fatalf("assistant ungranted tool error = %v, want runtime tools.ErrToolNotFound", executeErr)
	}

	if assistantRequest.Config.Tools[0].Name != assistant.options.ToolDefinitions[0].Name {
		t.Fatalf("assistant provider tool name = %q, plan tool name = %q", assistantRequest.Config.Tools[0].Name, assistant.options.ToolDefinitions[0].Name)
	}
	assistantRequest.Config.Tools[0].Parameters[0].Name = "mutated-request-copy"
	if assistant.options.ToolDefinitions[0].Parameters[0].Name == "mutated-request-copy" {
		t.Fatal("assistant provider request shares mutable parameter state with plan definitions")
	}
}

func TestBuildRoomParticipantPlans_DefaultRegistryUsesFilesystemPolicy(t *testing.T) {
	workdir := t.TempDir()
	additional := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(additional, "existing.txt"), []byte("additional"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "sentinel.txt"), []byte("SENTINEL-CONTENT"), 0o600); err != nil {
		t.Fatal(err)
	}

	opts, _ := newRoomTestRunOptions([]string{"assistant"}, map[string]*roomTestInferencer{
		"assistant": {},
	})
	opts.ConfigDir = t.TempDir()
	opts.WorkDir = workdir
	opts.AllowPaths = []string{additional, additional}
	opts.Manifest.Participants[0].Tools = []string{"read_file", "write_file", "edit_file", "append_file", "list_dir"}

	plans, _, err := buildRoomParticipantPlans(opts, room.ValidationOptions{LookupCredential: opts.CredentialLookup})
	if err != nil {
		t.Fatalf("buildRoomParticipantPlans: %v", err)
	}
	if len(plans) != 1 || plans[0].options.ToolExecutor == nil {
		t.Fatalf("plans = %#v, want one scoped participant executor", plans)
	}
	policy := plans[0].options.FilesystemPolicy
	canonicalWorkdir, err := filepath.EvalSymlinks(workdir)
	if err != nil {
		t.Fatalf("canonicalize workdir: %v", err)
	}
	canonicalAdditional, err := filepath.EvalSymlinks(additional)
	if err != nil {
		t.Fatalf("canonicalize additional root: %v", err)
	}
	if policy == nil || policy.PrimaryRoot() != canonicalWorkdir || len(policy.AdditionalRoots()) != 1 || policy.AdditionalRoots()[0] != canonicalAdditional {
		t.Fatalf("participant filesystem policy = %#v, want workdir %q and one additional root %q", policy, canonicalWorkdir, canonicalAdditional)
	}

	execute := func(name, arguments string) messages.ToolCallResponse {
		t.Helper()
		response, executeErr := plans[0].options.ToolExecutor.Execute(context.Background(), messages.ToolCall{
			ID:        "room-" + name,
			Name:      name,
			Arguments: arguments,
		})
		if executeErr != nil {
			t.Fatalf("room tool %q: %v", name, executeErr)
		}
		return response
	}

	workTarget := filepath.Join(workdir, "nested", "work.txt")
	response := execute("write_file", fmt.Sprintf(`{"path":%q,"content":"work"}`, workTarget))
	if !strings.Contains(response.Content, "File written") {
		t.Fatalf("workdir write response = %q, want success", response.Content)
	}
	additionalTarget := filepath.Join(additional, "existing.txt")
	response = execute("read_file", fmt.Sprintf(`{"path":%q}`, additionalTarget))
	if response.Content != "additional" {
		t.Fatalf("additional-root read response = %q, want content", response.Content)
	}
	response = execute("list_dir", fmt.Sprintf(`{"path":%q}`, additional))
	if !strings.Contains(response.Content, "FILE: existing.txt") {
		t.Fatalf("additional-root list response = %q, want entry", response.Content)
	}
	response = execute("edit_file", fmt.Sprintf(`{"path":%q,"old_text":"additional","new_text":"edited"}`, additionalTarget))
	if !strings.Contains(response.Content, "File edited") {
		t.Fatalf("additional-root edit response = %q, want success", response.Content)
	}
	response = execute("append_file", fmt.Sprintf(`{"path":%q,"content":"-appended"}`, additionalTarget))
	if !strings.Contains(response.Content, "Appended") {
		t.Fatalf("additional-root append response = %q, want success", response.Content)
	}
	if got, err := os.ReadFile(additionalTarget); err != nil || string(got) != "edited-appended" {
		t.Fatalf("additional-root mutation = %q, %v", got, err)
	}

	deniedParent := filepath.Join(outside, "not-created", "nested")
	deniedTarget := filepath.Join(deniedParent, "denied.txt")
	response = execute("write_file", fmt.Sprintf(`{"path":%q,"content":"must-not-write"}`, deniedTarget))
	if !strings.Contains(response.Content, "path escapes workspace") || strings.Contains(response.Content, "must-not-write") {
		t.Fatalf("outside write response = %q, want confinement denial without requested content", response.Content)
	}
	response = execute("read_file", fmt.Sprintf(`{"path":%q}`, filepath.Join(outside, "sentinel.txt")))
	if !strings.Contains(response.Content, "path escapes workspace") || strings.Contains(response.Content, "SENTINEL-CONTENT") {
		t.Fatalf("outside read response = %q, want confinement denial without sentinel", response.Content)
	}
	if _, err := os.Stat(deniedParent); !os.IsNotExist(err) {
		t.Fatalf("outside write parent = %v, want absent", err)
	}
	if got, err := os.ReadFile(filepath.Join(outside, "sentinel.txt")); err != nil || string(got) != "SENTINEL-CONTENT" {
		t.Fatalf("outside sentinel = %q, %v; want unchanged", got, err)
	}
}

func assertCompleteExecDefinition(t *testing.T, definition messages.ToolDefinition) {
	t.Helper()
	if definition.Name != "exec" {
		t.Fatalf("tool name = %q, want exec", definition.Name)
	}
	if definition.Description != "Execute a shell command on the local machine and return its output. Use with caution. Only for real shell work: never for browser-page actions, which have their own directly callable page tools." {
		t.Fatalf("exec description = %q, want complete exec description", definition.Description)
	}
	if len(definition.Parameters) != 2 {
		t.Fatalf("exec parameters = %#v, want command and working_dir", definition.Parameters)
	}
	parameters := make(map[string]messages.ToolParameter, len(definition.Parameters))
	for _, parameter := range definition.Parameters {
		if _, exists := parameters[parameter.Name]; exists {
			t.Fatalf("exec definition repeats parameter %q", parameter.Name)
		}
		parameters[parameter.Name] = parameter
	}
	want := map[string]messages.ToolParameter{
		"command": {
			Name:        "command",
			Type:        "string",
			Description: "The shell command to execute",
			Required:    true,
		},
		"working_dir": {
			Name:        "working_dir",
			Type:        "string",
			Description: "Optional working directory for the command",
		},
	}
	if len(parameters) != len(want) {
		t.Fatalf("exec parameters = %#v, want exactly %#v", parameters, want)
	}
	for name, wantParameter := range want {
		if got, ok := parameters[name]; !ok || got != wantParameter {
			t.Fatalf("exec parameter %q = %#v, want %#v", name, got, wantParameter)
		}
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
			inferencer, _, err := NewLiveSessionInferencer(SessionRunOptions{ModelCatalog: testModelCatalog(),
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
			instructions := requested.Request().Config.Instructions
			if !strings.HasPrefix(instructions, "participant instructions\n\n") {
				t.Fatalf("provider instructions = %q, want participant instructions first", instructions)
			}
			if strings.Count(instructions, "Tool-grounding requirements:") != 1 {
				t.Fatalf("grounding policy heading count = %d, want 1; instructions=%q", strings.Count(instructions, "Tool-grounding requirements:"), instructions)
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
