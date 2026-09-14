package wire

import (
	"errors"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturns"
)

func TestNewServiceReturnsPublicContractWithoutConnecting(t *testing.T) {
	service := NewService(Dependencies{})
	if service == nil {
		t.Fatal("NewService returned nil")
	}
	if _, err := service.StartTurn(sessionturns.TurnInput{Text: "wire"}, sessionturns.TurnDirectionUser, 1); err != nil {
		t.Fatalf("StartTurn = %v", err)
	}
	if err := service.Close(); !errors.Is(err, sessionturns.ErrSessionEndedWithActiveTurn) {
		t.Fatalf("active Close = %v", err)
	}
}
