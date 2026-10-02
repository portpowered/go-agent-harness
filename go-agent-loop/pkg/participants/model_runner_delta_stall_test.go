package participants

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

// A stalled delta consumer must not delay barge-in. MESSAGE.START and
// tool-call deltas must be delivered, so they wait for outbox capacity; the
// session goroutine that decides barge-in must keep observing user audio
// while that wait is outstanding.
func TestSessionModelRunner_BargeInCancelsWhileDeltaConsumerStalled(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		session := newRecordingSession()
		// A one-slot outbox that nobody reads: MESSAGE.START fills it, so the
		// tool-call delta behind it has to wait.
		runner := NewSessionModelRunner(&testSessionInferencer{session: session}, 1, nil)
		ctx, cancel := context.WithCancel(context.Background())
		errCh := make(chan error, 1)
		go func() { errCh <- runner.Run(ctx) }()
		defer func() {
			cancel()
			<-errCh
		}()

		stalled := []messages.StreamMessage{
			{Type: messages.StreamTypeMessageStart, ResponseID: "resp-1", Value: messages.NewMessageStartValue()},
			{Type: messages.StreamTypeToolCallStart, ResponseID: "resp-1", Value: messages.NewToolCallStartValue("call-1", "lookup")},
			{Type: messages.StreamTypeToolCallStart, ResponseID: "resp-1", Value: messages.NewToolCallStartValue("call-2", "lookup")},
		}
		for _, msg := range stalled {
			session.recv.Write(ctx, msg)
		}
		synctest.Wait()

		enqueueTestAudio(t, runner, loudPCM())
		synctest.Wait()

		sent := session.sentMessages()
		if countSent(sent, messages.StreamTypeResponseCancel) != 1 {
			t.Fatalf("provider-bound messages while the consumer is stalled = %#v, want one RESPONSE.CANCEL", sent)
		}
		if sent[0].Type != messages.StreamTypeResponseCancel || sent[len(sent)-1].Type != messages.StreamTypeAudioDelta {
			t.Fatalf("provider-bound messages = %#v, want RESPONSE.CANCEL before the interrupting audio", sent)
		}

		// Once the consumer resumes, every must-deliver delta arrives in order.
		for _, want := range stalled {
			got, ok := runner.DeltaOutbox.ReadBlocking(ctx.Done())
			if !ok {
				t.Fatalf("outbox closed before %s was delivered", want.Type)
			}
			if got.Type != want.Type || got.ResponseID != want.ResponseID {
				t.Fatalf("delivered %s/%s, want %s/%s", got.Type, got.ResponseID, want.Type, want.ResponseID)
			}
		}
	})
}

// While a must-deliver delta waits for a stalled consumer, ordinary deltas
// queue behind it in order while the queue is within the outbox capacity;
// beyond that they are shed and counted as outbox drops.
func TestSessionOutbox_OrdinaryDeltasShedBehindStalledMustDeliver(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		runner := NewSessionModelRunner(nil, 2, nil)
		text := func(content string) messages.StreamMessage {
			return messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue(content)}
		}
		end := messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Value: messages.NewMessageEndValue(messages.TokenUsage{})}
		for _, msg := range []messages.StreamMessage{text("a"), text("b"), end, text("c"), text("d")} {
			runner.writeSessionDelta(ctx, msg) // never blocks the caller
		}
		synctest.Wait()
		if drops := runner.DeltaOutbox.Drops(); drops != 1 {
			t.Fatalf("outbox drops = %d, want only the ordinary delta beyond the queue bound shed", drops)
		}

		var got []string
		for range 4 {
			msg, _ := runner.DeltaOutbox.ReadBlocking(ctx.Done())
			if value, ok := msg.Value.(*messages.TextDeltaValue); ok {
				got = append(got, value.Content)
			} else {
				got = append(got, string(msg.Type))
			}
			synctest.Wait()
		}
		want := []string{"a", "b", string(messages.StreamTypeMessageEnd), "c"}
		if len(got) != len(want) {
			t.Fatalf("delivered %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("delivered %v, want %v", got, want)
			}
		}
		runner.sessionOut.flush(ctx)
	})
}

// A terminal failure published while must-deliver deltas are still queued
// for a stalled consumer follows them: the held onset frame released at
// MESSAGE.END fails to send, and its ERROR must not overtake or evict that
// MESSAGE.END.
func TestSessionModelRunner_HeldAudioFailureFollowsQueuedMessageEnd(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		session := &outcomeRecordingSession{
			recordingSession: newRecordingSession(),
			outcomes: map[messages.StreamMessageType]messages.SessionSendOutcome{
				messages.StreamTypeAudioDelta: {Status: messages.SessionSendClosed},
			},
		}
		runner := NewSessionModelRunner(nil, 1, nil)
		state := newInFlightRunState(t, session, runner, "resp-1") // MESSAGE.START fills the outbox
		state.Onset.Hold(loudPCM(), clock.Real{}, time.Second)

		runner.forwardSessionMessageState(ctx, session, state, sessionMessage(messages.StreamTypeMessageEnd, "resp-1"))
		synctest.Wait()

		want := []messages.StreamMessageType{messages.StreamTypeMessageStart, messages.StreamTypeMessageEnd, messages.StreamTypeError}
		for _, kind := range want {
			got, ok := runner.DeltaOutbox.ReadBlocking(ctx.Done())
			if !ok || got.Type != kind {
				t.Fatalf("delivered %s (ok=%t), want %s; order must be %v", got.Type, ok, kind, want)
			}
		}
		if drops := runner.DeltaOutbox.Drops(); drops != 0 {
			t.Fatalf("outbox drops = %d, want none", drops)
		}
		runner.sessionOut.flush(ctx)
	})
}

// A consumer that never reads cannot grow the session outbox without bound:
// at the queue limit the session ends with ErrSessionDeltaOverflow and
// publishes that failure as its terminal ERROR.
func TestSessionModelRunner_StalledConsumerOverflowEndsSession(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		session := newRecordingSession()
		runner := NewSessionModelRunner(&testSessionInferencer{session: session}, 1, nil)
		runner.sessionDeltas().limit = 3
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		errCh := make(chan error, 1)
		go func() { errCh <- runner.Run(ctx) }()

		for index := range 8 {
			session.recv.Write(ctx, messages.StreamMessage{
				Type:       messages.StreamTypeToolCallStart,
				ResponseID: "resp-1",
				Value:      messages.NewToolCallStartValue(fmt.Sprintf("call-%d", index), "lookup"),
			})
		}
		synctest.Wait()

		select {
		case err := <-errCh:
			if !errors.Is(err, ErrSessionDeltaOverflow) {
				t.Fatalf("Run error = %v, want ErrSessionDeltaOverflow", err)
			}
		default:
			t.Fatal("Run kept running with an overflowed session outbox")
		}
		got, ok := runner.DeltaOutbox.Read()
		value, isError := got.Value.(*messages.ErrorValue)
		if !ok || !isError || value.Classification != sessionDeltaOverflowClassification || !errors.Is(value.Err, ErrSessionDeltaOverflow) {
			t.Fatalf("terminal delta = %#v, want the session_delta_overflow ERROR", got)
		}
		// Each tool-call delta the session forwarded before it ended is
		// dropped exactly once: shed from the abandoned queue, shed after
		// overflow, or evicted by the terminal ERROR. Deltas the provider
		// queued after the session ended were never forwarded.
		forwarded := int64(8 - session.recv.Len())
		if drops := runner.DeltaOutbox.Drops(); drops != forwarded {
			t.Fatalf("outbox drops = %d, want one per forwarded delta (%d)", drops, forwarded)
		}
	})
}

// Once the session context ends, the pump writes nothing more that is not
// terminal, and flush returns only after it exited: no delta is written to
// DeltaOutbox after the session is done with it.
func TestSessionOutbox_NothingWrittenAfterSessionEnds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		runner := NewSessionModelRunner(nil, 1, nil)
		start := messages.StreamMessage{Type: messages.StreamTypeMessageStart, Value: messages.NewMessageStartValue()}
		end := messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Value: messages.NewMessageEndValue(messages.TokenUsage{})}
		runner.writeSessionDelta(ctx, start)
		runner.writeSessionDelta(ctx, end) // queued behind the full outbox
		cancel()
		runner.sessionOut.flush(ctx)

		if got, _ := runner.DeltaOutbox.Read(); got.Type != messages.StreamTypeMessageStart {
			t.Fatalf("outbox head = %s, want the MESSAGE.START written before the session ended", got.Type)
		}
		synctest.Wait()
		if queued := runner.DeltaOutbox.Len(); queued != 0 {
			t.Fatalf("outbox holds %d deltas written after the session ended", queued)
		}
	})
}

// A provider delegation reaches the delta consumer even when it arrives while
// the outbox is full of speech audio: the audio around it may be shed, the
// delegation never is, because the provider never times one out.
func TestSessionModelRunner_DelegationSurvivesFullOutbox(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		session := newRecordingSession()
		runner := NewSessionModelRunner(&testSessionInferencer{session: session}, 1, nil)
		ctx, cancel := context.WithCancel(context.Background())
		errCh := make(chan error, 1)
		go func() { errCh <- runner.Run(ctx) }()
		defer func() {
			cancel()
			<-errCh
		}()

		audio := messages.StreamMessage{Type: messages.StreamTypeAudioDelta, ResponseID: "live_seg_1", Value: messages.NewAudioDeltaValue([]byte{1, 2})}
		delegation := messages.StreamMessage{Type: messages.StreamTypeDelegationCreated, Value: messages.NewDelegationCreatedValue("del_1", messages.DelegationTargetClient, 3600, nil)}
		for _, msg := range []messages.StreamMessage{audio, audio, audio, delegation, audio, audio} {
			session.recv.Write(ctx, msg)
		}
		synctest.Wait()
		if runner.DeltaOutbox.Drops() == 0 {
			t.Fatal("outbox drops = 0, want the stalled consumer to shed ordinary audio")
		}

		// Drain on virtual time: once the outbox stays empty for a second the
		// delegation was dropped, which fails here instead of deadlocking.
		for {
			var got messages.StreamMessage
			select {
			case got = <-runner.DeltaOutbox.Chan():
			case <-time.After(time.Second):
				t.Fatal("the outbox drained without DELEGATION.CREATED: it was dropped")
			}
			if got.Type == messages.StreamTypeDelegationCreated {
				value, ok := got.Value.(*messages.DelegationCreatedValue)
				if !ok || value.ID != "del_1" || got.ResponseID != "" {
					t.Fatalf("delivered delegation %+v (response %q), want del_1 with no response id", value, got.ResponseID)
				}
				return
			}
			synctest.Wait()
		}
	})
}
