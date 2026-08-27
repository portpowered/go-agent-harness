package services_test

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
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

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
