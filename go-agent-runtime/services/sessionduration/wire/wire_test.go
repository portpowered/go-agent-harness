package wire

import (
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
)

func TestNewServiceReturnsPublicContract(t *testing.T) {
	var service sessionduration.Service = NewService()
	if service == nil {
		t.Fatal("NewService returned nil")
	}
	if service.NewState(sessionduration.TerminalSource{}) == nil {
		t.Fatal("NewService did not construct state")
	}
}
