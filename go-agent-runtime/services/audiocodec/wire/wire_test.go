package wire

import (
	"context"
	"errors"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audiocodec"
)

func TestNewServiceConstructsIndependentPublicServices(t *testing.T) {
	first := NewService()
	second := NewService()
	if first == nil || second == nil || first == second {
		t.Fatal("NewService did not construct independent services")
	}
	limits := audiocodec.Limits{
		MaxInputBytes:  audiocodec.DefaultMaxInputBytes,
		MaxOutputBytes: audiocodec.DefaultMaxOutputBytes,
		MaxStderrBytes: audiocodec.DefaultMaxStderrBytes,
	}
	limits.MaxInputBytes = 0
	if _, err := first.Convert(context.Background(), audiocodec.Request{Limits: limits}); !errors.Is(err, audiocodec.ErrInvalidRequest) {
		t.Fatalf("invalid request error = %v", err)
	}
}
