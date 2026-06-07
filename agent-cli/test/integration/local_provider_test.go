package integration

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/portpowered/agent-cli/internal/config"
	"github.com/portpowered/agent-cli/internal/wire"
)

// TestLocalProvider_NoAuthHeader verifies the full integration flow:
// config loading → provider construction → HTTP request with no Authorization header.
func TestLocalProvider_NoAuthHeader(t *testing.T) {
	var mu sync.Mutex
	var capturedHeaders http.Header

	// 1. Start a mock HTTP server returning a valid OpenAI SSE stream response.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		capturedHeaders = r.Header.Clone()
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")

		events := []string{
			`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1700000000,"model":"llama3","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}`,
			`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1700000000,"model":"llama3","choices":[{"index":0,"delta":{"content":"Hello from local model!"},"finish_reason":null}]}`,
			`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1700000000,"model":"llama3","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1700000000,"model":"llama3","choices":[],"usage":{"prompt_tokens":5,"completion_tokens":5,"total_tokens":10}}`,
			`data: [DONE]`,
		}

		flusher, _ := w.(http.Flusher)
		for _, event := range events {
			if _, err := w.Write([]byte(event + "\n\n")); err != nil {
				t.Errorf("write SSE event: %v", err)
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	defer server.Close()

	// 2. Create a temp config directory with local provider pointing to mock server.
	tmpDir := t.TempDir()
	configYAML := `model:
  provider: local
  local:
    model: llama3
    base_url: ` + server.URL + `
`
	configPath := filepath.Join(tmpDir, config.ConfigFileName)
	if err := os.WriteFile(configPath, []byte(configYAML), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// 3. Run ask command with real executor (no inferencer override) to exercise full HTTP path.
	agentCLI, err := wire.InitializeAgentCLI()
	if err != nil {
		t.Fatalf("initialize CLI: %v", err)
	}

	tw := NewTestWriter()
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(tw.Stdout())
	rootCmd.SetErr(tw.Stderr())
	rootCmd.SetArgs([]string{"ask", "--config-dir", tmpDir, "--stream", "hello"})

	ctx := context.Background()
	if err := rootCmd.ExecuteContext(ctx); err != nil {
		t.Fatalf("execute ask: %v", err)
	}

	// 4. Assert: no Authorization header sent to the mock server.
	mu.Lock()
	authHeader := capturedHeaders.Get("Authorization")
	mu.Unlock()

	if authHeader != "" {
		t.Errorf("expected no Authorization header for local provider, got %q", authHeader)
	}

	// 5. Assert: response parsed correctly.
	output := strings.TrimSpace(tw.StdoutString())
	if !strings.Contains(output, "Hello from local model!") {
		t.Errorf("expected output to contain 'Hello from local model!', got %q", output)
	}
}

// TestLocalProvider_ResponseParsedCorrectly verifies the response is properly parsed
// and the output contains the model's message when using streaming.
func TestLocalProvider_ResponseParsedCorrectly_Streaming(t *testing.T) {
	// Start mock server that returns SSE stream.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		events := []string{
			`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1700000000,"model":"llama3","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}`,
			`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1700000000,"model":"llama3","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}`,
			`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1700000000,"model":"llama3","choices":[{"index":0,"delta":{"content":" from streaming!"},"finish_reason":null}]}`,
			`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1700000000,"model":"llama3","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1700000000,"model":"llama3","choices":[],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`,
			`data: [DONE]`,
		}

		flusher, _ := w.(http.Flusher)
		for _, event := range events {
			if _, err := w.Write([]byte(event + "\n\n")); err != nil {
				t.Errorf("write SSE event: %v", err)
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	configYAML := `model:
  provider: local
  local:
    model: llama3
    base_url: ` + server.URL + `
`
	configPath := filepath.Join(tmpDir, config.ConfigFileName)
	if err := os.WriteFile(configPath, []byte(configYAML), 0600); err != nil {
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
	rootCmd.SetArgs([]string{"ask", "--config-dir", tmpDir, "--stream", "hello"})

	if err := rootCmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute ask --stream: %v", err)
	}

	output := strings.TrimSpace(tw.StdoutString())
	if !strings.Contains(output, "Hello from streaming!") {
		t.Errorf("expected streaming output to contain 'Hello from streaming!', got %q", output)
	}
}
