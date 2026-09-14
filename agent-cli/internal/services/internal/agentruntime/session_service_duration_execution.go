package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	duration "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
)

func (r *durationServiceResources) handle(ctx context.Context, loop duration.Loop, controller duration.Controller, msg messages.StreamMessage) (duration.MessageResult, error) {
	concrete, err := durationAgentLoop(loop)
	if err != nil {
		return duration.MessageResult{}, err
	}
	if err := r.retry(ctx, concrete, controller, msg); err != nil {
		return duration.MessageResult{}, err
	}
	return r.handleMessage(ctx, concrete, controller, msg)
}

func durationAgentLoop(loop duration.Loop) (*agentloop.AgentLoop, error) {
	concrete, ok := loop.(*durationServiceLoop)
	if !ok || concrete.inner == nil {
		return nil, errors.New("session duration loop adapter is invalid")
	}
	return concrete.inner, nil
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

func (r *durationServiceResources) retry(ctx context.Context, loop *agentloop.AgentLoop, controller duration.Controller, msg messages.StreamMessage) error {
	terminal, ok := durationRetryTerminal(msg)
	if !ok {
		return nil
	}
	decision := controller.Retry(duration.RetryRequest{Terminal: terminal})
	if !decision.Eligible {
		return nil
	}
	if err := r.waitForRetry(ctx, controller, decision.Delay); err != nil {
		return err
	}
	if err := loop.SendSessionEvent(ctx, messages.StreamMessage{Type: messages.StreamTypeResponseCreate, Value: messages.NewResponseCreateValue()}); err != nil {
		return fmt.Errorf("send rate-limit retry response: %w", err)
	}
	if r.opts.observer != nil {
		r.opts.observer.observeProviderDispatch(messages.StreamMessage{Type: messages.StreamTypeResponseCreate})
	}
	return nil
}

func durationRetryTerminal(msg messages.StreamMessage) (*messages.MessageEndValue, bool) {
	if msg.Type != messages.StreamTypeMessageEnd {
		return nil, false
	}
	terminal, ok := msg.Value.(*messages.MessageEndValue)
	return terminal, ok && terminal != nil
}

func (r *durationServiceResources) waitForRetry(ctx context.Context, controller duration.Controller, delay time.Duration) error {
	timer := r.clock.NewTimer(delay)
	if timer == nil {
		return errors.New("session duration clock returned a nil retry timer")
	}
	defer timer.Stop()
	select {
	case <-timer.C():
	case err := <-controller.Errors():
		return normalizeDurationControllerError(err)
	case <-ctx.Done():
		return ctx.Err()
	case <-r.observed.Done():
		return context.Canceled
	}
	select {
	case err := <-controller.Errors():
		return normalizeDurationControllerError(err)
	default:
		return nil
	}
}

func normalizeDurationControllerError(err error) error {
	if errors.Is(err, duration.ErrMaxDurationExceeded) {
		return duration.ErrMaxDurationExceeded
	}
	return err
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
