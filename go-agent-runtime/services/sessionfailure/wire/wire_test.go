package wire

import (
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionfailure"
)

func TestNewServiceBindsPublicContract(t *testing.T) {
	var service sessionfailure.Service = NewService(sessionfailure.Dependencies{})
	if service == nil {
		t.Fatal("Wire returned a nil session-failure service")
	}
	if service.OutputState(sessionfailure.Progress{}) == "" {
		t.Fatal("Wire-bound service did not execute")
	}
}
