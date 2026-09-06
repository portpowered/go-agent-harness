package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	session "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/internal/persistence"
)

const iterativeInitialPrompt = "initial"

func executorTestConfig() *Config {
	return &Config{NoSystemInformation: true}
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

func containsMessageText(history []messages.Message, role messages.Role, text string) bool {
	for _, message := range history {
		if message.Role == role && message.TextContent() == text {
			return true
		}
	}
	return false
}

func TestExecutorRun_ContinuationAndResolvedPolicy(t *testing.T) {
	for _, penalty := range []float64{0, 1} {
		if got := buildInferenceDefaultsForPenalty(penalty); got != nil {
			t.Fatalf("penalty %v defaults = %+v, want nil", penalty, got)
		}
	}
	if got := BuildIterationAnnotation(2, 5, ""); got != "You are on iteration 2 of 5." {
		t.Fatalf("annotation without stop word = %q", got)
	}
	if got := BuildIterationAnnotation(2, 5, "DONE"); !strings.Contains(got, "end your response with DONE") {
		t.Fatalf("annotation with stop word = %q", got)
	}

	inf := &executorScriptedInferencer{steps: []executorInferenceStep{
		{result: messages.InferenceResult{Message: messages.NewTextMessage(messages.RoleAssistant, "first")}},
		{result: messages.InferenceResult{Message: messages.NewTextMessage(messages.RoleAssistant, "second")}},
	}}
	exec := resolvedExecutorForTest(t, inf, nil, nil, nil)
	data := newExecutorRunData(t, inf, nil, nil)
	data.Loop.EnqueueTodo("queued follow-up")
	var out strings.Builder
	got, err := exec.executeWithContinuation(context.Background(), data, agentloop.NewExecuteInput(iterativeInitialPrompt), &Config{MaxContinuationDepth: 1}, &out, "")
	if err != nil || got != "second" || executorInferenceCallCount(inf) != 2 {
		t.Fatalf("continuation result = (%q, %v), calls=%d; want second/2", got, err, executorInferenceCallCount(inf))
	}
	requests := executorInferenceRequests(inf)
	if len(requests) != 2 || requests[1].Messages[len(requests[1].Messages)-1].TextContent() != "queued follow-up" {
		t.Fatalf("continuation requests = %#v, want queued follow-up as second input", requests)
	}

	nudgeInf := &executorScriptedInferencer{steps: []executorInferenceStep{
		{result: messages.InferenceResult{Message: messages.NewTextMessage(messages.RoleAssistant, "first nudge")}},
		{result: messages.InferenceResult{Message: messages.NewTextMessage(messages.RoleAssistant, "second nudge")}},
	}}
	nudgeStorage := nudgeExecStorage(t)
	nudgeExec := resolvedExecutorForTest(t, nudgeInf, nudgeStorage, nil, nil).WithResolution(RuntimeResolution{
		Resolved:       true,
		Provider:       ProviderConfig{Provider: "test", Model: "test-model"},
		ModelPolicy:    ModelPolicy{ContinuationNudgeEnabled: true, ContinuationNudgeMessage: "keep-going"},
		Storage:        nudgeStorage,
		WorkspaceDir:   nudgeStorage.WorkspaceDir(),
		PromptResolved: true,
	})
	// Use a fresh run-data loop for this direct continuation test. Its policy is
	// host-resolved above; no model/config file is consulted.
	nudgeData := newExecutorRunData(t, nudgeInf, nil, nil)
	got, err = nudgeExec.executeWithContinuation(context.Background(), nudgeData, agentloop.NewExecuteInput(iterativeInitialPrompt), &Config{MaxContinuationDepth: 1}, &out, "")
	if err != nil || got != "second nudge" {
		t.Fatalf("nudge continuation = (%q, %v), want second nudge response", got, err)
	}
	nudgeRequests := executorInferenceRequests(nudgeInf)
	if len(nudgeRequests) != 2 || nudgeRequests[1].Messages[len(nudgeRequests[1].Messages)-1].TextContent() != "keep-going" {
		t.Fatalf("nudge requests = %#v, want resolved nudge as second input", nudgeRequests)
	}

	stopInf := &executorScriptedInferencer{steps: []executorInferenceStep{
		{result: messages.InferenceResult{Message: messages.NewTextMessage(messages.RoleAssistant, "finished DONE")}},
		{result: messages.InferenceResult{Message: messages.NewTextMessage(messages.RoleAssistant, "must not run")}},
	}}
	stopData := newExecutorRunData(t, stopInf, nil, nil)
	stopData.Loop.EnqueueTodo("should remain queued")
	if got, err := exec.executeWithContinuation(context.Background(), stopData, agentloop.NewExecuteInput(iterativeInitialPrompt), &Config{MaxContinuationDepth: 2}, &out, "DONE"); err != nil || got != "finished DONE" || executorInferenceCallCount(stopInf) != 1 {
		t.Fatalf("stop-word result = (%q, %v), calls=%d; want one call", got, err, executorInferenceCallCount(stopInf))
	}

	errorInf := &executorScriptedInferencer{steps: []executorInferenceStep{{streamErr: errors.New("inference failed")}}}
	errorData := newExecutorRunData(t, errorInf, nil, nil)
	if _, err := exec.executeWithContinuation(context.Background(), errorData, agentloop.NewExecuteInput(iterativeInitialPrompt), &Config{}, &out, ""); err == nil || !strings.Contains(err.Error(), "inference failed") {
		t.Fatalf("continuation error = %v, want inference context", err)
	}
}

func nudgeExecStorage(t *testing.T) Storage {
	t.Helper()
	return session.NewStorage(t.TempDir())
}

func TestExecutorRun_BuildLoopUsesResolvedDependencies(t *testing.T) {
	dir := t.TempDir()
	storage := session.NewStorage(dir)
	inf := &executorScriptedInferencer{steps: []executorInferenceStep{{result: messages.InferenceResult{Message: messages.NewTextMessage(messages.RoleAssistant, "built")}}}}
	tool := &executorRecordingTool{}
	defs := []messages.ToolDefinition{{Name: "lookup", Description: "lookup value"}}
	exec := resolvedExecutorForTest(t, inf, storage, tool, defs).WithResolution(RuntimeResolution{
		Resolved:       true,
		Provider:       ProviderConfig{Provider: "test", Model: "test-model"},
		ModelCatalog:   ModelCatalog{Models: []ModelInfo{{Name: "test-model", OutputModalities: []string{"text"}}}},
		ModelPolicy:    ModelPolicy{RepetitionPenalty: 1.4},
		Storage:        storage,
		WorkspaceDir:   dir,
		PromptResolved: true,
	})
	history := messages.NewTextMessage(messages.RoleUser, "history")
	runData, err := exec.BuildLoop(context.Background(), &Config{
		SystemPrompt:        "",
		NoSystemInformation: true,
		SessionID:           "explicit-session",
		InitialHistory:      []messages.Message{history},
	})
	if err != nil {
		t.Fatalf("BuildLoop() error = %v", err)
	}
	if runData.SessionID != "explicit-session" || runData.WorkspaceDir() != dir {
		t.Fatalf("RunData = session=%q workspace=%q, want explicit session and workspace", runData.SessionID, runData.WorkspaceDir())
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
	if !containsMessageText(runData.Loop.GetConversationHistory(), messages.RoleUser, "history") {
		t.Fatalf("conversation history = %#v, want initial history", runData.Loop.GetConversationHistory())
	}

	if data, err := NewExecutor(nil, nil, stubInferencer{}, true).BuildLoop(context.Background(), &Config{}); err == nil || data != nil {
		t.Fatalf("unresolved BuildLoop() = (%v, %v), want admission error and nil data", data, err)
	}
}

func TestExecutorRun_RunAskWithSessionPersistsHistory(t *testing.T) {
	dir := t.TempDir()
	storage := session.NewStorage(dir)
	if err := storage.Save("chat-session", []messages.Message{messages.NewTextMessage(messages.RoleUser, "prior")}); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	inf := &executorScriptedInferencer{steps: []executorInferenceStep{{result: messages.InferenceResult{Message: messages.NewTextMessage(messages.RoleAssistant, "answer")}}}}
	exec := resolvedExecutorForTest(t, inf, storage, nil, nil)
	var out strings.Builder
	got, err := exec.RunAskWithSession(context.Background(), "chat-session", &Config{NoSystemInformation: true}, agentloop.NewExecuteInput("current"), &out)
	if err != nil || got != "answer" || out.String() != "answer\n" {
		t.Fatalf("RunAskWithSession() = (%q, %v), output=%q; want answer", got, err, out.String())
	}
	saved, err := storage.Load("chat-session")
	if err != nil || !containsMessageText(saved, messages.RoleAssistant, "answer") {
		t.Fatalf("saved session = %#v, %v; want assistant answer", saved, err)
	}
}

func TestExecutorRun_RunIterativeLoopRecordsCompletionFailureAndResume(t *testing.T) {
	dir := t.TempDir()
	storage := session.NewStorage(dir)
	inf := &executorScriptedInferencer{steps: []executorInferenceStep{
		{result: messages.InferenceResult{Message: messages.NewTextMessage(messages.RoleAssistant, "first")}},
		{result: messages.InferenceResult{Message: messages.NewTextMessage(messages.RoleAssistant, "finished STOP")}},
	}}
	exec := resolvedExecutorForTest(t, inf, storage, nil, nil)
	var out strings.Builder
	result, err := exec.RunIterativeLoop(context.Background(), executorTestConfig(), IterativeLoopConfig{MaxIterations: 2, StopWord: "STOP"}, agentloop.NewExecuteInput("prompt"), &out)
	if err != nil || !result.Completed || len(result.Iterations) != 2 {
		t.Fatalf("iterative completion = %+v, %v; want two iterations/completed", result, err)
	}
	if !strings.Contains(out.String(), "Trace ID:") || !strings.Contains(out.String(), "Iteration 2/2") {
		t.Fatalf("iterative output = %q, want trace and iteration banners", out.String())
	}
	requests := executorInferenceRequests(inf)
	for i, request := range requests {
		annotation := BuildIterationAnnotation(i+1, 2, "STOP")
		if !containsMessageText(request.Messages, messages.RoleSystem, annotation) {
			t.Fatalf("iteration %d did not receive its annotation: %+v", i+1, request.Messages)
		}
	}
	trace, err := storage.LoadTrace(result.TraceID)
	if err != nil || trace == nil || trace.Status != session.TraceStatusCompleted || trace.Iterations[1].Status != session.IterationStatusCompleted {
		t.Fatalf("completion trace = %+v, %v; want completed trace", trace, err)
	}

}

func TestExecutorRun_IterativeFailureContinues(t *testing.T) {
	failureDir := t.TempDir()
	failureStorage := session.NewStorage(failureDir)
	failureInf := &executorScriptedInferencer{steps: []executorInferenceStep{
		{streamErr: errors.New("iteration failed")},
		{result: messages.InferenceResult{Message: messages.NewTextMessage(messages.RoleAssistant, "recovered")}},
	}}
	failureExec := resolvedExecutorForTest(t, failureInf, failureStorage, nil, nil)
	failureResult, err := failureExec.RunIterativeLoop(context.Background(), executorTestConfig(), IterativeLoopConfig{MaxIterations: 2}, agentloop.NewExecuteInput("prompt"), &strings.Builder{})
	if err != nil || len(failureResult.Iterations) != 2 || failureResult.Iterations[0].Err == nil || failureResult.Iterations[1].Text != "recovered" {
		t.Fatalf("failure iterative result = %+v, %v; want failed then recovered iterations", failureResult, err)
	}

}

func TestExecutorRun_IterativeResumeRestoresSavedConfiguration(t *testing.T) {
	resumeDir := t.TempDir()
	resumeStorage := session.NewStorage(resumeDir)
	resumeID := "resume-trace"
	if err := resumeStorage.SaveTrace(session.TraceRecord{
		TraceID:          resumeID,
		Status:           session.TraceStatusInterrupted,
		Config:           session.TraceConfig{MaxIterations: 2, StopWord: "STOP", Prompt: "saved prompt"},
		CurrentIteration: 1,
		Iterations:       []session.IterationTrace{{Iteration: 1, Status: session.IterationStatusInterrupted}},
	}); err != nil {
		t.Fatalf("seed resume trace: %v", err)
	}
	resumeInf := &executorScriptedInferencer{steps: []executorInferenceStep{{result: messages.InferenceResult{Message: messages.NewTextMessage(messages.RoleAssistant, "resume STOP")}}}}
	resumeExec := resolvedExecutorForTest(t, resumeInf, resumeStorage, nil, nil)
	var resumeOut strings.Builder
	resumed, err := resumeExec.RunIterativeLoop(context.Background(), executorTestConfig(), IterativeLoopConfig{TraceID: resumeID, MaxIterations: 99, StopWord: "wrong"}, agentloop.NewExecuteInput("ignored"), &resumeOut)
	if err != nil || !resumed.Completed || len(resumed.Iterations) != 1 || resumed.Iterations[0].Iteration != 1 {
		t.Fatalf("resumed result = %+v, %v; want restarted interrupted iteration", resumed, err)
	}
	if !strings.Contains(resumeOut.String(), "Resuming trace resume-trace from iteration 1/2") {
		t.Fatalf("resume output = %q, want resume banner", resumeOut.String())
	}

}

func TestExecutorRun_IterativeCorruptTrace(t *testing.T) {
	badTraceDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(badTraceDir, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(badTraceDir, "sessions", "trace-bad.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	badExec := resolvedExecutorForTest(t, stubInferencer{}, session.NewStorage(badTraceDir), nil, nil)
	if _, err := badExec.RunIterativeLoop(context.Background(), executorTestConfig(), IterativeLoopConfig{TraceID: "bad"}, agentloop.NewExecuteInput("prompt"), &strings.Builder{}); err == nil || !strings.Contains(err.Error(), "load trace bad") {
		t.Fatalf("bad trace error = %v, want load trace context", err)
	}
}

func TestExecutorRun_IterativeInteractionOwnsSteeringAndTracePrompt(t *testing.T) {
	storage := session.NewStorage(t.TempDir())
	inf := &executorScriptedInferencer{steps: []executorInferenceStep{
		{result: messages.InferenceResult{Message: messages.NewTextMessage(messages.RoleAssistant, "first")}},
		{result: messages.InferenceResult{Message: messages.NewTextMessage(messages.RoleAssistant, "second")}},
	}}
	exec := resolvedExecutorForTest(t, inf, storage, nil, nil)
	var ready IterativeTrace
	var seen []IterationRunResult
	interaction := &IterativeInteraction{
		InitialPrompt: func(context.Context) (string, bool, error) { return iterativeInitialPrompt, false, nil },
		TraceReady: func(_ context.Context, trace IterativeTrace) error {
			ready = trace
			return nil
		},
		OnIteration: func(_ context.Context, iteration IterationRunResult) (IterativeDecision, error) {
			seen = append(seen, iteration)
			if iteration.Iteration == 1 {
				return IterativeDecision{Action: IterativeContinue, Prompt: "steered"}, nil
			}
			return IterativeDecision{Action: IterativeStop}, nil
		},
	}
	result, err := exec.RunIterativeLoopWithInteraction(context.Background(), executorTestConfig(), IterativeLoopConfig{MaxIterations: 3}, agentloop.ExecuteInput{}, &strings.Builder{}, interaction)
	if err != nil || !result.Completed || len(result.Iterations) != 2 {
		t.Fatalf("interactive result = %+v, %v; want two iterations and completion", result, err)
	}
	if ready.TraceID == "" || ready.StartIteration != 1 || ready.MaxIterations != 3 || ready.Resumed {
		t.Fatalf("trace ready = %+v, want new three-iteration trace", ready)
	}
	if len(seen) != 2 || seen[0].Text != "first" || seen[1].Text != "second" {
		t.Fatalf("interaction iterations = %+v, want ordered responses", seen)
	}
	requests := executorInferenceRequests(inf)
	if len(requests) != 2 || !containsMessageText(requests[0].Messages, messages.RoleUser, iterativeInitialPrompt) || !containsMessageText(requests[1].Messages, messages.RoleUser, "steered") {
		t.Fatalf("interactive requests = %#v, want initial then steered prompts", requests)
	}
	trace, err := storage.LoadTrace(result.TraceID)
	if err != nil || trace == nil || trace.Status != session.TraceStatusCompleted || trace.Config.Prompt != "steered" {
		t.Fatalf("interactive trace = %+v, %v; want completed trace with latest prompt", trace, err)
	}
}

func TestExecutorRun_IterativeInteractionDoneSkipsTraceCreation(t *testing.T) {
	storage := session.NewStorage(t.TempDir())
	exec := resolvedExecutorForTest(t, stubInferencer{}, storage, nil, nil)
	interaction := &IterativeInteraction{
		InitialPrompt: func(context.Context) (string, bool, error) { return "", true, nil },
	}
	result, err := exec.RunIterativeLoopWithInteraction(context.Background(), executorTestConfig(), IterativeLoopConfig{MaxIterations: 1}, agentloop.ExecuteInput{}, &strings.Builder{}, interaction)
	if err != nil || result.TraceID != "" || len(result.Iterations) != 0 {
		t.Fatalf("done interaction = %+v, %v; want clean empty result", result, err)
	}
}
