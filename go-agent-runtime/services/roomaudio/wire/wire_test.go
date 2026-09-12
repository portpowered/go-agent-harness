package wire

import (
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomaudio"
)

func TestNewServiceReturnsIndependentServices(t *testing.T) {
	first := NewService()
	second := NewService()
	if first == nil || second == nil {
		t.Fatal("NewService returned nil")
	}
	if first == second {
		t.Fatal("NewService reused a service instance")
	}
	var _ roomaudio.Service = first
}
