package agentruntime

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	cliTools "github.com/portpowered/go-agent-harness/agent-cli/internal/tools"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/sight"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
)

func TestRunAgentLoopSession_ScreenTimeoutDeniedRecheckDeliversOneContinuation(t *testing.T) {
	const callID = "screen-timeout-session-call"
	settings := config.DefaultInteractiveToolConfig()
	settings.FastReadTimeout = 15 * time.Millisecond
	definitions := []messages.ToolDefinition{{Name: cliTools.ScreenToolID}}
	policy, err := NewInteractiveToolPolicyForSession(settings, definitions, nil, false)
	if err != nil {
		t.Fatalf("NewInteractiveToolPolicy: %v", err)
	}

	executor := newTimeoutScreenPermissionExecutor(cliTools.DisplayPermission{State: cliTools.DisplayPermissionGranted})
	out := newSignalingBuffer()
	var diagnostic SessionToolDiagnostic
	var diagnosticCalls int
	inferencer := newScriptedToolCallInferencer(
		out,
		"post-screen-timeout continuation",
		cliTools.ScreenRecordingPermissionDeniedErrorCode,
		scriptedTurn{events: toolCallEvents(callID, cliTools.ScreenToolID, `{"action":"screenshot"}`)},
	)
	go func() {
		<-executor.started
		executor.setPermission(cliTools.DisplayPermission{State: cliTools.DisplayPermissionDenied, Reason: "permission became ineffective during capture"})
	}()

	err = runAgentLoopSession(context.Background(), out, inferencer, sessionLoopOptions{
		audioService:          newTestAudioIOService(),
		MaxDuration:           2 * time.Second,
		WaitForClose:          true,
		ToolExecutor:          executor,
		ToolDefinitions:       definitions,
		InteractiveToolPolicy: policy,
		toolDiagnostics: SessionToolDiagnosticFunc(func(got SessionToolDiagnostic) {
			diagnostic = got
			diagnosticCalls++
		}),
	})
	if err != nil {
		t.Fatalf("runAgentLoopSession: %v\noutput:\n%s", err, out.String())
	}
	if executor.recheckCount() != 1 {
		t.Fatalf("screen permission rechecks = %d, want exactly one", executor.recheckCount())
	}
	if !strings.Contains(out.String(), cliTools.ScreenRecordingPermissionDeniedErrorCode) {
		t.Fatalf("session output = %q, want permission denial result", out.String())
	}
	if !strings.Contains(out.String(), screenSightUnavailable) {
		t.Fatalf("session output = %q, want customer-safe screen failure", out.String())
	}
	for _, forbidden := range []string{
		"System Settings → Privacy & Security → Screen & System Audio Recording",
		"hosting application",
		"Tell the customer",
		"completely quit and restart",
		"macOS Sequoia",
		"monthly re-confirmation",
	} {
		if strings.Contains(out.String(), forbidden) {
			t.Errorf("assistant-visible session output contains operator-only text %q: %s", forbidden, out.String())
		}
	}
	if diagnosticCalls != 1 || diagnostic.ToolCallID != callID || diagnostic.ToolName != cliTools.ScreenToolID || diagnostic.Source != sight.SourceScreen || diagnostic.ErrorCode != cliTools.ScreenRecordingPermissionDeniedErrorCode || diagnostic.Error == nil || !strings.Contains(diagnostic.Error.Error(), "System Settings → Privacy & Security → Screen & System Audio Recording") {
		t.Fatalf("operator diagnostic = %#v, calls=%d, want original typed denial with remediation", diagnostic, diagnosticCalls)
	}
	if !strings.Contains(out.String(), "post-screen-timeout continuation") {
		t.Fatalf("session did not reach one grounded continuation:\n%s", out.String())
	}
}

func TestRunAgentLoopSession_InteractivePolicyTimeoutDeliversOneCorrelatedContinuation(t *testing.T) {
	const (
		callID   = "policy-timeout-call"
		toolName = "policy_slow_read"
	)
	settings := config.DefaultInteractiveToolConfig()
	settings.FastReadTimeout = 15 * time.Millisecond
	definitions := []messages.ToolDefinition{{Name: toolName}}
	policy, err := NewInteractiveToolPolicyForSession(settings, definitions, nil, false)
	if err != nil {
		t.Fatalf("NewInteractiveToolPolicy: %v", err)
	}

	workerExited := make(chan struct{})
	out := newSignalingBuffer()
	inferencer := newScriptedToolCallInferencer(out, "post-policy-timeout continuation", sessionturn.ToolTimeoutClassification,
		scriptedTurn{events: toolCallEvents(callID, toolName, `{}`)},
	)
	executor := sessionToolExecutorFunc(func(ctx context.Context, _ messages.ToolCall) (messages.ToolCallResponse, error) {
		defer close(workerExited)
		<-ctx.Done()
		return messages.ToolCallResponse{}, ctx.Err()
	})

	startedAt := time.Now()
	err = runAgentLoopSession(context.Background(), out, inferencer, sessionLoopOptions{
		audioService:          newTestAudioIOService(),
		MaxDuration:           2 * time.Second,
		WaitForClose:          true,
		ToolExecutor:          executor,
		ToolDefinitions:       definitions,
		InteractiveToolPolicy: policy,
	})
	elapsed := time.Since(startedAt)
	if err != nil {
		t.Fatalf("runAgentLoopSession: %v\noutput:\n%s", err, out.String())
	}
	if elapsed > 750*time.Millisecond {
		t.Fatalf("policy timeout round trip took %s, want fast deadline plus deterministic tolerance", elapsed)
	}
	select {
	case <-workerExited:
	case <-time.After(time.Second):
		t.Fatal("context-cooperative timeout worker did not exit")
	}

	boundaries := sentToolBoundaries(t, inferencer)
	boundaries.requireCounts(t, 1, 1)
	result := toolResultValue(t, boundaries.results[0])
	if result.ToolCallID != callID || result.Name != toolName {
		t.Fatalf("provider tool-result correlation = (%q, %q), want (%q, %q)", result.ToolCallID, result.Name, callID, toolName)
	}
	if !strings.Contains(result.Arguments, "classification="+sessionturn.ToolTimeoutClassification) || !strings.Contains(result.Arguments, "tool execution timed out") {
		t.Fatalf("provider tool-result payload = %q, want stable classification and honest explanation", result.Arguments)
	}
	if boundaries.lastResult < 0 || boundaries.lastContinuation <= boundaries.lastResult {
		t.Fatalf("provider boundary order = result %d, continuation %d; sent=%#v", boundaries.lastResult, boundaries.lastContinuation, boundaries.sent)
	}
	if !strings.Contains(out.String(), "post-policy-timeout continuation") {
		t.Fatalf("session did not produce a non-empty assistant continuation:\n%s", out.String())
	}
}

func TestRunAgentLoopSession_InteractiveTimeoutPreservesParallelSiblingResults(t *testing.T) {
	const (
		slowID   = "parallel-slow-call"
		fastID   = "parallel-fast-call"
		slowName = "policy_slow_read"
		fastName = "policy_fast_read"
	)
	settings := config.DefaultInteractiveToolConfig()
	settings.FastReadTimeout = 15 * time.Millisecond
	definitions := []messages.ToolDefinition{{Name: slowName}, {Name: fastName}}
	policy, err := NewInteractiveToolPolicyForSession(settings, definitions, nil, false)
	if err != nil {
		t.Fatalf("NewInteractiveToolPolicy: %v", err)
	}

	var callsMu sync.Mutex
	var calls []messages.ToolCall
	slowStarted := make(chan struct{})
	slowExited := make(chan struct{})
	out := newSignalingBuffer()
	slowCall := messages.ToolCall{ID: slowID, Name: slowName, Arguments: `{}`}
	fastCall := messages.ToolCall{ID: fastID, Name: fastName, Arguments: `{}`}
	inferencer := newScriptedToolCallInferencer(out, "parallel continuation", "parallel-fast-result",
		scriptedTurn{events: parallelToolCallEvents(slowCall, fastCall)},
	)
	executor := sessionToolExecutorFunc(func(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
		callsMu.Lock()
		calls = append(calls, call)
		callsMu.Unlock()
		if call.ID == slowID {
			close(slowStarted)
			defer close(slowExited)
			<-ctx.Done()
			return messages.ToolCallResponse{}, ctx.Err()
		}
		return messages.ToolCallResponse{Content: "parallel-fast-result"}, nil
	})

	err = runAgentLoopSession(context.Background(), out, inferencer, sessionLoopOptions{
		audioService:          newTestAudioIOService(),
		MaxDuration:           2 * time.Second,
		WaitForClose:          true,
		ToolExecutor:          executor,
		ToolDefinitions:       definitions,
		InteractiveToolPolicy: policy,
	})
	if err != nil {
		t.Fatalf("runAgentLoopSession: %v\noutput:\n%s", err, out.String())
	}
	select {
	case <-slowStarted:
	case <-time.After(time.Second):
		t.Fatal("slow sibling did not start")
	}
	select {
	case <-slowExited:
	case <-time.After(time.Second):
		t.Fatal("slow sibling did not observe its independent timeout")
	}
	callsMu.Lock()
	gotCalls := append([]messages.ToolCall(nil), calls...)
	callsMu.Unlock()
	requireExactToolCalls(t, gotCalls, slowCall, fastCall)

	boundaries := sentToolBoundaries(t, inferencer)
	boundaries.requireCounts(t, 2, 1)
	for index, want := range []struct {
		callID  string
		name    string
		content string
	}{
		{callID: slowID, name: slowName, content: "classification=" + sessionturn.ToolTimeoutClassification},
		{callID: fastID, name: fastName, content: "parallel-fast-result"},
	} {
		result := toolResultValue(t, boundaries.results[index])
		if result.ToolCallID != want.callID || result.Name != want.name {
			t.Fatalf("provider result %d correlation = (%q, %q), want (%q, %q)", index, result.ToolCallID, result.Name, want.callID, want.name)
		}
		if !strings.Contains(result.Arguments, want.content) {
			t.Fatalf("provider result %d payload = %q, want %q", index, result.Arguments, want.content)
		}
		if index == 0 && strings.Contains(result.Arguments, "parallel-fast-result") {
			t.Fatalf("slow timeout result was contaminated by fast sibling payload: %q", result.Arguments)
		}
	}
	if !strings.Contains(out.String(), "parallel-fast-result") || !strings.Contains(out.String(), "parallel continuation") {
		t.Fatalf("parallel session lost sibling success or continuation:\n%s", out.String())
	}
}

func TestRunAgentLoopSession_ExecutesScriptedCallsInOrderAndKeepsSessionUsable(t *testing.T) {
	const (
		firstID    = "s14-call-1"
		secondID   = "s14-call-2"
		toolName   = "distinctive_session_tool"
		firstArgs  = "{\"city\":\"S\\u00e3o Paulo\",\"units\":\"metric\"}"
		secondArgs = "{\"city\":\"Osaka\"}"
	)

	out := newSignalingBuffer()
	inferencer := newScriptedToolCallInferencer(out,
		"follow-up turn after both tool results",
		resultPayloadFor(1),
		scriptedTurn{events: toolCallEvents(firstID, toolName, firstArgs)},
		scriptedTurn{events: toolCallEvents(secondID, toolName, secondArgs), after: resultPayloadFor(0)},
	)
	executor := &recordingSessionExecutor{}

	err := runAgentLoopSession(context.Background(), out, inferencer, sessionLoopOptions{
		audioService: newTestAudioIOService(),
		MaxDuration:  2 * time.Second,
		WaitForClose: true,
		ToolExecutor: executor,
	})
	if err != nil {
		t.Fatalf("runAgentLoopSession: %v\noutput:\n%s", err, out.String())
	}

	calls := executor.recorded()
	if len(calls) != 2 {
		t.Fatalf("executor invocations = %d, want exactly 2 (calls=%#v)\noutput:\n%s", len(calls), calls, out.String())
	}
	want := []messages.ToolCall{
		{ID: firstID, Name: toolName, Arguments: firstArgs},
		{ID: secondID, Name: toolName, Arguments: secondArgs},
	}
	for i, w := range want {
		if calls[i] != w {
			t.Fatalf("invocation %d = %#v, want %#v (raw values must be preserved in order)", i, calls[i], w)
		}
	}

	output := out.String()
	if !strings.Contains(output, resultPayloadFor(0)) || !strings.Contains(output, resultPayloadFor(1)) {
		t.Fatalf("output missing correlated tool results:\n%s", output)
	}
	if !strings.Contains(output, "follow-up turn after both tool results") {
		t.Fatalf("session did not continue to a later model turn:\n%s", output)
	}
	if strings.Contains(output, "default tool executor") {
		t.Fatalf("loop reached the default executor instead of the composed one:\n%s", output)
	}
	// In duplex mode executed results feed the loop's conversation history
	// (ordering.UpdateWorldHistory) rather than being re-sent over session.Send;
	// the emitted RoleTool deltas plus the follow-up model turn prove the round
	// trip through the harness.
}

// TestRunAgentLoopSession_FailureTableKeepsSessionAlive is the S4-style table:
// every failure mode crosses the in-memory session seam exactly like a real
// provider call and must produce one non-empty correlated failure without
// terminating the session.
func TestRunAgentLoopSession_FailureTableKeepsSessionAlive(t *testing.T) {
	const (
		callID     = "s4-call-9"
		toolName   = "broken_session_tool"
		args       = `{not-json}`
		successTag = "SUCCESS-PAYLOAD-MUST-NOT-APPEAR"
	)

	cases := []struct {
		name     string
		timeout  time.Duration
		executor sessionToolExecutorFunc
		contains string
	}{
		{
			name: "unknown tool",
			executor: func(context.Context, messages.ToolCall) (messages.ToolCallResponse, error) {
				return messages.ToolCallResponse{}, errors.New(`tool "missing_tool" not found`)
			},
			contains: "not found",
		},
		{
			name: "malformed arguments",
			executor: func(_ context.Context, got messages.ToolCall) (messages.ToolCallResponse, error) {
				if got.Arguments != args {
					return messages.ToolCallResponse{}, errors.New("arguments were changed")
				}
				return messages.ToolCallResponse{}, errors.New("failed to parse tool arguments")
			},
			contains: "parse tool arguments",
		},
		{
			name: "executor panic",
			executor: func(context.Context, messages.ToolCall) (messages.ToolCallResponse, error) {
				panic("panic detail must stay inside this invocation")
			},
			contains: "panicked",
		},
		{
			name:    "executor timeout",
			timeout: 10 * time.Millisecond,
			executor: func(ctx context.Context, _ messages.ToolCall) (messages.ToolCallResponse, error) {
				<-ctx.Done()
				return messages.ToolCallResponse{}, ctx.Err()
			},
			contains: "timed out",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			followUp := "recovered after " + tc.name
			out := newSignalingBuffer()
			inferencer := newScriptedToolCallInferencer(out, followUp, tc.contains,
				scriptedTurn{events: toolCallEvents(callID, toolName, args)},
			)

			attempts := 0
			var mu sync.Mutex
			executor := sessionToolExecutorFunc(func(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
				mu.Lock()
				attempts++
				current := attempts
				mu.Unlock()
				if current > 1 {
					return messages.ToolCallResponse{Content: successTag}, nil
				}
				return tc.executor(ctx, call)
			})

			err := runAgentLoopSession(context.Background(), out, inferencer, sessionLoopOptions{
				audioService:         newTestAudioIOService(),
				MaxDuration:          2 * time.Second,
				WaitForClose:         true,
				ToolExecutor:         executor,
				ToolExecutionTimeout: tc.timeout,
			})
			if err != nil {
				t.Fatalf("%s: failing tool must not terminate the session, got: %v\noutput:\n%s", tc.name, err, out.String())
			}

			mu.Lock()
			defer mu.Unlock()
			if attempts != 1 {
				t.Fatalf("%s: executor attempts = %d, want exactly 1\noutput:\n%s", tc.name, attempts, out.String())
			}
			assertRecoveredToolFailure(t, out.String(), []string{tc.contains, `"` + toolName + `" failed`, followUp}, successTag)
		})
	}
}

// TestRunAgentLoopSession_TimeoutWorkerExitsBoundedly drives the cooperative
// timeout contract through the session seam: the adapter returns on its
// deadline, the honoring worker exits promptly afterwards, and the session
// keeps processing later events.
func TestRunAgentLoopSession_TimeoutWorkerExitsBoundedly(t *testing.T) {
	workerExited := make(chan struct{})
	out := newSignalingBuffer()
	inferencer := newScriptedToolCallInferencer(out, "post-timeout continuation", "timed out",
		scriptedTurn{events: toolCallEvents("t-call-1", "slow_session_tool", "{}")},
	)

	executor := sessionToolExecutorFunc(func(ctx context.Context, _ messages.ToolCall) (messages.ToolCallResponse, error) {
		defer close(workerExited)
		<-ctx.Done()
		return messages.ToolCallResponse{}, ctx.Err()
	})

	err := runAgentLoopSession(context.Background(), out, inferencer, sessionLoopOptions{
		audioService:         newTestAudioIOService(),
		MaxDuration:          2 * time.Second,
		WaitForClose:         true,
		ToolExecutor:         executor,
		ToolExecutionTimeout: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("runAgentLoopSession: %v\noutput:\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "timed out") {
		t.Fatalf("output missing timeout failure:\n%s", out.String())
	}

	select {
	case <-workerExited:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout worker did not exit after the adapter returned")
	}
	if !strings.Contains(out.String(), "post-timeout continuation") {
		t.Fatalf("session did not continue after the timed-out tool:\n%s", out.String())
	}
}

// TestPlanSessionRuntimeThreadsToolExecutorAndDeadlineOverride proves the
// exported SessionRunOptions seam crosses both the composed executor and the
// per-invocation deadline override into the duplex loop options, and that a
// zero override keeps the production default path.
func TestPlanSessionRuntimeThreadsToolExecutorAndDeadlineOverride(t *testing.T) {
	executor := sessionToolExecutorFunc(func(context.Context, messages.ToolCall) (messages.ToolCallResponse, error) {
		return messages.ToolCallResponse{}, nil
	})

	plan, err := planSessionRuntime(SessionRunOptions{AudioService: newTestAudioIOService(), ModelCatalog: testModelCatalog(),
		ReplayPath:           "unused.json",
		SessionInferencer:    stubPlanSessionInferencer{},
		ToolExecutor:         executor,
		ToolExecutionTimeout: 7 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("planSessionRuntime: %v", err)
	}
	if plan.loop.ToolExecutor == nil {
		t.Fatal("plan dropped the composed tool executor")
	}
	if plan.loop.ToolExecutionTimeout != 7*time.Millisecond {
		t.Fatalf("plan.loop.ToolExecutionTimeout = %s, want 7ms", plan.loop.ToolExecutionTimeout)
	}

	defaultPlan, err := planSessionRuntime(SessionRunOptions{AudioService: newTestAudioIOService(), ModelCatalog: testModelCatalog(),
		ReplayPath:        "unused.json",
		SessionInferencer: stubPlanSessionInferencer{},
		ToolExecutor:      executor,
	})
	if err != nil {
		t.Fatalf("planSessionRuntime without override: %v", err)
	}
	if defaultPlan.loop.ToolExecutor == nil {
		t.Fatal("plan dropped the composed tool executor without an override")
	}
	if defaultPlan.loop.ToolExecutionTimeout != 0 {
		t.Fatalf("plan.loop.ToolExecutionTimeout = %s without override, want 0 (production default path)", defaultPlan.loop.ToolExecutionTimeout)
	}
}
