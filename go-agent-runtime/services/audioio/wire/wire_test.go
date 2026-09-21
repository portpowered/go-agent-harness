package wire

import (
	"context"
	"errors"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio"
)

func TestServiceRejectsOddPCM16WithoutPublication(t *testing.T) {
	service := NewService()
	_, err := service.ConvertPCM16(context.Background(), audioio.PCM16Request{
		PCM: []byte{1, 2, 3}, SourceRate: audioio.SampleRate16kHz, TargetRate: audioio.SampleRate16kHz,
	})
	if !errors.Is(err, audioio.ErrPCM16Truncated) {
		t.Fatalf("error = %v, want ErrPCM16Truncated", err)
	}
}

func TestServiceResolvesOneDuplexRate(t *testing.T) {
	service := NewService()
	got, err := service.ResolveRates(context.Background(), audioio.RateRequest{Provider: audioio.ProviderOpenAI})
	if err != nil {
		t.Fatalf("ResolveRates() error = %v", err)
	}
	if got.InputRate != audioio.RealtimeSampleRate || got.OutputRate != audioio.RealtimeSampleRate {
		t.Fatalf("rates = %+v, want realtime rate", got)
	}
	if _, err := service.ResolveRates(context.Background(), audioio.RateRequest{CapturedInputRate: audioio.SampleRate16kHz, CapturedOutputRate: audioio.SampleRate24kHz}); !errors.Is(err, audioio.ErrSampleRateConflict) {
		t.Fatalf("conflict = %v, want ErrSampleRateConflict", err)
	}
}
