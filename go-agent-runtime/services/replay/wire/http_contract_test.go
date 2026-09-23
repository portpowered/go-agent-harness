package wire

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
)

func TestHTTPReplayPublicContractMatchesRequestsAndReturnsRecordedResponses(t *testing.T) {
	captures := []recording.HTTPCapturePair{
		{
			Request: recording.HTTPCapturedRequest{
				Method: http.MethodPost,
				URL:    "https://provider.example.test/chat/completions",
				Body:   recording.HTTPBody(`{"messages":[{"role":"user","content":"hello"}]}`),
			},
			Response: recording.HTTPCapturedResponse{
				StatusCode: http.StatusCreated,
				Status:     "201 Created",
				Headers:    recording.HTTPHeaders{"X-Recorded": {"true"}},
				Body:       recording.HTTPBody(`{"id":"response-1"}`),
			},
		},
		{
			Request: recording.HTTPCapturedRequest{
				Method: http.MethodGet,
				URL:    "https://provider.example.test/status",
			},
			Response: recording.HTTPCapturedResponse{
				StatusCode: http.StatusNoContent,
				Status:     "204 No Content",
			},
		},
	}
	path := filepath.Join(t.TempDir(), "http-capture.json")
	transport := newHTTPReplayTransport(t, path, captures)
	requestBody := `{"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`
	request, err := http.NewRequest(http.MethodPost, "https://provider.example.test/chat/completions", strings.NewReader(requestBody))
	if err != nil {
		t.Fatal(err)
	}
	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusCreated || response.Header.Get("X-Recorded") != "true" || string(body) != `{"id":"response-1"}` {
		t.Fatalf("HTTP replay response = status %d, headers %v, body %q", response.StatusCode, response.Header, body)
	}
	if got, err := io.ReadAll(request.Body); err != nil || string(got) != requestBody {
		t.Fatalf("replay consumed the caller request body: body=%q error=%v", got, err)
	}

	request, err = http.NewRequest(http.MethodGet, "https://provider.example.test/status", nil)
	if err != nil {
		t.Fatal(err)
	}
	statusResponse, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	if statusResponse.StatusCode != http.StatusNoContent {
		t.Fatalf("bodyless replay status = %d, want 204", statusResponse.StatusCode)
	}
	if err := statusResponse.Body.Close(); err != nil {
		t.Fatalf("close bodyless replay response: %v", err)
	}

	request, err = http.NewRequest(http.MethodPost, "https://provider.example.test/chat/completions", strings.NewReader(`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.test/image"}}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	divergentResponse, divergentErr := transport.RoundTrip(request)
	if divergentResponse != nil && divergentResponse.Body != nil {
		if err := divergentResponse.Body.Close(); err != nil {
			t.Fatalf("close divergent HTTP response: %v", err)
		}
	}
	if divergentErr == nil || !strings.Contains(divergentErr.Error(), "no matching captures") {
		t.Fatalf("divergent HTTP request error = %v, want no matching capture", divergentErr)
	}
}

func newHTTPReplayTransport(t *testing.T, path string, captures []recording.HTTPCapturePair) http.RoundTripper {
	t.Helper()
	data, err := json.Marshal(captures)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	httpService, ok := NewService().(replay.HTTPReplayService)
	if !ok {
		t.Fatal("replay service does not expose its HTTP capture contract")
	}
	value, err := httpService.OpenHTTPReplay(path)
	if err != nil {
		t.Fatal(err)
	}
	transport, ok := value.(http.RoundTripper)
	if !ok {
		t.Fatalf("HTTP replay result %T is not a transport", value)
	}
	return transport
}
