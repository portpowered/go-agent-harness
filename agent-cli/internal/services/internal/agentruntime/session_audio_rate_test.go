package agentruntime

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

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
			options:              SessionRunOptions{AudioService: newTestAudioIOService()},
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
	service := newTestAudioIOService()
	pcm := []byte{1, 2, 3, 4}
	got, err := service.ConvertPCM16(context.Background(), audioio.PCM16Request{PCM: pcm, SourceRate: wavio.Rate24kHz, TargetRate: wavio.Rate24kHz})
	if err != nil {
		t.Fatalf("identity conversion: %v", err)
	}
	if !bytes.Equal(got, pcm) || &got[0] != &pcm[0] {
		t.Fatal("matched-rate conversion did not preserve byte identity")
	}
	if _, err := service.ConvertPCM16(context.Background(), audioio.PCM16Request{PCM: []byte{1}, SourceRate: wavio.Rate16kHz, TargetRate: wavio.Rate24kHz}); !errors.Is(err, audioio.ErrPCM16Truncated) {
		t.Fatalf("truncated PCM error = %v, want audioio.ErrPCM16Truncated", err)
	}
	if _, err := service.ConvertPCM16(context.Background(), audioio.PCM16Request{PCM: pcm, SourceRate: 22050, TargetRate: wavio.Rate24kHz}); !errors.Is(err, wavio.ErrUnsupportedResampleRate) {
		t.Fatalf("unsupported rate error = %v, want ErrUnsupportedResampleRate", err)
	}
}

func TestConvertScheduledAudioInputsUsesDeclaredSourceRate(t *testing.T) {
	samples := make([]int16, wavio.Rate16kHz/10)
	pcm := make([]byte, len(samples)*2)
	for index := range samples {
		binary.LittleEndian.PutUint16(pcm[index*2:], uint16(index))
	}
	converted, err := newTestAudioIOService().ConvertScheduledInputs(context.Background(), []ScheduledAudioInput{{
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
