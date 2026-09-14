package agentruntime

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"testing"

	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

func waitForAudioSignal(t *testing.T, ctx context.Context, signal <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-signal:
	case <-ctx.Done():
		t.Fatal(message)
	}
}

func TestStreamSessionAudioInputResamples16kHzAtProviderBoundary(t *testing.T) {
	samples := make([]int16, audio.FrameSize)
	for index := range samples {
		samples[index] = int16(index*11 - 2000)
	}
	var captured []byte
	endOfTurn := false
	source := &sessionAudioSource{
		source:       audio.NewSliceSource(samples),
		path:         "injected-16k",
		sourceRate:   wavio.Rate16kHz,
		providerRate: wavio.Rate24kHz,
		send: func(_ context.Context, pcm []byte) error {
			captured = append(captured, pcm...)
			return nil
		},
		endOfTurn: func(context.Context) error {
			endOfTurn = true
			return nil
		},
	}

	if err := streamSessionAudioInput(context.Background(), nil, source); err != nil {
		t.Fatalf("stream input: %v", err)
	}
	if !endOfTurn {
		t.Fatal("end-of-turn was not sent after converted audio")
	}
	gotSamples := len(captured) / 2
	wantSamples := len(samples) * wavio.Rate24kHz / wavio.Rate16kHz
	if gotSamples != wantSamples {
		t.Fatalf("provider samples = %d, want %d; duration changed", gotSamples, wantSamples)
	}
}

func TestConvertSessionAudioPCMIdentityAndFailures(t *testing.T) {
	pcm := []byte{1, 2, 3, 4}
	got, err := convertSessionAudioPCM(pcm, wavio.Rate24kHz, wavio.Rate24kHz)
	if err != nil {
		t.Fatalf("identity conversion: %v", err)
	}
	if !bytes.Equal(got, pcm) || &got[0] != &pcm[0] {
		t.Fatal("matched-rate conversion did not preserve byte identity")
	}
	if _, err := convertSessionAudioPCM([]byte{1}, wavio.Rate16kHz, wavio.Rate24kHz); !errors.Is(err, ErrSessionAudioPCM16Truncated) {
		t.Fatalf("truncated PCM error = %v, want ErrSessionAudioPCM16Truncated", err)
	}
	if _, err := convertSessionAudioPCM(pcm, 22050, wavio.Rate24kHz); !errors.Is(err, wavio.ErrUnsupportedResampleRate) {
		t.Fatalf("unsupported rate error = %v, want ErrUnsupportedResampleRate", err)
	}
}

func TestConvertScheduledAudioInputsUsesDeclaredSourceRate(t *testing.T) {
	samples := make([]int16, wavio.Rate16kHz/10)
	pcm := make([]byte, len(samples)*2)
	for index := range samples {
		binary.LittleEndian.PutUint16(pcm[index*2:], uint16(index))
	}
	converted, err := convertScheduledAudioInputs([]ScheduledAudioInput{{
		PCM:              pcm,
		SourceSampleRate: wavio.Rate16kHz,
		EndOfTurn:        true,
	}}, wavio.Rate24kHz)
	if err != nil {
		t.Fatalf("convert scheduled input: %v", err)
	}
	if got, want := len(converted[0].PCM)/2, len(samples)*wavio.Rate24kHz/wavio.Rate16kHz; got != want {
		t.Fatalf("scheduled provider samples = %d, want %d", got, want)
	}
	if converted[0].SourceSampleRate != wavio.Rate24kHz || !converted[0].EndOfTurn {
		t.Fatalf("converted metadata = %+v", converted[0])
	}
}
