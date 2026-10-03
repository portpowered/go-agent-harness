package service

import (
	"context"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/livedelegation"
)

// delegationTools runs the session's tools for one delegation: it takes the
// session's resource-group lock for the tool, the one the voice loop takes
// too, and reports each call as quiet progress before it runs.
type delegationTools struct {
	inner    messages.ToolExecutor
	lock     livedelegation.ToolLock
	progress func(context.Context, messages.ToolCall)
	observe  func(livedelegation.ToolEvent)
}

// Execute runs one call. The host observes its start and its end, including
// a call that never ran because its delegation ended while it waited.
func (t delegationTools) Execute(ctx context.Context, call messages.ToolCall) (response messages.ToolCallResponse, err error) {
	t.notify(livedelegation.ToolEvent{Call: call})
	defer func() { t.notify(livedelegation.ToolEvent{Call: call, Done: true, Response: response, Err: err}) }()
	if t.lock != nil {
		release, err := t.lock(ctx, call.Name)
		if err != nil {
			return messages.ToolCallResponse{}, err
		}
		defer release()
	}
	if t.progress != nil {
		t.progress(ctx, call)
	}
	return t.inner.Execute(ctx, call)
}

func (t delegationTools) notify(event livedelegation.ToolEvent) {
	if t.observe != nil {
		t.observe(event)
	}
}
