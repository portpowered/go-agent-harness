package webmcp_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/testkit"
)

func TestStatefulBrokerDirectCancelClassifiesTerminalAfterDispatch(t *testing.T) {
	tests := []struct {
		name       string
		status     string
		errorCode  string
		output     string
		wantCode   webmcp.ErrorCode
		wantResult string
	}{
		{
			name:       "completed",
			status:     "Completed",
			output:     `{"secret":"page-output"}`,
			wantCode:   webmcp.ErrorInvocationFailed,
			wantResult: "completed_anyway",
		},
		{
			name:       "page_error",
			status:     "Error",
			errorCode:  "page_exception",
			output:     `{"secret":"page-error-output"}`,
			wantCode:   webmcp.ErrorInvocationFailed,
			wantResult: "completed_anyway",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			original, fresh, runtime, session, ref := newDirectCancellationFixture(t, false)
			session.BlockInvocations()
			dispatched, err := original.Invoke(context.Background(), webmcp.InvokeRequest{
				ToolRef: ref,
				Input:   []byte(`{"step":1}`),
			})
			if err != nil {
				t.Fatalf(`invoke: %v`, err)
			}
			if _, err := session.WaitForInvocationAdmission(context.Background()); err != nil {
				t.Fatalf(`observe target invocation: %v`, err)
			}

			cancelDone := startDirectCancellation(t, runtime, fresh, dispatched.BrowserInvocationID, context.Background())
			if test.status == "Completed" {
				err = session.ReleaseInvocation(dispatched.InvocationID, []byte(test.output))
			} else {
				err = session.Emit(webmcp.BrowserEvent{
					Type:         webmcp.EventToolResponded,
					InvocationID: dispatched.InvocationID,
					Status:       test.status,
					ErrorCode:    test.errorCode,
					Output:       []byte(test.output),
				})
			}
			if err != nil {
				t.Fatalf(`emit terminal response: %v`, err)
			}
			cancelErr := <-cancelDone
			classified := requireDirectCancellationError(t, cancelErr, test.wantCode)
			if classified.Details[`invocation_id`] != string(dispatched.BrowserInvocationID) ||
				classified.Details[`outcome`] != test.wantResult ||
				classified.Details[`terminal_observed`] != true ||
				classified.Details[`side_effect_unknown`] != true {
				t.Fatalf(`direct cancellation details = %#v`, classified.Details)
			}
			if classified.Details[`terminal_event`] != string(webmcp.EventToolResponded) {
				t.Fatalf(`direct cancellation terminal event = %#v`, classified.Details[`terminal_event`])
			}
			if strings.Contains(classified.Message, test.output) || strings.Contains(fmt.Sprint(classified.Details), test.output) {
				t.Fatalf(`page output leaked in direct cancellation error: %v %#v`, classified.Message, classified.Details)
			}
			if _, ok := classified.Details[`output`]; ok {
				t.Fatalf(`direct cancellation details contain output: %#v`, classified.Details)
			}
		})
	}
}

func TestStatefulBrokerDirectCancelClassifiesExplicitProtocolRejectionAsUnconfirmed(t *testing.T) {
	cause := errors.New("cancel protocol rejected with secret transport details")
	rejection := webmcp.NewClassifiedError(webmcp.ErrorInvocationCanceled, "the browser rejected cancellation", map[string]any{
		"browser_id":          "browser-direct-cancel",
		"target_id":           "tab-a",
		"invocation_id":       "browser-receipt-rejected",
		"cancel_source":       "explicit",
		"phase":               "cancel",
		"reason_code":         "protocol_error",
		"side_effect_unknown": true,
	})
	rejection.Cause = cause
	original, fresh, _, session, ref := newDirectCancellationFixture(t, false, rejection)
	session.BlockInvocations()
	dispatched, err := original.Invoke(context.Background(), webmcp.InvokeRequest{
		ToolRef: ref,
		Input:   []byte(`{"step":1}`),
	})
	if err != nil {
		t.Fatalf(`invoke: %v`, err)
	}
	if _, err := session.WaitForInvocationAdmission(context.Background()); err != nil {
		t.Fatalf(`observe target invocation: %v`, err)
	}

	classified := requireDirectCancellationError(t, fresh.CancelDirect(context.Background(), webmcp.DirectCancelRequest{
		Target:       webmcp.TargetSelector{BrowserID: `browser-direct-cancel`, TargetID: `tab-a`},
		InvocationID: dispatched.BrowserInvocationID,
	}), webmcp.ErrorInvocationFailed)
	if classified.Retryable || classified.Details[`outcome`] != `cancellation_unconfirmed` ||
		classified.Details[`cancel_phase`] != `cancel_dispatched` ||
		classified.Details[`terminal_observed`] != false || classified.Details[`side_effect_unknown`] != true {
		t.Fatalf(`protocol rejection classification = code:%s retryable:%t details:%#v`, classified.Code, classified.Retryable, classified.Details)
	}
	if strings.Contains(classified.Error(), `secret transport details`) || strings.Contains(fmt.Sprint(classified.Details), `secret transport details`) {
		t.Fatalf(`protocol rejection leaked transport detail: %v %#v`, classified.Message, classified.Details)
	}
}

func TestStatefulBrokerDirectCancelRequiresExactTerminalAndBoundsLateEvent(t *testing.T) {
	original, fresh, runtime, session, ref := newDirectCancellationFixture(t, false)
	session.BlockInvocations()
	dispatched, err := original.Invoke(context.Background(), webmcp.InvokeRequest{
		ToolRef: ref,
		Input:   []byte(`{"step":1}`),
	})
	if err != nil {
		t.Fatalf(`invoke: %v`, err)
	}
	if _, err := session.WaitForInvocationAdmission(context.Background()); err != nil {
		t.Fatalf(`observe target invocation: %v`, err)
	}

	cancelContext, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	cancelDone := startDirectCancellation(t, runtime, fresh, dispatched.BrowserInvocationID, cancelContext)
	if err := session.Emit(webmcp.BrowserEvent{
		Type:         webmcp.EventToolResponded,
		InvocationID: `other-invocation`,
		Status:       `Canceled`,
	}); err != nil {
		t.Fatalf(`emit wrong-correlation response: %v`, err)
	}
	started := time.Now()
	cancelErr := <-cancelDone
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf(`direct cancellation exceeded its caller bound: %v`, elapsed)
	}
	classified := requireDirectCancellationError(t, cancelErr, webmcp.ErrorInvocationFailed)
	if classified.Details[`outcome`] != `cancellation_unconfirmed` ||
		classified.Details[`terminal_observed`] != false ||
		classified.Details[`side_effect_unknown`] != true {
		t.Fatalf(`unconfirmed cancellation details = %#v`, classified.Details)
	}
	if classified.Details[`invocation_id`] != string(dispatched.BrowserInvocationID) {
		t.Fatalf(`unconfirmed cancellation ID = %#v`, classified.Details[`invocation_id`])
	}

	// The terminal response arriving after the bounded command has returned
	// is late reconciliation only; it cannot reopen the direct waiter or cause
	// a second cancellation command.
	if err := session.ReleaseInvocation(dispatched.InvocationID, []byte(`{"late":true}`)); err != nil {
		t.Fatalf(`late terminal response: %v`, err)
	}
	cancelOperations := operationsOfKind(runtime.Operations(), testkit.OperationCancel)
	if len(cancelOperations) != 1 || cancelOperations[0].InvocationID != dispatched.BrowserInvocationID {
		t.Fatalf(`direct cancel operations = %#v, want one exact request`, cancelOperations)
	}
}

func TestStatefulBrokerDirectCancelSeparatesNavigationAndDisconnect(t *testing.T) {
	tests := []struct {
		name      string
		terminate func(*testkit.ScriptedBrowserRuntime, *testkit.ScriptedTargetSession) error
		wantCode  webmcp.ErrorCode
		wantEvent webmcp.BrowserEventType
		wantOut   string
	}{
		{
			name: `navigation`,
			terminate: func(_ *testkit.ScriptedBrowserRuntime, session *testkit.ScriptedTargetSession) error {
				return session.Navigate(`https://fixture.test/next`, `https://fixture.test`)
			},
			wantCode:  webmcp.ErrorPageNavigated,
			wantEvent: webmcp.EventPageNavigated,
			wantOut:   `page_navigated`,
		},
		{
			name: `browser_disconnect`,
			terminate: func(runtime *testkit.ScriptedBrowserRuntime, _ *testkit.ScriptedTargetSession) error {
				return runtime.Disconnect(`browser-direct-cancel`, `transport_lost`)
			},
			wantCode:  webmcp.ErrorBrowserDisconnected,
			wantEvent: webmcp.EventBrowserDisconnected,
			wantOut:   `browser_disconnected`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			original, fresh, runtime, session, ref := newDirectCancellationFixture(t, false)
			session.BlockInvocations()
			dispatched, err := original.Invoke(context.Background(), webmcp.InvokeRequest{
				ToolRef: ref,
				Input:   []byte(`{"step":1}`),
			})
			if err != nil {
				t.Fatalf(`invoke: %v`, err)
			}
			if _, err := session.WaitForInvocationAdmission(context.Background()); err != nil {
				t.Fatalf(`observe target invocation: %v`, err)
			}
			cancelDone := startDirectCancellation(t, runtime, fresh, dispatched.BrowserInvocationID, context.Background())
			if err := test.terminate(runtime, session); err != nil {
				t.Fatalf(`terminate target: %v`, err)
			}
			classified := requireDirectCancellationError(t, <-cancelDone, test.wantCode)
			if classified.Details[`outcome`] != test.wantOut ||
				classified.Details[`terminal_event`] != string(test.wantEvent) ||
				classified.Details[`terminal_observed`] != true ||
				classified.Details[`side_effect_unknown`] != true {
				t.Fatalf(`lifecycle cancellation details = %#v`, classified.Details)
			}
		})
	}
}

func newDirectCancellationFixture(t *testing.T, emitCancellationResponse bool, cancelErrors ...error) (*webmcp.StatefulBroker, *webmcp.StatefulBroker, *testkit.ScriptedBrowserRuntime, *testkit.ScriptedTargetSession, webmcp.ToolRef) {
	t.Helper()
	clock := testkit.NewFakeClock(time.Date(2026, time.August, 28, 12, 0, 0, 0, time.UTC))
	ids := testkit.NewDeterministicIDs()
	candidate := webmcp.BrowserCandidate{ID: `browser-direct-cancel`, Loopback: true}
	targetOptions := []testkit.ScriptedTargetSessionOption{
		testkit.WithContext(webmcp.PageContext{CatalogReady: true, CatalogEvidence: `test_fixture`}),
		testkit.WithInitialCatalog(pageTool(`write_state`, `frame-1`, `{}`)),
		testkit.WithCancellationResponse(emitCancellationResponse),
	}
	if len(cancelErrors) > 0 && cancelErrors[0] != nil {
		targetOptions = append(targetOptions, testkit.WithCancelError(cancelErrors[0]))
	}
	runtime := testkit.NewScriptedBrowserRuntimeWithOptions(
		testkit.RuntimeOptions{Clock: clock, IDs: ids},
		testkit.BrowserConfig{
			Candidate: candidate,
			Targets: []testkit.TargetConfig{testkit.NewTargetConfig(
				webmcp.Target{BrowserID: candidate.ID, ID: `tab-a`, Type: `page`},
				targetOptions...,
			)},
		},
	)
	t.Cleanup(func() { _ = runtime.Close() })

	original := webmcp.NewBroker(webmcp.BrokerOptions{
		Runtime:           runtime,
		Discoverer:        staticDiscoverer{candidate},
		IDs:               ids,
		Clock:             clock,
		InvocationTimeout: 30 * time.Second,
	})
	t.Cleanup(func() { _ = original.Close() })
	if _, err := original.Select(context.Background(), webmcp.TargetSelector{BrowserID: candidate.ID, TargetID: `tab-a`}); err != nil {
		t.Fatalf(`original select: %v`, err)
	}
	snapshot, err := original.ListTools(context.Background(), webmcp.ListToolsOptions{IncludeSchemas: true})
	if err != nil {
		t.Fatalf(`original tools: %v`, err)
	}
	if len(snapshot.Tools) != 1 {
		t.Fatalf(`original tools = %#v, want one`, snapshot.Tools)
	}
	handleValue, err := runtime.Open(context.Background(), candidate)
	if err != nil {
		t.Fatalf(`open fixture handle: %v`, err)
	}
	session := handleValue.(*testkit.ScriptedBrowserHandle).TargetSession(`tab-a`)
	if session == nil {
		t.Fatal(`fixture session is nil`)
	}

	fresh := webmcp.NewBroker(webmcp.BrokerOptions{
		Runtime:           runtime,
		Discoverer:        staticDiscoverer{candidate},
		IDs:               testkit.NewDeterministicIDs(),
		Clock:             clock,
		InvocationTimeout: 30 * time.Second,
	})
	t.Cleanup(func() { _ = fresh.Close() })
	if _, err := fresh.Select(context.Background(), webmcp.TargetSelector{BrowserID: candidate.ID, TargetID: `tab-a`}); err != nil {
		t.Fatalf(`fresh select: %v`, err)
	}
	return original, fresh, runtime, session, snapshot.Tools[0].Ref
}

func startDirectCancellation(t *testing.T, runtime *testkit.ScriptedBrowserRuntime, broker *webmcp.StatefulBroker, invocationID webmcp.InvocationID, ctx context.Context) <-chan error {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		done <- broker.CancelDirect(ctx, webmcp.DirectCancelRequest{
			Target:       webmcp.TargetSelector{BrowserID: `browser-direct-cancel`, TargetID: `tab-a`},
			InvocationID: invocationID,
		})
	}()
	waitContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := runtime.WaitForOperation(waitContext, testkit.OperationCancel); err != nil {
		t.Fatalf(`wait for direct cancel dispatch: %v`, err)
	}
	return done
}

func requireDirectCancellationError(t *testing.T, err error, code webmcp.ErrorCode) *webmcp.ClassifiedError {
	t.Helper()
	if err == nil {
		t.Fatalf(`direct cancellation unexpectedly succeeded`)
	}
	var classified *webmcp.ClassifiedError
	if !errors.As(err, &classified) || classified == nil || classified.Code != code {
		t.Fatalf(`direct cancellation error = %v, want %s`, err, code)
	}
	return classified
}
