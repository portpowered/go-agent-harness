package openaichatgpt_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"
	"testing/synctest"
	"time"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openai/chatgptauth"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openaichatgpt"
)

// stallingTransport answers with a 200 SSE body that sends one event and
// then nothing, without any network.
type stallingTransport struct{ writer *io.PipeWriter }

func (s *stallingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	reader, writer := io.Pipe()
	s.writer = writer
	go func() {
		if _, err := writer.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"par\"}\n\n")); err != nil {
			return
		}
	}()
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: reader, Request: req}, nil
}

type staticSource struct{}

func (staticSource) Credential(context.Context) (chatgptauth.Credential, error) {
	return chatgptauth.Credential{AccessToken: "tok", AccountID: "acct"}, nil
}

func TestConfiguredStreamIdleTimeoutFailsAStalledTurn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		transport := &stallingTransport{}
		const idle = 30 * time.Second
		provider := openaichatgpt.New(staticSource{}, openaichatgpt.WithModel("gpt-test"),
			openaichatgpt.WithHTTPClient(&http.Client{Transport: transport}), openaichatgpt.WithStreamIdleTimeout(idle))
		start := time.Now()
		_, err := provider.Infer(t.Context(), userPrompt("hi"))
		if !errors.Is(err, openaichatgpt.ErrStreamIdle) || !errors.Is(err, providers.ErrTransport) {
			t.Fatalf("error = %v, want ErrStreamIdle", err)
		}
		if waited := time.Since(start); waited != idle {
			t.Fatalf("failed after %v, want the configured %v", waited, idle)
		}
		if err := transport.writer.Close(); err != nil {
			t.Fatalf("close writer: %v", err)
		}
	})
}
