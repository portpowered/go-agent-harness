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
	"testing"

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
