package cli

import sessionclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"

import sessionservicewire "github.com/portpowered/go-agent-harness/agent-cli/internal/services/wire"

import (
	"bytes"
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	textsessionwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/wire"
	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/spf13/cobra"
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

func newChatTestSessionService(inferencer messages.Inferencer, defs []messages.ToolDefinition) session.Service {
	return textsessionwire.NewService(textsessionwire.Dependencies{
		ToolExecutor: chatTestToolExecutor{}, ToolDefinitions: defs,
		Inferencer: inferencer, RelaxValidation: true,
	})
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
	return executeInteractiveRoot(t, agentCLI, args, input)
}

func executeRoot(t *testing.T, agentCLI *AgentCLI, args []string, input string) chatRun {
	t.Helper()
	return executeRootWithMicrophone(t, agentCLI, args, input, func() (audio.AudioSource, error) {
		return audio.NewSliceSource(nil), nil
	})
}

func executeInteractiveRoot(t *testing.T, agentCLI *AgentCLI, args []string, input string) chatRun {
	t.Helper()
	original := chatInputIsInteractive
	chatInputIsInteractive = func(*cobra.Command) bool { return true }
	t.Cleanup(func() { chatInputIsInteractive = original })
	return executeRoot(t, agentCLI, args, input)
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
	textService := newChatTestSessionService(inferencer, services.DefaultToolDefs(nil))
	askFlags := flags.NewAskFlags()
	askFlags.NoSystemInformation = true
	loopFlags := flags.NewLoopFlags()
	chatFlags := flags.NewChatFlags()
	router := NewRouter(
		globalFlags,
		rootCommand,
		NewAskCommand(textService, askFlags, loopFlags, globalFlags),
		NewChatCommand(textService, askFlags, loopFlags, chatFlags, globalFlags, testFileStoreFactory()),
		NewToolCommand(globalFlags),
		NewInteractionCommand(),
		NewInteractionReplayCommand(),
		NewProbeCommand(),
		NewProbeRunCommandWithDeviceService(newDevicesTestService(), nil, nil),
		NewProbeGateCommand(),
		NewProbeReportCommand(),
		NewProbeFleetCommand(nil, nil),
		NewSessionCommand(askFlags, globalFlags, newTestSessionService(sessionservicewire.SessionDependencies{Clock: sessionclock.Real{}}), nil),
		NewSessionShowCommand(globalFlags, testFileStoreFactory()),
		NewSessionListCommand(globalFlags, testFileStoreFactory()),
		NewSessionDeleteCommand(globalFlags, testFileStoreFactory()),
		NewSessionReplayCommand(nil),
		newTestRoomRunCommand(globalFlags, defaultTestDeviceRegistry{}),
		NewConfigCommand(),
		NewConfigAddLocalCommand(globalFlags),
		defaultTestDeviceRegistry{},
		newDevicesTestService(),
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
			wantStdoutPart: "Usage:\n  yui chat [flags]",
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

func TestChatCommandRejectsNonInteractiveInputBeforeSetup(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "text chat", args: []string{"chat"}},
		{name: "loop chat", args: []string{"chat", "--loop"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inf := &chatTestInferencer{response: "must not run"}
			started := time.Now()
			got := executeRoot(t, newTestAgentCLI(t, inf), tt.args, "input that must not be consumed\n")
			if elapsed := time.Since(started); elapsed >= time.Second {
				t.Fatalf("non-interactive chat took %s, want less than one second", elapsed)
			}
			if got.exitCode != 1 || got.err == nil {
				t.Fatalf("exit code=%d error=%v, want immediate command failure", got.exitCode, got.err)
			}
			if got.err.Error() != chatInteractiveTerminalMessage {
				t.Fatalf("error = %q, want %q", got.err, chatInteractiveTerminalMessage)
			}
			if got.stdout != "" || got.stderr != "" {
				t.Fatalf("output = stdout %q stderr %q, want both empty", got.stdout, got.stderr)
			}
			if inf.calls != 0 {
				t.Fatalf("inferencer calls = %d, want zero before terminal admission", inf.calls)
			}
		})
	}
}

func normalizeTraceIDs(output string) string {
	return regexp.MustCompile(`(Trace ID: |trace: )\d+`).ReplaceAllString(output, `${1}<trace>`)
}

func TestChatCommand_AudioHelperStopsAtEOF(t *testing.T) {
	inf := &chatTestInferencer{response: "unused"}
	textService := newChatTestSessionService(inf, nil)
	global := flags.NewGlobalFlags()
	global.ConfigDirPath = t.TempDir()
	ask := flags.NewAskFlags()
	ask.NoSystemInformation = true
	var out, errOut bytes.Buffer

	err := RunChatWithAudio(context.Background(), &out, &errOut, textService, global, ask, audio.NewSliceSource(nil))
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

func TestChatCommand_LoopBranches(t *testing.T) {
	tests := []struct {
		name          string
		args          []string
		input         string
		response      string
		callErr       error
		wantExit      int
		wantOutput    []string
		wantCalls     int
		wantErrorPart string
	}{
		{
			// Regression guard for "out-of-range numeric flags are silently
			// discarded": --max-iterations 0 used to fall into
			// runLoopChat's own "maxIter <= 0 -> default to 5" fallback
			// with no warning at all. It must now be rejected before any
			// task prompt or iteration runs.
			name:          "zero max iterations is rejected",
			args:          []string{"chat", "--loop", "--max-iterations", "0"},
			wantExit:      1,
			wantErrorPart: "--max-iterations must be a positive integer, got 0",
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
			if tt.wantErrorPart != "" && (got.err == nil || !strings.Contains(got.err.Error(), tt.wantErrorPart)) {
				t.Fatalf("error = %v, want substring %q", got.err, tt.wantErrorPart)
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
			storage := newManagedSessionStoreForTest(t, configDir)
			if err := storage.SaveTrace(t.Context(), session.TraceRecord{
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
			got := executeInteractiveRoot(t, newTestAgentCLIAt(t, inf, configDir), []string{"chat", "--loop", "--trace-id", "trace-resume"}, tt.input)
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

func newManagedSessionStoreForTest(t *testing.T, directory string) session.ManagedStore {
	t.Helper()
	store, err := textsessionwire.NewFileStoreFactory().Open(session.FileStoreOptions{Directory: directory, WorkspaceDirectory: directory})
	if err != nil {
		t.Fatal(err)
	}
	return store
}
