package integration

import (
	"context"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	serviceTools "github.com/portpowered/go-agent-harness/agent-cli/internal/services/tools"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/wire"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// sessionLifecycleSafetyTimeout bounds each lifecycle wait in the hermetic
// tool-result session tests.
const sessionLifecycleSafetyTimeout = 10 * time.Second

func waitLifecycleSignal(t *testing.T, signal <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(sessionLifecycleSafetyTimeout):
		t.Fatalf("timed out waiting for %s after %s", name, sessionLifecycleSafetyTimeout)
	}
}

// liveToolSessionOptions describes one hermetic session run through the
// composed CLI with the application's session-inferencer and tool-service
// ports replaced by test doubles.
type liveToolSessionOptions struct {
	inferencer messages.SessionInferencer
	executor   messages.ToolExecutor
	toolNames  []string
	observer   func(messages.StreamMessage)
	output     io.Writer
	// inputPCM, when present, is admitted as one scheduled customer audio
	// turn (--audio-in-turn, which requires a --record-dir bundle).
	inputPCM []byte
	args     []string
}

// runLiveToolSession runs the production live session path for one
// invocation and returns its command result.
func runLiveToolSession(ctx context.Context, t *testing.T, options liveToolSessionOptions) error {
	t.Helper()
	definitions := make([]messages.ToolDefinition, 0, len(options.toolNames))
	for _, name := range options.toolNames {
		definitions = append(definitions, messages.ToolDefinition{Name: name, Description: "Hermetic " + name + " fixture."})
	}
	capabilities := serviceTools.Factory(func(*config.Config) (serviceTools.Capabilities, error) {
		return serviceTools.Capabilities{Executor: options.executor, Definitions: definitions}, nil
	})
	agentCLI, err := wire.InitializeMockAgentCLIWithPorts(
		wire.NewToolServicePort(capabilities),
		wire.NewPortSwap(wire.PortInferencer, &mockInferencer{response: "unused"}),
		wire.NewPortSwap(wire.PortSessionInferencer, options.inferencer),
	)
	if err != nil {
		t.Fatalf("initialize live tool session CLI: %v", err)
	}
	if options.observer != nil {
		agentCLI.SetSessionStreamObserver(options.observer)
	}
	output := options.output
	if output == nil {
		output = io.Discard
	}
	root := agentCLI.Generate()
	root.SetOut(output)
	root.SetErr(io.Discard)
	args := append([]string{"--config-dir", t.TempDir(), "session"}, options.args...)
	if len(options.inputPCM) > 0 {
		inputPath := filepath.Join(t.TempDir(), "customer-turn.wav")
		writeAsyncCollisionInputWAV(t, inputPath, options.inputPCM)
		args = append(args, "--audio-in-turn", inputPath, "--record-dir", filepath.Join(t.TempDir(), "recording"))
	}
	root.SetArgs(args)
	return root.ExecuteContext(ctx)
}
