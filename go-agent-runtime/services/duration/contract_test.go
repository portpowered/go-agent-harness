package duration

import (
	"errors"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestInvalidDurationErrorRetainsSentinelIdentity(t *testing.T) {
	var nilError *InvalidDurationError
	if nilError.Error() != ErrInvalidMaxDuration.Error() {
		t.Fatalf("nil error text=%q", nilError.Error())
	}
	err := &InvalidDurationError{Duration: -time.Millisecond}
	if !errors.Is(err, ErrInvalidMaxDuration) || !errors.Is(errors.Unwrap(err), ErrInvalidMaxDuration) {
		t.Fatalf("invalid duration identity lost: %v", err)
	}
}

func TestTerminalSummaryDecoderValidatesMetadata(t *testing.T) {
	decoder := TerminalSummaryDecoder{}
	if _, present, err := decoder.FromMessage(messages.StreamMessage{}); err != nil || present {
		t.Fatalf("non-terminal message: present=%v err=%v", present, err)
	}
	if _, present, err := decoder.FromMessage(messages.StreamMessage{Type: messages.StreamTypeSessionClose, Value: messages.NewSessionCloseValue("id", "close")}); err != nil || present {
		t.Fatalf("legacy terminal: present=%v err=%v", present, err)
	}
	valid := messages.NewSessionCloseValueWithTerminal(
		"id", "provider-close", "provider_close", messages.TerminalReasonProviderClose,
		messages.TerminalProvenanceProvider, messages.TerminalOutputPartial,
	)
	summary, present, err := decoder.FromMessage(messages.StreamMessage{Type: messages.StreamTypeSessionClose, Value: valid})
	if err != nil || !present || summary == nil || summary.TerminalProvenance != messages.TerminalProvenanceProvider {
		t.Fatalf("valid terminal summary=%+v present=%v err=%v", summary, present, err)
	}
	malformed := *valid
	malformed.OutputState = ""
	if _, present, err := decoder.FromMessage(messages.StreamMessage{Type: messages.StreamTypeSessionClose, Value: &malformed}); err == nil || present {
		t.Fatalf("malformed terminal accepted: present=%v err=%v", present, err)
	}
}
