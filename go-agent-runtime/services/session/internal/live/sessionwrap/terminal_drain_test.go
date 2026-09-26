package sessionwrap

import (
	"context"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

type queuedProvider struct {
	receive *messages.TypedBuffer[messages.StreamMessage]
	done    chan struct{}
}

func (*queuedProvider) Send(context.Context, messages.StreamMessage) bool { return true }
func (p *queuedProvider) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return p.receive
}
func (p *queuedProvider) Done() <-chan struct{} { return p.done }
func (p *queuedProvider) Close() error {
	select {
	case <-p.done:
	default:
		close(p.done)
	}
	return nil
}

// Provider output media is published before the relay forwards the matching
// lifecycle messages. SyncReceive must make every message the provider had
// already queued visible to the runner, in order, before input is admitted.
func TestTerminalDrainSyncReceivePublishesQueuedProviderMessages(t *testing.T) {
	ctx := context.Background()
	provider := &queuedProvider{receive: messages.NewTypedBuffer[messages.StreamMessage](64), done: make(chan struct{})}
	session := WrapSession(ctx, provider, false, 64)
	t.Cleanup(func() {
		if err := session.Close(); err != nil {
			t.Errorf("close relay session: %v", err)
		}
	})
	syncer, ok := session.(ReceiveSyncer)
	if !ok {
		t.Fatal("terminal drain session does not expose SyncReceive")
	}
	for round := range 50 {
		ids := []string{"response-a", "response-b", "response-c"}
		for _, id := range ids {
			provider.receive.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeMessageStart, ResponseID: id})
		}
		syncer.SyncReceive(ctx)
		for _, want := range ids {
			got, ok := session.Receive().Read()
			if !ok {
				t.Fatalf("round %d: message %q was still queued behind the relay after SyncReceive", round, want)
			}
			if got.ResponseID != want {
				t.Fatalf("round %d: relayed %q, want %q in provider order", round, got.ResponseID, want)
			}
		}
	}
}

func TestTerminalDrainSyncReceiveReturnsAfterClose(t *testing.T) {
	ctx := context.Background()
	provider := &queuedProvider{receive: messages.NewTypedBuffer[messages.StreamMessage](4), done: make(chan struct{})}
	session := WrapSession(ctx, provider, false, 4)
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	syncer, ok := session.(ReceiveSyncer)
	if !ok {
		t.Fatal("terminal drain session does not expose SyncReceive")
	}
	syncer.SyncReceive(ctx)
}
