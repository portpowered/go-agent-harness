package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
)

func TestStateProjectsOutputStates(t *testing.T) {
	cases := []struct {
		name string
		seen []messages.StreamMessage
		want messages.TerminalOutputState
	}{
		{name: "none", want: messages.TerminalOutputNone},
		{name: "partial", seen: []messages.StreamMessage{{Type: messages.StreamTypeTextDelta}}, want: messages.TerminalOutputPartial},
		{name: "complete", seen: []messages.StreamMessage{{Type: messages.StreamTypeTextDelta}, {Type: messages.StreamTypeMessageEnd}}, want: messages.TerminalOutputComplete},
		{name: "user transcript is not output", seen: []messages.StreamMessage{{Type: messages.StreamTypeTranscriptDelta, Role: messages.RoleUser}}, want: messages.TerminalOutputNone},
		{name: "assistant transcript is output", seen: []messages.StreamMessage{{Type: messages.StreamTypeTranscriptDelta, Role: messages.RoleAssistant}}, want: messages.TerminalOutputPartial},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state := New().NewState(sessionduration.TerminalSource{})
			for _, msg := range tc.seen {
				state.Observe(msg)
			}
			if got := state.OutputState(); got != tc.want {
				t.Fatalf("output state = %q, want %q", got, tc.want)
			}
		})
	}
	state := New().NewState(sessionduration.TerminalSource{})
	state.Observe(messages.StreamMessage{Type: messages.StreamTypeTextDelta})
	state.Observe(messages.StreamMessage{Type: messages.StreamTypeMessageStart})
	if got := state.OutputState(); got != messages.TerminalOutputNone {
		t.Fatalf("message start did not reset output state: %q", got)
	}
}

func TestStateAdmitsProviderTerminalBeforeLoopClose(t *testing.T) {
	provider := providerTerminal("provider-close")
	state := New().NewState(providerSource(provider))
	loopClose := messages.StreamMessage{Type: messages.StreamTypeSessionClose, Value: messages.NewSessionCloseValue("", "loop-close")}
	if _, write := state.Admit(true, loopClose); write {
		t.Fatal("loop shutdown close was treated as provider evidence")
	}
	if state.Written() {
		t.Fatal("rejected loop close marked the terminal written")
	}
	if got, write := state.Admit(true, provider); !write || got.Value != provider.Value {
		t.Fatalf("provider close = (%+v, %v), want admitted provider message", got, write)
	}
	if !state.Written() {
		t.Fatal("provider close did not mark the terminal written")
	}
	if _, write := state.Admit(true, provider); write {
		t.Fatal("duplicate provider close was admitted")
	}
	nonplanned := New().NewState(providerSource(provider))
	if _, write := nonplanned.Admit(false, loopClose); !write || nonplanned.Written() {
		t.Fatal("unplanned loop close precedence changed")
	}
}

func TestStatePublishesProviderTerminalOnceInArtifactThenOutputOrder(t *testing.T) {
	provider := providerTerminal("provider-close")
	state := New().NewState(providerSource(provider))
	var mu sync.Mutex
	var events []string
	publication := sessionduration.Publication{
		Artifacts: artifactFunc(func(messages.StreamMessage) error {
			mu.Lock()
			defer mu.Unlock()
			events = append(events, "artifact")
			return nil
		}),
		Write: sessionduration.MessageWriter(func(messages.StreamMessage) error {
			mu.Lock()
			defer mu.Unlock()
			events = append(events, "output")
			return nil
		}),
	}
	if err := state.PublishProviderTerminal(publication); err != nil {
		t.Fatalf("publish provider terminal: %v", err)
	}
	if err := state.PublishProviderTerminal(publication); err != nil {
		t.Fatalf("duplicate provider terminal: %v", err)
	}
	if got, want := len(events), 2; got != want {
		t.Fatalf("publication count = %d, want %d (%v)", got, want, events)
	}
	if events[0] != "artifact" || events[1] != "output" {
		t.Fatalf("publication order = %v, want artifact then output", events)
	}
}

func TestStateRetriesProviderTerminalAfterArtifactFailure(t *testing.T) {
	provider := providerTerminal("provider-close")
	state := New().NewState(providerSource(provider))
	artifactErr := errors.New("artifact identity")
	first := true
	publication := sessionduration.Publication{Artifacts: artifactFunc(func(messages.StreamMessage) error {
		if first {
			first = false
			return artifactErr
		}
		return nil
	}), Write: sessionduration.MessageWriter(func(messages.StreamMessage) error { return nil })}
	if err := state.PublishProviderTerminal(publication); !errors.Is(err, artifactErr) {
		t.Fatalf("artifact error = %v, want identity", err)
	}
	if state.Written() {
		t.Fatal("failed publication marked the terminal written")
	}
	if err := state.PublishProviderTerminal(publication); err != nil {
		t.Fatalf("retry provider terminal: %v", err)
	}
	if !state.Written() {
		t.Fatal("successful retry did not mark the terminal written")
	}
}

func TestServicePublishesMaxDurationMetadata(t *testing.T) {
	service := New()
	var accepted, written messages.StreamMessage
	publication := sessionduration.Publication{
		Artifacts: artifactFunc(func(msg messages.StreamMessage) error {
			accepted = msg
			return nil
		}),
		Write: sessionduration.MessageWriter(func(msg messages.StreamMessage) error {
			written = msg
			return nil
		}),
	}
	if err := service.PublishMaxDuration(publication, messages.TerminalOutputPartial); err != nil {
		t.Fatalf("publish max duration: %v", err)
	}
	if accepted.Type != messages.StreamTypeSessionClose || written.Type != messages.StreamTypeSessionClose {
		t.Fatalf("terminal messages were not published: accepted=%+v written=%+v", accepted, written)
	}
	value, ok := written.Value.(*messages.SessionCloseValue)
	if !ok || value == nil {
		t.Fatalf("max duration value = %T, want session close", written.Value)
	}
	if value.Reason != "max_duration" || value.Classification != "max_duration" || value.TerminalReason != messages.TerminalReason("max_duration") || value.TerminalProvenance != messages.TerminalProvenanceLoop || value.OutputState != messages.TerminalOutputPartial {
		t.Fatalf("max duration metadata = %+v", value)
	}
}

func TestStatePublishesMaxDurationOnceAndMarksWritten(t *testing.T) {
	state := New().NewState(sessionduration.TerminalSource{})
	writes := 0
	var accepted messages.StreamMessage
	publication := sessionduration.Publication{
		Artifacts: artifactFunc(func(msg messages.StreamMessage) error {
			accepted = msg
			return nil
		}),
		Write: sessionduration.MessageWriter(func(messages.StreamMessage) error {
			writes++
			return nil
		}),
	}
	if err := state.PublishMaxDuration(publication, messages.TerminalOutputComplete); err != nil {
		t.Fatalf("publish max duration: %v", err)
	}
	if err := state.PublishMaxDuration(publication, messages.TerminalOutputComplete); err != nil {
		t.Fatalf("duplicate max duration: %v", err)
	}
	if writes != 1 || !state.Written() {
		t.Fatalf("max duration writes=%d written=%v, want one write and written state", writes, state.Written())
	}
	value, ok := accepted.Value.(*messages.SessionCloseValue)
	if !ok || value == nil || value.TerminalReason != messages.TerminalReason("max_duration") || value.OutputState != messages.TerminalOutputComplete {
		t.Fatalf("max duration metadata = %+v", accepted.Value)
	}
}

func TestServiceJoinsLifecycleAndTransportErrors(t *testing.T) {
	runtimeErr := errors.New("runtime identity")
	closeErr := errors.New("close identity")
	bindingErr := errors.New("binding identity")
	err := New().LifecycleError(sessionduration.LifecycleFailures{Runtime: runtimeErr, Close: closeErr, Binding: bindingErr})
	if !errors.Is(err, runtimeErr) || !errors.Is(err, closeErr) || !errors.Is(err, bindingErr) {
		t.Fatalf("lifecycle identities lost: %v", err)
	}
	transportErr := errors.New("transport identity")
	if got := New().TransportError(transportErr); !errors.Is(got, transportErr) {
		t.Fatalf("transport identity lost: %v", got)
	}
	if New().TransportError(nil) != nil || New().LifecycleError(sessionduration.LifecycleFailures{}) != nil {
		t.Fatal("nil error composition was not nil")
	}
}

func TestStateSerializesDuplicateProviderPublication(t *testing.T) {
	provider := providerTerminal("provider-close")
	state := New().NewState(providerSource(provider))
	var mu sync.Mutex
	writes := 0
	publication := sessionduration.Publication{Write: sessionduration.MessageWriter(func(messages.StreamMessage) error {
		mu.Lock()
		writes++
		mu.Unlock()
		return nil
	})}
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := state.PublishProviderTerminal(publication); err != nil {
				t.Errorf("concurrent publication: %v", err)
			}
		}()
	}
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	if writes != 1 {
		t.Fatalf("concurrent writes = %d, want 1", writes)
	}
}

func TestServiceHandlesNilPublicationPorts(t *testing.T) {
	if err := New().PublishMaxDuration(sessionduration.Publication{}, messages.TerminalOutputNone); err != nil {
		t.Fatalf("nil max-duration ports: %v", err)
	}
	state := New().NewState(providerSource(providerTerminal("provider-close")))
	if err := state.PublishProviderTerminal(sessionduration.Publication{}); err != nil {
		t.Fatalf("nil provider ports: %v", err)
	}
	if !state.Written() {
		t.Fatal("nil publication did not complete the local terminal transition")
	}
}

type artifactFunc func(messages.StreamMessage) error

func (f artifactFunc) Accept(msg messages.StreamMessage) error { return f(msg) }

func providerSource(provider messages.StreamMessage) sessionduration.TerminalSource {
	value, ok := provider.Value.(*messages.SessionCloseValue)
	if !ok {
		panic("provider test message is not a session close")
	}
	return sessionduration.TerminalSource{
		Message: func() (messages.StreamMessage, bool) { return provider, true },
		Matches: func(msg messages.StreamMessage) bool {
			candidate, ok := msg.Value.(*messages.SessionCloseValue)
			return ok && candidate == value
		},
	}
}

func providerTerminal(reason string) messages.StreamMessage {
	return messages.StreamMessage{Type: messages.StreamTypeSessionClose, Value: messages.NewSessionCloseValueWithTerminal(
		"session", reason, "provider_close", messages.TerminalReasonProviderClose,
		messages.TerminalProvenanceProvider, messages.TerminalOutputPartial,
	)}
}

func TestServiceFacadeRetainsStateAndErrorContract(t *testing.T) {
	service := New()
	if err := service.ValidateDuration(-time.Second); !errors.Is(err, sessionduration.ErrInvalidDuration) {
		t.Fatalf("ValidateDuration() = %v, want invalid-duration identity", err)
	}
	if err := service.ValidateDuration(0); err != nil {
		t.Fatalf("ValidateDuration(0) = %v", err)
	}

	provider := providerTerminal("provider")
	state := service.NewState(providerSource(provider))
	state.Observe(messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant})
	if got := state.OutputState(); got != messages.TerminalOutputPartial {
		t.Fatalf("state output = %q, want partial", got)
	}
	if err := state.PublishProviderTerminal(sessionduration.Publication{}); err != nil {
		t.Fatalf("PublishProviderTerminal: %v", err)
	}
	if !state.Written() {
		t.Fatal("state did not retain terminal-written state")
	}
	if _, ok := state.Admit(true, provider); ok {
		t.Fatal("state admitted a duplicate provider terminal")
	}

	var published messages.StreamMessage
	if err := service.PublishMaxDuration(sessionduration.Publication{Write: func(msg messages.StreamMessage) error {
		published = msg
		return nil
	}}, messages.TerminalOutputPartial); err != nil {
		t.Fatalf("PublishMaxDuration: %v", err)
	}
	if published.Type != messages.StreamTypeSessionClose {
		t.Fatalf("max-duration publication type = %q", published.Type)
	}

	runtimeErr := errors.New("runtime")
	closeErr := errors.New("close")
	if got := service.LifecycleError(sessionduration.LifecycleFailures{Runtime: runtimeErr, Close: closeErr}); !errors.Is(got, runtimeErr) || !errors.Is(got, closeErr) {
		t.Fatalf("LifecycleError() = %v, missing causes", got)
	}
	if service.TransportError(nil) != nil || !errors.Is(service.TransportError(runtimeErr), runtimeErr) {
		t.Fatal("TransportError did not preserve nil and wrapped identities")
	}
}

func TestServiceFacadeWrapsAdmissionAndArtifacts(t *testing.T) {
	service := New()
	admission := service.NewEventAdmission()
	if admission == nil {
		t.Fatal("NewEventAdmission returned nil")
	}
	wrappedInferencer := service.NewAdmissionInferencer(contractInferencer{session: newContractSession()}, admission, nil)
	if wrappedInferencer == nil {
		t.Fatal("NewAdmissionInferencer returned nil")
	}
	wrapper := service.NewAdmissionSession(context.Background(), newContractSession(), admission, nil)
	if wrapper == nil || !wrapper.SupportsCompleteMessages() || !wrapper.SupportsCompleteMessagesWithoutResponse() {
		t.Fatal("NewAdmissionSession did not preserve complete-message capabilities")
	}
	if err := wrapper.Close(); err != nil {
		t.Fatalf("wrapped session Close: %v", err)
	}
	if service.NewAdmissionInferencer(contractInferencer{session: newContractSession()}, struct{}{}, nil) == nil {
		t.Fatal("NewAdmissionInferencer rejected an opaque admission boundary")
	}

	artifact := NewSessionDurationArtifactSetWithSinks(nil, &artifactTranscriptSink{})
	ctx := service.WithArtifacts(context.Background(), artifact)
	if service.ArtifactsFromContext(ctx) != artifact {
		t.Fatal("ArtifactsFromContext did not return the attached lifecycle")
	}
	ctx = service.WithTerminalRecorder(ctx, &terminalRecorderProbe{})
	if service.ArtifactsFromContext(ctx) == nil {
		t.Fatal("WithTerminalRecorder removed the artifact lifecycle")
	}
	if service.WithTerminalRecorder(ctx, nil) != ctx {
		t.Fatal("WithTerminalRecorder(nil) did not preserve the context")
	}
	ctx = service.WithArtifactPaths(ctx, sessionduration.SessionDurationArtifactPaths{AudioPath: "audio", TranscriptPath: "transcript"})
	if _, ok := ArtifactPathsFromContext(ctx); !ok {
		t.Fatal("WithArtifactPaths did not retain paths")
	}
	if _, err := service.PrepareArtifacts(context.Background()); err != nil {
		t.Fatalf("PrepareArtifacts with no paths: %v", err)
	}
	if err := service.FinalizeArtifacts(nil); err != nil {
		t.Fatalf("FinalizeArtifacts(nil): %v", err)
	}
}

func TestAdmissionContractHandlesUnavailableSession(t *testing.T) {
	inferencer := NewAdmissionInferencer(nil, nil, nil)
	if _, err := inferencer.ConnectSession(context.Background()); err == nil {
		t.Fatal("ConnectSession(nil) unexpectedly succeeded")
	}
	inferencer.CloseAdmission()
	inferencer.WaitForClose()

	session := NewAdmissionSession(context.Background(), nil, nil, nil)
	if session.Send(context.Background(), messages.StreamMessage{}) {
		t.Fatal("Send on unavailable session unexpectedly succeeded")
	}
	if outcome := session.RequestResponse(context.Background()); outcome.Status != messages.SessionSendTerminalFailure {
		t.Fatalf("RequestResponse status = %q, want terminal failure", outcome.Status)
	}
	if session.SendMessage(context.Background(), messages.NewTextMessage(messages.RoleUser, "tool")) || session.SendMessageWithoutResponse(context.Background(), messages.NewTextMessage(messages.RoleUser, "tool")) {
		t.Fatal("complete-message send on unavailable session unexpectedly succeeded")
	}
	if session.SupportsResponseRequests() || session.SupportsCompleteMessages() || session.SupportsCompleteMessagesWithoutResponse() {
		t.Fatal("unavailable session reported unsupported capabilities")
	}
	if _, ok := session.RTCMedia(); ok || session.TerminalError() != nil {
		t.Fatal("unavailable session reported transport capabilities")
	}
	if err := session.Close(); err != nil {
		t.Fatalf("Close unavailable session: %v", err)
	}
}

func TestServiceFacadeClassifiesRetryAndTerminalMessages(t *testing.T) {
	service := New()
	provider := providerTerminal("provider")
	retryTerminal := &messages.MessageEndValue{Status: "failed", ProviderErrorCode: "rate_limit_exceeded", ProviderErrorMessage: "please try again in 3s"}
	if decision := service.EvaluateRetry(sessionduration.RetryPolicy{Enabled: true}, retryTerminal); !decision.Eligible || decision.Delay != 3*time.Second {
		t.Fatalf("EvaluateRetry() = %+v", decision)
	}
	statusRetry := &messages.MessageEndValue{Status: "failed", StatusDetails: "code=rate_limit_exceeded,message=please try again in 1.5s"}
	if decision := service.EvaluateRetry(sessionduration.RetryPolicy{Enabled: true}, statusRetry); !decision.Eligible || decision.Delay != 1500*time.Millisecond {
		t.Fatalf("EvaluateRetry(status details) = %+v", decision)
	}
	if decision := service.EvaluateRetry(sessionduration.RetryPolicy{Enabled: true}, nil); decision != (sessionduration.RetryDecision{}) {
		t.Fatalf("EvaluateRetry(nil) = %+v, want empty decision", decision)
	}
	if decision := service.EvaluateRetry(sessionduration.RetryPolicy{Enabled: true, DefaultDelay: time.Second}, &messages.MessageEndValue{Status: "failed", ProviderErrorCode: "rate_limit_exceeded", ProviderErrorMessage: "retry later"}); decision.Delay != time.Second {
		t.Fatalf("EvaluateRetry(unparseable delay) = %+v, want default delay", decision)
	}
	if decision := service.EvaluateRetry(sessionduration.RetryPolicy{Enabled: true, DefaultDelay: time.Second}, &messages.MessageEndValue{Status: "failed", ProviderErrorCode: "rate_limit_exceeded", ProviderErrorMessage: "please try again in 0s"}); decision.Delay != time.Second {
		t.Fatalf("EvaluateRetry(zero delay) = %+v, want default delay", decision)
	}
	if !service.IsDurationShutdownMessage(provider) || !service.IsDurationForwardMessage(messages.StreamMessage{Type: messages.StreamTypeError}) {
		t.Fatal("duration message classification did not preserve terminal/error forwarding")
	}
	if summary, present, err := service.RecordingTerminalSummaryFromMessage(provider); err != nil || !present || summary == nil {
		t.Fatalf("RecordingTerminalSummaryFromMessage() = %+v, %v, %v", summary, present, err)
	}
	if _, present, err := service.RecordingTerminalSummaryFromMessage(messages.StreamMessage{Type: messages.StreamTypeSessionClose, Value: &messages.SessionCloseValue{Classification: "incomplete"}}); present || err == nil {
		t.Fatalf("invalid terminal summary = present:%v err:%v, want validation error", present, err)
	}
}

func TestRunHandlesWakeAndDoneBoundaryFailures(t *testing.T) {
	wakeErr := errors.New("wake failed")
	doneErr := errors.New("done failed")
	tests := []struct {
		name      string
		wake      <-chan struct{}
		done      <-chan struct{}
		onWake    func(context.Context, sessionduration.Loop, sessionduration.Controller) error
		doneError func() error
		want      error
	}{
		{
			name: "wake",
			wake: func() <-chan struct{} {
				wake := make(chan struct{}, 1)
				wake <- struct{}{}
				return wake
			}(),
			onWake: func(context.Context, sessionduration.Loop, sessionduration.Controller) error { return wakeErr },
			want:   wakeErr,
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
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := sessionduration.RunRequest{
				Context:    context.Background(),
				Inferencer: contractInferencer{session: newContractSession()},
				LoopFactory: func(context.Context, sessionduration.AdmissionInferencer, sessionduration.Controller) (sessionduration.Loop, error) {
					return &idleRunLoopProbe{deltas: messages.NewTypedBuffer[messages.StreamMessage](1)}, nil
				},
				Wake:      test.wake,
				OnWake:    test.onWake,
				Done:      test.done,
				DoneError: test.doneError,
				Drain:     func(context.Context, sessionduration.Loop, sessionduration.Controller) error { return nil },
			}
			if err := New().Run(request); !errors.Is(err, test.want) {
				t.Fatalf("Run() = %v, want %v", err, test.want)
			}
		})
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

type retryRunLoopProbe struct {
	deltas *messages.TypedBuffer[messages.StreamMessage]
	sent   chan messages.StreamMessage
}

func (l *retryRunLoopProbe) Run(ctx context.Context) error {
	terminal := messages.StreamMessage{
		Type: messages.StreamTypeMessageEnd,
		Role: messages.RoleAssistant,
		Value: &messages.MessageEndValue{
			Status:               "failed",
			ProviderErrorCode:    "rate_limit_exceeded",
			ProviderErrorMessage: "retry after 1s",
		},
	}
	if !l.deltas.Write(ctx, terminal) {
		return ctx.Err()
	}
	<-ctx.Done()
	return ctx.Err()
}

func (l *retryRunLoopProbe) Deltas() *messages.TypedBuffer[messages.StreamMessage] { return l.deltas }
func (l *retryRunLoopProbe) Send(context.Context, []messages.Message) error        { return nil }
func (l *retryRunLoopProbe) SendSessionEvent(ctx context.Context, msg messages.StreamMessage) error {
	select {
	case l.sent <- msg:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type retryRunLoopWithoutSessionEvents struct {
	deltas *messages.TypedBuffer[messages.StreamMessage]
}

func (l *retryRunLoopWithoutSessionEvents) Run(ctx context.Context) error {
	terminal := messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: &messages.MessageEndValue{
		Status:            "failed",
		ProviderErrorCode: "rate_limit_exceeded",
	}}
	if !l.deltas.Write(ctx, terminal) {
		return ctx.Err()
	}
	<-ctx.Done()
	return ctx.Err()
}

func (l *retryRunLoopWithoutSessionEvents) Deltas() *messages.TypedBuffer[messages.StreamMessage] {
	return l.deltas
}
func (l *retryRunLoopWithoutSessionEvents) Send(context.Context, []messages.Message) error {
	return nil
}

func TestWaitForLoopNormalizesCancellation(t *testing.T) {
	results := make(chan error, 2)
	results <- context.Canceled
	if err := waitForLoop(results); err != nil {
		t.Fatalf("waitForLoop(cancellation) = %v, want nil", err)
	}
	failure := errors.New("loop failed")
	results <- failure
	if err := waitForLoop(results); !errors.Is(err, failure) {
		t.Fatalf("waitForLoop(failure) = %v, want failure identity", err)
	}
}

type triggerScheduler struct {
	created chan *triggerTimer
}

type triggerTimer struct {
	events chan time.Time
}

func (s *triggerScheduler) NewTimer(time.Duration) sessionduration.Timer {
	timer := &triggerTimer{events: make(chan time.Time, 1)}
	s.created <- timer
	return timer
}
func (t *triggerTimer) C() <-chan time.Time      { return t.events }
func (t *triggerTimer) Stop() bool               { return false }
func (t *triggerTimer) Reset(time.Duration) bool { return true }

func TestRunExpiresAtMaxDurationAndClosesLoop(t *testing.T) {
	scheduler := &triggerScheduler{created: make(chan *triggerTimer, 1)}
	result := make(chan error, 1)
	go func() {
		result <- New().Run(sessionduration.RunRequest{
			Context:     context.Background(),
			Inferencer:  contractInferencer{session: newContractSession()},
			Clock:       scheduler,
			MaxDuration: time.Second,
			LoopFactory: func(context.Context, sessionduration.AdmissionInferencer, sessionduration.Controller) (sessionduration.Loop, error) {
				return &idleRunLoopProbe{deltas: messages.NewTypedBuffer[messages.StreamMessage](1)}, nil
			},
			Drain: func(context.Context, sessionduration.Loop, sessionduration.Controller) error { return nil },
		})
	}()
	var timer *triggerTimer
	select {
	case timer = <-scheduler.created:
	case <-time.After(time.Second):
		t.Fatal("max-duration timer was not created")
	}
	timer.events <- time.Now()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("Run after max duration = %v, want bounded clean stop", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not finish after max duration")
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
