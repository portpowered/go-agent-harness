package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/input"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	sessionwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/wire"
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
		stream := make(chan messages.StreamMessage, 1)
		stream <- messages.StreamMessage{Type: messages.StreamTypeError, Role: messages.RoleAssistant, Value: messages.NewErrorValueWithError(err)}
		close(stream)
		return stream, nil
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

type askTestReaderError struct{ err error }

func (r askTestReaderError) Read([]byte) (int, error) { return 0, r.err }

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

func newAskTestSubject(t *testing.T, inf *askTestInferencer) (*AskCommand, *cobra.Command, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = t.TempDir()
	askFlags := flags.NewAskFlags()
	askFlags.SystemPrompt = "none"
	askFlags.NoSystemInformation = true
	loopFlags := flags.NewLoopFlags()
	executor := sessionwire.NewService(sessionwire.Dependencies{
		ToolExecutor: askTestToolExecutor{}, Inferencer: inf,
		Resolver: askTestResolver(globalFlags),
	})
	subject := NewAskCommand(executor, askFlags, loopFlags, globalFlags)
	cmd := subject.Generate()
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	return subject, cmd, stdout, stderr
}

// The injected test inferencer accepts text attachments; its capability catalog
// must not accidentally inherit the user's configured real model restrictions.
func askTestResolver(globalFlags *flags.GlobalFlags) session.Resolver {
	host := services.NewSessionResolverWithStoreFactory(globalFlags, sessionwire.NewFileStoreFactory())
	return session.ResolverFunc(func(ctx context.Context, request session.Request) (session.Resolution, error) {
		resolved, err := host.Resolve(ctx, request)
		if err != nil {
			return session.Resolution{}, err
		}
		resolved.Models = []session.ModelInfo{{Name: resolved.Provider.Model, SupportedInputMimeTypes: []string{"text/plain"}}}
		return resolved, nil
	})
}

func runAskTestCommand(t *testing.T, args []string, stdin io.Reader, inf *askTestInferencer, out, errOut io.Writer) error {
	t.Helper()
	_, cmd, _, _ := newAskTestSubject(t, inf)
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
		wantSummary string
		wantCalls   int
		wantStreams int
		wantErr     string
		wantIs      error
	}{
		{name: "one-shot prompt", args: []string{"summarize this"}, response: "one-shot answer", wantOutput: "one-shot answer\n", wantCalls: 1},
		{name: "piped context plus prompt", args: []string{"answer from context"}, stdin: strings.NewReader("context text"), response: "context answer", wantOutput: "context answer\n", wantCalls: 1},
		{name: "stream mode", args: []string{"--stream", "stream it"}, response: "stream answer", wantOutput: "stream answer", wantCalls: 1, wantStreams: 1},
		{name: "loop mode", args: []string{"--loop", "--max-iterations", "2", "--stop-word", "DONE", "iterate"}, response: "DONE", wantOutput: "Trace ID:", wantSummary: "[Loop complete: 1 iteration(s), completed: true, trace: ", wantCalls: 1},
		{name: "loop-only flag without loop", args: []string{"--stop-word", "DONE", "prompt"}, wantErr: "--stop-word requires --loop", wantIs: errAskFlagConflict},
		{name: "record and replay conflict", args: []string{"--record", "capture", "--replay", "replay", "prompt"}, wantErr: "cannot use --record and --replay together", wantIs: errAskFlagConflict},
		{name: "malformed numeric flag", args: []string{"--loop", "--max-iterations", "not-a-number", "prompt"}, wantErr: `invalid argument "not-a-number" for "--max-iterations"`},
		{name: "conflicting session flags", args: []string{"--session-id", "session-1", "--continue-last-session", "prompt"}, wantErr: "cannot use session ID and continue last session together", wantIs: errAskFlagConflict},
		// Regression guards for "out-of-range numeric flags are silently
		// discarded": these values used to be accepted and then quietly
		// ignored (a negative --max-iterations fell into the "<=0 -> default
		// to 5" fallback; --context-pressure-threshold outside (0,1] either
		// disabled the notifier or wired it up permanently inert). They must
		// now be rejected before any inference call.
		{name: "negative max-iterations is rejected", args: []string{"--loop", "--max-iterations", "-5", "prompt"}, wantErr: "--max-iterations must be a positive integer, got -5", wantIs: errAskFlagConflict},
		{name: "negative context-pressure-threshold is rejected", args: []string{"--loop", "--context-pressure-threshold", "-3", "prompt"}, wantErr: "--context-pressure-threshold must be greater than 0 and at most 1", wantIs: errAskFlagConflict},
		{name: "context-pressure-threshold above 1 is rejected", args: []string{"--loop", "--context-pressure-threshold", "5000", "prompt"}, wantErr: "--context-pressure-threshold must be greater than 0 and at most 1", wantIs: errAskFlagConflict},
		// No-over-triggering guard: an in-range --context-pressure-threshold
		// alongside a valid --max-iterations must still run normally.
		{name: "in-range loop flags are accepted", args: []string{"--loop", "--max-iterations", "2", "--context-pressure-threshold", "0.5", "--stop-word", "DONE", "iterate"}, response: "DONE", wantOutput: "Trace ID:", wantSummary: "[Loop complete: 1 iteration(s), completed: true, trace: ", wantCalls: 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
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
				if tc.wantIs != nil && !errors.Is(err, tc.wantIs) {
					t.Fatalf("error = %v, want wrapped identity %v", err, tc.wantIs)
				}
				return
			}
			if err != nil {
				t.Fatalf("execute ask: %v", err)
			}
			if tc.wantOutput != "" && !strings.Contains(stdout.String(), tc.wantOutput) {
				t.Fatalf("stdout = %q, want substring %q", stdout.String(), tc.wantOutput)
			}
			assertAskLoopSummary(t, stdout.String(), tc.wantSummary)
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

func assertAskLoopSummary(t *testing.T, output, prefix string) {
	t.Helper()
	if prefix == "" {
		return
	}

	start := strings.Index(output, prefix)
	if start < 0 {
		t.Fatalf("stdout = %q, want loop summary prefix %q", output, prefix)
	}
	summaryLine := strings.SplitN(output[start:], "\n", 2)[0]
	if !strings.HasSuffix(summaryLine, "]") {
		t.Fatalf("loop summary = %q, want closing bracket", summaryLine)
	}
	traceID := strings.TrimSuffix(strings.TrimPrefix(summaryLine, prefix), "]")
	if traceID == "" {
		t.Fatal("loop summary did not include a trace ID")
	}
	if !strings.Contains(output, "Trace ID: "+traceID+"\n") {
		t.Errorf("stdout = %q, want summary trace ID %q to match trace banner", output, traceID)
	}
}

func TestAskCommandRejectsInvalidAttachmentsBeforeInference(t *testing.T) {
	dir := t.TempDir()
	directory := filepath.Join(dir, "directory")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	unsupported := filepath.Join(dir, "opaque.bin")
	if err := os.WriteFile(unsupported, []byte{0, 1, 2, 3}, 0o600); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name   string
		path   string
		reason string
	}{
		{name: "missing", path: filepath.Join(dir, "missing.txt"), reason: input.AttachmentReasonMissing},
		{name: "directory", path: directory, reason: input.AttachmentReasonNotRegular},
		{name: "unsupported content", path: unsupported, reason: input.AttachmentReasonUnsupported},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inf := &askTestInferencer{}
			stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
			err := runAskTestCommand(t, []string{"describe this", tc.path}, strings.NewReader(""), inf, stdout, stderr)
			if err == nil {
				t.Fatal("expected invalid attachment error")
			}
			if !strings.Contains(err.Error(), tc.path) || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("error = %q, want supplied path and reason %q", err, tc.reason)
			}
			if strings.Count(err.Error(), tc.path) != 1 {
				t.Errorf("error = %q, want supplied path exactly once", err)
			}
			inf.mu.Lock()
			calls := inf.inferCalls
			inf.mu.Unlock()
			if calls != 0 {
				t.Fatalf("inference calls = %d, want zero for rejected attachment", calls)
			}
			if stdout.Len() != 0 || stderr.Len() != 0 {
				t.Fatalf("stdout/stderr = %q/%q, want no command output before final rendering", stdout, stderr)
			}
		})
	}
}

func TestAskCommandValidAttachmentsReachInferenceExactlyOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(path, []byte("notes"), 0o600); err != nil {
		t.Fatal(err)
	}

	inf := &askTestInferencer{}
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	err := runAskTestCommand(t, []string{"summarize this", path}, strings.NewReader(""), inf, stdout, stderr)
	if err != nil {
		t.Fatalf("execute ask: %v", err)
	}
	inf.mu.Lock()
	defer inf.mu.Unlock()
	if inf.inferCalls != 1 || len(inf.requests) != 1 {
		t.Fatalf("inference calls/requests = %d/%d, want exactly one", inf.inferCalls, len(inf.requests))
	}
	var found bool
	for _, message := range inf.requests[0].Messages {
		if message.Role != messages.RoleUser {
			continue
		}
		if message.TextContent() != "summarize this" || len(message.ContentParts) != 2 {
			t.Fatalf("user message = %#v, want prompt plus one attachment", message)
		}
		var part messages.FilePart
		var ok bool
		for _, contentPart := range message.ContentParts {
			if candidate, isFile := contentPart.(messages.FilePart); isFile {
				part, ok = candidate, true
				break
			}
		}
		if !ok || part.Name != "notes.txt" || part.MediaType != "text/plain" || string(part.Bytes) != "notes" {
			t.Fatalf("attachment parts = %#v, want one text/plain notes.txt part", message.ContentParts)
		}
		found = true
	}
	if !found {
		t.Fatal("inference request did not contain the expected user message")
	}
}

func TestAskCommandValidThenInvalidAttachmentSendsNoPartialRequest(t *testing.T) {
	dir := t.TempDir()
	valid := filepath.Join(dir, "notes.txt")
	missing := filepath.Join(dir, "later-missing.txt")
	if err := os.WriteFile(valid, []byte("notes"), 0o600); err != nil {
		t.Fatal(err)
	}

	inf := &askTestInferencer{}
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	err := runAskTestCommand(t, []string{"summarize this", valid, missing}, strings.NewReader(""), inf, stdout, stderr)
	if err == nil {
		t.Fatal("expected later invalid attachment error")
	}
	if !strings.Contains(err.Error(), missing) || !strings.Contains(err.Error(), input.AttachmentReasonMissing) {
		t.Fatalf("error = %q, want later path and missing-file reason", err)
	}
	inf.mu.Lock()
	calls := inf.inferCalls
	requests := len(inf.requests)
	inf.mu.Unlock()
	if calls != 0 || requests != 0 {
		t.Fatalf("inference calls/requests = %d/%d, want zero for atomic attachment rejection", calls, requests)
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("stdout/stderr = %q/%q, want no command output before final rendering", stdout, stderr)
	}
}

func TestAskCommandRejectsUnreadableAttachmentBeforeInference(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mode-bit unreadability is not portable to Windows")
	}

	path := filepath.Join(t.TempDir(), "unreadable.txt")
	if err := os.WriteFile(path, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(path, 0o600)

	inf := &askTestInferencer{}
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	err := runAskTestCommand(t, []string{"describe this", path}, strings.NewReader(""), inf, stdout, stderr)
	if err == nil {
		t.Fatal("expected unreadable attachment error")
	}
	if !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), input.AttachmentReasonUnreadable) {
		t.Fatalf("error = %q, want supplied path and unreadable-file reason", err)
	}
	if strings.Count(err.Error(), path) != 1 {
		t.Errorf("error = %q, want supplied path exactly once", err)
	}
	inf.mu.Lock()
	calls := inf.inferCalls
	inf.mu.Unlock()
	if calls != 0 {
		t.Fatalf("inference calls = %d, want zero for rejected attachment", calls)
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("stdout/stderr = %q/%q, want no command output before final rendering", stdout, stderr)
	}
}

func TestAskCommandS4ErrorTable(t *testing.T) {
	sentinel := errors.New("inferencer failed")
	stdinErr := errors.New("stdin failed")
	outputErr := errors.New("output failed")
	traceErr := errors.New("trace failed")
	summaryErr := errors.New("summary failed")
	tests := []struct {
		name       string
		args       []string
		stdin      io.Reader
		inf        *askTestInferencer
		out        io.Writer
		wantErrors []string
		wantIs     []error
	}{
		{name: "input construction", inf: &askTestInferencer{}, wantErrors: []string{"no prompt: provide a prompt"}, wantIs: []error{errAskInput}},
		{name: "stdin construction", args: []string{"prompt"}, stdin: askTestReaderError{err: stdinErr}, inf: &askTestInferencer{}, wantErrors: []string{"read stdin: stdin failed"}, wantIs: []error{errAskInput, stdinErr}},
		{name: "one-shot execution", args: []string{"prompt"}, inf: &askTestInferencer{err: sentinel}, wantErrors: []string{"execution failed: inferencer failed"}, wantIs: []error{errAskExecution}},
		{name: "one-shot output", args: []string{"prompt"}, inf: &askTestInferencer{}, out: askTestFailWriter{err: outputErr}, wantErrors: []string{"execution failed: write output: output failed"}, wantIs: []error{errAskExecution, outputErr}},
		{name: "loop setup writer", args: []string{"--loop", "prompt"}, inf: &askTestInferencer{}, out: askTestFailWriter{err: traceErr, substring: "Trace ID:"}, wantErrors: []string{"loop execution failed: write trace ID: trace failed"}, wantIs: []error{errAskLoopExecution, traceErr}},
		{name: "loop summary", args: []string{"--loop", "--max-iterations", "1", "prompt"}, inf: &askTestInferencer{responses: []messages.InferenceResult{textInference("loop answer")}}, out: askTestFailWriter{err: summaryErr, substring: "Loop complete"}, wantErrors: []string{"write loop summary: summary failed"}, wantIs: []error{errAskLoopSummary, summaryErr}},
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
			for _, wantIs := range tc.wantIs {
				if !errors.Is(err, wantIs) {
					t.Errorf("error = %v, want wrapped identity %v", err, wantIs)
				}
			}
		})
	}
}

func TestAskCommandPropagatesPromptAndFlags(t *testing.T) {
	inf := &askTestInferencer{}
	subject, cmd, stdout, stderr := newAskTestSubject(t, inf)
	subject.globalFlags.VerboseMode = 2
	subject.globalFlags.LogToStdout = true
	const (
		wantSystemPrompt = "custom system prompt"
		wantModel        = "test-model"
		wantProvider     = "test-provider"
		wantAPIKey       = "test-api-key"
		wantBaseURL      = "https://provider.test/v1"
		wantModality     = "audio"
		wantModelConfig  = `{"temperature":0.2}`
	)
	cmd.SetArgs([]string{
		"--system-prompt", wantSystemPrompt,
		"--model", wantModel,
		"--provider", wantProvider,
		"--api-key", wantAPIKey,
		"--base-url", wantBaseURL,
		"--stream",
		"--output-json",
		"--output-reasoning-tokens",
		"--output-modality", wantModality,
		"--model-config", wantModelConfig,
		"question",
	})
	cmd.SetIn(strings.NewReader(""))
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	var gotCfg *session.Request
	var gotInput agentloop.ExecuteInput
	subject.runAsk = func(_ context.Context, cfg *session.Request, input agentloop.ExecuteInput) (string, error) {
		gotCfg = cfg
		gotInput = input
		return "answer", nil
	}
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute ask: %v", err)
	}
	if gotCfg == nil {
		t.Fatal("ask execution seam was not called")
	}
	if gotCfg.SystemPrompt != wantSystemPrompt {
		t.Errorf("system prompt = %q, want %q", gotCfg.SystemPrompt, wantSystemPrompt)
	}
	if gotCfg.Model != wantModel || gotCfg.Provider != wantProvider {
		t.Errorf("model/provider = %q/%q, want %q/%q", gotCfg.Model, gotCfg.Provider, wantModel, wantProvider)
	}
	if gotCfg.APIKey != wantAPIKey || gotCfg.BaseURL != wantBaseURL {
		t.Errorf("API key/base URL = %q/%q, want %q/%q", gotCfg.APIKey, gotCfg.BaseURL, wantAPIKey, wantBaseURL)
	}
	if !subject.askFlags.Stream || !subject.askFlags.OutputJSON || !subject.askFlags.OutputReasoningTokens {
		t.Errorf("CLI output flags = %+v, want stream/json/reasoning true", subject.askFlags)
	}
	if gotCfg.OutputModality != wantModality || gotCfg.ModelConfig != wantModelConfig {
		t.Errorf("modality/model config = %q/%q, want %q/%q", gotCfg.OutputModality, gotCfg.ModelConfig, wantModality, wantModelConfig)
	}
	if subject.globalFlags.VerboseMode != 2 || !subject.globalFlags.LogToStdout {
		t.Errorf("CLI global flags = %+v, want verbosity2/log-to-stdout true", subject.globalFlags)
	}
	if gotInput.Message != "question" || len(gotInput.ContentParts) != 0 {
		t.Errorf("execute input = %#v, want text-only question", gotInput)
	}
	if stdout.String() != "answer" {
		t.Errorf("stdout = %q, want answer from execution seam", stdout.String())
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

func TestAskCommandS4PreservesExecutionIdentity(t *testing.T) {
	want := errors.New("inferencer sentinel")
	subject, cmd, stdout, stderr := newAskTestSubject(t, &askTestInferencer{})
	subject.runAsk = func(context.Context, *session.Request, agentloop.ExecuteInput) (string, error) {
		return "", want
	}
	cmd.SetArgs([]string{"prompt"})
	cmd.SetIn(strings.NewReader(""))
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	err := cmd.ExecuteContext(context.Background())
	if err == nil || !errors.Is(err, want) {
		t.Fatalf("error = %v, want supplied execution identity", err)
	}
	if !errors.Is(err, errAskExecution) {
		t.Fatalf("error = %v, want CLI execution identity", err)
	}
}

// TestAskCommandLoopTotalFailureExitsNonZeroWithoutSuccessBanner pins
// Instance 2 of the exit-code-must-mean-the-work-happened defect family:
// "--loop" used to print "Loop complete" and exit 0 even when every
// iteration failed and nothing was accomplished. Per-iteration fault
// isolation inside RunIterativeLoop is untouched (each iteration still runs
// and its error is still recorded, same as before); only the CLI boundary
// now refuses to report a run that never produced one successful iteration
// as a success.
func TestAskCommandLoopTotalFailureExitsNonZeroWithoutSuccessBanner(t *testing.T) {
	sentinel := errors.New("inference unavailable")
	inf := &askTestInferencer{err: sentinel}
	stdout := &bytes.Buffer{}
	err := runAskTestCommand(t, []string{"--loop", "--max-iterations", "2", "prompt"}, strings.NewReader(""), inf, stdout, &bytes.Buffer{})
	if err == nil {
		t.Fatalf("loop with every iteration failing returned nil error; want a named non-zero failure instead of a silent success banner; stdout=%q", stdout.String())
	}
	if !errors.Is(err, errAskLoopTotalFailure) {
		t.Fatalf("error = %v, want wrapped identity errAskLoopTotalFailure", err)
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want wrapped underlying inference error", err)
	}
	if !strings.Contains(err.Error(), "2 iteration") {
		t.Fatalf("error = %q, want it to name the iteration count", err)
	}
	if strings.Contains(stdout.String(), "Loop complete") {
		t.Fatalf("stdout = %q, must not print a success banner when every iteration failed", stdout.String())
	}
	inf.mu.Lock()
	calls := inf.inferCalls
	inf.mu.Unlock()
	if calls != 2 {
		t.Fatalf("inference calls = %d, want 2 (one per iteration, no early stop)", calls)
	}
}

// TestAskCommandLoopAllIterationsSucceedStillPrintsLoopComplete asserts no
// over-triggering from the total-failure fix: a loop that runs its full
// iteration budget with every iteration succeeding (no stop word reached)
// must still print the success banner and exit 0, exactly as before.
func TestAskCommandLoopAllIterationsSucceedStillPrintsLoopComplete(t *testing.T) {
	inf := &askTestInferencer{responses: []messages.InferenceResult{textInference("first"), textInference("second")}}
	stdout := &bytes.Buffer{}
	err := runAskTestCommand(t, []string{"--loop", "--max-iterations", "2", "prompt"}, strings.NewReader(""), inf, stdout, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("loop with every iteration succeeding returned an error: %v\nstdout=%q", err, stdout.String())
	}
	if !strings.Contains(stdout.String(), "Loop complete: 2 iteration(s)") {
		t.Fatalf("stdout = %q, want the success banner for a fully successful loop", stdout.String())
	}
}
