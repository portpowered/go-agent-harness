package wire

import (
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionterminal"
)

func TestNewServiceReturnsIndependentUsableServices(t *testing.T) {
	first := NewService()
	second := NewService()
	if first == nil || second == nil {
		t.Fatal("Wire returned nil service")
	}
	if got := first.CancellationOutputState(sessionterminal.OutputSnapshot{}); got != "none" {
		t.Fatalf("first service output state = %q", got)
	}
	if got := second.CancellationOutputState(sessionterminal.OutputSnapshot{TurnsCompleted: 1}); got != "partial" {
		t.Fatalf("second service output state = %q", got)
	}
}
