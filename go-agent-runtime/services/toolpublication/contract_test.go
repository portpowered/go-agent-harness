package toolpublication_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/toolpublication"
)

func TestPublicationErrorPreservesIdentityAndBoundsText(t *testing.T) {
	cause := errors.New("  " + strings.Repeat("x", 300) + "  ")
	err := &toolpublication.PublicationError{Phase: "broker_event_send", Sequence: 42, Err: cause}
	if !errors.Is(err, toolpublication.ErrSessionDynamicToolPublication) || !errors.Is(err, cause) {
		t.Fatalf("error identity was not preserved: %v", err)
	}
	if !strings.Contains(err.Error(), "phase=broker_event_send sequence=42") || len(err.Error()) > 360 {
		t.Fatalf("bounded error text = %q", err)
	}
	if got := err.ErrString(); strings.TrimSpace(got) != got || len(got) > 259 {
		t.Fatalf("bounded cause text = %q", got)
	}
	var nilError *toolpublication.PublicationError
	if got := nilError.Error(); got != toolpublication.ErrSessionDynamicToolPublication.Error() {
		t.Fatalf("nil error text = %q", got)
	}
	if got := nilError.ErrString(); got != "" {
		t.Fatalf("nil error cause text = %q", got)
	}
	if !errors.Is(nilError, toolpublication.ErrSessionDynamicToolPublication) {
		t.Fatal("nil error did not preserve publication identity")
	}
	empty := &toolpublication.PublicationError{Err: errors.New("   ")}
	if empty.ErrString() != "" || !strings.Contains(empty.Error(), "sequence=0: unknown error") {
		t.Fatalf("empty error text = %q", empty.Error())
	}
}

func TestSessionUpdateSinkFuncAndNilError(t *testing.T) {
	called := false
	sink := toolpublication.SessionUpdateSinkFunc(func(context.Context, []messages.ToolDefinition) error {
		called = true
		return nil
	})
	if err := sink.SendSessionUpdate(context.Background(), nil); err != nil || !called {
		t.Fatalf("sink call = err %v called %v", err, called)
	}
	var nilSink toolpublication.SessionUpdateSinkFunc
	if err := nilSink.SendSessionUpdate(context.Background(), nil); err == nil {
		t.Fatal("nil sink unexpectedly succeeded")
	}
}
