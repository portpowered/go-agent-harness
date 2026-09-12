package service

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roommedia"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
)

type captureConfig struct {
	format       roommedia.PCM16Format
	inputRate    int
	frameSamples int
}

func (s *Service) CaptureHuman(ctx context.Context, request roommedia.HumanCaptureRequest) error {
	ctx = normalizedContext(ctx)
	config, err := validateCapture(request)
	if err != nil {
		return err
	}
	frame := make([]int16, config.frameSamples)
	for {
		captured, pcm, err := readCaptureFrame(ctx, request.Input, frame, config)
		if err != nil {
			return err
		}
		if request.ObserveSent != nil {
			request.ObserveSent(append([]byte(nil), pcm...))
		}
		if err := fanOutCapture(ctx, request, config, captured, pcm); err != nil {
			return err
		}
	}
}

func validateCapture(request roommedia.HumanCaptureRequest) (captureConfig, error) {
	if request.Input == nil || request.Mixer == nil {
		return captureConfig{}, roommedia.ErrUnavailable
	}
	format := request.Mixer.Format()
	if err := format.Validate(); err != nil {
		return captureConfig{}, err
	}
	inputRate := request.InputSampleRate
	if inputRate == 0 {
		inputRate = audio.SampleRate
	}
	frameSamples := request.FrameSamples
	if frameSamples == 0 {
		frameSamples = audio.FrameSize
	}
	if inputRate <= 0 || frameSamples <= 0 {
		return captureConfig{}, fmt.Errorf("%w: capture rate=%d frame_samples=%d", roommedia.ErrInvalidRequest, inputRate, frameSamples)
	}
	return captureConfig{format: format, inputRate: inputRate, frameSamples: frameSamples}, nil
}

func readCaptureFrame(ctx context.Context, input roommedia.Input, frame []int16, config captureConfig) ([]int16, []byte, error) {
	if err := input.ReadFrame(ctx, frame); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, nil, err
		}
		return nil, nil, fmt.Errorf("read human input device: %w", err)
	}
	captured := append([]int16(nil), frame...)
	converted, err := audio.ResamplePCM16(captured, config.inputRate, config.format.SampleRate)
	if err != nil {
		return nil, nil, fmt.Errorf("convert human input audio: %w", err)
	}
	return captured, codec.EncodePCM16(converted), nil
}

func fanOutCapture(ctx context.Context, request roommedia.HumanCaptureRequest, config captureConfig, captured []int16, pcm []byte) error {
	var targets []roommedia.FanoutTarget
	if request.Targets != nil {
		targets = request.Targets()
	}
	for _, target := range targets {
		if err := fanOutTarget(ctx, request, config, captured, pcm, target); err != nil {
			return err
		}
	}
	return nil
}

func fanOutTarget(ctx context.Context, request roommedia.HumanCaptureRequest, config captureConfig, captured []int16, pcm []byte, target roommedia.FanoutTarget) error {
	if target.ID == "" || target.Write == nil || target.ID == request.ParticipantID || !targetIsActive(target) {
		return nil
	}
	targetFormat := target.Format
	if targetFormat == (roommedia.PCM16Format{}) {
		targetFormat = config.format
	}
	if err := targetFormat.Validate(); err != nil {
		return fmt.Errorf("convert human input audio for %s: %w", target.ID, err)
	}
	targetPCM, err := convertTargetPCM(captured, pcm, config, targetFormat)
	if err != nil {
		return fmt.Errorf("convert human input audio for %s: %w", target.ID, err)
	}
	if err := target.Write(ctx, request.ParticipantID, append([]byte(nil), targetPCM...)); err != nil {
		if !targetIsActive(target) {
			return nil
		}
		return fmt.Errorf("receive fan out human PCM from %s: %w", request.ParticipantID, err)
	}
	if request.ObserveFanout != nil {
		request.ObserveFanout(request.ParticipantID, target.ID, append([]byte(nil), targetPCM...))
	}
	return nil
}

func targetIsActive(target roommedia.FanoutTarget) bool {
	return target.Active == nil || target.Active()
}

func convertTargetPCM(captured []int16, pcm []byte, config captureConfig, targetFormat roommedia.PCM16Format) ([]byte, error) {
	if targetFormat == config.format {
		return append([]byte(nil), pcm...), nil
	}
	targetSamples, err := audio.ResamplePCM16(captured, config.inputRate, targetFormat.SampleRate)
	if err != nil {
		return nil, err
	}
	return codec.EncodePCM16(targetSamples), nil
}
