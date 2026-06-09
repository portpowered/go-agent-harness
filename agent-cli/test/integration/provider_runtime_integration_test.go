package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/wire"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

func TestAskReplayUsesInjectedRuntimeWithoutLiveCredentials(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, config.ConfigFileName)
	configYAML := `model:
  provider: openrouter
  openrouter:
    model: z-ai/glm-4.7
    api_key: replay-dummy
`
	if err := os.WriteFile(configPath, []byte(configYAML), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	agentCLI, err := wire.InitializeAgentCLI()
	if err != nil {
		t.Fatalf("initialize CLI: %v", err)
	}

	tw := NewTestWriter()
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(tw.Stdout())
	rootCmd.SetErr(tw.Stderr())
	rootCmd.SetArgs([]string{
		"ask",
		"--config-dir", tmpDir,
		"--system-prompt", "none",
		"--no-system-information",
		"--stream",
		"--replay", locateCLIFixture(t, "streaming_2_2.json"),
		"what is 2 + 2?",
	})

	if err := rootCmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute ask --replay: %v", err)
	}

	output := strings.TrimSpace(tw.StdoutString())
	if !strings.Contains(output, "4") {
		t.Fatalf("expected replay output to contain %q, got %q", "4", output)
	}
}

func TestAskRecordFlushesCaptureFromInjectedRuntime(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")

		events := []string{
			`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1700000000,"model":"llama3","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}`,
			`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1700000000,"model":"llama3","choices":[{"index":0,"delta":{"content":"recorded through runtime seam"},"finish_reason":null}]}`,
			`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1700000000,"model":"llama3","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1700000000,"model":"llama3","choices":[],"usage":{"prompt_tokens":5,"completion_tokens":4,"total_tokens":9}}`,
			`data: [DONE]`,
		}

		flusher, _ := w.(http.Flusher)
		for _, event := range events {
			_, _ = w.Write([]byte(event + "\n\n"))
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, config.ConfigFileName)
	configYAML := `model:
  provider: local
  local:
    model: llama3
    base_url: ` + server.URL + `
`
	if err := os.WriteFile(configPath, []byte(configYAML), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	recordPath := filepath.Join(tmpDir, "capture.json")
	agentCLI, err := wire.InitializeAgentCLI()
	if err != nil {
		t.Fatalf("initialize CLI: %v", err)
	}

	tw := NewTestWriter()
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(tw.Stdout())
	rootCmd.SetErr(tw.Stderr())
	rootCmd.SetArgs([]string{
		"ask",
		"--config-dir", tmpDir,
		"--system-prompt", "none",
		"--no-system-information",
		"--stream",
		"--record", recordPath,
		"hello",
	})

	if err := rootCmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute ask --record: %v", err)
	}

	output := strings.TrimSpace(tw.StdoutString())
	if !strings.Contains(output, "recorded through runtime seam") {
		t.Fatalf("expected record output to contain %q, got %q", "recorded through runtime seam", output)
	}

	raw, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatalf("read record capture: %v", err)
	}

	var captures []gwtesting.CapturePair
	if err := json.Unmarshal(raw, &captures); err != nil {
		t.Fatalf("parse record capture: %v", err)
	}
	if len(captures) != 1 {
		t.Fatalf("len(captures) = %d, want 1", len(captures))
	}
	if captures[0].Request.URL != server.URL+"/chat/completions" {
		t.Fatalf("captures[0].Request.URL = %q, want %q", captures[0].Request.URL, server.URL+"/chat/completions")
	}
	if !strings.Contains(string(captures[0].Response.Body), "recorded through runtime seam") {
		t.Fatalf("record capture response body should contain model text, got %q", string(captures[0].Response.Body))
	}
}
