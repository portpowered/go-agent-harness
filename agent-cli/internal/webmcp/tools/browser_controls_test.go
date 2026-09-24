package tools

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// schemaObject asserts that a decoded schema value is a JSON object.
func schemaObject(t *testing.T, value any, what string) map[string]any {
	t.Helper()
	object, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("%s = %#v, want object", what, value)
	}
	return object
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
	castFunction := schemaObject(t, schemas[10]["function"], "cast function")
	castParameters := schemaObject(t, castFunction["parameters"], "cast parameters")
	castProperties := schemaObject(t, castParameters["properties"], "cast properties")
	modeSchema := schemaObject(t, castProperties["mode"], "cast mode schema")
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
