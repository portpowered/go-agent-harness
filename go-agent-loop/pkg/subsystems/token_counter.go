package subsystems

import (
	"context"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/state"
)

// TokenCounter counts tokens in the conversation buffer.
type TokenCounter interface {
	Count(messages []messages.Message) int
}

// TokenCounterSubsystem monitors token usage and can signal truncation.
type TokenCounterSubsystem struct {
	counter   TokenCounter
	maxTokens int
}

func NewTokenCounter(counter TokenCounter, maxTokens int) *TokenCounterSubsystem {
	return &TokenCounterSubsystem{
		counter:   counter,
		maxTokens: maxTokens,
	}
}

var _ Subsystem = (*TokenCounterSubsystem)(nil)

func (h *TokenCounterSubsystem) TickGroup() TickGroup {
	return TickGroupTokenCounter
}

func (h *TokenCounterSubsystem) Execute(ctx context.Context, curr *state.LoopState) error {
	if h.counter == nil || h.maxTokens <= 0 {
		return nil
	}

	count := h.counter.Count(curr.History.ConversationBuffer)
	if count <= h.maxTokens {
		return nil
	}

	// Truncate oldest non-system messages until under the token limit.
	msgs := curr.History.ConversationBuffer
	removed := make([]bool, len(msgs))
	remaining := count
	for i, msg := range msgs {
		if remaining <= h.maxTokens {
			break
		}
		if msg.Role == messages.RoleSystem {
			continue // preserve system messages
		}
		removed[i] = true
		remaining -= h.counter.Count([]messages.Message{msg})
	}

	var kept []messages.Message
	for i, msg := range msgs {
		if !removed[i] {
			kept = append(kept, msg)
		}
	}
	curr.History.ConversationBuffer = kept

	return nil
}
