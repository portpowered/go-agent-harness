package agentloop

import (
	"context"
	"errors"
	"testing"

	"github.com/portpowered/go-agent-loop/pkg/messages"
)

func TestExecuteResultFinalText_EmptySuccess(t *testing.T) {
	result := ExecuteResult{
		Messages: []messages.Message{
			messages.NewTextMessage(messages.RoleAssistant, ""),
		},
	}

	final := result.FinalText()
	if final.Status != FinalTextEmptySuccess {
		t.Fatalf("FinalText status = %q, want %q", final.Status, FinalTextEmptySuccess)
	}
	if final.Text != "" {
		t.Fatalf("FinalText text = %q, want empty", final.Text)
	}
	if final.Err != nil {
		t.Fatalf("FinalText err = %v, want nil", final.Err)
	}
}

func TestExecuteResultFinalText_NoFinalMessage(t *testing.T) {
	result := ExecuteResult{
		Messages: []messages.Message{
			{Role: messages.RoleAssistant, ToolCalls: []messages.ToolCall{{ID: "tc1", Name: "lookup"}}},
		},
	}

	final := result.FinalText()
	if final.Status != FinalTextNoFinalMessage {
		t.Fatalf("FinalText status = %q, want %q", final.Status, FinalTextNoFinalMessage)
	}
}

func TestExecuteResultFinalText_CanceledWithPartialText(t *testing.T) {
	result := ExecuteResult{
		Deltas: []messages.StreamMessage{
			{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("partial")},
		},
		Err: context.Canceled,
	}

	final := result.FinalText()
	if final.Status != FinalTextCanceled {
		t.Fatalf("FinalText status = %q, want %q", final.Status, FinalTextCanceled)
	}
	if !errors.Is(final.Err, context.Canceled) {
		t.Fatalf("FinalText err = %v, want context.Canceled", final.Err)
	}
	if !final.Partial {
		t.Fatal("FinalText Partial = false, want true")
	}
	if final.Text != "partial" {
		t.Fatalf("FinalText text = %q, want partial", final.Text)
	}
}

func TestExecuteResultFinalText_TerminalFailure(t *testing.T) {
	err := errors.New("inference failed")
	result := ExecuteResult{Err: err}

	final := result.FinalText()
	if final.Status != FinalTextFailed {
		t.Fatalf("FinalText status = %q, want %q", final.Status, FinalTextFailed)
	}
	if !errors.Is(final.Err, err) {
		t.Fatalf("FinalText err = %v, want %v", final.Err, err)
	}
	if final.Partial {
		t.Fatal("FinalText Partial = true, want false")
	}
}
