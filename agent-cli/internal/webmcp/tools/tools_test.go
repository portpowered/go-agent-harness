package tools

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestBrokerToolSetHasExactlyTheFrozenSixSchemas(t *testing.T) {
	set := NewToolSet(nil)
	schemas := set.DefinitionSchemas()
	if len(schemas) != 6 {
		t.Fatalf("schema count = %d, want six", len(schemas))
	}
	wantNames := webmcp.StableToolNames()
	for i, schema := range schemas {
		if schema["type"] != "function" {
			t.Fatalf("schema %d type = %#v, want function", i, schema["type"])
		}
		function, ok := schema["function"].(map[string]any)
		if !ok {
			t.Fatalf("schema %d function = %#v, want object", i, schema["function"])
		}
		if function["name"] != wantNames[i] {
			t.Fatalf("schema %d name = %#v, want %q", i, function["name"], wantNames[i])
		}
		parameters, ok := function["parameters"].(map[string]any)
		if !ok {
			t.Fatalf("schema %d parameters = %#v, want object", i, function["parameters"])
		}
		if parameters["type"] != "object" || parameters["additionalProperties"] != false {
			t.Fatalf("schema %q is not a closed object: %#v", wantNames[i], parameters)
		}
	}

	cases := []struct {
		name     string
		required []string
		defaults map[string]any
		fields   []string
	}{
		{name: webmcp.GetContextToolName, defaults: map[string]any{"refresh": false}, fields: []string{"refresh"}},
		{name: webmcp.ListTabsToolName, defaults: map[string]any{"browser_id": "", "origin_contains": "", "eligible_only": true, "include_zero_tool_pages": false}, fields: []string{"browser_id", "origin_contains", "eligible_only", "include_zero_tool_pages"}},
		{name: webmcp.SelectTabToolName, required: []string{"browser_id", "target_id"}, defaults: map[string]any{"activate": false}, fields: []string{"browser_id", "target_id", "activate"}},
		{name: webmcp.ListToolsToolName, defaults: map[string]any{"refresh": false, "name_contains": "", "include_schemas": true, "frame_id": ""}, fields: []string{"refresh", "name_contains", "include_schemas", "frame_id"}},
		{name: webmcp.InvokeToolName, required: []string{"tool_ref", "input_json", "reason"}, fields: []string{"tool_ref", "input_json", "reason"}},
		{name: webmcp.CancelToolName, required: []string{"invocation_id"}, defaults: map[string]any{"reason": ""}, fields: []string{"invocation_id", "reason"}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var schema map[string]any
			for _, candidate := range schemas {
				function := candidate["function"].(map[string]any)
				if function["name"] == testCase.name {
					schema = function["parameters"].(map[string]any)
					break
				}
			}
			properties := schema["properties"].(map[string]any)
			if !reflect.DeepEqual(testCase.fields, mapKeysInContractOrder(testCase.name, properties)) {
				t.Fatalf("property names = %#v, want %#v", mapKeysInContractOrder(testCase.name, properties), testCase.fields)
			}
			if got, ok := schema["required"].([]string); ok {
				if !reflect.DeepEqual(got, testCase.required) {
					t.Fatalf("required = %#v, want %#v", got, testCase.required)
				}
			} else if len(testCase.required) != 0 {
				t.Fatalf("required field missing, want %#v", testCase.required)
			}
			for name, wantDefault := range testCase.defaults {
				property := properties[name].(map[string]any)
				if got := property["default"]; !reflect.DeepEqual(got, wantDefault) {
					t.Errorf("%s default = %#v, want %#v", name, got, wantDefault)
				}
			}
		})
	}

	first := schemas[0]["function"].(map[string]any)["parameters"].(map[string]any)
	first["additionalProperties"] = true
	second := NewToolSet(nil).DefinitionSchemas()[0]["function"].(map[string]any)["parameters"].(map[string]any)
	if second["additionalProperties"] != false {
		t.Fatal("stable definitions share mutable schema state")
	}
}

func TestExecutorRejectsInvalidBrokerArgumentsBeforeCallingBroker(t *testing.T) {
	broker := &recordingBroker{}
	executor := NewExecutor(broker)
	cases := []struct {
		name       string
		arguments  string
		wantPath   string
		wantCode   string
		wantNoText string
	}{
		{name: webmcp.GetContextToolName, arguments: `{"refresh":false,"secret":"do-not-echo"}`, wantPath: "/secret", wantCode: "unknown_property", wantNoText: "do-not-echo"},
		{name: webmcp.SelectTabToolName, arguments: `{"browser_id":"browser-a"}`, wantPath: "/target_id", wantCode: "required"},
		{name: webmcp.ListToolsToolName, arguments: `[]`, wantPath: "/", wantCode: "object_required"},
		{name: webmcp.GetContextToolName, arguments: `{"refresh":false}{}`, wantPath: "/", wantCode: "multiple_json_values"},
	}
	for index, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			before := broker.callCount()
			response, err := executor.Execute(context.Background(), messages.ToolCall{ID: "call-" + string(rune('a'+index)), Name: testCase.name, Arguments: testCase.arguments})
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			assertTextualResponse(t, response, "call-"+string(rune('a'+index)), testCase.name)
			if broker.callCount() != before {
				t.Fatalf("broker calls changed from %d to %d for invalid input", before, broker.callCount())
			}
			envelope, err := webmcp.UnmarshalToolResult([]byte(response.Content))
			if err != nil {
				t.Fatalf("decode envelope: %v", err)
			}
			if envelope.OK || envelope.Error == nil || envelope.Error.Code != string(webmcp.ErrorInvalidToolInput) {
				t.Fatalf("envelope = %#v, want invalid_tool_input failure", envelope)
			}
			var details struct {
				Issues []webmcp.ToolResultIssue `json:"issues"`
			}
			if err := json.Unmarshal(mustRawJSON(t, envelope.Error.Details["issues"]), &details.Issues); err != nil {
				t.Fatalf("decode issues: %v", err)
			}
			found := false
			for _, issue := range details.Issues {
				if issue.Path == testCase.wantPath && issue.Code == testCase.wantCode {
					found = true
				}
			}
			if !found {
				t.Fatalf("issues = %#v, want %s/%s", details.Issues, testCase.wantPath, testCase.wantCode)
			}
			if testCase.wantNoText != "" && strings.Contains(response.Content, testCase.wantNoText) {
				t.Fatalf("invalid response echoed offending value %q: %s", testCase.wantNoText, response.Content)
			}
		})
	}
}

func TestExecutorReturnsCorrelatedCompactEnvelopesAndPreservesPageValues(t *testing.T) {
	broker := &recordingBroker{
		selected: webmcp.PageContext{
			Key:        webmcp.PageKey{BrowserID: "browser-a", TargetID: "tab-a"},
			Title:      "Fixture",
			URL:        "https://fixture.test/",
			Origin:     "https://fixture.test",
			Generation: 7,
			Connected:  true,
			Ready:      true,
		},
		targets: []webmcp.Target{
			{BrowserID: "browser-a", ID: "tab-a", Type: "page", Title: "Fixture", Origin: "https://fixture.test", Eligible: true},
			{BrowserID: "browser-a", ID: "tab-b", Type: "page", Title: "Other", Origin: "https://other.test", Eligible: false},
		},
		catalog: webmcp.ToolCatalogSnapshot{
			Generation: 7,
			Tools: []webmcp.ToolDescriptor{{
				Ref:         "webmcp.tool-ref.v1:AAECAwQFBgcICQoLDA0ODw",
				Name:        "read_state",
				Description: "Read fixture state",
				InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
				Annotations: webmcp.ToolAnnotations{ReadOnly: boolPointer(true)},
				FrameID:     "frame-1",
				Origin:      "https://fixture.test",
				Generation:  7,
			}},
		},
		invokeResult: webmcp.InvokeResult{InvocationID: "inv-1", State: webmcp.InvocationCompleted, Output: json.RawMessage(`{"value":42}`)},
	}
	executor := NewExecutor(broker)

	response, err := executor.Execute(context.Background(), messages.ToolCall{ID: "call-context", Name: webmcp.GetContextToolName, Arguments: `{}`})
	if err != nil {
		t.Fatalf("get context: %v", err)
	}
	assertTextualResponse(t, response, "call-context", webmcp.GetContextToolName)
	if response.Content != `{"version":"webmcp.tool-result.v1","ok":true,"data":{"browser_id":"browser-a","target_id":"tab-a","title":"Fixture","url":"https://fixture.test/","origin":"https://fixture.test","generation":7,"connected":true,"ready":true},"error":null}` {
		t.Fatalf("context golden = %s", response.Content)
	}

	response, err = executor.Execute(context.Background(), messages.ToolCall{ID: "call-invoke", Name: webmcp.InvokeToolName, Arguments: `{"tool_ref":"webmcp.tool-ref.v1:AAECAwQFBgcICQoLDA0ODw","input_json":"{\"count\":90071992547409931234567890}","reason":"read it"}`})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	assertTextualResponse(t, response, "call-invoke", webmcp.InvokeToolName)
	wantInvoke := `{"version":"webmcp.tool-result.v1","ok":true,"data":{"invocation_id":"inv-1","tool_ref":"webmcp.tool-ref.v1:AAECAwQFBgcICQoLDA0ODw","status":"completed","output":{"value":42}},"error":null}`
	if response.Content != wantInvoke {
		t.Fatalf("invoke golden = %s, want %s", response.Content, wantInvoke)
	}
	if string(broker.lastInvoke.Input) != `{"count":90071992547409931234567890}` {
		t.Fatalf("invoke input = %s, want original number token", broker.lastInvoke.Input)
	}

	for _, output := range []string{`[1,{"value":2}]`, `null`} {
		broker.invokeResult.InvocationID = webmcp.InvocationID("inv-" + strings.ReplaceAll(output, "", ""))
		broker.invokeResult.Output = json.RawMessage(output)
		response, err = executor.Execute(context.Background(), messages.ToolCall{ID: "call-output", Name: webmcp.InvokeToolName, Arguments: `{"tool_ref":"webmcp.tool-ref.v1:AAECAwQFBgcICQoLDA0ODw","input_json":"{}","reason":"read it"}`})
		if err != nil {
			t.Fatalf("invoke output %s: %v", output, err)
		}
		var envelope webmcp.ToolResultEnvelope
		if envelope, err = webmcp.UnmarshalToolResult([]byte(response.Content)); err != nil {
			t.Fatalf("decode output %s: %v", output, err)
		}
		var data struct {
			Output json.RawMessage `json:"output"`
		}
		if err := json.Unmarshal(envelope.Data, &data); err != nil {
			t.Fatalf("decode output data: %v", err)
		}
		if string(data.Output) != output {
			t.Fatalf("output = %s, want %s", data.Output, output)
		}
	}

	response, err = executor.Execute(context.Background(), messages.ToolCall{ID: "call-cancel", Name: webmcp.CancelToolName, Arguments: `{"invocation_id":"inv-1","reason":"user stopped"}`})
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	assertTextualResponse(t, response, "call-cancel", webmcp.CancelToolName)
	if response.Content != `{"version":"webmcp.tool-result.v1","ok":true,"data":{"invocation_id":"inv-1","status":"cancel_requested"},"error":null}` {
		t.Fatalf("cancel golden = %s", response.Content)
	}
	if broker.lastCancel.Reason != "user stopped" {
		t.Fatalf("cancel reason = %q", broker.lastCancel.Reason)
	}
}

func TestToolSetRegistryUsesTheSameTextualContract(t *testing.T) {
	broker := &recordingBroker{selected: webmcp.PageContext{Key: webmcp.PageKey{BrowserID: "browser-a", TargetID: "tab-a"}, Generation: 1}}
	set := NewToolSet(broker)
	registry, err := set.Registry()
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	if got := registry.List(); len(got) != 6 {
		t.Fatalf("registry names = %#v, want six tools", got)
	}
	msgs, err := registry.Execute(context.Background(), webmcp.GetContextToolName, map[string]any{"refresh": false})
	if err != nil {
		t.Fatalf("registry execute: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Role != messages.RoleTool || len(msgs[0].ContentParts) != 1 {
		t.Fatalf("registry result = %#v, want one plain tool message", msgs)
	}
	if _, err := webmcp.UnmarshalToolResult([]byte(msgs[0].TextContent())); err != nil {
		t.Fatalf("registry result envelope: %v", err)
	}
}

type recordingBroker struct {
	selected     webmcp.PageContext
	targets      []webmcp.Target
	catalog      webmcp.ToolCatalogSnapshot
	invokeResult webmcp.InvokeResult
	lastInvoke   webmcp.InvokeRequest
	lastCancel   webmcp.CancelRequest
	calls        []string
}

func (b *recordingBroker) Discover(context.Context, webmcp.DiscoverOptions) ([]webmcp.BrowserCandidate, error) {
	b.calls = append(b.calls, "discover")
	return nil, nil
}

func (b *recordingBroker) ListTargets(context.Context, webmcp.BrowserSelector) ([]webmcp.Target, error) {
	b.calls = append(b.calls, "list_targets")
	return append([]webmcp.Target(nil), b.targets...), nil
}

func (b *recordingBroker) Select(context.Context, webmcp.TargetSelector) (webmcp.PageContext, error) {
	b.calls = append(b.calls, "select")
	return b.selected, nil
}

func (b *recordingBroker) Selected(context.Context) (webmcp.PageContext, error) {
	b.calls = append(b.calls, "selected")
	return b.selected, nil
}

func (b *recordingBroker) ListTools(context.Context, webmcp.ListToolsOptions) (webmcp.ToolCatalogSnapshot, error) {
	b.calls = append(b.calls, "list_tools")
	return b.catalog, nil
}

func (b *recordingBroker) Invoke(_ context.Context, request webmcp.InvokeRequest) (webmcp.InvokeResult, error) {
	b.calls = append(b.calls, "invoke")
	b.lastInvoke = request
	return b.invokeResult, nil
}

func (b *recordingBroker) Cancel(_ context.Context, request webmcp.CancelRequest) error {
	b.calls = append(b.calls, "cancel")
	b.lastCancel = request
	return nil
}

func (b *recordingBroker) Watch(context.Context) <-chan webmcp.BrokerEvent {
	channel := make(chan webmcp.BrokerEvent)
	close(channel)
	return channel
}

func (b *recordingBroker) Close() error { return nil }

func (b *recordingBroker) callCount() int { return len(b.calls) }

func (b *recordingBroker) SelectWithOptions(_ context.Context, _ webmcp.TargetSelector, _ webmcp.SelectOptions) (webmcp.PageContext, error) {
	b.calls = append(b.calls, "select_with_options")
	return b.selected, nil
}

func (b *recordingBroker) SelectedWithRefresh(_ context.Context, _ bool) (webmcp.PageContext, error) {
	b.calls = append(b.calls, "selected_with_refresh")
	return b.selected, nil
}

func assertTextualResponse(t *testing.T, response messages.ToolCallResponse, callID, name string) {
	t.Helper()
	if response.ToolCallID != callID || response.Name != name {
		t.Fatalf("response correlation = (%q, %q), want (%q, %q)", response.ToolCallID, response.Name, callID, name)
	}
	if len(response.ContentParts) != 0 || response.Content == "" {
		t.Fatalf("response = %#v, want one textual content value", response)
	}
	if !json.Valid([]byte(response.Content)) {
		t.Fatalf("response content is not JSON: %s", response.Content)
	}
}

func mustRawJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal raw JSON: %v", err)
	}
	return encoded
}

func boolPointer(value bool) *bool { return &value }

func mapKeysInContractOrder(name string, properties map[string]any) []string {
	orders := map[string][]string{
		webmcp.GetContextToolName: {"refresh"},
		webmcp.ListTabsToolName:   {"browser_id", "origin_contains", "eligible_only", "include_zero_tool_pages"},
		webmcp.SelectTabToolName:  {"browser_id", "target_id", "activate"},
		webmcp.ListToolsToolName:  {"refresh", "name_contains", "include_schemas", "frame_id"},
		webmcp.InvokeToolName:     {"tool_ref", "input_json", "reason"},
		webmcp.CancelToolName:     {"invocation_id", "reason"},
	}
	result := make([]string, 0, len(properties))
	for _, property := range orders[name] {
		if _, ok := properties[property]; ok {
			result = append(result, property)
		}
	}
	return result
}

var _ webmcp.Broker = (*recordingBroker)(nil)
