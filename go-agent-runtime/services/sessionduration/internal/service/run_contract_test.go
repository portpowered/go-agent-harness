package service

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
)

func TestRunOwnsLoopExecutionAndBoundedCleanup(t *testing.T) {
	loop := newRunLoopProbe()
	var drained, closed bool
	err := New().Run(sessionduration.RunRequest{
		Context:    context.Background(),
		Inferencer: contractInferencer{session: newContractSession()},
		Clock:      testNoopScheduler{},
		LoopFactory: func(context.Context, sessionduration.AdmissionInferencer, sessionduration.Controller) (sessionduration.Loop, error) {
			return loop, nil
		},
		Drain: func(context.Context) error {
			drained = true
			return nil
		},
		Close: func() error {
			closed = true
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	select {
	case <-loop.ran:
	case <-time.After(time.Second):
		t.Fatal("Run did not start the loop")
	}
	if !drained || !closed {
		t.Fatalf("cleanup callbacks drained=%v closed=%v", drained, closed)
	}
}

func TestRunSelectsPlannedStopAfterAdmittedMessageEnd(t *testing.T) {
	loop := &gatedRunLoopProbe{deltas: messages.NewTypedBuffer[messages.StreamMessage](1)}
	var published []messages.StreamMessage
	err := New().Run(sessionduration.RunRequest{
		Context:    context.Background(),
		Inferencer: contractInferencer{session: newContractSession()},
		LoopFactory: func(context.Context, sessionduration.AdmissionInferencer, sessionduration.Controller) (sessionduration.Loop, error) {
			return loop, nil
		},
		Publication: sessionduration.Publication{Write: func(msg messages.StreamMessage) error {
			published = append(published, msg)
			return nil
		}},
		Drain: func(context.Context) error { return nil },
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(published) != 1 || published[0].Type != messages.StreamTypeMessageEnd {
		t.Fatalf("published messages = %+v, want one admitted message end", published)
	}
	if !loop.sent {
		t.Fatal("planned stop did not send the loop close control message")
	}
}

func TestRunUsesCallerAdmissionWithoutCreatingAnotherBoundary(t *testing.T) {
	service := New()
	provided := service.NewAdmissionInferencer(
		contractInferencer{session: newContractSession()},
		service.NewEventAdmission(),
		nil,
	)
	done := make(chan struct{})
	close(done)
	factoryCalled := false
	err := service.Run(sessionduration.RunRequest{
		Context:   context.Background(),
		Admission: provided,
		Done:      done,
		LoopFactory: func(ctx context.Context, admitted sessionduration.AdmissionInferencer, _ sessionduration.Controller) (sessionduration.Loop, error) {
			factoryCalled = true
			if admitted != provided {
				return nil, errors.New("duration service replaced the caller's admission boundary")
			}
			if _, err := admitted.ConnectSession(ctx); err != nil {
				return nil, err
			}
			return &idleRunLoopProbe{deltas: messages.NewTypedBuffer[messages.StreamMessage](1)}, nil
		},
	})
	if err != nil {
		t.Fatalf("Run with caller admission: %v", err)
	}
	if !factoryCalled {
		t.Fatal("duration service did not invoke the loop factory")
	}
}

func TestRunCancelsLoopBeforeWaitingWhenDrainCallbackIsMissing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	loop := &contextWaitingRunLoopProbe{
		deltas:  messages.NewTypedBuffer[messages.StreamMessage](1),
		started: make(chan struct{}),
	}
	done := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		//nolint:contextcheck // RunRequest carries the caller context being canceled by this test.
		result <- New().Run(sessionduration.RunRequest{
			Context:    ctx,
			Inferencer: contractInferencer{session: newContractSession()},
			LoopFactory: func(context.Context, sessionduration.AdmissionInferencer, sessionduration.Controller) (sessionduration.Loop, error) {
				return loop, nil
			},
			Done: done,
		})
	}()
	select {
	case <-loop.started:
	case <-time.After(time.Second):
		t.Fatal("session loop did not start")
	}
	close(done)
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(time.Second):
		cancel()
		select {
		case <-result:
		case <-time.After(time.Second):
		}
		t.Fatal("Run waited for the loop before canceling it")
	}
}

func TestControllerDrainsExpiredOutputAndReportsEmptyProviderResponse(t *testing.T) {
	controller, err := New().Begin(sessionduration.Options{Clock: testNoopScheduler{}})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := controller.Expire(); !errors.Is(err, sessionduration.ErrMaxDurationExceeded) {
		t.Fatalf("Expire: %v", err)
	}
	if admission := controller.Observe(messages.StreamMessage{Type: messages.StreamTypeError}); admission.Accepted {
		t.Fatal("terminal error was admitted after expiry")
	}
	if admission := controller.ObserveDrain(messages.StreamMessage{Type: messages.StreamTypeError}); admission.Accepted {
		t.Fatal("terminal error was admitted during post-expiry drain")
	}
	if admission := controller.ObserveDrain(messages.StreamMessage{Type: messages.StreamTypeTextDelta}); !admission.Accepted {
		t.Fatal("expired output was not admitted during bounded drain")
	}
	if admission := controller.ObserveDrain(messages.StreamMessage{Type: messages.StreamTypeSessionClose}); !admission.Accepted || !admission.TerminalSeen {
		t.Fatalf("drained provider terminal admission = %+v", admission)
	}
	if admission := controller.ObserveDrain(messages.StreamMessage{Type: messages.StreamTypeSessionClose}); admission.Accepted {
		t.Fatal("duplicate provider terminal was admitted during drain")
	}
	if _, err := controller.Finalize(context.Background(), sessionduration.FinalizeRequest{}); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if admission := controller.Observe(messages.StreamMessage{Type: messages.StreamTypeTextDelta}); admission.Accepted {
		t.Fatal("closed controller admitted output")
	}
	if admission := controller.ObserveDrain(messages.StreamMessage{Type: messages.StreamTypeTextDelta}); admission.Accepted {
		t.Fatal("closed controller admitted output after finalization")
	}

	liveness, err := New().Begin(sessionduration.Options{
		Clock:    testNoopScheduler{},
		Liveness: sessionduration.LivenessOptions{Enabled: true, Timeout: time.Second},
	})
	if err != nil {
		t.Fatalf("Begin liveness: %v", err)
	}
	liveness.Observe(messages.StreamMessage{Type: messages.StreamTypeMessageStart})
	empty := messages.StreamMessage{
		Type: messages.StreamTypeMessageEnd,
		Value: &messages.MessageEndValue{
			TerminalReason: messages.TerminalReasonPartialOutput,
			OutputState:    messages.TerminalOutputNone,
		},
	}
	admission := liveness.Observe(empty)
	if !errors.Is(admission.LivenessErr, sessionduration.ErrProviderEmptyResponse) {
		t.Fatalf("empty response admission = %+v, want typed liveness failure", admission)
	}
	if !errors.Is(liveness.LivenessFailure(), sessionduration.ErrProviderEmptyResponse) {
		t.Fatalf("LivenessFailure() = %v, want empty-response identity", liveness.LivenessFailure())
	}
	if _, err := liveness.Finalize(context.Background(), sessionduration.FinalizeRequest{}); err != nil {
		t.Fatalf("Finalize liveness: %v", err)
	}
}

func TestRunRejectsInvalidRequestsBeforeStartingResources(t *testing.T) {
	service := New()
	validInferencer := contractInferencer{session: newContractSession()}
	cases := []struct {
		name    string
		request sessionduration.RunRequest
		want    string
	}{
		{name: "negative duration", request: sessionduration.RunRequest{Inferencer: validInferencer, MaxDuration: -time.Second, LoopFactory: func(context.Context, sessionduration.AdmissionInferencer, sessionduration.Controller) (sessionduration.Loop, error) {
			t.Fatal("loop factory called")
			return nil, nil
		}}, want: "--max-duration"},
		{name: "missing inferencer", request: sessionduration.RunRequest{LoopFactory: func(context.Context, sessionduration.AdmissionInferencer, sessionduration.Controller) (sessionduration.Loop, error) {
			return newRunLoopProbe(), nil
		}}, want: "inferencer is required"},
		{name: "missing loop factory", request: sessionduration.RunRequest{Inferencer: validInferencer}, want: "loop factory is required"},
		{name: "loop factory failure", request: sessionduration.RunRequest{Inferencer: validInferencer, LoopFactory: func(context.Context, sessionduration.AdmissionInferencer, sessionduration.Controller) (sessionduration.Loop, error) {
			return nil, errors.New("factory failed")
		}}, want: "factory failed"},
		{name: "invalid loop", request: sessionduration.RunRequest{Inferencer: validInferencer, LoopFactory: func(context.Context, sessionduration.AdmissionInferencer, sessionduration.Controller) (sessionduration.Loop, error) {
			return newRunLoopProbeWithNilDeltas(), nil
		}}, want: "invalid loop"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			err := service.Run(test.request)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Run() = %v, want %q", err, test.want)
			}
		})
	}
}

func TestExecuteValidatesBeforeEffectsAndFinalizesAfterInvocation(t *testing.T) {
	service := New()
	var invalidEffectCalled bool
	invalidErr := service.Execute(sessionduration.ExecutionRequest{
		MaxDuration: -time.Second,
		Prepare: func(context.Context, io.Writer) error {
			invalidEffectCalled = true
			return nil
		},
		Run: func(context.Context, io.Writer, sessionduration.TimerScheduler) error {
			invalidEffectCalled = true
			return nil
		},
		Finalization: sessionduration.FinalizationPorts{
			PublishTerminal: func(io.Writer, error) error {
				invalidEffectCalled = true
				return nil
			},
		},
	})
	if invalidErr == nil || invalidEffectCalled {
		t.Fatalf("invalid execution error=%v effects-called=%v; want validation before effects", invalidErr, invalidEffectCalled)
	}

	runFailure := errors.New("provider invocation failed")
	var order []string
	err := service.Execute(sessionduration.ExecutionRequest{
		MaxDuration: 0,
		Prepare: func(context.Context, io.Writer) error {
			order = append(order, "prepare")
			return nil
		},
		Run: func(_ context.Context, _ io.Writer, selected sessionduration.TimerScheduler) error {
			order = append(order, "run")
			if selected == nil {
				t.Fatal("Execute did not select the supplied fallback clock")
			}
			return runFailure
		},
		FallbackClock: testNoopScheduler{},
		Finalization: sessionduration.FinalizationPorts{
			CloseSession: func() error {
				order = append(order, "close")
				return nil
			},
			PublishTerminal: func(_ io.Writer, runErr error) error {
				order = append(order, "publish")
				if !errors.Is(runErr, runFailure) {
					t.Fatalf("terminal publication lost invocation error: %v", runErr)
				}
				return nil
			},
		},
	})
	if !errors.Is(err, runFailure) {
		t.Fatalf("Execute() = %v, want invocation error identity", err)
	}
	if len(order) < 4 || order[0] != "prepare" || order[1] != "run" || order[len(order)-2] != "close" || order[len(order)-1] != "publish" {
		t.Fatalf("execution/finalization order = %v, want prepare, run, close, publish", order)
	}
}

func TestExecuteReportsArtifactFailureBeforeTerminalAndSkipsReplayCompletion(t *testing.T) {
	runErr := errors.New("provider invocation failed")
	flushErr := errors.New("artifact flush failed")
	closeErr := errors.New("artifact close failed")
	terminalErr := errors.New("terminal publication failed")
	var order []string
	replayCompleted := false
	artifactRecorded := false
	err := New().Execute(sessionduration.ExecutionRequest{
		Context:       context.Background(),
		MaxDuration:   0,
		FallbackClock: testNoopScheduler{},
		Prepare: func(context.Context, io.Writer) error {
			order = append(order, "prepare")
			return nil
		},
		Run: func(context.Context, io.Writer, sessionduration.TimerScheduler) error {
			order = append(order, "run")
			return runErr
		},
		Finalization: sessionduration.FinalizationPorts{
			Artifacts: artifactLifecycleFunc{
				flush: func() error { order = append(order, "flush"); return flushErr },
				close: func() error { order = append(order, "close"); return closeErr },
			},
			RecordArtifactFinalization: func(hasArtifacts bool, artifactErr error) {
				order = append(order, "record")
				artifactRecorded = hasArtifacts && errors.Is(artifactErr, flushErr) && errors.Is(artifactErr, closeErr)
			},
			HasIndependentFailure: func(failure error) bool {
				order = append(order, "failure-check")
				return errors.Is(failure, runErr) && errors.Is(failure, flushErr) && errors.Is(failure, closeErr)
			},
			CompleteReplay: func() { replayCompleted = true },
			PublishTerminal: func(_ io.Writer, failure error) error {
				order = append(order, "publish")
				if !errors.Is(failure, runErr) || !errors.Is(failure, flushErr) || !errors.Is(failure, closeErr) {
					t.Fatalf("terminal failure = %v, want invocation and artifact failures", failure)
				}
				return terminalErr
			},
		},
	})
	for _, cause := range []error{runErr, flushErr, closeErr, terminalErr} {
		if !errors.Is(err, cause) {
			t.Errorf("Execute error %v does not preserve %v", err, cause)
		}
	}
	if !artifactRecorded || replayCompleted {
		t.Fatalf("artifact recorded=%v replay completed=%v, want artifact failure recorded and replay skipped", artifactRecorded, replayCompleted)
	}
	if want := []string{"prepare", "run", "flush", "close", "record", "failure-check", "publish"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("execution/finalization order = %v, want %v", order, want)
	}
}

func TestExecuteRejectsPartialArtifactConfigurationBeforeInvocation(t *testing.T) {
	service := New()
	var invocationCalled bool
	var artifactStatusRecorded bool
	var terminalPublished bool
	err := service.Execute(sessionduration.ExecutionRequest{
		MaxDuration: 0,
		Context: service.WithArtifactPaths(context.Background(), sessionduration.SessionDurationArtifactPaths{
			AudioPath: "audio-only.wav",
		}),
		Prepare: func(context.Context, io.Writer) error {
			invocationCalled = true
			return nil
		},
		Run: func(context.Context, io.Writer, sessionduration.TimerScheduler) error {
			invocationCalled = true
			return nil
		},
		Finalization: sessionduration.FinalizationPorts{
			RecordArtifactFinalization: func(hasArtifacts bool, artifactErr error) {
				artifactStatusRecorded = !hasArtifacts && artifactErr == nil
			},
			PublishTerminal: func(_ io.Writer, runErr error) error {
				terminalPublished = runErr != nil && strings.Contains(runErr.Error(), "both audio and transcript paths")
				return nil
			},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "both audio and transcript paths") {
		t.Fatalf("Execute error = %v, want partial-artifact configuration failure", err)
	}
	if invocationCalled || !artifactStatusRecorded || !terminalPublished {
		t.Fatalf("invocation=%v artifact-status=%v terminal-published=%v", invocationCalled, artifactStatusRecorded, terminalPublished)
	}
}

func newRunLoopProbeWithNilDeltas() *runLoopProbe {
	return &runLoopProbe{ran: make(chan struct{})}
}
