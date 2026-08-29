package services

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/inference"
)

func TestLivePlannerFamiliesUseOneGroundingComposition(t *testing.T) {
	toolDefinitions := []messages.ToolDefinition{
		{Name: "read_file", Description: "Read a UTF-8 file."},
		{Name: "exec", Description: "Execute a command."},
	}

	tests := []struct {
		name  string
		build func(t *testing.T, opts SessionRunOptions) (sessionRuntimePlan, func(), error)
	}{
		{
			name: "ordinary text",
			build: func(_ *testing.T, opts SessionRunOptions) (sessionRuntimePlan, func(), error) {
				plan, err := planSessionWithResolvedInstructions(opts, "customer instructions")
				return plan, func() {}, err
			},
		},
		{
			name: "recording directory",
			build: func(_ *testing.T, opts SessionRunOptions) (sessionRuntimePlan, func(), error) {
				return planSessionForDirectoryRecordingWithInstructions(opts, "customer instructions", true)
			},
		},
		{
			name: "scheduled audio",
			build: func(_ *testing.T, opts SessionRunOptions) (sessionRuntimePlan, func(), error) {
				opts.AudioInputs = []ScheduledAudioInput{{PCM: []byte{1, 2, 3}}}
				plan, err := planSessionWithResolvedInstructions(opts, "customer instructions")
				return plan, func() {}, err
			},
		},
		{
			name: "image-composed",
			build: func(_ *testing.T, opts SessionRunOptions) (sessionRuntimePlan, func(), error) {
				plan, _, err := planSessionImageRuntime(opts, []messages.ImagePart{{
					Bytes:     []byte{0x89, 'P', 'N', 'G'},
					MediaType: "image/png",
				}}, SessionTextSeed{}, "customer instructions", false)
				return plan, func() {}, err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configDir := t.TempDir()
			writeSessionConfigFile(t, configDir, "model:\n  provider: openai\n")
			opts := SessionRunOptions{
				RecordPath:      filepath.Join(t.TempDir(), "session.json"),
				Provider:        config.ProviderOpenAI,
				Model:           openAIRealtimeDefaultModel,
				APIKey:          "test-key",
				ConfigDir:       configDir,
				ToolExecutor:    &messages.DefaultToolExecutor{},
				ToolDefinitions: append([]messages.ToolDefinition(nil), toolDefinitions...),
			}

			plan, cleanup, err := test.build(t, opts)
			if err != nil {
				t.Fatalf("build planner: %v", err)
			}
			defer cleanup()

			request := sessionRequestFromPlanner(t, plan.inferencer)
			instructions := request.Config.Instructions
			if !strings.HasPrefix(instructions, "customer instructions\n\n") {
				t.Fatalf("provider instructions = %q, want customer instructions first", instructions)
			}
			if strings.Count(instructions, "Tool-grounding requirements:") != 1 {
				t.Fatalf("grounding policy heading count = %d, want 1; instructions=%q", strings.Count(instructions, "Tool-grounding requirements:"), instructions)
			}
			if len(request.Config.Tools) != len(toolDefinitions) {
				t.Fatalf("provider tools = %#v, want %#v", request.Config.Tools, toolDefinitions)
			}
			wantToolNames := []string{"exec", "read_file"}
			for index, wantName := range wantToolNames {
				if request.Config.Tools[index].Name != wantName {
					t.Fatalf("provider tool %d = %#v, want %q", index, request.Config.Tools[index], wantName)
				}
			}
		})
	}
}

func TestIndependentSessionCompositionsProduceIdenticalInstructionsAndProviderUpdate(t *testing.T) {
	toolDefinitions := [][]messages.ToolDefinition{
		{
			{Name: "read_file", Description: "Read a UTF-8 file.", Parameters: []messages.ToolParameter{{Name: "path", Type: "string", Required: true}}},
			{Name: "exec", Description: "Execute a command.", Parameters: []messages.ToolParameter{{Name: "command", Type: "string", Required: true}}},
		},
		{
			{Name: "exec", Description: "Execute a command.", Parameters: []messages.ToolParameter{{Name: "command", Type: "string", Required: true}}},
			{Name: "read_file", Description: "Read a UTF-8 file.", Parameters: []messages.ToolParameter{{Name: "path", Type: "string", Required: true}}},
		},
	}
	labels := []string{"registration order one", "registration order two"}
	var wantInstructions []byte
	var wantProviderUpdate []byte

	for index, definitions := range toolDefinitions {
		t.Run(labels[index], func(t *testing.T) {
			configDir := t.TempDir()
			writeSessionConfigFile(t, configDir, "model:\n  provider: openai\n")
			conn := &replayHandshakeRecordingConn{}
			opts := SessionRunOptions{
				RecordPath:      filepath.Join(t.TempDir(), "session.json"),
				Provider:        config.ProviderOpenAI,
				Model:           openAIRealtimeDefaultModel,
				APIKey:          "test-key",
				ConfigDir:       configDir,
				ToolExecutor:    &messages.DefaultToolExecutor{},
				ToolDefinitions: definitions,
				WebSocketDialer: &replayHandshakeRecordingDialer{conn: conn},
			}

			plan, err := planSessionWithResolvedInstructions(opts, "customer instructions")
			if err != nil {
				t.Fatalf("build planner: %v", err)
			}

			request := sessionRequestFromPlanner(t, plan.inferencer)
			instructions := []byte(request.Config.Instructions)
			session, err := plan.inferencer.ConnectSession(context.Background())
			if err != nil {
				t.Fatalf("connect composed provider session: %v", err)
			}
			defer func() { _ = session.Close() }()

			conn.mu.Lock()
			writes := make([][]byte, len(conn.writes))
			for writeIndex := range conn.writes {
				writes[writeIndex] = append([]byte(nil), conn.writes[writeIndex]...)
			}
			conn.mu.Unlock()
			if len(writes) != 1 {
				t.Fatalf("provider writes = %d, want exactly initial session.update: %s", len(writes), writes)
			}

			if index == 0 {
				wantInstructions = instructions
				wantProviderUpdate = writes[0]
				return
			}
			if !bytes.Equal(instructions, wantInstructions) {
				t.Fatalf("independent composed instructions differ:\nfirst=%s\nsecond=%s", wantInstructions, instructions)
			}
			if !bytes.Equal(writes[0], wantProviderUpdate) {
				t.Fatalf("independent provider session.update bytes differ:\nfirst=%s\nsecond=%s", wantProviderUpdate, writes[0])
			}
		})
	}
}

func TestComposeSessionInstructionsIsIdempotentAndLeavesNoToolsUnchanged(t *testing.T) {
	withTools := SessionRunOptions{
		ToolDefinitions: []messages.ToolDefinition{{Name: "exec"}},
	}
	first := composeSessionInstructions(withTools, "customer instructions")
	second := composeSessionInstructions(withTools, first)
	if second != first {
		t.Fatalf("second composition changed instructions:\nfirst=%q\nsecond=%q", first, second)
	}
	if strings.Count(second, "Tool-grounding requirements:") != 1 {
		t.Fatalf("idempotent grounding policy count = %d, want 1", strings.Count(second, "Tool-grounding requirements:"))
	}

	withoutTools := composeSessionInstructions(SessionRunOptions{}, "customer instructions")
	if withoutTools != "customer instructions" {
		t.Fatalf("no-tools composition = %q, want unchanged customer instructions", withoutTools)
	}
}

func sessionRequestFromPlanner(t *testing.T, inferencer messages.SessionInferencer) inference.SessionRequest {
	t.Helper()
	if image, ok := inferencer.(*sessionImageInferencer); ok {
		inferencer = image.inner
	}
	requester, ok := inferencer.(interface {
		Request() inference.SessionRequest
	})
	if !ok {
		t.Fatalf("planner inferencer %T does not expose its provider request", inferencer)
	}
	return requester.Request()
}
