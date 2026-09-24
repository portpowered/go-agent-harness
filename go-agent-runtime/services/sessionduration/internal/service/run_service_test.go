package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
)

func TestRunHandlesWakeAndDoneBoundaryFailures(t *testing.T) {
	wakeErr := errors.New("wake failed")
	doneErr := errors.New("done failed")
	tests := []struct {
		name         string
		wake         <-chan struct{}
		wakeSources  []<-chan struct{}
		done         <-chan struct{}
		doneSources  []<-chan struct{}
		errorSources func() []<-chan error
		onWake       func(context.Context, sessionduration.Loop) (sessionduration.WakeResult, error)
		doneError    func() error
		want         error
	}{
		{
			name: "wake",
			wake: func() <-chan struct{} {
				wake := make(chan struct{}, 1)
				wake <- struct{}{}
				return wake
			}(),
			onWake: func(context.Context, sessionduration.Loop) (sessionduration.WakeResult, error) {
				return sessionduration.WakeResult{}, wakeErr
			},
			want: wakeErr,
		},
		{
			name: "wake source",
			wakeSources: []<-chan struct{}{func() <-chan struct{} {
				wake := make(chan struct{}, 1)
				wake <- struct{}{}
				return wake
			}()},
			onWake: func(context.Context, sessionduration.Loop) (sessionduration.WakeResult, error) {
				return sessionduration.WakeResult{}, wakeErr
			},
			want: wakeErr,
		},
		{
			name: "done",
			done: func() <-chan struct{} {
				done := make(chan struct{})
				close(done)
				return done
			}(),
			doneError: func() error { return doneErr },
			want:      doneErr,
		},
		{
			name: "done source",
			doneSources: []<-chan struct{}{func() <-chan struct{} {
				done := make(chan struct{})
				close(done)
				return done
			}()},
			doneError: func() error { return doneErr },
			want:      doneErr,
		},
		{
			name: "external error source",
			errorSources: func() []<-chan error {
				errors := make(chan error, 1)
				errors <- wakeErr
				return []<-chan error{errors}
			},
			want: wakeErr,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := sessionduration.RunRequest{
				Context:    context.Background(),
				Inferencer: contractInferencer{session: newContractSession()},
				LoopFactory: func(context.Context, sessionduration.AdmissionInferencer, sessionduration.Controller) (sessionduration.Loop, error) {
					return &idleRunLoopProbe{deltas: messages.NewTypedBuffer[messages.StreamMessage](1)}, nil
				},
				Wake:                 test.wake,
				WakeSources:          test.wakeSources,
				Effects:              sessionduration.RunEffects{OnWake: test.onWake},
				Done:                 test.done,
				DoneSources:          test.doneSources,
				ExternalErrorSources: test.errorSources,
				DoneError:            test.doneError,
				Drain: func(context.Context) error {
					return nil
				},
			}
			if err := New().Run(request); !errors.Is(err, test.want) {
				t.Fatalf("Run() = %v, want %v", err, test.want)
			}
		})
	}
}

func TestRunSendsConfiguredPromptOnceWhenSessionOpens(t *testing.T) {
	loop := &promptSessionOpenLoop{deltas: messages.NewTypedBuffer[messages.StreamMessage](2), sent: make(chan []messages.Message, 1)}
	done := make(chan struct{})
	userInput := make(chan string, 1)
	runDone := make(chan error, 1)
	go func() {
		runDone <- New().Run(sessionduration.RunRequest{
			Context:    context.Background(),
			Inferencer: contractInferencer{session: newContractSession()},
			Clock:      testNoopScheduler{},
			LoopFactory: func(context.Context, sessionduration.AdmissionInferencer, sessionduration.Controller) (sessionduration.Loop, error) {
				return loop, nil
			},
			Done: done,
			Policy: sessionduration.RunPolicy{
				Prompt:         "start with the user request",
				PromptProvided: true,
			},
			Effects: sessionduration.RunEffects{NoteUserTextInput: func(text string) { userInput <- text }},
		})
	}()
	select {
	case sent := <-loop.sent:
		if len(sent) != 1 || sent[0].Role != messages.RoleUser || sent[0].TextContent() != "start with the user request" {
			t.Fatalf("first message after session-open = %+v, want one configured user prompt", sent)
		}
	case <-time.After(time.Second):
		t.Fatal("session-open did not dispatch the configured prompt")
	}
	select {
	case got := <-userInput:
		if got != "start with the user request" {
			t.Fatalf("recorded user input = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("session-open prompt was not reported to the input observer")
	}
	close(done)
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Run after host completion: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not finish after the host completion signal")
	}
}

func TestRunKeepsArtifactWriteFailureAndJoinsAllArtifactCleanupCauses(t *testing.T) {
	acceptErr := errors.New("artifact write failed")
	flushErr := errors.New("artifact flush failed")
	closeErr := errors.New("artifact close failed")
	artifacts := &artifactFailureLifecycle{acceptErr: acceptErr, flushErr: flushErr, closeErr: closeErr}
	loop := &artifactFailureRunLoop{deltas: messages.NewTypedBuffer[messages.StreamMessage](1)}
	published := 0
	err := New().Run(sessionduration.RunRequest{
		Context:    context.Background(),
		Inferencer: contractInferencer{session: newContractSession()},
		Clock:      testNoopScheduler{},
		Artifacts:  artifacts,
		Publication: sessionduration.Publication{
			Artifacts: artifacts,
			Write:     func(messages.StreamMessage) error { published++; return nil },
		},
		LoopFactory: func(context.Context, sessionduration.AdmissionInferencer, sessionduration.Controller) (sessionduration.Loop, error) {
			return loop, nil
		},
	})
	for _, cause := range []error{acceptErr, flushErr, closeErr} {
		if !errors.Is(err, cause) {
			t.Errorf("Run error %v does not preserve %v", err, cause)
		}
	}
	if artifacts.accepts != 1 || artifacts.flushes != 1 || artifacts.closes != 1 || published != 0 {
		t.Fatalf("artifact/publish lifecycle accepts=%d flushes=%d closes=%d published=%d", artifacts.accepts, artifacts.flushes, artifacts.closes, published)
	}
}

func TestRunOwnsRateLimitRetryWaitAndDispatch(t *testing.T) {
	scheduler := &triggerScheduler{created: make(chan *triggerTimer, 1)}
	loop := &retryRunLoopProbe{
		deltas: messages.NewTypedBuffer[messages.StreamMessage](1),
		sent:   make(chan messages.StreamMessage, 1),
	}
	dispatched := make(chan messages.StreamMessage, 1)
	done := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- New().Run(sessionduration.RunRequest{
			Context:    context.Background(),
			Inferencer: contractInferencer{session: newContractSession()},
			Clock:      scheduler,
			Retry:      sessionduration.RetryPolicy{Enabled: true, MaxRetries: 1, DefaultDelay: time.Second},
			LoopFactory: func(context.Context, sessionduration.AdmissionInferencer, sessionduration.Controller) (sessionduration.Loop, error) {
				return loop, nil
			},
			RetryDispatched: func(msg messages.StreamMessage) { dispatched <- msg },
			Done:            done,
		})
	}()

	var timer *triggerTimer
	select {
	case timer = <-scheduler.created:
	case <-time.After(time.Second):
		t.Fatal("retry scheduler was not created")
	}
	select {
	case msg := <-loop.sent:
		t.Fatalf("retry was sent before its delay elapsed: %+v", msg)
	default:
	}
	timer.events <- time.Now()

	select {
	case msg := <-loop.sent:
		if msg.Type != messages.StreamTypeResponseCreate {
			t.Fatalf("retry control type = %q, want response.create", msg.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("retry control was not sent after the injected timer fired")
	}
	select {
	case msg := <-dispatched:
		if msg.Type != messages.StreamTypeResponseCreate {
			t.Fatalf("observed retry type = %q, want response.create", msg.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("successful retry dispatch was not reported")
	}

	close(done)
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("Run after retry completion = %v, want nil", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not stop after its completion signal")
	}
}

func TestRunRejectsRetryWhenLoopCannotSendSessionEvents(t *testing.T) {
	loop := &retryRunLoopWithoutSessionEvents{deltas: messages.NewTypedBuffer[messages.StreamMessage](1)}
	err := New().Run(sessionduration.RunRequest{
		Context:    context.Background(),
		Inferencer: contractInferencer{session: newContractSession()},
		Retry:      sessionduration.RetryPolicy{Enabled: true, MaxRetries: 1},
		LoopFactory: func(context.Context, sessionduration.AdmissionInferencer, sessionduration.Controller) (sessionduration.Loop, error) {
			return loop, nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "does not support provider session events") {
		t.Fatalf("Run() = %v, want the loop transport capability error", err)
	}
}

func TestRunExpiresAtMaxDurationAndClosesLoop(t *testing.T) {
	scheduler := &triggerScheduler{created: make(chan *triggerTimer, 1)}
	type runResult struct {
		snapshot sessionduration.Result
		err      error
	}
	result := make(chan runResult, 1)
	go func() {
		snapshot, err := New().RunWithResult(sessionduration.RunRequest{
			Context:     context.Background(),
			Inferencer:  contractInferencer{session: newContractSession()},
			Clock:       scheduler,
			MaxDuration: time.Second,
			LoopFactory: func(context.Context, sessionduration.AdmissionInferencer, sessionduration.Controller) (sessionduration.Loop, error) {
				return &idleRunLoopProbe{deltas: messages.NewTypedBuffer[messages.StreamMessage](1)}, nil
			},
			Drain: func(context.Context) error {
				return nil
			},
		})
		result <- runResult{snapshot: snapshot, err: err}
	}()
	var timer *triggerTimer
	select {
	case timer = <-scheduler.created:
	case <-time.After(time.Second):
		t.Fatal("max-duration timer was not created")
	}
	timer.events <- time.Now()
	select {
	case got := <-result:
		if got.err != nil {
			t.Fatalf("RunWithResult after max duration = %v, want bounded clean stop", got.err)
		}
		if !got.snapshot.Expired {
			t.Fatalf("RunWithResult snapshot = %+v, want service-owned expiration", got.snapshot)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not finish after max duration")
	}
}

func TestRunFinalizesProviderPlaybackAfterHostResourcesClose(t *testing.T) {
	var order []string
	inner := &orderedPlaybackSession{contractSession: newContractSession(), order: &order}
	done := make(chan struct{})
	close(done)
	err := New().Run(sessionduration.RunRequest{
		Context:    context.Background(),
		Inferencer: orderedPlaybackInferencer{session: inner},
		LoopFactory: func(ctx context.Context, admitted sessionduration.AdmissionInferencer, _ sessionduration.Controller) (sessionduration.Loop, error) {
			if _, err := admitted.ConnectSession(ctx); err != nil {
				return nil, err
			}
			return &idleRunLoopProbe{deltas: messages.NewTypedBuffer[messages.StreamMessage](1)}, nil
		},
		Done: done,
		Close: func() error {
			order = append(order, "host")
			return nil
		},
		CloseTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if want := []string{"host", "playback", "provider"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("shutdown order = %v, want %v", order, want)
	}
}

func TestRunBoundsProviderCloseAndPreservesTimeoutCause(t *testing.T) {
	release := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	session := &blockedProviderCloseSession{
		contractSession: newContractSession(),
		started:         make(chan struct{}),
		release:         release,
		finished:        make(chan struct{}),
	}
	done := make(chan struct{})
	close(done)
	result := make(chan error, 1)
	go func() {
		result <- New().Run(sessionduration.RunRequest{
			Context:    context.Background(),
			Inferencer: orderedPlaybackInferencer{session: session},
			LoopFactory: func(ctx context.Context, admitted sessionduration.AdmissionInferencer, _ sessionduration.Controller) (sessionduration.Loop, error) {
				if _, err := admitted.ConnectSession(ctx); err != nil {
					return nil, err
				}
				return &idleRunLoopProbe{deltas: messages.NewTypedBuffer[messages.StreamMessage](1)}, nil
			},
			Done:         done,
			CloseTimeout: 25 * time.Millisecond,
		})
	}()
	select {
	case <-session.started:
	case <-time.After(time.Second):
		t.Fatal("provider close did not start")
	}
	select {
	case err := <-result:
		if !errors.Is(err, sessionduration.ErrSessionCloseTimeout) {
			t.Fatalf("Run() = %v, want bounded provider-close timeout identity", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not return after the provider close deadline")
	}
	close(release)
	released = true
	select {
	case <-session.finished:
	case <-time.After(time.Second):
		t.Fatal("provider close did not finish after the test released it")
	}
}

func TestRunRetainsProviderTerminalPublishedDuringDurationExpiryClose(t *testing.T) {
	inner := &providerTerminalOnCloseSession{
		contractSession: newContractSession(),
		terminal: messages.StreamMessage{Type: messages.StreamTypeSessionClose, Value: messages.NewSessionCloseValueWithTerminal(
			"session-1", "provider close", "provider_close", messages.TerminalReasonProviderClose,
			messages.TerminalProvenanceProvider, messages.TerminalOutputPartial,
		)},
	}
	var published []messages.StreamMessage
	scheduler := &triggerScheduler{created: make(chan *triggerTimer, 1)}
	type runResult struct {
		result sessionduration.Result
		err    error
	}
	finished := make(chan runResult, 1)
	go func() {
		result, err := New().RunWithResult(sessionduration.RunRequest{
			Context:     context.Background(),
			Inferencer:  orderedPlaybackInferencer{session: inner},
			Clock:       scheduler,
			MaxDuration: time.Second,
			LoopFactory: func(ctx context.Context, admitted sessionduration.AdmissionInferencer, _ sessionduration.Controller) (sessionduration.Loop, error) {
				if _, err := admitted.ConnectSession(ctx); err != nil {
					return nil, err
				}
				return &idleRunLoopProbe{deltas: messages.NewTypedBuffer[messages.StreamMessage](1)}, nil
			},
			CloseTimeout: time.Second,
			Publication: sessionduration.Publication{Write: func(msg messages.StreamMessage) error {
				published = append(published, msg)
				return nil
			}},
		})
		finished <- runResult{result: result, err: err}
	}()
	var timer *triggerTimer
	select {
	case timer = <-scheduler.created:
	case <-time.After(time.Second):
		t.Fatal("max-duration timer was not created")
	}
	timer.events <- time.Now()
	var outcome runResult
	select {
	case outcome = <-finished:
	case <-time.After(time.Second):
		t.Fatal("RunWithResult did not finish after max-duration expiry")
	}
	result := outcome.result
	err := outcome.err
	if err != nil {
		t.Fatalf("RunWithResult: %v", err)
	}
	if !result.Expired || !result.TerminalWritten || result.OutputState != messages.TerminalOutputNone {
		t.Fatalf("shutdown result = %+v, want the provider's partial terminal", result)
	}
	if len(published) != 1 || published[0].Type != messages.StreamTypeSessionClose {
		t.Fatalf("published shutdown events = %+v, want one provider terminal", published)
	}
	value, ok := published[0].Value.(*messages.SessionCloseValue)
	if !ok || value.TerminalProvenance != messages.TerminalProvenanceProvider || value.TerminalReason != messages.TerminalReasonProviderClose {
		t.Fatalf("published terminal = %#v, want provider-close provenance", published[0].Value)
	}
}

func TestRunDispatchesAudioInterruptionAndJoinsItsPump(t *testing.T) {
	source := make(chan audioio.ScheduledAudioInput, 1)
	dispatched := make(chan audioio.ScheduledAudioInput, 1)
	done := make(chan struct{})
	input := audioio.ScheduledAudioInput{PCM: []byte{1, 2, 3}, SourceSampleRate: 24000, EndOfTurn: true}
	source <- input
	result := make(chan error, 1)
	go func() {
		result <- New().Run(sessionduration.RunRequest{
			Context:    context.Background(),
			Inferencer: contractInferencer{session: newContractSession()},
			Done:       done,
			LoopFactory: func(context.Context, sessionduration.AdmissionInferencer, sessionduration.Controller) (sessionduration.Loop, error) {
				return &idleRunLoopProbe{deltas: messages.NewTypedBuffer[messages.StreamMessage](1)}, nil
			},
			AudioInterruptions: sessionduration.AudioInterruptionPort{
				Source: source,
				Dispatch: func(_ context.Context, _ sessionduration.Loop, got audioio.ScheduledAudioInput) error {
					dispatched <- got
					return nil
				},
			},
		})
	}()
	select {
	case got := <-dispatched:
		if got.SourceSampleRate != input.SourceSampleRate || got.EndOfTurn != input.EndOfTurn || string(got.PCM) != string(input.PCM) {
			t.Fatalf("dispatched audio interruption = %+v, want %+v", got, input)
		}
	case <-time.After(time.Second):
		t.Fatal("duration service did not dispatch the queued audio interruption")
	}
	close(done)
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("duration run after cancellation = %v, want clean completion", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("duration run did not join its audio-interruption pump")
	}
}

func TestFinalizerOrdersOwnedCleanupAndIsIdempotent(t *testing.T) {
	var order []string
	step := func(name string) func() error {
		return func() error {
			order = append(order, name)
			return nil
		}
	}
	finalizer := New().NewFinalizer(sessionduration.FinalizationPorts{
		CloseCapabilities: step("capabilities"),
		CloseSession:      step("session"),
		CloseRuntime:      step("runtime"),
		FlushCapture:      step("flush"),
		Finalize: func(_ context.Context, out io.Writer) error {
			if out == nil {
				t.Fatal("finalizer received a nil output writer")
			}
			order = append(order, "finalize")
			return nil
		},
		ReleaseCapture: step("release"),
	})
	finalizer.SetDeviceBinding(step("binding"))
	primary := errors.New("primary")
	var finalizerContext context.Context
	if err := finalizer.Finish(finalizerContext, &bytes.Buffer{}, primary); !errors.Is(err, primary) {
		t.Fatalf("Finish() = %v, want primary identity", err)
	}
	if err := finalizer.Finish(context.Background(), nil, nil); err != nil {
		t.Fatalf("duplicate Finish() = %v, want nil", err)
	}
	want := []string{"capabilities", "session", "binding", "runtime", "flush", "finalize", "release"}
	if len(order) != len(want) {
		t.Fatalf("cleanup calls = %v, want %v", order, want)
	}
	for index := range want {
		if order[index] != want[index] {
			t.Fatalf("cleanup order = %v, want %v", order, want)
		}
	}
}

func TestFinalizerConvertsCleanupPanicToTypedFailure(t *testing.T) {
	finalizer := New().NewFinalizer(sessionduration.FinalizationPorts{
		CloseSession: func() error { panic("provider close panic") },
	})
	err := finalizer.Finish(context.Background(), nil, nil)
	if !errors.Is(err, sessionduration.ErrFinalizationPanic) {
		t.Fatalf("Finish() = %v, want finalization panic identity", err)
	}
}
