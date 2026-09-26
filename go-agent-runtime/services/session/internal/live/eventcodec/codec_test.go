package eventcodec

import (
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// A response the runner cancelled on barge-in while a tool continuation was
// owed is not that continuation: its terminal boundary must not resolve or
// fail the tool obligation. The tagged continuation response keeps the
// fail-closed contract when it is itself interrupted.
func TestInterruptedBeforeToolContinuation(t *testing.T) {
	interrupted := messages.NewMessageEndValueWithTerminal(messages.TokenUsage{}, messages.TerminalReasonPartialOutput,
		messages.TerminalProvenanceLoop, messages.TerminalOutputPartial)
	completed := messages.NewMessageEndValue(messages.TokenUsage{})
	for name, tc := range map[string]struct {
		msg  messages.StreamMessage
		want bool
	}{
		"interrupted playing response": {messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Value: interrupted}, true},
		"interrupted continuation": {messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Value: interrupted,
			ResponsePurpose: messages.ResponsePurposeToolContinuation}, false},
		"completed response": {messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Value: completed}, false},
		"provider VAD cancelled response": {messages.StreamMessage{Type: messages.StreamTypeMessageEnd,
			Value: &messages.MessageEndValue{Type: "message_end", Status: "cancelled"}}, true},
		"provider VAD cancelled continuation": {messages.StreamMessage{Type: messages.StreamTypeMessageEnd, ResponsePurpose: messages.ResponsePurposeToolContinuation,
			Value: &messages.MessageEndValue{Type: "message_end", Status: "cancelled"}}, false},
		"non-terminal message": {messages.StreamMessage{Type: messages.StreamTypeAudioDelta, Value: interrupted}, false},
	} {
		if got := InterruptedBeforeToolContinuation(tc.msg); got != tc.want {
			t.Errorf("%s: InterruptedBeforeToolContinuation = %t, want %t", name, got, tc.want)
		}
	}
}
