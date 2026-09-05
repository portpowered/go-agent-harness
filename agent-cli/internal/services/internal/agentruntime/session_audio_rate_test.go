package agentruntime

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

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

func TestRoomProviderInputPCMResamples16kHzMixerTo24kHzContract(t *testing.T) {
	mixer, err := room.NewPCM16Mixer(context.Background(), room.PCM16Format{
		SampleRate:    wavio.Rate16kHz,
		Channels:      1,
		FrameDuration: 30 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new mixer: %v", err)
	}
	t.Cleanup(func() { _ = mixer.Close() })
	sourceSamples := wavio.Rate16kHz * 30 / 1000
	pcm := make([]byte, sourceSamples*2)
	runtime := &roomParticipantRuntime{
		mixer: mixer,
		plan: &roomParticipantPlan{
			manifest:             room.Participant{ID: "agent-a"},
			inputAudioSampleRate: wavio.Rate24kHz,
		},
	}
	converted, err := roomProviderInputPCM(runtime, pcm)
	if err != nil {
		t.Fatalf("convert room input: %v", err)
	}
	if got, want := len(converted)/2, sourceSamples*wavio.Rate24kHz/wavio.Rate16kHz; got != want {
		t.Fatalf("room provider samples = %d, want %d", got, want)
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
