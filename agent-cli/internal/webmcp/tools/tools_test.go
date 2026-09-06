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

	"github.com/portpowered/go-agent-harness/agent-cli/internal/sight"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/testkit"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeTools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
	runtimeToolsWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/wire"
)

func TestBrokerToolSetPreservesFrozenSchemasAndAddsBrowserControls(t *testing.T) {
	set := NewToolSet(nil)
	schemas := set.DefinitionSchemas()
	if len(schemas) != 9 {
		t.Fatalf("schema count = %d, want six stable tools plus open-tab, navigate-tab, and show_page", len(schemas))
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
	openTab := schemas[len(wantNames)]
	openTabFunction, ok := openTab["function"].(map[string]any)
	if !ok || openTabFunction["name"] != webmcp.OpenTabToolName {
		t.Fatalf("open-tab schema = %#v", openTab)
	}
	navigateTab := schemas[len(wantNames)+1]
	navigateTabFunction, ok := navigateTab["function"].(map[string]any)
	if !ok || navigateTabFunction["name"] != webmcp.NavigateTabToolName {
		t.Fatalf("navigate-tab schema = %#v", navigateTab)
	}
	showPage := schemas[len(wantNames)+2]
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
		{name: webmcp.OpenTabToolName, required: []string{"url"}, defaults: map[string]any{"browser_id": "", "activate": true}, fields: []string{"browser_id", "url", "activate"}},
		{name: webmcp.NavigateTabToolName, required: []string{"url"}, fields: []string{"url"}},
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
func TestOpenTabAndWebCastToolsExecuteEndToEndThroughBroker(t *testing.T) {
	if got := NewBrokerToolSet(nil).Definitions(); len(got) != 9 {
		t.Fatalf("default definitions = %d, want cast controls disabled", len(got))
	}
	broker := &recordingBroker{
		selected: webmcp.PageContext{
			Key:       webmcp.PageKey{BrowserID: "browser-office", TargetID: "tab-example"},
			URL:       "https://example.com/",
			Origin:    "https://example.com",
			Connected: true,
		},
		castDevices: []webmcp.CastDevice{{Name: "Office TV", ID: "sink-office"}},
	}
	set := NewBrokerToolSet(broker, true)
	definitions := set.Definitions()
	if len(definitions) != 12 || definitions[9].Name != webmcp.ListCastDevicesToolName || definitions[10].Name != webmcp.CastTabToolName || definitions[11].Name != webmcp.StopCastingToolName {
		t.Fatalf("cast definitions = %+v", definitions)
	}
	schemas := set.DefinitionSchemas()
	if len(schemas) != 12 {
		t.Fatalf("cast schemas = %d, want 12", len(schemas))
	}
	castParameters := schemas[10]["function"].(map[string]any)["parameters"].(map[string]any)
	modeSchema := castParameters["properties"].(map[string]any)["mode"].(map[string]any)
	if modeSchema["default"] != string(webmcp.CastModeTab) || !reflect.DeepEqual(modeSchema["enum"], []string{string(webmcp.CastModeMedia), string(webmcp.CastModeTab)}) {
		t.Fatalf("cast mode schema = %+v", modeSchema)
	}

	calls := []messages.ToolCall{
		{ID: "open-tab", Name: webmcp.OpenTabToolName, Arguments: `{"url":"https://example.com/","activate":true}`},
		{ID: "list-cast", Name: webmcp.ListCastDevicesToolName, Arguments: `{}`},
		{ID: "cast-tab", Name: webmcp.CastTabToolName, Arguments: `{"device_name":"Office TV"}`},
		{ID: "cast-media", Name: webmcp.CastTabToolName, Arguments: `{"device_name":"Office TV","mode":"media"}`},
		{ID: "navigate-tab", Name: webmcp.NavigateTabToolName, Arguments: `{"url":"https://www.google.com/"}`},
		{ID: "stop-cast", Name: webmcp.StopCastingToolName, Arguments: `{"device_name":"Office TV"}`},
	}
	for _, call := range calls {
		response, err := set.Executor().Execute(context.Background(), call)
		if err != nil {
			t.Fatalf("execute %s: %v", call.Name, err)
		}
		envelope, err := webmcp.UnmarshalToolResult([]byte(response.Content))
		if err != nil || !envelope.OK {
			t.Fatalf("%s result = %s, err=%v", call.Name, response.Content, err)
		}
	}
	if !reflect.DeepEqual(broker.calls, []string{"open_tab", "list_cast_devices", "cast_tab", "cast_media", "navigate_tab", "stop_casting"}) || broker.castDeviceName != "Office TV" {
		t.Fatalf("cast broker calls = %v device=%q", broker.calls, broker.castDeviceName)
	}
	if broker.lastOpen.URL != "https://example.com/" || !broker.lastOpen.Activate {
		t.Fatalf("open-tab request = %+v", broker.lastOpen)
	}
	if broker.lastNavigate != "https://www.google.com/" {
		t.Fatalf("navigate-tab URL = %q", broker.lastNavigate)
	}
}
func TestCastToolRejectsUnknownModeBeforeCallingBroker(t *testing.T) {
	broker := &recordingBroker{}
	response, err := NewBrokerToolSet(broker, true).Executor().Execute(context.Background(), messages.ToolCall{
		ID: "cast-invalid", Name: webmcp.CastTabToolName, Arguments: `{"device_name":"Office TV","mode":"window"}`,
	})
	if err != nil {
		t.Fatalf("execute invalid cast mode: %v", err)
	}
	envelope, err := webmcp.UnmarshalToolResult([]byte(response.Content))
	if err != nil || envelope.OK {
		t.Fatalf("invalid cast mode result = %s, err=%v", response.Content, err)
	}
	if len(broker.calls) != 0 {
		t.Fatalf("invalid cast mode reached broker: %v", broker.calls)
	}
}
func TestOpenTabCreatesSelectsAndActivatesRequestedWebsite(t *testing.T) {
	want := webmcp.PageContext{
		Key:       webmcp.PageKey{BrowserID: "browser-a", TargetID: "tab-new"},
		URL:       "https://notes.example.test/",
		Origin:    "https://notes.example.test",
		Connected: true,
		Ready:     true,
	}
	broker := &recordingBroker{selected: want}
	response, err := NewBrokerToolSet(broker).Executor().Execute(context.Background(), messages.ToolCall{
		ID:        "open-tab-call",
		Name:      webmcp.OpenTabToolName,
		Arguments: `{"url":"https://notes.example.test/","activate":true}`,
	})
	if err != nil {
		t.Fatalf("open tab: %v", err)
	}
	envelope, err := webmcp.UnmarshalToolResult([]byte(response.Content))
	if err != nil || !envelope.OK {
		t.Fatalf("open-tab envelope = %#v (err %v), want success", envelope, err)
	}
	if broker.lastOpen.URL != want.URL || !broker.lastOpen.Activate || broker.lastOpen.BrowserID != "" {
		t.Fatalf("open-tab request = %+v", broker.lastOpen)
	}
	var selected selectionData
	if err := json.Unmarshal(envelope.Data, &selected); err != nil {
		t.Fatalf("decode open-tab selection: %v", err)
	}
	if selected.BrowserID != want.Key.BrowserID || selected.TargetID != want.Key.TargetID || !selected.Connected || !selected.Ready {
		t.Fatalf("open-tab selection = %+v, want %+v", selected, want)
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
	assertShowPageRichResponse(t, response, "show-call", imageBytes)
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
func TestComposedShowPagePreservesItsSingleImageProjection(t *testing.T) {
	imageBytes := testPNG(t, 2, 2)
	broker := &recordingBroker{
		selected:   webmcp.PageContext{Key: webmcp.PageKey{BrowserID: "browser-a", TargetID: "tab-a"}},
		screenshot: webmcp.PageScreenshot{MIMEType: "image/png", Bytes: imageBytes},
	}
	set := NewBrokerToolSet(broker)
	composed, err := runtimeToolsWire.NewService().Resolve(context.Background(), runtimeTools.Request{
		Browser: &runtimeTools.BrowserSurface{
			Executor:    set.Executor(),
			Definitions: set.Definitions(),
		},
	})
	if err != nil {
		t.Fatalf("compose browser surface: %v", err)
	}
	response, err := composed.Executor.Execute(context.Background(), messages.ToolCall{
		ID:        "composed-show-call",
		Name:      webmcp.ShowPageToolName,
		Arguments: `{}`,
	})
	if err != nil {
		t.Fatalf("composed show_page: %v", err)
	}
	assertShowPageRichResponse(t, response, "composed-show-call", imageBytes, 2, 2)
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

func TestGetContextReportsNoPageSelectedBeforeStaleSelection(t *testing.T) {
	broker := webmcp.NewBroker(webmcp.BrokerOptions{})
	defer func() { _ = broker.Close() }()

	response, err := NewBrokerToolSet(broker).Executor().Execute(context.Background(), messages.ToolCall{
		ID:        "no-page-call",
		Name:      webmcp.GetContextToolName,
		Arguments: `{}`,
	})
	if err != nil {
		t.Fatalf("get context without selection: %v", err)
	}
	envelope, err := webmcp.UnmarshalToolResult([]byte(response.Content))
	if err != nil {
		t.Fatalf("decode no-page context: %v", err)
	}
	assertNoPageSelectedContextError(t, envelope)

	staleBroker := &recordingBroker{
		selectedErr: webmcp.NewClassifiedError(webmcp.ErrorStaleSelection, webmcp.DefaultErrorMessage(webmcp.ErrorStaleSelection), map[string]any{
			"browser_id":          "browser-a",
			"target_id":           "target-a",
			"selected_generation": uint64(3),
			"reason":              "generation_changed",
		}),
	}
	response, err = NewBrokerToolSet(staleBroker).Executor().Execute(context.Background(), messages.ToolCall{
		ID:        "stale-page-call",
		Name:      webmcp.GetContextToolName,
		Arguments: `{}`,
	})
	if err != nil {
		t.Fatalf("get context with stale selection: %v", err)
	}
	envelope, err = webmcp.UnmarshalToolResult([]byte(response.Content))
	if err != nil {
		t.Fatalf("decode stale context: %v", err)
	}
	if envelope.OK || envelope.Error == nil || envelope.Error.Code != string(webmcp.ErrorStaleSelection) {
		t.Fatalf("stale context envelope = %+v, want stale_selection", envelope)
	}
	if envelope.Error.Message != webmcp.DefaultErrorMessage(webmcp.ErrorStaleSelection) {
		t.Fatalf("stale context message = %q, want %q", envelope.Error.Message, webmcp.DefaultErrorMessage(webmcp.ErrorStaleSelection))
	}
	if details := envelope.Error.Details; details["browser_id"] != "browser-a" || details["target_id"] != "target-a" || details["selected_generation"] != float64(3) || details["reason"] != "generation_changed" {
		t.Fatalf("stale context details = %#v, want retained stale identity", details)
	}
}

func assertNoPageSelectedContextError(t *testing.T, envelope webmcp.ToolResultEnvelope) {
	t.Helper()
	if envelope.OK || envelope.Error == nil || envelope.Error.Code != string(webmcp.ErrorStaleSelection) {
		t.Fatalf("no-page context envelope = %+v, want stale_selection failure", envelope)
	}
	if envelope.Error.Message != "no page is selected" {
		t.Fatalf("no-page context message = %q, want exact no-selection message", envelope.Error.Message)
	}
	details := envelope.Error.Details
	if details["browser_id"] != "" || details["target_id"] != "" || details["selected_generation"] != float64(0) || details["reason"] != "selection_not_connected" {
		t.Fatalf("no-page context details = %#v, want empty identity at generation zero", details)
	}
}

func TestShowPageNamespaceIsPreflightedWithStaticTools(t *testing.T) {
	validator, ok := runtimeToolsWire.NewService().(runtimeTools.DefinitionValidator)
	if !ok {
		t.Fatal("runtime tools service does not expose definition validation")
	}
	err := validator.ValidateToolDefinitionNamespaces(
		[]messages.ToolDefinition{{Name: webmcp.ShowPageToolName}},
		NewBrokerToolSet(nil).Definitions(),
	)
	if !errors.Is(err, runtimeTools.ErrToolCompositionCollision) {
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

func TestToolSetExecutorUsesTheSameTextualContract(t *testing.T) {
	broker := &recordingBroker{selected: webmcp.PageContext{Key: webmcp.PageKey{BrowserID: "browser-a", TargetID: "tab-a"}, Generation: 1}}
	set := NewToolSet(broker)
	if got := len(set.Definitions()); got != 9 {
		t.Fatalf("definition count = %d, want six stable tools plus open-tab, navigate-tab, and show_page", got)
	}
	response, err := set.Executor().Execute(context.Background(), messages.ToolCall{
		ID: "call-get-context", Name: webmcp.GetContextToolName, Arguments: `{"refresh":false}`,
	})
	if err != nil {
		t.Fatalf("executor execute: %v", err)
	}
	if response.Content == "" {
		t.Fatalf("executor result = %#v, want one textual tool response", response)
	}
	if _, err := webmcp.UnmarshalToolResult([]byte(response.Content)); err != nil {
		t.Fatalf("executor result envelope: %v", err)
	}
}

func TestToolSetExecutorPreservesShowPageImageProjection(t *testing.T) {
	imageBytes := testPNG(t, 1, 1)
	set := NewToolSet(&recordingBroker{
		selected:   webmcp.PageContext{Key: webmcp.PageKey{BrowserID: "browser-a", TargetID: "tab-a"}},
		screenshot: webmcp.PageScreenshot{MIMEType: "image/png", Bytes: imageBytes},
	})
	response, err := set.Executor().Execute(context.Background(), messages.ToolCall{
		ID: "call-show-page", Name: webmcp.ShowPageToolName, Arguments: `{}`,
	})
	if err != nil {
		t.Fatalf("executor show_page: %v", err)
	}
	if len(response.ContentParts) != 2 {
		t.Fatalf("executor show_page result = %#v, want metadata plus one image part", response)
	}
	part, ok := response.ContentParts[1].(messages.ImagePart)
	if !ok || !bytes.Equal(part.Bytes, imageBytes) || part.MediaType != "image/png" {
		t.Fatalf("executor show_page image = %#v, want exact PNG projection", response.ContentParts[1])
	}
}

type recordingBroker struct {
	selected       webmcp.PageContext
	selectedErr    error
	targets        []webmcp.Target
	catalog        webmcp.ToolCatalogSnapshot
	invokeResult   webmcp.InvokeResult
	lastInvoke     webmcp.InvokeRequest
	lastCancel     webmcp.CancelRequest
	lastOpen       webmcp.OpenTabRequest
	lastNavigate   string
	screenshot     webmcp.PageScreenshot
	screenshotErr  error
	calls          []string
	castDevices    []webmcp.CastDevice
	castDeviceName string
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
	return b.selected, b.selectedErr
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

func (b *recordingBroker) OpenTab(_ context.Context, request webmcp.OpenTabRequest) (webmcp.PageContext, error) {
	b.calls = append(b.calls, "open_tab")
	b.lastOpen = request
	return b.selected, b.selectedErr
}

func (b *recordingBroker) NavigateSelectedTab(_ context.Context, targetURL string) (webmcp.PageContext, error) {
	b.calls = append(b.calls, "navigate_tab")
	b.lastNavigate = targetURL
	b.selected.URL = targetURL
	return b.selected, b.selectedErr
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

func (b *recordingBroker) ListCastDevices(context.Context) ([]webmcp.CastDevice, error) {
	b.calls = append(b.calls, "list_cast_devices")
	return append([]webmcp.CastDevice(nil), b.castDevices...), nil
}

func (b *recordingBroker) CastSelectedTab(_ context.Context, deviceName string) error {
	b.calls = append(b.calls, "cast_tab")
	b.castDeviceName = deviceName
	return nil
}

func (b *recordingBroker) CastSelectedMedia(_ context.Context, deviceName string) error {
	b.calls = append(b.calls, "cast_media")
	b.castDeviceName = deviceName
	return nil
}

func (b *recordingBroker) StopCasting(_ context.Context, deviceName string) error {
	b.calls = append(b.calls, "stop_casting")
	b.castDeviceName = deviceName
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
	return b.selected, b.selectedErr
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

func assertShowPageRichResponse(t *testing.T, response messages.ToolCallResponse, callID string, wantBytes []byte, dimensions ...int) {
	t.Helper()
	width, height := 3, 2
	if len(dimensions) == 2 {
		width, height = dimensions[0], dimensions[1]
	}
	if response.ToolCallID != callID || response.Name != webmcp.ShowPageToolName {
		t.Fatalf("response correlation = (%q, %q), want (%q, %q)", response.ToolCallID, response.Name, callID, webmcp.ShowPageToolName)
	}
	if len(response.ContentParts) != 2 || response.Content == "" {
		t.Fatalf("response = %#v, want one metadata part and one image part", response)
	}
	result, err := sight.Decode([]byte(response.Content))
	if err != nil {
		t.Fatalf("decode show_page sight result: %v", err)
	}
	if result.Version != ShowPageResultVersion || result.Status != ShowPageResultStatusSuccess || result.Source != showPageSource || result.BrowserID != "browser-a" || result.TargetID != "tab-a" || result.MIMEType != "image/png" || result.ByteLength != len(wantBytes) || result.Width != width || result.Height != height || result.TypedProjection != ShowPageResultTypedProjectionInputImage {
		t.Fatalf("show_page sight result = %+v", result)
	}
	imagePart, ok := response.ContentParts[1].(messages.ImagePart)
	if !ok || string(imagePart.Bytes) != string(wantBytes) || imagePart.MediaType != "image/png" {
		t.Fatalf("show_page image part = %#v, want exact PNG projection", response.ContentParts[1])
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
		webmcp.GetContextToolName:  {"refresh"},
		webmcp.ListTabsToolName:    {"browser_id", "origin_contains", "eligible_only", "include_zero_tool_pages"},
		webmcp.SelectTabToolName:   {"browser_id", "target_id", "activate"},
		webmcp.OpenTabToolName:     {"browser_id", "url", "activate"},
		webmcp.NavigateTabToolName: {"url"},
		webmcp.ListToolsToolName:   {"refresh", "name_contains", "include_schemas", "frame_id"},
		webmcp.InvokeToolName:      {"tool_ref", "input_json", "reason"},
		webmcp.CancelToolName:      {"invocation_id", "reason"},
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
