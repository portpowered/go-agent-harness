//go:build live

package cli

import servicetest "github.com/portpowered/go-agent-harness/agent-cli/internal/services/servicetest"

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

const (
	sessionPageToolsLiveCubecadeURL    = "https://cubecade.openai.chatgpt.site/"
	sessionPageToolsLiveMarginURL      = "https://margin-local-docs.openai.chatgpt.site/"
	sessionPageToolsLiveCubecadeOrigin = "https://cubecade.openai.chatgpt.site"
	sessionPageToolsLiveMarginOrigin   = "https://margin-local-docs.openai.chatgpt.site"
)

// TestSessionPageToolsSwitchAgainstLiveChrome is the credit-free session-shape
// proof for WebMCP tab switching. The browser is externally owned: the
// operator launches one fresh, loopback-only, pinned Chrome with Cubecade as
// its sole startup URL, and supplies its /json/version endpoint through
// WEBMCP_PAGETOOLS_SWITCH_LIVE_CDP_URL. Margin is opened after startup via
// the legacy PUT /json/new?<encoded-url> endpoint. The provider is a fake
// persistent session, so this test exercises the real broker, page tools, and
// dynamic SESSION.UPDATE publication without provider credits.
func TestSessionPageToolsSwitchAgainstLiveChrome(t *testing.T) {
	cdpURL := strings.TrimSpace(os.Getenv("WEBMCP_PAGETOOLS_SWITCH_LIVE_CDP_URL"))
	if cdpURL == "" {
		t.Skip("set WEBMCP_PAGETOOLS_SWITCH_LIVE_CDP_URL to an externally launched pinned Chrome /json/version endpoint")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	assertLiveChromeStartupShape(t, ctx, cdpURL)
	openLiveMarginTab(t, ctx, cdpURL)

	browser := config.DefaultBrowserConfig()
	browser.Tools.Enabled = true
	browser.Tools.Backend = config.BrowserToolsBackendWebMCP
	browser.Connection.CDPURL = cdpURL
	browser.Selection.Origin = sessionPageToolsLiveCubecadeOrigin
	browser.Selection.AutoSelect = config.BrowserAutoSelectSingle
	browser.Selection.Persist = false
	browser.Policy.AllowedOrigins = []string{
		sessionPageToolsLiveCubecadeOrigin,
		sessionPageToolsLiveMarginOrigin,
	}
	cfg := &config.Config{
		Model: config.ModelConfig{
			Provider: config.ProviderGrok,
			Grok:     &config.GrokConfig{Model: "session-shape-fake", APIKey: "unused"},
		},
		Browser:   browser,
		ConfigDir: t.TempDir(),
	}
	for _, id := range config.DefaultToolIDs {
		cfg.Tools.List = append(cfg.Tools.List, config.ToolEntry{ID: id, Enabled: id == "exec"})
	}

	capabilities, err := NewSessionToolCapabilitiesFactory(nil, nil)(cfg)
	if err != nil {
		t.Fatalf("capability factory: %v", err)
	}
	defer func() {
		if capabilities.Close != nil {
			if closeErr := capabilities.Close(); closeErr != nil {
				t.Logf("capability close: %v", closeErr)
			}
		}
	}()

	if capabilities.Initialize == nil {
		t.Fatal("production capabilities did not expose session initialization")
	}
	if err := capabilities.Initialize(ctx); err != nil {
		if capabilities.Status != nil {
			status := capabilities.Status()
			t.Logf("bootstrap status: state=%s err=%v", status.State, status.Err)
		}
		t.Fatalf("initialize Cubecade selection: %v", err)
	}

	base := messages.CanonicalToolDefinitions(capabilities.Definitions)
	initialDefinitions, err := capabilities.RefreshDefinitionsWithError(ctx)
	if err != nil {
		t.Fatalf("refresh initial Cubecade definitions: %v", err)
	}
	requireLivePageSurface(t, initialDefinitions, base, []string{"get_cube_state", "queue_cube_moves"}, "Cubecade")

	tabs := waitForLivePageTargets(t, ctx, capabilities.Executor)
	cubeTarget, marginTarget := requireLivePageTargets(t, tabs)
	if cubeTarget.BrowserID != marginTarget.BrowserID {
		t.Fatalf("page targets use different browsers: Cubecade=%q Margin=%q", cubeTarget.BrowserID, marginTarget.BrowserID)
	}
	t.Logf("launch shape: external pinned Chrome, fresh profile, startup_urls=[%s], opened_after_startup=%s via PUT /json/new?<encoded-url>, endpoint_scope=loopback", sessionPageToolsLiveCubecadeURL, sessionPageToolsLiveMarginURL)
	t.Logf("targets: browser=%s Cubecade=%s (%s) Margin=%s (%s)", cubeTarget.BrowserID, cubeTarget.TargetID, cubeTarget.Origin, marginTarget.TargetID, marginTarget.Origin)

	agentBinary := buildLiveAgentCLI(t, ctx)

	providerSession := newSessionPageToolsLiveSession()
	provider := &sessionPageToolsLiveInferencer{session: providerSession}
	sessionCtx, cancelSession := context.WithCancel(ctx)
	runErr := make(chan error, 1)
	go func() {
		runErr <- servicetest.RunSessionWithMaxDuration(sessionCtx, io.Discard, servicetest.SessionRunOptions{
			Provider:               config.ProviderGrok,
			Model:                  "session-shape-fake",
			APIKey:                 "unused",
			LoadedConfig:           cfg,
			BrowserToolsEnabled:    true,
			WaitForClose:           true,
			ToolExecutor:           capabilities.Executor,
			ToolDefinitions:        initialDefinitions,
			ToolDefinitionBase:     base,
			RefreshToolDefinitions: capabilities.RefreshDefinitionsWithError,
			BrowserWatch:           capabilities.BrowserWatch,
			SessionInferencer:      provider,
		}, 4*time.Minute)
	}()
	defer func() {
		cancelSession()
		select {
		case err := <-runErr:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Logf("session shutdown: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("fake provider session did not stop after cancellation")
		}
	}()

	bootstrap := readSessionPageToolsLiveUpdate(t, ctx, runErr, providerSession)
	requireLiveSessionSurface(t, bootstrap, base, []string{"get_cube_state", "queue_cube_moves"}, "Cubecade bootstrap")

	execute := func(name string, args any) webmcp.ToolResultEnvelope {
		t.Helper()
		encoded, marshalErr := json.Marshal(args)
		if marshalErr != nil {
			t.Fatalf("marshal %s arguments: %v", name, marshalErr)
		}
		return executeSessionPageToolsLiveCall(t, ctx, capabilities.Executor, name, string(encoded))
	}

	cubeState := execute("get_cube_state", map[string]any{})
	requireLiveSuccess(t, cubeState, "Cubecade get_cube_state")
	t.Logf("result Cubecade.get_cube_state: ok=%t data_keys=%v", cubeState.OK, liveJSONKeys(cubeState.Data))

	selectMargin := execute(webmcp.SelectTabToolName, map[string]any{
		"browser_id": cubeTarget.BrowserID,
		"target_id":  marginTarget.TargetID,
	})
	requireLiveSuccess(t, selectMargin, "select Margin")
	marginUpdate := readLiveSessionSurface(t, ctx, runErr, providerSession, base, []string{
		"add_comment",
		"create_document",
		"get_document",
		"list_comments",
		"list_documents",
		"open_document",
		"reopen_comment",
		"reply_to_comment",
		"resolve_comment",
		"update_document",
	}, "Margin switch")

	staleCube := execute("get_cube_state", map[string]any{})
	if staleCube.OK || staleCube.Error == nil || staleCube.Error.Code != string(webmcp.ErrorStaleToolRef) {
		t.Fatalf("stale Cubecade tool result = %#v, want stale_tool_ref guidance", staleCube)
	}
	if !strings.Contains(staleCube.Error.Message, webmcp.ListToolsToolName) {
		t.Fatalf("stale Cubecade guidance = %q, want %s recovery hint", staleCube.Error.Message, webmcp.ListToolsToolName)
	}
	t.Logf("stale result Cubecade.get_cube_state while Margin selected: code=%s guidance=true", staleCube.Error.Code)

	token := fmt.Sprintf("%d", time.Now().UnixNano())
	title := "WebMCP switch " + token
	content := "session-shape validation " + token
	created := execute("create_document", map[string]any{
		"title":   title,
		"content": content,
	})
	requireLiveSuccess(t, created, "Margin create_document")
	documentID := liveDocumentID(created.Data)
	if documentID == "" {
		t.Fatalf("Margin create_document returned no document ID: %s", truncateLiveJSON(created.Data, 1200))
	}
	t.Logf("result Margin.create_document: ok=%t document_id=%s", created.OK, documentID)

	gotDocument := execute("get_document", map[string]any{"document_id": documentID})
	requireLiveSuccess(t, gotDocument, "Margin get_document")
	t.Logf("result Margin.get_document: ok=%t data_keys=%v", gotDocument.OK, liveJSONKeys(gotDocument.Data))

	selectCube := execute(webmcp.SelectTabToolName, map[string]any{
		"browser_id": marginTarget.BrowserID,
		"target_id":  cubeTarget.TargetID,
	})
	requireLiveSuccess(t, selectCube, "select Cubecade again")
	finalUpdate := readLiveSessionSurface(t, ctx, runErr, providerSession, base, []string{"get_cube_state", "queue_cube_moves"}, "Cubecade return")
	staleMargin := execute("get_document", map[string]any{})
	if staleMargin.OK || staleMargin.Error == nil || staleMargin.Error.Code != string(webmcp.ErrorStaleToolRef) {
		t.Fatalf("stale Margin tool result = %#v, want stale_tool_ref guidance", staleMargin)
	}
	if !strings.Contains(staleMargin.Error.Message, webmcp.ListToolsToolName) {
		t.Fatalf("stale Margin guidance = %q, want %s recovery hint", staleMargin.Error.Message, webmcp.ListToolsToolName)
	}
	t.Logf("stale result Margin.get_document while Cubecade selected: code=%s guidance=true", staleMargin.Error.Code)
	finalCubeState := execute("get_cube_state", map[string]any{})
	requireLiveSuccess(t, finalCubeState, "Cubecade get_cube_state after return")
	t.Logf("result Cubecade.get_cube_state after return: ok=%t data_keys=%v", finalCubeState.OK, liveJSONKeys(finalCubeState.Data))

	cubeOracle := directLiveCatalog(t, ctx, agentBinary, cdpURL, cubeTarget, []string{"get_cube_state", "queue_cube_moves"})
	logLiveCatalog(t, "Cubecade direct CLI oracle", cubeOracle)
	directCubeState := directLiveInvoke(t, ctx, agentBinary, cdpURL, cubeTarget, findDirectToolRef(t, cubeOracle, "get_cube_state"), map[string]any{})
	requireLiveSuccess(t, directCubeState, "direct CLI Cubecade get_cube_state")
	t.Logf("oracle direct CLI Cubecade.get_cube_state: ok=%t data_keys=%v", directCubeState.OK, liveJSONKeys(directCubeState.Data))

	marginOracle := directLiveCatalog(t, ctx, agentBinary, cdpURL, marginTarget, []string{
		"add_comment",
		"create_document",
		"get_document",
		"list_comments",
		"list_documents",
		"open_document",
		"reopen_comment",
		"reply_to_comment",
		"resolve_comment",
		"update_document",
	})
	logLiveCatalog(t, "Margin direct CLI oracle", marginOracle)
	directDocument := directLiveInvoke(t, ctx, agentBinary, cdpURL, marginTarget, findDirectToolRef(t, marginOracle, "get_document"), map[string]any{"document_id": documentID})
	requireLiveSuccess(t, directDocument, "direct CLI Margin get_document")
	t.Logf("oracle direct CLI Margin.get_document: ok=%t data_keys=%v", directDocument.OK, liveJSONKeys(directDocument.Data))

	if connections := provider.connections(); connections != 1 {
		t.Fatalf("provider connections = %d, want one persistent connection", connections)
	}
	assertLiveChromeStillRunning(t, ctx, cdpURL)
	t.Logf("session shape: provider_connections=%d, definition_transitions=[Cubecade(%d)->Margin(%d)->Cubecade(%d)], external_browser_left_running=true", provider.connections(), len(bootstrap), len(marginUpdate), len(finalUpdate))
}

type sessionPageToolsLiveTarget struct {
	BrowserID string `json:"browser_id"`
	TargetID  string `json:"target_id"`
	Type      string `json:"type"`
	Origin    string `json:"origin"`
	Eligible  bool   `json:"eligible"`
}

type sessionPageToolsLiveTabs struct {
	Targets []sessionPageToolsLiveTarget `json:"targets"`
}

type sessionPageToolsLiveCDPTarget struct {
	Type string `json:"type"`
	URL  string `json:"url"`
}

func assertLiveChromeStartupShape(t *testing.T, ctx context.Context, cdpURL string) {
	t.Helper()
	var targets []sessionPageToolsLiveCDPTarget
	if err := getLiveCDPJSON(ctx, cdpURL, "/json/list", &targets); err != nil {
		t.Fatalf("inspect external Chrome targets: %v", err)
	}
	pageCount := 0
	for _, target := range targets {
		if target.Type != "page" {
			continue
		}
		pageCount++
		if liveURLOrigin(target.URL) != sessionPageToolsLiveCubecadeOrigin {
			t.Fatalf("external Chrome startup page origin = %q, want only %q", liveURLOrigin(target.URL), sessionPageToolsLiveCubecadeOrigin)
		}
	}
	if pageCount != 1 {
		t.Fatalf("external Chrome startup page count = %d, want one Cubecade page", pageCount)
	}
}

func assertLiveChromeStillRunning(t *testing.T, ctx context.Context, cdpURL string) {
	t.Helper()
	var version struct {
		Browser string `json:"Browser"`
	}
	if err := getLiveCDPJSON(ctx, cdpURL, "/json/version", &version); err != nil {
		t.Fatalf("external Chrome stopped before scenario completed: %v", err)
	}
	if strings.TrimSpace(version.Browser) == "" {
		t.Fatalf("external Chrome /json/version omitted Browser identity")
	}
	var targets []sessionPageToolsLiveCDPTarget
	if err := getLiveCDPJSON(ctx, cdpURL, "/json/list", &targets); err != nil {
		t.Fatalf("inspect external Chrome after scenario: %v", err)
	}
	origins := make(map[string]bool)
	for _, target := range targets {
		if target.Type == "page" {
			origins[liveURLOrigin(target.URL)] = true
		}
	}
	if !origins[sessionPageToolsLiveCubecadeOrigin] || !origins[sessionPageToolsLiveMarginOrigin] {
		t.Fatalf("external Chrome lost a scenario page: page_origins=%v", origins)
	}
}

func openLiveMarginTab(t *testing.T, ctx context.Context, cdpURL string) {
	t.Helper()
	endpoint, err := liveCDPEndpoint(cdpURL, "/json/new", sessionPageToolsLiveMarginURL)
	if err != nil {
		t.Fatalf("build legacy Chrome /json/new endpoint: %v", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, nil)
	if err != nil {
		t.Fatalf("create Margin /json/new request: %v", err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("open Margin through /json/new: %v", err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		t.Fatalf("open Margin through /json/new status=%s body=%q", response.Status, string(body))
	}
}

func waitForLivePageTargets(t *testing.T, ctx context.Context, executor messages.ToolExecutor) sessionPageToolsLiveTabs {
	t.Helper()
	deadline := time.NewTimer(2 * time.Minute)
	defer deadline.Stop()
	for {
		callCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
		response, err := executor.Execute(callCtx, messages.ToolCall{
			ID:        "live-list-tabs",
			Name:      webmcp.ListTabsToolName,
			Arguments: `{"eligible_only":true,"include_zero_tool_pages":true}`,
		})
		cancel()
		if err == nil {
			var envelope webmcp.ToolResultEnvelope
			if unmarshalErr := json.Unmarshal([]byte(response.Content), &envelope); unmarshalErr == nil && envelope.OK {
				var tabs sessionPageToolsLiveTabs
				if decodeErr := json.Unmarshal(envelope.Data, &tabs); decodeErr == nil {
					if hasLiveOrigin(tabs.Targets, sessionPageToolsLiveCubecadeOrigin) && hasLiveOrigin(tabs.Targets, sessionPageToolsLiveMarginOrigin) {
						return tabs
					}
				}
			}
		}
		select {
		case <-time.After(2 * time.Second):
		case <-deadline.C:
			t.Fatalf("timed out waiting for eligible Cubecade and Margin targets")
		case <-ctx.Done():
			t.Fatalf("waiting for eligible page targets: %v", ctx.Err())
		}
	}
}

func requireLivePageTargets(t *testing.T, tabs sessionPageToolsLiveTabs) (sessionPageToolsLiveTarget, sessionPageToolsLiveTarget) {
	t.Helper()
	var cube, margin sessionPageToolsLiveTarget
	for _, target := range tabs.Targets {
		if target.Type != "page" || !target.Eligible {
			continue
		}
		switch target.Origin {
		case sessionPageToolsLiveCubecadeOrigin:
			if cube.TargetID != "" {
				t.Fatalf("multiple eligible Cubecade targets: %#v", tabs.Targets)
			}
			cube = target
		case sessionPageToolsLiveMarginOrigin:
			if margin.TargetID != "" {
				t.Fatalf("multiple eligible Margin targets: %#v", tabs.Targets)
			}
			margin = target
		}
	}
	if cube.TargetID == "" || margin.TargetID == "" {
		t.Fatalf("eligible page targets = %#v, want one Cubecade and one Margin", tabs.Targets)
	}
	return cube, margin
}

func hasLiveOrigin(targets []sessionPageToolsLiveTarget, origin string) bool {
	for _, target := range targets {
		if target.Type == "page" && target.Eligible && target.Origin == origin {
			return true
		}
	}
	return false
}

func requireLivePageSurface(t *testing.T, definitions, base []messages.ToolDefinition, want []string, label string) {
	t.Helper()
	if len(definitions) != len(base)+len(want) {
		t.Fatalf("%s definition count = %d, want %d", label, len(definitions), len(base)+len(want))
	}
	if !reflect.DeepEqual(definitions[:len(base)], base) {
		t.Fatalf("%s changed static/stable definition prefix", label)
	}
	got := make([]string, 0, len(want))
	for _, definition := range definitions[len(base):] {
		got = append(got, definition.Name)
	}
	sort.Strings(got)
	want = append([]string(nil), want...)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s page names = %v, want %v", label, got, want)
	}
	t.Logf("definition transition %s: total=%d page_count=%d page_names=%v", label, len(definitions), len(want), want)
}

func executeSessionPageToolsLiveCall(t *testing.T, ctx context.Context, executor messages.ToolExecutor, name, args string) webmcp.ToolResultEnvelope {
	t.Helper()
	response, err := executor.Execute(ctx, messages.ToolCall{ID: "live-" + name, Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("execute %s: %v", name, err)
	}
	envelope, err := webmcp.UnmarshalToolResult([]byte(response.Content))
	if err != nil {
		t.Fatalf("%s result is not a valid envelope: %v; content=%s", name, err, truncateLiveJSON(json.RawMessage(response.Content), 1200))
	}
	return envelope
}

func requireLiveSuccess(t *testing.T, envelope webmcp.ToolResultEnvelope, operation string) {
	t.Helper()
	if !envelope.OK {
		t.Fatalf("%s failed: %+v", operation, envelope.Error)
	}
}

func readSessionPageToolsLiveUpdate(t *testing.T, ctx context.Context, runErr <-chan error, session *sessionPageToolsLiveSession) []messages.ToolDefinition {
	t.Helper()
	for {
		select {
		case message := <-session.sent:
			if message.Type != messages.StreamTypeSessionUpdate {
				continue
			}
			value, ok := message.Value.(*messages.SessionUpdateValue)
			if !ok || value == nil {
				t.Fatalf("provider SESSION.UPDATE value = %T", message.Value)
			}
			return value.Tools
		case err := <-runErr:
			if err == nil {
				t.Fatalf("provider session ended before receiving SESSION.UPDATE")
			}
			t.Fatalf("session ended before receiving SESSION.UPDATE: %v", err)
		case <-ctx.Done():
			t.Fatalf("waiting for provider SESSION.UPDATE: %v", ctx.Err())
		}
	}
}

func requireLiveSessionSurface(t *testing.T, definitions, base []messages.ToolDefinition, want []string, label string) {
	t.Helper()
	if len(definitions) != len(base)+len(want) {
		t.Fatalf("%s provider definition count = %d, want %d", label, len(definitions), len(base)+len(want))
	}
	baseByName := make(map[string]messages.ToolDefinition, len(base))
	for _, definition := range base {
		baseByName[definition.Name] = definition
	}
	got := make([]string, 0, len(want))
	seenBase := make(map[string]bool, len(base))
	for _, definition := range definitions {
		if expected, ok := baseByName[definition.Name]; ok {
			if !reflect.DeepEqual(definition, expected) {
				t.Fatalf("%s changed static/stable provider definition %q: got=%#v want=%#v", label, definition.Name, definition, expected)
			}
			seenBase[definition.Name] = true
			continue
		}
		got = append(got, definition.Name)
	}
	if len(seenBase) != len(base) {
		missing := make([]string, 0, len(base)-len(seenBase))
		for _, definition := range base {
			if !seenBase[definition.Name] {
				missing = append(missing, definition.Name)
			}
		}
		t.Fatalf("%s missing static/stable provider definitions: %v", label, missing)
	}
	sort.Strings(got)
	want = append([]string(nil), want...)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s provider page names = %v, want %v", label, got, want)
	}
}

func readLiveSessionSurface(t *testing.T, ctx context.Context, runErr <-chan error, session *sessionPageToolsLiveSession, base []messages.ToolDefinition, want []string, label string) []messages.ToolDefinition {
	t.Helper()
	waitCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	for {
		definitions := readSessionPageToolsLiveUpdate(t, waitCtx, runErr, session)
		if len(definitions) == len(base) {
			if !sameLiveToolDefinitions(definitions, base) {
				t.Fatalf("%s interim provider surface changed the static/stable definitions", label)
			}
			t.Logf("definition transition %s: provider published the stable base while the page catalog was loading", label)
			continue
		}
		requireLiveSessionSurface(t, definitions, base, want, label)
		return definitions
	}
}

func sameLiveToolDefinitions(got, want []messages.ToolDefinition) bool {
	if len(got) != len(want) {
		return false
	}
	used := make([]bool, len(want))
	for _, definition := range got {
		found := false
		for index, expected := range want {
			if used[index] || !reflect.DeepEqual(definition, expected) {
				continue
			}
			used[index] = true
			found = true
			break
		}
		if !found {
			return false
		}
	}
	return true
}

func buildLiveAgentCLI(t *testing.T, ctx context.Context) string {
	t.Helper()
	root := liveRepositoryRoot(t)
	binary := filepath.Join(t.TempDir(), "agent")
	command := exec.CommandContext(ctx, "go", "build", "-o", binary, "./agent-cli/cmd/agent")
	command.Dir = root
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("build direct CLI: %v\n%s", err, truncateLiveText(output, 2000))
	}
	return binary
}

func directLiveCatalog(t *testing.T, ctx context.Context, binary, cdpURL string, target sessionPageToolsLiveTarget, want []string) WebMCPDirectToolsData {
	t.Helper()
	envelope := runDirectLiveCLI(t, ctx, binary, cdpURL, target, "tools")
	requireLiveSuccess(t, envelope, "direct CLI tools")
	var data WebMCPDirectToolsData
	if err := json.Unmarshal(envelope.Data, &data); err != nil {
		t.Fatalf("decode direct CLI tools: %v", err)
	}
	if data.BrowserID != target.BrowserID || data.TargetID != target.TargetID {
		t.Fatalf("direct CLI catalog target = %s/%s, want %s/%s", data.BrowserID, data.TargetID, target.BrowserID, target.TargetID)
	}
	if data.Generation == 0 {
		t.Fatalf("direct CLI catalog generation = 0, want a connected page generation")
	}
	if len(data.Tools) != len(want) {
		t.Fatalf("direct CLI catalog count = %d, want %d", len(data.Tools), len(want))
	}
	got := make([]string, 0, len(data.Tools))
	for _, tool := range data.Tools {
		got = append(got, tool.Name)
		if !strings.HasPrefix(tool.Ref, "webmcp.tool-ref.v1:") {
			t.Fatalf("direct CLI tool %q ref = %q, want webmcp.tool-ref.v1", tool.Name, tool.Ref)
		}
		if len(bytes.TrimSpace(tool.InputSchema)) == 0 || bytes.Equal(bytes.TrimSpace(tool.InputSchema), []byte("null")) || !json.Valid(tool.InputSchema) {
			t.Fatalf("direct CLI tool %q schema = %q, want valid non-null JSON schema", tool.Name, truncateLiveJSON(tool.InputSchema, 500))
		}
	}
	sort.Strings(got)
	want = append([]string(nil), want...)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("direct CLI catalog names = %v, want %v", got, want)
	}
	return data
}

func logLiveCatalog(t *testing.T, label string, data WebMCPDirectToolsData) {
	t.Helper()
	entries := make([]string, 0, len(data.Tools))
	for _, tool := range data.Tools {
		entries = append(entries, tool.Name+"="+tool.Ref)
	}
	t.Logf("%s: generation=%d count=%d tools=%s", label, data.Generation, len(data.Tools), strings.Join(entries, ","))
}

func findDirectToolRef(t *testing.T, data WebMCPDirectToolsData, name string) string {
	t.Helper()
	for _, tool := range data.Tools {
		if tool.Name == name {
			return tool.Ref
		}
	}
	t.Fatalf("direct CLI catalog has no %q", name)
	return ""
}

func directLiveInvoke(t *testing.T, ctx context.Context, binary, cdpURL string, target sessionPageToolsLiveTarget, toolRef string, input any) webmcp.ToolResultEnvelope {
	t.Helper()
	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal direct CLI input: %v", err)
	}
	return runDirectLiveCLI(t, ctx, binary, cdpURL, target, "invoke", "--tool-ref", toolRef, "--input-json", string(encoded), "--timeout", "90s", "--invocation-timeout", "120s")
}

func runDirectLiveCLI(t *testing.T, parent context.Context, binary, cdpURL string, target sessionPageToolsLiveTarget, operation string, operationArgs ...string) webmcp.ToolResultEnvelope {
	t.Helper()
	commandCtx, cancel := context.WithTimeout(parent, 150*time.Second)
	defer cancel()
	configDir := t.TempDir()
	args := []string{
		"-C", configDir, "webmcp", operation,
	}
	args = append(args, operationArgs...)
	args = append(args,
		"--cdp-url", cdpURL,
		"--browser", target.BrowserID,
		"--tab", target.TargetID,
		"--command-timeout", "120s",
		"--json",
	)
	command := exec.CommandContext(commandCtx, binary, args...)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	if err != nil {
		t.Fatalf("direct CLI %s: %v\nstdout=%s\nstderr=%s", operation, err, truncateLiveText(stdout.Bytes(), 2000), truncateLiveText(stderr.Bytes(), 2000))
	}
	envelope, err := webmcp.UnmarshalToolResult(bytes.TrimSpace(stdout.Bytes()))
	if err != nil {
		t.Fatalf("decode direct CLI %s result: %v\nstdout=%s\nstderr=%s", operation, err, truncateLiveText(stdout.Bytes(), 2000), truncateLiveText(stderr.Bytes(), 2000))
	}
	return envelope
}

func liveRepositoryRoot(t *testing.T) string {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatalf("get test working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(directory, "go.work")); err == nil {
			return directory
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			break
		}
		directory = parent
	}
	t.Fatal("could not locate repository go.work")
	return ""
}

func getLiveCDPJSON(ctx context.Context, cdpURL, path string, target any) error {
	endpoint, err := liveCDPEndpoint(cdpURL, path, "")
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("status %s", response.Status)
	}
	return json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(target)
}

func liveCDPEndpoint(cdpURL, path, queryTarget string) (string, error) {
	endpoint, err := url.Parse(cdpURL)
	if err != nil {
		return "", err
	}
	basePath := strings.TrimSuffix(strings.TrimRight(endpoint.Path, "/"), "/json/version")
	endpoint.Path = strings.TrimRight(basePath, "/") + path
	endpoint.RawPath = ""
	endpoint.Fragment = ""
	if queryTarget == "" {
		endpoint.RawQuery = ""
	} else {
		endpoint.RawQuery = url.QueryEscape(queryTarget)
	}
	return endpoint.String(), nil
}

func liveURLOrigin(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return parsed.Scheme + "://" + parsed.Host
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
		for key, value := range typed {
			if keys[key] {
				if stringValue, ok := value.(string); ok && stringValue != "" {
					return stringValue
				}
			}
		}
		for _, value := range typed {
			if found := findLiveString(value, keys); found != "" {
				return found
			}
		}
	case []any:
		for _, value := range typed {
			if found := findLiveString(value, keys); found != "" {
				return found
			}
		}
	}
	return ""
}

func liveJSONKeys(raw json.RawMessage) []string {
	var value map[string]json.RawMessage
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func truncateLiveJSON(raw json.RawMessage, limit int) string {
	return truncateLiveText(raw, limit)
}

func truncateLiveText(raw []byte, limit int) string {
	text := string(raw)
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "…"
}

type sessionPageToolsLiveInferencer struct {
	mu      sync.Mutex
	session *sessionPageToolsLiveSession
	connect int
}

func (i *sessionPageToolsLiveInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	i.mu.Lock()
	i.connect++
	session := i.session
	i.mu.Unlock()
	if !session.recv.Write(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeSessionOpen,
		Value: messages.NewSessionOpenValue("live-session-shape", "fake"),
	}) {
		return nil, ctx.Err()
	}
	if !session.recv.Write(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeSessionCreated,
		Value: messages.NewSessionCreatedValue("live-session-shape", "session-shape-fake"),
	}) {
		return nil, ctx.Err()
	}
	return session, nil
}

func (i *sessionPageToolsLiveInferencer) connections() int {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.connect
}

type sessionPageToolsLiveSession struct {
	recv      *messages.TypedBuffer[messages.StreamMessage]
	sent      chan messages.StreamMessage
	done      chan struct{}
	closeOnce sync.Once
}

func newSessionPageToolsLiveSession() *sessionPageToolsLiveSession {
	return &sessionPageToolsLiveSession{
		recv: messages.NewTypedBuffer[messages.StreamMessage](16),
		sent: make(chan messages.StreamMessage, 32),
		done: make(chan struct{}),
	}
}

func (s *sessionPageToolsLiveSession) Send(ctx context.Context, message messages.StreamMessage) bool {
	select {
	case s.sent <- message:
		return true
	case <-ctx.Done():
		return false
	}
}

func (s *sessionPageToolsLiveSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.recv
}

func (s *sessionPageToolsLiveSession) Done() <-chan struct{} { return s.done }

func (s *sessionPageToolsLiveSession) Close() error {
	s.closeOnce.Do(func() { close(s.done) })
	return nil
}

var _ messages.SessionInferencer = (*sessionPageToolsLiveInferencer)(nil)
var _ messages.Session = (*sessionPageToolsLiveSession)(nil)
