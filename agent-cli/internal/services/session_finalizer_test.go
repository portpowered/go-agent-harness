package services

import (
	"context"
	"errors"
	"io"
	"reflect"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

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

	finalizer := newSessionRuntimeFinalizer(plan)
	gotErr := finalizer.finish(context.Background(), io.Discard, primaryErr)
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

	secondErr := finalizer.finish(context.Background(), io.Discard, nil)
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

	gotErr := newSessionRuntimeFinalizer(plan).finish(context.Background(), io.Discard, primaryErr)
	if !errors.Is(gotErr, primaryErr) || !errors.Is(gotErr, ErrSessionFinalizationPanic) {
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

	ctx := WithSessionDurationArtifacts(context.Background(), artifacts)
	gotErr := runSessionDurationPlan(ctx, io.Discard, plan, 0, nil)
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
