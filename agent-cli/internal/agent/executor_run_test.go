package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/session"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func executorTestConfig(t *testing.T, dir string) *Config {
	t.Helper()
	return &Config{
		ConfigDir:           dir,
		SystemPrompt:        "none",
		NoSystemInformation: true,
	}
}

func executorInferenceCallCount(inf *executorScriptedInferencer) int {
	inf.mu.Lock()
	defer inf.mu.Unlock()
	return len(inf.requests)
}

func executorInferenceRequests(inf *executorScriptedInferencer) []messages.InferenceRequest {
	inf.mu.Lock()
	defer inf.mu.Unlock()
	return append([]messages.InferenceRequest(nil), inf.requests...)
}

func TestExecutorRun_ContinuationAndPureHelperBranches(t *testing.T) {
	exec := NewExecutor(nil, nil, stubInferencer{}, true)

	if got := exec.buildInferenceDefaults(&config.Config{}); got != nil {
		t.Fatalf("zero repetition penalty defaults = %+v, want nil", got)
	}
	if got := exec.buildInferenceDefaults(&config.Config{Model: config.ModelConfig{RepetitionPenalty: 1.0}}); got != nil {
		t.Fatalf("unit repetition penalty defaults = %+v, want nil", got)
	}
	defaults := exec.buildInferenceDefaults(&config.Config{Model: config.ModelConfig{RepetitionPenalty: 1.5}})
	if defaults == nil || defaults.FrequencyPenalty == nil || *defaults.FrequencyPenalty != 1.5 {
		t.Fatalf("repetition defaults = %+v, want frequency penalty 1.5", defaults)
	}

	if got := BuildIterationAnnotation(2, 5, ""); got != "You are on iteration 2 of 5." {
		t.Fatalf("annotation without stop word = %q", got)
	}
	if got := BuildIterationAnnotation(2, 5, "DONE"); !strings.Contains(got, "end your response with DONE") {
		t.Fatalf("annotation with stop word = %q", got)
	}

	for _, tt := range []struct {
		name       string
		history    []messages.Message
		stopWord   string
		wantQueued string
	}{
		{name: "no assistant", history: []messages.Message{messages.NewTextMessage(messages.RoleUser, "user")}},
		{name: "tool call", history: []messages.Message{{Role: messages.RoleAssistant, ToolCalls: []messages.ToolCall{{ID: "id"}}}}},
		{name: "stop word", history: []messages.Message{messages.NewTextMessage(messages.RoleAssistant, "contains DONE")}, stopWord: "DONE"},
		{name: "early stop", history: []messages.Message{messages.NewTextMessage(messages.RoleAssistant, "partial")}, wantQueued: "continue"},
		{name: "last non-assistant", history: []messages.Message{
			messages.NewTextMessage(messages.RoleAssistant, "partial"),
			messages.NewTextMessage(messages.RoleUser, "follow-up"),
		}, wantQueued: "continue"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			runData := newExecutorRunData(t, &executorScriptedInferencer{}, nil, nil, tt.history...)
			maybeEnqueueContinuationNudge(runData, tt.stopWord, tt.wantQueued)
			got, ok := runData.Loop.DequeueTodo()
			if tt.wantQueued == "" {
				if ok {
					t.Fatalf("unexpected queued nudge %q", got)
				}
			} else if !ok || got != tt.wantQueued {
				t.Fatalf("queued nudge = (%q, %v), want (%q, true)", got, ok, tt.wantQueued)
			}
		})
	}

	invalidConfig := filepath.Join(t.TempDir(), "config-parent-file")
	if err := os.WriteFile(invalidConfig, []byte("file"), 0o644); err != nil {
		t.Fatal(err)
	}
	if enabled, message := exec.loadContinuationNudgeConfig(&Config{ConfigDir: invalidConfig}); enabled || message != "" {
		t.Fatalf("invalid nudge config = (%v, %q), want disabled", enabled, message)
	}

	disabledDir := t.TempDir()
	writeExecutorConfig(t, disabledDir, validOpenRouterConfig("test-key"))
	if enabled, message := exec.loadContinuationNudgeConfig(&Config{ConfigDir: disabledDir}); enabled || message != "" {
		t.Fatalf("disabled nudge config = (%v, %q), want disabled", enabled, message)
	}

	defaultNudgeDir := t.TempDir()
	writeExecutorConfig(t, defaultNudgeDir, "model:\n  provider: openrouter\n  continuation_nudge_enabled: true\n  openrouter:\n    model: gpt-4o\n    api_key: test-key\n")
	if enabled, message := exec.loadContinuationNudgeConfig(&Config{ConfigDir: defaultNudgeDir}); !enabled || message != DefaultContinuationNudgeMessage {
		t.Fatalf("default nudge config = (%v, %q), want enabled/default", enabled, message)
	}

	customNudgeDir := t.TempDir()
	writeExecutorConfig(t, customNudgeDir, "model:\n  provider: openrouter\n  continuation_nudge_enabled: true\n  continuation_nudge_message: keep-going\n  openrouter:\n    model: gpt-4o\n    api_key: test-key\n")
	if enabled, message := exec.loadContinuationNudgeConfig(&Config{ConfigDir: customNudgeDir}); !enabled || message != "keep-going" {
		t.Fatalf("custom nudge config = (%v, %q), want enabled/keep-going", enabled, message)
	}

	continuationInf := &executorScriptedInferencer{steps: []executorInferenceStep{
		{result: messages.InferenceResult{Message: messages.NewTextMessage(messages.RoleAssistant, "first")}},
		{result: messages.InferenceResult{Message: messages.NewTextMessage(messages.RoleAssistant, "second")}},
	}}
	runData := newExecutorRunData(t, continuationInf, nil, nil)
	runData.Loop.EnqueueTodo("queued follow-up")
	var out strings.Builder
	got, err := exec.executeWithContinuation(context.Background(), runData, agentloop.NewExecuteInput("initial"), &Config{MaxContinuationDepth: 1}, &out, "")
	if err != nil || got != "second" || executorInferenceCallCount(continuationInf) != 2 {
		t.Fatalf("continuation result = (%q, %v), calls=%d; want second/2", got, err, executorInferenceCallCount(continuationInf))
	}
	requests := executorInferenceRequests(continuationInf)
	if len(requests) != 2 || requests[1].Messages[len(requests[1].Messages)-1].TextContent() != "queued follow-up" {
		t.Fatalf("continuation requests = %#v, want queued follow-up as second input", requests)
	}

	nudgeInf := &executorScriptedInferencer{steps: []executorInferenceStep{
		{result: messages.InferenceResult{Message: messages.NewTextMessage(messages.RoleAssistant, "first nudge")}},
		{result: messages.InferenceResult{Message: messages.NewTextMessage(messages.RoleAssistant, "second nudge")}},
	}}
	nudgeData := newExecutorRunData(t, nudgeInf, nil, nil)
	nudgeData.ConfigDir = customNudgeDir
	got, err = exec.executeWithContinuation(context.Background(), nudgeData, agentloop.NewExecuteInput("initial"), &Config{ConfigDir: customNudgeDir, MaxContinuationDepth: 1}, &out, "")
	if err != nil || got != "second nudge" {
		t.Fatalf("nudge continuation = (%q, %v), want second nudge response", got, err)
	}
	nudgeRequests := executorInferenceRequests(nudgeInf)
	if len(nudgeRequests) != 2 || nudgeRequests[1].Messages[len(nudgeRequests[1].Messages)-1].TextContent() != "keep-going" {
		t.Fatalf("nudge requests = %#v, want configured nudge as second input", nudgeRequests)
	}

	secondErrorInf := &executorScriptedInferencer{steps: []executorInferenceStep{
		{result: messages.InferenceResult{Message: messages.NewTextMessage(messages.RoleAssistant, "first")}},
		{streamErr: errors.New("second inference failed")},
	}}
	secondErrorData := newExecutorRunData(t, secondErrorInf, nil, nil)
	secondErrorData.Loop.EnqueueTodo("continue")
	if _, err := exec.executeWithContinuation(context.Background(), secondErrorData, agentloop.NewExecuteInput("initial"), &Config{MaxContinuationDepth: 1}, &out, ""); err == nil || !strings.Contains(err.Error(), "second inference failed") {
		t.Fatalf("second continuation error = %v, want second-call error", err)
	}

	secondStopInf := &executorScriptedInferencer{steps: []executorInferenceStep{
		{result: messages.InferenceResult{Message: messages.NewTextMessage(messages.RoleAssistant, "first")}},
		{result: messages.InferenceResult{Message: messages.NewTextMessage(messages.RoleAssistant, "second DONE")}},
	}}
	secondStopData := newExecutorRunData(t, secondStopInf, nil, nil)
	secondStopData.Loop.EnqueueTodo("continue")
	if got, err := exec.executeWithContinuation(context.Background(), secondStopData, agentloop.NewExecuteInput("initial"), &Config{MaxContinuationDepth: 1}, &out, "DONE"); err != nil || got != "second DONE" {
		t.Fatalf("second stop-word continuation = (%q, %v), want stop response", got, err)
	}

	stopInf := &executorScriptedInferencer{steps: []executorInferenceStep{
		{result: messages.InferenceResult{Message: messages.NewTextMessage(messages.RoleAssistant, "finished DONE")}},
		{result: messages.InferenceResult{Message: messages.NewTextMessage(messages.RoleAssistant, "must not run")}},
	}}
	stopData := newExecutorRunData(t, stopInf, nil, nil)
	stopData.Loop.EnqueueTodo("should remain queued")
	got, err = exec.executeWithContinuation(context.Background(), stopData, agentloop.NewExecuteInput("initial"), &Config{MaxContinuationDepth: 2}, &out, "DONE")
	if err != nil || got != "finished DONE" || executorInferenceCallCount(stopInf) != 1 {
		t.Fatalf("stop-word result = (%q, %v), calls=%d; want one call", got, err, executorInferenceCallCount(stopInf))
	}

	errorInf := &executorScriptedInferencer{steps: []executorInferenceStep{{streamErr: errors.New("inference failed")}}}
	errorData := newExecutorRunData(t, errorInf, nil, nil)
	if _, err := exec.executeWithContinuation(context.Background(), errorData, agentloop.NewExecuteInput("initial"), &Config{}, &out, ""); err == nil || !strings.Contains(err.Error(), "inference failed") {
		t.Fatalf("continuation error = %v, want inference context", err)
	}
}

func TestExecutorRun_BuildLoopUsesInjectedDependenciesAndDefaults(t *testing.T) {
	dir := t.TempDir()
	writeExecutorConfig(t, dir, "model:\n  provider: openrouter\n  repetition_penalty: 1.4\n  openrouter:\n    model: gpt-4o\n    api_key: test-key\n")
	inf := &executorScriptedInferencer{steps: []executorInferenceStep{{result: messages.InferenceResult{Message: messages.NewTextMessage(messages.RoleAssistant, "built")}}}}
	tool := &executorRecordingTool{}
	defs := []messages.ToolDefinition{{Name: "lookup", Description: "lookup value"}}
	exec := NewExecutor(tool, defs, inf, true)
	history := messages.NewTextMessage(messages.RoleUser, "history")
	runData, err := exec.BuildLoop(context.Background(), &Config{
		ConfigDir:           dir,
		NoSystemInformation: true,
		SessionID:           "explicit-session",
		InitialHistory:      []messages.Message{history},
	})
	if err != nil {
		t.Fatalf("BuildLoop() error = %v", err)
	}
	defer runData.CloseLogger()
	if runData.SessionID != "explicit-session" || runData.Models == nil || runData.SessionManager.WorkspaceDir() != dir {
		t.Fatalf("RunData = session=%q models=%v workspace=%q", runData.SessionID, runData.Models != nil, runData.SessionManager.WorkspaceDir())
	}

	if _, err := exec.ExecuteOneTurn(context.Background(), runData, agentloop.NewExecuteInput("question"), &Config{NoSystemInformation: true}, &strings.Builder{}); err != nil {
		t.Fatalf("built loop execution error = %v", err)
	}
	requests := executorInferenceRequests(inf)
	if len(requests) != 1 || len(requests[0].Tools) != 1 || !reflect.DeepEqual(requests[0].Tools[0], defs[0]) {
		t.Fatalf("inference request = %#v, want exact tool definition", requests)
	}
	if requests[0].FrequencyPenalty == nil || *requests[0].FrequencyPenalty != 1.4 {
		t.Fatalf("frequency penalty = %v, want 1.4", requests[0].FrequencyPenalty)
	}
	if len(runData.Loop.GetConversationHistory()) < 2 || !strings.Contains(runData.Loop.GetConversationHistory()[0].TextContent(), "# Agent CLI") {
		t.Fatalf("conversation history = %#v, want generated system prompt and initial history", runData.Loop.GetConversationHistory())
	}

	// With an inferencer override but no injected executor, BuildLoop uses the
	// registry executor path while remaining entirely offline.
	registryExec := NewExecutor(nil, nil, stubInferencer{}, true)
	if data, err := registryExec.BuildLoop(context.Background(), executorTestConfig(t, t.TempDir())); err != nil {
		t.Fatalf("registry BuildLoop() error = %v", err)
	} else {
		data.CloseLogger()
	}

	invalidCfg := &Config{SystemPrompt: "prompt", ContinueLastSession: true}
	if data, err := exec.BuildLoop(context.Background(), invalidCfg); err == nil || data != nil {
		t.Fatalf("invalid BuildLoop() = (%v, %v), want validation error and nil data", data, err)
	}

	replayBase := filepath.Join(t.TempDir(), "capture")
	if err := os.WriteFile(replayBase+".session.json", []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	replayCfg := executorTestConfig(t, t.TempDir())
	replayCfg.ReplayCapturePath = replayBase
	if data, err := registryExec.BuildLoop(context.Background(), replayCfg); err != nil {
		t.Fatalf("replay BuildLoop() error = %v", err)
	} else {
		data.CloseLogger()
	}
}

func TestExecutorRun_BuildLoopConstructsConfiguredProvidersOffline(t *testing.T) {
	tests := []struct {
		name   string
		config string
	}{
		{
			name:   "openrouter",
			config: "model:\n  provider: openrouter\n  openrouter:\n    model: gpt-4o\n    api_key: test-key\n",
		},
		{
			name:   "fal",
			config: "model:\n  provider: fal\n  fal:\n    model: fal-test-model\n    api_key: test-key\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeExecutorConfig(t, dir, tt.config)
			exec := NewExecutor(nil, nil, nil, true)
			data, err := exec.BuildLoop(context.Background(), &Config{
				ConfigDir:           dir,
				SystemPrompt:        "none",
				NoSystemInformation: true,
				ModelConfig:         `{"temperature":0}`,
				Verbose:             true,
			})
			if err != nil {
				t.Fatalf("BuildLoop() error = %v", err)
			}
			data.CloseLogger()
		})
	}
}

func TestExecutorRun_RunAskValidationAndBuildErrors(t *testing.T) {
	exec := NewExecutor(nil, nil, stubInferencer{})
	if _, err := exec.RunAsk(context.Background(), &Config{SystemPrompt: "prompt", ContinueLastSession: true}, agentloop.NewExecuteInput("ask"), &strings.Builder{}); err == nil || !strings.Contains(err.Error(), "cannot use system prompt") {
		t.Fatalf("invalid RunAsk() error = %v, want config validation context", err)
	}

	configFile := filepath.Join(t.TempDir(), "config-parent-file")
	if err := os.WriteFile(configFile, []byte("file"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.RunAsk(context.Background(), &Config{ConfigDir: configFile, SystemPrompt: "none"}, agentloop.NewExecuteInput("ask"), &strings.Builder{}); err == nil || !strings.Contains(err.Error(), "failed to load config") {
		t.Fatalf("BuildLoop RunAsk() error = %v, want config load context", err)
	}

	dir := t.TempDir()
	writeExecutorConfig(t, dir, validOpenRouterConfig("test-key"))
	if err := os.WriteFile(filepath.Join(dir, "models.yaml"), []byte("models:\n- name: gpt-4o\n  output_modalities: [text]\n  supportedInputMimeTypes: [image/png]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.RunAsk(context.Background(), &Config{ConfigDir: dir, SystemPrompt: "none", OutputModality: "audio"}, agentloop.NewExecuteInput("ask"), &strings.Builder{}); err == nil || !strings.Contains(err.Error(), "does not support audio output") {
		t.Fatalf("output validation RunAsk() error = %v, want modality context", err)
	}
	if _, err := exec.RunAsk(context.Background(), &Config{ConfigDir: dir, SystemPrompt: "none"}, agentloop.ExecuteInput{ContentParts: []messages.ContentPart{messages.ImagePart{MediaType: "image/webp"}}}, &strings.Builder{}); err == nil || !strings.Contains(err.Error(), "does not support input type") {
		t.Fatalf("input validation RunAsk() error = %v, want MIME context", err)
	}
}

func TestExecutorRun_InterruptedMidFlightAndCancelledContext(t *testing.T) {
	started := make(chan struct{})
	inf := &executorScriptedInferencer{steps: []executorInferenceStep{
		{blockOnCtx: true, started: started, partialText: "partial"},
		{result: messages.InferenceResult{Message: messages.NewTextMessage(messages.RoleAssistant, "resumed")}},
	}}
	exec := NewExecutor(nil, nil, inf, true)
	runData, err := exec.BuildLoop(context.Background(), executorTestConfig(t, t.TempDir()))
	if err != nil {
		t.Fatalf("BuildLoop() error = %v", err)
	}
	defer runData.CloseLogger()
	stream, err := exec.ExecuteStreamingTurn(context.Background(), runData, agentloop.NewExecuteInput("initial"), &Config{})
	if err != nil {
		t.Fatalf("ExecuteStreamingTurn() error = %v", err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for injected inferencer to start")
	}
	if err := runData.Loop.SendInterrupt(context.Background(), &messages.Message{Role: messages.RoleUser, ContentParts: []messages.ContentPart{messages.TextPart{Text: "follow-up"}}}); err != nil {
		t.Fatalf("SendInterrupt() error = %v", err)
	}
	var text string
	for stream.HasNext() {
		evt := stream.Response()
		if evt.Type == messages.StreamTypeTextDelta {
			if value, ok := evt.Value.(*messages.TextDeltaValue); ok {
				text += value.Content
			}
		}
	}
	_ = stream.Close()
	if !strings.Contains(text, "resumed") || executorInferenceCallCount(inf) < 2 {
		t.Fatalf("interrupted stream text=%q calls=%d, want resumed response and two calls", text, executorInferenceCallCount(inf))
	}
	history := runData.Loop.GetConversationHistory()
	if !containsMessageText(history, messages.RoleUser, "follow-up") || !containsMessageText(history, messages.RoleAssistant, "resumed") {
		t.Fatalf("interrupted history = %#v, want follow-up and resumed response", history)
	}

	cancelStarted := make(chan struct{})
	cancelInf := &executorScriptedInferencer{steps: []executorInferenceStep{{blockOnCtx: true, started: cancelStarted, partialText: "partial"}}}
	cancelExec := NewExecutor(nil, nil, cancelInf, true)
	cancelData, err := cancelExec.BuildLoop(context.Background(), executorTestConfig(t, t.TempDir()))
	if err != nil {
		t.Fatalf("cancel BuildLoop() error = %v", err)
	}
	cancelCtx, cancel := context.WithCancel(context.Background())
	cancelStream, err := cancelExec.ExecuteStreamingTurn(cancelCtx, cancelData, agentloop.NewExecuteInput("initial"), &Config{})
	if err != nil {
		cancelData.CloseLogger()
		t.Fatalf("cancel ExecuteStreamingTurn() error = %v", err)
	}
	select {
	case <-cancelStarted:
	case <-time.After(5 * time.Second):
		cancel()
		cancelData.CloseLogger()
		t.Fatal("timed out waiting for cancellation inferencer to start")
	}
	cancel()
	for cancelStream.HasNext() {
		cancelStream.Response()
	}
	outcome := cancelStream.Outcome()
	_ = cancelStream.Close()
	cancelData.CloseLogger()
	if outcome.Status != agentloop.StreamCanceled || !errors.Is(outcome.Err, context.Canceled) {
		t.Fatalf("cancel outcome = %+v, want canceled/context.Canceled", outcome)
	}
}

func TestExecutorRun_RunAskWithSessionPersistsHistory(t *testing.T) {
	dir := t.TempDir()
	storage := session.NewStorage(dir)
	if err := storage.Save("chat-session", []messages.Message{messages.NewTextMessage(messages.RoleUser, "prior")}); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	inf := &executorScriptedInferencer{steps: []executorInferenceStep{{result: messages.InferenceResult{Message: messages.NewTextMessage(messages.RoleAssistant, "answer")}}}}
	exec := NewExecutor(nil, nil, inf, true)
	cfg := &Config{ConfigDir: dir, NoSystemInformation: true}
	var out strings.Builder
	got, err := exec.RunAskWithSession(context.Background(), "chat-session", cfg, agentloop.NewExecuteInput("current"), &out)
	if err != nil || got != "answer" || out.String() != "answer\n" {
		t.Fatalf("RunAskWithSession() = (%q, %v), output=%q; want answer", got, err, out.String())
	}
	saved, err := storage.Load("chat-session")
	if err != nil || !containsMessageText(saved, messages.RoleAssistant, "answer") {
		t.Fatalf("saved session = %#v, %v; want assistant answer", saved, err)
	}

	if got, err := exec.RunAskWithSession(context.Background(), "", executorTestConfig(t, t.TempDir()), agentloop.NewExecuteInput("delegated"), &strings.Builder{}); err != nil || got != "answer" {
		t.Fatalf("empty session delegation = (%q, %v), want scripted response", got, err)
	}
}

func TestExecutorRun_RunIterativeLoopRecordsCompletionFailureAndResume(t *testing.T) {
	dir := t.TempDir()
	inf := &executorScriptedInferencer{steps: []executorInferenceStep{
		{result: messages.InferenceResult{Message: messages.NewTextMessage(messages.RoleAssistant, "first")}},
		{result: messages.InferenceResult{Message: messages.NewTextMessage(messages.RoleAssistant, "finished STOP")}},
	}}
	exec := NewExecutor(nil, nil, inf, true)
	cfg := executorTestConfig(t, dir)
	var out strings.Builder
	result, err := exec.RunIterativeLoop(context.Background(), cfg, IterativeLoopConfig{MaxIterations: 2, StopWord: "STOP"}, agentloop.NewExecuteInput("prompt"), &out)
	if err != nil || !result.Completed || len(result.Iterations) != 2 {
		t.Fatalf("iterative completion = %+v, %v; want two iterations/completed", result, err)
	}
	if !strings.Contains(out.String(), "Trace ID:") || !strings.Contains(out.String(), "Iteration 2/2") {
		t.Fatalf("iterative output = %q, want trace and iteration banners", out.String())
	}
	trace, err := session.NewStorage(dir).LoadTrace(result.TraceID)
	if err != nil || trace == nil || trace.Status != session.TraceStatusCompleted || trace.Iterations[1].Status != session.IterationStatusCompleted {
		t.Fatalf("completion trace = %+v, %v; want completed trace", trace, err)
	}

	failureDir := t.TempDir()
	failureInf := &executorScriptedInferencer{steps: []executorInferenceStep{
		{streamErr: errors.New("iteration failed")},
		{result: messages.InferenceResult{Message: messages.NewTextMessage(messages.RoleAssistant, "recovered")}},
	}}
	failureExec := NewExecutor(nil, nil, failureInf, true)
	failureResult, err := failureExec.RunIterativeLoop(context.Background(), executorTestConfig(t, failureDir), IterativeLoopConfig{MaxIterations: 2}, agentloop.NewExecuteInput("prompt"), &strings.Builder{})
	if err != nil || len(failureResult.Iterations) != 2 || failureResult.Iterations[0].Err == nil || failureResult.Iterations[1].Text != "recovered" {
		t.Fatalf("failure iterative result = %+v, %v; want failed then recovered iterations", failureResult, err)
	}

	resumeDir := t.TempDir()
	resumeID := "resume-trace"
	if err := session.NewStorage(resumeDir).SaveTrace(session.TraceRecord{
		TraceID:          resumeID,
		Status:           session.TraceStatusInterrupted,
		Config:           session.TraceConfig{MaxIterations: 2, StopWord: "STOP", Prompt: "saved prompt"},
		CurrentIteration: 1,
		Iterations:       []session.IterationTrace{{Iteration: 1, Status: session.IterationStatusInterrupted}},
	}); err != nil {
		t.Fatalf("seed resume trace: %v", err)
	}
	resumeInf := &executorScriptedInferencer{steps: []executorInferenceStep{{result: messages.InferenceResult{Message: messages.NewTextMessage(messages.RoleAssistant, "resume STOP")}}}}
	resumeExec := NewExecutor(nil, nil, resumeInf, true)
	var resumeOut strings.Builder
	resumed, err := resumeExec.RunIterativeLoop(context.Background(), executorTestConfig(t, resumeDir), IterativeLoopConfig{TraceID: resumeID, MaxIterations: 99, StopWord: "wrong"}, agentloop.NewExecuteInput("ignored"), &resumeOut)
	if err != nil || !resumed.Completed || len(resumed.Iterations) != 1 || resumed.Iterations[0].Iteration != 1 {
		t.Fatalf("resumed result = %+v, %v; want restarted interrupted iteration", resumed, err)
	}
	if !strings.Contains(resumeOut.String(), "Resuming trace resume-trace from iteration 1/2") {
		t.Fatalf("resume output = %q, want resume banner", resumeOut.String())
	}

	badTraceDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(badTraceDir, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(badTraceDir, "sessions", "trace-bad.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.RunIterativeLoop(context.Background(), executorTestConfig(t, badTraceDir), IterativeLoopConfig{TraceID: "bad"}, agentloop.NewExecuteInput("prompt"), &strings.Builder{}); err == nil || !strings.Contains(err.Error(), "load trace bad") {
		t.Fatalf("bad trace error = %v, want load trace context", err)
	}
}

func containsMessageText(history []messages.Message, role messages.Role, text string) bool {
	for _, message := range history {
		if message.Role == role && message.TextContent() == text {
			return true
		}
	}
	return false
}
