package service

import (
	"context"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/livedelegation"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

// budget wraps the backend inferencer of one delegation and enforces its
// turn and token limits: an inference beyond either fails before it reaches
// the backend, which ends the nested loop. It also records the time budget
// when its timer cancels the loop.
type budget struct {
	inner  messages.Inferencer
	limits livedelegation.Limits

	mu     sync.Mutex
	turns  int
	tokens int
	err    error
}

func newBudget(inner messages.Inferencer, limits livedelegation.Limits) *budget {
	return &budget{inner: inner, limits: limits}
}

// exceeded returns the first budget the delegation ran out of, or nil.
func (b *budget) exceeded() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.err
}

func (b *budget) fail(err error) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.err == nil {
		b.err = err
	}
	return b.err
}

// admit counts one inference, or refuses it when a budget is spent.
func (b *budget) admit() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch {
	case b.err != nil:
		return b.err
	case b.turns >= b.limits.MaxTurns:
		b.err = livedelegation.ErrTurnBudget
		return b.err
	case b.tokens >= b.limits.MaxTokens:
		b.err = livedelegation.ErrTokenBudget
		return b.err
	}
	b.turns++
	return nil
}

// uncount returns a turn the backend refused to stream, so the loop's
// non-streaming fallback for the same turn is admitted once.
func (b *budget) uncount() {
	b.mu.Lock()
	b.turns--
	b.mu.Unlock()
}

// tokensOf is an inference's reported total.
func tokensOf(usage messages.TokenUsage) int {
	if usage.TotalTokens > 0 {
		return usage.TotalTokens
	}
	return usage.PromptTokens + usage.CompletionTokens
}

func (b *budget) addTokens(used int) {
	if used <= 0 {
		return
	}
	b.mu.Lock()
	b.tokens += used
	b.mu.Unlock()
}

// Infer is the non-streaming path the loop falls back to.
func (b *budget) Infer(ctx context.Context, req messages.InferenceRequest) (messages.InferenceResult, error) {
	if err := b.admit(); err != nil {
		return messages.InferenceResult{}, err
	}
	result, err := b.inner.Infer(ctx, req)
	b.addTokens(tokensOf(result.TokenUsage))
	return result, err
}

// InferStream forwards the backend stream and counts the inference's
// reported usage. A provider may report usage on both MESSAGE.END and
// USAGE.INFO, so the larger report counts once. Usage is counted before the
// message carrying it is forwarded, so the loop's next inference is admitted
// against it.
func (b *budget) InferStream(ctx context.Context, req messages.InferenceRequest) (<-chan messages.StreamMessage, error) {
	if err := b.admit(); err != nil {
		return nil, err
	}
	inner, err := b.inner.InferStream(ctx, req)
	if err != nil || inner == nil {
		b.uncount()
		return inner, err
	}
	out := make(chan messages.StreamMessage)
	go b.forward(ctx, inner, out)
	return out, nil
}

func (b *budget) forward(ctx context.Context, inner <-chan messages.StreamMessage, out chan<- messages.StreamMessage) {
	defer close(out)
	counted := 0
	for msg := range inner {
		if reported, ok := reportedTokens(msg); ok && reported > counted {
			b.addTokens(reported - counted)
			counted = reported
		}
		select {
		case out <- msg:
		case <-ctx.Done():
			// The loop stopped reading; drain so the backend can finish.
			for range inner {
			}
			return
		}
	}
}

// reportedTokens is the usage a stream message reports, if any.
func reportedTokens(msg messages.StreamMessage) (int, bool) {
	switch value := msg.Value.(type) {
	case *messages.MessageEndValue:
		return tokensOf(value.Usage), true
	case *messages.UsageInfoValue:
		return tokensOf(value.Usage), true
	default:
		return 0, false
	}
}

// withTimeBudget returns a context that the budget's timer cancels after
// MaxDuration on scheduler. stop releases the timer and its watcher.
func (b *budget) withTimeBudget(ctx context.Context, scheduler platformclock.TimerSource) (context.Context, func()) {
	budgetCtx, cancel := context.WithCancelCause(ctx)
	timer := scheduler.NewTimer(b.limits.MaxDuration)
	done := make(chan struct{})
	watcher := make(chan struct{})
	go func() {
		defer close(watcher)
		select {
		case <-timer.C():
			cancel(b.fail(livedelegation.ErrTimeBudget))
		case <-done:
		case <-budgetCtx.Done():
		}
	}()
	return budgetCtx, func() {
		timer.Stop()
		close(done)
		<-watcher
		cancel(nil)
	}
}
