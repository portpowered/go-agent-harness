package chrome

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto/cdp"
	cdpTarget "github.com/chromedp/cdproto/target"
	cdpWebMCP "github.com/chromedp/cdproto/webmcp"
	"github.com/chromedp/chromedp"
	"github.com/go-json-experiment/json/jsontext"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

type invocationCall struct {
	method       string
	frameID      cdp.FrameID
	toolName     string
	input        jsontext.Value
	invocationID string
	wireParams   []byte
}

type invocationExecutor struct {
	mu           sync.Mutex
	calls        []invocationCall
	invocationID string
	errors       map[string]error
}

type wireTraceRecorder struct {
	mu     sync.Mutex
	traces []webmcp.WebMCPWireTrace
}

func (r *wireTraceRecorder) RecordWebMCPWireTrace(trace webmcp.WebMCPWireTrace) {
	r.mu.Lock()
	r.traces = append(r.traces, trace)
	r.mu.Unlock()
}

func (r *wireTraceRecorder) snapshot() []webmcp.WebMCPWireTrace {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]webmcp.WebMCPWireTrace(nil), r.traces...)
}

func (e *invocationExecutor) Execute(ctx context.Context, method string, params, result any) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	call := invocationCall{method: method}
	switch params := params.(type) {
	case *cdpWebMCP.InvokeToolParams:
		call.frameID = params.FrameID
		call.toolName = params.ToolName
		call.input = jsontext.Value(bytes.Clone(params.Input))
		call.wireParams, _ = json.Marshal(params)
	case *cdpWebMCP.CancelInvocationParams:
		call.invocationID = params.InvocationID
		call.wireParams, _ = json.Marshal(params)
	}

	e.mu.Lock()
	e.calls = append(e.calls, call)
	err := e.errors[method]
	invocationID := e.invocationID
	e.mu.Unlock()
	if err != nil {
		return err
	}
	if returns, ok := result.(*cdpWebMCP.InvokeToolReturns); ok {
		returns.InvocationID = invocationID
	}
	return nil
}

func (e *invocationExecutor) snapshot() []invocationCall {
	e.mu.Lock()
	defer e.mu.Unlock()
	result := append([]invocationCall(nil), e.calls...)
	for i := range result {
		result[i].input = jsontext.Value(bytes.Clone(result[i].input))
		result[i].wireParams = bytes.Clone(result[i].wireParams)
	}
	return result
}

func newInvocationTestSession(t *testing.T, executor cdp.Executor) *targetSession {
	t.Helper()
	handle := &handle{
		candidate: webmcp.BrowserCandidate{
			ID:           "browser-invocation",
			HTTPURL:      "http://127.0.0.1:9222",
			BrowserWSURL: "ws://127.0.0.1:9222/devtools/browser/browser-invocation",
			Loopback:     true,
		},
		browserExecutor: executor,
		httpClient:      nil,
		commandTimeout:  time.Second,
		eventBuffer:     16,
		sessions:        make(map[*targetSession]struct{}),
		done:            make(chan struct{}),
	}
	targetContext, cancelTarget := context.WithCancel(context.Background())
	session := newTargetSession(handle, targetContext, cancelTarget, webmcp.Target{
		BrowserID: handle.candidate.ID,
		ID:        "target-invocation",
		Type:      "page",
		Title:     "Invocation fixture",
		URL:       "https://example.test/invocation",
		Origin:    "https://example.test",
	}, webmcp.TargetOwnershipExternal)
	session.runAction = func(ctx context.Context, actions ...chromedp.Action) error {
		actionContext := cdp.WithExecutor(ctx, executor)
		for _, action := range actions {
			if err := action.Do(actionContext); err != nil {
				return err
			}
		}
		return nil
	}
	t.Cleanup(func() {
		if err := session.Close(); err != nil {
			t.Errorf("close invocation session: %v", err)
		}
	})
	return session
}

func classifiedCode(t *testing.T, err error) *webmcp.ClassifiedError {
	t.Helper()
	if err == nil {
		t.Fatal("expected classified error, got nil")
	}
	var classified *webmcp.ClassifiedError
	if !errors.As(err, &classified) {
		t.Fatalf("error = %v, want ClassifiedError", err)
	}
	return classified
}

func TestInvokeWebMCPUsesObjectInputAndCorrelatesCompletion(t *testing.T) {
	const (
		frameID      = "frame-invocation"
		toolName     = "submit_form"
		invocationID = "browser-invocation-7"
		output       = `{"accepted":true,"count":9007199254740993}`
	)
	executor := &invocationExecutor{invocationID: invocationID, errors: make(map[string]error)}
	session := newInvocationTestSession(t, executor)
	input := json.RawMessage(` { "count":9007199254740993, "nested":{"items":[true,null]} } `)

	gotID, err := session.InvokeWebMCP(context.Background(), frameID, toolName, input)
	if err != nil {
		t.Fatalf("invoke WebMCP: %v", err)
	}
	if gotID != webmcp.InvocationID(invocationID) {
		t.Fatalf("invocation ID = %q, want %q", gotID, invocationID)
	}
	calls := executor.snapshot()
	if len(calls) != 1 {
		t.Fatalf("protocol calls = %+v, want one invoke", calls)
	}
	call := calls[0]
	if call.method != cdpWebMCP.CommandInvokeTool || call.frameID != frameID || call.toolName != toolName {
		t.Fatalf("invoke call = %+v, want exact method/frame/tool", call)
	}
	if string(call.input) != string(input) {
		t.Fatalf("input bytes = %q, want exact object bytes %q", call.input, input)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(call.wireParams, &wire); err != nil {
		t.Fatalf("decode invoke params: %v", err)
	}
	if bytes.Equal(wire["input"], []byte(`"`+string(input)+`"`)) || len(wire["input"]) == 0 || wire["input"][0] != '{' {
		t.Fatalf("wire input = %s, want an object rather than a JSON string", wire["input"])
	}
	if !bytes.Contains(wire["input"], []byte("9007199254740993")) || !bytes.Contains(wire["input"], []byte(`"items":[true,null]`)) {
		t.Fatalf("wire input = %s, lost nested or integer tokens", wire["input"])
	}

	invokedInput := string(input)
	respondedOutput := jsontext.Value([]byte(output))
	session.enqueueProtocolEvent(&cdpWebMCP.EventToolInvoked{
		FrameID:      frameID,
		ToolName:     toolName,
		InvocationID: invocationID,
		Input:        invokedInput,
	})
	session.enqueueProtocolEvent(&cdpWebMCP.EventToolResponded{
		InvocationID: invocationID,
		Status:       cdpWebMCP.InvocationStatusCompleted,
		Output:       respondedOutput,
	})

	invoked := nextBrowserEvent(t, session.Events())
	responded := nextBrowserEvent(t, session.Events())
	if invoked.Type != webmcp.EventToolInvoked || invoked.FrameID != frameID || invoked.ToolName != toolName || invoked.InvocationID != invocationID || string(invoked.Input) != invokedInput {
		t.Fatalf("toolInvoked event = %+v, want correlated copied input", invoked)
	}
	if responded.Type != webmcp.EventToolResponded || responded.InvocationID != invocationID || responded.Status != string(cdpWebMCP.InvocationStatusCompleted) || string(responded.Output) != output || responded.ErrorCode != "" {
		t.Fatalf("toolResponded event = %+v, want completed correlated output", responded)
	}
	responded.Output[0] = 'X'
	if string(respondedOutput) != output {
		t.Fatal("consumer mutation leaked into generated response output")
	}
}

func TestInvokeWebMCPRejectsNonObjectInputBeforeDispatch(t *testing.T) {
	executor := &invocationExecutor{invocationID: "unused", errors: make(map[string]error)}
	session := newInvocationTestSession(t, executor)
	cases := []struct {
		name  string
		input json.RawMessage
		issue string
	}{
		{name: "empty", input: nil, issue: "empty"},
		{name: "whitespace", input: json.RawMessage(" \t\n"), issue: "empty"},
		{name: "malformed", input: json.RawMessage(`{"count":`), issue: "malformed"},
		{name: "trailing token", input: json.RawMessage(`{"count":1} {"other":2}`), issue: "malformed"},
		{name: "array", input: json.RawMessage(`[1,2]`), issue: "object_required"},
		{name: "scalar", input: json.RawMessage(`42`), issue: "object_required"},
		{name: "string", input: json.RawMessage(`"value"`), issue: "object_required"},
		{name: "null", input: json.RawMessage(`null`), issue: "object_required"},
		{name: "boolean", input: json.RawMessage(`true`), issue: "object_required"},
		{name: "invalid utf8", input: json.RawMessage{'{', '"', 'x', '"', ':', 0xff, '}'}, issue: "invalid_utf8"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			before := len(executor.snapshot())
			_, err := session.InvokeWebMCP(context.Background(), "frame-invocation", "tool", testCase.input)
			classified := classifiedCode(t, err)
			if classified.Code != webmcp.ErrorInvalidToolInput || !classified.Retryable {
				t.Fatalf("classified error = %+v, want retryable invalid_tool_input", classified)
			}
			issues, ok := classified.Details["issues"].([]map[string]string)
			if !ok || len(issues) != 1 || issues[0]["path"] != "/input_json" || issues[0]["code"] != testCase.issue {
				t.Fatalf("validation details = %+v, want issue %q", classified.Details, testCase.issue)
			}
			if after := len(executor.snapshot()); after != before {
				t.Fatalf("protocol calls changed from %d to %d for invalid input", before, after)
			}
		})
	}
}

func TestCancelWebMCPDispatchesExactIDWithoutSynthesizingResult(t *testing.T) {
	const invocationID = "cancel-exact-42"
	executor := &invocationExecutor{errors: make(map[string]error)}
	session := newInvocationTestSession(t, executor)

	if err := session.CancelWebMCP(context.Background(), webmcp.InvocationID(invocationID)); err != nil {
		t.Fatalf("cancel WebMCP: %v", err)
	}
	calls := executor.snapshot()
	if len(calls) != 1 || calls[0].method != cdpWebMCP.CommandCancelInvocation || calls[0].invocationID != invocationID {
		t.Fatalf("cancel calls = %+v, want exact cancellation ID", calls)
	}
	if bytes.Contains(calls[0].wireParams, []byte(`"`+invocationID+`"`)) == false {
		t.Fatalf("cancel wire params = %s, want invocation ID", calls[0].wireParams)
	}
	select {
	case event := <-session.Events():
		t.Fatalf("cancel synthesized an event before Chrome response: %+v", event)
	default:
	}

	session.enqueueProtocolEvent(&cdpWebMCP.EventToolResponded{
		InvocationID: invocationID,
		Status:       cdpWebMCP.InvocationStatus("Canceled"),
	})
	responded := nextBrowserEvent(t, session.Events())
	if responded.InvocationID != invocationID || responded.ErrorCode != string(webmcp.ErrorInvocationCanceled) {
		t.Fatalf("canceled response = %+v, want exact ID and cancellation semantics", responded)
	}
}

func TestCancelWebMCPRecordsExactTargetSessionBeforeDispatch(t *testing.T) {
	const (
		browserID    = "browser-invocation"
		targetID     = "target-invocation"
		sessionID    = "session-wire-trace"
		invocationID = "browser-receipt-wire-7"
	)
	executor := &invocationExecutor{errors: make(map[string]error)}
	session := newInvocationTestSession(t, executor)
	recorder := &wireTraceRecorder{}
	session.handle.wireTrace = recorder
	session.setProtocolTarget(&chromedp.Target{SessionID: sessionID, TargetID: targetID})
	session.markListenerReady()

	if err := session.CancelWebMCP(context.Background(), invocationID); err != nil {
		t.Fatalf("cancel WebMCP: %v", err)
	}
	calls := executor.snapshot()
	if len(calls) != 1 || calls[0].method != cdpWebMCP.CommandCancelInvocation || calls[0].invocationID != invocationID {
		t.Fatalf("cancel calls = %+v, want one exact CDP cancellation", calls)
	}
	traces := recorder.snapshot()
	if len(traces) != 1 {
		t.Fatalf("wire traces = %+v, want one trace", traces)
	}
	trace := traces[0]
	if trace.Version != webmcp.WebMCPWireTraceVersion || trace.Sequence != 1 || trace.BrowserID != browserID || trace.TargetID != targetID || trace.TargetSessionID != sessionID || trace.Method != webmcp.WebMCPCancelInvocationMethod || trace.InvocationID != invocationID || trace.Phase != webmcp.WebMCPWirePhaseBeforeDispatch || !trace.ListenerReady {
		t.Fatalf("wire trace = %+v, want exact target/session and ready-before-dispatch evidence", trace)
	}
	wire, err := json.Marshal(trace)
	if err != nil {
		t.Fatalf("marshal wire trace: %v", err)
	}
	for _, forbidden := range []string{"endpoint", "credential", "input", "output", "ws://", "https://"} {
		if bytes.Contains(wire, []byte(forbidden)) {
			t.Fatalf("wire trace contains forbidden %q: %s", forbidden, wire)
		}
	}
}

func TestInvocationFailuresAreClassifiedAndNotRetried(t *testing.T) {
	invokeFailure := errors.New("invoke failed with secret endpoint details")
	invokeExecutor := &invocationExecutor{
		errors: map[string]error{cdpWebMCP.CommandInvokeTool: invokeFailure},
	}
	invokeSession := newInvocationTestSession(t, invokeExecutor)
	_, err := invokeSession.InvokeWebMCP(context.Background(), "frame-invocation", "tool", json.RawMessage(`{}`))
	classified := classifiedCode(t, err)
	if classified.Code != webmcp.ErrorInvocationFailed || classified.Retryable {
		t.Fatalf("invoke failure = %+v, want non-retryable invocation_failed", classified)
	}
	if classified.Details["side_effect_unknown"] != true || classified.Details["phase"] != "invoke" {
		t.Fatalf("invoke failure details = %+v, want side-effect uncertainty", classified.Details)
	}
	if strings.Contains(classified.Error(), "secret endpoint") || strings.Contains(classified.Error(), "invoke failed") {
		t.Fatalf("invoke failure leaked transport detail: %v", classified)
	}
	if calls := invokeExecutor.snapshot(); len(calls) != 1 {
		t.Fatalf("invoke calls = %d, want one dispatch and no retry", len(calls))
	}

	cancelFailure := errors.New("cancel failed with secret endpoint details")
	cancelExecutor := &invocationExecutor{
		errors: map[string]error{cdpWebMCP.CommandCancelInvocation: cancelFailure},
	}
	cancelSession := newInvocationTestSession(t, cancelExecutor)
	err = cancelSession.CancelWebMCP(context.Background(), "cancel-failure-1")
	classified = classifiedCode(t, err)
	if classified.Code != webmcp.ErrorInvocationCanceled || classified.Retryable {
		t.Fatalf("cancel failure = %+v, want non-retryable invocation_canceled", classified)
	}
	if classified.Details["invocation_id"] != "cancel-failure-1" || classified.Details["cancel_source"] != "explicit" || classified.Details["side_effect_unknown"] != true {
		t.Fatalf("cancel failure details = %+v, want exact ID/uncertain side effect", classified.Details)
	}
	if strings.Contains(classified.Error(), "secret endpoint") || strings.Contains(classified.Error(), "cancel failed") {
		t.Fatalf("cancel failure leaked transport detail: %v", classified)
	}
	if calls := cancelExecutor.snapshot(); len(calls) != 1 {
		t.Fatalf("cancel calls = %d, want one dispatch and no retry", len(calls))
	}
}

func TestInvocationFailureUsesLifecycleClassification(t *testing.T) {
	t.Run("target detached", func(t *testing.T) {
		executor := &invocationExecutor{errors: make(map[string]error)}
		session := newInvocationTestSession(t, executor)
		protocolTarget := &chromedp.Target{SessionID: "session-detached", TargetID: "target-invocation"}
		session.setProtocolTarget(protocolTarget)
		session.enqueueProtocolEvent(&cdpTarget.EventDetachedFromTarget{SessionID: protocolTarget.SessionID})
		terminal := nextBrowserEvent(t, session.Events())
		if terminal.Type != webmcp.EventTargetDetached {
			t.Fatalf("terminal event = %+v, want target_detached", terminal)
		}

		_, err := session.InvokeWebMCP(context.Background(), "frame-invocation", "tool", json.RawMessage(`{}`))
		classified := classifiedCode(t, err)
		if classified.Code != webmcp.ErrorTargetDetached {
			t.Fatalf("invoke after detach = %+v, want target_detached", classified)
		}
		if calls := executor.snapshot(); len(calls) != 0 {
			t.Fatalf("invoke after detach dispatched %d commands", len(calls))
		}
	})

	t.Run("browser disconnected", func(t *testing.T) {
		executor := &invocationExecutor{errors: make(map[string]error)}
		session := newInvocationTestSession(t, executor)
		session.transportLost()

		_, err := session.InvokeWebMCP(context.Background(), "frame-invocation", "tool", json.RawMessage(`{}`))
		classified := classifiedCode(t, err)
		if classified.Code != webmcp.ErrorBrowserDisconnected {
			t.Fatalf("invoke after disconnect = %+v, want browser_disconnected", classified)
		}
		if calls := executor.snapshot(); len(calls) != 0 {
			t.Fatalf("invoke after disconnect dispatched %d commands", len(calls))
		}
	})
}

func TestToolRespondedPageFailureUsesC0InvocationClassification(t *testing.T) {
	executor := &invocationExecutor{errors: make(map[string]error)}
	session := newInvocationTestSession(t, executor)
	session.enqueueProtocolEvent(&cdpWebMCP.EventToolResponded{
		InvocationID: "page-failure-1",
		Status:       cdpWebMCP.InvocationStatus("Error"),
		ErrorText:    "untrusted page error text must stay out of the neutral event",
	})
	responded := nextBrowserEvent(t, session.Events())
	if responded.InvocationID != "page-failure-1" || responded.ErrorCode != string(webmcp.ErrorInvocationFailed) || responded.Reason != "page_error" {
		t.Fatalf("page failure event = %+v, want invocation_failed/page_error", responded)
	}
	if strings.Contains(responded.Reason, "untrusted") {
		t.Fatal("page error text leaked into neutral event")
	}
}
