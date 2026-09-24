package wire

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestPublicWriteMessagePrintsSessionTerminalFields(t *testing.T) {
	var out bytes.Buffer

	err := NewService().WriteMessage(&out, messages.StreamMessage{
		Type: messages.StreamTypeSessionClose,
		Value: messages.NewSessionCloseValueWithTerminal(
			"session-1",
			"provider_closed",
			string(messages.TerminalReasonProviderClose),
			messages.TerminalReasonProviderClose,
			messages.TerminalProvenanceProvider,
			messages.TerminalOutputNotApplicable,
		),
	})
	if err != nil {
		t.Fatalf("WriteMessage: %v", err)
	}

	got := out.String()
	if !strings.Contains(got, "[session closed: provider_closed]") {
		t.Fatalf("legacy close line missing from output:\n%s", got)
	}
	for _, want := range []string{
		"classification=provider_close",
		"terminal_reason=provider_close",
		"terminal_provenance=provider",
		"output_state=not_applicable",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("terminal output missing %q:\n%s", want, got)
		}
	}
}

func TestPublicWriteMessagePrintsTranscriptDelta(t *testing.T) {
	var out bytes.Buffer

	err := NewService().WriteMessage(&out, messages.StreamMessage{
		Type:  messages.StreamTypeTranscriptDelta,
		Value: messages.NewTranscriptDeltaValue("spoken image description"),
	})
	if err != nil {
		t.Fatalf("WriteMessage: %v", err)
	}
	if got := out.String(); got != "Assistant: spoken image description\n" {
		t.Fatalf("transcript output = %q, want %q", got, "Assistant: spoken image description\\n")
	}
}

func TestPublicTranscriptKeepsInterleavedTranscriptRolesSeparate(t *testing.T) {
	var out bytes.Buffer
	renderer := NewService().NewTranscript(&out, nil)
	events := []messages.StreamMessage{
		{Type: messages.StreamTypeTranscriptStart, Role: messages.RoleUser, Value: messages.NewTranscriptStartValue()},
		{Type: messages.StreamTypeTranscriptDelta, Role: messages.RoleUser, Value: messages.NewTranscriptDeltaValue("heard ")},
		{Type: messages.StreamTypeTranscriptDelta, Role: messages.RoleAssistant, Value: messages.NewTranscriptDeltaValue("reply")},
		{Type: messages.StreamTypeTranscriptEnd, Role: messages.RoleAssistant, Value: messages.NewTranscriptEndValue("reply")},
		{Type: messages.StreamTypeTranscriptDelta, Role: messages.RoleUser, Value: messages.NewTranscriptDeltaValue("again")},
		{Type: messages.StreamTypeTranscriptEnd, Role: messages.RoleUser, Value: messages.NewTranscriptEndValue("again")},
	}
	for _, event := range events {
		if err := NewService().WriteMessage(renderer, event); err != nil {
			t.Fatalf("write transcript event: %v", err)
		}
	}

	if got, want := out.String(), "User: heard \nAssistant: reply\nUser: again\n"; got != want {
		t.Fatalf("interleaved transcript output = %q, want %q", got, want)
	}
}

func TestPublicTranscriptKeepsActorChunksOnOneLineAcrossToolContinuation(t *testing.T) {
	var out bytes.Buffer
	renderer := NewService().NewTranscript(&out, nil)
	events := []messages.StreamMessage{
		{Type: messages.StreamTypeTranscriptStart, Role: messages.RoleAssistant, Value: messages.NewTranscriptStartValue()},
		{Type: messages.StreamTypeTranscriptDelta, Role: messages.RoleAssistant, Value: messages.NewTranscriptDeltaValue("I will check ")},
		{Type: messages.StreamTypeTranscriptDelta, Role: messages.RoleAssistant, Value: messages.NewTranscriptDeltaValue("that now.")},
		{Type: messages.StreamTypeToolCallStart, Role: messages.RoleAssistant, ToolCallId: "call-1", Value: messages.NewToolCallStartValue("call-1", "lookup")},
		{Type: messages.StreamTypeToolCallDelta, Role: messages.RoleAssistant, ToolCallId: "call-1", Value: messages.NewToolCallDeltaValue("{\n  \"city\": \"Paris\"\n}")},
		{Type: messages.StreamTypeToolCallEnd, Role: messages.RoleAssistant, ToolCallId: "call-1", Value: messages.NewToolCallEndValue("call-1", "lookup", "{\n  \"city\": \"Paris\"\n}")},
		{Type: messages.StreamTypeMessageStart, Role: messages.RoleTool, ToolCallId: "call-1", Value: messages.NewMessageStartValue()},
		{Type: messages.StreamTypeTextDelta, Role: messages.RoleTool, ToolCallId: "call-1", Value: messages.NewTextDeltaValue("sunny ")},
		{Type: messages.StreamTypeTextDelta, Role: messages.RoleTool, ToolCallId: "call-1", Value: messages.NewTextDeltaValue("and warm")},
		{Type: messages.StreamTypeTextEnd, Role: messages.RoleTool, ToolCallId: "call-1", Value: messages.NewTextEndValue()},
		{Type: messages.StreamTypeTranscriptStart, Role: messages.RoleAssistant, Value: messages.NewTranscriptStartValue()},
		{Type: messages.StreamTypeTranscriptDelta, Role: messages.RoleAssistant, Value: messages.NewTranscriptDeltaValue("It is sunny ")},
		{Type: messages.StreamTypeTranscriptDelta, Role: messages.RoleAssistant, Value: messages.NewTranscriptDeltaValue("and warm.")},
		{Type: messages.StreamTypeTranscriptEnd, Role: messages.RoleAssistant, Value: messages.NewTranscriptEndValue("It is sunny and warm.")},
	}
	for _, event := range events {
		if err := NewService().WriteMessage(renderer, event); err != nil {
			t.Fatalf("write event %s: %v", event.Type, err)
		}
	}

	want := "Assistant: I will check that now.\n" +
		"Tool call: lookup {\"city\":\"Paris\"}\n" +
		"Tool result: sunny and warm\n" +
		"Assistant: It is sunny and warm.\n"
	if got := out.String(); got != want {
		t.Fatalf("interleaved tool output = %q, want %q", got, want)
	}
}

func TestPublicTranscriptKeepsTextDeltasOnOneActorLine(t *testing.T) {
	var out bytes.Buffer
	renderer := NewService().NewTranscript(&out, nil)
	for _, event := range []messages.StreamMessage{
		{Type: messages.StreamTypeTextStart, Role: messages.RoleAssistant, Value: messages.NewTextStartValue()},
		{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("first ")},
		{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("second")},
		{Type: messages.StreamTypeTextEnd, Role: messages.RoleAssistant, Value: messages.NewTextEndValue()},
	} {
		if err := NewService().WriteMessage(renderer, event); err != nil {
			t.Fatalf("write event %s: %v", event.Type, err)
		}
	}
	if got, want := out.String(), "Assistant: first second\n"; got != want {
		t.Fatalf("text delta output = %q, want %q", got, want)
	}
}

func TestPublicTranscriptKeepsTranscriptContiguousAcrossAudioPackets(t *testing.T) {
	var out bytes.Buffer
	renderer := NewService().NewTranscript(&out, nil)
	events := []messages.StreamMessage{
		{Type: messages.StreamTypeTranscriptDelta, Role: messages.RoleAssistant, ResponseID: "response-1", Value: messages.NewTranscriptDeltaValue("Incorrect. ")},
		{Type: messages.StreamTypeAudioDelta, Role: messages.RoleAssistant, ResponseID: "response-1", Value: messages.NewAudioDeltaValue([]byte{0x01, 0x02})},
		{Type: messages.StreamTypeTranscriptDelta, Role: messages.RoleAssistant, ResponseID: "response-1", Value: messages.NewTranscriptDeltaValue("Me llam")},
		{Type: messages.StreamTypeAudioDelta, Role: messages.RoleAssistant, ResponseID: "response-1", Value: messages.NewAudioDeltaValue([]byte{0x03, 0x04})},
		{Type: messages.StreamTypeTranscriptDelta, Role: messages.RoleAssistant, ResponseID: "response-1", Value: messages.NewTranscriptDeltaValue("o means my name is.")},
		{Type: messages.StreamTypeAudioEnd, Role: messages.RoleAssistant, ResponseID: "response-1", Value: messages.NewAudioEndValue()},
		{Type: messages.StreamTypeTranscriptEnd, Role: messages.RoleAssistant, ResponseID: "response-1", Value: messages.NewTranscriptEndValue("Incorrect. Me llamo means my name is.")},
	}
	for _, event := range events {
		if err := NewService().WriteMessage(renderer, event); err != nil {
			t.Fatalf("write event %s: %v", event.Type, err)
		}
	}

	if got, want := out.String(), "Assistant: Incorrect. Me llamo means my name is.\n"; got != want {
		t.Fatalf("interleaved audio transcript output = %q, want %q", got, want)
	}
}

func TestPublicTranscriptBreaksOnlyWhenVisibleActorChanges(t *testing.T) {
	var out bytes.Buffer
	renderer := NewService().NewTranscript(&out, nil)
	events := []messages.StreamMessage{
		{Type: messages.StreamTypeTranscriptDelta, Role: messages.RoleAssistant, Value: messages.NewTranscriptDeltaValue("assistant ")},
		{Type: messages.StreamTypeAudioStart, Role: messages.RoleAssistant, Value: messages.NewAudioStartValue()},
		{Type: messages.StreamTypeAudioDelta, Role: messages.RoleAssistant, Value: messages.NewAudioDeltaValue([]byte{0x01})},
		{Type: messages.StreamTypeUsageInfo, Role: messages.RoleAssistant, Value: messages.NewUsageInfoValue(messages.TokenUsage{})},
		{Type: messages.StreamTypeTranscriptDelta, Role: messages.RoleAssistant, Value: messages.NewTranscriptDeltaValue("continued")},
		{Type: messages.StreamTypeTranscriptDelta, Role: messages.RoleUser, Value: messages.NewTranscriptDeltaValue("user ")},
		{Type: messages.StreamTypeVADSpeechStopped, Role: messages.RoleUser, Value: messages.NewVADSpeechStoppedValue()},
		{Type: messages.StreamTypeInputItemAdded, Role: messages.RoleUser, Value: messages.NewInputItemAddedValue("item-1")},
		{Type: messages.StreamTypeSessionUpdated, Value: messages.NewSessionUpdatedValue("session-1")},
		{Type: messages.StreamTypeTranscriptDelta, Role: messages.RoleUser, Value: messages.NewTranscriptDeltaValue("continued")},
		{Type: messages.StreamTypeTextDelta, Role: messages.RoleTool, Value: messages.NewTextDeltaValue("tool ")},
		{Type: messages.StreamTypeAudioEnd, Role: messages.RoleAssistant, Value: messages.NewAudioEndValue()},
		{Type: messages.StreamTypeTextDelta, Role: messages.RoleTool, Value: messages.NewTextDeltaValue("continued")},
		{Type: messages.StreamTypeTextEnd, Role: messages.RoleTool, Value: messages.NewTextEndValue()},
	}
	for _, event := range events {
		if err := NewService().WriteMessage(renderer, event); err != nil {
			t.Fatalf("write event %s: %v", event.Type, err)
		}
	}

	want := "Assistant: assistant continued\n" +
		"User: user continued\n" +
		"Tool result: tool continued\n"
	if got := out.String(); got != want {
		t.Fatalf("actor-aware stream output = %q, want %q", got, want)
	}
}

func TestPublicTranscriptIgnoresLateCompletionForInactiveRole(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		events []messages.StreamMessage
	}{
		{
			name: "assistant completion while user line is active",
			events: []messages.StreamMessage{
				{Type: messages.StreamTypeTranscriptStart, Role: messages.RoleAssistant, Value: messages.NewTranscriptStartValue()},
				{Type: messages.StreamTypeTranscriptDelta, Role: messages.RoleAssistant, Value: messages.NewTranscriptDeltaValue("draft")},
				{Type: messages.StreamTypeTranscriptDelta, Role: messages.RoleUser, Value: messages.NewTranscriptDeltaValue("heard")},
				// The assistant completion arrives after the user role became active.
				{Type: messages.StreamTypeTranscriptEnd, Role: messages.RoleAssistant, Value: messages.NewTranscriptEndValue("draft revised")},
				{Type: messages.StreamTypeTranscriptEnd, Role: messages.RoleUser, Value: messages.NewTranscriptEndValue("heard revised")},
			},
		},
		{
			name: "assistant completion after user line closes",
			events: []messages.StreamMessage{
				{Type: messages.StreamTypeTranscriptStart, Role: messages.RoleAssistant, Value: messages.NewTranscriptStartValue()},
				{Type: messages.StreamTypeTranscriptDelta, Role: messages.RoleAssistant, Value: messages.NewTranscriptDeltaValue("draft")},
				{Type: messages.StreamTypeTranscriptDelta, Role: messages.RoleUser, Value: messages.NewTranscriptDeltaValue("heard")},
				{Type: messages.StreamTypeTranscriptEnd, Role: messages.RoleUser, Value: messages.NewTranscriptEndValue("heard revised")},
				// The assistant completion is late even though the active user
				// line has already been closed.
				{Type: messages.StreamTypeTranscriptEnd, Role: messages.RoleAssistant, Value: messages.NewTranscriptEndValue("draft revised")},
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			var out bytes.Buffer
			renderer := NewService().NewTranscript(&out, nil)
			for _, event := range testCase.events {
				if err := NewService().WriteMessage(renderer, event); err != nil {
					t.Fatalf("write transcript event: %v", err)
				}
			}

			if got, want := out.String(), "Assistant: draft\nUser: heard\n"; got != want {
				t.Fatalf("late interleaved transcript output = %q, want %q", got, want)
			}
		})
	}
}

func TestPublicTranscriptRendersInactiveCompletionOnly(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		events []messages.StreamMessage
		want   string
	}{
		{
			name: "assistant completion while user line is active",
			events: []messages.StreamMessage{
				{Type: messages.StreamTypeTranscriptStart, Role: messages.RoleUser, Value: messages.NewTranscriptStartValue()},
				{Type: messages.StreamTypeTranscriptDelta, Role: messages.RoleUser, Value: messages.NewTranscriptDeltaValue("heard")},
				// The assistant has no deltas, so its completion must be
				// rendered without disturbing the role attribution.
				{Type: messages.StreamTypeTranscriptEnd, Role: messages.RoleAssistant, Value: messages.NewTranscriptEndValue("reply")},
				{Type: messages.StreamTypeTranscriptEnd, Role: messages.RoleUser, Value: messages.NewTranscriptEndValue("heard")},
			},
			want: "User: heard\nAssistant: reply\n",
		},
		{
			name: "user completion while assistant line is active",
			events: []messages.StreamMessage{
				{Type: messages.StreamTypeTranscriptStart, Role: messages.RoleAssistant, Value: messages.NewTranscriptStartValue()},
				{Type: messages.StreamTypeTranscriptDelta, Role: messages.RoleAssistant, Value: messages.NewTranscriptDeltaValue("draft")},
				// The user has no deltas, so its completion must be rendered
				// as a separate line while the assistant remains distinct.
				{Type: messages.StreamTypeTranscriptEnd, Role: messages.RoleUser, Value: messages.NewTranscriptEndValue("heard")},
				{Type: messages.StreamTypeTranscriptEnd, Role: messages.RoleAssistant, Value: messages.NewTranscriptEndValue("draft")},
			},
			want: "Assistant: draft\nUser: heard\n",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			var out bytes.Buffer
			renderer := NewService().NewTranscript(&out, nil)
			for _, event := range testCase.events {
				if err := NewService().WriteMessage(renderer, event); err != nil {
					t.Fatalf("write transcript event: %v", err)
				}
			}

			if got := out.String(); got != testCase.want {
				t.Fatalf("completion-only transcript output = %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestPublicWriteMessageReturnsSessionErrorTerminalFields(t *testing.T) {
	err := NewService().WriteMessage(io.Discard, messages.StreamMessage{
		Type: messages.StreamTypeError,
		Value: messages.NewErrorValueWithTerminal(
			"provider rejected request",
			"provider_rejected",
			messages.TerminalReasonTerminalFailure,
			messages.TerminalProvenanceProvider,
			messages.TerminalOutputNone,
		),
	})
	if err == nil {
		t.Fatal("expected session error")
	}

	got := err.Error()
	for _, want := range []string{
		"session error: provider rejected request",
		"classification=provider_rejected",
		"terminal_reason=terminal_failure",
		"terminal_provenance=provider",
		"output_state=none",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("session error missing %q: %v", want, err)
		}
	}
}

func TestPublicWriteMessageIgnoresNonTerminalDiagnostic(t *testing.T) {
	err := NewService().WriteMessage(io.Discard, messages.StreamMessage{
		Type:  messages.StreamTypeError,
		Value: messages.NewNonTerminalErrorValue("response is not active", "response_cancel_not_active"),
	})
	if err != nil {
		t.Fatalf("nonterminal diagnostic became a replay error: %v", err)
	}
}

func TestPublicAdmissionForwardsNonTerminalDiagnosticWithoutShutdown(t *testing.T) {
	msg := messages.StreamMessage{
		Type:  messages.StreamTypeError,
		Value: messages.NewNonTerminalErrorValue("response is not active", "response_cancel_not_active"),
	}
	if NewService().IsDurationShutdownMessage(msg) {
		t.Fatal("nonterminal provider diagnostic is a shutdown message")
	}
	if !NewService().IsDurationForwardMessage(msg) {
		t.Fatal("nonterminal provider diagnostic was not retained for forwarding")
	}
}
