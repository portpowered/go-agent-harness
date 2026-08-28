package services_test

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/cli"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

func TestRunSession_OpenAIAdvertisesRegistryExecDefinition(t *testing.T) {
	registry := tools.NewToolRegistry()
	execTool, ok := registry.Get("exec")
	if !ok {
		t.Fatal("default registry does not contain exec")
	}
	definitions := registry.ToAgentLoopDefs()

	configDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(configDir, config.ConfigFileName), []byte("model:\n  provider: openai\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	conn := newRecordingRealtimeTestConn()
	recordPath := filepath.Join(t.TempDir(), "openai-tools.session.json")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := services.RunSession(ctx, io.Discard, services.SessionRunOptions{
		RecordPath:      recordPath,
		Provider:        "openai",
		Model:           "gpt-realtime",
		APIKey:          "test-api-key",
		ConfigDir:       configDir,
		Prompt:          "advertise the default tools",
		ToolExecutor:    tools.NewRegistryExecutor(registry),
		ToolDefinitions: definitions,
		WebSocketDialer: &recordingRealtimeTestDialer{conn: conn},
	})
	if err != nil {
		t.Fatalf("RunSession: %v", err)
	}

	capture, err := gwtesting.LoadSessionCapture(recordPath)
	if err != nil {
		t.Fatalf("LoadSessionCapture: %v", err)
	}
	var sessionUpdate json.RawMessage
	for _, record := range capture.Records {
		if record.Direction == gwtesting.DirectionClientToServer && record.Type == "session.update" {
			sessionUpdate = append(json.RawMessage(nil), record.Payload...)
			break
		}
	}
	if len(sessionUpdate) == 0 {
		t.Fatalf("capture has no outbound session.update: %#v", capture.Records)
	}

	var envelope struct {
		Session struct {
			Tools []struct {
				Type        string `json:"type"`
				Name        string `json:"name"`
				Description string `json:"description"`
				Parameters  struct {
					Type       string `json:"type"`
					Properties map[string]struct {
						Type        string `json:"type"`
						Description string `json:"description"`
					} `json:"properties"`
					Required []string `json:"required"`
				} `json:"parameters"`
			} `json:"tools"`
		} `json:"session"`
	}
	if err := json.Unmarshal(sessionUpdate, &envelope); err != nil {
		t.Fatalf("decode session.update: %v", err)
	}
	if len(envelope.Session.Tools) == 0 {
		t.Fatal("OpenAI session.update advertised no tools")
	}

	advertisedIndex := -1
	for i := range envelope.Session.Tools {
		tool := &envelope.Session.Tools[i]
		if tool.Name == execTool.Name() {
			advertisedIndex = i
			break
		}
	}
	if advertisedIndex < 0 {
		t.Fatalf("OpenAI session.update omitted exec: %#v", envelope.Session.Tools)
	}
	advertised := envelope.Session.Tools[advertisedIndex]
	if advertised.Type != "function" {
		t.Fatalf("exec wire type = %q, want function", advertised.Type)
	}
	if advertised.Description != execTool.Description() {
		t.Fatalf("exec wire description = %q, want registry description %q", advertised.Description, execTool.Description())
	}
	if advertised.Parameters.Type != "object" {
		t.Fatalf("exec parameter schema type = %q, want object", advertised.Parameters.Type)
	}
	command, ok := advertised.Parameters.Properties["command"]
	if !ok {
		t.Fatalf("exec parameter schema omitted command: %#v", advertised.Parameters.Properties)
	}
	if command.Type != "string" || command.Description != "The shell command to execute" {
		t.Fatalf("exec command parameter = %#v, want string command contract", command)
	}
	if !containsString(advertised.Parameters.Required, "command") {
		t.Fatalf("exec parameter schema required = %#v, want command", advertised.Parameters.Required)
	}
}

func TestRunSession_OpenAIAdvertisesComposedWebMCPDefinitions(t *testing.T) {
	cfg := &config.Config{
		Model:   config.ModelConfig{Provider: config.ProviderOpenAI},
		Browser: config.DefaultBrowserConfig(),
	}
	for _, id := range config.DefaultToolIDs {
		cfg.Tools.List = append(cfg.Tools.List, config.ToolEntry{ID: id, Enabled: id == "sleep"})
	}
	cfg.Browser.Tools.Enabled = true

	capabilityFactory := cli.NewSessionToolCapabilitiesFactory(nil, func(config.BrowserConfig) (webmcp.Broker, error) {
		return webmcp.NewBroker(webmcp.BrokerOptions{}), nil
	})
	capabilities, err := capabilityFactory(cfg)
	if err != nil {
		t.Fatalf("resolve composed session capabilities: %v", err)
	}
	if len(capabilities.Definitions) != 7 {
		t.Fatalf("composed definitions = %d, want one static plus six broker definitions", len(capabilities.Definitions))
	}

	conn := newRecordingRealtimeTestConn()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := services.RunSession(ctx, io.Discard, services.SessionRunOptions{
		Provider:            config.ProviderOpenAI,
		Model:               "gpt-realtime",
		APIKey:              "test-api-key",
		LoadedConfig:        cfg,
		BrowserToolsEnabled: true,
		Prompt:              "advertise the browser tools",
		ToolExecutor:        capabilities.Executor,
		ToolDefinitions:     capabilities.Definitions,
		CapabilityClose:     capabilities.Close,
		WebSocketDialer:     &recordingRealtimeTestDialer{conn: conn},
	}); err != nil {
		t.Fatalf("RunSession: %v", err)
	}

	conn.mu.Lock()
	writes := make([][]byte, len(conn.writes))
	for i := range conn.writes {
		writes[i] = append([]byte(nil), conn.writes[i]...)
	}
	conn.mu.Unlock()

	type wireTool struct {
		Type        string `json:"type"`
		Name        string `json:"name"`
		Description string `json:"description"`
		Parameters  struct {
			Type       string `json:"type"`
			Properties map[string]struct {
				Type string `json:"type"`
			} `json:"properties"`
			Required             []string `json:"required"`
			AdditionalProperties *bool    `json:"additionalProperties"`
		} `json:"parameters"`
	}
	var sessionUpdates []struct {
		Type    string `json:"type"`
		Session struct {
			Tools []wireTool `json:"tools"`
		} `json:"session"`
	}
	for _, write := range writes {
		var event struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(write, &event); err != nil {
			t.Fatalf("decode outbound event %q: %v", string(write), err)
		}
		if event.Type != "session.update" {
			continue
		}
		var update struct {
			Type    string `json:"type"`
			Session struct {
				Tools []wireTool `json:"tools"`
			} `json:"session"`
		}
		if err := json.Unmarshal(write, &update); err != nil {
			t.Fatalf("decode session.update %q: %v", string(write), err)
		}
		sessionUpdates = append(sessionUpdates, update)
	}
	if len(sessionUpdates) != 1 {
		t.Fatalf("OpenAI session.update count = %d, want exactly one initial provider configuration; writes=%q", len(sessionUpdates), writes)
	}
	advertised := sessionUpdates[0].Session.Tools
	if len(advertised) != 7 {
		t.Fatalf("OpenAI advertised tools = %d, want one static plus six broker tools: %#v", len(advertised), advertised)
	}

	expectedBroker := map[string]struct {
		description string
		properties  map[string]string
		required    map[string]bool
	}{
		webmcp.GetContextToolName: {
			description: "Return the current selected browser page context.",
			properties:  map[string]string{"refresh": "boolean"},
		},
		webmcp.ListTabsToolName: {
			description: "List browser tabs available for WebMCP selection.",
			properties: map[string]string{
				"browser_id":              "string",
				"origin_contains":         "string",
				"eligible_only":           "boolean",
				"include_zero_tool_pages": "boolean",
			},
		},
		webmcp.SelectTabToolName: {
			description: "Select a browser tab for WebMCP operations.",
			properties: map[string]string{
				"browser_id": "string",
				"target_id":  "string",
				"activate":   "boolean",
			},
			required: map[string]bool{"browser_id": true, "target_id": true},
		},
		webmcp.ListToolsToolName: {
			description: "List tools exposed by the selected WebMCP page.",
			properties: map[string]string{
				"refresh":         "boolean",
				"name_contains":   "string",
				"include_schemas": "boolean",
				"frame_id":        "string",
			},
		},
		webmcp.InvokeToolName: {
			description: "Invoke one page tool from the current catalog.",
			properties: map[string]string{
				"tool_ref":   "string",
				"input_json": "string",
				"reason":     "string",
			},
			required: map[string]bool{"tool_ref": true, "input_json": true, "reason": true},
		},
		webmcp.CancelToolName: {
			description: "Cancel a pending WebMCP invocation.",
			properties: map[string]string{
				"invocation_id": "string",
				"reason":        "string",
			},
			required: map[string]bool{"invocation_id": true},
		},
	}
	seenStatic := false
	seenBroker := make(map[string]bool, len(expectedBroker))
	for _, tool := range advertised {
		if tool.Type != "function" {
			t.Fatalf("wire tool %q type = %q, want function", tool.Name, tool.Type)
		}
		if tool.Name == "sleep" {
			seenStatic = true
			continue
		}
		expected, ok := expectedBroker[tool.Name]
		if !ok {
			t.Fatalf("unexpected advertised tool %q", tool.Name)
		}
		seenBroker[tool.Name] = true
		if tool.Description != expected.description {
			t.Fatalf("tool %q description = %q, want %q", tool.Name, tool.Description, expected.description)
		}
		if tool.Parameters.Type != "object" {
			t.Fatalf("tool %q parameter type = %q, want object", tool.Name, tool.Parameters.Type)
		}
		if tool.Parameters.AdditionalProperties == nil || *tool.Parameters.AdditionalProperties {
			t.Fatalf("tool %q additionalProperties = %#v, want false", tool.Name, tool.Parameters.AdditionalProperties)
		}
		if len(tool.Parameters.Properties) != len(expected.properties) {
			t.Fatalf("tool %q properties = %#v, want %#v", tool.Name, tool.Parameters.Properties, expected.properties)
		}
		for name, wantType := range expected.properties {
			property, ok := tool.Parameters.Properties[name]
			if !ok || property.Type != wantType {
				t.Fatalf("tool %q property %q = %#v, want type %q", tool.Name, name, property, wantType)
			}
		}
		gotRequired := make(map[string]bool, len(tool.Parameters.Required))
		for _, name := range tool.Parameters.Required {
			gotRequired[name] = true
		}
		if len(gotRequired) != len(expected.required) {
			t.Fatalf("tool %q required = %#v, want %#v", tool.Name, gotRequired, expected.required)
		}
		for name := range expected.required {
			if !gotRequired[name] {
				t.Fatalf("tool %q required omitted %q: %#v", tool.Name, name, gotRequired)
			}
		}
	}
	if !seenStatic {
		t.Fatal("OpenAI session.update omitted enabled static sleep tool")
	}
	if len(seenBroker) != len(expectedBroker) {
		t.Fatalf("OpenAI session.update broker tools = %#v, want %#v", seenBroker, expectedBroker)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
