package agent

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gwtesting "github.com/portpowered/go-llm-gateway/pkg/testing"
)

func TestBuildProviderHTTPRuntime_LiveModeUsesExplicitDefaultTransport(t *testing.T) {
	runtime, err := buildProviderHTTPRuntime(&Config{})
	if err != nil {
		t.Fatalf("buildProviderHTTPRuntime() error = %v", err)
	}
	if runtime.Client == nil {
		t.Fatal("expected HTTP client")
	}
	if runtime.Client.Transport != http.DefaultTransport {
		t.Fatalf("transport = %#v, want http.DefaultTransport", runtime.Client.Transport)
	}
	if runtime.Recorder != nil {
		t.Fatal("expected no recorder in live mode")
	}
}

func TestBuildProviderHTTPRuntime_RecordModeReturnsRecorderBackedClient(t *testing.T) {
	runtime, err := buildProviderHTTPRuntime(&Config{RecordCapturePath: filepath.Join(t.TempDir(), "capture.json")})
	if err != nil {
		t.Fatalf("buildProviderHTTPRuntime() error = %v", err)
	}
	if runtime.Client == nil {
		t.Fatal("expected HTTP client")
	}
	if runtime.Recorder == nil {
		t.Fatal("expected recorder in record mode")
	}
	if runtime.Client.Transport != runtime.Recorder {
		t.Fatal("expected recorder transport to back the client")
	}
}

func TestBuildProviderHTTPRuntime_RecordModeCapturesRoundTripAndFlushes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer server.Close()

	runtime, err := buildProviderHTTPRuntime(&Config{RecordCapturePath: filepath.Join(t.TempDir(), "capture.json")})
	if err != nil {
		t.Fatalf("buildProviderHTTPRuntime() error = %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", strings.NewReader(`{"message":"hello"}`))
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := runtime.Client.Do(req)
	if err != nil {
		t.Fatalf("runtime.Client.Do() error = %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("io.ReadAll() error = %v", err)
	}
	if string(body) != `{"ok":true}` {
		t.Fatalf("response body = %q, want %q", string(body), `{"ok":true}`)
	}

	capturePath := filepath.Join(t.TempDir(), "captured.json")
	if err := runtime.Recorder.FlushToFile(capturePath); err != nil {
		t.Fatalf("FlushToFile() error = %v", err)
	}

	raw, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatalf("os.ReadFile() error = %v", err)
	}

	var captures []gwtesting.CapturePair
	if err := json.Unmarshal(raw, &captures); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if len(captures) != 1 {
		t.Fatalf("len(captures) = %d, want 1", len(captures))
	}
	if captures[0].Request.URL != server.URL+"/v1/chat/completions" {
		t.Fatalf("captures[0].Request.URL = %q, want %q", captures[0].Request.URL, server.URL+"/v1/chat/completions")
	}
	if string(captures[0].Request.Body) != `{"message":"hello"}` {
		t.Fatalf("captures[0].Request.Body = %q, want %q", string(captures[0].Request.Body), `{"message":"hello"}`)
	}
	if string(captures[0].Response.Body) != `{"ok":true}` {
		t.Fatalf("captures[0].Response.Body = %q, want %q", string(captures[0].Response.Body), `{"ok":true}`)
	}
}

func TestBuildProviderHTTPRuntime_ReplayModeUsesReplayTransport(t *testing.T) {
	fixturePath := filepath.Join("..", "..", "test", "integration", "testdata", "streaming_2_2.json")
	runtime, err := buildProviderHTTPRuntime(&Config{ReplayCapturePath: fixturePath})
	if err != nil {
		t.Fatalf("buildProviderHTTPRuntime() error = %v", err)
	}
	if runtime.Client == nil {
		t.Fatal("expected HTTP client")
	}
	if runtime.Client.Transport == nil {
		t.Fatal("expected replay transport")
	}
	if runtime.Client.Transport == http.DefaultTransport {
		t.Fatal("expected replay transport, got http.DefaultTransport")
	}
	if runtime.Recorder != nil {
		t.Fatal("expected no recorder in replay mode")
	}
}

func TestBuildProviderHTTPRuntime_ReplayModeServesCapturedResponse(t *testing.T) {
	fixturePath := filepath.Join("..", "..", "test", "integration", "testdata", "streaming_2_2.json")
	runtime, err := buildProviderHTTPRuntime(&Config{ReplayCapturePath: fixturePath})
	if err != nil {
		t.Fatalf("buildProviderHTTPRuntime() error = %v", err)
	}

	reqBody := `{"messages":[{"content":[{"text":"what is 2 + 2?","type":"text"}],"role":"user"}],"model":"z-ai/glm-4.7","tools":[{"function":{"name":"edit_file","description":"Edit a file","parameters":{"properties":{"path":{"type":"string"}},"required":["path"],"type":"object"}},"type":"function"}],"stream":true}`
	req, err := http.NewRequest(http.MethodPost, "https://openrouter.ai/api/v1/chat/completions", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := runtime.Client.Do(req)
	if err != nil {
		t.Fatalf("runtime.Client.Do() error = %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("io.ReadAll() error = %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("resp.StatusCode = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if !strings.Contains(string(body), "4") {
		t.Fatalf("response body %q should contain replayed answer", string(body))
	}
}

func TestBuildProviderHTTPRuntime_ReplayModePropagatesFixtureErrors(t *testing.T) {
	_, err := buildProviderHTTPRuntime(&Config{ReplayCapturePath: filepath.Join(t.TempDir(), "missing.json")})
	if err == nil {
		t.Fatal("expected replay fixture error")
	}
}
