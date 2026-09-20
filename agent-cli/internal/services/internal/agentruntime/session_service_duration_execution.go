package agentruntime

import (
	"context"
	"errors"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	duration "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
)

func (r *durationServiceResources) handle(ctx context.Context, loop duration.Loop, controller duration.Controller, msg messages.StreamMessage) (duration.MessageResult, error) {
	concrete, err := durationAgentLoop(loop)
	if err != nil {
		return duration.MessageResult{}, err
	}
	return r.handleMessage(ctx, concrete, controller, msg)
}

func durationAgentLoop(loop duration.Loop) (*agentloop.AgentLoop, error) {
	concrete, ok := loop.(*agentloop.AgentLoop)
	if !ok || concrete == nil {
		return nil, errors.New("session duration loop adapter is invalid")
	}
	return concrete, nil
}

func (r *durationServiceResources) handleMessage(ctx context.Context, loop *agentloop.AgentLoop, _ duration.Controller, msg messages.StreamMessage) (duration.MessageResult, error) {
	if err := r.prepareMessage(ctx, loop, msg); err != nil {
		return duration.MessageResult{}, err
	}
	return r.finishMessage(ctx, loop, msg)
}

func (r *durationServiceResources) prepareMessage(ctx context.Context, loop *agentloop.AgentLoop, msg messages.StreamMessage) error {
	if msg.Type == messages.StreamTypeSessionCreated && r.publisher != nil {
		r.publisher.markSessionReady()
	}
	if msg.Type == messages.StreamTypeSessionOpen {
		if err := r.startSessionUpdatedTimer(); err != nil {
			return err
		}
		if err := r.open(ctx, loop); err != nil {
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

func (r *durationServiceResources) finishMessage(ctx context.Context, loop *agentloop.AgentLoop, msg messages.StreamMessage) (duration.MessageResult, error) {
	if r.opts.observer != nil && r.opts.observer.scheduledAudioReady() {
		r.stopSessionUpdatedTimer()
	}
	if r.shouldQueueClose(msg) {
		r.closeAfterOpen = true
	}
	state, err := closePendingSessionIfReady(ctx, loop, r.opts, sessionLoopMessageState{
		closeSent:             r.closeSent,
		closeAfterOpenPending: r.closeAfterOpen,
	})
	if err != nil {
		return duration.MessageResult{}, err
	}
	r.closeSent = state.closeSent
	if shouldStopSessionLoop(msg, r.opts) {
		r.drainPlayback = true
		return duration.MessageResult{Stop: true}, nil
	}
	return duration.MessageResult{}, nil
}

func (r *durationServiceResources) open(ctx context.Context, loop *agentloop.AgentLoop) error {
	promptProvided := r.opts.PromptProvided || r.opts.Prompt != ""
	if promptProvided && !r.promptSent {
		r.promptSent = true
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
	if r.opts.CloseAfterOpen && !promptProvided && r.opts.AudioIn == nil && !r.closeSent {
		r.closeAfterOpen = true
	}
	return nil
}

func (r *durationServiceResources) shouldQueueClose(msg messages.StreamMessage) bool {
	promptProvided := r.opts.PromptProvided || r.opts.Prompt != ""
	return r.opts.CloseAfterOpen && promptProvided && msg.Type == messages.StreamTypeMessageEnd &&
		(r.opts.observer == nil || r.opts.observer.lastMessageEndAdmitted()) && !r.closeSent
}

func (r *durationServiceResources) handleWake(ctx context.Context, loop duration.Loop) error {
	concrete, err := durationAgentLoop(loop)
	if err != nil {
		return err
	}
	if r.opts.observer != nil {
		if err := r.opts.observer.dispatchScheduledInputs(ctx, concrete); err != nil {
			return err
		}
	}
	state, err := closePendingSessionIfReady(ctx, concrete, r.opts, sessionLoopMessageState{
		closeSent:             r.closeSent,
		closeAfterOpenPending: r.closeAfterOpen,
	})
	if err != nil {
		return err
	}
	r.closeSent = state.closeSent
	return nil
}
