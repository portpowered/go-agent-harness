package stream

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

func (s *Service) ValidateSpec(input InputSpec, conflict error) error {
	if input.DevicePresent {
		if conflict == nil {
			conflict = ErrConflict
		}
		return &Error{Kind: KindConflict, Path: input.Path, Err: fmt.Errorf("audio device input cannot be combined with --audio-in: %w", conflict)}
	}
	if strings.TrimSpace(input.Path) == "" {
		return &Error{Kind: KindEmpty, Path: input.Path, Err: fmt.Errorf("path is empty: %w", ErrEmpty)}
	}
	return nil
}

func (s *Service) AdaptError(err error, path string, interruptibleKind ErrorKind, conflict error) error {
	if err == nil {
		return nil
	}
	var typed *Error
	if !errors.As(err, &typed) {
		return err
	}
	if path == "" {
		path = typed.Path
	}
	if typed.Kind != KindUninterruptible && typed.Kind != KindConflict && path == typed.Path {
		return err
	}
	clone := *typed
	clone.Path = path
	if clone.Kind == KindUninterruptible {
		clone.Kind, clone.Err = interruptibleKind, errors.Join(clone.Err, ErrUninterruptible)
	}
	if clone.Kind == KindConflict && conflict != nil {
		clone.Err = errors.Join(clone.Err, conflict)
	}
	return &clone
}

func (s *Service) ClassifyOpenError(path string, err error) error {
	kind := KindUnreadable
	switch {
	case errors.Is(err, audio.ErrUnsupportedFormat) || errors.Is(err, ErrFormat) ||
		errors.Is(err, wavio.ErrMalformed) || errors.Is(err, wavio.ErrTruncated) ||
		errors.Is(err, wavio.ErrUnsupported) || errors.Is(err, wavio.ErrEmpty):
		kind = KindFormat
		if !errors.Is(err, audio.ErrUnsupportedFormat) {
			err = errors.Join(audio.ErrUnsupportedFormat, err)
		}
	case errors.Is(err, fs.ErrNotExist):
		kind = KindMissing
	case errors.Is(err, audio.ErrNilStream):
		kind = KindUnreadable
	}
	return &Error{Kind: kind, Path: path, Err: err}
}

func (s *Service) PreferRate(declared, fallback int) int {
	if declared > 0 {
		return declared
	}
	return fallback
}

func (s *Service) NegotiateRate(inputRate, outputRate, fallback int, conflict error) (int, error) {
	if inputRate > 0 && outputRate > 0 && inputRate != outputRate {
		if conflict == nil {
			conflict = errors.New("audio input and output sample rates conflict")
		}
		return 0, fmt.Errorf("%w: input=%d Hz output=%d Hz", conflict, inputRate, outputRate)
	}
	if inputRate > 0 {
		return inputRate, nil
	}
	if outputRate > 0 {
		return outputRate, nil
	}
	if fallback > 0 {
		return fallback, nil
	}
	return audio.SampleRate, nil
}
