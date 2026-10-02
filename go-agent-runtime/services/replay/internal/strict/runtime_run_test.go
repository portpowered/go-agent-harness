package strict

import (
	"context"
	"strings"
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
		msg := messages.StreamMessage{Type: messages.StreamTypeToolCallDelta, ActorID: messages.Model}
		if outcome := messages.WriteStreamDelta(ctx, l.deltas, msg); !outcome.OK() {
			return outcome.Err
		}
	}
	return nil
}

func (l *floodingReplayLoop) SendSessionEventWaiting(ctx context.Context, _ messages.StreamMessage) error {
	text := messages.StreamMessage{Type: messages.StreamTypeTextDelta, ActorID: messages.Model, Value: messages.NewTextDeltaValue("done")}
	end := messages.StreamMessage{Type: messages.StreamTypeMessageEnd, ActorID: messages.Model}
	for _, msg := range []messages.StreamMessage{text, end} {
		if outcome := messages.WriteStreamDelta(ctx, l.deltas, msg); !outcome.OK() {
			return outcome.Err
		}
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
		var out strings.Builder
		if err := runtime.Run(context.Background(), &out); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if out.String() != "done" {
			t.Fatalf("replay output = %q, want the response text after the flooded input", out.String())
		}
	})
}
