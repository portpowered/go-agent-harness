package services

import (
	"context"
	"fmt"
	"strconv"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/metrics"
)

func (o *sessionProgressObserver) toolResultsEnabledForObservation() bool {
	if o == nil {
		return false
	}
	o.toolStateMu.Lock()
	defer o.toolStateMu.Unlock()
	return o.toolResultsEnabled
}

// noteUserTextInput accounts for prompt text injected into the session as
// user input.
func (o *sessionProgressObserver) noteUserTextInput(text string) {
	if o == nil || text == "" {
		return
	}
	o.account(metrics.DirectionInput, metrics.ModalityText, len(text))
}

// dispatchScheduledInputs delivers due scheduled audio through the loop's
// existing SendAudioInput seam and attributes the bytes to the in-flight turn.
func (o *sessionProgressObserver) dispatchScheduledInputs(ctx context.Context, loop scheduledSessionInputSender) error {
	if o == nil || loop == nil {
		return nil
	}
	// A response boundary is not enough to release the next spoken turn. The
	// current provider call must have its accepted result and terminal
	// continuation first; this check keeps scheduling independent of the
	// particular input source that created the call.
	if o.hasToolLifecycleObligation() || !o.scheduledAudioReady() {
		return nil
	}
	for len(o.pendingInputs) > 0 && o.pendingInputs[0].AfterCompletedTurns <= o.turnsCompleted && !o.hasToolLifecycleObligation() {
		input := o.pendingInputs[0]
		inputIndex := o.scheduledInputs - len(o.pendingInputs) + 1
		if err := loop.SendAudioInput(ctx, input.PCM); err != nil {
			return fmt.Errorf("send scheduled audio input %d: %w", inputIndex, err)
		}
		if input.EndOfTurn {
			if err := loop.SendSessionEvent(ctx, messages.StreamMessage{Type: messages.StreamTypeMessageEnd}); err != nil {
				return fmt.Errorf("send scheduled audio input %d end-of-turn: %w", inputIndex, err)
			}
		}
		if !o.scheduledTurnBaseSet {
			o.scheduledTurnBase = o.turnsCompleted
			o.scheduledTurnBaseSet = true
		}
		o.dispatchedInputs++
		o.pendingInputs = o.pendingInputs[1:]
		o.account(metrics.DirectionInput, metrics.ModalityAudio, len(input.PCM))
	}
	return nil
}

// scheduledAudioReady reports whether the scheduler may release its next
// input. The acknowledgement requirement is opt-in so replay and existing
// non-OpenAI session paths preserve their previous behavior.
func (o *sessionProgressObserver) scheduledAudioReady() bool {
	return o == nil || !o.requireSessionUpdated || (o.sawSessionOpen && o.sessionUpdated)
}

func (o *sessionProgressObserver) scheduledAudioAwaitingConfiguration() bool {
	return o != nil && o.requireSessionUpdated && len(o.pendingInputs) > 0 && !o.scheduledAudioReady()
}

// scheduledAudioComplete reports whether every scheduled input has been
// accepted and its corresponding assistant response has crossed MESSAGE.END.
// It is intentionally separate from replay capture inspection: live planning
// owns the decision to close after the schedule, while replay follows its
// captured lifecycle.
func (o *sessionProgressObserver) scheduledAudioComplete() bool {
	return o != nil && o.scheduledInputs > 0 && len(o.pendingInputs) == 0 && o.completedScheduled >= o.scheduledInputs && !o.hasToolLifecycleObligation()
}

func (o *sessionProgressObserver) scheduledAudioIncomplete() bool {
	return o != nil && o.scheduledInputs > 0 && !o.scheduledAudioComplete()
}

// scheduledAudioCounts returns the terminal schedule counters in a stable
// order. Completed is the number of scheduled inputs whose assistant response
// reached MESSAGE.END; it is distinct from the total session turn count when
// a prompt or seed turn precedes scheduled audio.
func (o *sessionProgressObserver) scheduledAudioCounts() (completed, dispatched, scheduled int) {
	if o == nil {
		return 0, 0, 0
	}
	return o.completedScheduled, o.dispatchedInputs, o.scheduledInputs
}

// noteProviderUsage accumulates the provider-reported token usage delivered on
// MESSAGE.END. Each value is an incremental contribution for the completed
// turn; the terminal runtime observation publishes the resulting
// session-cumulative totals.
func (o *sessionProgressObserver) noteProviderUsage(usage messages.TokenUsage) {
	if o == nil {
		return
	}
	if usage.PromptTokens < 0 || usage.CompletionTokens < 0 || usage.TotalTokens < 0 || usage.ReasoningTokens < 0 {
		return
	}
	o.usagePrompt += uint64(usage.PromptTokens)
	o.usageCompletion += uint64(usage.CompletionTokens)
	o.usageTotal += uint64(usage.TotalTokens)
	o.usageReasoning += uint64(usage.ReasoningTokens)
	if usage.PromptTokens != 0 || usage.CompletionTokens != 0 || usage.TotalTokens != 0 || usage.ReasoningTokens != 0 {
		o.usageSeen = true
	}
}

// completeTurn closes the current turn boundary and emits the per-turn record.
func (o *sessionProgressObserver) completeTurn() {
	o.turnsCompleted++
	if o.scheduledTurnBaseSet && o.turnsCompleted > o.scheduledTurnBase {
		completed := o.turnsCompleted - o.scheduledTurnBase
		if completed > o.dispatchedInputs {
			completed = o.dispatchedInputs
		}
		if completed > o.scheduledInputs {
			completed = o.scheduledInputs
		}
		o.completedScheduled = completed
	}
	if o.runtime != nil {
		o.runtime.turnCompleted(o.turnsCompleted)
	}
	if o.sink != nil {
		o.sink.RecordSessionDiagnostic(SessionDiagnosticRecord{
			Event: SessionDiagnosticEventTurn,
			Fields: map[string]string{
				fieldTurnIndex:        strconv.Itoa(o.turnsCompleted),
				fieldInputAudioBytes:  strconv.FormatUint(o.counters.inputAudio, 10),
				fieldOutputToolBytes:  strconv.FormatUint(o.counters.outTool, 10),
				fieldInputTextBytes:   strconv.FormatUint(o.counters.inputText, 10),
				fieldOutputAudioBytes: strconv.FormatUint(o.counters.outAudio, 10),
				fieldOutputTextBytes:  strconv.FormatUint(o.counters.outText, 10),
			},
		})
	}
	o.counters.reset()
}
