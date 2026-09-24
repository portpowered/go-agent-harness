package wire

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

func TestNewServiceReturnsPublicContract(t *testing.T) {
	var service sessionduration.Service = NewService()
	if service == nil {
		t.Fatal("NewService returned nil")
	}
	if service.NewState(sessionduration.TerminalSource{}) == nil {
		t.Fatal("NewService did not construct state")
	}
}

func TestPublicLivenessIgnoresUserAndSystemMessages(t *testing.T) {
	for _, role := range []messages.Role{messages.RoleUser, messages.RoleSystem} {
		t.Run(string(role), func(t *testing.T) { assertPublicLivenessRole(t, role) })
	}
}

func TestPublicLivenessSeparatesToolAcknowledgementFromNewResponse(t *testing.T) {
	clock := platformclock.NewDeterministic(time.Unix(120, 0), time.Millisecond)
	controller, err := NewService().Begin(sessionduration.Options{
		Clock:    clock,
		Liveness: sessionduration.LivenessOptions{Enabled: true, Timeout: 5 * time.Millisecond},
	})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	t.Cleanup(func() {
		if _, err := controller.Finalize(context.Background(), sessionduration.FinalizeRequest{}); err != nil {
			t.Errorf("Finalize: %v", err)
		}
	})

	controller.Observe(messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant})
	controller.Observe(messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant})
	if got := controller.OutputState(); got != messages.TerminalOutputComplete {
		t.Fatalf("completed assistant output state = %q", got)
	}
	controller.Observe(messages.StreamMessage{
		Type:  messages.StreamTypeResponseCreate,
		Role:  messages.RoleAssistant,
		Value: messages.NewToolAcknowledgementResponseCreateValue(),
	})
	if got := controller.OutputState(); got != messages.TerminalOutputComplete {
		t.Fatalf("tool acknowledgement changed prior output state to %q", got)
	}
	clock.AdvanceBy(20 * time.Millisecond)
	select {
	case err := <-controller.Errors():
		t.Fatalf("tool acknowledgement started a liveness timer: %v", err)
	case <-time.After(20 * time.Millisecond):
	}

	controller.Observe(messages.StreamMessage{
		Type:       messages.StreamTypeResponseCreate,
		Role:       messages.RoleAssistant,
		ResponseID: "provider-response-cancelled",
		Value:      messages.NewResponseCreateValue(),
	})
	if got := controller.OutputState(); got != messages.TerminalOutputNone {
		t.Fatalf("new response retained prior output state %q", got)
	}
	controller.Observe(messages.StreamMessage{Type: messages.StreamTypeResponseCancel, Role: messages.RoleUser})
	assertNoPublicLivenessError(t, controller, clock.AdvanceBy, "cancelled response")

	controller.Observe(messages.StreamMessage{
		Type:       messages.StreamTypeResponseCreate,
		Role:       messages.RoleAssistant,
		ResponseID: "provider-response-next",
		Value:      messages.NewResponseCreateValue(),
	})
	clock.AdvanceBy(6 * time.Millisecond)
	select {
	case err := <-controller.Errors():
		var typed *sessionduration.LivenessError
		if !errors.Is(err, sessionduration.ErrProviderLivenessTimeout) || !errors.As(err, &typed) || typed.ResponseID != "provider-response-next" {
			t.Fatalf("new response liveness error = %v, want timeout for provider-response-next", err)
		}
	case <-time.After(time.Second):
		t.Fatal("normal response.create did not arm liveness")
	}
}

func assertNoPublicLivenessError(t *testing.T, controller sessionduration.Controller, advance func(time.Duration) time.Time, reason string) {
	t.Helper()
	advance(20 * time.Millisecond)
	select {
	case err := <-controller.Errors():
		t.Fatalf("%s triggered a liveness error: %v", reason, err)
	case <-time.After(20 * time.Millisecond):
	}
}

func assertPublicLivenessRole(t *testing.T, role messages.Role) {
	t.Helper()
	clock := platformclock.NewDeterministic(time.Unix(60, 0), time.Millisecond)
	controller, err := NewService().Begin(sessionduration.Options{
		Clock:    clock,
		Liveness: sessionduration.LivenessOptions{Enabled: true, Timeout: 5 * time.Millisecond},
	})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	t.Cleanup(func() {
		if _, err := controller.Finalize(context.Background(), sessionduration.FinalizeRequest{}); err != nil {
			t.Errorf("Finalize: %v", err)
		}
	})

	controller.Observe(messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: role})
	controller.Observe(messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: role})
	if got := controller.OutputState(); got != messages.TerminalOutputNone {
		t.Fatalf("non-provider output state = %q, want none", got)
	}
	clock.AdvanceBy(20 * time.Millisecond)
	select {
	case err := <-controller.Errors():
		t.Fatalf("non-provider input triggered liveness failure: %v", err)
	case <-time.After(20 * time.Millisecond):
	}

	controller.Observe(messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, ResponseID: "provider-response-1"})
	controller.Observe(messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant})
	if got := controller.OutputState(); got != messages.TerminalOutputPartial {
		t.Fatalf("assistant output state = %q, want partial", got)
	}
	clock.AdvanceBy(2 * time.Millisecond)
	controller.Observe(messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: role})
	controller.Observe(messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: role})
	controller.Observe(messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: role})
	if got := controller.OutputState(); got != messages.TerminalOutputPartial {
		t.Fatalf("non-provider message changed assistant output state to %q", got)
	}
	clock.AdvanceBy(4 * time.Millisecond)
	select {
	case err := <-controller.Errors():
		if !errors.Is(err, sessionduration.ErrProviderLivenessTimeout) {
			t.Fatalf("assistant liveness error = %v, want timeout", err)
		}
		var typed *sessionduration.LivenessError
		if !errors.As(err, &typed) || typed.ResponseID != "provider-response-1" {
			t.Fatalf("assistant liveness error = %#v, want response ID provider-response-1", typed)
		}
	case <-time.After(time.Second):
		t.Fatal("assistant response did not retain the liveness timeout")
	}
}

func TestPublicRetryCapsFallbackDelay(t *testing.T) {
	terminal := &messages.MessageEndValue{
		Status:               "failed",
		ProviderErrorCode:    "rate_limit_exceeded",
		ProviderErrorMessage: "retry after provider reset",
	}
	decision := NewService().EvaluateRetry(sessionduration.RetryPolicy{
		Enabled:      true,
		DefaultDelay: 2 * time.Second,
		MaxDelay:     500 * time.Millisecond,
	}, terminal)
	if !decision.Eligible || decision.Delay != 500*time.Millisecond {
		t.Fatalf("EvaluateRetry() = %+v, want eligible retry capped at 500ms", decision)
	}
}

func TestPublicRunSurfacesMissingRetryScheduler(t *testing.T) {
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
		Context:    context.Background(),
		Inferencer: publicInferencer{session: newPublicSession()},
		Retry:      sessionduration.RetryPolicy{Enabled: true},
		LoopFactory: func(context.Context, sessionduration.AdmissionInferencer, sessionduration.Controller) (sessionduration.Loop, error) {
			return loop, nil
		},
	})
	if !errors.Is(err, sessionduration.ErrSchedulerUnavailable) {
		t.Fatalf("Run() = %v, want missing retry scheduler error", err)
	}
	if loop.sentEvents != 0 {
		t.Fatalf("retry sent %d session events without a scheduler", loop.sentEvents)
	}
}

func TestPublicRunDeadlineInterruptsRateLimitBackoff(t *testing.T) {
	scheduler := &publicManualScheduler{created: make(chan *publicManualTimer, 4)}
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
	result := make(chan error, 1)
	go func() {
		result <- NewService().Run(sessionduration.RunRequest{
			Context:     context.Background(),
			Inferencer:  publicInferencer{session: newPublicSession()},
			Clock:       scheduler,
			MaxDuration: time.Second,
			Retry:       sessionduration.RetryPolicy{Enabled: true, MaxRetries: 1},
			LoopFactory: func(context.Context, sessionduration.AdmissionInferencer, sessionduration.Controller) (sessionduration.Loop, error) {
				return loop, nil
			},
		})
	}()
	maxDuration := receivePublicTimer(t, scheduler)
	if maxDuration.duration != time.Second {
		t.Fatalf("first timer duration = %v, want session deadline", maxDuration.duration)
	}
	retryBackoff := receivePublicTimer(t, scheduler)
	if retryBackoff.duration != 2*time.Second {
		t.Fatalf("retry timer duration = %v, want the default backoff", retryBackoff.duration)
	}
	maxDuration.Fire()
	quietPeriod := receivePublicTimer(t, scheduler)
	if quietPeriod.duration != 25*time.Millisecond {
		t.Fatalf("drain timer duration = %v, want the bounded quiet period", quietPeriod.duration)
	}
	quietPeriod.Fire()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("Run after max duration interrupted retry = %v, want clean stop", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not finish when the session deadline interrupted retry")
	}
	if loop.sentEvents != 0 {
		t.Fatalf("Run dispatched %d retry events after the session deadline", loop.sentEvents)
	}
}

func TestPublicTerminalRecorderFailureIsSurfaced(t *testing.T) {
	recordErr := errors.New("terminal summary storage failed")
	service := NewService()
	ctx := service.WithTerminalRecorder(context.Background(), publicTerminalRecorder{err: recordErr})
	artifacts := service.ArtifactsFromContext(ctx)
	if artifacts == nil {
		t.Fatal("WithTerminalRecorder did not attach a lifecycle")
	}
	terminal := messages.StreamMessage{
		Type: messages.StreamTypeSessionClose,
		Value: messages.NewSessionCloseValueWithTerminal(
			"session", "done", "provider_close", messages.TerminalReasonProviderClose,
			messages.TerminalProvenanceProvider, messages.TerminalOutputComplete,
		),
	}
	if err := artifacts.Accept(terminal); !errors.Is(err, recordErr) {
		t.Fatalf("terminal Accept() = %v, want recorder error", err)
	}
}

func TestPublicRunPropagatesHostAndPublicationFailures(t *testing.T) {
	hostErr := errors.New("host callback failed")
	publicationErr := errors.New("public output failed")
	loopCloseErr := errors.New("loop close failed")
	tests := []struct {
		name      string
		message   messages.StreamMessage
		configure func(*sessionduration.RunRequest, *publicLoop)
		want      error
	}{
		{
			name: "external error",
			configure: func(request *sessionduration.RunRequest, _ *publicLoop) {
				external := make(chan error, 1)
				external <- hostErr
				request.ExternalErrors = external
			},
			want: hostErr,
		},
		{
			name:    "host message handler",
			message: messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant},
			configure: func(request *sessionduration.RunRequest, _ *publicLoop) {
				request.Handle = func(context.Context, sessionduration.Loop, sessionduration.Controller, messages.StreamMessage) (sessionduration.MessageResult, error) {
					return sessionduration.MessageResult{}, hostErr
				}
			},
			want: hostErr,
		},
		{
			name:    "publication writer",
			message: messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant},
			configure: func(request *sessionduration.RunRequest, _ *publicLoop) {
				request.Publication.Write = func(messages.StreamMessage) error { return publicationErr }
			},
			want: publicationErr,
		},
		{
			name:    "planned loop close",
			message: messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant},
			configure: func(request *sessionduration.RunRequest, loop *publicLoop) {
				loop.sendErr = loopCloseErr
				request.Handle = func(context.Context, sessionduration.Loop, sessionduration.Controller, messages.StreamMessage) (sessionduration.MessageResult, error) {
					return sessionduration.MessageResult{Stop: true, Planned: true}, nil
				}
			},
			want: loopCloseErr,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			loop := &publicLoop{deltas: messages.NewTypedBuffer[messages.StreamMessage](1)}
			if test.message.Type != "" && !loop.deltas.Write(context.Background(), test.message) {
				t.Fatal("test message was not queued")
			}
			request := sessionduration.RunRequest{
				Context:    context.Background(),
				Inferencer: publicInferencer{session: newPublicSession()},
				LoopFactory: func(context.Context, sessionduration.AdmissionInferencer, sessionduration.Controller) (sessionduration.Loop, error) {
					return loop, nil
				},
			}
			test.configure(&request, loop)
			if err := NewService().Run(request); !errors.Is(err, test.want) {
				t.Fatalf("Run() = %v, want %v", err, test.want)
			}
		})
	}
}

func TestPublicRunDisablesClosedWakeChannel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	wake := make(chan struct{})
	close(wake)
	woke := make(chan struct{}, 1)
	started := make(chan struct{})
	result := make(chan error, 1)
	session := newPublicSession()
	go func() {
		result <- NewService().Run(sessionduration.RunRequest{
			Context:    ctx,
			Inferencer: publicInferencer{session: session},
			Wake:       wake,
			OnWake: func(context.Context, sessionduration.Loop, sessionduration.Controller) error {
				select {
				case woke <- struct{}{}:
				default:
				}
				return nil
			},
			LoopFactory: func(ctx context.Context, inferencer sessionduration.AdmissionInferencer, _ sessionduration.Controller) (sessionduration.Loop, error) {
				if _, err := inferencer.ConnectSession(ctx); err != nil {
					return nil, err
				}
				close(started)
				return &publicLoop{deltas: messages.NewTypedBuffer[messages.StreamMessage](1)}, nil
			},
			Close: func() error { return session.Close() },
			Drain: func(context.Context, sessionduration.Loop, sessionduration.Controller) error { return nil },
		})
	}()
	<-started
	select {
	case <-woke:
		cancel()
		if err := waitPublicRun(t, result); !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() = %v after wake, want caller cancellation", err)
		}
		t.Fatal("closed wake channel kept invoking OnWake")
	case <-time.After(20 * time.Millisecond):
	}
	cancel()
	if err := waitPublicRun(t, result); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() = %v, want caller cancellation", err)
	}
}

func waitPublicRun(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(time.Second):
		t.Fatal("Run did not stop within one second after cancellation")
		return nil
	}
}

type publicInferencer struct{ session *publicSession }

type publicTerminalRecorder struct{ err error }

func (r publicTerminalRecorder) RecordTerminalSummary(transcript.RecordingTerminalSummary) error {
	return r.err
}

type publicManualScheduler struct {
	created chan *publicManualTimer
}

func (s *publicManualScheduler) NewTimer(duration time.Duration) sessionduration.Timer {
	timer := &publicManualTimer{duration: duration, events: make(chan time.Time, 1)}
	s.created <- timer
	return timer
}

type publicManualTimer struct {
	duration time.Duration
	events   chan time.Time
}

func (t *publicManualTimer) C() <-chan time.Time { return t.events }
func (*publicManualTimer) Stop() bool            { return true }
func (t *publicManualTimer) Fire()               { t.events <- time.Now() }

func receivePublicTimer(t *testing.T, scheduler *publicManualScheduler) *publicManualTimer {
	t.Helper()
	select {
	case timer := <-scheduler.created:
		return timer
	case <-time.After(time.Second):
		t.Fatal("session duration timer was not created")
		return nil
	}
}

func (i publicInferencer) ConnectSession(context.Context) (messages.Session, error) {
	return i.session, nil
}

type publicSession struct {
	receive   *messages.TypedBuffer[messages.StreamMessage]
	done      chan struct{}
	closeOnce sync.Once
}

func newPublicSession() *publicSession {
	return &publicSession{receive: messages.NewTypedBuffer[messages.StreamMessage](8), done: make(chan struct{})}
}

func (s *publicSession) Send(context.Context, messages.StreamMessage) bool      { return true }
func (s *publicSession) Receive() *messages.TypedBuffer[messages.StreamMessage] { return s.receive }
func (s *publicSession) Done() <-chan struct{}                                  { return s.done }
func (s *publicSession) Close() error {
	s.closeOnce.Do(func() { close(s.done) })
	return nil
}

type publicLoop struct {
	deltas     *messages.TypedBuffer[messages.StreamMessage]
	sentEvents int
	sendErr    error
}

func (l *publicLoop) Run(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}
func (l *publicLoop) Deltas() *messages.TypedBuffer[messages.StreamMessage] { return l.deltas }
func (l *publicLoop) Send(context.Context, []messages.Message) error        { return l.sendErr }
func (l *publicLoop) SendSessionEvent(context.Context, messages.StreamMessage) error {
	l.sentEvents++
	return nil
}
