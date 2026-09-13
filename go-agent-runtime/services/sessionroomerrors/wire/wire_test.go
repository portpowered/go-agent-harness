package wire

import (
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionroomerrors"
)

func TestNewServiceReturnsIndependentPublicContracts(t *testing.T) {
	first := NewService()
	second := NewService()
	if first == nil || second == nil {
		t.Fatal("NewService returned nil")
	}
	if first == second {
		t.Fatal("NewService returned shared service state")
	}
	if got := first.FailureResult(nil, nil); got.Participants == nil {
		t.Fatal("NewService returned invalid failed-room projection")
	}
	var _ sessionroomerrors.Service = first
}
