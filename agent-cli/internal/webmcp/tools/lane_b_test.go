package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/discovery"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestStableLaneBDefinitionsAreClosedAndHaveFrozenDefaults(t *testing.T) {
	definitions := StableToolDefinitions()
	wantNames := []string{GetContextToolName, ListTabsToolName, SelectTabToolName}
	if len(definitions) != len(wantNames) {
		t.Fatalf("definition count = %d, want %d", len(definitions), len(wantNames))
	}
	for index, definition := range definitions {
		if definition.Name != wantNames[index] {
			t.Fatalf("definition %d name = %q, want %q", index, definition.Name, wantNames[index])
		}
		if definition.Parameters["type"] != "object" || definition.Parameters["additionalProperties"] != false {
			t.Fatalf("%s schema is not a closed object: %#v", definition.Name, definition.Parameters)
		}
		properties, ok := definition.Parameters["properties"].(map[string]any)
		if !ok {
			t.Fatalf("%s properties = %#v, want object", definition.Name, definition.Parameters["properties"])
		}
		for name, value := range properties {
			property, ok := value.(map[string]any)
			if !ok || property["type"] != "string" && property["type"] != "boolean" {
				t.Fatalf("%s.%s is not a scalar property: %#v", definition.Name, name, value)
			}
		}
	}

	listProperties := StableToolDefinitions()[1].Parameters["properties"].(map[string]any)
	defaults := map[string]any{
		"browser_id":              "",
		"origin_contains":         "",
		"eligible_only":           true,
		"include_zero_tool_pages": false,
	}
	for name, want := range defaults {
		property := listProperties[name].(map[string]any)
		if property["default"] != want {
			t.Fatalf("list %s default = %#v, want %#v", name, property["default"], want)
		}
	}
	selectDefinition := definitions[2]
	required, ok := selectDefinition.Parameters["required"].([]string)
	if !ok || !equalStrings(required, []string{"browser_id", "target_id"}) {
		t.Fatalf("select required = %#v, want browser_id,target_id", selectDefinition.Parameters["required"])
	}
	activate := selectDefinition.Parameters["properties"].(map[string]any)["activate"].(map[string]any)
	if activate["default"] != false {
		t.Fatalf("activate default = %#v, want false", activate["default"])
	}

	// Fresh schemas prevent a provider adapter from changing later calls.
	first := StableToolSchemas()
	first[0]["function"].(map[string]any)["parameters"].(map[string]any)["additionalProperties"] = true
	if StableToolSchemas()[0]["function"].(map[string]any)["parameters"].(map[string]any)["additionalProperties"] != false {
		t.Fatal("stable schemas share mutable state")
	}
}

func TestLaneBToolExecutorGoldenSuccessAndCorrelation(t *testing.T) {
	browser := discovery.BrowserCandidate{ID: "browser-a", Product: "Chrome/Test", Protocol: "1.3", Source: discovery.SourceConfigured, Loopback: true}
	target := discovery.Target{
		BrowserID:      browser.ID,
		ID:             "target-a",
		Type:           "page",
		Title:          "Orders",
		URL:            "https://example.test/orders",
		Origin:         "https://example.test",
		Generation:     3,
		WebMCP:         true,
		WebMCPKnown:    true,
		ToolCount:      2,
		ToolCountKnown: true,
		Eligible:       true,
	}
	selected := discovery.Selection{
		BrowserID:  browser.ID,
		TargetID:   target.ID,
		Title:      target.Title,
		URL:        target.URL,
		Origin:     target.Origin,
		Generation: target.Generation,
		Target:     target,
	}
	fake := &fakeDiscovery{
		candidates: []discovery.BrowserCandidate{browser},
		selected:   selected,
		selectedOK: true,
		snapshots: map[string]discovery.TargetSnapshot{
			browser.ID: {Browsers: []discovery.BrowserCandidate{browser}, Targets: []discovery.Target{target}, CandidateCount: 1, EligibleCount: 1},
		},
	}
	set := New(Options{
		Service:       fake,
		Inputs:        discovery.ConnectionInputs{AllowRemoteCDP: false},
		PendingCount:  func() int { return 2 },
		PolicySummary: map[string]any{"approval": "never"},
	})
	executor := set.Executor()

	response, err := executor.Execute(context.Background(), messages.ToolCall{
		ID:        "call-context",
		Name:      GetContextToolName,
		Arguments: `{}`,
	})
	if err != nil {
		t.Fatalf("get context: %v", err)
	}
	assertLaneBTextualResponse(t, response, "call-context", GetContextToolName)
	wantContext := `{"version":"webmcp.tool-result.v1","ok":true,"data":{"browser_id":"browser-a","browser_product":"Chrome/Test","target_id":"target-a","title":"Orders","url":"https://example.test/orders","origin":"https://example.test","generation":3,"connected":true,"ready":true,"catalog_ready":true,"tool_count":2,"tool_count_known":true,"pending_count":2,"policy_summary":{"approval":"never"}},"error":null}`
	if response.Content != wantContext {
		t.Fatalf("context golden = %s, want %s", response.Content, wantContext)
	}

	response, err = executor.Execute(context.Background(), messages.ToolCall{
		ID:        "call-list",
		Name:      ListTabsToolName,
		Arguments: `{}`,
	})
	if err != nil {
		t.Fatalf("list tabs: %v", err)
	}
	assertLaneBTextualResponse(t, response, "call-list", ListTabsToolName)
	wantList := `{"version":"webmcp.tool-result.v1","ok":true,"data":{"browsers":[{"browser_id":"browser-a","product":"Chrome/Test","protocol":"1.3"}],"targets":[{"browser_id":"browser-a","target_id":"target-a","type":"page","title":"Orders","url":"https://example.test/orders","origin":"https://example.test","generation":3,"webmcp":true,"tool_count":2,"tool_count_known":true,"eligible":true}],"candidate_count":1,"eligible_count":1,"filters":{"browser_id":"","origin_contains":"","eligible_only":true,"include_zero_tool_pages":false}},"error":null}`
	if response.Content != wantList {
		t.Fatalf("list golden = %s, want %s", response.Content, wantList)
	}

	response, err = executor.Execute(context.Background(), messages.ToolCall{
		ID:        "call-select",
		Name:      SelectTabToolName,
		Arguments: `{"browser_id":"browser-a","target_id":"target-a"}`,
	})
	if err != nil {
		t.Fatalf("select tab: %v", err)
	}
	assertLaneBTextualResponse(t, response, "call-select", SelectTabToolName)
	if response.Content != wantContext {
		t.Fatalf("select context golden = %s, want %s", response.Content, wantContext)
	}
	if len(fake.selectRequests) != 1 || fake.selectRequests[0].Activate {
		t.Fatalf("selection requests = %#v, want one non-activating request", fake.selectRequests)
	}
}

func TestLaneBInputValidationHasNoDiscoverySideEffects(t *testing.T) {
	fake := &fakeDiscovery{}
	executor := New(Options{Service: fake}).Executor()
	cases := []struct {
		name      string
		arguments string
		path      string
		code      string
	}{
		{GetContextToolName, `{"refresh":false,"secret":"not returned"}`, "/secret", "unknown_property"},
		{ListTabsToolName, `[]`, "/", "object_required"},
		{SelectTabToolName, `{"browser_id":"browser-a"}`, "/target_id", "required"},
		{SelectTabToolName, `{"browser_id":"bad/id","target_id":"target-a"}`, "/browser_id", "invalid_identifier"},
		{GetContextToolName, `{"refresh":false}{}`, "/", "multiple_json_values"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name+testCase.code, func(t *testing.T) {
			before := fake.callCount()
			response, err := executor.Execute(context.Background(), messages.ToolCall{ID: "invalid", Name: testCase.name, Arguments: testCase.arguments})
			if err != nil {
				t.Fatalf("execute: %v", err)
			}
			assertLaneBTextualResponse(t, response, "invalid", testCase.name)
			if fake.callCount() != before {
				t.Fatalf("discovery calls changed from %d to %d", before, fake.callCount())
			}
			envelope, err := UnmarshalToolResult([]byte(response.Content))
			if err != nil {
				t.Fatalf("decode result: %v", err)
			}
			if envelope.OK || envelope.Error == nil || envelope.Error.Code != string(ErrorInvalidToolInput) {
				t.Fatalf("envelope = %#v, want invalid_tool_input", envelope)
			}
			var details struct {
				Issues []ToolResultIssue `json:"issues"`
			}
			if err := json.Unmarshal(mustJSON(t, envelope.Error.Details["issues"]), &details.Issues); err != nil {
				t.Fatalf("decode issues: %v", err)
			}
			found := false
			for _, issue := range details.Issues {
				if issue.Path == testCase.path && issue.Code == testCase.code {
					found = true
				}
			}
			if !found {
				t.Fatalf("issues = %#v, want %s/%s", details.Issues, testCase.path, testCase.code)
			}
			if strings.Contains(response.Content, "not returned") || strings.Contains(response.Content, "bad/id") {
				t.Fatalf("invalid input echoed into result: %s", response.Content)
			}
		})
	}
}

func TestLaneBOutputReappliesSafePageMetadataBoundary(t *testing.T) {
	browser := discovery.BrowserCandidate{ID: "browser-a", Product: "Chrome/Test", Protocol: "1.3"}
	target := discovery.Target{
		BrowserID:      browser.ID,
		ID:             "target-a",
		Type:           "page",
		Title:          "Orders",
		URL:            "https://user:pass@example.test/orders?token=secret#fragment",
		Origin:         "https://user:pass@example.test",
		Generation:     1,
		WebMCP:         true,
		WebMCPKnown:    true,
		ToolCount:      1,
		ToolCountKnown: true,
		Eligible:       true,
	}
	fake := &fakeDiscovery{
		candidates: []discovery.BrowserCandidate{browser},
		selected: discovery.Selection{
			BrowserID: browser.ID,
			TargetID:  target.ID,
			Title:     target.Title,
			URL:       target.URL,
			Origin:    target.Origin,
			Target:    target,
		},
		selectedOK: true,
		snapshots: map[string]discovery.TargetSnapshot{
			browser.ID: {Targets: []discovery.Target{target}, CandidateCount: 1},
		},
	}
	executor := New(Options{Service: fake}).Executor()
	contextResponse, err := executor.Execute(context.Background(), messages.ToolCall{ID: "safe-context", Name: GetContextToolName, Arguments: `{}`})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(contextResponse.Content, "user:pass") || strings.Contains(contextResponse.Content, "token=secret") || strings.Contains(contextResponse.Content, "fragment") {
		t.Fatalf("context leaked unsafe page metadata: %s", contextResponse.Content)
	}
	if !strings.Contains(contextResponse.Content, `"url":"redacted"`) || !strings.Contains(contextResponse.Content, `"origin":""`) {
		t.Fatalf("context did not redact unsafe page metadata: %s", contextResponse.Content)
	}
	listResponse, err := executor.Execute(context.Background(), messages.ToolCall{ID: "safe-list", Name: ListTabsToolName, Arguments: `{}`})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(listResponse.Content, "user:pass") || strings.Contains(listResponse.Content, "token=secret") || strings.Contains(listResponse.Content, "fragment") {
		t.Fatalf("list leaked unsafe page metadata: %s", listResponse.Content)
	}
	if !strings.Contains(listResponse.Content, `"url":"redacted"`) || !strings.Contains(listResponse.Content, `"origin":""`) {
		t.Fatalf("list did not redact unsafe page metadata: %s", listResponse.Content)
	}
}

func TestLaneBToolFailuresRefreshAndActivation(t *testing.T) {
	browser := discovery.BrowserCandidate{ID: "browser-a", Product: "Chrome/Test", Protocol: "1.3"}
	target := discovery.Target{BrowserID: browser.ID, ID: "target-a", Type: "page", Title: "Orders", URL: "https://example.test/orders", Origin: "https://example.test", Generation: 4, WebMCP: true, WebMCPKnown: true, ToolCount: 1, ToolCountKnown: true, Eligible: true}
	selected := discovery.Selection{BrowserID: browser.ID, TargetID: target.ID, Title: target.Title, URL: target.URL, Origin: target.Origin, Generation: target.Generation, Target: target}

	t.Run("empty list is classified", func(t *testing.T) {
		fake := &fakeDiscovery{candidates: []discovery.BrowserCandidate{browser}, snapshots: map[string]discovery.TargetSnapshot{browser.ID: {Browsers: []discovery.BrowserCandidate{browser}, CandidateCount: 0}}}
		response, err := New(Options{Service: fake}).Executor().Execute(context.Background(), messages.ToolCall{ID: "empty", Name: ListTabsToolName, Arguments: `{}`})
		if err != nil {
			t.Fatal(err)
		}
		assertFailureCode(t, response, ErrorNoEligibleTab)
	})

	t.Run("ambiguous selection is preserved", func(t *testing.T) {
		fake := &fakeDiscovery{candidates: []discovery.BrowserCandidate{browser}, selectErr: &discovery.DiscoveryError{
			Code:      discovery.CodeAmbiguousTab,
			Message:   "multiple browser tabs matched; an exact target ID is required",
			Retryable: true,
			Details:   map[string]any{"browser_id": browser.ID, "candidate_target_ids": []string{"target-a", "target-b"}},
		}}
		response, err := New(Options{Service: fake}).Executor().Execute(context.Background(), messages.ToolCall{ID: "ambiguous", Name: SelectTabToolName, Arguments: `{"browser_id":"browser-a","target_id":"target-a"}`})
		if err != nil {
			t.Fatal(err)
		}
		assertFailureCode(t, response, ErrorAmbiguousTab)
	})

	t.Run("refresh reports stale selection", func(t *testing.T) {
		fake := &fakeDiscovery{selected: selected, selectedOK: true, refreshErr: &discovery.DiscoveryError{
			Code:      discovery.CodeStaleSelection,
			Message:   "the selected browser target is no longer current",
			Retryable: true,
			Details:   map[string]any{"browser_id": browser.ID, "target_id": target.ID, "selected_generation": uint64(3), "reason": "generation_changed"},
		}}
		response, err := New(Options{Service: fake}).Executor().Execute(context.Background(), messages.ToolCall{ID: "stale", Name: GetContextToolName, Arguments: `{"refresh":true}`})
		if err != nil {
			t.Fatal(err)
		}
		assertFailureCode(t, response, ErrorStaleSelection)
		if fake.refreshCalls != 1 {
			t.Fatalf("refresh calls = %d, want one", fake.refreshCalls)
		}
	})

	t.Run("disconnect is safe and classified", func(t *testing.T) {
		fake := &fakeDiscovery{discoverErr: &discovery.DiscoveryError{
			Code:      discovery.CodeBrowserDisconnected,
			Message:   "browser connection ended; an exact reconnect is required",
			Retryable: false,
			Details:   map[string]any{"browser_id": browser.ID, "target_id": target.ID, "phase": "version", "reconnect_required": true},
		}}
		response, err := New(Options{Service: fake}).Executor().Execute(context.Background(), messages.ToolCall{ID: "disconnect", Name: ListTabsToolName, Arguments: `{}`})
		if err != nil {
			t.Fatal(err)
		}
		assertFailureCode(t, response, ErrorBrowserDisconnected)
		if strings.Contains(response.Content, "ws://") || strings.Contains(response.Content, "token") {
			t.Fatalf("disconnect result leaked transport data: %s", response.Content)
		}
	})

	t.Run("activation is passed exactly once", func(t *testing.T) {
		fake := &fakeDiscovery{candidates: []discovery.BrowserCandidate{browser}, selected: selected, selectedOK: true}
		response, err := New(Options{Service: fake}).Executor().Execute(context.Background(), messages.ToolCall{ID: "activate", Name: SelectTabToolName, Arguments: `{"browser_id":"browser-a","target_id":"target-a","activate":true}`})
		if err != nil {
			t.Fatal(err)
		}
		if !responseOK(response) || len(fake.selectRequests) != 1 || !fake.selectRequests[0].Activate {
			t.Fatalf("activation response/request = %#v/%#v", response, fake.selectRequests)
		}
	})
}

func TestLaneBListTabsRequiresExactBrowserBeforeListing(t *testing.T) {
	fake := &fakeDiscovery{candidates: []discovery.BrowserCandidate{
		{ID: "browser-b", Product: "Beta"},
		{ID: "browser-a", Product: "Alpha"},
	}}
	response, err := New(Options{Service: fake}).Executor().Execute(context.Background(), messages.ToolCall{
		ID:        "ambiguous-list",
		Name:      ListTabsToolName,
		Arguments: `{}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertFailureCode(t, response, ErrorAmbiguousBrowser)
	if fake.listCalls != 0 {
		t.Fatalf("target list calls = %d, want zero before ambiguity is reported", fake.listCalls)
	}
	envelope, err := UnmarshalToolResult([]byte(response.Content))
	if err != nil {
		t.Fatalf("decode result: %v", err)
	}
	var ids []string
	if err := json.Unmarshal(mustJSON(t, envelope.Error.Details["candidate_browser_ids"]), &ids); err != nil {
		t.Fatalf("decode candidate IDs: %v", err)
	}
	if !equalStrings(ids, []string{"browser-a", "browser-b"}) {
		t.Fatalf("candidate browser IDs = %v, want sorted exact IDs", ids)
	}
}

func TestResultEnvelopeStrictShape(t *testing.T) {
	valid := `{"version":"webmcp.tool-result.v1","ok":true,"data":{"value":1},"error":null}`
	for _, raw := range []string{
		strings.Replace(valid, ToolResultVersion, "webmcp.tool-result.v0", 1),
		strings.Replace(valid, `,"error":null`, `,"error":null,"extra":true`, 1),
		strings.Replace(valid, `,"error":null`, `,"error":null,"ok":true`, 1),
		strings.Replace(valid, `"version":"webmcp.tool-result.v1"`, `"version":null`, 1),
		strings.Replace(valid, `"ok":true`, `"ok":null`, 1),
	} {
		if _, err := UnmarshalToolResult([]byte(raw)); err == nil {
			t.Fatalf("accepted invalid result %s", raw)
		}
	}
	for _, raw := range []string{
		`{"version":"webmcp.tool-result.v1","ok":false,"data":null,"error":{"code":"no_eligible_tab","message":"No eligible browser tab matched the request.","retryable":null,"details":{}}}`,
		`{"version":"webmcp.tool-result.v1","ok":false,"data":null,"error":{"code":"no_eligible_tab","message":null,"retryable":true,"details":{}}}`,
	} {
		if _, err := UnmarshalToolResult([]byte(raw)); err == nil {
			t.Fatalf("accepted null result error scalar %s", raw)
		}
	}
	failure, err := EncodeToolResult(nil, &ToolResultError{Code: string(ErrorNoEligibleTab), Message: "No eligible browser tab matched the request.", Retryable: true, Details: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if string(failure) != `{"version":"webmcp.tool-result.v1","ok":false,"data":null,"error":{"code":"no_eligible_tab","message":"No eligible browser tab matched the request.","retryable":true,"details":{}}}` {
		t.Fatalf("failure envelope = %s", failure)
	}
}

type fakeDiscovery struct {
	candidates     []discovery.BrowserCandidate
	discoverErr    error
	snapshots      map[string]discovery.TargetSnapshot
	selected       discovery.Selection
	selectedOK     bool
	refreshErr     error
	refreshCalls   int
	selectErr      error
	selectRequests []discovery.TargetSelectionRequest
	discoverCalls  int
	listCalls      int
}

func (f *fakeDiscovery) DiscoverAll(context.Context, discovery.ConnectionInputs) ([]discovery.BrowserCandidate, error) {
	f.discoverCalls++
	if f.discoverErr != nil {
		return nil, f.discoverErr
	}
	return append([]discovery.BrowserCandidate(nil), f.candidates...), nil
}

func (f *fakeDiscovery) ListTargetSnapshot(_ context.Context, browser discovery.BrowserCandidate, options ...discovery.TargetListOptions) (discovery.TargetSnapshot, error) {
	f.listCalls++
	snapshot, ok := f.snapshots[browser.ID]
	if !ok {
		return discovery.TargetSnapshot{Browsers: []discovery.BrowserCandidate{browser}}, &discovery.DiscoveryError{Code: discovery.CodeNoEligibleTab, Message: "no eligible browser tab matched the requested filters", Retryable: true, Details: map[string]any{"filters": map[string]any{}, "candidate_count": 0}}
	}
	return snapshot, nil
}

func (f *fakeDiscovery) Select(_ context.Context, request discovery.TargetSelectionRequest) (discovery.Selection, error) {
	f.selectRequests = append(f.selectRequests, request)
	if f.selectErr != nil {
		return discovery.Selection{}, f.selectErr
	}
	return f.selected, nil
}

func (f *fakeDiscovery) Selected() (discovery.Selection, bool) { return f.selected, f.selectedOK }

func (f *fakeDiscovery) RefreshSelection(context.Context) (discovery.Selection, error) {
	f.refreshCalls++
	if f.refreshErr != nil {
		return discovery.Selection{}, f.refreshErr
	}
	if !f.selectedOK {
		return discovery.Selection{}, noSelectionError()
	}
	return f.selected, nil
}

func (f *fakeDiscovery) Browser(browserID string) (discovery.BrowserCandidate, bool) {
	for _, candidate := range f.candidates {
		if candidate.ID == browserID {
			return candidate, true
		}
	}
	return discovery.BrowserCandidate{}, false
}

func (f *fakeDiscovery) callCount() int {
	return f.discoverCalls + f.listCalls + len(f.selectRequests) + f.refreshCalls
}

func assertLaneBTextualResponse(t *testing.T, response messages.ToolCallResponse, callID, name string) {
	t.Helper()
	if response.ToolCallID != callID || response.Name != name {
		t.Fatalf("response correlation = (%q,%q), want (%q,%q)", response.ToolCallID, response.Name, callID, name)
	}
	if response.Content == "" || len(response.ContentParts) != 0 || !json.Valid([]byte(response.Content)) {
		t.Fatalf("response = %#v, want one compact textual JSON result", response)
	}
	if strings.TrimSpace(response.Content) != response.Content {
		t.Fatalf("response has outer whitespace: %q", response.Content)
	}
}

func assertFailureCode(t *testing.T, response messages.ToolCallResponse, want ErrorCode) {
	t.Helper()
	envelope, err := UnmarshalToolResult([]byte(response.Content))
	if err != nil {
		t.Fatalf("decode failure: %v", err)
	}
	if envelope.OK || envelope.Data == nil || envelope.Error == nil || envelope.Error.Code != string(want) {
		t.Fatalf("failure envelope = %#v, want %s", envelope, want)
	}
}

func responseOK(response messages.ToolCallResponse) bool {
	envelope, err := UnmarshalToolResult([]byte(response.Content))
	return err == nil && envelope.OK && envelope.Error == nil
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
