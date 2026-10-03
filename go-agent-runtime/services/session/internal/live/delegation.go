package live

import (
	"context"
	"errors"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/livedelegation"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
)

// openDelegations starts the session's delegation executor when the live
// service has one and the request carries a delegation policy
// (gpt-live-provider.md 2.5). Its context is the run context, so only the
// end of the session (or the executor's own Cancel) stops a delegation: a
// user interrupt cancels the loop's tool and model executions, never this.
// Delegations reuse the session's tool executor under the same capability
// surface as the provider would, and take the resource-group locks the voice
// loop's tool calls take (tool_locks.go): browser and WebMCP tools share one
// lock, filesystem writes another, and each other long-running tool its own.
func (h *handle) openDelegations(ctx context.Context, loop *agentloop.AgentLoop, toolExecutor messages.ToolExecutor) error {
	if h.delegationService == nil || h.request.Delegation == nil {
		return nil
	}
	h.mu.Lock()
	explicitCapability := h.request.Capabilities != nil && !h.request.Capabilities.InheritDefaults
	locks := h.toolLocks
	h.mu.Unlock()
	var toolLock livedelegation.ToolLock
	if locks != nil {
		toolLock = locks.acquire
	}
	executor, err := h.delegationService.Open(ctx, livedelegation.Binding{
		SessionID:   h.request.SessionID,
		Policy:      *h.request.Delegation,
		Tools:       restrictToolExecutor(toolExecutor, h.offeredToolDefinitions, explicitCapability),
		Definitions: h.offeredToolDefinitions,
		ToolLock:    toolLock,
		History:     loop.GetConversationHistory,
		Append:      h.appendDelegationContext,
		OnTool:      h.recordDelegationTool,
		Scheduler:   h.scheduler,
	})
	if err != nil {
		return fmt.Errorf("open live delegation executor: %w", err)
	}
	h.mu.Lock()
	h.delegations = executor
	h.mu.Unlock()
	return nil
}

// submitDelegation hands one DELEGATION.CREATED to the executor. It is an
// observation: it never becomes a loop tool call or a response request.
func (h *handle) submitDelegation(msg messages.StreamMessage) {
	value, ok := msg.Value.(*messages.DelegationCreatedValue)
	if !ok || value == nil {
		return
	}
	h.mu.Lock()
	executor := h.delegations
	h.mu.Unlock()
	if executor == nil {
		return
	}
	// Submit fails only after Close, when the session is over and nobody
	// is left to answer.
	if err := executor.Submit(*value); err != nil && !errors.Is(err, livedelegation.ErrClosed) {
		h.Cancel(fmt.Errorf("submit live delegation %q: %w", value.ID, err))
	}
}

// closeDelegations cancels the delegations still running at the end of the
// session and waits for their workers.
func (h *handle) closeDelegations() error {
	h.mu.Lock()
	executor := h.delegations
	h.delegations = nil
	h.mu.Unlock()
	if executor == nil {
		return nil
	}
	return executor.Close()
}

// appendDelegationContext sends one CONTEXT.APPEND through the session's
// ordered ingress and waits until the provider admits or rejects it, so a
// provider without the channel reports a failure instead of a silent drop.
func (h *handle) appendDelegationContext(ctx context.Context, value *messages.ContextAppendValue) error {
	loop, err := h.liveControlLoop()
	if err != nil {
		return err
	}
	ackID, ack, err := h.media.RegisterAck()
	if err != nil {
		return err
	}
	event := messages.StreamMessage{Type: messages.StreamTypeContextAppend, Value: value, ActorProvidedID: ackID}
	if err := loop.SendSessionEventWaiting(ctx, event); err != nil {
		h.media.AbortAck(ackID)
		return liveInputError(err)
	}
	select {
	case accepted := <-ack:
		if !accepted {
			return errors.New("live provider rejected the delegation context append")
		}
		return nil
	case <-ctx.Done():
		h.media.CancelAck(ackID)
		return ctx.Err()
	}
}

// recordDelegationTool publishes a delegation's tool call as session
// evidence, so the call is auditable like the voice loop's: the event stream
// and the invocation recorder both receive it, tagged with the delegation
// id, with the tool's whole arguments and result: recorders keep
// LiveDelegationTool.Audited, redacted with the session credentials and then
// bounded. It is a distinct kind, never a TOOLCALL message, so no observer
// counts it as a provider tool call that owes a result.
func (h *handle) recordDelegationTool(event livedelegation.ToolEvent) {
	kind := session.LiveEventDelegationToolCall
	tool := &session.LiveDelegationTool{Name: event.Call.Name, Arguments: event.Call.Arguments}
	if event.Done {
		kind = session.LiveEventDelegationToolResult
		tool.Result = event.Response.Content
	}
	h.publish(session.LiveEvent{
		Kind: string(kind), SessionID: h.request.SessionID, Critical: true,
		ItemID: event.DelegationID, ToolCallID: event.Call.ID, Text: event.Call.Name, Error: event.Err,
		DelegationTool: tool,
	}, false)
}
