package wire

import (
	"errors"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rtcsession"
)

func TestNewServiceReturnsPublicServiceContract(t *testing.T) {
	service := NewService(rtcsession.SessionRTCComponents{}, nil, nil)
	if service == nil {
		t.Fatal("NewService returned nil")
	}
	if _, err := service.NewRuntime(rtcsession.SessionRuntimeSelection{}); !errors.Is(err, rtcsession.ErrSessionRTCRuntimeUnavailable) {
		t.Fatalf("invalid component graph error = %v, want unavailable identity", err)
	}
}
