package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"reflect"
	"strings"
	"testing"

	cliTools "github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/testkit"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestBrokerToolSetPreservesFrozenSchemasAndAddsShowPage(t *testing.T) {
	set := NewToolSet(nil)
	schemas := set.DefinitionSchemas()
	if len(schemas) != 7 {
		t.Fatalf("schema count = %d, want six stable tools plus show_page", len(schemas))
	}
	wantNames := webmcp.StableToolNames()
	for i, schema := range schemas[:len(wantNames)] {
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
	showPage := schemas[len(wantNames)]
	showPageFunction, ok := showPage["function"].(map[string]any)
	if !ok || showPageFunction["name"] != webmcp.ShowPageToolName {
		t.Fatalf("show_page schema = %#v", showPage)
	}
	showPageParameters, ok := showPageFunction["parameters"].(map[string]any)
	if !ok || showPageParameters["type"] != "object" || showPageParameters["additionalProperties"] != false {
		t.Fatalf("show_page parameters = %#v, want a closed object", showPageFunction["parameters"])
	}
	if properties, ok := showPageParameters["properties"].(map[string]any); !ok || len(properties) != 0 {
		t.Fatalf("show_page properties = %#v, want empty", showPageParameters["properties"])
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

func TestShowPageReturnsValidatedBoundedMetadata(t *testing.T) {
	imageBytes := testPNG(t, 3, 2)
	broker := &recordingBroker{
		selected: webmcp.PageContext{Key: webmcp.PageKey{BrowserID: "browser-a", TargetID: "tab-a"}},
		screenshot: webmcp.PageScreenshot{
			MIMEType: "IMAGE/PNG",
			Bytes:    imageBytes,
		},
	}
	response, err := NewBrokerToolSet(broker).Executor().Execute(context.Background(), messages.ToolCall{
		ID:        "show-call",
		Name:      webmcp.ShowPageToolName,
		Arguments: `{}`,
	})
	if err != nil {
		t.Fatalf("show_page: %v", err)
	}
	assertTextualResponse(t, response, "show-call", webmcp.ShowPageToolName)
	envelope, err := webmcp.UnmarshalToolResult([]byte(response.Content))
	if err != nil || !envelope.OK {
		t.Fatalf("show_page envelope = %#v (err %v), want success", envelope, err)
	}
	var result ShowPageResult
	if err := json.Unmarshal(envelope.Data, &result); err != nil {
		t.Fatalf("decode show_page data: %v", err)
	}
	digest := sha256.Sum256(imageBytes)
	if result.Version != ShowPageResultVersion || result.Source != showPageSource ||
		result.BrowserID != "browser-a" || result.TargetID != "tab-a" ||
		result.MIMEType != "image/png" || result.ByteLength != len(imageBytes) ||
		result.Width != 3 || result.Height != 2 || result.SHA256 != fmt.Sprintf("%x", digest) {
		t.Fatalf("show_page result = %+v, want normalized capture metadata", result)
	}
	if strings.Contains(response.Content, string(imageBytes)) {
		t.Fatal("show_page result exposed raw image bytes")
	}
	if got := broker.calls; len(got) != 1 || got[0] != "capture_page" {
		t.Fatalf("broker calls = %#v, want one capture call", got)
	}
}

func TestShowPageReturnsClassifiedErrorsWithoutImageData(t *testing.T) {
	valid := testPNG(t, 2, 2)
	cases := []struct {
		name   string
		shot   webmcp.PageScreenshot
		reason string
	}{
		{name: "empty", shot: webmcp.PageScreenshot{MIMEType: "image/png"}, reason: "empty_capture"},
		{name: "unsupported mime", shot: webmcp.PageScreenshot{MIMEType: "image/webp", Bytes: valid}, reason: "unsupported_mime_type"},
		{name: "mime mismatch", shot: webmcp.PageScreenshot{MIMEType: "image/jpeg", Bytes: valid}, reason: "mime_mismatch"},
		{name: "malformed", shot: webmcp.PageScreenshot{MIMEType: "image/png", Bytes: []byte("not an image")}, reason: "malformed_image"},
		{name: "dimension mismatch", shot: webmcp.PageScreenshot{MIMEType: "image/png", Bytes: valid, Width: 9}, reason: "dimension_mismatch"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			broker := &recordingBroker{
				selected:   webmcp.PageContext{Key: webmcp.PageKey{BrowserID: "browser-a", TargetID: "tab-a"}},
				screenshot: testCase.shot,
			}
			response, err := NewBrokerToolSet(broker).Executor().Execute(context.Background(), messages.ToolCall{
				ID:        "error-call",
				Name:      webmcp.ShowPageToolName,
				Arguments: `{}`,
			})
			if err != nil {
				t.Fatalf("show_page: %v", err)
			}
			envelope, err := webmcp.UnmarshalToolResult([]byte(response.Content))
			if err != nil || envelope.OK || envelope.Error == nil {
				t.Fatalf("show_page envelope = %#v (err %v), want failure", envelope, err)
			}
			if envelope.Error.Code != string(webmcp.ErrorInvocationFailed) || envelope.Data == nil {
				t.Fatalf("show_page error = %#v, want invocation_failed with null data", envelope.Error)
			}
			if got := envelope.Error.Details["reason_code"]; got != testCase.reason {
				t.Fatalf("reason_code = %#v, want %q", got, testCase.reason)
			}
			if string(envelope.Data) != "null" {
				t.Fatalf("failed show_page data = %s, want null", envelope.Data)
			}
		})
	}
}

func TestShowPageIsDisabledAndInputClosedOutsideBrowserSessions(t *testing.T) {
	response, err := NewBrokerToolSet(nil).Executor().Execute(context.Background(), messages.ToolCall{
		ID:        "disabled-show",
		Name:      webmcp.ShowPageToolName,
		Arguments: `{}`,
	})
	if err != nil {
		t.Fatalf("disabled show_page: %v", err)
	}
	envelope, err := webmcp.UnmarshalToolResult([]byte(response.Content))
	if err != nil || envelope.OK || envelope.Error == nil || envelope.Error.Code != string(webmcp.ErrorWebMCPDisabled) {
		t.Fatalf("disabled show_page = %#v (err %v), want webmcp_disabled", envelope, err)
	}

	broker := &recordingBroker{
		selected:   webmcp.PageContext{Key: webmcp.PageKey{BrowserID: "browser-a", TargetID: "tab-a"}},
		screenshot: webmcp.PageScreenshot{MIMEType: "image/png", Bytes: testPNG(t, 1, 1)},
	}
	response, err = NewBrokerToolSet(broker).Executor().Execute(context.Background(), messages.ToolCall{
		ID:        "invalid-show",
		Name:      webmcp.ShowPageToolName,
		Arguments: `{"unexpected":true}`,
	})
	if err != nil {
		t.Fatalf("invalid show_page: %v", err)
	}
	envelope, err = webmcp.UnmarshalToolResult([]byte(response.Content))
	if err != nil || envelope.OK || envelope.Error == nil || envelope.Error.Code != string(webmcp.ErrorInvalidToolInput) {
		t.Fatalf("invalid show_page = %#v (err %v), want invalid_tool_input", envelope, err)
	}
	if len(broker.calls) != 0 {
		t.Fatalf("invalid show_page called broker: %#v", broker.calls)
	}
}

func TestShowPagePreservesCancellationAndDeadlineClassification(t *testing.T) {
	for _, testCase := range []struct {
		name string
		err  error
		code webmcp.ErrorCode
	}{
		{name: "canceled", err: context.Canceled, code: webmcp.ErrorInvocationCanceled},
		{name: "deadline", err: context.DeadlineExceeded, code: webmcp.ErrorInvocationTimedOut},
		{name: "closed", err: webmcp.ErrClosed, code: webmcp.ErrorInvocationFailed},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			broker := &recordingBroker{
				selected:      webmcp.PageContext{Key: webmcp.PageKey{BrowserID: "browser-a", TargetID: "tab-a"}},
				screenshotErr: testCase.err,
			}
			response, err := NewBrokerToolSet(broker).Executor().Execute(context.Background(), messages.ToolCall{
				ID:        "error-classification",
				Name:      webmcp.ShowPageToolName,
				Arguments: `{}`,
			})
			if err != nil {
				t.Fatalf("show_page: %v", err)
			}
			envelope, err := webmcp.UnmarshalToolResult([]byte(response.Content))
			if err != nil || envelope.OK || envelope.Error == nil || envelope.Error.Code != string(testCase.code) {
				t.Fatalf("show_page envelope = %#v (err %v), want %s", envelope, err, testCase.code)
			}
		})
	}
}

func TestShowPageNamespaceIsPreflightedWithStaticTools(t *testing.T) {
	err := cliTools.ValidateToolDefinitionNamespaces(
		[]messages.ToolDefinition{{Name: webmcp.ShowPageToolName}},
		NewBrokerToolSet(nil).Definitions(),
	)
	if !errors.Is(err, cliTools.ErrToolCompositionCollision) {
		t.Fatalf("show_page namespace error = %v, want collision before composition", err)
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

	response, err = executor.Execute(context.Background(), messages.ToolCall{
		ID:        "call-select",
		Name:      webmcp.SelectTabToolName,
		Arguments: `{"browser_id":"browser-a","target_id":"tab-a","activate":true}`,
	})
	if err != nil {
		t.Fatalf("select tab: %v", err)
	}
	assertTextualResponse(t, response, "call-select", webmcp.SelectTabToolName)
	var selected struct {
		BrowserID webmcp.BrowserID `json:"browser_id"`
		TargetID  webmcp.TargetID  `json:"target_id"`
		NextStep  string           `json:"next_step"`
	}
	envelope, err := webmcp.UnmarshalToolResult([]byte(response.Content))
	if err != nil {
		t.Fatalf("decode select result: %v", err)
	}
	if !envelope.OK {
		t.Fatalf("select envelope = %#v, want success", envelope)
	}
	if err := json.Unmarshal(envelope.Data, &selected); err != nil {
		t.Fatalf("decode select data: %v", err)
	}
	if selected.BrowserID != "browser-a" || selected.TargetID != "tab-a" || selected.NextStep != "selected; call webmcp_list_tools to obtain tool refs" {
		t.Fatalf("select data = %+v, want exact selection and next step", selected)
	}
	if len(broker.calls) == 0 || broker.calls[len(broker.calls)-1] != "select_with_options" {
		t.Fatalf("select broker calls = %#v, want select_with_options", broker.calls)
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

func TestExecutorSelectsAndListsAfterLiveActivationFailure(t *testing.T) {
	candidate := webmcp.BrowserCandidate{ID: "browser-headless", Product: "Chrome/Test", Loopback: true}
	target := webmcp.Target{
		BrowserID: candidate.ID,
		ID:        "tab-headless",
		Type:      "page",
		Title:     "Headless page",
		URL:       "https://fixture.test/headless",
		Origin:    "https://fixture.test",
	}
	tool := webmcp.ToolDescriptor{
		Name:        "read_state",
		Description: "Read fixture state",
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`),
		FrameID:     "frame-1",
		Origin:      target.Origin,
	}
	runtime := testkit.NewScriptedBrowserRuntime(testkit.BrowserConfig{
		Candidate:     candidate,
		ActivateError: errors.New("foreground activation rejected by headless Chrome"),
		Targets: []testkit.TargetConfig{
			testkit.NewTargetConfig(target, testkit.WithInitialCatalog(tool)),
		},
	})
	defer func() { _ = runtime.Close() }()
	browser := webmcp.NewBroker(webmcp.BrokerOptions{
		Runtime:    runtime,
		Discoverer: staticToolTestDiscoverer{candidate: candidate},
	})
	defer func() { _ = browser.Close() }()
	executor := NewExecutor(browser)

	selected, err := executor.Execute(context.Background(), messages.ToolCall{
		ID:        "call-select-headless",
		Name:      webmcp.SelectTabToolName,
		Arguments: `{"browser_id":"browser-headless","target_id":"tab-headless","activate":true}`,
	})
	if err != nil {
		t.Fatalf("select tab after activation failure: %v", err)
	}
	selectionEnvelope, err := webmcp.UnmarshalToolResult([]byte(selected.Content))
	if err != nil {
		t.Fatalf("decode selection envelope: %v", err)
	}
	if !selectionEnvelope.OK {
		t.Fatalf("selection envelope = %#v, want success", selectionEnvelope)
	}
	var selectionData struct {
		BrowserID webmcp.BrowserID `json:"browser_id"`
		TargetID  webmcp.TargetID  `json:"target_id"`
		Connected bool             `json:"connected"`
		Ready     bool             `json:"ready"`
	}
	if err := json.Unmarshal(selectionEnvelope.Data, &selectionData); err != nil {
		t.Fatalf("decode selection data: %v", err)
	}
	if selectionData.BrowserID != candidate.ID || selectionData.TargetID != target.ID || !selectionData.Connected || !selectionData.Ready {
		t.Fatalf("selection data = %#v, want exact connected ready selection", selectionData)
	}

	listed, err := executor.Execute(context.Background(), messages.ToolCall{
		ID:        "call-list-tools-headless",
		Name:      webmcp.ListToolsToolName,
		Arguments: `{"include_schemas":true}`,
	})
	if err != nil {
		t.Fatalf("list tools after activation failure: %v", err)
	}
	listEnvelope, err := webmcp.UnmarshalToolResult([]byte(listed.Content))
	if err != nil {
		t.Fatalf("decode list-tools envelope: %v", err)
	}
	if !listEnvelope.OK {
		t.Fatalf("list-tools envelope = %#v, want success", listEnvelope)
	}
	var data struct {
		Generation uint64 `json:"generation"`
		Tools      []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(listEnvelope.Data, &data); err != nil {
		t.Fatalf("decode list-tools data: %v", err)
	}
	if data.Generation == 0 || len(data.Tools) != 1 || data.Tools[0].Name != tool.Name {
		t.Fatalf("list-tools data = %#v, want ready catalog", data)
	}
}

func TestToolSetRegistryUsesTheSameTextualContract(t *testing.T) {
	broker := &recordingBroker{selected: webmcp.PageContext{Key: webmcp.PageKey{BrowserID: "browser-a", TargetID: "tab-a"}, Generation: 1}}
	set := NewToolSet(broker)
	registry, err := set.Registry()
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	if got := registry.List(); len(got) != 7 {
		t.Fatalf("registry names = %#v, want six stable tools plus show_page", got)
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
	selected      webmcp.PageContext
	targets       []webmcp.Target
	catalog       webmcp.ToolCatalogSnapshot
	invokeResult  webmcp.InvokeResult
	lastInvoke    webmcp.InvokeRequest
	lastCancel    webmcp.CancelRequest
	screenshot    webmcp.PageScreenshot
	screenshotErr error
	calls         []string
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

func (b *recordingBroker) CapturePageScreenshot(context.Context) (webmcp.PageScreenshot, error) {
	b.calls = append(b.calls, "capture_page")
	if b.screenshotErr != nil {
		return webmcp.PageScreenshot{}, b.screenshotErr
	}
	screenshot := b.screenshot
	if screenshot.BrowserID == "" {
		screenshot.BrowserID = b.selected.Key.BrowserID
	}
	if screenshot.TargetID == "" {
		screenshot.TargetID = b.selected.Key.TargetID
	}
	screenshot.Bytes = append([]byte(nil), screenshot.Bytes...)
	return screenshot, nil
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

func testPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	imageValue := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			imageValue.SetRGBA(x, y, color.RGBA{R: uint8(x + 1), G: uint8(y + 1), B: 0x7f, A: 0xff})
		}
	}
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, imageValue); err != nil {
		t.Fatalf("encode test PNG: %v", err)
	}
	return buffer.Bytes()
}

type staticToolTestDiscoverer struct {
	candidate webmcp.BrowserCandidate
}

func (d staticToolTestDiscoverer) Discover(context.Context, webmcp.DiscoverOptions) ([]webmcp.BrowserCandidate, error) {
	return []webmcp.BrowserCandidate{d.candidate}, nil
}

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
