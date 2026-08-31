package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/testkit"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

const (
	queryParityToolName = "list_documents"
	queryParityFrame    = webmcp.FrameID("frame-margin")
	queryParityBrowser  = webmcp.BrowserID("browser-margin")
	queryParityTarget   = webmcp.TargetID("tab-margin")
)

type queryParityFixture struct {
	broker    *webmcp.StatefulBroker
	runtime   *testkit.ScriptedBrowserRuntime
	session   *testkit.ScriptedTargetSession
	candidate webmcp.BrowserCandidate
	target    webmcp.Target
	tool      webmcp.ToolDescriptor
	ref       webmcp.ToolRef
}

type queryParityLiveResult struct {
	response messages.ToolCallResponse
	err      error
}

// queryParityDirectBroker lets the direct command exercise its normal
// cleanup hook without retiring the shared fixture broker before the test can
// inspect the unchanged page selection. Optional broker methods, including
// WaitInvocation, remain promoted from the embedded StatefulBroker.
type queryParityDirectBroker struct {
	*webmcp.StatefulBroker
}

func (queryParityDirectBroker) Close() error { return nil }

// TestWebMCPLiveSessionAndDirectQueryPayloadParity is the mandatory hermetic
// production-shaped regression for the live-session false-negative. It uses
// the first-class session executor and the real direct CLI invoke command
// against one scripted target and one unchanged page generation. The stale
// terminal subtest proves the live provider boundary receives an actionable
// freshness failure instead of an empty success, while the follow-up direct
// call proves the target remains usable.
func TestWebMCPLiveSessionAndDirectQueryPayloadParity(t *testing.T) {
	t.Run("fresh_nonempty", func(t *testing.T) {
		fixture := newQueryParityFixture(t)
		live := fixture.runLiveQuery(t, "live-list-documents", `{"count":1,"documents":[{"id":"welcome-to-margin","title":"Welcome to Margin"}]}`)
		direct := fixture.runDirectQuery(t, "direct-list-documents", `{"count":1,"documents":[{"id":"welcome-to-margin","title":"Welcome to Margin"}]}`)

		liveOutput := decodeLiveQueryOutput(t, live, fixture.ref)
		directOutput := decodeDirectQueryOutput(t, direct, fixture.ref)
		assertWelcomeDocument(t, liveOutput)
		assertWelcomeDocument(t, directOutput)
		if !jsonEqual(liveOutput, directOutput) {
			t.Fatalf("live/direct decoded page payloads differ: live=%s direct=%s", liveOutput, directOutput)
		}
		fixture.assertUnchangedSelection(t)
		fixture.assertOneTerminalPerInvocation(t, 2)
	})

	t.Run("fresh_empty", func(t *testing.T) {
		fixture := newQueryParityFixture(t)
		live := fixture.runLiveQuery(t, "live-empty-documents", `{"count":0,"documents":[]}`)
		direct := fixture.runDirectQuery(t, "direct-empty-documents", `{"count":0,"documents":[]}`)

		liveOutput := decodeLiveQueryOutput(t, live, fixture.ref)
		directOutput := decodeDirectQueryOutput(t, direct, fixture.ref)
		var livePayload, directPayload documentListPayload
		decodeDocumentList(t, liveOutput, &livePayload)
		decodeDocumentList(t, directOutput, &directPayload)
		if livePayload.Count != 0 || len(livePayload.Documents) != 0 || directPayload.Count != 0 || len(directPayload.Documents) != 0 {
			t.Fatalf("fresh empty payloads = live=%+v direct=%+v, want two genuine empty results", livePayload, directPayload)
		}
		if !jsonEqual(liveOutput, directOutput) {
			t.Fatalf("live/direct decoded empty payloads differ: live=%s direct=%s", liveOutput, directOutput)
		}
		fixture.assertUnchangedSelection(t)
		fixture.assertOneTerminalPerInvocation(t, 2)
	})

	t.Run("stale_terminal_fails_closed_and_followup_works", func(t *testing.T) {
		fixture := newQueryParityFixture(t)
		staleOutput := json.RawMessage(`{"count":0,"documents":[]}`)
		if err := fixture.session.Emit(webmcp.BrowserEvent{
			Type:         webmcp.EventToolResponded,
			InvocationID: webmcp.InvocationID("inv-000001"),
			Status:       "Completed",
			Output:       staleOutput,
			Generation:   fixture.selectedGeneration(t),
		}); err != nil {
			t.Fatalf("inject stale terminal: %v", err)
		}

		live := fixture.runLiveQueryWithoutTerminal(t, "live-stale-documents")
		if live.response.ToolCallID != "live-stale-documents" || live.response.Name != queryParityToolName {
			t.Fatalf("stale live response correlation = %+v, want original call ID/name", live.response)
		}
		envelope, err := webmcp.UnmarshalToolResult([]byte(live.response.Content))
		if err != nil {
			t.Fatalf("decode stale live envelope: %v; content=%s", err, live.response.Content)
		}
		if envelope.OK || envelope.Error == nil || envelope.Error.Code != string(webmcp.ErrorInvocationFailed) || !envelope.Error.Retryable {
			t.Fatalf("stale live envelope = %#v, want retryable invocation_failed", envelope)
		}
		if envelope.Error.Details["phase"] != "result_freshness" || envelope.Error.Details["freshness_phase"] != "terminal_provenance" || envelope.Error.Details["reason_code"] != "terminal_before_invocation" {
			t.Fatalf("stale live error details = %#v, want terminal freshness classification", envelope.Error.Details)
		}
		if !jsonEqual(envelope.Data, []byte("null")) || strings.Contains(live.response.Content, string(staleOutput)) {
			t.Fatalf("stale live response returned or leaked the empty page payload: %s", live.response.Content)
		}

		// The failed live call must not poison the unchanged selected page or
		// prevent the direct adapter from serving the current non-empty result.
		direct := fixture.runDirectQuery(t, "direct-after-stale", `{"count":1,"documents":[{"id":"welcome-to-margin","title":"Welcome to Margin"}]}`)
		directOutput := decodeDirectQueryOutput(t, direct, fixture.ref)
		assertWelcomeDocument(t, directOutput)
		fixture.assertUnchangedSelection(t)
		fixture.assertOneTerminalPerInvocation(t, 2)
	})
}

func newQueryParityFixture(t *testing.T) queryParityFixture {
	return newQueryParityFixtureWithTool(t, webmcp.ToolDescriptor{
		Name:        queryParityToolName,
		Description: "List documents in the current Margin page.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
		FrameID:     queryParityFrame,
	})
}

func newQueryParityFixtureWithTool(t *testing.T, tool webmcp.ToolDescriptor) queryParityFixture {
	t.Helper()
	clock := testkit.NewFakeClock(time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC))
	ids := testkit.NewDeterministicIDs()
	candidate := webmcp.BrowserCandidate{ID: queryParityBrowser, Product: "fixture", Loopback: true}
	target := webmcp.Target{
		BrowserID: candidate.ID,
		ID:        queryParityTarget,
		Type:      "page",
		Title:     "Margin",
		URL:       "https://margin.fixture/",
		Origin:    "https://margin.fixture",
		Eligible:  true,
	}
	if tool.Name == "" {
		t.Fatal("query parity fixture tool name is empty")
	}
	if tool.FrameID == "" {
		tool.FrameID = queryParityFrame
	}
	if tool.Origin == "" {
		tool.Origin = target.Origin
	}
	if len(tool.InputSchema) == 0 {
		tool.InputSchema = json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
	}
	if tool.Annotations.ReadOnly == nil {
		readOnly := true
		tool.Annotations.ReadOnly = &readOnly
	}
	runtime := testkit.NewScriptedBrowserRuntimeWithOptions(
		testkit.RuntimeOptions{Clock: clock, IDs: ids},
		testkit.BrowserConfig{
			Candidate: candidate,
			Targets: []testkit.TargetConfig{testkit.NewTargetConfig(
				target,
				testkit.WithContext(webmcp.PageContext{
					Generation:      1,
					CatalogReady:    true,
					CatalogEvidence: "scripted_fixture",
				}),
				testkit.WithInitialCatalog(tool),
			)},
		},
	)
	broker := webmcp.NewBroker(webmcp.BrokerOptions{
		Runtime:        runtime,
		Discoverer:     directDiscoverer{candidates: []webmcp.BrowserCandidate{candidate}},
		IDs:            ids,
		Clock:          clock,
		ToolRefFactory: webmcp.StableToolRef,
	})
	t.Cleanup(func() {
		_ = broker.Close()
		_ = runtime.Close()
	})
	selected, err := broker.Select(context.Background(), webmcp.TargetSelector{BrowserID: candidate.ID, TargetID: target.ID})
	if err != nil {
		t.Fatalf("select query parity target: %v", err)
	}
	catalog, err := broker.ListTools(context.Background(), webmcp.ListToolsOptions{IncludeSchemas: true})
	if err != nil {
		t.Fatalf("resolve query parity catalog: %v", err)
	}
	if len(catalog.Tools) != 1 || catalog.Tools[0].Name != tool.Name || catalog.Tools[0].Ref == "" {
		t.Fatalf("query parity catalog = %#v, want one list_documents ref", catalog.Tools)
	}
	session := runtime.Browser(candidate.ID).TargetSession(target.ID)
	if session == nil {
		t.Fatal("query parity target session is nil")
	}
	session.BlockInvocations()
	if selected.Generation != 1 {
		t.Fatalf("query parity selection generation = %d, want 1", selected.Generation)
	}
	return queryParityFixture{
		broker:    broker,
		runtime:   runtime,
		session:   session,
		candidate: candidate,
		target:    target,
		tool:      tool,
		ref:       catalog.Tools[0].Ref,
	}
}

func (f queryParityFixture) liveExecutor(t *testing.T) messages.ToolExecutor {
	t.Helper()
	capabilityConfig := browserCapabilityConfig(true)
	capabilityConfig.Browser.Connection.CDPURL = "http://127.0.0.1:9222"
	capabilityConfig.Browser.Selection.Persist = false
	capabilityFactory := NewSessionToolCapabilitiesFactory(nil, func(config.BrowserConfig) (webmcp.Broker, error) {
		return f.broker, nil
	})
	capabilities, err := capabilityFactory(capabilityConfig)
	if err != nil {
		t.Fatalf("construct live session capabilities: %v", err)
	}
	pageDefinitions := capabilities.RefreshDefinitions(context.Background())
	found := false
	for _, definition := range pageDefinitions {
		if definition.Name == f.tool.Name {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("live session definitions = %#v, want %q", pageDefinitions, f.tool.Name)
	}
	if capabilities.Executor == nil {
		t.Fatal("live session executor is nil")
	}
	return capabilities.Executor
}

func (f queryParityFixture) runLiveQuery(t *testing.T, callID, output string) queryParityLiveResult {
	t.Helper()
	return f.runLiveQueryWithInputAndTerminal(t, callID, `{}`, output, true)
}

func (f queryParityFixture) runLiveQueryWithoutTerminal(t *testing.T, callID string) queryParityLiveResult {
	t.Helper()
	return f.runLiveQueryWithInputAndTerminal(t, callID, `{}`, "", false)
}

func (f queryParityFixture) runLiveQueryWithTerminal(t *testing.T, callID, output string, releaseTerminal bool) queryParityLiveResult {
	t.Helper()
	return f.runLiveQueryWithInputAndTerminal(t, callID, `{}`, output, releaseTerminal)
}

func (f queryParityFixture) runLiveQueryWithInput(t *testing.T, callID, input, output string) queryParityLiveResult {
	t.Helper()
	return f.runLiveQueryWithInputAndTerminal(t, callID, input, output, true)
}

func (f queryParityFixture) runLiveQueryWithInputAndTerminal(t *testing.T, callID, input, output string, releaseTerminal bool) queryParityLiveResult {
	t.Helper()
	callContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resultCh := make(chan queryParityLiveResult, 1)
	executor := f.liveExecutor(t)
	go func() {
		response, err := executor.Execute(callContext, messages.ToolCall{
			ID:        callID,
			Name:      f.tool.Name,
			Arguments: input,
		})
		resultCh <- queryParityLiveResult{response: response, err: err}
	}()

	invocation, err := f.session.WaitForInvocation(callContext)
	if err != nil {
		t.Fatalf("wait for live target invocation: %v", err)
	}
	f.assertInvocationInput(t, invocation, []byte(input))
	if releaseTerminal {
		if err := f.session.ReleaseInvocation(invocation.ID, json.RawMessage(output)); err != nil {
			t.Fatalf("release live target invocation %q: %v", invocation.ID, err)
		}
	}
	select {
	case result := <-resultCh:
		if result.err != nil {
			t.Fatalf("live query %q: %v", callID, result.err)
		}
		if result.response.ToolCallID != callID || result.response.Name != f.tool.Name || len(result.response.ContentParts) != 0 {
			t.Fatalf("live query response = %+v, want one text response correlated to %q/%q", result.response, callID, f.tool.Name)
		}
		return result
	case <-callContext.Done():
		t.Fatalf("live query %q did not return: %v", callID, callContext.Err())
		return queryParityLiveResult{}
	}
}

func (f queryParityFixture) runDirectQuery(t *testing.T, callID, output string) directCommandResult {
	t.Helper()
	return f.runDirectQueryWithInput(t, callID, `{}`, output)
}

func (f queryParityFixture) runDirectQueryWithInput(t *testing.T, callID, input, output string) directCommandResult {
	t.Helper()
	configDir := writeDirectConfig(t, "")
	callContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resultCh := make(chan directCommandResult, 1)
	go func() {
		resultCh <- executeDirectCommandContext(t, callContext, configDir, nil, directFactory(queryParityDirectBroker{StatefulBroker: f.broker}),
			"invoke",
			"--browser", string(f.candidate.ID),
			"--tab", string(f.target.ID),
			"--tool-ref", string(f.ref),
			"--input-json", input,
			"--reason", "query parity regression",
			"--timeout", "5s",
			"--json",
		)
	}()

	invocation, err := f.session.WaitForInvocation(callContext)
	if err != nil {
		result := <-resultCh
		t.Fatalf("wait for direct target invocation: %v; command err=%v stdout=%s stderr=%s", err, result.err, result.stdout, result.stderr)
	}
	f.assertInvocationInput(t, invocation, []byte(input))
	if err := f.session.ReleaseInvocation(invocation.ID, json.RawMessage(output)); err != nil {
		t.Fatalf("release direct target invocation %q: %v", invocation.ID, err)
	}
	select {
	case result := <-resultCh:
		if result.err != nil {
			t.Fatalf("direct query %q: %v\nstdout=%s\nstderr=%s", callID, result.err, result.stdout, result.stderr)
		}
		return result
	case <-callContext.Done():
		t.Fatalf("direct query %q did not return: %v", callID, callContext.Err())
		return directCommandResult{}
	}
}

func (f queryParityFixture) assertInvocation(t *testing.T, invocation testkit.InvocationRecord) {
	t.Helper()
	f.assertInvocationInput(t, invocation, []byte(`{}`))
}

func (f queryParityFixture) assertInvocationInput(t *testing.T, invocation testkit.InvocationRecord, input []byte) {
	t.Helper()
	if invocation.BrowserID != f.candidate.ID || invocation.TargetID != f.target.ID || invocation.Generation != 1 || invocation.FrameID != f.tool.FrameID || invocation.ToolName != f.tool.Name || !jsonEqual(invocation.Input, input) {
		t.Fatalf("target invocation = %+v, want exact browser/target/generation/frame/tool/input provenance", invocation)
	}
}

func (f queryParityFixture) selectedGeneration(t *testing.T) uint64 {
	t.Helper()
	selected, err := f.broker.Selected(context.Background())
	if err != nil {
		t.Fatalf("read selected generation: %v", err)
	}
	return selected.Generation
}

func (f queryParityFixture) assertUnchangedSelection(t *testing.T) {
	t.Helper()
	selected, err := f.broker.Selected(context.Background())
	if err != nil {
		t.Fatalf("read selection after parity calls: %v", err)
	}
	if selected.Key.BrowserID != f.candidate.ID || selected.Key.TargetID != f.target.ID || selected.Generation != 1 {
		t.Fatalf("selection after parity calls = %+v, want unchanged target generation 1", selected)
	}
	if pending := f.broker.PendingInvocations(); len(pending) != 0 {
		t.Fatalf("broker pending invocations after parity calls = %#v, want none", pending)
	}
}

func (f queryParityFixture) assertOneTerminalPerInvocation(t *testing.T, expected int) {
	t.Helper()
	counts := make(map[webmcp.InvocationID]int)
	for _, publication := range f.runtime.PublishedEvents() {
		if publication.Event.Type == webmcp.EventToolResponded {
			counts[publication.Event.InvocationID]++
		}
	}
	if len(counts) != expected {
		t.Fatalf("terminal browser events = %#v, want %d invocation terminals", counts, expected)
	}
	for id, count := range counts {
		if count != 1 {
			t.Fatalf("terminal browser events for %q = %d, want exactly one", id, count)
		}
	}
}

type documentListPayload struct {
	Count     int `json:"count"`
	Documents []struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	} `json:"documents"`
}

func decodeLiveQueryOutput(t *testing.T, response queryParityLiveResult, ref webmcp.ToolRef) json.RawMessage {
	t.Helper()
	envelope, err := webmcp.UnmarshalToolResult([]byte(response.response.Content))
	if err != nil {
		t.Fatalf("decode live query envelope: %v; content=%s", err, response.response.Content)
	}
	if !envelope.OK {
		t.Fatalf("live query failed: %+v", envelope.Error)
	}
	var data struct {
		InvocationID string          `json:"invocation_id"`
		ToolRef      webmcp.ToolRef  `json:"tool_ref"`
		Status       string          `json:"status"`
		Output       json.RawMessage `json:"output"`
	}
	if err := json.Unmarshal(envelope.Data, &data); err != nil {
		t.Fatalf("decode live invocation data: %v", err)
	}
	if data.InvocationID == "" || data.ToolRef != ref || data.Status != string(webmcp.InvocationCompleted) {
		t.Fatalf("live invocation data = %+v, want completed result for ref %q", data, ref)
	}
	if len(data.Output) == 0 || !json.Valid(data.Output) {
		t.Fatalf("live query output = %s, want one JSON page payload", data.Output)
	}
	return data.Output
}

func decodeDirectQueryOutput(t *testing.T, result directCommandResult, ref webmcp.ToolRef) json.RawMessage {
	t.Helper()
	envelope := requireDirectSuccess(t, result)
	var data WebMCPDirectInvocation
	decodeDirectData(t, envelope.Data, &data)
	if data.InvocationID == "" || data.ToolRef != string(ref) || data.Status != string(webmcp.InvocationCompleted) || len(data.Output) == 0 || !json.Valid(data.Output) {
		t.Fatalf("direct query data = %+v, want completed result for ref %q", data, ref)
	}
	return data.Output
}

func decodeDocumentList(t *testing.T, raw json.RawMessage, payload *documentListPayload) {
	t.Helper()
	if err := json.Unmarshal(raw, payload); err != nil {
		t.Fatalf("decode document list payload: %v; payload=%s", err, raw)
	}
}

func assertWelcomeDocument(t *testing.T, raw json.RawMessage) {
	t.Helper()
	var payload documentListPayload
	decodeDocumentList(t, raw, &payload)
	if payload.Count != 1 || len(payload.Documents) != 1 || payload.Documents[0].ID != "welcome-to-margin" || payload.Documents[0].Title != "Welcome to Margin" {
		t.Fatalf("document list payload = %+v, want count 1 and Welcome to Margin", payload)
	}
}
