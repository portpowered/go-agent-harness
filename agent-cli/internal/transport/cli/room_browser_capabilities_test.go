package cli

import (
	"reflect"
	"testing"

	runtimeRooms "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
)

func TestNewRoomParticipantBrowserCapabilitiesFactoryCreatesIndependentOwners(t *testing.T) {
	factory := NewRoomParticipantBrowserCapabilitiesFactory(t.TempDir())
	participant := func(id string) runtimeRooms.Participant {
		browser := defaultRuntimeBrowserToolsConfig()
		browser.Connection.CDPURL = "http://127.0.0.1:9222"
		return runtimeRooms.Participant{ID: id, BrowserTools: &browser}
	}

	alpha, err := factory(participant("alpha"))
	if err != nil {
		t.Fatalf("construct alpha browser capability: %v", err)
	}
	beta, err := factory(participant("beta"))
	if err != nil {
		_ = alpha.Close()
		t.Fatalf("construct beta browser capability: %v", err)
	}
	if alpha.Executor == nil || beta.Executor == nil {
		_ = alpha.Close()
		_ = beta.Close()
		t.Fatal("room browser capability returned a nil executor")
	}
	if reflect.ValueOf(alpha.Executor).Pointer() == reflect.ValueOf(beta.Executor).Pointer() {
		_ = alpha.Close()
		_ = beta.Close()
		t.Fatal("room participants share a composed browser executor")
	}
	if len(alpha.Definitions) == 0 || len(alpha.ToolDefinitionBase) == 0 || len(alpha.Definitions) != len(beta.Definitions) {
		_ = alpha.Close()
		_ = beta.Close()
		t.Fatalf("browser definitions = %d/%d, want independent complete stable surfaces", len(alpha.Definitions), len(beta.Definitions))
	}
	if err := alpha.Close(); err != nil {
		t.Fatalf("close alpha browser capability: %v", err)
	}
	if err := beta.Close(); err != nil {
		t.Fatalf("close beta browser capability: %v", err)
	}
}
