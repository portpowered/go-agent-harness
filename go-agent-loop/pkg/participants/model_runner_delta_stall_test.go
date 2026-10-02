package participants

import (
	"context"
	"testing"
	"testing/synctest"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
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
