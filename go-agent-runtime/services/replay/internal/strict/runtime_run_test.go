package strict

import (
	"context"
	"io"
	"testing"
	"testing/synctest"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// floodingReplayLoop answers every input frame with more must-deliver deltas
// than its delta outbox holds, before the input call returns: a provider
// that responds while input is still being sent.
type floodingReplayLoop struct {
	deltas   *messages.TypedBuffer[messages.StreamMessage]
	perFrame int
}

func (l *floodingReplayLoop) Run(ctx context.Context) error {
	l.deltas.WriteWaitContext(ctx, messages.StreamMessage{Type: messages.StreamTypeSessionOpen})
	<-ctx.Done()
	return ctx.Err()
}

func (l *floodingReplayLoop) Deltas() *messages.TypedBuffer[messages.StreamMessage] { return l.deltas }

func (l *floodingReplayLoop) Send(context.Context, []messages.Message) error { return nil }

func (l *floodingReplayLoop) SendAudioInput(ctx context.Context, _ []byte) error {
	for range l.perFrame {
		if err := l.writeMustDeliver(ctx, messages.StreamTypeToolCallDelta); err != nil {
			return err
		}
	}
	return nil
}

// SendSessionEventWaiting ends the turn; the provider's response ends behind
// the flooded deltas.
func (l *floodingReplayLoop) SendSessionEventWaiting(ctx context.Context, _ messages.StreamMessage) error {
	return l.writeMustDeliver(ctx, messages.StreamTypeMessageEnd)
}

func (l *floodingReplayLoop) writeMustDeliver(ctx context.Context, kind messages.StreamMessageType) error {
	if outcome := messages.WriteStreamDelta(ctx, l.deltas, messages.StreamMessage{Type: kind, ActorID: messages.Model}); !outcome.OK() {
		return outcome.Err
	}
	return nil
}

// Replay must drain deltas while it sends input. A provider that emits more
// must-deliver deltas during input than the outbox holds would otherwise
// block the input send forever, and replay would never read again.
func TestStrictReplayDrainsDeltasWhileSendingInput(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		loop := &floodingReplayLoop{deltas: messages.NewTypedBuffer[messages.StreamMessage](2), perFrame: 8}
		runtime := &coreRuntime{
			loop:    loop,
			actions: []replayInputAction{{audio: [][]byte{{0, 0}, {0, 0}, {0, 0}}, responseEnds: 1}},
		}
		// Run returns only once replay has read the response's MESSAGE.END,
		// which the provider queues behind the flooded input.
		if err := runtime.Run(context.Background(), io.Discard); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if queued := loop.deltas.Len(); queued != 0 {
			t.Fatalf("replay left %d deltas unread", queued)
		}
	})
}
