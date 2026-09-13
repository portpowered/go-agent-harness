package service

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"reflect"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audiorate"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

func TestConvertPCMCanonicalRatesAndIdentity(t *testing.T) {
	service := New()
	samples := []int16{-32000, -1000, 1000, 32000}
	pcm := codec.EncodePCM16(samples)

	got, err := service.ConvertPCM(context.Background(), pcm, wavio.Rate24kHz, wavio.Rate24kHz)
	if err != nil {
		t.Fatalf("identity conversion: %v", err)
	}
	if !bytes.Equal(got, pcm) || &got[0] != &pcm[0] {
		t.Fatal("matched-rate conversion did not preserve input backing identity")
	}

	for _, test := range []struct {
		name       string
		sourceRate int
		targetRate int
	}{
		{name: "16-to-24", sourceRate: wavio.Rate16kHz, targetRate: wavio.Rate24kHz},
		{name: "24-to-48", sourceRate: wavio.Rate24kHz, targetRate: wavio.Rate48kHz},
		{name: "48-to-16", sourceRate: wavio.Rate48kHz, targetRate: wavio.Rate16kHz},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := service.ConvertPCM(context.Background(), pcm, test.sourceRate, test.targetRate)
			if err != nil {
				t.Fatalf("convert PCM: %v", err)
			}
			wantSamples := (len(samples)*test.targetRate + test.sourceRate - 1) / test.sourceRate
			if gotSamples := len(got) / 2; gotSamples != wantSamples {
				t.Fatalf("converted samples = %d, want %d", gotSamples, wantSamples)
			}
		})
	}

	got, err = service.ConvertPCM(context.Background(), pcm, 0, wavio.Rate24kHz)
	if err != nil {
		t.Fatalf("provider-rate identity conversion: %v", err)
	}
	if &got[0] != &pcm[0] {
		t.Fatal("zero-source-rate conversion did not preserve input backing identity")
	}
}

func TestConvertPCMRejectsOddAndUnsupportedRatesBeforeConversion(t *testing.T) {
	service := New()
	for _, test := range []struct {
		name      string
		pcm       []byte
		source    int
		target    int
		wantError error
		wantCodec error
	}{
		{name: "odd source already provider rate", pcm: []byte{1}, target: wavio.Rate24kHz, wantError: audiorate.ErrPCM16Truncated, wantCodec: codec.ErrPCM16OddLength},
		{name: "unsupported source", pcm: []byte{1, 2}, source: 22050, target: wavio.Rate24kHz, wantError: audiorate.ErrUnsupportedSampleRate},
		{name: "unsupported target", pcm: []byte{1, 2}, source: wavio.Rate16kHz, target: 22050, wantError: audiorate.ErrUnsupportedSampleRate},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := service.ConvertPCM(context.Background(), test.pcm, test.source, test.target)
			if got != nil {
				t.Fatalf("failed conversion returned %v", got)
			}
			if !errors.Is(err, test.wantError) {
				t.Fatalf("error = %v, want errors.Is(%v)", err, test.wantError)
			}
			if test.wantCodec != nil && !errors.Is(err, test.wantCodec) {
				t.Fatalf("error = %v, want errors.Is(%v)", err, test.wantCodec)
			}
			if errors.Is(test.wantError, audiorate.ErrUnsupportedSampleRate) && !errors.Is(err, wavio.ErrUnsupportedResampleRate) {
				t.Fatalf("error = %v, want canonical unsupported-rate identity", err)
			}
		})
	}
}

func TestConvertScheduledAudioInputsPreservesOrderMetadataAndInputs(t *testing.T) {
	service := New()
	firstPCM := pcm16(1, 2, 3)
	secondPCM := pcm16(4, 5)
	inputs := []audiorate.ScheduledAudioInput{
		{AfterCompletedTurns: 2, PCM: firstPCM, SourceSampleRate: wavio.Rate16kHz, EndOfTurn: true},
		{AfterCompletedTurns: 0, PCM: secondPCM, SourceSampleRate: wavio.Rate24kHz},
	}
	original := cloneInputs(inputs)

	converted, err := service.ConvertScheduledAudioInputs(context.Background(), inputs, wavio.Rate48kHz)
	if err != nil {
		t.Fatalf("convert scheduled inputs: %v", err)
	}
	if !reflect.DeepEqual(inputs, original) {
		t.Fatal("scheduled input was mutated")
	}
	if len(converted) != len(inputs) || converted[0].AfterCompletedTurns != 2 || !converted[0].EndOfTurn || converted[1].AfterCompletedTurns != 0 {
		t.Fatalf("scheduled metadata/order changed: %+v", converted)
	}
	for index, input := range converted {
		if input.SourceSampleRate != wavio.Rate48kHz || len(input.PCM) == 0 {
			t.Fatalf("converted[%d] metadata = %+v", index, input)
		}
	}

	failed, err := service.ConvertScheduledAudioInputs(context.Background(), append(inputs, audiorate.ScheduledAudioInput{PCM: []byte{1}}), wavio.Rate48kHz)
	if failed != nil || !errors.Is(err, audiorate.ErrPCM16Truncated) {
		t.Fatalf("failed scheduled conversion = %v, %v; want no partial result and truncation", failed, err)
	}
}

func TestConvertPCMHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := New().ConvertPCM(ctx, pcm16(1), wavio.Rate16kHz, wavio.Rate24kHz)
	if got != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled conversion = %v, %v; want context.Canceled and no bytes", got, err)
	}
}

func TestServiceHandlesNilContextsAndEmptySchedules(t *testing.T) {
	service := New()
	var nilContext context.Context
	if _, err := service.ConvertPCM(nilContext, pcm16(1), wavio.Rate16kHz, wavio.Rate24kHz); err != nil {
		t.Fatalf("nil conversion context: %v", err)
	}
	if _, err := service.ResolveSampleRate(nilContext, audiorate.RateResolutionRequest{Provider: audiorate.ProviderOpenAI}); err != nil {
		t.Fatalf("nil resolution context: %v", err)
	}
	if got, err := service.ConvertScheduledAudioInputs(context.Background(), nil, 0); err != nil || got != nil {
		t.Fatalf("nil schedule = %v, %v; want nil, nil", got, err)
	}
	empty := []audiorate.ScheduledAudioInput{}
	got, err := service.ConvertScheduledAudioInputs(context.Background(), empty, wavio.Rate24kHz)
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("empty schedule = %#v, %v; want non-nil empty result", got, err)
	}
	withSource, err := service.ConvertScheduledAudioInputs(context.Background(), []audiorate.ScheduledAudioInput{{PCM: pcm16(1), SourceSampleRate: wavio.Rate16kHz}}, 0)
	if err != nil || withSource[0].SourceSampleRate != wavio.Rate16kHz {
		t.Fatalf("zero provider schedule = %#v, %v", withSource, err)
	}
	withDefault, err := service.ConvertScheduledAudioInputs(context.Background(), []audiorate.ScheduledAudioInput{{PCM: pcm16(1)}}, 0)
	if err != nil || withDefault[0].SourceSampleRate != audiorate.DefaultSampleRate {
		t.Fatalf("zero provider/source schedule = %#v, %v", withDefault, err)
	}
}

func TestConvertPCMReportsCanonicalPayloadLimit(t *testing.T) {
	pcm := make([]byte, codec.MaxPCM16Bytes+2)
	got, err := New().ConvertPCM(context.Background(), pcm, wavio.Rate16kHz, wavio.Rate24kHz)
	if got != nil || !errors.Is(err, codec.ErrPayloadTooLarge) {
		t.Fatalf("oversized conversion = %v, %v; want codec payload-limit identity", got, err)
	}
}

func TestResolveSampleRateUsesExplicitFactsAndDefaults(t *testing.T) {
	service := New()
	for _, test := range []struct {
		name string
		in   audiorate.RateResolutionRequest
		want int
		err  error
	}{
		{name: "plain default", want: audiorate.DefaultSampleRate},
		{name: "live openai default", in: audiorate.RateResolutionRequest{Provider: audiorate.ProviderOpenAI}, want: audiorate.RealtimeSampleRate},
		{name: "live grok default", in: audiorate.RateResolutionRequest{Provider: audiorate.ProviderGrok}, want: audiorate.RealtimeSampleRate},
		{name: "replay keeps default", in: audiorate.RateResolutionRequest{Provider: audiorate.ProviderOpenAI, Replay: true}, want: audiorate.DefaultSampleRate},
		{name: "captured wins request", in: audiorate.RateResolutionRequest{CapturedInputRate: wavio.Rate16kHz, RequestedInputRate: wavio.Rate48kHz}, want: wavio.Rate16kHz},
		{name: "requested output", in: audiorate.RateResolutionRequest{RequestedOutputRate: wavio.Rate48kHz}, want: wavio.Rate48kHz},
		{name: "conflicting input output", in: audiorate.RateResolutionRequest{RequestedInputRate: wavio.Rate16kHz, RequestedOutputRate: wavio.Rate24kHz}, err: audiorate.ErrSampleRateConflict},
		{name: "unsupported explicit rate", in: audiorate.RateResolutionRequest{CapturedInputRate: 22050}, err: audiorate.ErrUnsupportedSampleRate},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := service.ResolveSampleRate(context.Background(), test.in)
			if test.err != nil {
				if got != 0 || !errors.Is(err, test.err) {
					t.Fatalf("resolution = %d, %v; want errors.Is(%v)", got, err, test.err)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("resolution = %d, %v; want %d", got, err, test.want)
			}
		})
	}
}

func TestConfigureSessionAudioContractValidatesBeforeSettersAndStopsOnCancel(t *testing.T) {
	service := New()
	output := &configurer{}
	input := &configurer{}
	_, err := service.ConfigureSessionAudioContract(context.Background(), audiorate.ConfigureRequest{
		Resolution: audiorate.RateResolutionRequest{RequestedInputRate: wavio.Rate16kHz, RequestedOutputRate: wavio.Rate24kHz},
		Input:      input,
		Output:     output,
	})
	if !errors.Is(err, audiorate.ErrSampleRateConflict) || output.calls != 0 || input.calls != 0 {
		t.Fatalf("conflicting configuration = %v with setter calls %d/%d", err, output.calls, input.calls)
	}

	ctx, cancel := context.WithCancel(context.Background())
	output.cancel = cancel
	_, err = service.ConfigureSessionAudioContract(ctx, audiorate.ConfigureRequest{
		Resolution: audiorate.RateResolutionRequest{RequestedInputRate: wavio.Rate24kHz},
		Input:      input,
		Output:     output,
	})
	if !errors.Is(err, context.Canceled) || output.calls != 1 || input.calls != 0 {
		t.Fatalf("canceled configuration = %v with setter calls %d/%d", err, output.calls, input.calls)
	}

	input = &configurer{}
	if rate, err := service.ConfigureSessionAudioContract(context.Background(), audiorate.ConfigureRequest{
		Resolution: audiorate.RateResolutionRequest{RequestedInputRate: wavio.Rate16kHz},
		Input:      input,
	}); err != nil || rate != wavio.Rate16kHz || input.calls != 1 {
		t.Fatalf("input-only configuration = %d, %v, calls=%d", rate, err, input.calls)
	}
	output = &configurer{}
	if rate, err := service.ConfigureSessionAudioContract(context.Background(), audiorate.ConfigureRequest{
		Resolution: audiorate.RateResolutionRequest{RequestedOutputRate: wavio.Rate48kHz},
		Output:     output,
	}); err != nil || rate != wavio.Rate48kHz || output.calls != 1 {
		t.Fatalf("output-only configuration = %d, %v, calls=%d", rate, err, output.calls)
	}

	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	input, output = &configurer{}, &configurer{}
	if _, err := service.ConfigureSessionAudioContract(ctx, audiorate.ConfigureRequest{
		Resolution: audiorate.RateResolutionRequest{RequestedInputRate: wavio.Rate24kHz},
		Input:      input,
		Output:     output,
	}); !errors.Is(err, context.Canceled) || input.calls != 0 || output.calls != 0 {
		t.Fatalf("pre-canceled configuration = %v with setter calls %d/%d", err, output.calls, input.calls)
	}
}

type configurer struct {
	calls  int
	cancel context.CancelFunc
}

func (c *configurer) SetSessionAudioInput(audiorate.AudioFormat, int) {
	c.calls++
}

func (c *configurer) SetSessionAudioOutput(audiorate.AudioFormat, int) {
	c.calls++
	if c.cancel != nil {
		c.cancel()
	}
}

func pcm16(samples ...int16) []byte {
	pcm := make([]byte, len(samples)*2)
	for index, sample := range samples {
		binary.LittleEndian.PutUint16(pcm[index*2:], uint16(sample))
	}
	return pcm
}

func cloneInputs(inputs []audiorate.ScheduledAudioInput) []audiorate.ScheduledAudioInput {
	cloned := make([]audiorate.ScheduledAudioInput, len(inputs))
	for index, input := range inputs {
		cloned[index] = input
		cloned[index].PCM = append([]byte(nil), input.PCM...)
	}
	return cloned
}
