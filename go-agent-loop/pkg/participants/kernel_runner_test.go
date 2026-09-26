package participants

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// --- Delta dispatch to deltaEventCh ---

func TestKernelRunner_DispatchDeltaToDeltaEventCh(t *testing.T) {
	kr := NewKernelRunner(nil, 8)
	evCh := kr.NewDeltaEventReader(8)

	delta := messages.StreamMessage{
		Type:  messages.StreamTypeTextDelta,
		Value: messages.NewTextDeltaValue("hello"),
	}
	kr.dispatchDelta(context.Background(), messages.KernelDeltaRequest{
		Source: messages.Model,
		Delta:  delta,
	})

	select {
	case got := <-evCh:
		if got.Type != messages.StreamTypeTextDelta {
			t.Errorf("Type: got %s, want %s", got.Type, messages.StreamTypeTextDelta)
		}
		v, ok := got.Value.(*messages.TextDeltaValue)
		if !ok {
			t.Fatalf("Value: got %T, want *messages.TextDeltaValue", got.Value)
		}
		if v.Content != "hello" {
			t.Errorf("Content: got %q, want %q", v.Content, "hello")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for delta on deltaEventCh")
	}
}

func TestKernelRunner_SystemFullMessageNotSentToDeltaEventCh(t *testing.T) {
	kr := NewKernelRunner(nil, 8)
	evCh := kr.NewDeltaEventReader(8)

	kr.dispatchDelta(context.Background(), messages.KernelDeltaRequest{
		Source: messages.Model,
		Delta: messages.StreamMessage{
			Type:  messages.StreamTypeSystemFullMessage,
			Value: messages.NewInferenceResultValue("model", messages.NewTextMessage(messages.RoleAssistant, "hi")),
		},
	})

	select {
	case got := <-evCh:
		t.Errorf("SYSTEM.FULL_MESSAGE should not be sent to deltaEventCh, got %v", got)
	case <-time.After(50 * time.Millisecond):
		// expected: nothing on deltaEventCh
	}
}

func TestKernelRunner_NoDeltaEventChNoPanic(t *testing.T) {
	kr := NewKernelRunner(nil, 8)
	// Do NOT call NewDeltaEventReader — deltaEventCh is nil.
	// Dispatching should not panic.
	kr.dispatchDelta(context.Background(), messages.KernelDeltaRequest{
		Source: messages.Model,
		Delta: messages.StreamMessage{
			Type:  messages.StreamTypeTextDelta,
			Value: messages.NewTextDeltaValue("no listener"),
		},
	})
}

func TestKernelRunner_MultipleDeltaTypesDispatched(t *testing.T) {
	kr := NewKernelRunner(nil, 8)
	evCh := kr.NewDeltaEventReader(8)

	deltas := []messages.StreamMessage{
		{Type: messages.StreamTypeMessageStart, Value: messages.NewMessageStartValue()},
		{Type: messages.StreamTypeTextStart, Value: messages.NewTextStartValue()},
		{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("data")},
		{Type: messages.StreamTypeTextEnd, Value: messages.NewTextEndValue()},
		{Type: messages.StreamTypeMessageEnd, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
	}

	for _, d := range deltas {
		kr.dispatchDelta(context.Background(), messages.KernelDeltaRequest{
			Source: messages.Model,
			Delta:  d,
		})
	}

	for i, want := range deltas {
		select {
		case got := <-evCh:
			if got.Type != want.Type {
				t.Errorf("delta[%d].Type: got %s, want %s", i, got.Type, want.Type)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for delta[%d]", i)
		}
	}
}

// --- Message dispatch to messageOutCh ---

func TestKernelRunner_DispatchFullMessageToMessageOutCh(t *testing.T) {
	kr := NewKernelRunner(nil, 8)
	ch := make(chan messages.KernelMessageRequest, 8)
	kr.messageOutCh = ch

	msg := messages.NewTextMessage(messages.RoleAssistant, "response")
	kr.dispatchDelta(context.Background(), messages.KernelDeltaRequest{
		Source: messages.Model,
		Delta: messages.StreamMessage{
			Type:  messages.StreamTypeSystemFullMessage,
			Value: messages.NewInferenceResultValue("model", msg),
		},
	})

	select {
	case got := <-ch:
		if got.Source != messages.Model {
			t.Errorf("Source: got %s, want %s", got.Source, messages.Model)
		}
		if got.Message.TextContent() != "response" {
			t.Errorf("Message text: got %q, want %q", got.Message.TextContent(), "response")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for message on messageOutCh")
	}
}

func TestKernelRunner_NoMessageOutChNoPanic(t *testing.T) {
	kr := NewKernelRunner(nil, 8)
	// messageOutCh is nil — should not panic.
	kr.dispatchDelta(context.Background(), messages.KernelDeltaRequest{
		Source: messages.Tool,
		Delta: messages.StreamMessage{
			Type:  messages.StreamTypeSystemFullMessage,
			Value: messages.NewInferenceResultValue("tool", messages.NewTextMessage(messages.RoleTool, "result")),
		},
	})
}

func TestKernelRunner_LoopEndClosesMessageOutCh(t *testing.T) {
	kr := NewKernelRunner(nil, 8)
	ch := make(chan messages.KernelMessageRequest, 8)
	kr.messageOutCh = ch

	kr.dispatchDelta(context.Background(), messages.KernelDeltaRequest{
		Source: messages.System,
		Delta: messages.StreamMessage{
			Type:  messages.StreamTypeLoopEnd,
			Value: messages.NewLoopEndValue(),
		},
	})

	// Channel should be closed.
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("messageOutCh should be closed after LOOP.END, but received a value")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out: messageOutCh not closed after LOOP.END")
	}
}

func TestKernelRunner_LoopEndClosesDeltaEventCh(t *testing.T) {
	kr := NewKernelRunner(nil, 8)
	evCh := kr.NewDeltaEventReader(8)

	kr.dispatchDelta(context.Background(), messages.KernelDeltaRequest{
		Source: messages.System,
		Delta: messages.StreamMessage{
			Type:  messages.StreamTypeLoopEnd,
			Value: messages.NewLoopEndValue(),
		},
	})

	// LOOP.END is forwarded to deltaEventCh before the channel is closed
	// (it passes the StreamTypeSystemFullMessage filter).
	select {
	case got := <-evCh:
		if got.Type != messages.StreamTypeLoopEnd {
			t.Errorf("expected LOOP.END delta, got %s", got.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for LOOP.END delta")
	}

	// After the LOOP.END delta, the channel should be closed.
	select {
	case _, ok := <-evCh:
		if ok {
			t.Error("deltaEventCh should be closed after LOOP.END, but received a value")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out: deltaEventCh not closed after LOOP.END")
	}
}

func TestKernelRunner_LoopEndNilsChannels(t *testing.T) {
	kr := NewKernelRunner(nil, 8)
	ch := make(chan messages.KernelMessageRequest, 8)
	kr.messageOutCh = ch
	kr.NewDeltaEventReader(8)

	kr.dispatchDelta(context.Background(), messages.KernelDeltaRequest{
		Source: messages.System,
		Delta: messages.StreamMessage{
			Type:  messages.StreamTypeLoopEnd,
			Value: messages.NewLoopEndValue(),
		},
	})

	// Internal channels should be nil after LOOP.END.
	if kr.messageOutCh != nil {
		t.Error("messageOutCh should be nil after LOOP.END")
	}
	if kr.deltaEventCh != nil {
		t.Error("deltaEventCh should be nil after LOOP.END")
	}
}

// --- closeStreamWithError behavior ---

func TestKernelRunner_CloseStreamWithErrorClosesMessageOutCh(t *testing.T) {
	kr := NewKernelRunner(nil, 8)
	ch := make(chan messages.KernelMessageRequest, 8)
	kr.messageOutCh = ch

	kr.closeStreamWithError(errors.New("test error"))

	select {
	case _, ok := <-ch:
		if ok {
			t.Error("messageOutCh should be closed after closeStreamWithError")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out: messageOutCh not closed")
	}
}

func TestKernelRunner_CloseStreamWithErrorClosesDeltaEventCh(t *testing.T) {
	kr := NewKernelRunner(nil, 8)
	evCh := kr.NewDeltaEventReader(8)

	kr.closeStreamWithError(errors.New("test error"))

	select {
	case _, ok := <-evCh:
		if ok {
			t.Error("deltaEventCh should be closed after closeStreamWithError")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out: deltaEventCh not closed")
	}
}

func TestKernelRunner_CloseStreamWithErrorDrainsDeltaInbox(t *testing.T) {
	kr := NewKernelRunner(nil, 8)

	// Write some deltas to DeltaInbox before closing.
	ctx := context.Background()
	kr.DeltaInbox.Write(ctx, messages.KernelDeltaRequest{
		Source: messages.Model,
		Delta:  messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("stale1")},
	})
	kr.DeltaInbox.Write(ctx, messages.KernelDeltaRequest{
		Source: messages.Model,
		Delta:  messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("stale2")},
	})

	kr.closeStreamWithError(errors.New("retry"))

	// DeltaInbox should be empty after drain.
	if _, ok := kr.DeltaInbox.Read(); ok {
		t.Error("DeltaInbox should be drained after closeStreamWithError")
	}
}

func TestKernelRunner_CloseStreamWithErrorNilChannelsSafe(t *testing.T) {
	kr := NewKernelRunner(nil, 8)
	// Both messageOutCh and deltaEventCh are nil — should not panic.
	kr.closeStreamWithError(errors.New("no channels"))
}

func TestKernelRunner_CloseStreamWithErrorNilsChannels(t *testing.T) {
	kr := NewKernelRunner(nil, 8)
	ch := make(chan messages.KernelMessageRequest, 8)
	kr.messageOutCh = ch
	kr.NewDeltaEventReader(8)

	kr.closeStreamWithError(errors.New("err"))

	if kr.messageOutCh != nil {
		t.Error("messageOutCh should be nil after closeStreamWithError")
	}
	if kr.deltaEventCh != nil {
		t.Error("deltaEventCh should be nil after closeStreamWithError")
	}
}

// --- NewDeltaEventReader channel creation and replacement ---

func TestKernelRunner_NewDeltaEventReaderReplacesChannel(t *testing.T) {
	kr := NewKernelRunner(nil, 8)
	first := kr.NewDeltaEventReader(4)
	second := kr.NewDeltaEventReader(8)

	// The internal deltaEventCh should now be the second channel.
	// Dispatch a delta and verify it arrives on the second channel.
	kr.dispatchDelta(context.Background(), messages.KernelDeltaRequest{
		Source: messages.Model,
		Delta: messages.StreamMessage{
			Type:  messages.StreamTypeTextDelta,
			Value: messages.NewTextDeltaValue("replaced"),
		},
	})

	select {
	case got := <-second:
		if got.Type != messages.StreamTypeTextDelta {
			t.Errorf("Type: got %s, want %s", got.Type, messages.StreamTypeTextDelta)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting on second channel")
	}

	// First channel should have nothing.
	select {
	case <-first:
		t.Error("first channel should not receive after replacement")
	default:
		// expected
	}
}

// --- Tick with context cancellation ---

func TestKernelRunner_TickCancelledContext(t *testing.T) {
	kr := NewKernelRunner(nil, 8)
	ch := make(chan messages.KernelMessageRequest, 8)
	kr.messageOutCh = ch
	kr.NewDeltaEventReader(8)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := kr.Tick(ctx)
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}

	// Channels should be closed by closeStreamWithError.
	if kr.messageOutCh != nil {
		t.Error("messageOutCh should be nil after context cancellation")
	}
	if kr.deltaEventCh != nil {
		t.Error("deltaEventCh should be nil after context cancellation")
	}
}

func TestKernelRunner_TickProcessesDelta(t *testing.T) {
	kr := NewKernelRunner(nil, 8)
	evCh := kr.NewDeltaEventReader(8)

	ctx := context.Background()
	kr.DeltaInbox.Write(ctx, messages.KernelDeltaRequest{
		Source: messages.Model,
		Delta: messages.StreamMessage{
			Type:  messages.StreamTypeTextDelta,
			Value: messages.NewTextDeltaValue("tick data"),
		},
	})

	// Use a timeout context to prevent hanging.
	tickCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if err := kr.Tick(tickCtx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	select {
	case got := <-evCh:
		if got.Type != messages.StreamTypeTextDelta {
			t.Errorf("Type: got %s, want %s", got.Type, messages.StreamTypeTextDelta)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for delta from Tick")
	}
}

// A single loud frame (a cough, a door) must not cancel the response, while
// sustained speech does. Frames are held only until onset is decided; the
// cancel still precedes every interrupting frame at the provider, and a
// transient is released unchanged, in order.
func TestSessionModelRunner_TransientDoesNotBargeInButSustainedSpeechDoes(t *testing.T) {
	session := &playbackSession{recordingSession: newRecordingSession(), inputRate: 24000}
	runner := NewSessionModelRunner(nil, 16, nil)
	state := newInFlightRunState(t, session, runner, "resp-transient")
	cough, quiet := pcmFrameAtLevel(9000, 480), pcmFrameAtLevel(0, 480)
	sendUserAudio(t, runner, session, state, cough)
	sendUserAudio(t, runner, session, state, quiet)
	sent := session.sentMessages()
	if countSent(sent, messages.StreamTypeResponseCancel) != 0 || len(sent) != 2 ||
		string(audioDeltaContent(t, sent[0].Value)) != string(cough) || string(audioDeltaContent(t, sent[1].Value)) != string(quiet) {
		t.Fatalf("a 20 ms transient produced %#v, want the two frames forwarded in order and no cancel", sent)
	}
	for range 2 {
		sendUserAudio(t, runner, session, state, pcmFrameAtLevel(9000, 480))
	}
	sent = session.sentMessages()[2:]
	if len(sent) != 3 || sent[0].Type != messages.StreamTypeResponseCancel || sent[1].Type != messages.StreamTypeAudioDelta || sent[2].Type != messages.StreamTypeAudioDelta {
		t.Fatalf("40 ms of speech produced %#v, want RESPONSE.CANCEL before both speech frames", sent)
	}
}

// Held onset audio never falls behind a turn boundary queued after it.
func TestSessionModelRunner_HeldOnsetAudioPrecedesLaterControl(t *testing.T) {
	session := &playbackSession{recordingSession: newRecordingSession(), inputRate: 24000}
	runner := NewSessionModelRunner(nil, 16, nil)
	state := newInFlightRunState(t, session, runner, "resp-held")
	sendUserAudio(t, runner, session, state, pcmFrameAtLevel(9000, 480))
	runner.forwardQueuedSessionEvent(context.Background(), session, state, messages.StreamMessage{Type: messages.StreamTypeMessageEnd})
	sent := session.sentMessages()
	if len(sent) != 2 || sent[0].Type != messages.StreamTypeAudioDelta || sent[1].Type != messages.StreamTypeMessageEnd {
		t.Fatalf("sent %#v, want the held frame before MESSAGE.END", sent)
	}
}

// pcmFrameAtLevel returns samples of PCM16 whose RMS is level.
func pcmFrameAtLevel(level int16, samples int) []byte {
	pcm := make([]byte, samples*2)
	for i := 0; i < len(pcm); i += 2 {
		sample := level
		if i%4 == 0 {
			sample = -level
		}
		pcm[i], pcm[i+1] = byte(uint16(sample)), byte(uint16(sample)>>8)
	}
	return pcm
}

// An explicit host interrupt is the cancel; a frame held for onset is the
// interrupting audio. The cancel reaches the provider first.
func TestSessionModelRunner_ExplicitCancelPrecedesHeldOnsetAudio(t *testing.T) {
	session := &playbackSession{recordingSession: newRecordingSession(), inputRate: 24000}
	runner := NewSessionModelRunner(nil, 16, nil)
	state := newInFlightRunState(t, session, runner, "resp-explicit")
	sendUserAudio(t, runner, session, state, pcmFrameAtLevel(9000, 480))
	runner.forwardQueuedSessionEvent(context.Background(), session, state, messages.StreamMessage{Type: messages.StreamTypeResponseCancel, Value: messages.NewResponseCancelValue()})
	sent := session.sentMessages()
	if len(sent) != 2 || sent[0].Type != messages.StreamTypeResponseCancel || sent[1].Type != messages.StreamTypeAudioDelta {
		t.Fatalf("sent %#v, want RESPONSE.CANCEL before the held frame", sent)
	}
}

// A frame held because it could interrupt a response is released when that
// response ends: there is nothing left for it to interrupt.
func TestSessionModelRunner_HeldOnsetAudioReleasedWhenResponseEnds(t *testing.T) {
	session := &playbackSession{recordingSession: newRecordingSession(), inputRate: 24000}
	runner := NewSessionModelRunner(nil, 16, nil)
	state := newInFlightRunState(t, session, runner, "resp-ending")
	sendUserAudio(t, runner, session, state, pcmFrameAtLevel(9000, 480))
	runner.forwardSessionMessageState(context.Background(), session, state, sessionMessage(messages.StreamTypeMessageEnd, "resp-ending"))
	sent := session.sentMessages()
	if len(sent) != 1 || sent[0].Type != messages.StreamTypeAudioDelta {
		t.Fatalf("sent %#v, want the held frame released at the response's end", sent)
	}
}

// With sparse input (a relay that sends no silence) nothing follows a lone
// loud frame. Once onset can no longer be reached the frame is released.
func TestSessionModelRunner_HeldOnsetAudioReleasedAfterOnsetWindow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		session := newRecordingSession()
		runner := NewSessionModelRunner(&testSessionInferencer{session: session}, 16, nil)
		ctx, stop := context.WithCancel(t.Context())
		ran := make(chan error, 1)
		go func() { ran <- runner.Run(ctx) }()
		go func() {
			for _, ok := runner.DeltaOutbox.ReadBlockingContext(ctx); ok; _, ok = runner.DeltaOutbox.ReadBlockingContext(ctx) {
			}
		}()
		session.recv.Write(ctx, sessionMessage(messages.StreamTypeMessageStart, "resp-sparse"))
		synctest.Wait()
		if err := runner.EnqueueSessionAudioInput(ctx, pcmFrameAtLevel(9000, 480)); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if got := len(session.sentMessages()); got != 0 {
			t.Fatalf("sent %d messages while onset was undecided, want the frame held", got)
		}
		time.Sleep(DefaultBargeInConfig().MinSpeech)
		synctest.Wait()
		sent := session.sentMessages()
		stop()
		<-ran
		if len(sent) != 1 || sent[0].Type != messages.StreamTypeAudioDelta {
			t.Fatalf("sent %#v after the onset window, want only the released frame", sent)
		}
	})
}

// relayedSession models a provider response that is already open at the
// provider but still queued behind the relay that publishes provider messages
// to the runner. SyncReceive publishes it.
type relayedSession struct {
	*recordingSession
	queued []messages.StreamMessage
}

func (s *relayedSession) SyncReceive(ctx context.Context) {
	for _, msg := range s.queued {
		s.recv.Write(ctx, msg)
	}
	s.queued = nil
}

// Without a purpose echo the continuation binds to the first response opened
// after its request. A server-VAD response already open at the provider but
// not yet relayed would win that race, so the runner first publishes every
// provider message queued before the request: it then sees the response in
// flight and defers the continuation until it ends.
func TestSessionModelRunner_ContinuationRequestSyncsProviderMessagesFirst(t *testing.T) {
	ctx := context.Background()
	session := &relayedSession{recordingSession: newRecordingSession(), queued: []messages.StreamMessage{sessionMessage(messages.StreamTypeMessageStart, "resp-vad")}}
	runner := NewSessionModelRunner(nil, 16, nil)
	state := newSessionResponseState()
	runner.forwardQueuedSessionEvent(ctx, session, state, continuationCreate())
	if got := countSent(session.sentMessages(), messages.StreamTypeResponseCreate); got != 0 || state.continuationInFlight {
		t.Fatalf("continuation requested over the relayed server-VAD response: creates=%d state=%+v", got, state)
	}
	runner.forwardSessionMessageState(ctx, session, state, sessionMessage(messages.StreamTypeMessageEnd, "resp-vad"))
	runner.forwardSessionMessageState(ctx, session, state, sessionMessage(messages.StreamTypeMessageStart, "resp-continuation"))
	if countSent(session.sentMessages(), messages.StreamTypeResponseCreate) != 1 || state.continuationResponseID != "resp-continuation" {
		t.Fatalf("continuation bound to %q after the server-VAD response, want resp-continuation", state.continuationResponseID)
	}
}

// closingSession closes admission like a room shutting down: afterwards
// every send is rejected as closed.
type closingSession struct {
	*playbackSession
	closed bool
}

func (s *closingSession) SessionAdmissionClosed() bool { return s.closed }

func (s *closingSession) SendWithOutcome(ctx context.Context, msg messages.StreamMessage) messages.SessionSendOutcome {
	if s.closed {
		return messages.SessionSendOutcome{Status: messages.SessionSendClosed}
	}
	s.Send(ctx, msg)
	return messages.SessionSendOutcome{Status: messages.SessionSendSucceeded}
}

// Room shutdown closes admission before it cancels. Onset audio held across
// that boundary is discarded like any other late frame, not reported as a
// terminal audio failure.
func TestSessionModelRunner_HeldOnsetAudioDiscardedAfterAdmissionCloses(t *testing.T) {
	session := &closingSession{playbackSession: &playbackSession{recordingSession: newRecordingSession(), inputRate: 24000}}
	runner := NewSessionModelRunner(nil, 16, nil)
	state := newInFlightRunState(t, session, runner, "resp-closing")
	sendUserAudio(t, runner, session, state, pcmFrameAtLevel(9000, 480))
	session.closed = true
	runner.forwardSessionMessageState(context.Background(), session, state, sessionMessage(messages.StreamTypeMessageEnd, "resp-closing"))
	for msg, ok := runner.DeltaOutbox.Read(); ok; msg, ok = runner.DeltaOutbox.Read() {
		if msg.Type == messages.StreamTypeError {
			t.Fatalf("published %#v after admission closed, want the held frame discarded", msg.Value)
		}
	}
	if len(state.heldAudio) != 0 || len(session.sentMessages()) != 0 {
		t.Fatalf("held=%d sent=%#v, want the held frame discarded", len(state.heldAudio), session.sentMessages())
	}
}
