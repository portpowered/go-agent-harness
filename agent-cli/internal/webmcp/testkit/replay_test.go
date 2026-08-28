package testkit

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestBrowserReplayExecuteConsumesOperationsAndEvents(t *testing.T) {
	script, err := LoadBrowserScript([]byte(validBrowserScriptJSON))
	if err != nil {
		t.Fatalf("LoadBrowserScript: %v", err)
	}
	replay, err := NewBrowserReplay(script, WithReplayClock(NewFakeClock(100)), WithReplayIDSource(NewDeterministicIDSource("run")))
	if err != nil {
		t.Fatalf("NewBrowserReplay: %v", err)
	}

	if _, err := replay.Execute(context.Background(), OperationRequest{Type: OperationEnableLifecycle}); err != nil {
		t.Fatalf("enable lifecycle: %v", err)
	}
	if _, err := replay.Execute(context.Background(), OperationRequest{Type: OperationEnableWebMCP}); err != nil {
		t.Fatalf("enable webmcp: %v", err)
	}
	execution, err := replay.Execute(context.Background(), OperationRequest{
		Type:     OperationInvokeTool,
		FrameID:  "frame-1",
		ToolName: "read_state",
		Input:    MustJSONValue(map[string]any{"count": 9007199254740993}),
	})
	if err != nil {
		t.Fatalf("invoke tool: %v", err)
	}
	if string(execution.Result) != `{"invocation_id":"inv-1"}` || len(execution.Events) != 1 {
		t.Fatalf("execution = %+v", execution)
	}
	if got := replay.Outcome(); !got.OK() || len(got.Pending) != 0 {
		t.Fatalf("outcome = %+v, want completed with no pending invocations", got)
	}
	if got := len(replay.Observations()); got != 2 {
		t.Fatalf("accepted event count = %d, want 2", got)
	}
	select {
	case <-replay.Done():
	default:
		t.Fatal("completed replay Done channel is open")
	}
	if err := replay.Complete(); err != nil {
		t.Fatalf("Complete after automatic completion: %v", err)
	}
	if _, err := replay.Execute(context.Background(), OperationRequest{Type: OperationEnableLifecycle}); !errors.Is(err, ErrReplayClosed) {
		t.Fatalf("operation after completion = %v, want ErrReplayClosed", err)
	}
}

func TestBrowserReplayObserveEventReportsSafeDivergenceAndOrdering(t *testing.T) {
	script, err := LoadBrowserScript([]byte(validBrowserScriptJSON))
	if err != nil {
		t.Fatalf("LoadBrowserScript: %v", err)
	}
	replay, err := NewBrowserReplay(script)
	if err != nil {
		t.Fatalf("NewBrowserReplay: %v", err)
	}
	if _, err := replay.ObserveOperation(context.Background(), OperationRequest{Type: OperationEnableLifecycle}); err != nil {
		t.Fatalf("lifecycle operation: %v", err)
	}
	if _, err := replay.ObserveOperation(context.Background(), OperationRequest{Type: OperationEnableWebMCP}); err != nil {
		t.Fatalf("ObserveOperation: %v", err)
	}
	if _, err := replay.ObserveOperation(context.Background(), OperationRequest{Type: OperationEnableLifecycle}); !errors.Is(err, ErrReplayMismatch) {
		t.Fatalf("operation before expected event = %v, want replay mismatch", err)
	}
	if got := replay.Outcome().Status; got != ReplayDiverged {
		t.Fatalf("ordering outcome = %q, want diverged", got)
	}

	replay, err = NewBrowserReplay(script)
	if err != nil {
		t.Fatalf("NewBrowserReplay(second): %v", err)
	}
	if _, err := replay.ObserveOperation(context.Background(), OperationRequest{Type: OperationEnableLifecycle}); err != nil {
		t.Fatalf("lifecycle operation(second): %v", err)
	}
	if _, err := replay.ObserveOperation(context.Background(), OperationRequest{Type: OperationEnableWebMCP}); err != nil {
		t.Fatalf("ObserveOperation(second): %v", err)
	}
	secret := "sentinel-event-secret"
	err = replay.ObserveEvent(context.Background(), EmittedEvent{
		Type:         EmittedToolResponded,
		InvocationID: "inv-1",
		Status:       "Error",
		Error:        MustJSONValue(map[string]any{"code": "safe"}),
	})
	if !errors.Is(err, ErrReplayMismatch) {
		t.Fatalf("wrong event = %v, want replay mismatch", err)
	}
	var mismatch *ReplayMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("wrong event error = %T, want *ReplayMismatchError", err)
	}
	if mismatch.Path != "type" || mismatch.Position != 2 || mismatch.Expected != "tools_added" || mismatch.Actual != "event tool_responded" {
		t.Fatalf("mismatch context = %+v", mismatch)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("mismatch leaked event value: %v", err)
	}
}

func TestBrowserReplayReportsInputAndTerminalDivergenceWithoutValues(t *testing.T) {
	script, err := LoadBrowserScript([]byte(validBrowserScriptJSON))
	if err != nil {
		t.Fatalf("LoadBrowserScript: %v", err)
	}
	replay, err := NewBrowserReplay(script)
	if err != nil {
		t.Fatalf("NewBrowserReplay: %v", err)
	}
	if _, err := replay.Execute(context.Background(), OperationRequest{Type: OperationEnableLifecycle}); err != nil {
		t.Fatalf("lifecycle: %v", err)
	}
	if _, err := replay.Execute(context.Background(), OperationRequest{Type: OperationEnableWebMCP}); err != nil {
		t.Fatalf("webmcp: %v", err)
	}
	secret := "sentinel-input-secret"
	_, err = replay.Execute(context.Background(), OperationRequest{
		Type:     OperationInvokeTool,
		FrameID:  "frame-1",
		ToolName: "read_state",
		Input:    MustJSONValue(map[string]any{"count": secret}),
	})
	if !errors.Is(err, ErrReplayMismatch) {
		t.Fatalf("wrong input = %v, want replay mismatch", err)
	}
	var mismatch *ReplayMismatchError
	if !errors.As(err, &mismatch) || mismatch.Path != "input.count" {
		t.Fatalf("input mismatch = %+v", mismatch)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("input mismatch leaked value: %v", err)
	}

	minimal := replayTestScript(BrowserScriptOperation{
		Expect: OperationExpectation{Type: OperationInvokeTool, FrameID: "frame-1", ToolName: "write_state", Input: MustJSONValue(map[string]any{})},
		Result: MustJSONValue(map[string]any{"invocation_id": "inv-terminal"}),
		Emit: []EmittedEvent{{
			Type:         EmittedToolResponded,
			InvocationID: "inv-terminal",
			Status:       "Completed",
			Output:       MustJSONValue(map[string]any{"value": 1}),
		}},
	})
	replay, err = NewBrowserReplay(minimal)
	if err != nil {
		t.Fatalf("NewBrowserReplay(minimal): %v", err)
	}
	if _, err := replay.ObserveOperation(context.Background(), minimal.Operations[0].ExpectRequest()); err != nil {
		t.Fatalf("minimal operation: %v", err)
	}
	err = replay.ObserveEvent(context.Background(), EmittedEvent{
		Type:         EmittedToolResponded,
		InvocationID: "inv-terminal",
		Status:       "Completed",
		Output:       MustJSONValue(map[string]any{"value": 2}),
	})
	if !errors.Is(err, ErrReplayMismatch) {
		t.Fatalf("unexpected terminal event = %v, want replay mismatch", err)
	}
	var terminalMismatch *ReplayMismatchError
	if !errors.As(err, &terminalMismatch) || terminalMismatch.Path != "output.value" {
		t.Fatalf("terminal mismatch = %+v", terminalMismatch)
	}
}

func TestBrowserReplayIncompleteAndPendingErrorsAreDistinct(t *testing.T) {
	script := replayTestScript(BrowserScriptOperation{Expect: OperationExpectation{Type: OperationEnableLifecycle}})
	replay, err := NewBrowserReplay(script)
	if err != nil {
		t.Fatalf("NewBrowserReplay: %v", err)
	}
	err = replay.Close()
	if !errors.Is(err, ErrReplayIncomplete) || errors.Is(err, ErrReplayMismatch) {
		t.Fatalf("premature close = %v, want incomplete only", err)
	}
	var incomplete *ReplayIncompleteError
	if !errors.As(err, &incomplete) || incomplete.OperationsRemaining != 1 || incomplete.EventsRemaining != 0 {
		t.Fatalf("incomplete context = %+v", incomplete)
	}
	if replay.Outcome().Status != ReplayIncomplete {
		t.Fatalf("incomplete outcome = %+v", replay.Outcome())
	}

	pendingScript := replayTestScript(BrowserScriptOperation{
		Expect: OperationExpectation{Type: OperationInvokeTool, FrameID: "frame-1", ToolName: "write_state", Input: MustJSONValue(map[string]any{})},
		Result: MustJSONValue(map[string]any{"invocation_id": "inv-pending"}),
	})
	replay, err = NewBrowserReplay(pendingScript)
	if err != nil {
		t.Fatalf("NewBrowserReplay(pending): %v", err)
	}
	if _, err := replay.ObserveOperation(context.Background(), pendingScript.Operations[0].ExpectRequest()); err != nil {
		t.Fatalf("pending operation: %v", err)
	}
	err = replay.Complete()
	if !errors.Is(err, ErrReplayIncomplete) || !errors.Is(err, ErrReplayPendingInvocations) {
		t.Fatalf("pending completion = %v, want incomplete and pending classifications", err)
	}
	if len(replay.Outcome().Pending) != 1 || replay.Outcome().Status != ReplayIncomplete {
		t.Fatalf("pending outcome = %+v", replay.Outcome())
	}
}

func TestBrowserReplayDiagnosticModeToleratesOnlyReadOnlyListWork(t *testing.T) {
	script := replayTestScript(BrowserScriptOperation{Expect: OperationExpectation{Type: OperationEnableLifecycle}})
	diagnostic, err := NewBrowserReplay(script, WithDiagnosticReplay())
	if err != nil {
		t.Fatalf("NewBrowserReplay(diagnostic): %v", err)
	}
	if _, err := diagnostic.Execute(context.Background(), OperationRequest{Type: OperationListTools}); err != nil {
		t.Fatalf("diagnostic list operation: %v", err)
	}
	if _, err := diagnostic.Execute(context.Background(), OperationRequest{Type: OperationEnableLifecycle}); err != nil {
		t.Fatalf("diagnostic scripted operation: %v", err)
	}
	if !diagnostic.Outcome().OK() || len(diagnostic.IgnoredOperations()) != 1 {
		t.Fatalf("diagnostic outcome = %+v ignored=%#v", diagnostic.Outcome(), diagnostic.IgnoredOperations())
	}

	strict, err := NewBrowserReplay(script)
	if err != nil {
		t.Fatalf("NewBrowserReplay(strict): %v", err)
	}
	if _, err := strict.Execute(context.Background(), OperationRequest{Type: OperationListTools}); !errors.Is(err, ErrReplayMismatch) {
		t.Fatalf("strict list operation = %v, want mismatch", err)
	}

	diagnostic, err = NewBrowserReplay(script, WithDiagnosticReplay())
	if err != nil {
		t.Fatalf("NewBrowserReplay(diagnostic invalid): %v", err)
	}
	if _, err := diagnostic.Execute(context.Background(), OperationRequest{Type: OperationListTools, Input: MustJSONValue(map[string]any{"write": true})}); !errors.Is(err, ErrReplayMismatch) {
		t.Fatalf("diagnostic list with arguments = %v, want mismatch", err)
	}
}

func TestBrowserReplayCancellationPreservesPrimaryOutcome(t *testing.T) {
	script := replayTestScript(BrowserScriptOperation{Expect: OperationExpectation{Type: OperationEnableLifecycle}})
	replay, err := NewBrowserReplay(script)
	if err != nil {
		t.Fatalf("NewBrowserReplay: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := replay.Wait(ctx); !errors.Is(err, ErrReplayCanceled) || !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait cancellation = %v, want replay and context cancellation", err)
	}
	if replay.Outcome().Status != ReplayCanceled {
		t.Fatalf("canceled outcome = %+v", replay.Outcome())
	}

	replay, err = NewBrowserReplay(script)
	if err != nil {
		t.Fatalf("NewBrowserReplay(diverged): %v", err)
	}
	if _, err := replay.Execute(context.Background(), OperationRequest{Type: OperationEnableWebMCP}); !errors.Is(err, ErrReplayMismatch) {
		t.Fatalf("divergent operation = %v", err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	if err := replay.Wait(ctx); !errors.Is(err, ErrReplayMismatch) || errors.Is(err, ErrReplayCanceled) {
		t.Fatalf("Wait after divergence = %v, want original mismatch", err)
	}
}

func TestBrowserReplayCleanupLeavesNoPendingInvocation(t *testing.T) {
	script := replayTestScript(
		BrowserScriptOperation{
			Expect: OperationExpectation{Type: OperationInvokeTool, FrameID: "frame-1", ToolName: "write_state", Input: MustJSONValue(map[string]any{})},
			Result: MustJSONValue(map[string]any{"invocation_id": "inv-clean"}),
			Emit: []EmittedEvent{{
				Type:         EmittedToolResponded,
				InvocationID: "inv-clean",
				Status:       "Completed",
				Output:       MustJSONValue(map[string]any{"ok": true}),
			}},
		},
		BrowserScriptOperation{Expect: OperationExpectation{Type: OperationCloseTarget}},
	)
	replay, err := NewBrowserReplay(script)
	if err != nil {
		t.Fatalf("NewBrowserReplay: %v", err)
	}
	if _, err := replay.Execute(context.Background(), script.Operations[0].ExpectRequest()); err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if _, err := replay.Execute(context.Background(), OperationRequest{Type: OperationCloseTarget}); err != nil {
		t.Fatalf("close target: %v", err)
	}
	if !replay.Outcome().OK() || len(replay.PendingInvocationIDs()) != 0 {
		t.Fatalf("cleanup outcome = %+v pending=%#v", replay.Outcome(), replay.PendingInvocationIDs())
	}
}

func replayTestScript(operations ...BrowserScriptOperation) BrowserScript {
	return BrowserScript{
		Version: BrowserScriptVersion,
		Endpoint: BrowserEndpoint{
			Version: EndpointVersionInfo{
				Browser:              "Chrome/Test",
				ProtocolVersion:      "1.3",
				WebSocketDebuggerURL: "ws://fixture/browser",
			},
			Targets: []BrowserTarget{{
				ID:                   "tab-1",
				Type:                 "page",
				Title:                "Fixture",
				URL:                  "https://fixture.test/",
				WebSocketDebuggerURL: "ws://fixture/page/tab-1",
			}},
		},
		Operations: operations,
	}
}

// ExpectRequest is intentionally a test-only convenience for translating the
// frozen fixture expectation into a caller request without decoding its raw
// input through map[string]any.
func (o BrowserScriptOperation) ExpectRequest() OperationRequest {
	return OperationRequest{
		Type:         o.Expect.Type,
		FrameID:      o.Expect.FrameID,
		ToolName:     o.Expect.ToolName,
		Input:        cloneRaw(o.Expect.Input),
		InvocationID: o.Expect.InvocationID,
		URL:          o.Expect.URL,
	}
}
