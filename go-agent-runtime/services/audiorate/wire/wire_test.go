package wire

import (
	"context"
	"testing"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

func TestNewServiceProvidesPublicAudioRateContract(t *testing.T) {
	service := NewService()
	if service == nil {
		t.Fatal("NewService returned nil")
	}
	pcm := codec.EncodePCM16([]int16{1, 2, 3})
	converted, err := service.ConvertPCM(context.Background(), pcm, wavio.Rate16kHz, wavio.Rate24kHz)
	if err != nil {
		t.Fatalf("convert through Wire service: %v", err)
	}
	if got, want := len(converted)/2, (3*wavio.Rate24kHz+wavio.Rate16kHz-1)/wavio.Rate16kHz; got != want {
		t.Fatalf("converted samples = %d, want %d", got, want)
	}

}
