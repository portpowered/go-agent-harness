//go:build e2e_internal

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

const (
	sessionPaperieMarginLiveEnv      = "WEBMCP_PAPERIE_MARGIN_LIVE"
	sessionPaperieMarginCDPEnv       = "WEBMCP_PAPERIE_MARGIN_LIVE_CDP_URL"
	sessionPaperieMarginArtifactEnv  = "WEBMCP_PAPERIE_MARGIN_ARTIFACT_DIR"
	sessionPaperieMarginModel        = "gpt-realtime-2.1-mini"
	sessionPaperieMarginURL          = "https://paperie-webmcp-greeting-cards.openai.chatgpt.site/"
	sessionPaperieMarginOrigin       = "https://paperie-webmcp-greeting-cards.openai.chatgpt.site"
	sessionPaperieMarginMaxDuration  = 55 * time.Second
	sessionPaperieMarginRunGrace     = 25 * time.Second
	sessionPaperieMarginArtifactMode = 0o700
	sessionPaperieMarginEvidenceMode = 0o600
)

var sessionPaperieTools = []string{
	"apply_generated_artwork", "get_card_state", "list_print_options",
	"review_and_print", "select_card_template", "set_card_design",
	"set_card_message", "set_card_size", "set_envelope",
	"set_print_quantity", "set_preview", "set_recipient_context",
	"start_custom_card",
}

var sessionMarginTools = []string{
	"add_comment", "create_document", "get_document", "list_comments",
	"list_documents", "open_document", "reopen_comment", "reply_to_comment",
	"resolve_comment", "update_document",
}

// TestSessionPaperieMarginFromBaselineAgentsMD is the billed outside-in proof
// for purpose-based tab selection and cross-page editing. Chrome is externally
// owned so the same test works on macOS, Linux, and Windows. Start a fresh
// WebMCP-enabled Chrome with Paperie as its only page and pass /json/version in
// WEBMCP_PAPERIE_MARGIN_LIVE_CDP_URL; the test opens Margin itself.
func TestSessionPaperieMarginFromBaselineAgentsMD(t *testing.T) {
	if os.Getenv(sessionPaperieMarginLiveEnv) != "1" {
		t.Skipf("set %s=1 to run the credentialed Paperie/Margin acceptance proof", sessionPaperieMarginLiveEnv)
	}
	apiKey, keySource := sessionPageToolsSwitchVoiceAPIKey(t)
	cdpURL := strings.TrimSpace(os.Getenv(sessionPaperieMarginCDPEnv))
	if cdpURL == "" {
		t.Skipf("set %s to a fresh WebMCP-enabled Chrome /json/version endpoint", sessionPaperieMarginCDPEnv)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	assertLiveChromeStartupShape(t, ctx, cdpURL, sessionPaperieMarginOrigin, "Paperie")
	openLiveCDPTab(t, ctx, cdpURL, sessionPageToolsLiveMarginURL, "Margin")
	paperie, margin := discoverPaperieMarginTargets(t, ctx, cdpURL)
	if paperie.BrowserID != margin.BrowserID {
		t.Fatalf("scenario targets use different browsers: Paperie=%q Margin=%q", paperie.BrowserID, margin.BrowserID)
	}

	artifactRoot, workspace, configDir := preparePaperieMarginWorkspace(t)

	token := fmt.Sprintf("%d", time.Now().UnixNano())
	title := "Customer follow-up " + token
	const (
		eyebrow    = "CELEBRATING YOU"
		front      = "Bright days ahead!"
		inside     = "Maya, congratulations on your new studio. Jordan."
		initial    = "First draft."
		appendText = "Bring the greeting cards."
	)
	prompt := fmt.Sprintf("Switch to the already-open greeting-card studio. Start a blank custom card addressed to Maya from Jordan. Set the cover label exactly %q, the front message exactly %q, and the inside message exactly %q. Read the card state back. Then switch to the already-open local document editor. Create a note titled exactly %q with the text exactly %q. Next append a new paragraph containing exactly %q and read that document back. Do not use shell or screenshots; finish only after both pages verify the requested text.", eyebrow, front, inside, title, initial, appendText)

	agentBinary := buildLiveAgentCLI(t, ctx)
	capturePath := filepath.Join(artifactRoot, "provider.json")
	recordDir := filepath.Join(artifactRoot, "recording")
	args := []string{
		"-C", configDir,
		"--workdir", workspace,
		"session",
		"--provider", config.ProviderOpenAI,
		"--model", sessionPaperieMarginModel,
		"--browser-tools", "webmcp",
		"--browser-cdp-url", cdpURL,
		"--browser-auto-select", "off",
		"--browser-activate-tab", "false",
		"--browser-persist-selection", "false",
		"--browser-allowed-origin", sessionPaperieMarginOrigin,
		"--browser-allowed-origin", sessionPageToolsLiveMarginOrigin,
		"--browser-approval", "never",
		"--browser-cancel-on-interrupt", "always",
		"--browser-invocation-timeout", "30s",
		"--browser-record", "true",
		"--browser-record-arguments", "true",
		"--browser-record-results", "true",
		"--record", capturePath,
		"--record-dir", recordDir,
		"--prompt", prompt,
		"--max-duration", sessionPaperieMarginMaxDuration.String(),
	}
	processCtx, cancelProcess := context.WithTimeout(ctx, sessionPaperieMarginMaxDuration+sessionPaperieMarginRunGrace)
	process := exec.CommandContext(processCtx, agentBinary, args...)
	process.Env = append(os.Environ(), "AGENT_MODEL__OPENAI__API_KEY="+apiKey)
	var stdout, stderr bytes.Buffer
	process.Stdout = &stdout
	process.Stderr = &stderr
	runErr := process.Run()
	cancelProcess()
	if runErr != nil && !errors.Is(runErr, context.DeadlineExceeded) {
		t.Fatalf("production session failed: %v\nstdout=%s\nstderr=%s", runErr, truncateLiveText(stdout.Bytes(), 2000), truncateLiveText(stderr.Bytes(), 3000))
	}

	capture, err := gwtesting.LoadSessionCapture(capturePath)
	if err != nil {
		t.Fatalf("load provider capture: %v", err)
	}
	observation, err := inspectSessionPageToolsSwitchVoiceCapture(capture)
	if err != nil {
		t.Fatalf("inspect provider capture: %v", err)
	}
	documentID, err := validatePaperieMarginObservation(observation, paperie, margin, title, eyebrow, front, inside, initial, appendText)
	if err != nil {
		t.Fatalf("validate production trace: %v", err)
	}

	paperieCatalog := directLiveCatalog(t, ctx, agentBinary, cdpURL, paperie, sessionPaperieTools)
	cardState := directLiveInvoke(t, ctx, agentBinary, cdpURL, paperie, findDirectToolRef(t, paperieCatalog, "get_card_state"), map[string]any{})
	requireLiveSuccess(t, cardState, "direct Paperie state oracle")
	if !paperieMarginJSONContains(cardState.Data, "Maya", "Jordan", eyebrow, front, inside) {
		t.Fatalf("Paperie state does not contain the exact requested fields: %s", truncateLiveText(cardState.Data, 3000))
	}
	marginCatalog := directLiveCatalog(t, ctx, agentBinary, cdpURL, margin, sessionMarginTools)
	document := directLiveInvoke(t, ctx, agentBinary, cdpURL, margin, findDirectToolRef(t, marginCatalog, liveGetDocumentToolName), map[string]any{"document_id": documentID})
	requireLiveSuccess(t, document, "direct Margin document oracle")
	if !paperieMarginJSONContains(document.Data, title, initial, appendText) {
		t.Fatalf("Margin document does not contain created and appended text: %s", truncateLiveText(document.Data, 3000))
	}
	if err := validateSessionPageToolsSwitchVoiceRecordDir(recordDir); err != nil {
		t.Fatalf("validate record-dir: %v", err)
	}
	assertLiveChromeStillHasOrigins(t, ctx, cdpURL, sessionPaperieMarginOrigin, sessionPageToolsLiveMarginOrigin)
	t.Logf("WEBMCP_PAPERIE_MARGIN_PASS model=%s key_source=%s browser=%s paperie=%s margin=%s document=%s artifacts=%s", sessionPaperieMarginModel, keySource, paperie.BrowserID, paperie.TargetID, margin.TargetID, documentID, artifactRoot)
}

func discoverPaperieMarginTargets(t *testing.T, ctx context.Context, cdpURL string) (sessionPageToolsLiveTarget, sessionPageToolsLiveTarget) {
	t.Helper()
	browser := config.DefaultBrowserConfig()
	browser.Tools.Enabled = true
	browser.Tools.Backend = config.BrowserToolsBackendWebMCP
	browser.Connection.CDPURL = cdpURL
	browser.Selection.AutoSelect = config.BrowserAutoSelectOff
	browser.Selection.Persist = false
	browser.Policy.AllowedOrigins = []string{sessionPaperieMarginOrigin, sessionPageToolsLiveMarginOrigin}
	cfg := &config.Config{Browser: browser, ConfigDir: t.TempDir()}
	capabilities, err := NewSessionToolCapabilitiesFactory(nil, nil)(cfg)
	if err != nil {
		t.Fatalf("create target-discovery capabilities: %v", err)
	}
	if capabilities.Close != nil {
		defer func() {
			if closeErr := capabilities.Close(); closeErr != nil {
				t.Logf("close target-discovery capabilities: %v", closeErr)
			}
		}()
	}
	if err := capabilities.Initialize(ctx); err != nil {
		t.Fatalf("initialize target discovery: %v", err)
	}
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		response, callErr := capabilities.Executor.Execute(ctx, messages.ToolCall{ID: "paperie-margin-list-tabs", Name: webmcp.ListTabsToolName, Arguments: `{"eligible_only":true,"include_zero_tool_pages":true}`})
		if callErr == nil {
			if paperie, margin, found := paperieMarginTargetsFromTabs(response.Content); found {
				return paperie, margin
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("discover targets: %v", ctx.Err())
		case <-time.After(2 * time.Second):
		}
	}
	t.Fatal("timed out discovering Paperie and Margin targets")
	return sessionPageToolsLiveTarget{}, sessionPageToolsLiveTarget{}
}

// paperieMarginTargetsFromTabs finds the Paperie and Margin targets in one
// successful list-tabs result.
func paperieMarginTargetsFromTabs(content string) (paperie, margin sessionPageToolsLiveTarget, found bool) {
	envelope, err := webmcp.UnmarshalToolResult([]byte(content))
	if err != nil || !envelope.OK {
		return paperie, margin, false
	}
	var tabs sessionPageToolsLiveTabs
	if json.Unmarshal(envelope.Data, &tabs) != nil {
		return paperie, margin, false
	}
	for _, target := range tabs.Targets {
		switch target.Origin {
		case sessionPaperieMarginOrigin:
			paperie = target
		case sessionPageToolsLiveMarginOrigin:
			margin = target
		}
	}
	return paperie, margin, paperie.TargetID != "" && margin.TargetID != ""
}

func paperieMarginArtifactRoot(t *testing.T) string {
	t.Helper()
	parent := strings.TrimSpace(os.Getenv(sessionPaperieMarginArtifactEnv))
	if parent == "" {
		return t.TempDir()
	}
	if err := os.MkdirAll(parent, sessionPaperieMarginArtifactMode); err != nil {
		t.Fatalf("create artifact parent: %v", err)
	}
	root, err := os.MkdirTemp(parent, "paperie-margin-")
	if err != nil {
		t.Fatalf("create artifact root: %v", err)
	}
	return root
}

// preparePaperieMarginWorkspace creates the run's artifact root, a workspace
// holding the page-agnostic baseline AGENTS.md, and a browser-only config.
func preparePaperieMarginWorkspace(t *testing.T) (artifactRoot, workspace, configDir string) {
	t.Helper()
	artifactRoot = paperieMarginArtifactRoot(t)
	workspace = filepath.Join(artifactRoot, "workspace")
	configDir = filepath.Join(artifactRoot, "config")
	for _, directory := range []string{workspace, configDir} {
		if err := os.MkdirAll(directory, sessionPaperieMarginArtifactMode); err != nil {
			t.Fatalf("create %s: %v", filepath.Base(directory), err)
		}
	}
	if err := os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte(sessionWebMCPWorkspaceBaseline), sessionPaperieMarginEvidenceMode); err != nil {
		t.Fatalf("write baseline AGENTS.md: %v", err)
	}
	if err := writePaperieMarginBrowserOnlyConfig(configDir); err != nil {
		t.Fatalf("write browser-only config: %v", err)
	}
	return artifactRoot, workspace, configDir
}

func writePaperieMarginBrowserOnlyConfig(configDir string) error {
	var builder strings.Builder
	builder.WriteString("tools:\n  list:\n")
	for _, id := range config.DefaultToolIDs {
		fmt.Fprintf(&builder, "    - id: %q\n      enabled: false\n", id)
	}
	return os.WriteFile(filepath.Join(configDir, config.ConfigFileName), []byte(builder.String()), sessionPaperieMarginEvidenceMode)
}

func validatePaperieMarginObservation(observation sessionPageToolsSwitchVoiceObservation, paperie, margin sessionPageToolsLiveTarget, title, eyebrow, front, inside, initial, appendText string) (string, error) {
	if observation.Provider != config.ProviderOpenAI || observation.Model != sessionPaperieMarginModel || observation.SessionCreated != 1 {
		return "", fmt.Errorf("provider=(%q,%q) session.created=%d, want openai/%s and one session", observation.Provider, observation.Model, observation.SessionCreated, sessionPaperieMarginModel)
	}
	if !paperieMarginSurfaceContains(observation.Surfaces, sessionPaperieTools) || !paperieMarginSurfaceContains(observation.Surfaces, sessionMarginTools) {
		return "", errors.New("dynamic tool surfaces did not advertise both Paperie and Margin page tools")
	}
	state := paperieMarginCallState{
		paperie: paperie, margin: margin, title: title, eyebrow: eyebrow, front: front, inside: inside, initial: initial, appendText: appendText,
		outputs: make(map[string]sessionPageToolsSwitchVoiceOutput, len(observation.Outputs)),
	}
	for _, output := range observation.Outputs {
		if output.CallID == "" || !output.Envelope.OK {
			return "", fmt.Errorf("failed or uncorrelated tool output at record %d: %+v", output.Index, output.Envelope.Error)
		}
		state.outputs[output.CallID] = output
	}
	for _, call := range observation.Calls {
		if err := state.observe(call); err != nil {
			return "", err
		}
	}
	return state.finish()
}

// paperieMarginCallState replays the provider tool calls in order and tracks
// the selected page and the card and document edits made so far.
type paperieMarginCallState struct {
	paperie, margin                                    sessionPageToolsLiveTarget
	title, eyebrow, front, inside, initial, appendText string
	outputs                                            map[string]sessionPageToolsSwitchVoiceOutput
	paperieSelected, marginSelected, cardMutated       bool
	cardReadsBefore, cardReadsAfter                    int
	created, updated, readDocument                     bool
	documentID                                         string
}

func (state *paperieMarginCallState) observe(call sessionPageToolsSwitchVoiceCall) error {
	output, hasOutput := state.outputs[call.CallID]
	if call.CallID == "" || call.ArgumentsAt <= call.Index || !hasOutput {
		return fmt.Errorf("uncorrelated call: %+v", call)
	}
	switch {
	case call.Name == webmcp.SelectTabToolName:
		return state.observeSelection(call)
	case containsSessionPageToolsSwitchVoice(sessionPaperieTools, call.Name):
		return state.observePaperieCall(call)
	case containsSessionPageToolsSwitchVoice(sessionMarginTools, call.Name):
		return state.observeMarginCall(call, output)
	case !containsSessionPageToolsSwitchVoice(webmcp.StableToolNames(), call.Name):
		return fmt.Errorf("unexpected tool call %q", call.Name)
	}
	return nil
}

func (state *paperieMarginCallState) observeSelection(call sessionPageToolsSwitchVoiceCall) error {
	var raw map[string]string
	if err := json.Unmarshal([]byte(call.Arguments), &raw); err != nil {
		return fmt.Errorf("decode select arguments: %w", err)
	}
	browserID, targetID := raw["browser_id"], raw["target_id"]
	switch {
	case !state.paperieSelected && browserID == state.paperie.BrowserID && targetID == state.paperie.TargetID:
		state.paperieSelected = true
	case state.paperieSelected && !state.marginSelected && browserID == state.margin.BrowserID && targetID == state.margin.TargetID:
		state.marginSelected = true
	default:
		return fmt.Errorf("unexpected tab selection browser=%q target=%q", browserID, targetID)
	}
	return nil
}

func (state *paperieMarginCallState) observePaperieCall(call sessionPageToolsSwitchVoiceCall) error {
	inPaperie := state.paperieSelected && !state.marginSelected
	switch call.Name {
	case "get_card_state":
		if !inPaperie {
			return errors.New("get_card_state ran outside the Paperie interval")
		}
		if state.cardMutated {
			state.cardReadsAfter++
		} else {
			state.cardReadsBefore++
		}
	case "set_recipient_context":
		if !inPaperie || !paperieMarginArgumentsContain(call.Arguments, "Maya", "Jordan") {
			return fmt.Errorf("recipient edit was misplaced or inexact: %s", call.Arguments)
		}
		state.cardMutated = true
	case "set_card_message":
		if !inPaperie || !paperieMarginArgumentsContain(call.Arguments, state.eyebrow, state.front, state.inside) {
			return fmt.Errorf("card-message edit was misplaced or inexact: %s", call.Arguments)
		}
		state.cardMutated = true
	default:
		if !inPaperie {
			return fmt.Errorf("Paperie tool %q ran outside the Paperie interval", call.Name)
		}
	}
	return nil
}

func (state *paperieMarginCallState) observeMarginCall(call sessionPageToolsSwitchVoiceCall, output sessionPageToolsSwitchVoiceOutput) error {
	switch call.Name {
	case liveCreateDocumentToolName:
		if !state.marginSelected || !paperieMarginArgumentsContain(call.Arguments, state.title, state.initial) {
			return fmt.Errorf("document create was misplaced or inexact: %s", call.Arguments)
		}
		state.documentID = liveDocumentID(output.Envelope.Data)
		if state.documentID == "" {
			return errors.New("create_document omitted document ID")
		}
		state.created = true
	case "update_document":
		if !state.created || !paperieMarginArgumentsContain(call.Arguments, state.documentID, state.appendText) {
			return fmt.Errorf("document append was misplaced or inexact: %s", call.Arguments)
		}
		state.updated = true
	case liveGetDocumentToolName:
		if state.updated && paperieMarginArgumentsContain(call.Arguments, state.documentID) {
			state.readDocument = true
		}
	default:
		if !state.marginSelected {
			return fmt.Errorf("Margin tool %q ran before Margin selection", call.Name)
		}
	}
	return nil
}

func (state *paperieMarginCallState) finish() (string, error) {
	if !state.paperieSelected || !state.marginSelected || state.cardReadsBefore == 0 || state.cardReadsAfter == 0 || !state.cardMutated || !state.created || !state.updated || !state.readDocument {
		return "", fmt.Errorf("incomplete scenario: paperie=%t margin=%t card_reads=%d/%d card_mutated=%t created=%t updated=%t document_read=%t", state.paperieSelected, state.marginSelected, state.cardReadsBefore, state.cardReadsAfter, state.cardMutated, state.created, state.updated, state.readDocument)
	}
	return state.documentID, nil
}

func paperieMarginSurfaceContains(surfaces []sessionPageToolsSwitchVoiceSurface, want []string) bool {
	for _, surface := range surfaces {
		seen := make(map[string]bool, len(surface.Tools))
		for _, tool := range surface.Tools {
			seen[tool.Name] = true
		}
		matched := true
		for _, name := range want {
			if !seen[name] {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func paperieMarginArgumentsContain(raw string, values ...string) bool {
	var value any
	return json.Unmarshal([]byte(raw), &value) == nil && paperieMarginValueContains(value, values...)
}

func paperieMarginJSONContains(raw json.RawMessage, values ...string) bool {
	var value any
	return json.Unmarshal(raw, &value) == nil && paperieMarginValueContains(value, values...)
}

func paperieMarginValueContains(value any, values ...string) bool {
	encoded, err := json.Marshal(value)
	if err != nil {
		return false
	}
	text := string(encoded)
	for _, want := range values {
		quoted, err := json.Marshal(want)
		if err != nil {
			return false
		}
		if !strings.Contains(text, string(quoted)[1:len(quoted)-1]) {
			return false
		}
	}
	return true
}

func TestValidatePaperieMarginObservationRejectsIncompleteOrMisroutedRuns(t *testing.T) {
	paperie := sessionPageToolsLiveTarget{BrowserID: "browser", TargetID: "paperie"}
	margin := sessionPageToolsLiveTarget{BrowserID: "browser", TargetID: "margin"}
	valid := func() sessionPageToolsSwitchVoiceObservation {
		calls := []sessionPageToolsSwitchVoiceCall{
			{Name: webmcp.SelectTabToolName, Arguments: `{"browser_id":"browser","target_id":"paperie"}`},
			{Name: "get_card_state", Arguments: `{}`},
			{Name: "start_custom_card", Arguments: `{}`},
			{Name: "set_recipient_context", Arguments: `{"recipientName":"Maya","senderName":"Jordan"}`},
			{Name: "set_card_message", Arguments: `{"eyebrowText":"CELEBRATING YOU","frontMessage":"Bright days ahead!","insideMessage":"Maya, congratulations on your new studio. Jordan."}`},
			{Name: "get_card_state", Arguments: `{}`},
			{Name: webmcp.SelectTabToolName, Arguments: `{"browser_id":"browser","target_id":"margin"}`},
			{Name: "create_document", Arguments: `{"title":"Customer follow-up token","content":"First draft."}`},
			{Name: "update_document", Arguments: `{"document_id":"doc-1","append":"Bring the greeting cards."}`},
			{Name: "get_document", Arguments: `{"document_id":"doc-1"}`},
		}
		outputs := make([]sessionPageToolsSwitchVoiceOutput, len(calls))
		for index := range calls {
			calls[index].Index = index * 3
			calls[index].ArgumentsAt = index*3 + 1
			calls[index].CallID = fmt.Sprintf("call-%d", index)
			data := json.RawMessage(`{}`)
			if calls[index].Name == "create_document" {
				data = json.RawMessage(`{"document_id":"doc-1"}`)
			}
			outputs[index] = sessionPageToolsSwitchVoiceOutput{Index: index*3 + 2, CallID: calls[index].CallID, Envelope: webmcp.ToolResultEnvelope{OK: true, Data: data}}
		}
		paperieSurface := make([]sessionPageToolsSwitchVoiceTool, 0, len(sessionPaperieTools))
		for _, name := range sessionPaperieTools {
			paperieSurface = append(paperieSurface, sessionPageToolsSwitchVoiceTool{Name: name})
		}
		marginSurface := make([]sessionPageToolsSwitchVoiceTool, 0, len(sessionMarginTools))
		for _, name := range sessionMarginTools {
			marginSurface = append(marginSurface, sessionPageToolsSwitchVoiceTool{Name: name})
		}
		return sessionPageToolsSwitchVoiceObservation{
			Provider:       config.ProviderOpenAI,
			Model:          sessionPaperieMarginModel,
			SessionCreated: 1,
			Surfaces: []sessionPageToolsSwitchVoiceSurface{
				{Tools: paperieSurface}, {Tools: marginSurface},
			},
			Calls: calls, Outputs: outputs,
		}
	}
	validate := func(observation sessionPageToolsSwitchVoiceObservation) error {
		_, err := validatePaperieMarginObservation(observation, paperie, margin, "Customer follow-up token", "CELEBRATING YOU", "Bright days ahead!", "Maya, congratulations on your new studio. Jordan.", "First draft.", "Bring the greeting cards.")
		return err
	}
	if err := validate(valid()); err != nil {
		t.Fatalf("valid synthetic trace rejected: %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*sessionPageToolsSwitchVoiceObservation)
	}{
		{name: "wrong first tab", mutate: func(o *sessionPageToolsSwitchVoiceObservation) {
			o.Calls[0].Arguments = `{"browser_id":"browser","target_id":"margin"}`
		}},
		{name: "missing initial state read", mutate: func(o *sessionPageToolsSwitchVoiceObservation) { o.Calls[1].Name = "start_custom_card" }},
		{name: "inexact customer message", mutate: func(o *sessionPageToolsSwitchVoiceObservation) {
			o.Calls[4].Arguments = `{"frontMessage":"Close enough"}`
		}},
		{name: "missing post-edit state read", mutate: func(o *sessionPageToolsSwitchVoiceObservation) { o.Calls[5].Name = "set_preview" }},
		{name: "Margin tool before switch", mutate: func(o *sessionPageToolsSwitchVoiceObservation) { o.Calls[6], o.Calls[7] = o.Calls[7], o.Calls[6] }},
		{name: "append loses created id", mutate: func(o *sessionPageToolsSwitchVoiceObservation) {
			o.Calls[8].Arguments = `{"document_id":"wrong","append":"Bring the greeting cards."}`
		}},
		{name: "missing document readback", mutate: func(o *sessionPageToolsSwitchVoiceObservation) { o.Calls[9].Name = queryParityToolName }},
		{name: "failed page output", mutate: func(o *sessionPageToolsSwitchVoiceObservation) { o.Outputs[4].Envelope.OK = false }},
		{name: "wrong model", mutate: func(o *sessionPageToolsSwitchVoiceObservation) { o.Model = "other" }},
		{name: "missing dynamic surface", mutate: func(o *sessionPageToolsSwitchVoiceObservation) { o.Surfaces = o.Surfaces[:1] }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observation := valid()
			test.mutate(&observation)
			if err := validate(observation); err == nil {
				t.Fatal("adversarial trace was accepted")
			}
		})
	}
}
