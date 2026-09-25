package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// TestSessionPageToolsFirstClassAgainstLiveChrome is the credit-free
// session-shape proof for first-class page tools: against an externally
// launched Chrome (WEBMCP_PAGETOOLS_LIVE_CDP_URL) whose active tab exposes a
// WebMCP catalog, the production capability factory bootstraps, the
// refreshed definitions advertise the page tools first-class, bare-name
// calls execute through the composed invoke path with terminal status, and
// an unknown name yields guidance instead of a composition dead-end.
func TestSessionPageToolsFirstClassAgainstLiveChrome(t *testing.T) {
	cdpURL := strings.TrimSpace(os.Getenv("WEBMCP_PAGETOOLS_LIVE_CDP_URL"))
	if cdpURL == "" {
		t.Skip("set WEBMCP_PAGETOOLS_LIVE_CDP_URL to a live Chrome DevTools HTTP endpoint to run the live page-tools proof")
	}

	cfg := livePageToolsConfig(t, cdpURL)
	capabilities, err := NewSessionToolCapabilitiesFactory(nil, nil)(cfg)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	t.Cleanup(func() {
		if capabilities.Close != nil {
			closeForTest(t, capabilities.Close)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	if capabilities.Initialize != nil {
		if err := capabilities.Initialize(ctx); err != nil {
			t.Fatalf("bootstrap initialize: %v", err)
		}
	}
	base := len(capabilities.Definitions)
	refreshed := capabilities.RefreshDefinitions(ctx)
	if len(refreshed) <= base {
		t.Fatalf("refresh advertised no page tools: %d composed, %d refreshed", base, len(refreshed))
	}
	pageNames := make([]string, 0, len(refreshed)-base)
	for _, definition := range refreshed[base:] {
		pageNames = append(pageNames, definition.Name)
		t.Logf("page tool: %s | %s | params=%d closed=%v", definition.Name, definition.Description, len(definition.Parameters), definition.ParametersClosed)
	}

	execute := func(name, args string) webmcp.ToolResultEnvelope {
		t.Helper()
		return executeSessionPageToolsLiveCall(t, ctx, capabilities.Executor, name, args)
	}

	first := execute(pageNames[0], `{}`)
	requireLiveSuccess(t, first, "bare-name "+pageNames[0])
	t.Logf("%s -> %.200s", pageNames[0], string(first.Data))

	guidance := execute("definitely_not_a_page_tool", `{}`)
	if guidance.OK || guidance.Error == nil {
		t.Fatalf("unknown name did not produce guidance: %+v", guidance)
	}
	if !strings.Contains(guidance.Error.Message, "webmcp_list_tools") {
		t.Fatalf("guidance %q lacks the stable-path hint", guidance.Error.Message)
	}
}

// TestSessionPageToolsConcurrentColdSessions is the load-shaped proof for the
// gate's paired-probe reality: two sessions bootstrap concurrently against
// two separate Chromes and each serves its FIRST first-class page-tool call
// within the bounded long-running interactive budget. Set
// WEBMCP_PAGETOOLS_LIVE_CDP_URLS to two comma-separated DevTools HTTP
// endpoints whose active tabs expose a WebMCP catalog.
func TestSessionPageToolsConcurrentColdSessions(t *testing.T) {
	raw := strings.TrimSpace(os.Getenv("WEBMCP_PAGETOOLS_LIVE_CDP_URLS"))
	if raw == "" {
		t.Skip("set WEBMCP_PAGETOOLS_LIVE_CDP_URLS=<url1>,<url2> to run the concurrent cold-session proof")
	}
	urls := strings.Split(raw, ",")
	if len(urls) != 2 {
		t.Fatalf("need exactly two endpoints, got %d", len(urls))
	}

	type outcome struct {
		index    int
		duration time.Duration
		err      error
	}
	results := make(chan outcome, len(urls))
	for index, cdpURL := range urls {
		go func(index int, cdpURL string) {
			started := time.Now()
			err := runColdPageToolSession(livePageToolsConfig(t, strings.TrimSpace(cdpURL)))
			results <- outcome{index: index, duration: time.Since(started), err: err}
		}(index, cdpURL)
	}
	for range urls {
		result := <-results
		if result.err != nil {
			t.Fatalf("concurrent session %d failed: %v", result.index, result.err)
		}
		t.Logf("session %d first page-tool call served (total %s)", result.index, result.duration)
	}
}

// livePageToolsConfig enables WebMCP browser tools against one live endpoint
// with single-target automatic selection.
func livePageToolsConfig(t *testing.T, cdpURL string) *config.Config {
	browser := config.DefaultBrowserConfig()
	browser.Tools.Enabled = true
	browser.Tools.Backend = config.BrowserToolsBackendWebMCP
	browser.Connection.CDPURL = cdpURL
	browser.Selection.AutoSelect = config.BrowserAutoSelectSingle
	cfg := &config.Config{Browser: browser, ConfigDir: t.TempDir()}
	for _, id := range config.DefaultToolIDs {
		cfg.Tools.List = append(cfg.Tools.List, config.ToolEntry{ID: id, Enabled: id == "exec"})
	}
	return cfg
}

// runColdPageToolSession bootstraps one session's capabilities and serves its
// first first-class page-tool call under the interactive long-running budget.
func runColdPageToolSession(cfg *config.Config) (err error) {
	capabilities, err := NewSessionToolCapabilitiesFactory(nil, nil)(cfg)
	if err != nil {
		return fmt.Errorf("factory: %w", err)
	}
	defer func() {
		if capabilities.Close != nil {
			err = errors.Join(err, capabilities.Close())
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if capabilities.Initialize != nil {
		if err := capabilities.Initialize(ctx); err != nil {
			return fmt.Errorf("bootstrap: %w", err)
		}
	}
	refreshed := capabilities.RefreshDefinitions(ctx)
	if len(refreshed) <= len(capabilities.Definitions) {
		return fmt.Errorf("no page tools advertised")
	}
	name := refreshed[len(capabilities.Definitions)].Name
	// The first page-tool call runs under the bounded long-running
	// interactive budget, exactly as the session executor applies it.
	callContext, cancelCall := context.WithTimeout(ctx, config.DefaultInteractiveLongRunningTimeout)
	defer cancelCall()
	callStarted := time.Now()
	response, err := capabilities.Executor.Execute(callContext, messages.ToolCall{ID: "cold-" + name, Name: name, Arguments: `{}`})
	if err != nil {
		return fmt.Errorf("first page-tool call: %w", err)
	}
	var envelope webmcp.ToolResultEnvelope
	if err := json.Unmarshal([]byte(response.Content), &envelope); err != nil || !envelope.OK {
		return fmt.Errorf("first page-tool call after %s: %s", time.Since(callStarted), response.Content)
	}
	return nil
}

func executeSessionPageToolsLiveCall(t *testing.T, ctx context.Context, executor messages.ToolExecutor, name, args string) webmcp.ToolResultEnvelope {
	t.Helper()
	response, err := executor.Execute(ctx, messages.ToolCall{ID: "live-" + name, Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("execute %s: %v", name, err)
	}
	envelope, err := webmcp.UnmarshalToolResult([]byte(response.Content))
	if err != nil {
		t.Fatalf("%s result is not a valid envelope: %v; content=%s", name, err, truncateLiveText(json.RawMessage(response.Content), 1200))
	}
	return envelope
}

func requireLiveSuccess(t *testing.T, envelope webmcp.ToolResultEnvelope, operation string) {
	t.Helper()
	if !envelope.OK {
		t.Fatalf("%s failed: %+v", operation, envelope.Error)
	}
}

func truncateLiveText(raw []byte, limit int) string {
	text := string(raw)
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "…"
}

// Page-tool names shared by the live WebMCP session proofs and their trace validators.
const (
	sessionPageToolsSwitchVoiceModel = "gpt-realtime-2.1-mini"
	liveCreateDocumentToolName       = "create_document"
	liveGetDocumentToolName          = "get_document"
)

func liveCubecadePageTools() []string {
	return []string{ambiguousCubeStateTool, sessionAudioInterruptQueueTool}
}

func liveMarginPageTools() []string {
	return []string{
		"add_comment", liveCreateDocumentToolName, liveGetDocumentToolName, "list_comments", queryParityToolName,
		"open_document", "reopen_comment", "reply_to_comment", "resolve_comment", "update_document",
	}
}

func liveDocumentID(raw json.RawMessage) string {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return findLiveString(value, map[string]bool{"document_id": true, "id": true})
}

func findLiveString(value any, keys map[string]bool) string {
	switch typed := value.(type) {
	case map[string]any:
		if found := findLiveStringKey(typed, keys); found != "" {
			return found
		}
		for _, child := range typed {
			if found := findLiveString(child, keys); found != "" {
				return found
			}
		}
	case []any:
		for _, child := range typed {
			if found := findLiveString(child, keys); found != "" {
				return found
			}
		}
	}
	return ""
}

// findLiveStringKey returns a non-empty string value stored directly under
// one of keys in object.
func findLiveStringKey(object map[string]any, keys map[string]bool) string {
	for key, value := range object {
		if !keys[key] {
			continue
		}
		if stringValue, ok := value.(string); ok && stringValue != "" {
			return stringValue
		}
	}
	return ""
}

type sessionPageToolsSwitchVoiceTool struct {
	Name string
	Raw  json.RawMessage
}

type sessionPageToolsSwitchVoiceSurface struct {
	Index int
	Tools []sessionPageToolsSwitchVoiceTool
}

type sessionPageToolsSwitchVoiceCall struct {
	Index       int
	Name        string
	CallID      string
	Arguments   string
	ArgumentsAt int
}

type sessionPageToolsSwitchVoiceOutput struct {
	Index    int
	CallID   string
	Envelope webmcp.ToolResultEnvelope
}

type sessionPageToolsSwitchVoiceObservation struct {
	Provider             string
	Model                string
	SessionCreated       int
	Surfaces             []sessionPageToolsSwitchVoiceSurface
	Calls                []sessionPageToolsSwitchVoiceCall
	Outputs              []sessionPageToolsSwitchVoiceOutput
	UserTranscripts      []string
	AssistantTranscripts []string
}

func validateSessionPageToolsSwitchVoiceObservation(observation sessionPageToolsSwitchVoiceObservation, cubeTarget, marginTarget sessionPageToolsLiveTarget, title, content string) (string, error) {
	if err := validateSessionPageToolsSwitchVoiceIdentity(observation); err != nil {
		return "", err
	}
	pageNames := sessionPageToolsSwitchVoicePageNames()
	baseNames, err := sessionPageToolsSwitchVoiceBaseNames(observation.Surfaces, pageNames)
	if err != nil {
		return "", err
	}
	if err := validateSessionPageToolsSwitchVoiceSurfaces(observation.Surfaces, baseNames, pageNames); err != nil {
		return "", err
	}
	outputs, err := sessionPageToolsSwitchVoiceOutputsByCall(observation.Outputs)
	if err != nil {
		return "", err
	}
	state := sessionPageToolsSwitchVoiceCallState{
		cube: cubeTarget, margin: marginTarget, title: title, content: content,
		outputs: outputs, pageNames: pageNames, pageCallCount: map[string]int{},
	}
	for _, call := range observation.Calls {
		if err := state.observe(call); err != nil {
			return "", err
		}
	}
	return state.finish()
}

func validateSessionPageToolsSwitchVoiceIdentity(observation sessionPageToolsSwitchVoiceObservation) error {
	if observation.Provider != config.ProviderOpenAI || observation.Model != sessionPageToolsSwitchVoiceModel {
		return fmt.Errorf("provider identity=(%q,%q), want (openai,%q)", observation.Provider, observation.Model, sessionPageToolsSwitchVoiceModel)
	}
	if observation.SessionCreated != 1 {
		return fmt.Errorf("provider session.created count=%d, want one persistent connection", observation.SessionCreated)
	}
	if len(observation.UserTranscripts) < 5 {
		return fmt.Errorf("user transcript turns=%d, want five spoken turns", len(observation.UserTranscripts))
	}
	if len(observation.AssistantTranscripts) == 0 {
		return errors.New("voice capture has no assistant transcript")
	}
	transcript := strings.ToLower(strings.Join(observation.UserTranscripts, " "))
	for _, phrase := range []string{"cube", "document editor", "exact title", "switch back", "goodbye"} {
		if !strings.Contains(transcript, phrase) {
			return fmt.Errorf("spoken transcript %q is missing %q", transcript, phrase)
		}
	}
	return nil
}

func sessionPageToolsSwitchVoicePageNames() map[string]struct{} {
	names := map[string]struct{}{}
	for _, name := range append(liveCubecadePageTools(), liveMarginPageTools()...) {
		names[name] = struct{}{}
	}
	return names
}

func sessionPageToolsSwitchVoiceOutputsByCall(observed []sessionPageToolsSwitchVoiceOutput) (map[string]sessionPageToolsSwitchVoiceOutput, error) {
	outputs := map[string]sessionPageToolsSwitchVoiceOutput{}
	for _, output := range observed {
		if output.CallID == "" {
			return nil, fmt.Errorf("tool output at record %d omitted call_id", output.Index)
		}
		if _, exists := outputs[output.CallID]; exists {
			return nil, fmt.Errorf("tool output call_id=%q occurred more than once", output.CallID)
		}
		outputs[output.CallID] = output
		if !output.Envelope.OK {
			return nil, fmt.Errorf("voice tool %q failed: %+v", output.CallID, output.Envelope.Error)
		}
	}
	return outputs, nil
}

// sessionPageToolsSwitchVoiceCallState replays the provider tool calls in
// order and tracks which page is selected when each page tool runs.
type sessionPageToolsSwitchVoiceCallState struct {
	cube, margin                                      sessionPageToolsLiveTarget
	title, content                                    string
	outputs                                           map[string]sessionPageToolsSwitchVoiceOutput
	pageNames                                         map[string]struct{}
	initialCubeSelected, marginSelected, cubeSelected bool
	cubeReadsBefore, cubeReadsAfter                   int
	pageCallCount                                     map[string]int
	documentID                                        string
}

func (state *sessionPageToolsSwitchVoiceCallState) observe(call sessionPageToolsSwitchVoiceCall) error {
	if call.CallID == "" || call.ArgumentsAt <= call.Index {
		return fmt.Errorf("uncorrelated voice call: %+v", call)
	}
	if _, ok := state.outputs[call.CallID]; !ok {
		return fmt.Errorf("voice call %q has no tool result", call.CallID)
	}
	if call.Name == webmcp.SelectTabToolName {
		return state.observeSelection(call)
	}
	if _, isPage := state.pageNames[call.Name]; isPage {
		return state.observePageCall(call)
	}
	if !containsSessionPageToolsSwitchVoice(webmcp.StableToolNames(), call.Name) {
		return fmt.Errorf("unexpected provider tool call %q", call.Name)
	}
	return nil
}

func (state *sessionPageToolsSwitchVoiceCallState) observeSelection(call sessionPageToolsSwitchVoiceCall) error {
	var args struct {
		BrowserID string `json:"browser_id"`
		TargetID  string `json:"target_id"`
	}
	if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil {
		return fmt.Errorf("decode %s arguments: %w", call.Name, err)
	}
	isMargin := args.BrowserID == state.margin.BrowserID && args.TargetID == state.margin.TargetID
	isCube := args.BrowserID == state.cube.BrowserID && args.TargetID == state.cube.TargetID
	switch {
	case isMargin && !state.marginSelected:
		state.marginSelected = true
	case isCube && !state.marginSelected && !state.initialCubeSelected:
		state.initialCubeSelected = true
	case isCube && state.marginSelected && !state.cubeSelected:
		state.cubeSelected = true
	default:
		return fmt.Errorf("unexpected selection call arguments: browser=%q target=%q", args.BrowserID, args.TargetID)
	}
	return nil
}

func (state *sessionPageToolsSwitchVoiceCallState) observePageCall(call sessionPageToolsSwitchVoiceCall) error {
	state.pageCallCount[call.Name]++
	if call.Name == ambiguousCubeStateTool || call.Name == sessionAudioInterruptQueueTool {
		return state.observeCubeCall(call.Name)
	}
	if !state.marginSelected || state.cubeSelected {
		return fmt.Errorf("Margin page tool %q was called outside the selected Margin interval", call.Name)
	}
	switch call.Name {
	case liveCreateDocumentToolName:
		return state.observeCreateDocument(call)
	case liveGetDocumentToolName:
		var args struct {
			DocumentID string `json:"document_id"`
		}
		if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil {
			return fmt.Errorf("decode get_document arguments: %w", err)
		}
		if args.DocumentID != state.documentID {
			return fmt.Errorf("get_document document_id=%q, want created document %q", args.DocumentID, state.documentID)
		}
	}
	return nil
}

func (state *sessionPageToolsSwitchVoiceCallState) observeCubeCall(name string) error {
	if state.marginSelected && !state.cubeSelected {
		return fmt.Errorf("Cubecade page tool %q was called while Margin was selected", name)
	}
	if name == sessionAudioInterruptQueueTool {
		return errors.New("voice scenario unexpectedly attempted to move the cube")
	}
	if state.cubeSelected {
		state.cubeReadsAfter++
	} else {
		state.cubeReadsBefore++
	}
	return nil
}

func (state *sessionPageToolsSwitchVoiceCallState) observeCreateDocument(call sessionPageToolsSwitchVoiceCall) error {
	var args struct {
		Title   string `json:"title"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil {
		return fmt.Errorf("decode create_document arguments: %w", err)
	}
	if args.Title != state.title || args.Content != state.content {
		return fmt.Errorf("create_document arguments=(%q,%q), want exact=(%q,%q)", args.Title, args.Content, state.title, state.content)
	}
	state.documentID = liveDocumentID(state.outputs[call.CallID].Envelope.Data)
	if state.documentID == "" {
		return errors.New("create_document result omitted document ID")
	}
	return nil
}

func (state *sessionPageToolsSwitchVoiceCallState) finish() (string, error) {
	if !state.initialCubeSelected || !state.marginSelected || !state.cubeSelected {
		return "", fmt.Errorf("selection calls initial_cube=%t margin=%t return_cube=%t, want exact startup selection and both directions", state.initialCubeSelected, state.marginSelected, state.cubeSelected)
	}
	if state.cubeReadsBefore == 0 || state.cubeReadsAfter == 0 || state.pageCallCount[liveCreateDocumentToolName] != 1 || state.pageCallCount[liveGetDocumentToolName] < 1 {
		return "", fmt.Errorf("page call counts=%v cube_reads_before=%d cube_reads_after=%d, want cube reads on both sides and one create/get", state.pageCallCount, state.cubeReadsBefore, state.cubeReadsAfter)
	}
	if state.documentID == "" {
		return "", errors.New("voice trace did not produce a document ID")
	}
	return state.documentID, nil
}

func containsSessionPageToolsSwitchVoice(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

type sessionPageToolsLiveTarget struct {
	BrowserID string `json:"browser_id"`
	TargetID  string `json:"target_id"`
	Type      string `json:"type"`
	Origin    string `json:"origin"`
	Eligible  bool   `json:"eligible"`
}
