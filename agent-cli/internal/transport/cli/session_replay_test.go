package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	runtimeReplay "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
)

type replayCommandService struct {
	request runtimeReplay.Request
	err     error
}

func (*replayCommandService) Prepare(context.Context, runtimeReplay.Request) (runtimeReplay.Prepared, error) {
	panic("CLI must execute the replay service, not construct its runtime")
}

func (s *replayCommandService) Run(_ context.Context, out io.Writer, request runtimeReplay.Request) (runtimeReplay.Result, error) {
	s.request = request
	_, _ = io.WriteString(out, "recorded answer")
	return runtimeReplay.Result{WireEvents: 7, ToolCalls: 1, Scope: runtimeReplay.EvidenceScope{Protocol: true, Tools: true, RenderTapUnavailable: true}}, s.err
}

func TestSessionReplayCommandReportsVerifiedScopeOnlyOnSuccess(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "incomplete"}[failure], func(t *testing.T) {
			service := &replayCommandService{}
			if failure {
				service.err = runtimeReplay.ErrBundleIncomplete
			}
			cmd := NewSessionReplayCommand(service).Generate()
			var output, diagnostics bytes.Buffer
			cmd.SetOut(&output)
			cmd.SetErr(&diagnostics)
			cmd.SetArgs([]string{"customer-bundle"})
			err := cmd.Execute()
			if !errors.Is(err, service.err) {
				t.Fatalf("error=%v, want %v", err, service.err)
			}
			if service.request.BundlePath != "customer-bundle" {
				t.Fatalf("request=%+v", service.request)
			}
			if strings.Contains(diagnostics.String(), "Replay verified") == failure {
				t.Fatalf("misleading diagnostics: %s", diagnostics.String())
			}
			if !failure && !strings.Contains(diagnostics.String(), "render tap unavailable: true") {
				t.Fatalf("missing scope: %s", diagnostics.String())
			}
			if output.String() != "recorded answer" {
				t.Fatalf("output=%q", output.String())
			}
		})
	}
}
