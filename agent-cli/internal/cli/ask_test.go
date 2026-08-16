package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/agent"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/spf13/cobra"
)

type askTestInferencer struct {
	mu          sync.Mutex
	responses   []messages.InferenceResult
	err         error
	inferCalls  int
	streamCalls int
	requests    []messages.InferenceRequest
}

func (f *askTestInferencer) next(req messages.InferenceRequest) (messages.InferenceResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, req)
	f.inferCalls++
	if f.err != nil {
		return messages.InferenceResult{}, f.err
	}
	if len(f.responses) == 0 {
		return messages.InferenceResult{Message: messages.NewTextMessage(messages.RoleAssistant, "ok")}, nil
	}
	index := f.inferCalls - 1
	if index >= len(f.responses) {
		index = len(f.responses) - 1
	}
	return f.responses[index], nil
}

func (f *askTestInferencer) Infer(_ context.Context, req messages.InferenceRequest) (messages.InferenceResult, error) {
	return f.next(req)
}

func (f *askTestInferencer) InferStream(_ context.Context, req messages.InferenceRequest) (<-chan messages.StreamMessage, error) {
	f.mu.Lock()
	f.streamCalls++
	f.mu.Unlock()
	result, err := f.next(req)
	if err != nil {
		return nil, err
	}
	stream := make(chan messages.StreamMessage, 8)
	stream <- messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()}
	stream <- messages.StreamMessage{Type: messages.StreamTypeTextStart, Role: messages.RoleAssistant, Value: messages.NewTextStartValue()}
	if text := result.Message.TextContent(); text != "" {
		stream <- messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue(text)}
	}
	stream <- messages.StreamMessage{Type: messages.StreamTypeTextEnd, Role: messages.RoleAssistant, Value: messages.NewTextEndValue()}
	stream <- messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(result.TokenUsage)}
	close(stream)
	return stream, nil
}

type askTestToolExecutor struct{}

func (askTestToolExecutor) Execute(_ context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	return messages.ToolCallResponse{ToolCallID: call.ID, Name: call.Name}, nil
}

type askTestReaderError struct{}

func (askTestReaderError) Read([]byte) (int, error) { return 0, errors.New("stdin failed") }

type askTestFailWriter struct {
	err       error
	substring string
}

func (w askTestFailWriter) Write(p []byte) (int, error) {
	if w.substring == "" || strings.Contains(string(p), w.substring) {
		return 0, w.err
	}
	return len(p), nil
}

func newAskTestCommand(t *testing.T, inf *askTestInferencer) (*cobra.Command, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = t.TempDir()
	askFlags := flags.NewAskFlags()
	askFlags.SystemPrompt = "none"
	askFlags.NoSystemInformation = true
	loopFlags := flags.NewLoopFlags()
	executor := agent.NewExecutor(askTestToolExecutor{}, nil, inf)
	cmd := NewAskCommand(executor, askFlags, loopFlags, globalFlags).Generate()
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	return cmd, stdout, stderr
}

func runAskTestCommand(t *testing.T, args []string, stdin io.Reader, inf *askTestInferencer, out, errOut io.Writer) error {
	t.Helper()
	cmd, _, _ := newAskTestCommand(t, inf)
	cmd.SetArgs(args)
	cmd.SetIn(stdin)
	cmd.SetOut(out)
	cmd.SetErr(errOut)
	return cmd.ExecuteContext(context.Background())
}

func textInference(text string) messages.InferenceResult {
	return messages.InferenceResult{Message: messages.NewTextMessage(messages.RoleAssistant, text)}
}

func TestAskCommandS2FlagMatrix(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		stdin       io.Reader
		response    string
		wantOutput  string
		wantCalls   int
		wantStreams int
		skipReason  string
		wantErr     string
	}{
		{name: "one-shot prompt", args: []string{"summarize this"}, response: "one-shot answer", wantOutput: "one-shot answer\n", wantCalls: 1},
		{name: "piped context plus prompt", args: []string{"answer from context"}, stdin: strings.NewReader("context text"), response: "context answer", wantOutput: "context answer\n", wantCalls: 1},
		{name: "stream mode", args: []string{"--stream", "stream it"}, response: "stream answer", wantOutput: "stream answer", wantCalls: 1, wantStreams: 1},
		{name: "loop mode", args: []string{"--loop", "--max-iterations", "2", "--stop-word", "DONE", "iterate"}, response: "DONE", wantOutput: "Trace ID:", wantCalls: 1},
		{name: "loop-only flag without loop", args: []string{"--stop-word", "DONE", "prompt"}, skipReason: "production currently accepts loop-only flags without --loop; preserve the intended validation assertion for review", wantErr: "requires --loop"},
		{name: "record and replay conflict", args: []string{"--record", "capture", "--replay", "replay", "prompt"}, skipReason: "production currently accepts --record and --replay together; preserve the intended validation assertion for review", wantErr: "cannot use --record and --replay together"},
		{name: "malformed numeric flag", args: []string{"--loop", "--max-iterations", "not-a-number", "prompt"}, wantErr: `invalid argument "not-a-number" for "--max-iterations"`},
		{name: "conflicting session flags", args: []string{"--session-id", "session-1", "--continue-last-session", "prompt"}, wantErr: "cannot use session ID and continue last session together"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.skipReason != "" {
				t.Skip(tc.skipReason)
			}
			inf := &askTestInferencer{responses: []messages.InferenceResult{textInference(tc.response)}}
			stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
			if tc.stdin == nil {
				tc.stdin = strings.NewReader("")
			}
			err := runAskTestCommand(t, tc.args, tc.stdin, inf, stdout, stderr)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want message containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("execute ask: %v", err)
			}
			if tc.wantOutput != "" && !strings.Contains(stdout.String(), tc.wantOutput) {
				t.Fatalf("stdout = %q, want substring %q", stdout.String(), tc.wantOutput)
			}
			inf.mu.Lock()
			calls, streams := inf.inferCalls, inf.streamCalls
			inf.mu.Unlock()
			if calls != tc.wantCalls {
				t.Errorf("inference calls = %d, want %d", calls, tc.wantCalls)
			}
			if tc.wantStreams > 0 && streams != tc.wantStreams {
				t.Errorf("stream calls = %d, want %d", streams, tc.wantStreams)
			}
		})
	}
}

func TestAskCommandS4ErrorTable(t *testing.T) {
	sentinel := errors.New("inferencer failed")
	tests := []struct {
		name       string
		args       []string
		stdin      io.Reader
		inf        *askTestInferencer
		out        io.Writer
		wantErrors []string
	}{
		{name: "input construction", inf: &askTestInferencer{}, wantErrors: []string{"no prompt: provide a prompt"}},
		{name: "stdin construction", args: []string{"prompt"}, stdin: askTestReaderError{}, inf: &askTestInferencer{}, wantErrors: []string{"read stdin: stdin failed"}},
		{name: "one-shot execution", args: []string{"prompt"}, inf: &askTestInferencer{err: sentinel}, wantErrors: []string{"execution failed: inferencer failed"}},
		{name: "one-shot output", args: []string{"prompt"}, inf: &askTestInferencer{}, out: askTestFailWriter{err: errors.New("output failed")}, wantErrors: []string{"execution failed: write output: output failed"}},
		{name: "loop setup writer", args: []string{"--loop", "prompt"}, inf: &askTestInferencer{}, out: askTestFailWriter{err: errors.New("trace failed"), substring: "Trace ID:"}, wantErrors: []string{"loop execution failed: write trace ID: trace failed"}},
		{name: "loop summary", args: []string{"--loop", "--max-iterations", "1", "prompt"}, inf: &askTestInferencer{responses: []messages.InferenceResult{textInference("loop answer")}}, out: askTestFailWriter{err: errors.New("summary failed"), substring: "Loop complete"}, wantErrors: []string{"write loop summary: summary failed"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.stdin == nil {
				tc.stdin = strings.NewReader("")
			}
			if tc.out == nil {
				tc.out = &bytes.Buffer{}
			}
			errOut := &bytes.Buffer{}
			err := runAskTestCommand(t, tc.args, tc.stdin, tc.inf, tc.out, errOut)
			if err == nil {
				t.Fatal("expected command error")
			}
			for _, want := range tc.wantErrors {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error = %q, want substring %q", err, want)
				}
			}
		})
	}
}

func TestAskCommandPropagatesPromptAndFlags(t *testing.T) {
	inf := &askTestInferencer{responses: []messages.InferenceResult{textInference("answer")}}
	cmd, stdout, _ := newAskTestCommand(t, inf)
	cmd.SetArgs([]string{"--system-prompt", "none", "question"})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute ask: %v", err)
	}
	if stdout.Len() == 0 {
		t.Fatal("ask produced no output")
	}
	inf.mu.Lock()
	defer inf.mu.Unlock()
	if len(inf.requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(inf.requests))
	}
	if got := inf.requests[0].Messages[len(inf.requests[0].Messages)-1].TextContent(); got != "question" {
		t.Errorf("last request message = %q, want question", got)
	}
}

func TestAskCommandWriterErrorKeepsIdentity(t *testing.T) {
	want := errors.New("writer sentinel")
	inf := &askTestInferencer{responses: []messages.InferenceResult{textInference("answer")}}
	err := runAskTestCommand(t, []string{"prompt"}, strings.NewReader(""), inf, askTestFailWriter{err: want}, &bytes.Buffer{})
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want wrapped writer error", err)
	}
	if !strings.Contains(err.Error(), "execution failed") {
		t.Errorf("error = %v, want execution context", err)
	}
}
