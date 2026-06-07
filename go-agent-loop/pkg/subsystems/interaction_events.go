package subsystems

import (
	"context"
	"fmt"

	"github.com/portpowered/go-agent-loop/pkg/logging"
	"github.com/portpowered/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-loop/pkg/state"
)

// InteractionEvents maps normalized gateway events into loop state and existing
// observable outputs without importing gateway packages into go-agent-loop.
type InteractionEvents struct {
	logger logging.Logger
}

func NewInteractionEvents(logger logging.Logger) *InteractionEvents {
	return &InteractionEvents{logger: logger}
}

var _ Subsystem = (*InteractionEvents)(nil)

func (s *InteractionEvents) TickGroup() TickGroup {
	return TickGroupInteractionEvents
}

func (s *InteractionEvents) Execute(ctx context.Context, curr *state.LoopState) error {
	for _, event := range curr.Inputs.InteractionEvents {
		if err := s.applyEvent(ctx, curr, event); err != nil {
			return err
		}
	}
	return nil
}

func (s *InteractionEvents) applyEvent(ctx context.Context, curr *state.LoopState, event messages.InteractionEvent) error {
	if err := validateInteractionEvent(curr.Interaction, event); err != nil {
		return err
	}

	if event.Type == messages.InteractionEventStart {
		curr.Interaction = messages.InteractionState{
			ActiveInteractionID: event.InteractionID,
			Provider:            event.Provider,
			Model:               event.Model,
			LatestSequence:      event.Sequence,
		}
		return nil
	}

	curr.Interaction.LatestSequence = event.Sequence
	if event.Provider != "" {
		curr.Interaction.Provider = event.Provider
	}
	if event.Model != "" {
		curr.Interaction.Model = event.Model
	}

	switch event.Type {
	case messages.InteractionEventTextDelta:
		writeKernelDelta(ctx, curr, messages.Model, messages.StreamMessage{
			Type:  messages.StreamTypeTextDelta,
			Role:  messages.RoleAssistant,
			Value: messages.NewTextDeltaValue(event.TextDelta),
		})
	case messages.InteractionEventFinalMessage:
		if event.FinalMessage == nil {
			return fmt.Errorf("interaction %q sequence %d: final message is required", event.InteractionID, event.Sequence)
		}
		msg := messages.CloneInteractionState(messages.InteractionState{FinalMessage: event.FinalMessage}).FinalMessage
		curr.Interaction.FinalMessage = msg
		curr.Interaction.PendingToolCalls = nil
		curr.Outputs.UserInbox.Write(ctx, messages.UserRequest{Message: *msg})
		writeFullMessage(ctx, curr, messages.Model, *msg)
	case messages.InteractionEventToolCallRequest:
		if event.ToolCall == nil {
			return fmt.Errorf("interaction %q sequence %d: tool call is required", event.InteractionID, event.Sequence)
		}
		call := *event.ToolCall
		curr.Interaction.PendingToolCalls = append(curr.Interaction.PendingToolCalls, call)
		writeKernelDelta(ctx, curr, messages.Model, messages.StreamMessage{
			Type:       messages.StreamTypeToolCallStart,
			Role:       messages.RoleAssistant,
			ToolCallId: call.ID,
			Value:      messages.NewToolCallStartValue(call.ID, call.Name),
		})
		writeKernelDelta(ctx, curr, messages.Model, messages.StreamMessage{
			Type:       messages.StreamTypeToolCallEnd,
			Role:       messages.RoleAssistant,
			ToolCallId: call.ID,
			Value:      messages.NewToolCallEndValue(call.ID, call.Name, call.Arguments),
		})
	case messages.InteractionEventToolResultAccepted:
		if event.ToolCall == nil {
			return nil
		}
		curr.Interaction.PendingToolCalls = removePendingToolCall(curr.Interaction.PendingToolCalls, event.ToolCall.ID)
	case messages.InteractionEventUsage:
		if event.Usage != nil {
			usage := *event.Usage
			curr.Interaction.Usage = &usage
		}
	case messages.InteractionEventError:
		if event.Error == nil {
			return fmt.Errorf("interaction %q sequence %d: error payload is required", event.InteractionID, event.Sequence)
		}
		errPayload := *event.Error
		curr.Interaction.TerminalError = &errPayload
		writeKernelDelta(ctx, curr, messages.System, messages.StreamMessage{
			Type:  messages.StreamTypeError,
			Value: messages.NewErrorValue(errPayload.Message),
		})
	case messages.InteractionEventCancellation:
		if event.Cancellation == nil {
			return fmt.Errorf("interaction %q sequence %d: cancellation payload is required", event.InteractionID, event.Sequence)
		}
		cancelPayload := *event.Cancellation
		curr.Interaction.Cancellation = &cancelPayload
	case messages.InteractionEventEnd:
		curr.Interaction.Completed = true
		curr.Inputs.TerminateLoop = true
	}

	return nil
}

func validateInteractionEvent(current messages.InteractionState, event messages.InteractionEvent) error {
	if event.InteractionID == "" {
		return fmt.Errorf("interaction event is missing interaction ID")
	}
	if event.Sequence <= 0 {
		return fmt.Errorf("interaction %q has invalid sequence %d", event.InteractionID, event.Sequence)
	}

	if current.ActiveInteractionID == "" || (current.Completed && event.Type == messages.InteractionEventStart && current.ActiveInteractionID != event.InteractionID) {
		return nil
	}

	if current.ActiveInteractionID != event.InteractionID {
		return fmt.Errorf("interaction %q does not match active interaction %q", event.InteractionID, current.ActiveInteractionID)
	}
	if event.Sequence <= current.LatestSequence {
		return fmt.Errorf("interaction %q sequence %d is not after %d", event.InteractionID, event.Sequence, current.LatestSequence)
	}
	return nil
}

func removePendingToolCall(calls []messages.ToolCall, id string) []messages.ToolCall {
	if id == "" || len(calls) == 0 {
		return calls
	}
	out := calls[:0]
	for _, call := range calls {
		if call.ID != id {
			out = append(out, call)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func writeKernelDelta(ctx context.Context, curr *state.LoopState, source messages.ParticipantID, delta messages.StreamMessage) {
	curr.Outputs.KernelDeltaInbox.Write(ctx, messages.KernelDeltaRequest{
		Source: source,
		Delta:  delta,
	})
}

func writeFullMessage(ctx context.Context, curr *state.LoopState, source messages.ParticipantID, msg messages.Message) {
	writeKernelDelta(ctx, curr, source, messages.StreamMessage{
		Type:  messages.StreamTypeSystemFullMessage,
		Value: messages.NewInferenceResultValue(string(source), msg),
	})
}
