package wire

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
)

func TestPublicCancellationDisarmsTimerCreatedConcurrently(t *testing.T) {
	release := make(chan struct{})
	scheduler := &publicGatedScheduler{
		entered: make(chan struct{}), release: release, created: make(chan *publicManualTimer, 2),
	}
	controller, err := NewService().Begin(sessionduration.Options{
		Liveness:      sessionduration.LivenessOptions{Enabled: true, Timeout: time.Millisecond},
		LivenessClock: scheduler,
	})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	t.Cleanup(func() {
		if _, err := controller.Finalize(context.Background(), sessionduration.FinalizeRequest{}); err != nil {
			t.Errorf("Finalize: %v", err)
		}
	})

	observed := make(chan struct{})
	go func() {
		controller.Observe(messages.StreamMessage{Type: messages.StreamTypeResponseCreate, Role: messages.RoleAssistant, ResponseID: "cancelled"})
		close(observed)
	}()
	select {
	case <-scheduler.entered:
	case <-time.After(time.Second):
		t.Fatal("liveness timer creation did not block")
	}
	controller.Observe(messages.StreamMessage{Type: messages.StreamTypeResponseCancel, Role: messages.RoleUser, ResponseID: "cancelled"})
	close(release)
	select {
	case <-observed:
	case <-time.After(time.Second):
		t.Fatal("response observation did not finish after timer creation resumed")
	}

	cancelledTimer := receiveGatedPublicTimer(t, scheduler)
	cancelledTimer.Fire()
	select {
	case err := <-controller.Errors():
		t.Fatalf("response cancelled during timer creation triggered liveness: %v", err)
	case <-time.After(20 * time.Millisecond):
	}

	controller.Observe(messages.StreamMessage{Type: messages.StreamTypeResponseCreate, Role: messages.RoleAssistant, ResponseID: "next"})
	nextTimer := receiveGatedPublicTimer(t, scheduler)
	nextTimer.Fire()
	select {
	case err := <-controller.Errors():
		var typed *sessionduration.LivenessError
		if !errors.Is(err, sessionduration.ErrProviderLivenessTimeout) || !errors.As(err, &typed) || typed.ResponseID != "next" {
			t.Fatalf("next response liveness error = %v, want timeout for next", err)
		}
	case <-time.After(time.Second):
		t.Fatal("new response did not restart liveness after cancellation")
	}
}

func TestPublicParallelLocalToolsKeepProviderWatchdogDisarmed(t *testing.T) {
	scheduler := &publicManualScheduler{created: make(chan *publicManualTimer, 4)}
	controller, err := NewService().Begin(sessionduration.Options{
		Liveness:      sessionduration.LivenessOptions{Enabled: true, Timeout: time.Millisecond},
		LivenessClock: scheduler,
	})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	t.Cleanup(func() {
		if _, err := controller.Finalize(context.Background(), sessionduration.FinalizeRequest{}); err != nil {
			t.Errorf("Finalize: %v", err)
		}
	})

	controller.Observe(messages.StreamMessage{Type: messages.StreamTypeResponseCreate, Role: messages.RoleAssistant, ResponseID: "parallel-tools"})
	_ = receivePublicTimer(t, scheduler)
	controller.BeginLocalToolExecution()
	controller.BeginLocalToolExecution()
	controller.EndLocalToolExecution()
	select {
	case timer := <-scheduler.created:
		timer.Fire()
		t.Fatal("one completed tool rearmed liveness while its sibling ran")
	default:
	}

	controller.EndLocalToolExecution()
	resumedTimer := receivePublicTimer(t, scheduler)
	resumedTimer.Fire()
	select {
	case err := <-controller.Errors():
		if !errors.Is(err, sessionduration.ErrProviderLivenessTimeout) {
			t.Fatalf("resumed liveness error = %v, want timeout", err)
		}
	case <-time.After(time.Second):
		t.Fatal("liveness did not resume after all local tools completed")
	}
}

func TestPublicCancelledContextCannotRearmProviderWatchdog(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	scheduler := &publicManualScheduler{created: make(chan *publicManualTimer, 3)}
	controller, err := NewService().Begin(sessionduration.Options{
		Context:       ctx,
		Liveness:      sessionduration.LivenessOptions{Enabled: true, Timeout: time.Millisecond},
		LivenessClock: scheduler,
	})
	if err != nil {
		cancel()
		t.Fatalf("Begin: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		if _, err := controller.Finalize(context.Background(), sessionduration.FinalizeRequest{}); err != nil {
			t.Errorf("Finalize: %v", err)
		}
	})

	controller.Observe(messages.StreamMessage{Type: messages.StreamTypeResponseCreate, Role: messages.RoleAssistant, ResponseID: "before-cancel"})
	firstTimer := receivePublicTimer(t, scheduler)
	cancel()
	controller.Observe(messages.StreamMessage{Type: messages.StreamTypeResponseCreate, Role: messages.RoleAssistant, ResponseID: "after-cancel"})
	select {
	case <-scheduler.created:
		t.Fatal("cancelled controller created a new liveness timer")
	default:
	}
	firstTimer.Fire()
	select {
	case err := <-controller.Errors():
		t.Fatalf("cancelled controller reported liveness failure: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestPublicRetryNilTimerPreservesUnavailableSchedulerIdentity(t *testing.T) {
	loop := &publicLoop{deltas: messages.NewTypedBuffer[messages.StreamMessage](1)}
	if !loop.deltas.Write(context.Background(), messages.StreamMessage{
		Type: messages.StreamTypeMessageEnd,
		Value: &messages.MessageEndValue{
			Status:            "failed",
			ProviderErrorCode: "rate_limit_exceeded",
		},
	}) {
		t.Fatal("rate-limit terminal was not queued")
	}
	err := NewService().Run(sessionduration.RunRequest{
		Context:     context.Background(),
		Inferencer:  publicInferencer{session: newPublicSession()},
		Clock:       publicNilTimerScheduler{},
		DrainPolicy: sessionduration.DrainPolicy{Clock: publicImmediateTimerScheduler{}},
		Retry:       sessionduration.RetryPolicy{Enabled: true},
		LoopFactory: func(context.Context, sessionduration.AdmissionInferencer, sessionduration.Controller) (sessionduration.Loop, error) {
			return loop, nil
		},
	})
	if !errors.Is(err, sessionduration.ErrSchedulerUnavailable) {
		t.Fatalf("Run() = %v, want ErrSchedulerUnavailable for nil retry timer", err)
	}
	if loop.sentEvents != 0 {
		t.Fatalf("retry sent %d session events with an unavailable scheduler", loop.sentEvents)
	}
}

func TestPublicFinalizeNilDrainTimerPreservesUnavailableSchedulerIdentity(t *testing.T) {
	controller, err := NewService().Begin(sessionduration.Options{})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	_, err = controller.Finalize(context.Background(), sessionduration.FinalizeRequest{
		DrainLoop:   &publicLoop{deltas: messages.NewTypedBuffer[messages.StreamMessage](1)},
		DrainPolicy: sessionduration.DrainPolicy{Clock: publicNilTimerScheduler{}},
	})
	if !errors.Is(err, sessionduration.ErrSchedulerUnavailable) {
		t.Fatalf("Finalize() = %v, want ErrSchedulerUnavailable for nil drain timer", err)
	}
}

func TestPublicFinalizeNilResetTimerPreservesUnavailableSchedulerIdentity(t *testing.T) {
	deltas := messages.NewTypedBuffer[messages.StreamMessage](1)
	scheduler := &publicOneTimerThenNilScheduler{created: make(chan *publicManualTimer, 1)}
	controller, err := NewService().Begin(sessionduration.Options{})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	finalized := make(chan error, 1)
	go func() {
		_, err := controller.Finalize(context.Background(), sessionduration.FinalizeRequest{
			DrainLoop:   &publicLoop{deltas: deltas},
			DrainPolicy: sessionduration.DrainPolicy{Clock: scheduler},
		})
		finalized <- err
	}()
	_ = receivePublicTimer(t, &publicManualScheduler{created: scheduler.created})
	if !deltas.Write(context.Background(), messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant}) {
		t.Fatal("could not queue output during bounded drain")
	}
	select {
	case err := <-finalized:
		if !errors.Is(err, sessionduration.ErrSchedulerUnavailable) {
			t.Fatalf("Finalize() = %v, want ErrSchedulerUnavailable for nil reset timer", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Finalize did not stop after the drain scheduler returned a nil reset timer")
	}
}

func TestPublicNilArtifactLifecycleIsNoop(t *testing.T) {
	service := NewService()
	ctx := service.WithArtifacts(context.Background(), nil)
	if service.ArtifactsFromContext(ctx) != nil {
		t.Fatal("nil artifact lifecycle was attached to the context")
	}
}

func TestPublicTerminalRecorderPreservesArtifactPathsDuringPreparation(t *testing.T) {
	service := NewService()
	directory := t.TempDir()
	paths := sessionduration.SessionDurationArtifactPaths{
		AudioPath:      filepath.Join(directory, "session.wav"),
		TranscriptPath: filepath.Join(directory, "session.jsonl"),
	}
	ctx := service.WithArtifactPaths(context.Background(), paths)
	recorder := &publicCapturingTerminalRecorder{}
	ctx = service.WithTerminalRecorder(ctx, recorder)
	prepared, err := service.PrepareArtifacts(ctx)
	if err != nil {
		t.Fatalf("PrepareArtifacts: %v", err)
	}
	for _, path := range []string{paths.AudioPath, paths.TranscriptPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("artifact path %q was not opened: %v", path, err)
		}
	}
	lifecycle := service.ArtifactsFromContext(prepared)
	terminal := messages.StreamMessage{Type: messages.StreamTypeSessionClose, Value: messages.NewSessionCloseValueWithTerminal(
		"session", "done", "provider_close", messages.TerminalReasonProviderClose,
		messages.TerminalProvenanceProvider, messages.TerminalOutputPartial,
	)}
	if err := lifecycle.Accept(terminal); err != nil {
		t.Fatalf("terminal Accept: %v", err)
	}
	if len(recorder.summaries) != 1 || recorder.summaries[0].TerminalReason != messages.TerminalReasonProviderClose {
		t.Fatalf("recorded terminal summaries = %+v", recorder.summaries)
	}
	if err := service.FinalizeArtifacts(lifecycle); err != nil {
		t.Fatalf("FinalizeArtifacts: %v", err)
	}
}

func TestPublicRunBoundsLoopThatIgnoresCancellation(t *testing.T) {
	done := make(chan struct{})
	close(done)
	loop := &publicStubbornLoop{deltas: messages.NewTypedBuffer[messages.StreamMessage](1), release: make(chan struct{}), exited: make(chan struct{})}
	err := NewService().Run(sessionduration.RunRequest{
		Context:    context.Background(),
		Inferencer: publicInferencer{session: newPublicSession()},
		Done:       done,
		DrainPolicy: sessionduration.DrainPolicy{
			LoopJoinTimeout: 20 * time.Millisecond,
		},
		LoopFactory: func(context.Context, sessionduration.AdmissionInferencer, sessionduration.Controller) (sessionduration.Loop, error) {
			return loop, nil
		},
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run() = %v, want bounded loop-join deadline", err)
	}
	close(loop.release)
	select {
	case <-loop.exited:
	case <-time.After(time.Second):
		t.Fatal("uncooperative loop did not exit after the test released it")
	}
}

type publicOneTimerThenNilScheduler struct {
	created chan *publicManualTimer
	calls   int
}

func (s *publicOneTimerThenNilScheduler) NewTimer(duration time.Duration) sessionduration.Timer {
	if s.calls > 0 {
		return nil
	}
	s.calls++
	timer := &publicManualTimer{duration: duration, events: make(chan time.Time, 1)}
	s.created <- timer
	return timer
}

type publicStubbornLoop struct {
	deltas  *messages.TypedBuffer[messages.StreamMessage]
	release chan struct{}
	exited  chan struct{}
}

func (l *publicStubbornLoop) Run(context.Context) error {
	<-l.release
	close(l.exited)
	return nil
}
func (l *publicStubbornLoop) Deltas() *messages.TypedBuffer[messages.StreamMessage] { return l.deltas }
func (*publicStubbornLoop) Send(context.Context, []messages.Message) error          { return nil }

type publicCapturingTerminalRecorder struct {
	summaries []transcript.RecordingTerminalSummary
}

func (r *publicCapturingTerminalRecorder) RecordTerminalSummary(summary transcript.RecordingTerminalSummary) error {
	r.summaries = append(r.summaries, summary)
	return nil
}

type publicNilTimerScheduler struct{}

func (publicNilTimerScheduler) NewTimer(time.Duration) sessionduration.Timer { return nil }

type publicImmediateTimerScheduler struct{}

func (publicImmediateTimerScheduler) NewTimer(duration time.Duration) sessionduration.Timer {
	timer := &publicManualTimer{duration: duration, events: make(chan time.Time, 1)}
	timer.Fire()
	return timer
}

type publicGatedScheduler struct {
	entered chan struct{}
	release chan struct{}
	created chan *publicManualTimer
	calls   int
}

func (s *publicGatedScheduler) NewTimer(duration time.Duration) sessionduration.Timer {
	if s.calls == 0 {
		s.calls++
		close(s.entered)
		<-s.release
	}
	timer := &publicManualTimer{duration: duration, events: make(chan time.Time, 1)}
	s.created <- timer
	return timer
}

func receiveGatedPublicTimer(t *testing.T, scheduler *publicGatedScheduler) *publicManualTimer {
	t.Helper()
	select {
	case timer := <-scheduler.created:
		return timer
	case <-time.After(time.Second):
		t.Fatal("session duration timer was not created")
		return nil
	}
}
