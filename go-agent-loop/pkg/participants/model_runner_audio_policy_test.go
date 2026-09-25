package participants

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

type providerConfiguredRecordingSession struct {
	*recordingSession
}

func (*providerConfiguredRecordingSession) InitialSessionConfigSent() bool { return true }

func TestSessionModelRunner_DoesNotEchoProviderOwnedInitialConfig(t *testing.T) {
	session := &providerConfiguredRecordingSession{recordingSession: newRecordingSession()}
	runner := NewSessionModelRunner(&testSessionInferencer{session: session}, 8, &messages.SessionUpdateConfig{
		Instructions: "be brief", Model: "gpt-realtime",
		Tools: []messages.ToolDefinition{{Name: "lookup_weather"}},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ap := NewActiveParticipant(messages.Model, runner)
	ap.Start(ctx)
	defer ap.Stop()
	if !session.recv.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeSessionCreated, Value: messages.NewSessionCreatedValue("provider-owned", "gpt-realtime")}) {
		t.Fatal("failed to enqueue SESSION.CREATED")
	}
	forwarded, ok := runner.DeltaOutbox.ReadBlocking(ctx.Done())
	if !ok {
		t.Fatal("context cancelled waiting for forwarded SESSION.CREATED")
	}
	if forwarded.Type != messages.StreamTypeSessionCreated {
		t.Fatalf("forwarded type = %s, want %s", forwarded.Type, messages.StreamTypeSessionCreated)
	}
	if sent := session.sentMessages(); len(sent) != 0 {
		t.Fatalf("provider messages = %#v, want no echoed initial SESSION.UPDATE", sent)
	}
}

func TestSessionModelRunner_PreservesLaterSessionUpdateAndAcknowledgement(t *testing.T) {
	session := &providerConfiguredRecordingSession{recordingSession: newRecordingSession()}
	runner := NewSessionModelRunner(nil, 8, &messages.SessionUpdateConfig{Tools: []messages.ToolDefinition{{Name: "lookup_weather"}}})
	ctx := context.Background()
	failure, deferred, accepted := runner.forwardSessionEvent(ctx, session, messages.StreamMessage{Type: messages.StreamTypeSessionUpdate, Value: messages.NewSessionUpdateValue(runner.sessionConfig)})
	if failure.Type != "" || deferred || accepted {
		t.Fatalf("later SESSION.UPDATE outcome = (%#v, %t, %t), want accepted ordinary update", failure, deferred, accepted)
	}
	sent := session.sentMessages()
	if len(sent) != 1 || sent[0].Type != messages.StreamTypeSessionUpdate {
		t.Fatalf("later provider messages = %#v, want one SESSION.UPDATE", sent)
	}
	runner.forwardSessionMessageState(ctx, session, newSessionResponseState(), messages.StreamMessage{Type: messages.StreamTypeSessionUpdated, Value: messages.NewSessionUpdatedValue("provider-owned")})
	acknowledgement, ok := runner.DeltaOutbox.Read()
	if !ok || acknowledgement.Type != messages.StreamTypeSessionUpdated {
		t.Fatalf("forwarded acknowledgement = %#v, ok=%t; want SESSION.UPDATED", acknowledgement, ok)
	}
}

func TestSessionModelRunnerWaitingAudioIngressBackpressuresUntilCapacity(t *testing.T) {
	runner := NewSessionModelRunner(nil, 8, nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for i := 0; i < cap(runner.sessionInputInbox); i++ {
		if err := runner.EnqueueSessionAudioInput(ctx, []byte{byte(i)}); err != nil {
			t.Fatalf("fill ordered session ingress at %d: %v", i, err)
		}
	}
	admitted := make(chan error, 1)
	go func() {
		admitted <- runner.EnqueueSessionAudioInputWithPolicyWaiting(ctx, []byte{0xff}, messages.SessionAudioInputPolicyDefault)
	}()
	<-runner.sessionInputInbox
	if err := <-admitted; err != nil {
		t.Fatalf("waiting audio admission = %v, want capacity backpressure then success", err)
	}
}

func fillSessionIngress(t *testing.T, runner *ModelRunner) {
	t.Helper()
	for i := 0; i < cap(runner.sessionInputInbox); i++ {
		if err := runner.EnqueueSessionAudioInput(t.Context(), []byte{byte(i)}); err != nil {
			t.Fatalf("fill ordered session ingress at %d: %v", i, err)
		}
	}
}

func TestSessionModelRunnerWaitingEventIngressBackpressuresUntilCapacity(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		runner := NewSessionModelRunner(nil, 8, nil)
		fillSessionIngress(t, runner)
		commit := messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Value: messages.NewMessageEndValue(messages.TokenUsage{})}
		if err := runner.EnqueueSessionEvent(t.Context(), commit); !errors.Is(err, ErrSessionInputQueueFull) {
			t.Fatalf("non-waiting event admission = %v, want ErrSessionInputQueueFull", err)
		}
		admitted := make(chan error, 1)
		go func() { admitted <- runner.EnqueueSessionEventWaiting(t.Context(), commit) }()
		synctest.Wait()
		select {
		case err := <-admitted:
			t.Fatalf("waiting event admission returned %v while the ingress was full", err)
		default:
		}
		if got := len(runner.sessionInputInbox); got != cap(runner.sessionInputInbox) {
			t.Fatalf("ingress length = %d before draining, want full %d", got, cap(runner.sessionInputInbox))
		}
		<-runner.sessionInputInbox
		if err := <-admitted; err != nil {
			t.Fatalf("waiting event admission = %v, want capacity backpressure then success", err)
		}
		for i := 1; i < cap(runner.sessionInputInbox); i++ {
			if input := <-runner.sessionInputInbox; input.kind != sessionInputAudio {
				t.Fatalf("ingress slot %d kind = %d, want earlier audio before the event", i, input.kind)
			}
		}
		if input := <-runner.sessionInputInbox; input.kind != sessionInputEvent || input.event.Type != messages.StreamTypeMessageEnd {
			t.Fatalf("last ingress input = %#v, want queued MESSAGE.END behind audio", input)
		}
	})
}

func TestSessionModelRunnerStopReleasesParkedWaitingAdmissions(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		runner := NewSessionModelRunner(&failingConnectInferencer{err: errors.New("provider session closed")}, 8, nil)
		fillSessionIngress(t, runner)
		event := make(chan error, 1)
		go func() {
			event <- runner.EnqueueSessionEventWaiting(t.Context(), messages.StreamMessage{
				Type:  messages.StreamTypeToolCallEnd,
				Value: messages.NewToolCallEndValue("call-stop", "tool", "result"),
			})
		}()
		synctest.Wait()
		select {
		case err := <-event:
			t.Fatalf("waiting event returned %v before the runner stopped", err)
		default:
		}
		if err := runner.Run(t.Context()); err == nil {
			t.Fatal("Run with a failing provider connection returned nil")
		}
		if err := <-event; !errors.Is(err, ErrSessionClosed) {
			t.Fatalf("parked event admission after runner stop = %v, want ErrSessionClosed", err)
		}
		if runner.hasPendingSessionToolEvents() {
			t.Fatal("abandoned waiting tool event remained marked pending")
		}
		err := runner.EnqueueSessionAudioInputWithPolicyWaiting(t.Context(), []byte{1}, messages.SessionAudioInputPolicyDefault)
		if !errors.Is(err, ErrSessionClosed) {
			t.Fatalf("waiting audio admission after runner stop = %v, want ErrSessionClosed", err)
		}
		// A second session run on the same runner must re-arm waiting admission.
		for len(runner.sessionInputInbox) > 0 {
			<-runner.sessionInputInbox
		}
		runner.sessionInferencer = &testSessionInferencer{session: newRecordingSession()}
		ctx, cancel := context.WithCancel(t.Context())
		runDone := make(chan error, 1)
		go func() { runDone <- runner.Run(ctx) }()
		synctest.Wait()
		if err := runner.EnqueueSessionAudioInputWithPolicyWaiting(t.Context(), []byte{2}, messages.SessionAudioInputPolicyDefault); err != nil {
			t.Fatalf("waiting audio admission during a second session run = %v, want success", err)
		}
		cancel()
		<-runDone
	})
}

func TestSessionModelRunnerWaitingEventIngressHonorsCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		runner := NewSessionModelRunner(nil, 8, nil)
		for i := 0; i < cap(runner.sessionInputInbox); i++ {
			if err := runner.EnqueueSessionAudioInput(t.Context(), []byte{byte(i)}); err != nil {
				t.Fatalf("fill ordered session ingress at %d: %v", i, err)
			}
		}
		ctx, cancel := context.WithCancel(t.Context())
		admitted := make(chan error, 1)
		go func() {
			admitted <- runner.EnqueueSessionEventWaiting(ctx, messages.StreamMessage{
				Type:  messages.StreamTypeToolCallEnd,
				Value: messages.NewToolCallEndValue("call-wait", "tool", "result"),
			})
		}()
		synctest.Wait()
		if !runner.hasPendingSessionToolEvents() {
			t.Fatal("waiting tool event was not marked pending while blocked on capacity")
		}
		cancel()
		if err := <-admitted; !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled waiting event admission = %v, want context.Canceled", err)
		}
		if runner.hasPendingSessionToolEvents() {
			t.Fatal("cancelled waiting tool event remained marked pending")
		}
		var nilCtx context.Context
		if err := runner.EnqueueSessionEventWaiting(nilCtx, messages.StreamMessage{}); err == nil {
			t.Fatal("nil context accepted by waiting event admission")
		}
	})
}

func TestModelRunner_ExplicitSessionAudioPolicyControlsCancellation(t *testing.T) {
	tests := []struct {
		name          string
		policy        messages.SessionAudioInputPolicy
		wantCancel    bool
		wantSentCount int
	}{
		{
			name:          "peer audio does not cancel",
			policy:        messages.SessionAudioInputPolicyDoNotInterrupt,
			wantCancel:    false,
			wantSentCount: 1,
		},
		{
			name:          "customer audio cancels",
			policy:        messages.SessionAudioInputPolicyInterrupt,
			wantCancel:    true,
			wantSentCount: 2,
		},
		{
			name:          "unknown audio origin cancels by default",
			policy:        messages.SessionAudioInputPolicy("unclassified"),
			wantCancel:    true,
			wantSentCount: 2,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			session := newRecordingSession()
			runner := NewSessionModelRunner(nil, 8, nil)
			state := newInFlightRunState(t, session, runner, "resp-policy")

			if err := runner.EnqueueSessionAudioInputWithPolicy(ctx, []byte{1, 2, 3, 4}, test.policy); err != nil {
				t.Fatalf("EnqueueSessionAudioInputWithPolicy: %v", err)
			}
			input := <-runner.sessionInputInbox
			if input.kind != sessionInputAudio {
				t.Fatalf("queued session input kind = %d, want audio", input.kind)
			}
			if err := runner.forwardSessionAudioInputWithState(ctx, session, input.audio, state); err != nil {
				t.Fatalf("forwardSessionAudioInputWithState: %v", err)
			}

			assertPolicyAudioForwarded(t, session.sentMessages(), test.wantSentCount, test.wantCancel)
			if state.responseInFlight != true {
				t.Fatalf("response state = %+v, want response to remain in flight until provider MESSAGE.END", state)
			}
		})
	}
}

// assertPolicyAudioForwarded checks the provider sends for one policy case:
// the expected count, at most the expected cancellation, and the original PCM
// as the final AUDIO.DELTA.
func assertPolicyAudioForwarded(t *testing.T, sent []messages.StreamMessage, wantSentCount int, wantCancel bool) {
	t.Helper()
	if len(sent) != wantSentCount {
		t.Fatalf("sent %d messages = %#v, want %d", len(sent), sent, wantSentCount)
	}
	cancelCount := 0
	for _, msg := range sent {
		if msg.Type == messages.StreamTypeResponseCancel {
			cancelCount++
		}
	}
	if (cancelCount == 1) != wantCancel {
		t.Fatalf("RESPONSE.CANCEL count = %d, want cancel=%t", cancelCount, wantCancel)
	}
	if sent[len(sent)-1].Type != messages.StreamTypeAudioDelta {
		t.Fatalf("last sent message = %s, want AUDIO.DELTA", sent[len(sent)-1].Type)
	}
	value, ok := sent[len(sent)-1].Value.(*messages.AudioDeltaValue)
	if !ok {
		t.Fatalf("last sent value = %T, want *messages.AudioDeltaValue", sent[len(sent)-1].Value)
	}
	if got := value.Content; string(got) != string([]byte{1, 2, 3, 4}) {
		t.Fatalf("forwarded PCM = %v, want [1 2 3 4]", got)
	}
}

func TestModelRunner_DrainSessionAudioForwardsQueuedFrames(t *testing.T) {
	session := newRecordingSession()
	runner := NewSessionModelRunner(&testSessionInferencer{session: session}, 8, nil)
	runner.UserAudioInbox <- []byte{4, 5, 6}

	responseInFlight := false
	responseCancelSent := false
	if err := runner.drainSessionAudio(context.Background(), session, &responseInFlight, &responseCancelSent); err != nil {
		t.Fatalf("drain queued audio: %v", err)
	}

	sent := session.sentMessages()
	if len(sent) != 1 || sent[0].Type != messages.StreamTypeAudioDelta {
		t.Fatalf("drained sends = %#v, want one AUDIO.DELTA", sent)
	}
	value, ok := sent[0].Value.(*messages.AudioDeltaValue)
	if !ok || string(value.Content) != string([]byte{4, 5, 6}) {
		t.Fatalf("drained audio = %#v, want original frame", sent[0].Value)
	}

	close(runner.UserAudioInbox)
	if err := runner.drainSessionAudio(context.Background(), session, &responseInFlight, &responseCancelSent); err != nil {
		t.Fatalf("drain closed audio inbox: %v", err)
	}
}

func TestModelRunner_ExplicitInterruptPolicyDoesNotCancelToolContinuation(t *testing.T) {
	ctx := context.Background()
	session := newRecordingSession()
	runner := NewSessionModelRunner(nil, 8, nil)
	state := newInFlightRunState(t, session, runner, "resp-continuation-policy")
	state.awaitingContinuation = true

	if err := runner.EnqueueSessionAudioInputWithPolicy(ctx, []byte{7, 7, 7}, messages.SessionAudioInputPolicyInterrupt); err != nil {
		t.Fatalf("EnqueueSessionAudioInputWithPolicy: %v", err)
	}
	input := <-runner.sessionInputInbox
	if input.kind != sessionInputAudio {
		t.Fatalf("queued session input kind = %d, want audio", input.kind)
	}
	if err := runner.forwardSessionAudioInputWithState(ctx, session, input.audio, state); err != nil {
		t.Fatalf("forwardSessionAudioInputWithState: %v", err)
	}

	sent := session.sentMessages()
	if len(sent) != 1 || sent[0].Type != messages.StreamTypeAudioDelta {
		t.Fatalf("tool continuation sends = %#v, want only AUDIO.DELTA", sent)
	}
	if state.responseCancelSent {
		t.Fatalf("tool continuation state = %+v, want cancellation exemption preserved", state)
	}
}

func acknowledgementCreate() messages.StreamMessage {
	return messages.StreamMessage{Type: messages.StreamTypeResponseCreate, Value: messages.NewToolAcknowledgementResponseCreateValue()}
}

// drainOrdinaryResponse forwards an untagged provider response and reports
// whether any customer-visible delta was re-tagged as an acknowledgement.
func drainOrdinaryResponse(t *testing.T, runner *ModelRunner, session messages.Session, state *sessionRunState, responseID string) bool {
	t.Helper()
	ctx := context.Background()
	for _, msg := range []messages.StreamMessage{
		{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, ResponseID: responseID, Value: messages.NewTextDeltaValue("answer")},
		{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, ResponseID: responseID, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
	} {
		runner.forwardSessionMessageState(ctx, session, state, msg)
	}
	tagged := false
	for {
		delta, ok := runner.DeltaOutbox.Read()
		if !ok {
			return tagged
		}
		tagged = tagged || delta.ResponsePurpose == messages.ResponsePurposeToolAcknowledgement
	}
}

func TestSessionModelRunner_AcknowledgementWaitsOutActiveResponse(t *testing.T) {
	session := newRecordingSession()
	runner := NewSessionModelRunner(nil, 16, nil)
	state := newInFlightRunState(t, session, runner, "resp-normal")

	runner.forwardQueuedSessionEvent(context.Background(), session, state, acknowledgementCreate())
	for _, sent := range session.sentMessages() {
		if sent.Type == messages.StreamTypeResponseCreate {
			t.Fatalf("acknowledgement was requested while a response was active: %#v", session.sentMessages())
		}
	}
	if drainOrdinaryResponse(t, runner, session, state, "resp-normal") || state.acknowledgementOutstanding || !state.responseCompleted {
		t.Fatalf("active response lost ordinary accounting: %+v", state)
	}
}

func TestSessionModelRunner_RejectedAcknowledgementReleasesOrdinaryResponse(t *testing.T) {
	serverStart := messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, ResponseID: "resp-server", Value: messages.NewMessageStartValue()}
	rejection := messages.StreamMessage{
		Type: messages.StreamTypeError,
		Value: &messages.ErrorValue{Type: "error", Message: "active response", NonTerminal: true,
			Classification: messages.ErrorClassificationResponseCreateActive},
	}
	// The provider may announce its own response before or after rejecting
	// the acknowledgement request; either way that response stays ordinary.
	for name, order := range map[string][]messages.StreamMessage{
		"start before rejection": {serverStart, rejection},
		"rejection before start": {rejection, serverStart},
	} {
		t.Run(name, func(t *testing.T) { assertRejectedAcknowledgementReleases(t, order) })
	}
}

func assertRejectedAcknowledgementReleases(t *testing.T, order []messages.StreamMessage) {
	t.Helper()
	ctx := context.Background()
	session := newRecordingSession()
	runner := NewSessionModelRunner(nil, 16, nil)
	state := &sessionRunState{}
	state.ensureMaps()
	runner.forwardQueuedSessionEvent(ctx, session, state, acknowledgementCreate())
	if !state.acknowledgementOutstanding {
		t.Fatalf("idle acknowledgement request was not admitted: %+v", state)
	}
	for _, msg := range order {
		runner.forwardSessionMessageState(ctx, session, state, msg)
	}
	if state.acknowledgementOutstanding {
		t.Fatalf("rejected acknowledgement stayed outstanding: %+v", state)
	}
	if start := lastAnnouncedStart(runner); start == nil || start.ResponseID != "resp-server" || start.ResponsePurpose != "" {
		t.Fatalf("last announced start = %#v, want an ordinary resp-server start", start)
	}
	if drainOrdinaryResponse(t, runner, session, state, "resp-server") || !state.responseCompleted {
		t.Fatalf("ordinary response after rejection was treated as an acknowledgement: %+v", state)
	}
}

// lastAnnouncedStart drains the runner's outbox and returns the final
// response start it announced.
func lastAnnouncedStart(runner *ModelRunner) *messages.StreamMessage {
	var last *messages.StreamMessage
	for {
		delta, ok := runner.DeltaOutbox.Read()
		if !ok {
			return last
		}
		if delta.Type == messages.StreamTypeMessageStart {
			last = &delta
		}
	}
}
