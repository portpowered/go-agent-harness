package agentruntime

import (
	"context"
	"errors"
	"io"
	"testing"
)

func TestPlanSessionRuntimeClosesTransferredCapabilityOnPlanningFailure(t *testing.T) {
	closeErr := errors.New("capability close failed")
	closeCalls := 0

	_, err := planSessionRuntimeWithFactory(SessionRunOptions{ModelCatalog: testModelCatalog(),
		Transport:       "unsupported",
		CapabilityClose: func() error { closeCalls++; return closeErr },
	}, sessionRuntimeFactory{})
	if err == nil || !errors.Is(err, closeErr) {
		t.Fatalf("planning error = %v, want capability cleanup failure joined", err)
	}
	if closeCalls != 1 {
		t.Fatalf("planning cleanup calls = %d, want one", closeCalls)
	}
}

func TestSessionRuntimePlanClosesTransferredCapabilityOnNormalExit(t *testing.T) {
	closeCalls := 0
	plan := sessionRuntimePlan{
		capabilityCoordinator: NewSessionCapabilityCoordinator(func() error {
			closeCalls++
			return nil
		}),
	}

	if err := plan.run(context.Background(), io.Discard); err != nil {
		t.Fatalf("plan run: %v", err)
	}
	if closeCalls != 1 {
		t.Fatalf("normal-exit cleanup calls = %d, want one", closeCalls)
	}
}

func TestSessionDurationPlanClosesTransferredCapabilityOnPreflightExit(t *testing.T) {
	closeCalls := 0
	plan := sessionRuntimePlan{
		capabilityCoordinator: NewSessionCapabilityCoordinator(func() error {
			closeCalls++
			return nil
		}),
		rtcDeviceRequest: RTCDeviceBindingRequest{
			InputPresent: true,
		},
	}

	err := runSessionDurationPlan(context.Background(), io.Discard, plan, 1, nil)
	if err == nil {
		t.Fatal("duration preflight unexpectedly succeeded")
	}
	if closeCalls != 1 {
		t.Fatalf("duration preflight cleanup calls = %d, want one", closeCalls)
	}
}
