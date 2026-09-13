// Package service contains the private process-backed implementation of the
// public audiocodec contract.
package service

import (
	"context"
	"errors"
	"fmt"
	"os"

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
	runner runner
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
	return &Service{runner: newProcessRunner("ffmpeg")}
}

func newWithRunner(process runner) *Service {
	return &Service{runner: process}
}

// Convert implements audiocodec.Service.
func (s *Service) Convert(ctx context.Context, request audiocodec.Request) (audiocodec.Result, error) {
	if ctx == nil {
		return audiocodec.Result{}, newError(audiocodec.ErrorInvalidRequest, nil, "nil context")
	}
	if s == nil || s.runner == nil {
		return audiocodec.Result{}, newError(audiocodec.ErrorInvalidRequest, nil, "nil service")
	}
	if err := request.Limits.Validate(); err != nil {
		return audiocodec.Result{}, err
	}
	if len(request.Input) > request.Limits.MaxInputBytes {
		return audiocodec.Result{}, newError(
			audiocodec.ErrorInputTooLarge,
			nil,
			fmt.Sprintf("got %d bytes, want at most %d", len(request.Input), request.Limits.MaxInputBytes),
		)
	}
	format, err := detectFormat(request.Input, request.FormatHint)
	if err != nil {
		return audiocodec.Result{}, err
	}

	conversionContext := ctx
	if request.Limits.MaxDuration > 0 {
		var cancel context.CancelFunc
		conversionContext, cancel = context.WithTimeout(ctx, request.Limits.MaxDuration)
		defer cancel()
	}

	tmp, err := os.CreateTemp("", "audiocodec-input-*")
	if err != nil {
		return audiocodec.Result{}, newError(audiocodec.ErrorInputFile, err, "create temporary input")
	}
	tmpPath := tmp.Name()
	defer removeTempFile(tmpPath)

	if _, err := tmp.Write(request.Input); err != nil {
		_ = tmp.Close()
		return audiocodec.Result{}, newError(audiocodec.ErrorInputFile, err, "write temporary input")
	}
	if err := tmp.Close(); err != nil {
		return audiocodec.Result{}, newError(audiocodec.ErrorInputFile, err, "close temporary input")
	}

	output, err := s.runner.run(conversionContext, tmpPath, request.Limits)
	if err != nil {
		if isTypedCodecError(err) {
			return audiocodec.Result{}, err
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return audiocodec.Result{}, newError(audiocodec.ErrorCanceled, err, "decoder context ended")
		}
		return audiocodec.Result{}, newError(audiocodec.ErrorProcessWait, err, "decoder process")
	}
	if err := codec.ValidatePCM16(output.stdout, request.Limits.MaxOutputBytes); err != nil {
		kind := audiocodec.ErrorInvalidPCM16
		if errors.Is(err, codec.ErrPayloadTooLarge) {
			kind = audiocodec.ErrorOutputTooLarge
		}
		return audiocodec.Result{}, newError(kind, err, "validate decoder output")
	}

	return audiocodec.Result{
		PCM16:       append([]byte(nil), output.stdout...),
		InputFormat: format,
		SampleRate:  audiocodec.PCM16SampleRate,
		Channels:    audiocodec.PCM16Channels,
		Encoding:    audiocodec.PCM16Encoding,
	}, nil
}

func isTypedCodecError(err error) bool {
	var typed *audiocodec.Error
	return errors.As(err, &typed)
}

func removeTempFile(path string) {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		// Conversion already has a primary result. The temporary file is private
		// cleanup state, so there is no second error channel to expose here.
		return
	}
}
