package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roommedia"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
)

func (s *Service) PumpHumanOutput(ctx context.Context, request roommedia.HumanOutputRequest) error {
	ctx = normalizedContext(ctx)
	if request.Mixer == nil || request.Output == nil {
		return roommedia.ErrUnavailable
	}
	format := request.Mixer.Format()
	buffer, err := s.NewOutputBuffer(roommedia.OutputBufferRequest{Output: request.Output, Format: format})
	if err != nil {
		return err
	}
	holdTone := audio.NewHoldToneFiller(audio.DefaultHoldToneConfig(), format.SampleRate, s.clock.Now())
	readContext := request.ReadContext //nolint:contextcheck // an explicit host read context overrides the pump fallback.
	if readContext == nil {
		readContext = ctx
	}
	for {
		mixed, err := readOutputFrame(readContext, request.Mixer)
		if err != nil {
			return err
		}
		frame := audio.ApplyHoldTonePCM16(holdTone, s.clock.Now(), append([]byte(nil), mixed.PCM...))
		if err := writeOutputFrame(ctx, buffer, request, mixed, frame); err != nil {
			return err
		}
	}
}

func readOutputFrame(ctx context.Context, mixer roommedia.Mixer) (roommedia.MixedFrame, error) {
	frame, err := mixer.ReadFrameWithSources(ctx)
	if err == nil || errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return frame, err
	}
	return roommedia.MixedFrame{}, fmt.Errorf("read human output mixer: %w", err)
}

func writeOutputFrame(ctx context.Context, buffer roommedia.OutputBuffer, request roommedia.HumanOutputRequest, mixed roommedia.MixedFrame, frame []byte) error {
	if err := buffer.WriteFrame(ctx, frame); err != nil {
		if request.Resolve != nil {
			request.Resolve(append([]string(nil), mixed.Sources...), len(frame), roommedia.ParticipantOutputRejectedReason)
		}
		return fmt.Errorf("write human output device: %w", err)
	}
	if request.Resolve != nil {
		request.Resolve(append([]string(nil), mixed.Sources...), len(frame), "")
	}
	if request.ObserveReceived != nil {
		request.ObserveReceived(append([]byte(nil), frame...))
	}
	return nil
}

func (s *Service) NewOutputBuffer(request roommedia.OutputBufferRequest) (roommedia.OutputBuffer, error) {
	if request.Output == nil {
		return nil, roommedia.ErrUnavailable
	}
	if err := request.Format.Validate(); err != nil {
		return nil, err
	}
	targetRate := request.TargetSampleRate
	if targetRate == 0 {
		targetRate = audio.SampleRate
	}
	frameSamples := request.FrameSamples
	if frameSamples == 0 {
		frameSamples = audio.FrameSize
	}
	if targetRate <= 0 || frameSamples <= 0 {
		return nil, fmt.Errorf("%w: target_rate=%d frame_samples=%d", roommedia.ErrInvalidRequest, targetRate, frameSamples)
	}
	return &outputBuffer{output: request.Output, format: request.Format, targetRate: targetRate, frameSamples: frameSamples}, nil
}

type outputBuffer struct {
	mu           sync.Mutex
	output       roommedia.Output
	format       roommedia.PCM16Format
	targetRate   int
	frameSamples int
	pending      []int16
}

func (b *outputBuffer) WriteFrame(ctx context.Context, pcm []byte) error {
	ctx = normalizedContext(ctx)
	if len(pcm)%2 != 0 {
		return fmt.Errorf("%w: got %d bytes", roommedia.ErrPCM16Truncated, len(pcm))
	}
	samples, err := codec.DecodePCM16WithLimit(pcm, len(pcm))
	if err != nil {
		return fmt.Errorf("decode human output mixer PCM16: %w", err)
	}
	converted, err := audio.ResamplePCM16(samples, b.format.SampleRate, b.targetRate)
	if err != nil {
		return fmt.Errorf("resample mixer audio from %d Hz to %d Hz: %w", b.format.SampleRate, b.targetRate, err)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.pending = append(b.pending, converted...)
	for len(b.pending) >= b.frameSamples {
		frame := append([]int16(nil), b.pending[:b.frameSamples]...)
		if err := b.output.WriteFrame(ctx, frame); err != nil {
			return err
		}
		b.pending = b.pending[b.frameSamples:]
	}
	return nil
}

func (b *outputBuffer) PendingSamples() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.pending)
}
