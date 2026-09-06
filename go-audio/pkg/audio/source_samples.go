package audio

import (
	"errors"
	"fmt"
	"io"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
)

func (s *FileSource) readStateError() error {
	if s.closed {
		return &ClosedError{Operation: "read", Path: s.path}
	}
	if s.termErr != nil {
		return s.termErr
	}
	if s.done {
		return io.EOF
	}
	return nil
}

func (s *FileSource) readWAVSamples(buf []int16) (int, error) {
	if s.position >= len(s.samples) {
		s.done = true
		return 0, io.EOF
	}
	n := copy(buf, s.samples[s.position:])
	s.position += n
	if s.position >= len(s.samples) {
		s.done = true
	}
	return n, nil
}

func (s *FileSource) readRawSamples(buf []int16) (int, error) {
	encoded := make([]byte, len(buf)*2)
	count, err := io.ReadFull(s.reader, encoded)
	if markerErr := rawEndOfTurnError(s, count, err); markerErr != nil {
		return 0, markerErr
	}
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return 0, newStreamError("read", s.path, s.format, err)
	}
	if count == 0 {
		s.done = true
		return 0, io.EOF
	}
	if count%2 != 0 {
		s.termErr = &TruncatedPCMError{Path: s.path, Bytes: count % 2}
		return 0, s.termErr
	}
	if decodeErr := codec.DecodePCM16Into(buf[:count/2], encoded[:count]); decodeErr != nil {
		return 0, newStreamError("read", s.path, s.format, decodeErr)
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		s.done = true
	}
	return count / 2, nil
}

func rawEndOfTurnError(s *FileSource, count int, err error) error {
	if !errors.Is(err, ErrEndOfTurn) {
		return nil
	}
	if count != 0 {
		return newStreamError("read", s.path, s.format, fmt.Errorf("end-of-turn marker after %d PCM bytes", count))
	}
	return ErrEndOfTurn
}
