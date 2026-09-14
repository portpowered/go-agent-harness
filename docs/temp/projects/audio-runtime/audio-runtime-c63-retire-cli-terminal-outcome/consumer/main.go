package main

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/terminaloutcome"
	terminaloutcomewire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/terminaloutcome/wire"
)

func main() {
	service := terminaloutcomewire.NewService()
	reporter := service.NewReporter()
	reporter.MarkRunStarted()
	reporter.ObserveStreamMessage(messages.StreamMessage{
		Type:  messages.StreamTypeTextDelta,
		Value: messages.NewTextDeltaValue("consumer output is observed, not rendered twice"),
	}, false)
	reporter.ObserveStreamMessage(messages.StreamMessage{
		Type: messages.StreamTypeSessionClose,
		Value: messages.NewSessionCloseValueWithTerminal(
			"", "", "consumer", messages.TerminalReasonProviderAuthoredCompletion,
			messages.TerminalProvenanceProvider, messages.TerminalOutputComplete,
		),
	}, true)

	var output bytes.Buffer
	if err := reporter.Publish(&output, nil); err != nil {
		panic(fmt.Errorf("publish terminal outcome: %w", err))
	}
	want := "\n[session terminal: classification=consumer terminal_reason=provider_authored_completion terminal_provenance=provider output_state=complete]\n"
	if output.String() != want {
		panic(fmt.Errorf("terminal output %q, want %q", output.String(), want))
	}
	if err := reporter.Publish(&output, nil); !errors.Is(err, terminaloutcome.ErrSessionTerminalAlreadyPublished) {
		panic(fmt.Errorf("second publish error %v is not ErrSessionTerminalAlreadyPublished", err))
	}
	fmt.Print(output.String())
}
