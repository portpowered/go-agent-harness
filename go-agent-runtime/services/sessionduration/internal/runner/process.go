package runner

import (
	"context"
	"errors"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
)

func (r *runLoop) process(msg messages.StreamMessage) error {
	admission, err := r.admitMessage(msg)
	if err != nil || !admission.Accepted {
		return err
	}
	return r.processAdmittedMessage(admission.Message)
}

func (r *runLoop) admitMessage(msg messages.StreamMessage) (sessionduration.Admission, error) {
	admission := r.controller.Observe(msg)
	if !admission.Accepted {
		r.pending = append(r.pending, msg)
		return admission, nil
	}
	if err := r.publish(r.request.Publication, admission.Message); err != nil {
		return admission, err
	}
	retryDispatched, err := r.retry(admission.Message)
	if err != nil {
		return admission, r.handleBoundaryError(err)
	}
	if admission.Message.Type == messages.StreamTypeMessageEnd && !retryDispatched {
		r.state = r.state.WithAwaitingResponse(false)
	}
	return admission, nil
}

func (r *runLoop) processAdmittedMessage(msg messages.StreamMessage) error {
	if r.finished {
		return nil
	}
	if err := r.startSessionUpdatedTimer(msg); err != nil {
		return err
	}
	result, err := r.handleAdmittedMessage(msg)
	if result.State != nil {
		r.state = *result.State
	}
	if err != nil {
		return r.handleBoundaryError(err)
	}
	if r.request.SessionUpdated.Ready != nil && r.request.SessionUpdated.Ready() {
		r.stopSessionUpdatedTimer()
	}
	if !result.Stop {
		return nil
	}
	return r.finish(result.Planned, nil)
}

func (r *runLoop) handleAdmittedMessage(msg messages.StreamMessage) (sessionduration.MessageResult, error) {
	if r.request.Handle == nil {
		return r.handleSessionMessage(msg)
	}
	result, err := r.request.Handle(r.runCtx, r.loop, r.controller, msg, r.state)
	return result, err
}

func (r *runLoop) handleBoundaryError(err error) error {
	if errors.Is(err, sessionduration.ErrMaxDurationExceeded) {
		return r.finishMaxDuration()
	}
	return err
}

func (r *runLoop) handleSessionMessage(msg messages.StreamMessage) (sessionduration.MessageResult, error) {
	state := r.state
	if err := r.handleSessionLifecycle(msg, &state); err != nil {
		return sessionduration.MessageResult{State: &state}, err
	}
	if err := r.dispatchScheduledMessageAudio(msg); err != nil {
		return sessionduration.MessageResult{State: &state}, err
	}
	result, done := sessionStopResult(msg, r.request.Policy, r.request.Facts, state)
	if result.State != nil {
		state = *result.State
	}
	if done {
		return result, nil
	}
	state, err := r.closePendingSessionIfReady(r.runCtx, r.loop, state)
	if err != nil {
		return sessionduration.MessageResult{State: &state}, err
	}
	return sessionduration.MessageResult{State: &state}, nil
}

func (r *runLoop) handleSessionLifecycle(msg messages.StreamMessage, state *sessionduration.RunState) error {
	if msg.Type == messages.StreamTypeSessionCreated && r.request.Effects.SessionCreated != nil {
		if err := r.request.Effects.SessionCreated(r.runCtx, r.loop); err != nil {
			return err
		}
	}
	if msg.Type != messages.StreamTypeSessionOpen {
		return nil
	}
	if err := r.openSession(r.runCtx, r.loop, state); err != nil {
		return err
	}
	if r.request.Effects.SessionOpened != nil {
		if err := r.request.Effects.SessionOpened(r.runCtx, r.loop); err != nil {
			return err
		}
	}
	return r.startAudioInput(r.runCtx, r.loop)
}

func (r *runLoop) dispatchScheduledMessageAudio(msg messages.StreamMessage) error {
	dispatch := r.request.Effects.DispatchScheduledInputs
	if dispatch != nil && shouldDispatchScheduledAudio(msg, r.request.Policy.ScheduledAudioDispatch) {
		return dispatch(r.runCtx, r.loop)
	}
	return nil
}

func sessionStopResult(msg messages.StreamMessage, policy sessionduration.RunPolicy, facts sessionduration.RunFacts, state sessionduration.RunState) (sessionduration.MessageResult, bool) {
	if shouldQueueSessionClose(msg, policy, facts, state) {
		// Queue the close and let closePendingSessionIfReady send it through the
		// live loop. Returning a planned stop here would send the control during
		// finalization and cancel the loop before its SESSION.CLOSE delta can be
		// observed by the bounded runner.
		state = state.WithCloseAfterOpenPending(true)
	}
	if policy.HasAudioInput {
		if shouldStopAudioInputSession(msg, policy, facts, state) {
			state = state.WithDrainPlayback()
			return sessionduration.MessageResult{Stop: true, State: &state}, true
		}
	} else if shouldStopSession(msg, policy, facts) {
		state = state.WithDrainPlayback()
		return sessionduration.MessageResult{Stop: true, State: &state}, true
	}
	return sessionduration.MessageResult{State: &state}, false
}

func (r *runLoop) openSession(ctx context.Context, loop sessionduration.Loop, state *sessionduration.RunState) error {
	policy := r.request.Policy
	promptProvided := policy.PromptProvided || policy.Prompt != ""
	if promptProvided && !state.PromptSent() {
		*state = state.WithPromptSent()
		message := messages.NewTextMessage(messages.RoleUser, policy.Prompt)
		if err := loop.Send(ctx, []messages.Message{message}); err != nil {
			return fmt.Errorf("send session message: %w", err)
		}
		if r.request.Effects.NoteUserTextInput != nil {
			r.request.Effects.NoteUserTextInput(policy.Prompt)
		}
		if r.request.Effects.AwaitFirstTurn != nil {
			if err := r.request.Effects.AwaitFirstTurn(ctx); err != nil {
				return fmt.Errorf("send session first turn: %w", err)
			}
		}
	}
	if policy.CloseAfterOpen && !promptProvided && !policy.HasAudioInput && !state.CloseSent() {
		*state = state.WithCloseAfterOpenPending(true)
	}
	return nil
}

func (r *runLoop) closePendingSessionIfReady(ctx context.Context, loop sessionduration.Loop, state sessionduration.RunState) (sessionduration.RunState, error) {
	if state.CloseSent() || fact(r.request.Facts.HasToolLifecycleObligation) {
		return state, nil
	}
	closeAfterOpen := r.request.Policy.CloseAfterOpen &&
		(state.CloseAfterOpenPending() || (state.PromptSent() && fact(r.request.Facts.LastMessageEndAdmitted)))
	closeAfterScheduled := r.request.Policy.CloseAfterScheduledAudio && fact(r.request.Facts.ScheduledAudioComplete)
	if !closeAfterOpen && !closeAfterScheduled {
		return state, nil
	}
	message := messages.Message{
		Role: messages.RoleUser,
		ContentParts: []messages.ContentPart{
			messages.ControlPlanePart{ControlPlaneMessageType: messages.ControlPlaneMessageTypeSessionClose},
		},
	}
	if err := loop.Send(ctx, []messages.Message{message}); err != nil {
		return state, fmt.Errorf("close session loop: %w", err)
	}
	return state.WithCloseSent(true), nil
}

func shouldQueueSessionClose(msg messages.StreamMessage, policy sessionduration.RunPolicy, facts sessionduration.RunFacts, state sessionduration.RunState) bool {
	promptProvided := policy.PromptProvided || policy.Prompt != ""
	return policy.CloseAfterOpen && promptProvided && msg.Type == messages.StreamTypeMessageEnd &&
		fact(facts.LastMessageEndAdmitted) && !state.CloseSent()
}

func shouldStopSession(msg messages.StreamMessage, policy sessionduration.RunPolicy, facts sessionduration.RunFacts) bool {
	if isAuthoritativeSessionStop(msg) || hasTerminalRunFailure(msg, facts) {
		return true
	}
	if policy.CloseAfterOpen || policy.WaitForClose {
		return false
	}
	switch msg.Type {
	case messages.StreamTypeMessageEnd:
		if !fact(facts.LastMessageEndAdmitted) || fact(facts.HasToolLifecycleObligation) {
			return false
		}
		if policy.CloseAfterScheduledAudio && !fact(facts.ScheduledAudioComplete) {
			return false
		}
		return true
	case messages.StreamTypeTextEnd:
		return !fact(facts.HasToolLifecycleObligation)
	default:
		return false
	}
}

func shouldStopAudioInputSession(msg messages.StreamMessage, policy sessionduration.RunPolicy, facts sessionduration.RunFacts, state sessionduration.RunState) bool {
	if !state.AwaitingResponse() {
		return msg.Type == messages.StreamTypeSessionClose
	}
	if hasTerminalRunFailure(msg, facts) {
		return true
	}
	if policy.WaitForClose {
		return isRunTerminalErrorMessage(msg) || msg.Type == messages.StreamTypeSessionClose
	}
	switch msg.Type {
	case messages.StreamTypeMessageEnd:
		if !fact(facts.LastMessageEndAdmitted) {
			return false
		}
		if policy.RequireAssistantResponse && (msg.Role == messages.RoleTool || !fact(facts.AssistantResponseCompleted)) {
			return false
		}
		return true
	case messages.StreamTypeSessionClose:
		return true
	default:
		return isRunTerminalErrorMessage(msg)
	}
}

func hasTerminalRunFailure(msg messages.StreamMessage, facts sessionduration.RunFacts) bool {
	return msg.Type == messages.StreamTypeMessageEnd &&
		(fact(facts.HasTerminalToolContinuationFailure) || fact(facts.HasTerminalScheduledResponseFailure))
}

func isAuthoritativeSessionStop(msg messages.StreamMessage) bool {
	return msg.Type == messages.StreamTypeSessionClose || msg.Type == messages.StreamTypeLoopEnd || isRunTerminalErrorMessage(msg)
}

func isRunTerminalErrorMessage(msg messages.StreamMessage) bool {
	if msg.Type != messages.StreamTypeError {
		return false
	}
	value, ok := msg.Value.(*messages.ErrorValue)
	return ok && value != nil && !value.IsNonTerminal()
}

func shouldDispatchScheduledAudio(msg messages.StreamMessage, policy sessionduration.ScheduledAudioDispatch) bool {
	switch msg.Type {
	case messages.StreamTypeSessionOpen, messages.StreamTypeMessageEnd, messages.StreamTypeSessionUpdated:
		return true
	case messages.StreamTypeMessageStart, messages.StreamTypeAudioStart:
		return policy == sessionduration.ScheduledAudioActiveResponse
	default:
		return false
	}
}

func fact(read func() bool) bool {
	return read != nil && read()
}
