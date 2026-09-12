package wire

import (
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomreplaybundle"
)

func TestNewServiceReturnsIndependentAdmissionServices(t *testing.T) {
	first := NewService()
	second := NewService()
	if first == nil || second == nil {
		t.Fatal("NewService returned nil")
	}
	if first == second {
		t.Fatal("NewService returned shared service state")
	}
	var _ roomreplaybundle.Service = first
}
