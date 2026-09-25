package integration

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

// TestRecordReplayStateless exercises the HTTP replay round-tripper by loading
// the streaming_2_2.json fixture (a recorded "what is 2 + 2?" response from
// OpenRouter) and replaying a matching request against it.
//
// This validates the core replay mechanism: fixture loading, request matching,
// and response reconstruction. The fixture file is checked into testdata/ for
// CI reproducibility.
func TestRecordReplayStateless(t *testing.T) {
	fixturePath := locateCLIFixture(t, "streaming_2_2.json")

	replayRT, err := gwtesting.NewReplayRoundTripper(fixturePath)
	if err != nil {
		t.Fatalf("load replay transport: %v", err)
	}

	// Build a request matching the fixture's captured request shape:
	// single user message with text content, same URL/method as the fixture.
	reqBody := `{"messages":[{"content":[{"text":"what is 2 + 2?","type":"text"}],"role":"user"}],"model":"z-ai/glm-4.7","tools":[{"function":{"name":"edit_file","description":"Edit a file","parameters":{"properties":{"path":{"type":"string"}},"required":["path"],"type":"object"}},"type":"function"}],"stream":true}`
	req, err := http.NewRequest("POST", "https://openrouter.ai/api/v1/chat/completions", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := replayRT.RoundTrip(req)
	if err != nil {
		t.Fatalf("replay round trip: %v", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Errorf("close replay response: %v", err)
		}
	}()

	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read replay response: %v", err)
	}
	if len(body) == 0 {
		t.Error("expected non-empty response body from replay")
	}

	// The fixture response is a streaming SSE response containing "4".
	if !strings.Contains(string(body), "4") {
		bodyPreview := string(body)
		if len(bodyPreview) > 500 {
			bodyPreview = bodyPreview[:500]
		}
		t.Errorf("expected response body to contain '4'; got:\n%s", bodyPreview)
	}
}

func locateSharedSessionFixture(t *testing.T, name string) string {
	t.Helper()

	path := gwtesting.SharedSessionFixturePath(name)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("shared session fixture %q not found at %q: %v", name, path, err)
	}
	return path
}

func locateCLIFixture(t *testing.T, name string) string {
	t.Helper()

	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve CLI fixture helper path: runtime.Caller failed")
	}

	path := filepath.Join(filepath.Dir(currentFile), "testdata", name)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("CLI fixture %q not found at %q: %v", name, path, err)
	}
	return path
}
