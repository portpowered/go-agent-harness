package cli

import (
	"errors"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	sessionservicewire "github.com/portpowered/go-agent-harness/agent-cli/internal/services/wire"
	providerswire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers/wire"
	"strings"
	"testing"
)

// Tests compose the same runtime and use-case services as the application graph.
func newTestSessionService(deps sessionservicewire.SessionDependencies) agentsession.SessionService {
	deps.Runtime = sessionservicewire.NewSessionRuntime(deps.Clock, deps.ToolService, sessionservicewire.NewSessionRuntimeFactory(), deps.RuntimeFactory, deps.SessionInferencer, deps.ToolExecutor, deps.DeviceRegistry, deps.RuntimeObserver, deps.MetricSampler, deps.Logger, providerswire.NewModelCatalog())
	return sessionservicewire.NewSessionService(deps)
}

type chatFlagMatrixCase struct {
	name            string
	args            []string
	input           string
	wantExit        int
	wantOutput      string
	wantOutputParts []string
	wantErrorPart   string
	wantFlagParse   bool
	wantInferCalls  int
	checkFlags      func(*testing.T, *flags.ChatFlags, *flags.LoopFlags)
}

const chatTextCancelOutput = "Port OS Agent Chat (type 'exit' or 'quit' to end)\n---\n\x1b[?25l\x1b[?2004h\r \r\x1b[2K\r\x1b[?2004l\x1b[?25h\x1b[?1002l\x1b[?1003l\x1b[?1006l"
const chatAudioEOFOutput = "Port OS Agent Chat - Audio Mode (Ctrl+C to exit)\n---\n\nListening...\nGoodbye!\n"

func TestChatCommand_FlagMatrix(t *testing.T) {
	for _, makeCases := range []func() []chatFlagMatrixCase{
		chatFlagMatrixAudioCases,
		chatFlagMatrixValidationCases,
		chatFlagMatrixParseCases,
	} {
		for _, tt := range makeCases() {
			tt := tt
			t.Run(tt.name, func(t *testing.T) {
				runChatFlagMatrixCase(t, tt)
			})
		}
	}
}

func runChatFlagMatrixCase(t *testing.T, tt chatFlagMatrixCase) {
	t.Helper()
	inf := &chatTestInferencer{response: "matrix response"}
	agentCLI, chatFlags, loopFlags := newTestAgentCLIAtWithFlags(t, inf, t.TempDir())
	got := executeInteractiveRoot(t, agentCLI, tt.args, tt.input)
	if got.exitCode != tt.wantExit {
		t.Fatalf("exit code = %d, want %d (err=%v)", got.exitCode, tt.wantExit, got.err)
	}
	if tt.wantErrorPart == "" {
		assertChatFlagMatrixSuccess(t, tt, got, inf, chatFlags, loopFlags)
		return
	}
	if got.err == nil || !strings.Contains(got.err.Error(), tt.wantErrorPart) {
		t.Fatalf("error = %v, want substring %q", got.err, tt.wantErrorPart)
	}
	if !tt.wantFlagParse {
		if got.stdout != "" || got.stderr != "" {
			t.Fatalf("local validation output = stdout %q stderr %q, want both empty", got.stdout, got.stderr)
		}
		if tt.checkFlags != nil {
			tt.checkFlags(t, chatFlags, loopFlags)
		}
		return
	}
	var typed *chatFlagParseError
	if !errors.As(got.err, &typed) {
		t.Fatalf("error type = %T, want *chatFlagParseError", got.err)
	}
	if !strings.Contains(got.stdout, "Usage:\n  yui chat [flags]") {
		t.Fatalf("stdout = %q, want Cobra usage on flag failure", got.stdout)
	}
	if got.stderr != "Error: "+got.err.Error()+"\n" {
		t.Fatalf("stderr = %q, want exact Cobra error channel", got.stderr)
	}
}

func assertChatFlagMatrixSuccess(t *testing.T, tt chatFlagMatrixCase, got chatRun, inf *chatTestInferencer, chatFlags *flags.ChatFlags, loopFlags *flags.LoopFlags) {
	t.Helper()
	if got.err != nil {
		t.Fatalf("ExecuteContext() error = %v", got.err)
	}
	if got.stderr != "" {
		t.Fatalf("stderr = %q, want empty", got.stderr)
	}
	if tt.wantOutput != "" && got.stdout != tt.wantOutput {
		t.Fatalf("stdout = %q, want %q", got.stdout, tt.wantOutput)
	}
	for _, want := range tt.wantOutputParts {
		if !strings.Contains(got.stdout, want) {
			t.Fatalf("stdout = %q, want substring %q", got.stdout, want)
		}
	}
	if tt.wantInferCalls >= 0 && inf.calls != tt.wantInferCalls {
		t.Fatalf("inferencer calls = %d, want %d", inf.calls, tt.wantInferCalls)
	}
	if tt.checkFlags != nil {
		tt.checkFlags(t, chatFlags, loopFlags)
	}
}

func chatFlagMatrixAudioCases() []chatFlagMatrixCase {
	return []chatFlagMatrixCase{
		{
			name:       "activate audio input alone",
			args:       []string{"chat", "--activate-audio-in"},
			wantExit:   0,
			wantOutput: chatAudioEOFOutput,
			checkFlags: func(t *testing.T, chatFlags *flags.ChatFlags, _ *flags.LoopFlags) {
				if !chatFlags.ActivateAudioIn {
					t.Fatal("ActivateAudioIn = false, want true")
				}
			},
		},
		{
			name:       "activate audio output alone",
			args:       []string{"chat", "--activate-audio-out"},
			input:      "\x03",
			wantExit:   0,
			wantOutput: chatTextCancelOutput,
			checkFlags: func(t *testing.T, chatFlags *flags.ChatFlags, _ *flags.LoopFlags) {
				if !chatFlags.ActivateAudioOut {
					t.Fatal("ActivateAudioOut = false, want true")
				}
			},
		},
		{
			name:            "loop alone",
			args:            []string{"chat", "--loop"},
			input:           "task\n",
			wantExit:        0,
			wantOutputParts: []string{"Port OS Agent Loop Chat (up to 5 iterations)", "Loop complete: 5 iteration(s)"},
			wantInferCalls:  5,
			checkFlags: func(t *testing.T, _ *flags.ChatFlags, loopFlags *flags.LoopFlags) {
				if !loopFlags.Loop || loopFlags.MaxIterations != 5 {
					t.Fatalf("loop flags = %+v, want Loop=true MaxIterations=5", *loopFlags)
				}
			},
		},
		{
			name:          "max iterations alone",
			args:          []string{"chat", "--max-iterations", "2"},
			input:         "\x03",
			wantExit:      1,
			wantErrorPart: "--max-iterations requires --loop",
			checkFlags: func(t *testing.T, _ *flags.ChatFlags, loopFlags *flags.LoopFlags) {
				if loopFlags.MaxIterations != 2 {
					t.Fatalf("MaxIterations = %d, want 2", loopFlags.MaxIterations)
				}
			},
		},
		{
			name:          "stop word alone",
			args:          []string{"chat", "--stop-word", "DONE"},
			input:         "\x03",
			wantExit:      1,
			wantErrorPart: "--stop-word requires --loop",
			checkFlags: func(t *testing.T, _ *flags.ChatFlags, loopFlags *flags.LoopFlags) {
				if loopFlags.StopWord != "DONE" {
					t.Fatalf("StopWord = %q, want %q", loopFlags.StopWord, "DONE")
				}
			},
		},
		{
			name:          "context pressure threshold alone",
			args:          []string{"chat", "--context-pressure-threshold", "0.4"},
			input:         "\x03",
			wantExit:      1,
			wantErrorPart: "--context-pressure-threshold requires --loop",
			checkFlags: func(t *testing.T, _ *flags.ChatFlags, loopFlags *flags.LoopFlags) {
				if loopFlags.ContextPressureThreshold != 0.4 {
					t.Fatalf("ContextPressureThreshold = %v, want 0.4", loopFlags.ContextPressureThreshold)
				}
			},
		},
	}
}

func chatFlagMatrixValidationCases() []chatFlagMatrixCase {
	return []chatFlagMatrixCase{
		{
			name:          "negative max iterations with loop is rejected",
			args:          []string{"chat", "--loop", "--max-iterations", "-5"},
			wantExit:      1,
			wantErrorPart: "--max-iterations must be a positive integer, got -5",
		},
		{
			name:          "negative context pressure threshold with loop is rejected",
			args:          []string{"chat", "--loop", "--context-pressure-threshold", "-3"},
			wantExit:      1,
			wantErrorPart: "--context-pressure-threshold must be greater than 0 and at most 1",
		},
		{
			name:          "context pressure threshold above 1 with loop is rejected",
			args:          []string{"chat", "--loop", "--context-pressure-threshold", "5000"},
			wantExit:      1,
			wantErrorPart: "--context-pressure-threshold must be greater than 0 and at most 1",
		},
		{
			name:            "in-range loop flags with loop are accepted",
			args:            []string{"chat", "--loop", "--max-iterations", "2", "--context-pressure-threshold", "0.5"},
			input:           "task\nsteer\n",
			wantExit:        0,
			wantOutputParts: []string{"Port OS Agent Loop Chat (up to 2 iterations)", "Loop complete: 2 iteration(s)"},
			wantInferCalls:  2,
		},
		{
			name:          "context pressure message alone",
			args:          []string{"chat", "--context-pressure-message", "warning"},
			input:         "\x03",
			wantExit:      1,
			wantErrorPart: "--context-pressure-message requires --loop",
			checkFlags: func(t *testing.T, _ *flags.ChatFlags, loopFlags *flags.LoopFlags) {
				if loopFlags.ContextPressureMessage != "warning" {
					t.Fatalf("ContextPressureMessage = %q, want %q", loopFlags.ContextPressureMessage, "warning")
				}
			},
		},
		{
			name:          "trace id alone",
			args:          []string{"chat", "--trace-id", "ignored-trace"},
			input:         "\x03",
			wantExit:      1,
			wantErrorPart: "--trace-id requires --loop",
			checkFlags: func(t *testing.T, _ *flags.ChatFlags, loopFlags *flags.LoopFlags) {
				if loopFlags.TraceID != "ignored-trace" {
					t.Fatalf("TraceID = %q, want %q", loopFlags.TraceID, "ignored-trace")
				}
			},
		},
		{
			name:          "loop rejects audio input",
			args:          []string{"chat", "--loop", "--max-iterations", "1", "--activate-audio-in"},
			input:         "task\n",
			wantExit:      1,
			wantErrorPart: "--activate-audio-in cannot be combined with --loop",
			checkFlags: func(t *testing.T, chatFlags *flags.ChatFlags, loopFlags *flags.LoopFlags) {
				if !chatFlags.ActivateAudioIn || !loopFlags.Loop || loopFlags.MaxIterations != 1 {
					t.Fatalf("flags = chat=%+v loop=%+v, want audio input, loop, max iterations 1", *chatFlags, *loopFlags)
				}
			},
		},
	}
}

func chatFlagMatrixParseCases() []chatFlagMatrixCase {
	return []chatFlagMatrixCase{
		{
			name:          "boolean audio input rejects wrong type",
			args:          []string{"chat", "--activate-audio-in=maybe"},
			wantExit:      1,
			wantFlagParse: true,
			wantErrorPart: `invalid argument "maybe" for "--activate-audio-in" flag`,
		},
		{
			name:          "boolean audio output rejects wrong type",
			args:          []string{"chat", "--activate-audio-out=maybe"},
			wantExit:      1,
			wantFlagParse: true,
			wantErrorPart: `invalid argument "maybe" for "--activate-audio-out" flag`,
		},
		{
			name:          "boolean loop rejects wrong type",
			args:          []string{"chat", "--loop=maybe"},
			wantExit:      1,
			wantFlagParse: true,
			wantErrorPart: `invalid argument "maybe" for "--loop" flag`,
		},
		{
			name:          "integer rejects wrong type",
			args:          []string{"chat", "--max-iterations", "many"},
			wantExit:      1,
			wantFlagParse: true,
			wantErrorPart: `invalid argument "many" for "--max-iterations" flag`,
		},
		{
			name:          "float rejects wrong type",
			args:          []string{"chat", "--context-pressure-threshold", "many"},
			wantExit:      1,
			wantFlagParse: true,
			wantErrorPart: `invalid argument "many" for "--context-pressure-threshold" flag`,
		},
		{
			name:          "stop word requires a value",
			args:          []string{"chat", "--stop-word"},
			wantExit:      1,
			wantFlagParse: true,
			wantErrorPart: "flag needs an argument: --stop-word",
		},
		{
			name:          "context message requires a value",
			args:          []string{"chat", "--context-pressure-message"},
			wantExit:      1,
			wantFlagParse: true,
			wantErrorPart: "flag needs an argument: --context-pressure-message",
		},
		{
			name:          "trace id requires a value",
			args:          []string{"chat", "--trace-id"},
			wantExit:      1,
			wantFlagParse: true,
			wantErrorPart: "flag needs an argument: --trace-id",
		},
	}
}
