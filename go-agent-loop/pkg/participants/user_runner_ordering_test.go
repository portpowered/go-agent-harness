package participants

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// TestUserRunnerConcurrentWritesTakeUniqueOrderedIndices covers
// AgentLoop.Send and SendInterrupt writing the user participant from separate
// goroutines: every message takes its own actor index and reaches the outbox
// in index order.
func TestUserRunnerConcurrentWritesTakeUniqueOrderedIndices(t *testing.T) {
	const writers, perWriter = 4, 50
	runner := NewUserRunner(writers * perWriter)
	var group sync.WaitGroup
	for writer := range writers {
		group.Go(func() {
			for index := range perWriter {
				msg := messages.NewTextMessage(messages.RoleUser, fmt.Sprintf("w%d-%d", writer, index))
				var err error
				if index%2 == 0 {
					err = runner.Write(context.Background(), msg)
				} else {
					err = runner.Stop(context.Background())
				}
				if err != nil {
					t.Errorf("writer %d message %d: %v", writer, index, err)
				}
			}
		})
	}
	group.Wait()

	if got := runner.Outbox.Len(); got != writers*perWriter {
		t.Fatalf("outbox holds %d messages, want %d", got, writers*perWriter)
	}
	for want := range writers * perWriter {
		response, ok := runner.Outbox.Read()
		if !ok {
			t.Fatalf("outbox ended at index %d", want)
		}
		if got := response.Message.ActorProvidedIndex; got != want {
			t.Fatalf("message %d has actor index %d, want %d", want, got, want)
		}
		if response.Message.ActorID != messages.User {
			t.Fatalf("message %d actor = %v, want user", want, response.Message.ActorID)
		}
	}
}
