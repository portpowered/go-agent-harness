package consumer_test

import (
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration/wire"
)

func TestExternalConsumerUsesPublicSessionDurationContract(t *testing.T) {
	provider := messages.StreamMessage{Type: messages.StreamTypeSessionClose, Value: messages.NewSessionCloseValueWithTerminal(
		"session", "provider-close", "provider_close", messages.TerminalReasonProviderClose,
		messages.TerminalProvenanceProvider, messages.TerminalOutputPartial,
	)}
	providerValue := provider.Value.(*messages.SessionCloseValue)
	service := wire.NewService()
	state := service.NewState(sessionduration.TerminalSource{
		Message: func() (messages.StreamMessage, bool) { return provider, true },
		Matches: func(msg messages.StreamMessage) bool {
			value, ok := msg.Value.(*messages.SessionCloseValue)
			return ok && value == providerValue
		},
	})
	state.Observe(messages.StreamMessage{Type: messages.StreamTypeTextDelta})
	if got := state.OutputState(); got != messages.TerminalOutputPartial {
		t.Fatalf("output state = %q", got)
	}
	writes := 0
	if err := state.PublishProviderTerminal(sessionduration.Publication{
		Write: sessionduration.MessageWriter(func(messages.StreamMessage) error {
			writes++
			return nil
		}),
	}); err != nil {
		t.Fatalf("publish provider terminal: %v", err)
	}
	if writes != 1 || !state.Written() {
		t.Fatalf("provider publication writes=%d written=%v", writes, state.Written())
	}
	if err := service.PublishMaxDuration(sessionduration.Publication{}, messages.TerminalOutputNone); err != nil {
		t.Fatalf("publish synthesized terminal: %v", err)
	}
}
