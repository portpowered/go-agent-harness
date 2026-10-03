package service

import (
	"context"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// toolGate serializes the tools that are not safe to run concurrently. Each
// such tool has its own lock, shared by every delegation of the session, so
// one call of it runs at a time; other tools run as concurrently as the
// delegations do. The voice loop is never involved.
type toolGate struct {
	serialized func(string) bool

	mu    sync.Mutex
	locks map[string]chan struct{}
}

func newToolGate(serialized func(string) bool) *toolGate {
	return &toolGate{serialized: serialized, locks: make(map[string]chan struct{})}
}

// acquire waits for name's lock, or returns a no-op release for a
// concurrency-safe tool. It gives up when ctx ends.
func (g *toolGate) acquire(ctx context.Context, name string) (func(), error) {
	if g.serialized == nil || !g.serialized(name) {
		return func() {}, nil
	}
	g.mu.Lock()
	lock, ok := g.locks[name]
	if !ok {
		lock = make(chan struct{}, 1)
		g.locks[name] = lock
	}
	g.mu.Unlock()
	select {
	case lock <- struct{}{}:
		return func() { <-lock }, nil
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	}
}

// delegationTools runs the session's tools for one delegation: it takes the
// tool's lock when the tool is serialized, and reports each call as quiet
// progress before it runs.
type delegationTools struct {
	inner    messages.ToolExecutor
	gate     *toolGate
	progress func(context.Context, messages.ToolCall)
}

func (t delegationTools) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	release, err := t.gate.acquire(ctx, call.Name)
	if err != nil {
		return messages.ToolCallResponse{}, err
	}
	defer release()
	if t.progress != nil {
		t.progress(ctx, call)
	}
	return t.inner.Execute(ctx, call)
}
