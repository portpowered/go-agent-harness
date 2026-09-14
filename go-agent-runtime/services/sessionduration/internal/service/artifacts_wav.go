package service

import (
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

type sessionDurationWAVSink struct {
	mu       sync.Mutex
	path     string
	file     *os.File
	writer   *wavio.StreamWriter
	closed   bool
	closeErr error
}

func newSessionDurationWAVSink(path string) (*sessionDurationWAVSink, error) {
	if path == "" {
		return nil, errors.New("duration audio path is empty")
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, sessionDurationArtifactFileMode)
	if err != nil {
		return nil, fmt.Errorf("open duration audio %q: %w", path, err)
	}
	writer, err := wavio.NewStreamWriter(file, wavio.Rate16kHz)
	if err != nil {
		return nil, errors.Join(err, file.Close())
	}
	return &sessionDurationWAVSink{path: path, file: file, writer: writer}, nil
}

func (s *sessionDurationWAVSink) WriteSamples(samples []int16) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("duration audio sink is closed")
	}
	if err := s.writer.WriteSamples(samples); err != nil {
		return err
	}
	return s.writer.Checkpoint()
}

func (s *sessionDurationWAVSink) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return s.closeErr
	}
	if s.file == nil {
		return nil
	}
	return s.file.Sync()
}

func (s *sessionDurationWAVSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return s.closeErr
	}
	s.closed = true
	var closeErrs []error
	if s.file != nil {
		if err := s.writer.Close(); err != nil {
			closeErrs = append(closeErrs, fmt.Errorf("write duration audio %q: %w", s.path, err))
		} else if err := s.file.Sync(); err != nil {
			closeErrs = append(closeErrs, fmt.Errorf("flush duration audio %q: %w", s.path, err))
		}
		if err := s.file.Close(); err != nil {
			closeErrs = append(closeErrs, fmt.Errorf("close duration audio %q: %w", s.path, err))
		}
	}
	s.closeErr = errors.Join(closeErrs...)
	return s.closeErr
}
