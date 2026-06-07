package integration

import (
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	gwtesting "github.com/portpowered/go-llm-gateway/pkg/testing"
)

// TestRecordReplayStateless exercises the HTTP replay round-tripper by loading
// the streaming_2_2.json fixture (a recorded "what is 2 + 2?" response from
// OpenRouter) and replaying a matching request against it.
//
// This validates the core replay mechanism: fixture loading, request matching,
// and response reconstruction. The fixture file is checked into testdata/ for
// CI reproducibility.
func TestRecordReplayStateless(t *testing.T) {
	fixturePath := locateFixture(t, "streaming_2_2.json")

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
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
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

// locateFixture finds a fixture file relative to the module root or test dir.
func locateFixture(t *testing.T, name string) string {
	t.Helper()
	candidates := []string{
		gwtesting.SharedSessionFixturePath(name),
		"test/integration/testdata/" + name,
		"testdata/" + name,
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	t.Fatalf("fixture %q not found in any of: %v", name, candidates)
	return ""
}
