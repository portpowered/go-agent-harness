package webmcp

import (
	"context"
	"testing"
)

func TestWaitForAdmissionDispatchPreservesReportedBrowserIDWhenContextIsCanceled(t *testing.T) {
	for iteration := 0; iteration < 128; iteration++ {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		broker := &StatefulBroker{closedCh: make(chan struct{})}
		invocation := &brokerInvocation{dispatchDone: make(chan invocationDispatch, 1)}
		invocation.dispatchDone <- invocationDispatch{result: InvokeResult{
			InvocationID:        "broker-invocation-1",
			BrowserInvocationID: "browser-invocation-1",
			State:               InvocationDispatched,
		}}

		result, err := broker.waitForAdmissionDispatch(ctx, invocation)
		cancel()
		if err != nil {
			t.Fatalf("iteration %d admission result error = %v", iteration, err)
		}
		if result.InvocationID != "broker-invocation-1" || result.BrowserInvocationID != "browser-invocation-1" || result.State != InvocationDispatched {
			t.Fatalf("iteration %d admission result = %#v, want the reported dispatch", iteration, result)
		}
	}
}
