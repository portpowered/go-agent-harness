package fileports

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	devicegateway "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
)

// frameAudioSource keeps the explicit legacy replay compatibility path on the
// canonical fixed-frame AudioSource contract. Ordinary file and finite-turn
// callers retain count-aware source tails.
type frameAudioSource struct {
	source audio.AudioSource
}

func (s *frameAudioSource) ReadFrame(ctx context.Context, buf []int16) error {
	if s == nil || s.source == nil {
		return io.EOF
	}
	return s.source.ReadFrame(ctx, buf)
}

func (s *frameAudioSource) Close() error {
	if s == nil || s.source == nil {
		return nil
	}
	return s.source.Close()
}

// interruptibleAudioSource owns a process-local duplicate of stdin. The
// generic AudioSource contract cannot cancel an in-flight io.ReadFull call,
// while the live runtime must close and join a capture worker after the
// provider ends a session. Closing the duplicate first wakes that read without
// closing the caller-owned command descriptor.
type interruptibleAudioSource struct {
	source audio.AudioSource
	input  *os.File

	mu        sync.Mutex
	closed    bool
	closeOnce sync.Once
	closeErr  error
}

func (s *interruptibleAudioSource) ReadFrame(ctx context.Context, buf []int16) error {
	if s == nil || s.source == nil {
		return io.EOF
	}
	err := s.source.ReadFrame(ctx, buf)
	if s.isClosed() && errors.Is(err, os.ErrClosed) {
		return context.Canceled
	}
	return err
}

func (s *interruptibleAudioSource) ReadSamples(ctx context.Context, buf []int16) (int, error) {
	if s == nil || s.source == nil {
		return 0, io.EOF
	}
	countSource, ok := s.source.(audio.SampleSource)
	if !ok {
		return 0, fmt.Errorf("interruptible audio source does not support sample-count reads")
	}
	count, err := countSource.ReadSamples(ctx, buf)
	if s.isClosed() && errors.Is(err, os.ErrClosed) {
		return count, context.Canceled
	}
	return count, err
}

func (s *interruptibleAudioSource) isClosed() bool {
	if s == nil {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *interruptibleAudioSource) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		// Close the duplicate before asking FileSource to close. FileSource
		// serializes its state around the underlying read, so reversing this
		// order would wait forever for a blocked stdin read.
		if s.input != nil {
			s.closeErr = s.input.Close()
		}
		if s.source != nil {
			s.closeErr = errors.Join(s.closeErr, s.source.Close())
		}
	})
	return s.closeErr
}

func openAudioInput(input devices.FileMediaSource, label string) (audio.AudioSource, int, error) {
	path := input.Path
	if strings.EqualFold(filepath.Ext(path), ".wav") && path != stdinPath {
		file, err := os.Open(path)
		if err != nil {
			return nil, 0, fmt.Errorf("%s %q: %w", label, path, err)
		}
		source, err := audio.NewWAVSource(path, file)
		if err != nil {
			return nil, 0, errors.Join(fmt.Errorf("%s %q: %w", label, path, err), file.Close())
		}
		return source, source.SampleRate(), nil
	}
	source, interruptibleInput, err := openFileAudioSource(input, label)
	if err != nil {
		return nil, 0, err
	}
	rate := input.SampleRate
	if rate <= 0 {
		rate = audio.SampleRate
	}
	if interruptibleInput != nil {
		return &interruptibleAudioSource{source: source, input: interruptibleInput}, rate, nil
	}
	return source, rate, nil
}

func openFileAudioSource(input devices.FileMediaSource, label string) (audio.AudioSource, *os.File, error) {
	stdin := input.Stdin
	var interruptibleInput *os.File
	if input.Path == stdinPath && input.CloseStdinOnCancel {
		if file, ok := stdin.(*os.File); ok {
			var err error
			interruptibleInput, err = devicegateway.OpenInterruptibleInput(file)
			if err != nil {
				return nil, nil, fmt.Errorf("%s %q: %w", label, input.Path, err)
			}
			stdin = interruptibleInput
		}
	}
	source, err := audio.NewFileSource(input.Path, stdin)
	if err != nil {
		if interruptibleInput != nil {
			if closeErr := interruptibleInput.Close(); closeErr != nil {
				return nil, nil, errors.Join(fmt.Errorf("%s %q: %w", label, input.Path, err), fmt.Errorf("close interruptible input: %w", closeErr))
			}
		}
		return nil, nil, fmt.Errorf("%s %q: %w", label, input.Path, err)
	}
	return source, interruptibleInput, nil
}
