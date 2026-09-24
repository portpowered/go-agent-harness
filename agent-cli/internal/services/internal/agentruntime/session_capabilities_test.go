package agentruntime

import (
	"context"
	"errors"
	"io"
	"reflect"
	"testing"
	"time"

	sessioncontract "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimedevices "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	duration "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
	durationwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration/wire"
	sessionterminalwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionterminal/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
	sessiontracewire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace/wire"
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
		loop: sessionLoopOptions{audioService: newTestAudioIOService()},
		capabilityCoordinator: NewSessionCapabilityCoordinator(func() error {
			closeCalls++
			return nil
		}),
		rtcDeviceRequest: runtimedevices.RTCBindingRequest{
			InputPresent: true,
		},
	}

	err := runSessionPlanWithDuration(context.Background(), io.Discard, plan, 1, nil, nil)
	if err == nil {
		t.Fatal("duration preflight unexpectedly succeeded")
	}
	if closeCalls != 1 {
		t.Fatalf("duration preflight cleanup calls = %d, want one", closeCalls)
	}
}

type sessionFinalizerRuntimeProbe struct {
	close func() error
}

func (p *sessionFinalizerRuntimeProbe) Start(context.Context) (SessionRTCDataPlane, error) {
	return nil, nil
}

func (p *sessionFinalizerRuntimeProbe) Close() error {
	if p == nil || p.close == nil {
		return nil
	}
	return p.close()
}

func TestSessionRuntimeFinalizerRunsOrderedStagesOnceAndJoinsFailures(t *testing.T) {
	primaryErr := errors.New("session loop failed")
	capabilityErr := errors.New("browser cleanup failed")
	providerErr := errors.New("provider close failed")
	runtimeErr := errors.New("runtime close failed")
	captureErr := errors.New("capture flush failed")
	finalizeErr := errors.New("capture finalization failed")
	var order []string
	capabilityCalls := 0
	flushCalls := 0
	finalizeCalls := 0

	plan := sessionRuntimePlan{
		mode:        sessionRuntimeModeRecordGrok,
		capturePath: "capture.json",
		capabilityCoordinator: NewSessionCapabilityCoordinator(func() error {
			capabilityCalls++
			order = append(order, "capability")
			return capabilityErr
		}),
		closeSession: func() error {
			order = append(order, "provider")
			return providerErr
		},
		rtcRuntime: &sessionFinalizerRuntimeProbe{close: func() error {
			order = append(order, "runtime")
			return runtimeErr
		}},
		flushCapture: func() error {
			flushCalls++
			order = append(order, "capture")
			return captureErr
		},
		finalize: func(context.Context, io.Writer) error {
			finalizeCalls++
			order = append(order, "finalize")
			return finalizeErr
		},
	}

	finalizer := plan.newFinalizer(sessionterminalwire.NewReporter())
	gotErr := finalizer.Finish(context.Background(), io.Discard, primaryErr)
	for _, wantErr := range []error{primaryErr, capabilityErr, providerErr, runtimeErr, captureErr, finalizeErr} {
		if !errors.Is(gotErr, wantErr) {
			t.Fatalf("finalizer error = %v, want errors.Is(..., %v)", gotErr, wantErr)
		}
	}
	if want := []string{"capability", "provider", "runtime", "capture", "finalize"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("finalization order = %#v, want %#v", order, want)
	}
	if capabilityCalls != 1 || flushCalls != 1 || finalizeCalls != 1 {
		t.Fatalf("finalization calls = capability:%d capture:%d finalize:%d, want one each", capabilityCalls, flushCalls, finalizeCalls)
	}

	secondErr := finalizer.Finish(context.Background(), io.Discard, nil)
	for _, wantErr := range []error{capabilityErr, providerErr, runtimeErr, captureErr, finalizeErr} {
		if !errors.Is(secondErr, wantErr) {
			t.Fatalf("second finalizer error = %v, want errors.Is(..., %v)", secondErr, wantErr)
		}
	}
	if !reflect.DeepEqual(order, []string{"capability", "provider", "runtime", "capture", "finalize"}) {
		t.Fatalf("second finalization performed new work: %#v", order)
	}
}

func TestSessionRuntimeFinalizerContinuesAfterCleanupPanic(t *testing.T) {
	primaryErr := errors.New("session loop failed")
	var order []string
	plan := sessionRuntimePlan{
		closeSession: func() error {
			order = append(order, "provider")
			panic("provider cleanup panic")
		},
		rtcRuntime: &sessionFinalizerRuntimeProbe{close: func() error {
			order = append(order, "runtime")
			return nil
		}},
		flushCapture: func() error {
			order = append(order, "capture")
			return nil
		},
		finalize: func(context.Context, io.Writer) error {
			order = append(order, "finalize")
			return nil
		},
	}

	gotErr := plan.newFinalizer(sessionterminalwire.NewReporter()).Finish(context.Background(), io.Discard, primaryErr)
	if !errors.Is(gotErr, primaryErr) || !errors.Is(gotErr, duration.ErrFinalizationPanic) {
		t.Fatalf("panic finalization error = %v, want primary and panic identities", gotErr)
	}
	if want := []string{"provider", "runtime", "capture", "finalize"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("panic finalization order = %#v, want %#v", order, want)
	}
}

type sessionFinalizerFailingWriter struct{ err error }

func (w sessionFinalizerFailingWriter) Write([]byte) (int, error) { return 0, w.err }

func TestSessionRuntimePlanFinalizesAfterAnnouncementOutputFailure(t *testing.T) {
	primaryErr := errors.New("announcement output failed")
	capabilityCalls := 0
	flushCalls := 0
	finalizeCalls := 0
	plan := sessionRuntimePlan{
		mode:        sessionRuntimeModeRecordGrok,
		announce:    "starting session",
		capturePath: "capture.json",
		capabilityCoordinator: NewSessionCapabilityCoordinator(func() error {
			capabilityCalls++
			return nil
		}),
		flushCapture: func() error {
			flushCalls++
			return nil
		},
		finalize: func(context.Context, io.Writer) error {
			finalizeCalls++
			return nil
		},
	}

	gotErr := plan.run(context.Background(), sessionFinalizerFailingWriter{err: primaryErr})
	if !errors.Is(gotErr, primaryErr) {
		t.Fatalf("announcement error = %v, want errors.Is(..., %v)", gotErr, primaryErr)
	}
	if capabilityCalls != 1 || flushCalls != 1 || finalizeCalls != 1 {
		t.Fatalf("announcement cleanup calls = capability:%d capture:%d finalize:%d, want one each", capabilityCalls, flushCalls, finalizeCalls)
	}
}

type sessionFinalizerArtifactProbe struct {
	flushErr   error
	closeErr   error
	flushCalls int
	closeCalls int
}

func (*sessionFinalizerArtifactProbe) Accept(messages.StreamMessage) error { return nil }

func (p *sessionFinalizerArtifactProbe) Flush() error {
	p.flushCalls++
	return p.flushErr
}

func (p *sessionFinalizerArtifactProbe) Close() error {
	p.closeCalls++
	return p.closeErr
}

func TestRunSessionDurationPlanUsesCommonFinalizerOnLoopFailure(t *testing.T) {
	primaryErr := errors.New("provider connect failed")
	capabilityErr := errors.New("browser cleanup failed")
	captureErr := errors.New("capture flush failed")
	finalizeErr := errors.New("capture finalization failed")
	artifacts := &sessionFinalizerArtifactProbe{
		flushErr: captureErr,
		closeErr: errors.New("duration artifact close failed"),
	}
	capabilityCalls := 0
	flushCalls := 0
	finalizeCalls := 0
	plan := sessionRuntimePlan{
		mode:        sessionRuntimeModeRecordOpenAI,
		capturePath: "capture.json",
		loop:        sessionLoopOptions{audioService: newTestAudioIOService()},
		inferencer:  &durationTestInferencer{connectErr: primaryErr},
		capabilityCoordinator: NewSessionCapabilityCoordinator(func() error {
			capabilityCalls++
			return capabilityErr
		}),
		flushCapture: func() error {
			flushCalls++
			return captureErr
		},
		finalize: func(context.Context, io.Writer) error {
			finalizeCalls++
			return finalizeErr
		},
	}

	ctx := durationwire.NewService().WithArtifacts(context.Background(), artifacts)
	gotErr := runSessionPlanWithDuration(ctx, io.Discard, plan, time.Hour, &durationTestClock{}, nil)
	for _, wantErr := range []error{primaryErr, capabilityErr, captureErr, finalizeErr, artifacts.closeErr} {
		if !errors.Is(gotErr, wantErr) {
			t.Fatalf("duration finalization error = %v, want errors.Is(..., %v)", gotErr, wantErr)
		}
	}
	if capabilityCalls != 1 || flushCalls != 1 || finalizeCalls != 1 {
		t.Fatalf("duration finalization calls = capability:%d capture:%d finalize:%d, want one each", capabilityCalls, flushCalls, finalizeCalls)
	}
	if artifacts.flushCalls != 1 || artifacts.closeCalls != 1 {
		t.Fatalf("duration artifact calls = flush:%d close:%d, want one each", artifacts.flushCalls, artifacts.closeCalls)
	}
}

// cancellingToolExecutor models a caller cancellation that lands while local
// execution runs, before the provider stream has published TOOL_CALL.END.
type cancellingToolExecutor struct{ cancel context.CancelFunc }

func (e cancellingToolExecutor) Execute(ctx context.Context, _ messages.ToolCall) (messages.ToolCallResponse, error) {
	e.cancel()
	<-ctx.Done()
	return messages.ToolCallResponse{}, ctx.Err()
}

func TestSessionToolExecutorRecordsProviderObligationBeforeLocalExecution(t *testing.T) {
	observer := sessiontracewire.NewObserver(sessiontrace.NewObserverOptions{Provider: "test", Model: "test"})
	observer.SetToolResultsEnabled(true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	executor := newSessionToolExecutorWithTimeoutAndObserverAndCancellationIntent(
		cancellingToolExecutor{cancel: cancel}, time.Minute, composeSessionToolLifecycleObserver(nil, observer, nil), nil,
	)
	//nolint:errcheck // The cancelled execution result is not delivered; the obligation is asserted below.
	executor.Execute(ctx, messages.ToolCall{ID: "call-before-stream", Name: "lookup"})

	err := observer.Finish(ctx.Err())
	var unresolved *sessioncontract.SessionUnresolvedToolResultsError
	if !errors.As(err, &unresolved) || !errors.Is(err, context.Canceled) {
		t.Fatalf("Finish() = %v, want caller cancellation with an unresolved tool result", err)
	}
	if got := unresolved.UnresolvedCallIDs(); len(got) != 1 || got[0] != "call-before-stream" {
		t.Fatalf("unresolved call IDs = %v, want [call-before-stream]", got)
	}
}
