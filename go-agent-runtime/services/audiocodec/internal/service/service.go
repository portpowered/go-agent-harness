// Package service contains the private process-backed implementation of the
// public audiocodec contract.
package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audiocodec"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
)

type runner interface {
	run(context.Context, string, audiocodec.Limits) (runResult, error)
}

type runResult struct {
	stdout []byte
}

// Service is intentionally stateless apart from its process runner. Each
// Convert call owns its temporary input and result buffers.
type Service struct {
	runner     runner
	removeTemp func(string) error
}

func newError(kind audiocodec.ErrorKind, cause error, detail string) error {
	return &audiocodec.Error{Kind: kind, Cause: cause, Detail: detail}
}

func defaultLimits() audiocodec.Limits {
	return audiocodec.Limits{
		MaxInputBytes:  audiocodec.DefaultMaxInputBytes,
		MaxOutputBytes: audiocodec.DefaultMaxOutputBytes,
		MaxStderrBytes: audiocodec.DefaultMaxStderrBytes,
		MaxDuration:    audiocodec.DefaultMaxDuration,
	}
}

// New constructs the default ffmpeg-backed implementation. It performs no
// process lookup or filesystem access until Convert is called.
func New() *Service {
	return &Service{runner: newProcessRunner("ffmpeg"), removeTemp: removeTempFile}
}

func newWithRunner(process runner) *Service {
	return &Service{runner: process, removeTemp: removeTempFile}
}

// Convert implements audiocodec.Service.
func (s *Service) Convert(ctx context.Context, request audiocodec.Request) (audiocodec.Result, error) {
	format, err := s.validateRequest(ctx, request)
	if err != nil {
		return audiocodec.Result{}, err
	}

	conversionContext, cancel := contextForConversion(ctx, request.Limits.MaxDuration)
	defer cancel()
	output, err := s.decode(conversionContext, request.Input, request.Limits)
	if err != nil {
		return audiocodec.Result{}, normalizeDecodeError(err)
	}
	if err := codec.ValidatePCM16(output.stdout, request.Limits.MaxOutputBytes); err != nil {
		return audiocodec.Result{}, invalidPCM16Error(err)
	}

	return audiocodec.Result{
		PCM16:       append([]byte(nil), output.stdout...),
		InputFormat: format,
		SampleRate:  audiocodec.PCM16SampleRate,
		Channels:    audiocodec.PCM16Channels,
		Encoding:    audiocodec.PCM16Encoding,
	}, nil
}

func (s *Service) validateRequest(ctx context.Context, request audiocodec.Request) (audiocodec.InputFormat, error) {
	if ctx == nil {
		return "", newError(audiocodec.ErrorInvalidRequest, nil, "nil context")
	}
	if s == nil || s.runner == nil {
		return "", newError(audiocodec.ErrorInvalidRequest, nil, "nil service")
	}
	if err := request.Limits.Validate(); err != nil {
		return "", err
	}
	if len(request.Input) > request.Limits.MaxInputBytes {
		return "", newError(
			audiocodec.ErrorInputTooLarge,
			nil,
			fmt.Sprintf("got %d bytes, want at most %d", len(request.Input), request.Limits.MaxInputBytes),
		)
	}
	return detectFormat(request.Input, request.FormatHint)
}

func contextForConversion(ctx context.Context, duration time.Duration) (context.Context, context.CancelFunc) {
	if duration <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, duration)
}

func (s *Service) decode(ctx context.Context, input []byte, limits audiocodec.Limits) (result runResult, returnErr error) {
	tmp, err := os.CreateTemp("", "audiocodec-input-*")
	if err != nil {
		return runResult{}, newError(audiocodec.ErrorInputFile, err, "create temporary input")
	}
	tmpPath := tmp.Name()
	removeTemp := s.removeTemp
	if removeTemp == nil {
		removeTemp = removeTempFile
	}
	defer func() {
		if err := removeTemp(tmpPath); err != nil {
			returnErr = errors.Join(returnErr, newError(audiocodec.ErrorInputFile, err, "remove temporary input"))
			result = runResult{}
		}
	}()

	if _, err := tmp.Write(input); err != nil {
		if closeErr := tmp.Close(); closeErr != nil {
			return runResult{}, newError(
				audiocodec.ErrorInputFile,
				fmt.Errorf("%w (close temporary input: %w)", err, closeErr),
				"write temporary input",
			)
		}
		return runResult{}, newError(audiocodec.ErrorInputFile, err, "write temporary input")
	}
	if err := tmp.Close(); err != nil {
		return runResult{}, newError(audiocodec.ErrorInputFile, err, "close temporary input")
	}
	return s.runner.run(ctx, tmpPath, limits)
}

func normalizeDecodeError(err error) error {
	if isTypedCodecError(err) {
		return err
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return newError(audiocodec.ErrorCanceled, err, "decoder context ended")
	}
	return newError(audiocodec.ErrorProcessWait, err, "decoder process")
}

func invalidPCM16Error(err error) error {
	kind := audiocodec.ErrorInvalidPCM16
	if errors.Is(err, codec.ErrPayloadTooLarge) {
		kind = audiocodec.ErrorOutputTooLarge
	}
	return newError(kind, err, "validate decoder output")
}

func isTypedCodecError(err error) bool {
	var typed *audiocodec.Error
	return errors.As(err, &typed)
}

func removeTempFile(path string) error {
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
