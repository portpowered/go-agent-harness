package fal

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"testing"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
)

// TestMain keeps the package hermetic. New() defaults to an http.Client that
// uses http.DefaultTransport, so a test that forgets WithHTTPClient would
// otherwise reach the live fal.run endpoint. Every non-loopback request made
// through the default transport now fails with an explicit error.
func TestMain(m *testing.M) {
	http.DefaultTransport = loopbackOnlyTransport{next: http.DefaultTransport}
	os.Exit(m.Run())
}

type loopbackOnlyTransport struct {
	next http.RoundTripper
}

func (t loopbackOnlyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	host := req.URL.Hostname()
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return nil, fmt.Errorf("hermetic test attempted a real network request to %s; inject WithHTTPClient", req.URL.Host)
	}
	return t.next.RoundTrip(req)
}

// TestFalProvider_Infer_QwenTTS_MissingEmbeddingUsesDefaultVoice proves a
// text-only Qwen TTS request is valid: without a speaker embedding the
// provider asks fal.ai for the default Vivian voice. (This test used to call
// the live fal.run endpoint and passed only because that call failed.)
func TestFalProvider_Infer_QwenTTS_MissingEmbeddingUsesDefaultVoice(t *testing.T) {
	ctx := context.Background()
	transport := &mockTransport{
		statusCode: 200,
		body:       `{"audio":{"url":"https://storage.example.com/vivian.mp3","content_type":"audio/mpeg"}}`,
	}
	p := New(WithAPIKey("test-key"), WithHTTPClient(&http.Client{Transport: transport}))

	req := providers.InferenceRequest{
		Model: ModelQwenTTS,
		Messages: []models.Message{{
			Role:         models.RoleUser,
			ContentParts: []models.ContentPart{models.TextPart{Text: "Say hello"}},
		}},
	}

	resp, err := p.Infer(ctx, req)
	if err != nil {
		t.Fatalf("Infer() unexpected error: %v", err)
	}
	var body qwenTTSRequest
	if err := json.Unmarshal(transport.lastBody, &body); err != nil {
		t.Fatalf("request body JSON: %v", err)
	}
	if body.SpeakerEmbeddingURL != "" || body.Voice != string(QwenVoiceVivian) || body.Text != "Say hello" {
		t.Fatalf("request body = %+v, want text with the default %s voice and no embedding", body, QwenVoiceVivian)
	}
	if got := resp.Message.TextContent(); got != "https://storage.example.com/vivian.mp3" {
		t.Fatalf("Message.TextContent() = %q, want the returned audio URL", got)
	}
}
