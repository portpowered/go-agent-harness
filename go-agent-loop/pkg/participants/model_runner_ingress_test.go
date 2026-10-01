package participants

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/participants/internal/sessionstate"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

// enqueueTestAudio admits interrupting-by-default audio through the ordered
// session ingress, waiting for capacity like a live capture loop.
func enqueueTestAudio(t *testing.T, runner *ModelRunner, pcm []byte) {
	t.Helper()
	if err := runner.EnqueueSessionInput(context.Background(), SessionAudio(pcm, messages.SessionAudioInputPolicyDefault), SessionAdmitWaiting); err != nil {
		t.Fatalf("enqueue session audio: %v", err)
	}
}

// enqueueTestEvent admits a control event through the ordered session ingress.
func enqueueTestEvent(t *testing.T, runner *ModelRunner, msg messages.StreamMessage) {
	t.Helper()
	if err := runner.EnqueueSessionInput(context.Background(), SessionEvent(msg), SessionAdmitWaiting); err != nil {
		t.Fatalf("enqueue session event: %v", err)
	}
}

// drainTestInputs forwards every queued session input against state, as the
// session loop's preflight does.
func drainTestInputs(ctx context.Context, runner *ModelRunner, session messages.Session, state *sessionRunState) error {
	_, err := runner.forwardPendingSessionInputs(ctx, session, state)
	return err
}

// wireLogSession records every provider-bound input, stream events and
// complete messages alike, in one sequence.
type wireLogSession struct {
	*recordingSession
	mu  sync.Mutex
	log []string
	got chan struct{}
}

func newWireLogSession() *wireLogSession {
	return &wireLogSession{recordingSession: newRecordingSession(), got: make(chan struct{}, 64)}
}

func (s *wireLogSession) record(entry string) {
	s.mu.Lock()
	s.log = append(s.log, entry)
	s.mu.Unlock()
	s.got <- struct{}{}
}

func (s *wireLogSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	entry := string(msg.Type)
	if value, ok := msg.Value.(*messages.AudioDeltaValue); ok {
		entry += ":" + string(value.Content)
	}
	s.record(entry)
	return s.recordingSession.Send(ctx, msg)
}

func (s *wireLogSession) SendMessageWithoutResponse(_ context.Context, msg messages.Message) bool {
	s.record("COMPLETE:" + msg.TextContent())
	return true
}

func (s *wireLogSession) waitFor(t *testing.T, n int) []string {
	t.Helper()
	for range n {
		select {
		case <-s.got:
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for %d provider inputs", n)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.log...)
}

func assertWireOrder(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("provider inputs = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("provider inputs = %q, want %q", got, want)
		}
	}
}

// runQueuedInputs starts the runner after every input is already queued, so
// the order the provider sees depends only on the ingress.
func runQueuedInputs(t *testing.T, runner *ModelRunner, session *wireLogSession, n int) []string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	got := session.waitFor(t, n)
	cancel()
	<-done
	return got
}

func admit(t *testing.T, runner *ModelRunner, inputs ...SessionInput) {
	t.Helper()
	for _, input := range inputs {
		if err := runner.EnqueueSessionInput(context.Background(), input, SessionAdmitOrFail); err != nil {
			t.Fatalf("admit %+v: %v", input, err)
		}
	}
}

func audioInput(b byte) SessionInput {
	return SessionAudio([]byte{b}, messages.SessionAudioInputPolicyDoNotInterrupt)
}

// Audio, control events, and complete messages share one FIFO: a frame never
// overtakes the turn boundary admitted before it, and never falls behind one
// admitted after it.
func TestSessionIngress_AudioControlsAndMessagesKeepAdmissionOrder(t *testing.T) {
	session := newWireLogSession()
	runner := NewSessionModelRunner(&testSessionInferencer{session: session}, 16, nil)
	admit(t, runner,
		audioInput('1'),
		SessionEvent(messages.StreamMessage{Type: messages.StreamTypeMessageEnd}),
		audioInput('2'),
		SessionMessage(messages.NewTextMessage(messages.RoleUser, "look"), false),
		audioInput('3'),
		SessionEvent(messages.StreamMessage{Type: messages.StreamTypeResponseCreate, Value: messages.NewResponseCreateValue()}),
		audioInput('4'),
	)
	assertWireOrder(t, runQueuedInputs(t, runner, session, 7), []string{
		"AUDIO.DELTA:1", "MESSAGE.END", "AUDIO.DELTA:2", "COMPLETE:look", "AUDIO.DELTA:3", "RESPONSE.CREATE", "AUDIO.DELTA:4",
	})
}

// With no control queued, an interrupt overtakes queued audio; behind a
// queued control (event or complete message) it keeps its FIFO position.
func TestSessionIngress_CancelPriorityRespectsQueuedControls(t *testing.T) {
	cancel := SessionEvent(messages.StreamMessage{Type: messages.StreamTypeResponseCancel, Value: messages.NewResponseCancelValue()})
	for _, test := range []struct {
		name   string
		inputs []SessionInput
		want   []string
	}{
		{
			name:   "overtakes audio",
			inputs: []SessionInput{audioInput('1'), audioInput('2'), cancel, audioInput('3')},
			want:   []string{"RESPONSE.CANCEL", "AUDIO.DELTA:1", "AUDIO.DELTA:2", "AUDIO.DELTA:3"},
		},
		{
			name:   "behind turn boundary",
			inputs: []SessionInput{audioInput('1'), SessionEvent(messages.StreamMessage{Type: messages.StreamTypeMessageEnd}), cancel, audioInput('2')},
			want:   []string{"AUDIO.DELTA:1", "MESSAGE.END", "RESPONSE.CANCEL", "AUDIO.DELTA:2"},
		},
		{
			name:   "behind complete message",
			inputs: []SessionInput{audioInput('1'), SessionMessage(messages.NewTextMessage(messages.RoleUser, "hi"), false), cancel, audioInput('2')},
			want:   []string{"AUDIO.DELTA:1", "COMPLETE:hi", "RESPONSE.CANCEL", "AUDIO.DELTA:2"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			session := newWireLogSession()
			runner := NewSessionModelRunner(&testSessionInferencer{session: session}, 16, nil)
			admit(t, runner, test.inputs...)
			assertWireOrder(t, runQueuedInputs(t, runner, session, len(test.want)), test.want)
		})
	}
}

// A rejected admission must not leave a control counted as queued, or every
// later interrupt would lose its priority lane.
func TestSessionIngress_RejectedControlReleasesCancelPriority(t *testing.T) {
	runner := NewSessionModelRunner(nil, 8, nil)
	for i := range cap(runner.ingress.ordered) {
		admit(t, runner, audioInput(byte(i)))
	}
	err := runner.EnqueueSessionInput(context.Background(), SessionMessage(messages.NewTextMessage(messages.RoleUser, "x"), true), SessionAdmitOrFail)
	if !errors.Is(err, ErrSessionInputQueueFull) {
		t.Fatalf("message admission on a full ingress = %v, want ErrSessionInputQueueFull", err)
	}
	if got := runner.ingress.queuedControls.Load(); got != 0 {
		t.Fatalf("queued controls after rejection = %d, want 0", got)
	}
	admit(t, runner, SessionEvent(messages.StreamMessage{Type: messages.StreamTypeResponseCancel}))
	if len(runner.ingress.priority) != 1 {
		t.Fatal("interrupt after a rejected control did not take the priority lane")
	}
}

func TestSessionIngress_AdmissionPreconditions(t *testing.T) {
	turnBased := NewModelRunner(nil, 1)
	if err := turnBased.EnqueueSessionInput(context.Background(), audioInput(1), SessionAdmitOrFail); !errors.Is(err, ErrNotSessionMode) {
		t.Fatalf("turn-based admission = %v, want ErrNotSessionMode", err)
	}
	var nilRunner *ModelRunner
	if nilRunner.SessionMode() {
		t.Fatal("nil runner reported session mode")
	}
	runner := NewSessionModelRunner(nil, 8, nil)
	var nilCtx context.Context
	if err := runner.EnqueueSessionInput(nilCtx, audioInput(1), SessionAdmitWaiting); !errors.Is(err, errNilAdmissionContext) {
		t.Fatalf("waiting admission without context = %v, want errNilAdmissionContext", err)
	}
	if err := runner.EnqueueSessionInput(nilCtx, audioInput(1), SessionAdmitOrFail); err != nil {
		t.Fatalf("non-waiting admission without context = %v, want admitted", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := runner.EnqueueSessionInput(cancelled, SessionMessage(messages.Message{}, true), SessionAdmitOrFail); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled admission = %v, want context.Canceled", err)
	}
}

// heldOnsetFrame is 20 ms of speech at 24 kHz: loud enough to be held, too
// short to complete the default 40 ms onset alone.
func heldOnsetFrame() []byte { return pcmFrameAtLevel(9000) }

func expiryReady(state *sessionRunState) bool {
	select {
	case <-state.Onset.Expiry():
		return true
	default:
		return false
	}
}

// The held-onset window is timed by the injected clock and the configured
// MinSpeech: the frame is released exactly when onset can no longer be
// reached, never earlier.
func TestSessionModelRunner_HeldOnsetExpiryFollowsInjectedClock(t *testing.T) {
	fake := clock.NewDeterministic(time.Time{}, time.Millisecond)
	session := newRecordingSession()
	runner := NewSessionModelRunner(nil, 16, nil)
	runner.SetClock(fake)
	config := DefaultBargeInConfig()
	config.MinSpeech = 100 * time.Millisecond
	runner.SetBargeInConfig(config)
	state := newInFlightRunState(t, session, runner, "resp-held")

	sendUserAudio(t, runner, session, state, heldOnsetFrame())
	if len(state.Onset.Frames) != 1 || len(session.sentMessages()) != 0 {
		t.Fatalf("held=%d sent=%d, want the frame held while onset is undecided", len(state.Onset.Frames), len(session.sentMessages()))
	}
	fake.AdvanceBy(config.MinSpeech - time.Millisecond)
	if expiryReady(state) {
		t.Fatal("held onset expired before the configured onset window elapsed")
	}
	fake.AdvanceBy(time.Millisecond)
	if !expiryReady(state) {
		t.Fatal("held onset did not expire once the onset window elapsed on the injected clock")
	}
	runner.flushHeldAudio(context.Background(), session, state)
	sent := session.sentMessages()
	if len(sent) != 1 || sent[0].Type != messages.StreamTypeAudioDelta || state.Onset.Expiry() != nil {
		t.Fatalf("sent=%#v expiry armed=%t, want only the released frame and no timer", sent, state.Onset.Expiry() != nil)
	}
}

// The session loop itself releases the held frame from the timer case of
// its select once the injected clock passes the onset window.
func TestSessionModelRunner_RunReleasesHeldOnsetOnInjectedClock(t *testing.T) {
	fake := clock.NewDeterministic(time.Time{}, time.Millisecond)
	session := newRecordingSession()
	runner := NewSessionModelRunner(&testSessionInferencer{session: session}, 16, nil)
	runner.SetClock(fake)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ap := NewActiveParticipant(messages.Model, runner)
	ap.Start(ctx)
	defer ap.Stop()

	session.recv.Write(ctx, sessionMessage(messages.StreamTypeMessageStart, "resp-sparse"))
	waitForDelta(t, ctx, runner, messages.StreamTypeMessageStart)
	enqueueTestAudio(t, runner, heldOnsetFrame())
	if err := fake.WaitForTimers(ctx, 1); err != nil {
		t.Fatalf("held onset timer was never armed: %v", err)
	}
	if got := len(session.sentMessages()); got != 0 {
		t.Fatalf("sent %d messages while onset was undecided, want the frame held", got)
	}
	fake.AdvanceBy(DefaultBargeInConfig().MinSpeech)
	if sent := waitForSentMessage(t, ctx, session); sent.Type != messages.StreamTypeAudioDelta {
		t.Fatalf("released %s, want the held AUDIO.DELTA", sent.Type)
	}
	if got := countSent(session.sentMessages(), messages.StreamTypeResponseCancel); got != 0 {
		t.Fatalf("a lone transient sent %d RESPONSE.CANCEL, want none", got)
	}
}

// A held frame that the provider rejects when it is finally released is the
// runner's terminal audio failure, published so the engine observes it.
func TestSessionModelRunner_HeldOnsetReleaseFailureIsPublished(t *testing.T) {
	session := &outcomeRecordingSession{
		recordingSession: newRecordingSession(),
		outcomes: map[messages.StreamMessageType]messages.SessionSendOutcome{
			messages.StreamTypeAudioDelta: {Status: messages.SessionSendClosed},
		},
	}
	runner := NewSessionModelRunner(nil, 16, nil)
	runner.SetClock(clock.NewDeterministic(time.Time{}, time.Millisecond))
	state := newInFlightRunState(t, session, runner, "resp-rejecting")
	for _, ok := runner.DeltaOutbox.Read(); ok; _, ok = runner.DeltaOutbox.Read() {
	}
	sendUserAudio(t, runner, session, state, heldOnsetFrame())
	runner.flushHeldAudio(context.Background(), session, state)
	failure, ok := runner.DeltaOutbox.Read()
	if !ok || failure.Type != messages.StreamTypeError {
		t.Fatalf("published %#v (ok=%t), want the release failure", failure, ok)
	}
	if value, ok := failure.Value.(*messages.ErrorValue); !ok || value.Classification != sessionAudioSendFailureClassification {
		t.Fatalf("failure value = %#v, want %s", failure.Value, sessionAudioSendFailureClassification)
	}
	if len(state.Onset.Frames) != 0 {
		t.Fatal("a failed release kept the frame held")
	}
}

// Speech after a completed response but before a requested progress
// acknowledgement opens cancels that acknowledgement: the user's next turn
// boundary waits for it, its response is cancelled as soon as it starts, and
// its late output never reaches the customer.
func TestSessionModelRunner_BargeInCancelsAcknowledgementBeforeItStarts(t *testing.T) {
	ctx := context.Background()
	session := newRecordingSession()
	runner := NewSessionModelRunner(nil, 32, nil)
	state := newInFlightRunState(t, session, runner, "resp-turn")
	runner.forwardSessionMessageState(ctx, session, state, sessionMessage(messages.StreamTypeMessageEnd, "resp-turn"))
	runner.forwardQueuedSessionEvent(ctx, session, state, acknowledgementCreate())
	sendUserAudio(t, runner, session, state, loudPCM())
	if !state.Ack.Cancelled() || !state.Response.Completed() {
		t.Fatalf("state after speech = %+v, want the pending acknowledgement cancelled and the completed turn kept", state)
	}
	runner.forwardQueuedSessionEvent(ctx, session, state, messages.StreamMessage{Type: messages.StreamTypeMessageEnd})
	if len(state.Deferred) != 1 {
		t.Fatalf("deferred = %d, want the turn boundary held behind the cancelled acknowledgement", len(state.Deferred))
	}
	for _, ok := runner.DeltaOutbox.Read(); ok; _, ok = runner.DeltaOutbox.Read() {
	}
	runner.forwardSessionMessageState(ctx, session, state, sessionMessage(messages.StreamTypeMessageStart, "resp-ack"))
	if state.Response.Phase != sessionstate.ResponseCancelling || state.ResponseIDs.Get("resp-ack") != sessionstate.ResponseIDCancelled {
		t.Fatalf("acknowledgement start = %+v, want it cancelled on arrival", state)
	}
	runner.forwardSessionMessageState(ctx, session, state, sessionMessage(messages.StreamTypeAudioDelta, ""))
	runner.forwardSessionMessageState(ctx, session, state, sessionMessage(messages.StreamTypeMessageEnd, "resp-ack"))
	for delta, ok := runner.DeltaOutbox.Read(); ok; delta, ok = runner.DeltaOutbox.Read() {
		if delta.Type == messages.StreamTypeAudioDelta {
			t.Fatalf("cancelled acknowledgement output reached the customer: %#v", delta)
		}
	}
	want := []messages.StreamMessageType{messages.StreamTypeResponseCreate, messages.StreamTypeResponseCancel, messages.StreamTypeAudioDelta, messages.StreamTypeMessageEnd}
	sent := session.sentMessages()
	if len(sent) != len(want) {
		t.Fatalf("sent %#v, want %v", sent, want)
	}
	for i := range want {
		if sent[i].Type != want[i] {
			t.Fatalf("sent %#v, want %v", sent, want)
		}
	}
	if state.Ack.Outstanding() || state.Response.Phase != sessionstate.ResponseIdle || len(state.Deferred) != 0 {
		t.Fatalf("state after the acknowledgement ended = %+v, want idle with the boundary released", state)
	}
}

func TestSessionModelRunner_CompleteMessageRequiresProviderSupport(t *testing.T) {
	ctx := context.Background()
	runner := NewSessionModelRunner(nil, 8, nil)
	state := &sessionRunState{}
	for _, requestResponse := range []bool{true, false} {
		err := runner.forwardSessionInput(ctx, newStreamOnlyRecordingSession(), state, SessionMessage(messages.NewTextMessage(messages.RoleUser, "hi"), requestResponse))
		if err == nil {
			t.Fatalf("stream-only session accepted a complete message (requestResponse=%t)", requestResponse)
		}
	}
	session := newRecordingSession()
	if err := runner.forwardSessionInput(ctx, session, state, SessionMessage(messages.NewTextMessage(messages.RoleUser, "now"), true)); err != nil {
		t.Fatalf("complete message: %v", err)
	}
	if got := session.completeMessages(); len(got) != 1 || got[0].TextContent() != "now" {
		t.Fatalf("complete messages = %#v, want the admitted message", got)
	}
}
