package wire

import (
	"bytes"
	"errors"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/terminaloutcome"
)

func TestPublicWireConsumerExercisesTerminalOutcomeContract(t *testing.T) {
	service := NewService()
	reporter := service.NewReporter()
	reporter.MarkRunStarted()
	reporter.ObserveStreamMessage(messages.StreamMessage{
		Type:  messages.StreamTypeTextDelta,
		Value: messages.NewTextDeltaValue("external consumer"),
	}, false)
	reporter.ObserveStreamMessage(messages.StreamMessage{
		Type: messages.StreamTypeSessionClose,
		Value: messages.NewSessionCloseValueWithTerminal(
			"", "", "provider", messages.TerminalReasonProviderAuthoredCompletion,
			messages.TerminalProvenanceProvider, messages.TerminalOutputComplete,
		),
	}, true)

	var out bytes.Buffer
	if err := reporter.Publish(&out, nil); err != nil {
		t.Fatalf("external publish: %v", err)
	}
	want := "[session terminal: classification=provider terminal_reason=provider_authored_completion terminal_provenance=provider output_state=complete]\n"
	if got := out.String(); got == "" || !bytes.Contains(out.Bytes(), []byte(want)) {
		t.Fatalf("external output = %q, want terminal bytes %q", got, want)
	}
	if err := reporter.Publish(&out, nil); !errors.Is(err, terminaloutcome.ErrSessionTerminalAlreadyPublished) {
		t.Fatalf("external second publish = %v, want sentinel", err)
	}
}
