package wire_test

import (
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionfailure"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionfailure/wire"
)

func TestNewServiceBindsPublicContract(t *testing.T) {
	var service sessionfailure.Service = wire.NewService(sessionfailure.Dependencies{})
	if service == nil {
		t.Fatal("Wire returned a nil session-failure service")
	}
	if service.OutputState(sessionfailure.Progress{}) == "" {
		t.Fatal("Wire-bound service did not execute")
	}
}
