package operations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

const (
	testInvocationID = "broker-invocation"
	testBrowserInvID = "browser-invocation"
)

func parseTestArguments(args []string) (map[string]any, error) {
	values := make(map[string]any, len(args))
	for _, arg := range args {
		key, value, ok := strings.Cut(arg, "=")
		if !ok {
			return nil, errTestBroker
		}
		values[key] = value
	}
	return values, nil
}

func TestResolveInvocationSelectsToolAndInput(t *testing.T) {
	broker := newFakeBroker()
	cases := []struct {
		name  string
		input InvocationInput
		ref   webmcp.ToolRef
		body  string
	}{
		{name: "ref", input: InvocationInput{ToolRef: testToolRef, InputJSON: `{"q":1}`}, ref: testToolRef, body: `{"q":1}`},
		{name: "ref default input", input: InvocationInput{ToolRef: testToolRef}, ref: testToolRef, body: `{}`},
		{name: "opaque malformed input", input: InvocationInput{ToolRef: testToolRef, InputJSON: `{broken`}, ref: testToolRef, body: `{broken`},
		{name: "name", input: InvocationInput{Args: []string{testToolName}, InputJSON: `{"q":2}`}, ref: testToolRef, body: `{"q":2}`},
		{name: "key values", input: InvocationInput{Args: []string{testToolName, "q=x"}, ParseArguments: parseTestArguments}, ref: testToolRef, body: `{"q":"x"}`},
	}
	for _, testCase := range cases {
		ref, body, err := ResolveInvocation(context.Background(), broker, testCase.input)
		if err != nil || ref != testCase.ref || string(body) != testCase.body {
			t.Fatalf("%s: ResolveInvocation = %q %s %v, want %q %s", testCase.name, ref, body, err, testCase.ref, testCase.body)
		}
	}
}

func TestResolveInvocationRejectsAmbiguousOrConflictingInput(t *testing.T) {
	broker := newFakeBroker()
	broker.tools = append(broker.tools, webmcp.ToolDescriptor{Ref: "webmcp.tool-ref.v1:other", Name: "dup"}, webmcp.ToolDescriptor{Ref: "webmcp.tool-ref.v1:dup2", Name: "dup"})
	cases := []struct {
		name  string
		input InvocationInput
		path  string
	}{
		{name: "ref and name", input: InvocationInput{ToolRef: testToolRef, Args: []string{testToolName}}, path: issuePathToolRef},
		{name: "json and key values", input: InvocationInput{Args: []string{testToolName, "q=x"}, InputJSON: `{}`}, path: "/input_json"},
		{name: "nothing", input: InvocationInput{}, path: issuePathToolRef},
		{name: "ambiguous", input: InvocationInput{Args: []string{"dup"}}, path: issuePathToolName},
		{name: "unknown", input: InvocationInput{Args: []string{"missing"}}, path: issuePathToolName},
		{name: "no parser", input: InvocationInput{Args: []string{testToolName, "q=x"}}, path: "/arguments"},
	}
	for _, testCase := range cases {
		_, _, err := ResolveInvocation(context.Background(), broker, testCase.input)
		code, details := classifiedCode(err)
		issues, ok := details["issues"].([]webmcp.ToolResultIssue)
		if !ok || code != webmcp.ErrorInvalidToolInput || len(issues) != 1 || issues[0].Path != testCase.path {
			t.Fatalf("%s: error = %v (%v), want invalid input at %s", testCase.name, err, details, testCase.path)
		}
	}
	if _, _, err := ResolveInvocation(context.Background(), broker, InvocationInput{Args: []string{testToolName, "bad"}, ParseArguments: parseTestArguments}); !errors.Is(err, errTestBroker) {
		t.Fatalf("parser failure = %v, want the parser error", err)
	}
}

func TestInvocationResultErrorPropagatesFreshnessRetryability(t *testing.T) {
	err := InvocationResultError(webmcp.InvokeResult{
		InvocationID:        testInvocationID,
		BrowserInvocationID: testBrowserInvID,
		State:               webmcp.InvocationError,
		ErrorCode:           string(webmcp.ErrorInvocationFailed),
		ErrorDetails:        map[string]any{detailPhase: "result_freshness", safeRetryableKey: true},
	}, testToolRef)
	var classified *webmcp.ClassifiedError
	if !errors.As(err, &classified) || !classified.Retryable || classified.Details[detailInvocationID] != testBrowserInvID {
		t.Fatalf("freshness error = %#v, want retryable browser-correlated failure", err)
	}
	if _, leaked := classified.Details[safeRetryableKey]; leaked {
		t.Fatalf("internal retry marker leaked: %#v", classified.Details)
	}
	for state, want := range map[webmcp.InvocationState]webmcp.ErrorCode{
		webmcp.InvocationCanceled: webmcp.ErrorInvocationCanceled,
		webmcp.InvocationTimedOut: webmcp.ErrorInvocationTimedOut,
		webmcp.InvocationOrphaned: webmcp.ErrorInvocationOrphaned,
		webmcp.InvocationError:    webmcp.ErrorInvocationFailed,
	} {
		code, details := classifiedCode(InvocationResultError(webmcp.InvokeResult{InvocationID: testInvocationID, State: state}, testToolRef))
		if code != want || details[detailToolRef] != testToolRef || details[detailPhase] != phaseInvoke {
			t.Fatalf("state %s: code %s details %v, want %s", state, code, details, want)
		}
	}
}

func invokeRequest(receipts *bytes.Buffer) InvokeRequest {
	return InvokeRequest{Selector: singleSelector(), Input: InvocationInput{ToolRef: testToolRef}, Reason: "test", Receipts: receipts}
}

func TestInvokeWritesReceiptAndWaitsForTerminalResult(t *testing.T) {
	broker := &canceller{fakeBroker: newFakeBroker()}
	broker.invokeResult = webmcp.InvokeResult{InvocationID: testInvocationID, BrowserInvocationID: testBrowserInvID, State: webmcp.InvocationDispatched}
	broker.waitResult = webmcp.InvokeResult{InvocationID: testInvocationID, State: webmcp.InvocationCompleted, Output: json.RawMessage(`{"ok":true}`)}
	var receipts bytes.Buffer
	result, err := Invoke(context.Background(), broker, invokeRequest(&receipts))
	if err != nil {
		t.Fatalf("Invoke = %v", err)
	}
	if result.InvocationID != testBrowserInvID || result.Status != string(webmcp.InvocationCompleted) || string(result.Output) != `{"ok":true}` {
		t.Fatalf("result = %+v", result)
	}
	var receipt Receipt
	if err := json.Unmarshal(receipts.Bytes(), &receipt); err != nil || receipt.Version != ReceiptVersion || receipt.InvocationID != testBrowserInvID || receipt.State != string(webmcp.InvocationDispatched) {
		t.Fatalf("receipt = %q (%v)", receipts.String(), err)
	}
	broker.waitResult = webmcp.InvokeResult{InvocationID: testInvocationID, State: webmcp.InvocationError}
	if _, err := Invoke(context.Background(), broker, invokeRequest(&receipts)); err == nil {
		t.Fatal("a failed terminal result was reported as success")
	}
}

func TestInvokeDefaultsEmptyOutputAndStatus(t *testing.T) {
	broker := newFakeBroker()
	broker.invokeResult = webmcp.InvokeResult{BrowserInvocationID: testBrowserInvID}
	result, err := Invoke(context.Background(), broker, invokeRequest(&bytes.Buffer{}))
	if err != nil || string(result.Output) != jsonNull || result.Status != string(webmcp.InvocationDispatched) {
		t.Fatalf("Invoke = %+v, %v", result, err)
	}
}

func TestInvokeReportsUnwritableReceiptAsUnknownSideEffect(t *testing.T) {
	broker := newFakeBroker()
	broker.invokeResult = webmcp.InvokeResult{InvocationID: testInvocationID}
	request := invokeRequest(nil)
	request.Receipts = nil
	_, err := Invoke(context.Background(), broker, request)
	if code, details := classifiedCode(err); code != webmcp.ErrorInvocationFailed || details[detailSideEffectUnknown] != true {
		t.Fatalf("receipt error = %v, want side_effect_unknown failure", err)
	}
	broker.invokeResult = webmcp.InvokeResult{}
	if _, err := Invoke(context.Background(), broker, invokeRequest(&bytes.Buffer{})); err == nil {
		t.Fatal("a dispatch without an invocation ID wrote a receipt")
	}
}

func TestInvokeInterruptBeforeDispatchDoesNotFabricateInvocationID(t *testing.T) {
	broker := newFakeBroker()
	broker.invokeErr = errTestBroker
	request := invokeRequest(&bytes.Buffer{})
	request.Interrupted = func() bool { return true }
	_, err := Invoke(context.Background(), broker, request)
	result := webmcp.ResultErrorFor(err, webmcp.ErrorInvocationFailed, nil)
	if result.Code != string(webmcp.ErrorInvocationCanceled) || result.Retryable || result.Details[detailPhase] != phaseBeforeDispatch {
		t.Fatalf("pre-dispatch cancellation = %+v", result)
	}
	if _, ok := result.Details[detailInvocationID]; ok {
		t.Fatalf("pre-dispatch cancellation fabricated an invocation ID: %#v", result.Details)
	}
	broker.discoverErr = errTestBroker
	if _, err := Invoke(context.Background(), broker, request); err == nil || !strings.Contains(err.Error(), "cancel") {
		t.Fatalf("interrupted selection failure = %v, want cancellation", err)
	}
}

func TestInvokeInterruptAfterDispatchReconcilesWithBoundedCancel(t *testing.T) {
	broker := &canceller{fakeBroker: newFakeBroker()}
	broker.invokeResult = webmcp.InvokeResult{InvocationID: testInvocationID, BrowserInvocationID: testBrowserInvID, State: webmcp.InvocationDispatched}
	broker.waitErr = context.Canceled
	interrupted := false
	request := invokeRequest(&bytes.Buffer{})
	request.Interrupted = func() bool { return interrupted }
	request.Input.InputJSON = `{}`
	broker.waitResult = webmcp.InvokeResult{}
	interruptingWait := &interruptingBroker{canceller: broker, interrupt: func() { interrupted = true }}
	_, err := Invoke(context.Background(), interruptingWait, request)
	code, details := classifiedCode(err)
	if code != webmcp.ErrorInvocationCanceled || details["cancel_status"] != cancelStatusRequested || details[detailInvocationID] != testBrowserInvID {
		t.Fatalf("interrupt error = %v (%v)", err, details)
	}
	if len(broker.cancels) != 1 || broker.cancels[0].InvocationID != testInvocationID || broker.cancels[0].Reason != cancelSourceInterrupt {
		t.Fatalf("cancel requests = %+v, want one broker-ID interrupt cancel", broker.cancels)
	}
}

// interruptingBroker simulates SIGINT arriving while the wait is blocked.
type interruptingBroker struct {
	*canceller
	interrupt func()
}

func (b *interruptingBroker) WaitInvocation(ctx context.Context, id webmcp.InvocationID) (webmcp.InvokeResult, error) {
	b.interrupt()
	return b.canceller.WaitInvocation(ctx, id)
}

func TestReconcileInterruptReportsCancellationStatus(t *testing.T) {
	selected := webmcp.PageKey{BrowserID: testBrowserID, TargetID: testTargetID}
	direct := &canceller{fakeBroker: newFakeBroker()}
	cases := []struct {
		name     string
		broker   webmcp.Broker
		result   webmcp.InvokeResult
		selected webmcp.PageKey
		want     string
	}{
		{name: "no broker", want: cancelStatusNoBroker},
		{name: "no IDs", broker: newFakeBroker(), want: cancelStatusNotRequested},
		{name: "browser ID direct", broker: direct, result: webmcp.InvokeResult{BrowserInvocationID: testBrowserInvID}, selected: selected, want: cancelStatusRequested},
		{name: "browser ID without direct", broker: newFakeBroker(), result: webmcp.InvokeResult{BrowserInvocationID: testBrowserInvID}, selected: selected, want: cancelStatusNoInvocation},
		{name: "browser ID without target", broker: direct, result: webmcp.InvokeResult{BrowserInvocationID: testBrowserInvID}, want: cancelStatusNoInvocation},
	}
	for _, testCase := range cases {
		err := ReconcileInterrupt(context.Background(), testCase.broker, testCase.result, testCase.selected, "", "")
		if code, details := classifiedCode(err); code != webmcp.ErrorInvocationCanceled || details["cancel_status"] != testCase.want || details[detailSideEffectUnknown] != true {
			t.Fatalf("%s: %v (%v), want %s", testCase.name, err, details, testCase.want)
		}
	}
	if len(direct.directCancels) != 1 || direct.directCancels[0].InvocationID != testBrowserInvID {
		t.Fatalf("direct cancels = %+v", direct.directCancels)
	}
}

func TestBoundedCancellationStatusUsesIndependentContext(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if status := boundedCancellationStatus(canceled, func(ctx context.Context) error { return ctx.Err() }); status != cancelStatusRequested {
		t.Fatalf("cleanup status = %q, want requested", status)
	}
	if status := boundedCancellationStatus(context.Background(), func(context.Context) error { return errTestBroker }); status != cancelStatusRejected {
		t.Fatalf("rejected status = %q", status)
	}
	if status := boundedCancellationStatus(context.Background(), func(context.Context) error { return context.DeadlineExceeded }); status != cancelStatusTimedOut {
		t.Fatalf("deadline status = %q", status)
	}
	if status := boundedCancellationStatus(context.Background(), nil); status != cancelStatusNotRequested {
		t.Fatalf("nil request status = %q", status)
	}
}

func TestNewInterruptContextFollowsParentAndStops(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	ctx, interrupted, stop := NewInterruptContext(parent)
	cancel()
	<-ctx.Done()
	if interrupted() {
		t.Fatal("parent cancellation was reported as SIGINT")
	}
	stop()
	stop()
}
