package livehost

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

// negotiatedFileSink delays WAV construction until a physical playback tap
// reports the device's negotiated rate. File paths remain host-owned, while
// the runtime device service supplies the actual post-conversion sample rate.
type negotiatedFileSink struct {
	path         string
	stdout       io.Writer
	fallbackRate int

	mu         sync.Mutex
	sink       audio.AudioSink
	sampleRate int
	rateSet    bool
	closed     bool
	closeErr   error
}

func newNegotiatedFileSink(path string, stdout io.Writer, fallbackRate int) (audio.AudioSink, error) {
	if fallbackRate <= 0 {
		fallbackRate = audio.SampleRate
	}
	if path == "-" {
		return audio.NewFileSinkAtSampleRate(path, stdout, fallbackRate)
	}
	probe, err := audio.NewFileSinkAtSampleRate(path, stdout, fallbackRate)
	if err != nil {
		return nil, err
	}
	if err := probe.Close(); err != nil && !errors.Is(err, wavio.ErrEmptySamples) {
		return nil, err
	}
	return &negotiatedFileSink{path: path, stdout: stdout, fallbackRate: fallbackRate}, nil
}

func (s *negotiatedFileSink) WriteFrame(ctx context.Context, samples []int16) error {
	return s.write(ctx, s.fallbackRate, samples)
}

func (s *negotiatedFileSink) WriteSamples(ctx context.Context, samples []int16) error {
	return s.write(ctx, s.fallbackRate, samples)
}

func (s *negotiatedFileSink) WriteSamplesAtRate(ctx context.Context, rate int, samples []int16) error {
	return s.write(ctx, rate, samples)
}

func (s *negotiatedFileSink) write(ctx context.Context, rate int, samples []int16) error {
	if s == nil {
		return errors.New("negotiated file sink is unavailable")
	}
	if ctx == nil {
		return errors.New("negotiated file sink context is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		if s.closeErr != nil {
			return errors.Join(audio.ErrClosed, s.closeErr)
		}
		return audio.ErrClosed
	}
	if rate <= 0 {
		rate = s.fallbackRate
	}
	if s.rateSet && rate != s.sampleRate {
		return fmt.Errorf("negotiated output rate changed from %d Hz to %d Hz", s.sampleRate, rate)
	}
	if s.sink == nil {
		opened, err := audio.NewFileSinkAtSampleRate(s.path, s.stdout, rate)
		if err != nil {
			return fmt.Errorf("open negotiated output at %d Hz: %w", rate, err)
		}
		s.sink = opened
		s.sampleRate = rate
		s.rateSet = true
	}
	if writer, ok := s.sink.(audio.SampleSink); ok {
		return writer.WriteSamples(ctx, samples)
	}
	return s.sink.WriteFrame(ctx, samples)
}

func (s *negotiatedFileSink) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return s.closeErr
	}
	s.closed = true
	if s.sink == nil {
		opened, err := audio.NewFileSinkAtSampleRate(s.path, s.stdout, s.fallbackRate)
		if err != nil {
			s.closeErr = err
			return err
		}
		s.sink = opened
		s.sampleRate = s.fallbackRate
		s.rateSet = true
	}
	s.closeErr = s.sink.Close()
	if errors.Is(s.closeErr, wavio.ErrEmptySamples) && strings.EqualFold(filepath.Ext(s.path), ".wav") {
		if err := os.Remove(s.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			s.closeErr = errors.Join(s.closeErr, err)
		} else {
			s.closeErr = nil
		}
	}
	return s.closeErr
}

var _ audio.AudioSink = (*negotiatedFileSink)(nil)
var _ audio.SampleSink = (*negotiatedFileSink)(nil)
