package service

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

type recordingOutbound struct {
	frames []audio.PCMFrame
}

func (o *recordingOutbound) WriteFrame(ctx context.Context, frame audio.PCMFrame) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	o.frames = append(o.frames, audio.PCMFrame{Samples: append([]int16(nil), frame.Samples...), EndOfResponse: frame.EndOfResponse})
	return nil
}

func (*recordingOutbound) Close() error { return nil }

type recordingSink struct {
	samples []int16
}

func (s *recordingSink) WriteFrame(ctx context.Context, frame []int16) error {
	return s.WriteSamples(ctx, frame)
}

func (s *recordingSink) WriteSamples(ctx context.Context, samples []int16) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.samples = append(s.samples, samples...)
	return nil
}

func (*recordingSink) Close() error { return nil }

type recordingInbound struct {
	frames []audio.PCMFrame
	index  int
}

func (i *recordingInbound) ReadFrame(ctx context.Context) (audio.PCMFrame, error) {
	if err := ctx.Err(); err != nil {
		return audio.PCMFrame{}, err
	}
	if i.index == len(i.frames) {
		return audio.PCMFrame{}, io.EOF
	}
	frame := i.frames[i.index]
	i.index++
	return frame, nil
}

func (*recordingInbound) Close() error { return nil }

type turnSource struct {
	turns int
}

func (s *turnSource) ReadFrame(_ context.Context, frame []int16) error {
	if s.turns == 0 {
		for index := range frame {
			frame[index] = 1000
		}
		s.turns++
		return nil
	}
	if s.turns == 1 {
		s.turns++
		return audio.ErrEndOfTurn
	}
	return io.EOF
}

func (*turnSource) Close() error { return nil }

func TestServiceResolvesRatesAndAudioPolicies(t *testing.T) {
	service := New()
	ctx := context.Background()
	var nilContext context.Context
	for _, test := range []struct {
		name    string
		want    int
		request audioio.RateRequest
	}{
		{name: "realtime default", want: audioio.RealtimeSampleRate, request: audioio.RateRequest{Provider: audioio.ProviderOpenAI}},
		{name: "replay default", want: audioio.DefaultSampleRate, request: audioio.RateRequest{Provider: audioio.ProviderOpenAI, Replay: true}},
		{name: "requested", want: audioio.SampleRate48kHz, request: audioio.RateRequest{RequestedInputRate: audioio.SampleRate48kHz}},
		{name: "captured", want: audioio.SampleRate16kHz, request: audioio.RateRequest{CapturedOutputRate: audioio.SampleRate16kHz}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := service.ResolveRates(ctx, test.request)
			if err != nil {
				t.Fatalf("ResolveRates() error = %v", err)
			}
			if got.InputRate != test.want || got.OutputRate != test.want {
				t.Fatalf("rates = %+v, want %d", got, test.want)
			}
		})
	}
	if _, err := service.ResolveRates(ctx, audioio.RateRequest{CapturedInputRate: audioio.SampleRate16kHz, CapturedOutputRate: audioio.SampleRate24kHz}); !errors.Is(err, audioio.ErrSampleRateConflict) {
		t.Fatalf("conflict = %v, want ErrSampleRateConflict", err)
	}
	if _, err := service.ResolveRates(nilContext, audioio.RateRequest{}); err == nil {
		t.Fatal("nil context unexpectedly accepted")
	}
	if got := service.ResolveTranscription(audioio.TranscriptionRequest{Provider: audioio.ProviderOpenAI, AcceptsAudioInput: true, Enabled: true}); !got.Enabled || got.Model != audioio.DefaultTranscriptionModel {
		t.Fatalf("transcription = %+v, want enabled default", got)
	}
	if got := service.ResolveTranscription(audioio.TranscriptionRequest{Provider: audioio.ProviderOpenAI, AcceptsAudioInput: true, Enabled: true, Replay: true}); got.Enabled {
		t.Fatal("replay transcription unexpectedly enabled")
	}
	if service.VoiceGainDB("verse") <= 0 {
		t.Fatal("voice gain did not resolve")
	}
	timer, err := service.NewTimer(platformclock.Real{}, 0)
	if err != nil {
		t.Fatalf("NewTimer() error = %v", err)
	}
	timer.Stop()
}

func TestServiceConvertsPCM16AndRejectsOddTail(t *testing.T) {
	service := New()
	ctx := context.Background()
	var nilContext context.Context
	pcm := []byte{1, 2, 3, 4}
	got, err := service.ConvertPCM16(ctx, audioio.PCM16Request{PCM: pcm, SourceRate: audioio.SampleRate16kHz, TargetRate: audioio.SampleRate16kHz})
	if err != nil || string(got) != string(pcm) {
		t.Fatalf("equal-rate conversion = %v, %v; want exact payload", got, err)
	}
	if _, err := service.ConvertPCM16(ctx, audioio.PCM16Request{PCM: []byte{1, 2, 3}, SourceRate: audioio.SampleRate16kHz}); !errors.Is(err, audioio.ErrPCM16Truncated) {
		t.Fatalf("odd tail = %v, want ErrPCM16Truncated", err)
	}
	converted, err := service.ConvertPCM16(ctx, audioio.PCM16Request{PCM: pcm, SourceRate: audioio.SampleRate16kHz, TargetRate: audioio.SampleRate24kHz})
	if err != nil || len(converted) == 0 {
		t.Fatalf("resampled conversion = %v, %v", converted, err)
	}
	marker := []byte{7}
	converted, err = service.ConvertPCM16(ctx, audioio.PCM16Request{PCM: marker})
	if err != nil || string(converted) != string(marker) {
		t.Fatalf("injected payload = %v, %v; want marker preserved", converted, err)
	}
	if _, err := service.ConvertPCM16(nilContext, audioio.PCM16Request{}); err == nil {
		t.Fatal("nil conversion context unexpectedly accepted")
	}
}

func TestServiceInputPumpPreservesTurnAndTail(t *testing.T) {
	service := New()
	input, err := service.OpenInput(context.Background(), audioio.InputRequest{
		Source: audio.NewSliceSource([]int16{1000, -1000}), SourceRate: audioio.SampleRate16kHz, ProviderRate: audioio.SampleRate16kHz,
	})
	if err != nil {
		t.Fatalf("OpenInput() error = %v", err)
	}
	out := &recordingOutbound{}
	if err := input.Pump(context.Background(), out); err != nil {
		t.Fatalf("Pump() error = %v", err)
	}
	if len(out.frames) != 1 || len(out.frames[0].Samples) != 2 || !out.frames[0].EndOfResponse {
		t.Fatalf("frames = %+v, want one exact terminal tail", out.frames)
	}
	if err := input.Close(); err != nil {
		t.Fatalf("first Close() error = %v", err)
	}
	if err := input.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	if _, err := service.OpenInput(context.Background(), audioio.InputRequest{}); err == nil {
		t.Fatal("nil source unexpectedly accepted")
	}
}

func TestServiceInputPumpNotifiesExplicitTurnBoundary(t *testing.T) {
	service := New()
	boundaries := 0
	input, err := service.OpenInput(context.Background(), audioio.InputRequest{
		Source: &turnSource{}, SourceRate: audioio.SampleRate16kHz, ProviderRate: audioio.SampleRate16kHz,
		OnTurnBoundary: func(context.Context) error { boundaries++; return nil },
	})
	if err != nil {
		t.Fatalf("OpenInput() error = %v", err)
	}
	out := &recordingOutbound{}
	if err := input.Pump(context.Background(), out); err != nil {
		t.Fatalf("Pump() error = %v", err)
	}
	if boundaries != 1 {
		t.Fatalf("boundaries = %d, want one", boundaries)
	}
}

func TestServiceOutputPumpWritesAndCloses(t *testing.T) {
	service := New()
	sink := &recordingSink{}
	output, err := service.OpenOutput(context.Background(), audioio.OutputRequest{
		Sink: sink, SinkRate: audioio.SampleRate16kHz, ProviderRate: audioio.SampleRate16kHz, Voice: "verse",
	})
	if err != nil {
		t.Fatalf("OpenOutput() error = %v", err)
	}
	if err := output.Pump(context.Background(), &recordingInbound{frames: []audio.PCMFrame{{Samples: make([]int16, audio.FrameSize), EndOfResponse: true}}}); err != nil {
		t.Fatalf("Pump() error = %v", err)
	}
	if len(sink.samples) != audio.FrameSize {
		t.Fatalf("written samples = %d, want %d", len(sink.samples), audio.FrameSize)
	}
	if err := output.Write(context.Background(), audio.PCMFrame{Samples: []int16{1, 2}}); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := output.Close(); err != nil {
		t.Fatalf("first Close() error = %v", err)
	}
	if err := output.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	if _, err := service.OpenOutput(context.Background(), audioio.OutputRequest{}); err == nil {
		t.Fatal("nil sink unexpectedly accepted")
	}
}
