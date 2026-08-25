package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"regexp"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/agent"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/session"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

type chatTestToolExecutor struct{}

func (chatTestToolExecutor) Execute(context.Context, messages.ToolCall) (messages.ToolCallResponse, error) {
	return messages.ToolCallResponse{}, nil
}

type chatTestInferencer struct {
	response string
	callErr  error
	calls    int
}

func (m *chatTestInferencer) Infer(context.Context, messages.InferenceRequest) (messages.InferenceResult, error) {
	m.calls++
	if m.callErr != nil {
		return messages.InferenceResult{}, m.callErr
	}
	return messages.InferenceResult{Message: messages.NewTextMessage(messages.RoleAssistant, m.response)}, nil
}

func (m *chatTestInferencer) InferStream(context.Context, messages.InferenceRequest) (<-chan messages.StreamMessage, error) {
	return nil, errors.New("streaming not used by chat coverage tests")
}

type chatRun struct {
	err      error
	exitCode int
	stdout   string
	stderr   string
}

func executeChat(t *testing.T, args []string, input string, inferencer messages.Inferencer) chatRun {
	t.Helper()
	agentCLI := newTestAgentCLI(t, inferencer)
	return executeRoot(t, agentCLI, args, input)
}

func executeRoot(t *testing.T, agentCLI *AgentCLI, args []string, input string) chatRun {
	t.Helper()
	return executeRootWithMicrophone(t, agentCLI, args, input, func() (audio.AudioSource, error) {
		return audio.NewSliceSource(nil), nil
	})
}

func executeRootWithMicrophone(t *testing.T, agentCLI *AgentCLI, args []string, input string, factory func() (audio.AudioSource, error)) chatRun {
	t.Helper()
	originalMicrophoneSource := newMicrophoneSource
	newMicrophoneSource = factory
	t.Cleanup(func() { newMicrophoneSource = originalMicrophoneSource })

	var stdout, stderr bytes.Buffer
	root := agentCLI.Generate()
	root.SetIn(strings.NewReader(input))
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(args)
	err := root.ExecuteContext(context.Background())
	code := 0
	if err != nil {
		code = 1
	}
	return chatRun{err: err, exitCode: code, stdout: stdout.String(), stderr: stderr.String()}
}

func newTestAgentCLI(t *testing.T, inferencer messages.Inferencer) *AgentCLI {
	t.Helper()
	agentCLI, _, _ := newTestAgentCLIAtWithFlags(t, inferencer, t.TempDir())
	return agentCLI
}

func newTestAgentCLIAt(t *testing.T, inferencer messages.Inferencer, configDir string) *AgentCLI {
	t.Helper()
	agentCLI, _, _ := newTestAgentCLIAtWithFlags(t, inferencer, configDir)
	return agentCLI
}

func newTestAgentCLIAtWithFlags(t *testing.T, inferencer messages.Inferencer, configDir string) (*AgentCLI, *flags.ChatFlags, *flags.LoopFlags) {
	t.Helper()
	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = configDir
	rootCommand := NewRootCommand(globalFlags)
	registry := tools.NewToolRegistry()
	executor := agent.NewExecutor(chatTestToolExecutor{}, services.DefaultToolDefs(registry), inferencer, true)
	askFlags := flags.NewAskFlags()
	askFlags.NoSystemInformation = true
	loopFlags := flags.NewLoopFlags()
	chatFlags := flags.NewChatFlags()
	router := NewRouter(
		globalFlags,
		rootCommand,
		NewAskCommand(executor, askFlags, loopFlags, globalFlags),
		NewChatCommand(executor, askFlags, loopFlags, chatFlags, globalFlags),
		NewToolCommand(globalFlags),
		NewInteractionCommand(),
		NewInteractionReplayCommand(),
		NewProbeCommand(),
		NewProbeRunCommand(),
		NewSessionCommand(askFlags, globalFlags, nil, nil),
		NewSessionShowCommand(globalFlags),
		NewSessionListCommand(globalFlags),
		NewSessionDeleteCommand(globalFlags),
		NewConfigCommand(),
		NewConfigAddLocalCommand(globalFlags),
	)
	return NewAgentCLI(router), chatFlags, loopFlags
}

func TestChatCommand_ExecuteThroughRoot(t *testing.T) {
	tests := []struct {
		name           string
		args           []string
		input          string
		wantCode       int
		wantStdout     string
		wantStdoutPart string
		wantStderr     string
		wantErr        string
	}{
		{
			name:       "successful loop session",
			args:       []string{"chat", "--loop", "--max-iterations", "1"},
			input:      "task\n",
			wantCode:   0,
			wantStdout: "Port OS Agent Loop Chat (up to 1 iterations)\n---\nEnter your task: Trace ID: <trace>\n\n--- Iteration 1/1 ---\nunused\n\n[Loop complete: 1 iteration(s), trace: <trace>]\n",
			wantStderr: "",
		},
		{
			name:           "invalid float fails before dispatch",
			args:           []string{"chat", "--context-pressure-threshold", "not-a-float"},
			wantCode:       1,
			wantStdout:     "",
			wantStdoutPart: "Usage:\n  agent chat [flags]",
			wantStderr:     "Error: invalid argument \"not-a-float\" for \"--context-pressure-threshold\" flag: strconv.ParseFloat: parsing \"not-a-float\": invalid syntax\n",
			wantErr:        `invalid argument "not-a-float" for "--context-pressure-threshold" flag`,
		},
		{
			name:       "text session cancels through root",
			args:       []string{"chat"},
			input:      "\x03",
			wantCode:   0,
			wantStdout: "Port OS Agent Chat (type 'exit' or 'quit' to end)\n---\n\x1b[?25l\x1b[?2004h\r \r\x1b[2K\r\x1b[?2004l\x1b[?25h\x1b[?1002l\x1b[?1003l\x1b[?1006l",
			wantStderr: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := executeChat(t, tt.args, tt.input, &chatTestInferencer{response: "unused"})
			if got.exitCode != tt.wantCode {
				t.Fatalf("exit code = %d, want %d (err=%v)", got.exitCode, tt.wantCode, got.err)
			}
			stdout := got.stdout
			if tt.name == "successful loop session" {
				stdout = normalizeTraceIDs(stdout)
			}
			if tt.wantStdout != "" && stdout != tt.wantStdout {
				t.Fatalf("stdout = %q, want %q", stdout, tt.wantStdout)
			}
			if tt.wantStdoutPart != "" && !strings.Contains(got.stdout, tt.wantStdoutPart) {
				t.Fatalf("stdout = %q, want substring %q", got.stdout, tt.wantStdoutPart)
			}
			if got.stderr != tt.wantStderr {
				t.Fatalf("stderr = %q, want %q", got.stderr, tt.wantStderr)
			}
			if tt.wantErr == "" {
				if got.err != nil {
					t.Fatalf("ExecuteContext() error = %v", got.err)
				}
				return
			}
			if got.err == nil || !strings.Contains(got.err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want substring %q", got.err, tt.wantErr)
			}
			var typed *chatFlagParseError
			if !errors.As(got.err, &typed) {
				t.Fatalf("error type = %T, want *chatFlagParseError", got.err)
			}
		})
	}
}

func normalizeTraceIDs(output string) string {
	return regexp.MustCompile(`(Trace ID: |trace: )\d+`).ReplaceAllString(output, `${1}<trace>`)
}

func TestChatCommand_AudioHelperStopsAtEOF(t *testing.T) {
	inf := &chatTestInferencer{response: "unused"}
	exec := agent.NewExecutor(chatTestToolExecutor{}, nil, inf, true)
	global := flags.NewGlobalFlags()
	global.ConfigDirPath = t.TempDir()
	ask := flags.NewAskFlags()
	ask.NoSystemInformation = true
	var out, errOut bytes.Buffer

	err := RunChatWithAudio(context.Background(), &out, &errOut, exec, global, ask, audio.NewSliceSource(nil))
	if err != nil {
		t.Fatalf("RunChatWithAudio() error = %v", err)
	}
	if got, want := out.String(), "Port OS Agent Chat - Audio Mode (Ctrl+C to exit)\n---\n\nListening...\nGoodbye!\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if errOut.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", errOut.String())
	}
}

func TestChatCommand_FlagMatrix(t *testing.T) {
	const textCancelOutput = "Port OS Agent Chat (type 'exit' or 'quit' to end)\n---\n\x1b[?25l\x1b[?2004h\r \r\x1b[2K\r\x1b[?2004l\x1b[?25h\x1b[?1002l\x1b[?1003l\x1b[?1006l"
	const audioEOFOutput = "Port OS Agent Chat - Audio Mode (Ctrl+C to exit)\n---\n\nListening...\nGoodbye!\n"

	tests := []struct {
		name            string
		args            []string
		input           string
		wantExit        int
		wantOutput      string
		wantOutputParts []string
		wantErrorPart   string
		wantInferCalls  int
		checkFlags      func(*testing.T, *flags.ChatFlags, *flags.LoopFlags)
	}{
		{
			name:       "activate audio input alone",
			args:       []string{"chat", "--activate-audio-in"},
			wantExit:   0,
			wantOutput: audioEOFOutput,
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
			wantOutput: textCancelOutput,
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
			name:       "max iterations alone",
			args:       []string{"chat", "--max-iterations", "2"},
			input:      "\x03",
			wantExit:   0,
			wantOutput: textCancelOutput,
			checkFlags: func(t *testing.T, _ *flags.ChatFlags, loopFlags *flags.LoopFlags) {
				if loopFlags.MaxIterations != 2 {
					t.Fatalf("MaxIterations = %d, want 2", loopFlags.MaxIterations)
				}
			},
		},
		{
			name:       "stop word alone",
			args:       []string{"chat", "--stop-word", "DONE"},
			input:      "\x03",
			wantExit:   0,
			wantOutput: textCancelOutput,
			checkFlags: func(t *testing.T, _ *flags.ChatFlags, loopFlags *flags.LoopFlags) {
				if loopFlags.StopWord != "DONE" {
					t.Fatalf("StopWord = %q, want %q", loopFlags.StopWord, "DONE")
				}
			},
		},
		{
			name:       "context pressure threshold alone",
			args:       []string{"chat", "--context-pressure-threshold", "0.4"},
			input:      "\x03",
			wantExit:   0,
			wantOutput: textCancelOutput,
			checkFlags: func(t *testing.T, _ *flags.ChatFlags, loopFlags *flags.LoopFlags) {
				if loopFlags.ContextPressureThreshold != 0.4 {
					t.Fatalf("ContextPressureThreshold = %v, want 0.4", loopFlags.ContextPressureThreshold)
				}
			},
		},
		{
			name:       "context pressure message alone",
			args:       []string{"chat", "--context-pressure-message", "warning"},
			input:      "\x03",
			wantExit:   0,
			wantOutput: textCancelOutput,
			checkFlags: func(t *testing.T, _ *flags.ChatFlags, loopFlags *flags.LoopFlags) {
				if loopFlags.ContextPressureMessage != "warning" {
					t.Fatalf("ContextPressureMessage = %q, want %q", loopFlags.ContextPressureMessage, "warning")
				}
			},
		},
		{
			name:       "trace id alone",
			args:       []string{"chat", "--trace-id", "ignored-trace"},
			input:      "\x03",
			wantExit:   0,
			wantOutput: textCancelOutput,
			checkFlags: func(t *testing.T, _ *flags.ChatFlags, loopFlags *flags.LoopFlags) {
				if loopFlags.TraceID != "ignored-trace" {
					t.Fatalf("TraceID = %q, want %q", loopFlags.TraceID, "ignored-trace")
				}
			},
		},
		{
			name:            "loop takes precedence over audio input",
			args:            []string{"chat", "--loop", "--max-iterations", "1", "--activate-audio-in"},
			input:           "task\n",
			wantExit:        0,
			wantOutputParts: []string{"Port OS Agent Loop Chat (up to 1 iterations)", "Loop complete: 1 iteration(s)"},
			wantInferCalls:  1,
			checkFlags: func(t *testing.T, chatFlags *flags.ChatFlags, loopFlags *flags.LoopFlags) {
				if !chatFlags.ActivateAudioIn || !loopFlags.Loop || loopFlags.MaxIterations != 1 {
					t.Fatalf("flags = chat=%+v loop=%+v, want audio input, loop, max iterations 1", *chatFlags, *loopFlags)
				}
			},
		},
		{
			name:          "boolean audio input rejects wrong type",
			args:          []string{"chat", "--activate-audio-in=maybe"},
			wantExit:      1,
			wantErrorPart: `invalid argument "maybe" for "--activate-audio-in" flag`,
		},
		{
			name:          "boolean audio output rejects wrong type",
			args:          []string{"chat", "--activate-audio-out=maybe"},
			wantExit:      1,
			wantErrorPart: `invalid argument "maybe" for "--activate-audio-out" flag`,
		},
		{
			name:          "boolean loop rejects wrong type",
			args:          []string{"chat", "--loop=maybe"},
			wantExit:      1,
			wantErrorPart: `invalid argument "maybe" for "--loop" flag`,
		},
		{
			name:          "integer rejects wrong type",
			args:          []string{"chat", "--max-iterations", "many"},
			wantExit:      1,
			wantErrorPart: `invalid argument "many" for "--max-iterations" flag`,
		},
		{
			name:          "float rejects wrong type",
			args:          []string{"chat", "--context-pressure-threshold", "many"},
			wantExit:      1,
			wantErrorPart: `invalid argument "many" for "--context-pressure-threshold" flag`,
		},
		{
			name:          "stop word requires a value",
			args:          []string{"chat", "--stop-word"},
			wantExit:      1,
			wantErrorPart: "flag needs an argument: --stop-word",
		},
		{
			name:          "context message requires a value",
			args:          []string{"chat", "--context-pressure-message"},
			wantExit:      1,
			wantErrorPart: "flag needs an argument: --context-pressure-message",
		},
		{
			name:          "trace id requires a value",
			args:          []string{"chat", "--trace-id"},
			wantExit:      1,
			wantErrorPart: "flag needs an argument: --trace-id",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inf := &chatTestInferencer{response: "matrix response"}
			agentCLI, chatFlags, loopFlags := newTestAgentCLIAtWithFlags(t, inf, t.TempDir())
			got := executeRoot(t, agentCLI, tt.args, tt.input)
			if got.exitCode != tt.wantExit {
				t.Fatalf("exit code = %d, want %d (err=%v)", got.exitCode, tt.wantExit, got.err)
			}
			if tt.wantErrorPart == "" {
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
				return
			}

			if got.err == nil || !strings.Contains(got.err.Error(), tt.wantErrorPart) {
				t.Fatalf("error = %v, want substring %q", got.err, tt.wantErrorPart)
			}
			var typed *chatFlagParseError
			if !errors.As(got.err, &typed) {
				t.Fatalf("error type = %T, want *chatFlagParseError", got.err)
			}
			if !strings.Contains(got.stdout, "Usage:\n  agent chat [flags]") {
				t.Fatalf("stdout = %q, want Cobra usage on flag failure", got.stdout)
			}
			if got.stderr != "Error: "+got.err.Error()+"\n" {
				t.Fatalf("stderr = %q, want exact Cobra error channel", got.stderr)
			}
		})
	}
}

func TestChatCommand_LoopBranches(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		input      string
		response   string
		callErr    error
		wantExit   int
		wantOutput []string
		wantCalls  int
	}{
		{
			name:       "zero max iterations defaults and EOF returns",
			args:       []string{"chat", "--loop", "--max-iterations", "0"},
			wantOutput: []string{"up to 5 iterations", "Enter your task:"},
		},
		{
			name:       "empty task is rejected",
			args:       []string{"chat", "--loop"},
			input:      "\n",
			wantExit:   1,
			wantOutput: []string{"Enter your task:"},
		},
		{
			name:       "iteration errors are reported and continue",
			args:       []string{"chat", "--loop", "--max-iterations", "2"},
			input:      "task\n",
			callErr:    errors.New("inference failed"),
			wantOutput: []string{"Iteration 1 error: inference failed", "Iteration 2 error: inference failed", "Loop complete: 2 iteration(s)"},
			wantCalls:  2,
		},
		{
			name:       "stop word completes early",
			args:       []string{"chat", "--loop", "--max-iterations", "3", "--stop-word", "DONE"},
			input:      "task\n",
			response:   "DONE",
			wantOutput: []string{"Completion detected in iteration 1"},
			wantCalls:  1,
		},
		{
			name:       "steering updates the next prompt",
			args:       []string{"chat", "--loop", "--max-iterations", "2"},
			input:      "task\nsteer\n",
			response:   "continue",
			wantOutput: []string{"Enter steering for iteration 2", "Loop complete: 2 iteration(s)"},
			wantCalls:  2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inf := &chatTestInferencer{response: tt.response, callErr: tt.callErr}
			got := executeChat(t, tt.args, tt.input, inf)
			if got.exitCode != tt.wantExit {
				t.Fatalf("exit code = %d, want %d (err=%v)", got.exitCode, tt.wantExit, got.err)
			}
			if tt.wantExit == 0 && got.err != nil {
				t.Fatalf("ExecuteContext() error = %v", got.err)
			}
			for _, want := range tt.wantOutput {
				if !strings.Contains(got.stdout, want) {
					t.Fatalf("stdout = %q, want substring %q", got.stdout, want)
				}
			}
			if tt.callErr != nil && (got.err != nil || !strings.Contains(got.stdout, tt.callErr.Error())) {
				t.Fatalf("iteration error should remain an output event: err=%v stdout=%q", got.err, got.stdout)
			}
			if tt.wantCalls > 0 && inf.calls != tt.wantCalls {
				t.Fatalf("inferencer calls = %d, want %d", inf.calls, tt.wantCalls)
			}
		})
	}
}

func TestChatCommand_AudioFactoryErrorIsReported(t *testing.T) {
	deviceErr := errors.New("device unavailable")
	got := executeRootWithMicrophone(t, newTestAgentCLI(t, &chatTestInferencer{}), []string{"chat", "--activate-audio-in"}, "", func() (audio.AudioSource, error) {
		return nil, deviceErr
	})
	if got.exitCode != 1 || got.err == nil {
		t.Fatalf("exit code=%d error=%v, want command failure", got.exitCode, got.err)
	}
	if !errors.Is(got.err, deviceErr) || got.err.Error() != "open microphone: device unavailable" {
		t.Fatalf("error = %v, want wrapped device error", got.err)
	}
	if got.stderr != "Error: open microphone: device unavailable\n" {
		t.Fatalf("stderr = %q, want exact error channel", got.stderr)
	}
}

func TestChatCommand_ResumesStoredTraces(t *testing.T) {
	tests := []struct {
		name           string
		status         session.IterationStatus
		input          string
		wantOutput     []string
		wantInferCalls int
	}{
		{
			name:           "completed iteration resumes after the stored last turn",
			status:         session.IterationStatusCompleted,
			wantOutput:     []string{"Resuming trace trace-resume from iteration 2/2", "Trace ID: trace-resume", "Loop complete: 2 iteration(s)"},
			wantInferCalls: 1,
		},
		{
			name:           "interrupted iteration restarts from its stored turn",
			status:         session.IterationStatusInterrupted,
			input:          "resume task\n",
			wantOutput:     []string{"Resuming trace trace-resume from iteration 1/2", "Iteration 1/2", "Loop complete: 2 iteration(s)"},
			wantInferCalls: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configDir := t.TempDir()
			storage := session.NewStorage(configDir)
			if err := storage.SaveTrace(session.TraceRecord{
				TraceID: "trace-resume",
				Status:  session.TraceStatusInterrupted,
				Config: session.TraceConfig{
					MaxIterations: 2,
					StopWord:      "stored stop",
					Prompt:        "stored prompt",
				},
				Iterations: []session.IterationTrace{{Iteration: 1, Status: tt.status}},
			}); err != nil {
				t.Fatalf("SaveTrace() error = %v", err)
			}
			inf := &chatTestInferencer{response: "resume response"}
			got := executeRoot(t, newTestAgentCLIAt(t, inf, configDir), []string{"chat", "--loop", "--trace-id", "trace-resume"}, tt.input)
			if got.exitCode != 0 || got.err != nil {
				t.Fatalf("exit code=%d error=%v stdout=%q", got.exitCode, got.err, got.stdout)
			}
			for _, want := range tt.wantOutput {
				if !strings.Contains(got.stdout, want) {
					t.Fatalf("stdout = %q, want substring %q", got.stdout, want)
				}
			}
			if inf.calls != tt.wantInferCalls {
				t.Fatalf("inferencer calls = %d, want %d", inf.calls, tt.wantInferCalls)
			}
		})
	}
}

type failAfterWriter struct {
	failAt int
	writes int
	err    error
}

func (w *failAfterWriter) Write(p []byte) (int, error) {
	w.writes++
	if w.writes >= w.failAt {
		return 0, w.err
	}
	return len(p), nil
}

func executeChatWithWriters(t *testing.T, agentCLI *AgentCLI, args []string, input string, out, errOut io.Writer) error {
	t.Helper()
	root := agentCLI.Generate()
	root.SetIn(strings.NewReader(input))
	root.SetOut(out)
	root.SetErr(errOut)
	root.SetArgs(args)
	return root.ExecuteContext(context.Background())
}

func TestChatCommand_LoopWriterErrors(t *testing.T) {
	tests := []struct {
		name    string
		failAt  int
		args    []string
		input   string
		inf     *chatTestInferencer
		wantErr string
	}{
		{name: "header", failAt: 1, args: []string{"chat", "--loop"}, wantErr: "write chat header"},
		{name: "header separator", failAt: 2, args: []string{"chat", "--loop"}, wantErr: "write chat header separator"},
		{name: "task prompt", failAt: 3, args: []string{"chat", "--loop"}, wantErr: "write task prompt"},
		{name: "trace id", failAt: 4, args: []string{"chat", "--loop"}, input: "task\n", wantErr: "write trace ID"},
		{name: "iteration header", failAt: 5, args: []string{"chat", "--loop"}, input: "task\n", wantErr: "write iteration header"},
		{name: "iteration error", failAt: 6, args: []string{"chat", "--loop"}, input: "task\n", inf: &chatTestInferencer{callErr: errors.New("inference failed")}, wantErr: "write iteration error"},
		{name: "completion banner", failAt: 7, args: []string{"chat", "--loop", "--max-iterations", "2", "--stop-word", "DONE"}, input: "task\n", inf: &chatTestInferencer{response: "DONE"}, wantErr: "write completion banner"},
		{name: "steering prompt", failAt: 7, args: []string{"chat", "--loop", "--max-iterations", "2"}, input: "task\n", inf: &chatTestInferencer{response: "continue"}, wantErr: "write steering prompt"},
		{name: "loop completion banner", failAt: 7, args: []string{"chat", "--loop", "--max-iterations", "1"}, input: "task\n", inf: &chatTestInferencer{response: "continue"}, wantErr: "write loop completion banner"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inf := tt.inf
			if inf == nil {
				inf = &chatTestInferencer{response: "unused"}
			}
			out := &failAfterWriter{failAt: tt.failAt, err: errors.New("output failed")}
			var errOut bytes.Buffer
			err := executeChatWithWriters(t, newTestAgentCLI(t, inf), tt.args, tt.input, out, &errOut)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}

func TestChatCommand_AudioWriterErrors(t *testing.T) {
	tests := []struct {
		name       string
		failAt     int
		wantErr    string
		withSpeech bool
	}{
		{name: "audio header", failAt: 1, wantErr: "write audio chat header"},
		{name: "audio header separator", failAt: 2, wantErr: "write audio chat header separator"},
		{name: "listening status", failAt: 3, wantErr: "write listening status"},
		{name: "speech status", failAt: 4, wantErr: "write speech detected status", withSpeech: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exec := agent.NewExecutor(chatTestToolExecutor{}, nil, &chatTestInferencer{response: "audio response"}, true)
			global := flags.NewGlobalFlags()
			global.ConfigDirPath = t.TempDir()
			ask := flags.NewAskFlags()
			ask.NoSystemInformation = true
			samples := []int16(nil)
			if tt.withSpeech {
				samples = make([]int16, audio.FrameSize*(3+audio.DefaultVADConfig.MaxSilenceFrames))
				for i := 0; i < audio.FrameSize*3; i++ {
					samples[i] = 1000
				}
			}
			out := &failAfterWriter{failAt: tt.failAt, err: errors.New("audio output failed")}
			var errOut bytes.Buffer
			err := RunChatWithAudio(context.Background(), out, &errOut, exec, global, ask, audio.NewSliceSource(samples))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}

type scriptedAudioSource struct {
	steps []error
	index int
}

func (s *scriptedAudioSource) ReadFrame(_ context.Context, _ []int16) error {
	if s.index >= len(s.steps) {
		return io.EOF
	}
	err := s.steps[s.index]
	s.index++
	return err
}

func (*scriptedAudioSource) Close() error { return nil }

func TestChatCommand_AudioHelperProcessesSpeechAndReportsPipelineErrors(t *testing.T) {
	t.Run("speech dispatches once", func(t *testing.T) {
		inf := &chatTestInferencer{response: "audio response"}
		exec := agent.NewExecutor(chatTestToolExecutor{}, nil, inf, true)
		global := flags.NewGlobalFlags()
		global.ConfigDirPath = t.TempDir()
		ask := flags.NewAskFlags()
		ask.NoSystemInformation = true
		samples := make([]int16, audio.FrameSize*(3+audio.DefaultVADConfig.MaxSilenceFrames))
		for i := 0; i < audio.FrameSize*3; i++ {
			samples[i] = 1000
		}
		var out, errOut bytes.Buffer

		if err := RunChatWithAudio(context.Background(), &out, &errOut, exec, global, ask, audio.NewSliceSource(samples)); err != nil {
			t.Fatalf("RunChatWithAudio() error = %v", err)
		}
		if inf.calls != 1 {
			t.Fatalf("inferencer calls = %d, want 1; stdout=%q stderr=%q", inf.calls, out.String(), errOut.String())
		}
		for _, want := range []string{"Audio Mode", "(speech detected, processing...)", "Goodbye!"} {
			if !strings.Contains(out.String(), want) {
				t.Fatalf("stdout = %q, want substring %q", out.String(), want)
			}
		}
		if errOut.Len() != 0 {
			t.Fatalf("stderr = %q, want empty", errOut.String())
		}
	})

	t.Run("pipeline error is reported and loop continues", func(t *testing.T) {
		exec := agent.NewExecutor(chatTestToolExecutor{}, nil, &chatTestInferencer{}, true)
		global := flags.NewGlobalFlags()
		global.ConfigDirPath = t.TempDir()
		ask := flags.NewAskFlags()
		ask.NoSystemInformation = true
		var out, errOut bytes.Buffer
		sourceErr := errors.New("frame failed")

		if err := RunChatWithAudio(context.Background(), &out, &errOut, exec, global, ask, &scriptedAudioSource{steps: []error{sourceErr, io.EOF}}); err != nil {
			t.Fatalf("RunChatWithAudio() error = %v", err)
		}
		if got, want := errOut.String(), "Audio pipeline error: read audio frame: frame failed\n"; got != want {
			t.Fatalf("stderr = %q, want %q", got, want)
		}
		if !strings.Contains(out.String(), "Goodbye!") {
			t.Fatalf("stdout = %q, want Goodbye", out.String())
		}
	})

	t.Run("context cancellation says goodbye", func(t *testing.T) {
		exec := agent.NewExecutor(chatTestToolExecutor{}, nil, &chatTestInferencer{}, true)
		global := flags.NewGlobalFlags()
		global.ConfigDirPath = t.TempDir()
		ask := flags.NewAskFlags()
		ask.NoSystemInformation = true
		var out, errOut bytes.Buffer

		if err := RunChatWithAudio(context.Background(), &out, &errOut, exec, global, ask, &scriptedAudioSource{steps: []error{context.Canceled}}); err != nil {
			t.Fatalf("RunChatWithAudio() error = %v", err)
		}
		if !strings.Contains(out.String(), "Goodbye!") || errOut.Len() != 0 {
			t.Fatalf("stdout=%q stderr=%q, want goodbye and empty stderr", out.String(), errOut.String())
		}
	})
}
