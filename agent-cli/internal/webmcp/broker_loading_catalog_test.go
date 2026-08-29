package webmcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/testkit"
)

func TestStatefulBrokerUsesLoadingAwareFirstCatalogAllowance(t *testing.T) {
	tests := []struct {
		name              string
		documentState     string
		documentLoading   bool
		wantDeadline      int
		advanceBeforeFire time.Duration
		advanceAfterCheck time.Duration
	}{
		{
			name:              "loading",
			documentState:     webmcp.DocumentReadyStateLoading,
			documentLoading:   true,
			wantDeadline:      50,
			advanceBeforeFire: 10 * time.Millisecond,
			advanceAfterCheck: 40 * time.Millisecond,
		},
		{
			name:              "load_complete_without_producer",
			documentState:     webmcp.DocumentReadyStateComplete,
			documentLoading:   false,
			wantDeadline:      10,
			advanceBeforeFire: 10 * time.Millisecond,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clock := testkit.NewFakeClock(0)
			timers := &loadingCatalogTimerFactory{clock: clock, created: make(chan time.Duration, 4)}
			candidate := webmcp.BrowserCandidate{ID: webmcp.BrowserID("browser-loading-" + test.name), Product: "fixture", Loopback: true}
			runtime := testkit.NewScriptedBrowserRuntime(testkit.NewBrowserConfig(candidate,
				testkit.NewTargetConfig(
					webmcp.Target{BrowserID: candidate.ID, ID: "tab-loading", Type: "page", URL: "https://fixture.test/loading"},
					testkit.WithContext(webmcp.PageContext{
						DocumentReadyState:   test.documentState,
						DocumentLoading:      test.documentLoading,
						DocumentLoadingKnown: true,
					}),
				),
			))
			broker := webmcp.NewBroker(webmcp.BrokerOptions{
				Runtime:            runtime,
				Discoverer:         staticDiscoverer{candidate},
				Clock:              clock,
				Timers:             timers,
				CatalogWait:        10 * time.Millisecond,
				LoadingCatalogWait: 50 * time.Millisecond,
			})
			defer func() { _ = broker.Close() }()

			selectContext, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			result := make(chan error, 1)
			go func() {
				_, err := broker.Select(selectContext, webmcp.TargetSelector{BrowserID: candidate.ID, TargetID: "tab-loading"})
				result <- err
			}()
			if _, err := runtime.WaitForOperationAdmitted(selectContext, testkit.OperationEnableAcknowledged); err != nil {
				t.Fatalf("wait for enable acknowledgement: %v", err)
			}
			select {
			case wait := <-timers.created:
				if wait != time.Duration(test.wantDeadline)*time.Millisecond {
					t.Fatalf("catalog timer duration = %s, want %d ms", wait, test.wantDeadline)
				}
			case <-selectContext.Done():
				t.Fatalf("wait for catalog timer creation: %v", selectContext.Err())
			}

			clock.Advance(test.advanceBeforeFire)
			if test.advanceAfterCheck > 0 {
				select {
				case err := <-result:
					t.Fatalf("select returned before loading-aware deadline: %v", err)
				default:
				}
				clock.Advance(test.advanceAfterCheck)
			}

			var selectErr error
			select {
			case selectErr = <-result:
			case <-selectContext.Done():
				t.Fatalf("wait for loading-aware select result: %v", selectContext.Err())
			}
			if selectErr == nil {
				t.Fatal("select succeeded without catalog evidence")
			}
			var classified *webmcp.ClassifiedError
			if !errors.As(selectErr, &classified) {
				t.Fatalf("select error = %T %v, want classified catalog error", selectErr, selectErr)
			}
			if classified.Code != webmcp.ErrorBrowserProtocol || !classified.Retryable {
				t.Fatalf("select error = %+v, want retryable browser protocol error", classified)
			}
			if classified.Details["reason_code"] != "page_tools_unverified" || classified.Details["reason"] != "deadline_exceeded" {
				t.Fatalf("select details = %#v, want page_tools_unverified/deadline_exceeded", classified.Details)
			}
			if classified.Details["deadline_ms"] != test.wantDeadline {
				t.Fatalf("select deadline details = %#v, want %d ms", classified.Details["deadline_ms"], test.wantDeadline)
			}

			selected, err := broker.Selected(context.Background())
			if err != nil {
				t.Fatalf("selected after catalog deadline: %v", err)
			}
			if !selected.Connected || selected.Ready || selected.CatalogReady || selected.Generation != 1 {
				t.Fatalf("selected after catalog deadline = %+v, want connected unready generation one", selected)
			}
			if selected.DocumentReadyState != test.documentState || selected.DocumentLoading != test.documentLoading || !selected.DocumentLoadingKnown {
				t.Fatalf("selected document readiness = %+v, want explicit %s loading=%t", selected, test.documentState, test.documentLoading)
			}

			if test.documentLoading {
				session := runtime.Browser(candidate.ID).TargetSession("tab-loading")
				if session == nil {
					t.Fatal("runtime did not retain loading target session")
				}
				lateTool := webmcp.ToolDescriptor{
					Name:        "late_loading_tool",
					FrameID:     "frame-loading",
					Description: "late loading fixture tool",
					InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
				}
				session.SetAutoResponse("Completed", json.RawMessage(`{"registered":true}`))
				if err := session.EmitToolsAdded(lateTool); err != nil {
					t.Fatalf("emit late loading catalog: %v", err)
				}
				listContext, listCancel := context.WithTimeout(context.Background(), time.Second)
				defer listCancel()
				snapshot, err := broker.ListTools(listContext, webmcp.ListToolsOptions{IncludeSchemas: true})
				if err != nil {
					t.Fatalf("list late loading catalog: %v", err)
				}
				if snapshot.Generation != 1 || len(snapshot.Tools) != 1 || snapshot.Tools[0].Name != lateTool.Name {
					t.Fatalf("late loading catalog = %+v, want one generation-one tool", snapshot)
				}
				invocation, err := broker.Invoke(context.Background(), webmcp.InvokeRequest{ToolRef: snapshot.Tools[0].Ref, Input: json.RawMessage(`{}`)})
				if err != nil {
					t.Fatalf("invoke late loading tool: %v", err)
				}
				terminal, err := broker.WaitInvocation(context.Background(), invocation.InvocationID)
				if err != nil {
					t.Fatalf("wait late loading invocation: %v", err)
				}
				if terminal.State != webmcp.InvocationCompleted || string(terminal.Output) != `{"registered":true}` {
					t.Fatalf("late loading invocation = %+v, want completed response", terminal)
				}
				operations := runtime.Operations()
				attachCount := 0
				for _, operation := range operations {
					if operation.Kind == testkit.OperationAttach {
						attachCount++
					}
				}
				if attachCount != 1 {
					t.Fatalf("attach operation count = %d, want one same-session attachment", attachCount)
				}
			}
		})
	}
}

type loadingCatalogTimerFactory struct {
	clock   *testkit.FakeClock
	created chan time.Duration
}

func (f *loadingCatalogTimerFactory) NewTimer(duration time.Duration) webmcp.Timer {
	timer := f.clock.NewTimer(duration)
	f.created <- duration
	return timer
}
