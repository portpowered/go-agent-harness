package agentruntime

import (
	"context"
	"errors"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	duration "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
)

func (r *durationServiceResources) handle(ctx context.Context, loop duration.Loop, controller duration.Controller, msg messages.StreamMessage, state duration.RunState) (duration.MessageResult, error) {
	concrete, err := durationAgentLoop(loop)
	if err != nil {
		return duration.MessageResult{State: &state}, err
	}
	return r.handleMessage(ctx, concrete, msg, state)
}

func durationAgentLoop(loop duration.Loop) (*agentloop.AgentLoop, error) {
	concrete, ok := loop.(*agentloop.AgentLoop)
	if !ok || concrete == nil {
		return nil, errors.New("session duration loop adapter is invalid")
	}
	return concrete, nil
}

func (r *durationServiceResources) handleMessage(ctx context.Context, loop *agentloop.AgentLoop, msg messages.StreamMessage, state duration.RunState) (duration.MessageResult, error) {
	if err := r.prepareMessage(ctx, loop, msg, &state); err != nil {
		return duration.MessageResult{State: &state}, err
	}
	return r.finishMessage(ctx, loop, msg, state)
}

func (r *durationServiceResources) prepareMessage(ctx context.Context, loop *agentloop.AgentLoop, msg messages.StreamMessage, state *duration.RunState) error {
	if msg.Type == messages.StreamTypeSessionCreated && r.publisher != nil {
		r.publisher.markSessionReady()
	}
	if msg.Type == messages.StreamTypeSessionOpen {
		if err := r.open(ctx, loop, state); err != nil {
			return err
		}
	}
	if shouldDispatchScheduledAudioForMessage(msg, r.opts.ScheduledAudioDispatch) && r.opts.observer != nil {
		if err := r.opts.observer.dispatchScheduledInputs(ctx, loop); err != nil {
			return err
		}
	}
	return nil
}

func (r *durationServiceResources) finishMessage(ctx context.Context, loop *agentloop.AgentLoop, msg messages.StreamMessage, state duration.RunState) (duration.MessageResult, error) {
	if r.shouldQueueClose(msg, state) {
		state = state.WithCloseAfterOpenPending(true)
	}
	messageState, err := closePendingSessionIfReady(ctx, loop, r.opts, sessionLoopMessageState{
		closeSent:             state.CloseSent(),
		closeAfterOpenPending: state.CloseAfterOpenPending(),
	})
	if err != nil {
		return duration.MessageResult{State: &state}, err
	}
	state = state.WithCloseSent(messageState.closeSent).WithCloseAfterOpenPending(messageState.closeAfterOpenPending)
	if shouldStopSessionLoop(msg, r.opts) {
		state = state.WithDrainPlayback()
		return duration.MessageResult{Stop: true, State: &state}, nil
	}
	return duration.MessageResult{State: &state}, nil
}

func (r *durationServiceResources) open(ctx context.Context, loop *agentloop.AgentLoop, state *duration.RunState) error {
	promptProvided := r.opts.PromptProvided || r.opts.Prompt != ""
	if promptProvided && !state.PromptSent() {
		*state = state.WithPromptSent()
		if err := loop.Send(ctx, []messages.Message{messages.NewTextMessage(messages.RoleUser, r.opts.Prompt)}); err != nil {
			return fmt.Errorf("send session message: %w", err)
		}
		if r.opts.observer != nil {
			r.opts.observer.noteUserTextInput(r.opts.Prompt)
		}
		if r.opts.awaitFirstTurn != nil {
			if err := awaitSessionFirstTurnWithClock(ctx, r.opts.awaitFirstTurn, r.opts.clockSource); err != nil {
				return fmt.Errorf("send session first turn: %w", err)
			}
		}
	}
	if r.opts.CloseAfterOpen && !promptProvided && r.opts.AudioIn == nil && !state.CloseSent() {
		*state = state.WithCloseAfterOpenPending(true)
	}
	return nil
}

func (r *durationServiceResources) shouldQueueClose(msg messages.StreamMessage, state duration.RunState) bool {
	promptProvided := r.opts.PromptProvided || r.opts.Prompt != ""
	return r.opts.CloseAfterOpen && promptProvided && msg.Type == messages.StreamTypeMessageEnd &&
		(r.opts.observer == nil || r.opts.observer.lastMessageEndAdmitted()) && !state.CloseSent()
}

func (r *durationServiceResources) handleWake(ctx context.Context, loop duration.Loop, state duration.RunState) (duration.RunState, error) {
	concrete, err := durationAgentLoop(loop)
	if err != nil {
		return state, err
	}
	if r.opts.observer != nil {
		if err := r.opts.observer.dispatchScheduledInputs(ctx, concrete); err != nil {
			return state, err
		}
	}
	messageState, err := closePendingSessionIfReady(ctx, concrete, r.opts, sessionLoopMessageState{
		closeSent:             state.CloseSent(),
		closeAfterOpenPending: state.CloseAfterOpenPending(),
	})
	if err != nil {
		return state, err
	}
	state = state.WithCloseSent(messageState.closeSent).WithCloseAfterOpenPending(messageState.closeAfterOpenPending)
	return state, nil
}
