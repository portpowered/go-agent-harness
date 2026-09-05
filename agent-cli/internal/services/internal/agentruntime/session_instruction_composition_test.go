package agentruntime

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/inference"
)

func TestLivePlannerFamiliesUseOneGroundingComposition(t *testing.T) {
	toolDefinitions := []messages.ToolDefinition{
		{Name: "read_file", Description: "Read a UTF-8 file."},
		{Name: "exec", Description: "Execute a command."},
	}

	tests := []struct {
		name  string
		build func(t *testing.T, opts SessionRunOptions) (sessionRuntimePlan, func(), error)
	}{
		{
			name: "ordinary text",
			build: func(_ *testing.T, opts SessionRunOptions) (sessionRuntimePlan, func(), error) {
				plan, err := planSessionWithResolvedInstructions(opts, "customer instructions")
				return plan, func() {}, err
			},
		},
		{
			name: "recording directory",
			build: func(_ *testing.T, opts SessionRunOptions) (sessionRuntimePlan, func(), error) {
				return planSessionForDirectoryRecordingWithInstructions(opts, "customer instructions", true)
			},
		},
		{
			name: "scheduled audio",
			build: func(_ *testing.T, opts SessionRunOptions) (sessionRuntimePlan, func(), error) {
				opts.AudioInputs = []ScheduledAudioInput{{PCM: []byte{1, 2, 3}}}
				plan, err := planSessionWithResolvedInstructions(opts, "customer instructions")
				return plan, func() {}, err
			},
		},
		{
			name: "image-composed",
			build: func(_ *testing.T, opts SessionRunOptions) (sessionRuntimePlan, func(), error) {
				plan, _, err := planSessionImageRuntime(opts, []messages.ImagePart{{
					Bytes:     []byte{0x89, 'P', 'N', 'G'},
					MediaType: "image/png",
				}}, SessionTextSeed{}, "customer instructions", false)
				return plan, func() {}, err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configDir := t.TempDir()
			writeSessionConfigFile(t, configDir, "model:\n  provider: openai\n")
			opts := SessionRunOptions{
				RecordPath:      filepath.Join(t.TempDir(), "session.json"),
				Provider:        config.ProviderOpenAI,
				Model:           openAIRealtimeDefaultModel,
				APIKey:          "test-key",
				ConfigDir:       configDir,
				ToolExecutor:    &messages.DefaultToolExecutor{},
				ToolDefinitions: append([]messages.ToolDefinition(nil), toolDefinitions...),
			}

			plan, cleanup, err := test.build(t, opts)
			if err != nil {
				t.Fatalf("build planner: %v", err)
			}
			defer cleanup()

			request := sessionRequestFromPlanner(t, plan.inferencer)
			instructions := request.Config.Instructions
			if !strings.HasPrefix(instructions, "customer instructions\n\n") {
				t.Fatalf("provider instructions = %q, want customer instructions first", instructions)
			}
			if strings.Count(instructions, "Tool-grounding requirements:") != 1 {
				t.Fatalf("grounding policy heading count = %d, want 1; instructions=%q", strings.Count(instructions, "Tool-grounding requirements:"), instructions)
			}
			if len(request.Config.Tools) != len(toolDefinitions) {
				t.Fatalf("provider tools = %#v, want %#v", request.Config.Tools, toolDefinitions)
			}
			wantToolNames := []string{"exec", "read_file"}
			for index, wantName := range wantToolNames {
				if request.Config.Tools[index].Name != wantName {
					t.Fatalf("provider tool %d = %#v, want %q", index, request.Config.Tools[index], wantName)
				}
			}
		})
	}
}

func TestIndependentSessionCompositionsProduceIdenticalInstructionsAndProviderUpdate(t *testing.T) {
	toolDefinitions := [][]messages.ToolDefinition{
		{
			{Name: "read_file", Description: "Read a UTF-8 file.", Parameters: []messages.ToolParameter{{Name: "path", Type: "string", Required: true}}},
			{Name: "exec", Description: "Execute a command.", Parameters: []messages.ToolParameter{{Name: "command", Type: "string", Required: true}}},
		},
		{
			{Name: "exec", Description: "Execute a command.", Parameters: []messages.ToolParameter{{Name: "command", Type: "string", Required: true}}},
			{Name: "read_file", Description: "Read a UTF-8 file.", Parameters: []messages.ToolParameter{{Name: "path", Type: "string", Required: true}}},
		},
	}
	labels := []string{"registration order one", "registration order two"}
	var wantInstructions []byte
	var wantProviderUpdate []byte

	for index, definitions := range toolDefinitions {
		t.Run(labels[index], func(t *testing.T) {
			configDir := t.TempDir()
			writeSessionConfigFile(t, configDir, "model:\n  provider: openai\n")
			conn := &replayHandshakeRecordingConn{}
			opts := SessionRunOptions{
				RecordPath:      filepath.Join(t.TempDir(), "session.json"),
				Provider:        config.ProviderOpenAI,
				Model:           openAIRealtimeDefaultModel,
				APIKey:          "test-key",
				ConfigDir:       configDir,
				ToolExecutor:    &messages.DefaultToolExecutor{},
				ToolDefinitions: definitions,
				WebSocketDialer: &replayHandshakeRecordingDialer{conn: conn},
			}

			plan, err := planSessionWithResolvedInstructions(opts, "customer instructions")
			if err != nil {
				t.Fatalf("build planner: %v", err)
			}

			request := sessionRequestFromPlanner(t, plan.inferencer)
			instructions := []byte(request.Config.Instructions)
			session, err := plan.inferencer.ConnectSession(context.Background())
			if err != nil {
				t.Fatalf("connect composed provider session: %v", err)
			}
			defer func() { _ = session.Close() }()

			conn.mu.Lock()
			writes := make([][]byte, len(conn.writes))
			for writeIndex := range conn.writes {
				writes[writeIndex] = append([]byte(nil), conn.writes[writeIndex]...)
			}
			conn.mu.Unlock()
			if len(writes) != 1 {
				t.Fatalf("provider writes = %d, want exactly initial session.update: %s", len(writes), writes)
			}

			if index == 0 {
				wantInstructions = instructions
				wantProviderUpdate = writes[0]
				return
			}
			if !bytes.Equal(instructions, wantInstructions) {
				t.Fatalf("independent composed instructions differ:\nfirst=%s\nsecond=%s", wantInstructions, instructions)
			}
			if !bytes.Equal(writes[0], wantProviderUpdate) {
				t.Fatalf("independent provider session.update bytes differ:\nfirst=%s\nsecond=%s", wantProviderUpdate, writes[0])
			}
		})
	}
}

func TestComposeSessionInstructionsIsIdempotentAndLeavesNoToolsUnchanged(t *testing.T) {
	withTools := SessionRunOptions{
		ToolDefinitions: []messages.ToolDefinition{{Name: "exec"}},
	}
	first := composeSessionInstructions(withTools, "customer instructions")
	second := composeSessionInstructions(withTools, first)
	if second != first {
		t.Fatalf("second composition changed instructions:\nfirst=%q\nsecond=%q", first, second)
	}
	if strings.Count(second, "Tool-grounding requirements:") != 1 {
		t.Fatalf("idempotent grounding policy count = %d, want 1", strings.Count(second, "Tool-grounding requirements:"))
	}

	withoutTools := composeSessionInstructions(SessionRunOptions{}, "customer instructions")
	if withoutTools != "customer instructions" {
		t.Fatalf("no-tools composition = %q, want unchanged customer instructions", withoutTools)
	}
}

func TestComposeSessionInstructionsAddsDeterministicSightRouting(t *testing.T) {
	got := composeSessionInstructions(SessionRunOptions{
		BrowserToolsEnabled: true,
		ToolDefinitions:     []messages.ToolDefinition{{Name: "show_page"}},
	}, "customer instructions")
	for _, want := range []string{
		"Sight routing requirements:",
		"show_page",
		"authoritative page sight",
		"structured state",
		"board-state tool is authoritative",
		"show_screen",
		"Never use host-display sight as a fallback",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("sight instructions = %q, missing %q", got, want)
		}
	}
	for _, forbidden := range []string{
		"System Settings",
		"Privacy & Security",
		"Tell the customer",
		"restart",
	} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("sight instructions = %q, contains operator-only text %q", got, forbidden)
		}
	}
	if second := composeSessionInstructions(SessionRunOptions{
		BrowserToolsEnabled: true,
		ToolDefinitions:     []messages.ToolDefinition{{Name: "show_page"}},
	}, got); second != got {
		t.Fatalf("sight instruction composition is not idempotent:\nfirst=%q\nsecond=%q", got, second)
	}
}

func TestComposeSessionInstructionsDistinguishesConnectedUnselectedBrowser(t *testing.T) {
	states := []webmcp.BrowserCapabilityState{
		webmcp.BrowserCapabilityDisabled,
		webmcp.BrowserCapabilityUnavailable,
		webmcp.BrowserCapabilityDisconnected,
		webmcp.BrowserCapabilitySelected,
	}
	for _, state := range states {
		t.Run(string(state), func(t *testing.T) {
			got := composeSessionInstructions(SessionRunOptions{
				BrowserCapabilityState: state,
				ToolDefinitions:        []messages.ToolDefinition{{Name: webmcp.ListTabsToolName}},
			}, "customer instructions")
			if strings.Contains(got, sessionConnectedUnselectedBrowserGrounding) || strings.Contains(got, "browser endpoint is connected") {
				t.Fatalf("state %q received connected-unselected grounding: %q", state, got)
			}
		})
	}

	got := composeSessionInstructions(SessionRunOptions{
		BrowserCapabilityState: webmcp.BrowserCapabilityConnectedUnselected,
		ToolDefinitions:        []messages.ToolDefinition{{Name: webmcp.ListTabsToolName}, {Name: webmcp.SelectTabToolName}},
	}, "customer instructions")
	for _, want := range []string{
		"browser endpoint is connected",
		"no page is selected",
		webmcp.ListTabsToolName,
		"ask the customer which page to use",
		"exact browser_id and target_id",
		"do not invoke page tools",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("connected-unselected instructions = %q, missing %q", got, want)
		}
	}
	if strings.Count(got, "WebMCP browser selection:") != 1 || strings.Count(got, "Tool-grounding requirements:") != 1 {
		t.Fatalf("connected-unselected grounding is not bounded/idempotent: %q", got)
	}
}

func TestProviderInitialInstructionsCarryConnectedUnselectedBrowserContract(t *testing.T) {
	configDir := t.TempDir()
	writeSessionConfigFile(t, configDir, "model:\n  provider: openai\n")
	opts := SessionRunOptions{
		RecordPath:   filepath.Join(t.TempDir(), "session.json"),
		Provider:     config.ProviderOpenAI,
		Model:        openAIRealtimeDefaultModel,
		APIKey:       "test-key",
		ConfigDir:    configDir,
		ToolExecutor: &messages.DefaultToolExecutor{},
		ToolDefinitions: []messages.ToolDefinition{
			{Name: webmcp.ListTabsToolName},
			{Name: webmcp.SelectTabToolName},
		},
		BrowserCapabilityState: webmcp.BrowserCapabilityConnectedUnselected,
	}

	plan, err := planSessionWithResolvedInstructions(opts, "customer instructions")
	if err != nil {
		t.Fatalf("build planner: %v", err)
	}
	request := sessionRequestFromPlanner(t, plan.inferencer)
	for _, want := range []string{
		"browser endpoint is connected",
		"no page is selected",
		"Before any page work, call webmcp_list_tabs",
		"ask the customer which page to use",
		"exact browser_id and target_id",
		"do not invoke page tools",
	} {
		if !strings.Contains(request.Config.Instructions, want) {
			t.Fatalf("provider instructions = %q, missing %q", request.Config.Instructions, want)
		}
	}
	if !strings.HasPrefix(request.Config.Instructions, "customer instructions\n\n") {
		t.Fatalf("provider instructions = %q, want customer instructions first", request.Config.Instructions)
	}
}

func TestComposeSessionInstructionsAddsBoundedWebMCPAmbiguityRecovery(t *testing.T) {
	opts := SessionRunOptions{
		BrowserToolsEnabled: true,
		ToolDefinitions: []messages.ToolDefinition{
			{Name: "webmcp_get_context"},
			{Name: "webmcp_list_tabs"},
			{Name: "webmcp_select_tab"},
		},
	}

	first := composeSessionInstructions(opts, "customer instructions")
	second := composeSessionInstructions(opts, first)
	if second != first {
		t.Fatalf("second browser composition changed instructions:\nfirst=%q\nsecond=%q", first, second)
	}
	if strings.Count(first, "WebMCP ambiguity recovery:") != 1 {
		t.Fatalf("ambiguity policy count = %d, want 1; instructions=%q", strings.Count(first, "WebMCP ambiguity recovery:"), first)
	}
	for _, want := range []string{
		"error.code \"ambiguous_tab\"",
		"details.recovery.action \"ask_customer\"",
		"Ask exactly one concise spoken/text question",
		"name every candidate",
		"details.candidate_browser_ids",
		"do not repeat webmcp_get_context, webmcp_list_tabs, or webmcp_select_tab",
		"exact browser_id and target_id",
		"Do not substitute by list order",
	} {
		if !strings.Contains(first, want) {
			t.Fatalf("ambiguity policy = %q, missing %q", first, want)
		}
	}

	withoutBrowser := composeSessionInstructions(SessionRunOptions{
		ToolDefinitions: opts.ToolDefinitions,
	}, "customer instructions")
	if strings.Contains(withoutBrowser, "WebMCP ambiguity recovery:") {
		t.Fatalf("browser ambiguity policy leaked into non-browser session: %q", withoutBrowser)
	}
}

// TestComposeSessionInstructionsCalibratesSingleMatchActImmediately covers
// the ask-vs-act calibration's act side (required tests 1-3): a customer
// request that resolves to exactly one eligible tab -- whether by exact
// title, by an obvious paraphrase of that title, or by the page's stated
// purpose or category -- must be switched to immediately, with confirmation
// only after the switch, and never gated behind a pre-emptive clarifying
// question. This is the Session-1 ("the document editor") and Session-4
// ("the local first writing app" paraphrase) live failure: a single resolved
// candidate still produced a clarifying question instead of a switch.
func TestComposeSessionInstructionsCalibratesSingleMatchActImmediately(t *testing.T) {
	got := composeSessionInstructions(SessionRunOptions{
		BrowserToolsEnabled: true,
		ToolDefinitions: []messages.ToolDefinition{
			{Name: webmcp.ListTabsToolName},
			{Name: webmcp.SelectTabToolName},
		},
	}, "customer instructions")

	if strings.Count(got, "WebMCP tab selection calibration:") != 1 {
		t.Fatalf("tab selection calibration heading count = %d, want 1; instructions=%q", strings.Count(got, "WebMCP tab selection calibration:"), got)
	}
	for _, want := range []string{
		// Test 1: exact title.
		"matches by exact title",
		// Test 2: paraphrase.
		"an obvious paraphrase of that title",
		"Do not require the customer's wording to be a literal, word-for-word match",
		// Test 3: purpose/category.
		"the page's stated purpose or category",
		"document editor",
		// Act immediately on the single match; confirm only afterward.
		"Exactly one eligible tab: call webmcp_select_tab with its exact browser_id and target_id immediately",
		"Do not ask a clarifying or confirmation question first",
		"Confirm only after the switch succeeds",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("tab selection calibration = %q, missing %q", got, want)
		}
	}

	second := composeSessionInstructions(SessionRunOptions{
		BrowserToolsEnabled: true,
		ToolDefinitions: []messages.ToolDefinition{
			{Name: webmcp.ListTabsToolName},
			{Name: webmcp.SelectTabToolName},
		},
	}, got)
	if second != got {
		t.Fatalf("tab selection calibration is not idempotent:\nfirst=%q\nsecond=%q", got, second)
	}
}

func TestComposeSessionInstructionsDistinguishesCurrentTabNavigation(t *testing.T) {
	got := composeSessionInstructions(SessionRunOptions{
		BrowserCapabilityState: webmcp.BrowserCapabilitySelected,
		BrowserToolsEnabled:    true,
		ToolDefinitions: []messages.ToolDefinition{
			{Name: webmcp.ListTabsToolName},
			{Name: webmcp.SelectTabToolName},
			{Name: webmcp.OpenTabToolName},
			{Name: webmcp.NavigateTabToolName},
		},
	}, "customer instructions")

	for _, want := range []string{
		"Distinguish tab selection from navigation",
		"asks to open a new tab, call webmcp_open_tab",
		"asks to change, redirect, or navigate the currently selected tab",
		"call webmcp_navigate_tab directly",
		"do not open or select another tab first",
		"preserves the selected target and any active cast",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("current-tab navigation calibration = %q, missing %q", got, want)
		}
	}
}

func TestComposeSessionInstructionsFollowsExplicitMultiPageOrder(t *testing.T) {
	got := composeSessionInstructions(SessionRunOptions{
		BrowserCapabilityState: webmcp.BrowserCapabilityConnectedUnselected,
		BrowserToolsEnabled:    true,
		ToolDefinitions: []messages.ToolDefinition{
			{Name: webmcp.ListTabsToolName},
			{Name: webmcp.SelectTabToolName},
		},
	}, "Edit the greeting card, then update the document editor.")

	for _, want := range []string{
		"the first unfinished page is the current requested page",
		"never ask which page comes first when the customer already supplied the order",
		"resolve only the first unfinished page when deciding the current match",
		"do not treat the ordered set itself as selection ambiguity",
		"Two or more tabs matching the same current step",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("ordered multi-page calibration = %q, missing %q", got, want)
		}
	}
	if strings.Contains(got, "If multiple eligible tabs are returned, ask the customer which page to use; do not guess.") {
		t.Fatalf("ordered multi-page calibration retained the conflicting blanket ambiguity rule: %q", got)
	}
}

// TestComposeSessionInstructionsCalibratesGenuineAmbiguityAsksNamingBoth
// covers required test 4: when two or more tabs genuinely match, the
// calibration must direct exactly one question naming every candidate, and
// it must never fall back to declaring the capability unavailable. This
// mirrors the working "genuinely ambiguous" probe on current main, which
// this change must not regress.
func TestComposeSessionInstructionsCalibratesGenuineAmbiguityAsksNamingBoth(t *testing.T) {
	got := composeSessionInstructions(SessionRunOptions{
		BrowserToolsEnabled: true,
		ToolDefinitions: []messages.ToolDefinition{
			{Name: webmcp.ListTabsToolName},
			{Name: webmcp.SelectTabToolName},
		},
	}, "customer instructions")

	for _, want := range []string{
		"Two or more tabs matching the same current step: ask exactly one concise question naming every matching candidate by its title",
		"Do not guess and do not select by list order",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("tab selection calibration = %q, missing %q", got, want)
		}
	}
}

// TestComposeSessionInstructionsNeverDeniesCapabilityForSelectionAmbiguity
// covers required test 5: unresolved selection ambiguity must never surface
// to the model as "the capability is unavailable." This is the Session-2/3
// live failure (zero tool calls, "I can't directly inspect..."). Critically,
// the calibration block -- unlike sessionConnectedUnselectedBrowserGrounding
// -- must appear for every browser capability state as long as browser tools
// are enabled, not only the narrow "connected but never yet selected" state,
// because the failing live probe had already selected a page earlier in the
// session before the customer asked to switch tabs.
func TestComposeSessionInstructionsNeverDeniesCapabilityForSelectionAmbiguity(t *testing.T) {
	states := []webmcp.BrowserCapabilityState{
		webmcp.BrowserCapabilityConnectedUnselected,
		webmcp.BrowserCapabilitySelected,
		webmcp.BrowserCapabilityInitializing,
		"",
	}
	for _, state := range states {
		t.Run(string(state)+"/enabled", func(t *testing.T) {
			got := composeSessionInstructions(SessionRunOptions{
				BrowserCapabilityState: state,
				BrowserToolsEnabled:    true,
				ToolDefinitions: []messages.ToolDefinition{
					{Name: webmcp.ListTabsToolName},
					{Name: webmcp.SelectTabToolName},
				},
			}, "customer instructions")
			if !strings.Contains(got, "WebMCP tab selection calibration:") {
				t.Fatalf("state %q with browser tools enabled did not receive proactive tab selection calibration: %q", state, got)
			}
			if !strings.Contains(got, "do not deny the capability") {
				t.Fatalf("state %q calibration missing the never-deny-capability instruction: %q", state, got)
			}
		})
	}

	// Without browser tools enabled, the browser-specific calibration must
	// not leak in -- this is not a browser-capable session at all.
	withoutBrowserTools := composeSessionInstructions(SessionRunOptions{
		BrowserCapabilityState: webmcp.BrowserCapabilitySelected,
		ToolDefinitions: []messages.ToolDefinition{
			{Name: webmcp.ListTabsToolName},
			{Name: webmcp.SelectTabToolName},
		},
	}, "customer instructions")
	if strings.Contains(withoutBrowserTools, "WebMCP tab selection calibration:") {
		t.Fatalf("tab selection calibration leaked into a non-browser-tools session: %q", withoutBrowserTools)
	}
}

// TestComposeSessionInstructionsSingleEligibleHappyPathStaysUnchanged covers
// required test 6: the single-eligible-tab happy path (no genuine ambiguity
// at all) must keep resolving exactly as before -- the new calibration text
// must not introduce an extra "ask first" step for the already-correct
// connected-but-unselected grounding, and composition must stay idempotent
// and free of duplicate headings when both blocks apply together.
func TestComposeSessionInstructionsSingleEligibleHappyPathStaysUnchanged(t *testing.T) {
	got := composeSessionInstructions(SessionRunOptions{
		BrowserCapabilityState: webmcp.BrowserCapabilityConnectedUnselected,
		BrowserToolsEnabled:    true,
		ToolDefinitions: []messages.ToolDefinition{
			{Name: webmcp.ListTabsToolName},
			{Name: webmcp.SelectTabToolName},
		},
	}, "customer instructions")

	for _, heading := range []string{
		"WebMCP browser selection:",
		"WebMCP tab selection calibration:",
		"WebMCP ambiguity recovery:",
		"Tool-grounding requirements:",
	} {
		if strings.Count(got, heading) != 1 {
			t.Fatalf("heading %q count = %d, want 1; instructions=%q", heading, strings.Count(got, heading), got)
		}
	}
	// The connected-unselected grounding must distinguish unrelated tabs from
	// multiple matches for the current step.
	if !strings.Contains(got, "only ask the customer which page to use when multiple tabs still match that current step") {
		t.Fatalf("connected-unselected grounding lost its current-step ambiguity rule: %q", got)
	}

	second := composeSessionInstructions(SessionRunOptions{
		BrowserCapabilityState: webmcp.BrowserCapabilityConnectedUnselected,
		BrowserToolsEnabled:    true,
		ToolDefinitions: []messages.ToolDefinition{
			{Name: webmcp.ListTabsToolName},
			{Name: webmcp.SelectTabToolName},
		},
	}, got)
	if second != got {
		t.Fatalf("combined grounding composition is not idempotent:\nfirst=%q\nsecond=%q", got, second)
	}
}

func TestComposeSessionInstructionsRequiresHonestFilesystemRefusalHandling(t *testing.T) {
	instructions := composeSessionInstructions(SessionRunOptions{
		ToolDefinitions: []messages.ToolDefinition{{Name: "write_file"}},
	}, "customer instructions")
	for _, want := range []string{
		"filesystem refusal envelope",
		"refused and not performed",
		"operation, path, workdir, reason, and remediation",
		"--allow-path",
		"protected or sensitive reads cannot be authorized",
	} {
		if !strings.Contains(instructions, want) {
			t.Fatalf("composed refusal policy = %q, want substring %q", instructions, want)
		}
	}
}

func sessionRequestFromPlanner(t *testing.T, inferencer messages.SessionInferencer) inference.SessionRequest {
	t.Helper()
	if image, ok := inferencer.(*sessionImageInferencer); ok {
		inferencer = image.inner
	}
	requester, ok := inferencer.(interface {
		Request() inference.SessionRequest
	})
	if !ok {
		t.Fatalf("planner inferencer %T does not expose its provider request", inferencer)
	}
	return requester.Request()
}
