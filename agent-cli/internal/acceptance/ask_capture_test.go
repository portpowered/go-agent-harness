package acceptance_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/transport/cli"
	providerwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers/wire"
	recordingwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording/wire"
	sessionwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/wire"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

type captureTransport struct{ calls int }

func (transport *captureTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.calls++
	if transport.calls > 1 {
		return nil, fmt.Errorf("replay reached live transport")
	}
	const body = "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"captured answer\"},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
}

type barrierResponseBody struct {
	reader       *strings.Reader
	closeStarted chan struct{}
	release      chan struct{}
	closeOnce    sync.Once
	closeCalls   atomic.Int32
}

func newBarrierResponseBody(body string) *barrierResponseBody {
	return &barrierResponseBody{
		reader:       strings.NewReader(body),
		closeStarted: make(chan struct{}),
		release:      make(chan struct{}),
	}
}

func (body *barrierResponseBody) Read(dst []byte) (int, error) {
	return body.reader.Read(dst)
}

func (body *barrierResponseBody) Close() error {
	body.closeOnce.Do(func() {
		body.closeCalls.Add(1)
		close(body.closeStarted)
		<-body.release
	})
	return nil
}

type barrierCaptureTransport struct {
	body  *barrierResponseBody
	calls atomic.Int32
}

func (transport *barrierCaptureTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.calls.Add(1)
	if transport.calls.Load() > 1 {
		return nil, fmt.Errorf("replay reached live transport")
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       transport.body,
		Request:    request,
	}, nil
}

func TestAskRecordsAndReplaysThroughProviderService(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", streaming), func(t *testing.T) {
			capturePath := filepath.Join(t.TempDir(), "capture.json")
			transport := &captureTransport{}
			provider := providerwire.NewService(providerwire.Dependencies{Recording: recordingwire.NewService(clock.Real{}), HTTPClient: &http.Client{Transport: transport}})
			service := sessionwire.NewService(sessionwire.Dependencies{ProviderService: provider, RelaxValidation: true})
			run := func(mode string) string {
				t.Helper()
				command := cli.NewAskCommand(service, flags.NewAskFlags(), flags.NewLoopFlags(), flags.NewGlobalFlags()).Generate()
				stdout := &bytes.Buffer{}
				command.SetOut(stdout)
				command.SetErr(io.Discard)
				command.SetIn(strings.NewReader(""))
				args := []string{mode, capturePath, "--provider", "openai", "--model", "capture-model", "--base-url", "https://capture.example/v1", "hello"}
				if streaming {
					args = append([]string{"--stream"}, args...)
				}
				command.SetArgs(args)
				if err := command.ExecuteContext(t.Context()); err != nil {
					t.Fatal(err)
				}
				return stdout.String()
			}
			recorded := run("--record")
			assertAskCapture(t, capturePath)
			replayed := run("--replay")
			if recorded != replayed || !strings.Contains(recorded, "captured answer") {
				t.Fatalf("record=%q replay=%q", recorded, replayed)
			}
			if transport.calls != 1 {
				t.Fatalf("live transport calls = %d, want one", transport.calls)
			}
		})
	}
}

func TestAskStreamingCaptureJoinsResponseBodyBeforeFlush(t *testing.T) {
	const responseBody = "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"barrier answer\"},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
	capturePath := filepath.Join(t.TempDir(), "capture.json")
	body := newBarrierResponseBody(responseBody)
	transport := &barrierCaptureTransport{body: body}
	provider := providerwire.NewService(providerwire.Dependencies{
		Recording:  recordingwire.NewService(clock.Real{}),
		HTTPClient: &http.Client{Transport: transport},
	})
	service := sessionwire.NewService(sessionwire.Dependencies{ProviderService: provider, RelaxValidation: true})
	command := cli.NewAskCommand(service, flags.NewAskFlags(), flags.NewLoopFlags(), flags.NewGlobalFlags()).Generate()
	stdout := &bytes.Buffer{}
	command.SetOut(stdout)
	command.SetErr(io.Discard)
	command.SetIn(strings.NewReader(""))
	command.SetArgs([]string{"--stream", "--record", capturePath, "--provider", "openai", "--model", "capture-model", "--base-url", "https://capture.example/v1", "hello"})

	finished := make(chan error, 1)
	go func() { finished <- command.ExecuteContext(t.Context()) }()
	select {
	case <-body.closeStarted:
	case err := <-finished:
		t.Fatalf("streaming ask finished before response-body cleanup: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("streaming ask did not reach the response-body cleanup barrier")
	}
	close(body.release)
	select {
	case err := <-finished:
		if err != nil {
			t.Fatalf("streaming ask failed after response-body cleanup: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("streaming ask did not finish after response-body cleanup")
	}
	if got := body.closeCalls.Load(); got != 1 {
		t.Fatalf("underlying response body close calls = %d, want exactly one", got)
	}
	if got := transport.calls.Load(); got != 1 {
		t.Fatalf("live transport calls = %d, want one before replay", got)
	}

	data, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatal(err)
	}
	var captures []gatewaytesting.CapturePair
	if err := json.Unmarshal(data, &captures); err != nil {
		t.Fatal(err)
	}
	if len(captures) != 1 {
		t.Fatalf("capture count = %d, want one", len(captures))
	}
	if string(captures[0].Response.Body) != responseBody {
		t.Fatalf("capture response body = %q, want complete %q", captures[0].Response.Body, responseBody)
	}
	if captures[0].Request.Headers.Get("Authorization") != "" || captures[0].Response.Headers.Get("Set-Cookie") != "" {
		t.Fatal("capture retained credential-bearing headers")
	}
	if !bytes.Contains(captures[0].Request.Body, []byte("hello")) || !strings.Contains(stdout.String(), "barrier answer") {
		t.Fatalf("capture/output lost request or response: request=%q output=%q", captures[0].Request.Body, stdout.String())
	}

	replay := cli.NewAskCommand(service, flags.NewAskFlags(), flags.NewLoopFlags(), flags.NewGlobalFlags()).Generate()
	replayedOutput := &bytes.Buffer{}
	replay.SetOut(replayedOutput)
	replay.SetErr(io.Discard)
	replay.SetIn(strings.NewReader(""))
	replay.SetArgs([]string{"--stream", "--replay", capturePath, "--provider", "openai", "--model", "capture-model", "--base-url", "https://capture.example/v1", "hello"})
	if err := replay.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if replayedOutput.String() != stdout.String() {
		t.Fatalf("recorded output=%q replayed output=%q", stdout.String(), replayedOutput.String())
	}
	if got := transport.calls.Load(); got != 1 {
		t.Fatalf("live transport calls after replay = %d, want one", got)
	}
}

func assertAskCapture(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var captures []gatewaytesting.CapturePair
	if err := json.Unmarshal(data, &captures); err != nil {
		t.Fatal(err)
	}
	if len(captures) != 1 {
		t.Fatalf("capture count = %d", len(captures))
	}
	if !bytes.Contains(captures[0].Request.Body, []byte("hello")) || !bytes.Contains(captures[0].Response.Body, []byte("captured answer")) {
		t.Fatalf("capture lost request or response payload")
	}
}
