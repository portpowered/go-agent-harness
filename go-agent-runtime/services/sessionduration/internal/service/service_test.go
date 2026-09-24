package service

import (
	"context"
	"errors"
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

func TestServiceCompletesBoundedResponseAndScheduleFailures(t *testing.T) {
	var service sessionduration.Service = New()
	primary := errors.New("provider close")
	output := errors.New("audio output failed")
	err := service.Complete(sessionduration.CompletionRequest{
		RunError:                      primary,
		AudioOutputError:              func() error { return output },
		RequireTerminalAssistantReply: true,
		CloseAfterScheduledAudio:      true,
		ScheduledAudioIncomplete:      true,
		ScheduledAudioCompleted:       2,
		ScheduledAudioDispatched:      2,
		ScheduledAudioCount:           3,
		ProviderScheduledStatus:       "failed",
		ProviderScheduledErrorCode:    "server_error",
		ProviderScheduledErrorDetails: "reason=error, code=server_error",
	})
	for _, cause := range []error{primary, output, sessionduration.ErrAssistantResponseIncomplete, sessionduration.ErrScheduledAudioIncomplete} {
		if !errors.Is(err, cause) {
			t.Errorf("completed error %v does not retain %v", err, cause)
		}
	}
	var incomplete *sessionduration.ScheduledAudioIncompleteError
	if !errors.As(err, &incomplete) {
		t.Fatalf("completed error = %v, want typed scheduled-audio failure", err)
	}
	if incomplete.Completed != 2 || incomplete.Dispatched != 2 || incomplete.Scheduled != 3 ||
		incomplete.ProviderStatus != "failed" || incomplete.ProviderErrorCode != "server_error" ||
		incomplete.ProviderDetails != "reason=error, code=server_error" {
		t.Fatalf("scheduled-audio evidence = %+v", incomplete)
	}
	//nolint:errorlint // exact identity proves repeated completion does not wrap the result again.
	if again := service.Complete(sessionduration.CompletionRequest{
		RunError:                 err,
		CloseAfterScheduledAudio: true,
		ScheduledAudioIncomplete: true,
	}); again != err {
		t.Fatalf("scheduled failure was wrapped twice: first=%v second=%v", err, again)
	}
}

func TestServiceDoesNotPromoteBoundOrDurationShutdownToIncompleteResponse(t *testing.T) {
	var service sessionduration.Service = New()
	for _, request := range []sessionduration.CompletionRequest{
		{RequireTerminalAssistantReply: true, RoomBoundCancellation: true},
		{RequireTerminalAssistantReply: true, DurationExpired: true, CloseAfterScheduledAudio: true, ScheduledAudioIncomplete: true},
	} {
		if err := service.Complete(request); err != nil {
			t.Fatalf("shutdown completion = %v, want clean bounded terminal", err)
		}
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
