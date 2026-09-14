package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audiorate"
	audioratewire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/audiorate/wire"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

func main() {
	if err := run(); err != nil {
		panic(err)
	}
}

func run() error {
	first := audioratewire.NewService()
	second := audioratewire.NewService()
	if first == nil || second == nil {
		return errors.New("wire returned a nil service")
	}
	pcm := codec.EncodePCM16([]int16{-3, 0, 3})
	original := append([]byte(nil), pcm...)
	converted, err := first.ConvertPCM(context.Background(), pcm, wavio.Rate16kHz, wavio.Rate24kHz)
	if err != nil {
		return fmt.Errorf("convert PCM: %w", err)
	}
	if len(converted)/2 != 5 || !bytes.Equal(pcm, original) {
		return fmt.Errorf("conversion result changed count or input: samples=%d inputUnchanged=%t", len(converted)/2, bytes.Equal(pcm, original))
	}
	secondConverted, err := second.ConvertPCM(context.Background(), pcm, wavio.Rate16kHz, wavio.Rate24kHz)
	if err != nil || !bytes.Equal(converted, secondConverted) {
		return fmt.Errorf("independent conversion mismatch: err=%v", err)
	}

	scheduled, err := first.ConvertScheduledAudioInputs(context.Background(), []audiorate.ScheduledAudioInput{
		{AfterCompletedTurns: 2, PCM: pcm, SourceSampleRate: wavio.Rate16kHz, EndOfTurn: true},
		{AfterCompletedTurns: 0, PCM: pcm, SourceSampleRate: wavio.Rate24kHz},
	}, wavio.Rate24kHz)
	if err != nil || len(scheduled) != 2 || scheduled[0].AfterCompletedTurns != 2 || !scheduled[0].EndOfTurn || scheduled[1].AfterCompletedTurns != 0 {
		return fmt.Errorf("scheduled conversion changed ordering or metadata: %v %+v", err, scheduled)
	}

	defaultRate, err := first.ResolveSampleRate(context.Background(), audiorate.RateResolutionRequest{Provider: audiorate.ProviderOpenAI})
	if err != nil || defaultRate != audiorate.RealtimeSampleRate {
		return fmt.Errorf("live default rate=%d err=%v", defaultRate, err)
	}
	spy := &configSpy{}
	configured, err := first.ConfigureSessionAudioContract(context.Background(), audiorate.ConfigureRequest{
		Resolution: audiorate.RateResolutionRequest{RequestedInputRate: wavio.Rate48kHz},
		Input:      spy,
		Output:     spy,
	})
	if err != nil || configured != wavio.Rate48kHz || spy.inputCalls != 1 || spy.outputCalls != 1 || spy.inputRate != wavio.Rate48kHz || spy.outputRate != wavio.Rate48kHz || spy.inputFormat != audiorate.AudioFormatPCM16 || spy.outputFormat != audiorate.AudioFormatPCM16 {
		return fmt.Errorf("configuration rate=%d err=%v spy=%+v", configured, err, spy)
	}

	spy = &configSpy{}
	_, err = first.ConfigureSessionAudioContract(context.Background(), audiorate.ConfigureRequest{
		Resolution: audiorate.RateResolutionRequest{RequestedInputRate: wavio.Rate16kHz, RequestedOutputRate: wavio.Rate24kHz},
		Input:      spy,
		Output:     spy,
	})
	if !errors.Is(err, audiorate.ErrSampleRateConflict) || spy.inputCalls != 0 || spy.outputCalls != 0 {
		return fmt.Errorf("conflict error=%v setter calls=%d/%d", err, spy.inputCalls, spy.outputCalls)
	}
	if _, err = first.ConvertPCM(context.Background(), []byte{1}, 0, wavio.Rate24kHz); !errors.Is(err, audiorate.ErrPCM16Truncated) {
		return fmt.Errorf("odd-tail error=%v", err)
	}
	if _, err = first.ConvertPCM(context.Background(), pcm, 22050, wavio.Rate24kHz); !errors.Is(err, audiorate.ErrUnsupportedSampleRate) || !errors.Is(err, wavio.ErrUnsupportedResampleRate) {
		return fmt.Errorf("unsupported-rate error=%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := first.ConvertPCM(ctx, pcm, wavio.Rate16kHz, wavio.Rate24kHz); got != nil || !errors.Is(err, context.Canceled) {
		return fmt.Errorf("cancellation got=%v err=%v", got, err)
	}

	fmt.Printf("external-consumer-ok samples=%d default=%d configured=%d\n", len(converted)/2, defaultRate, configured)
	return nil
}

type configSpy struct {
	inputFormat  audiorate.AudioFormat
	inputRate    int
	inputCalls   int
	outputFormat audiorate.AudioFormat
	outputRate   int
	outputCalls  int
}

func (s *configSpy) SetSessionAudioInput(format audiorate.AudioFormat, rate int) {
	s.inputFormat, s.inputRate, s.inputCalls = format, rate, s.inputCalls+1
}

func (s *configSpy) SetSessionAudioOutput(format audiorate.AudioFormat, rate int) {
	s.outputFormat, s.outputRate, s.outputCalls = format, rate, s.outputCalls+1
}
