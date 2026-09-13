package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audiocodec"
	audiocodecwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/audiocodec/wire"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

func main() {
	if err := run(); err != nil {
		panic(err)
	}
}

func run() error {
	var encoded bytes.Buffer
	if err := wavio.Write(&encoded, audiocodec.PCM16SampleRate, []int16{-3, 0, 7}); err != nil {
		return fmt.Errorf("build WAV: %w", err)
	}
	input := append([]byte(nil), encoded.Bytes()...)
	service := audiocodecwire.NewService()
	second := audiocodecwire.NewService()
	if service == nil || second == nil || service == second {
		return errors.New("Wire did not construct independent services")
	}
	result, err := service.Convert(context.Background(), audiocodec.Request{Input: input, Limits: defaultLimits()})
	if err != nil {
		return fmt.Errorf("convert WAV: %w", err)
	}
	if !bytes.Equal(input, encoded.Bytes()) {
		return errors.New("conversion mutated the input")
	}
	if result.InputFormat != audiocodec.FormatWAV || result.SampleRate != audiocodec.PCM16SampleRate || result.Channels != audiocodec.PCM16Channels || result.Encoding != audiocodec.PCM16Encoding {
		return fmt.Errorf("unexpected result metadata: %#v", result)
	}
	if err := codec.ValidatePCM16(result.PCM16, audiocodec.DefaultMaxOutputBytes); err != nil {
		return fmt.Errorf("validate PCM16: %w", err)
	}
	limited := defaultLimits()
	limited.MaxOutputBytes = 2
	if _, err := service.Convert(context.Background(), audiocodec.Request{Input: input, Limits: limited}); !errors.Is(err, audiocodec.ErrOutputTooLarge) {
		return fmt.Errorf("output bound error = %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.Convert(canceled, audiocodec.Request{Input: input, Limits: defaultLimits()}); !errors.Is(err, audiocodec.ErrCanceled) || !errors.Is(err, context.Canceled) {
		return fmt.Errorf("cancellation error = %v", err)
	}
	return nil
}

func defaultLimits() audiocodec.Limits {
	return audiocodec.Limits{
		MaxInputBytes:  audiocodec.DefaultMaxInputBytes,
		MaxOutputBytes: audiocodec.DefaultMaxOutputBytes,
		MaxStderrBytes: audiocodec.DefaultMaxStderrBytes,
		MaxDuration:    audiocodec.DefaultMaxDuration,
	}
}
