package probe

import (
	"context"
	"sync"
	"time"
)

type duplexProgressState struct {
	mu            sync.Mutex
	changed       chan struct{}
	startedAt     time.Time
	inputBytes    int64
	inputFrames   int
	outputBytes   int64
	outputReads   int
	inputSegments int
	output        []DuplexOutputEvent
	outputClosed  bool
}

func newDuplexProgressState() *duplexProgressState {
	return &duplexProgressState{changed: make(chan struct{})}
}

func (s *duplexProgressState) snapshot() DuplexProgressSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return DuplexProgressSnapshot{
		At:            s.elapsedLocked(),
		InputBytes:    s.inputBytes,
		InputFrames:   s.inputFrames,
		OutputBytes:   s.outputBytes,
		OutputReads:   s.outputReads,
		InputSegments: s.inputSegments,
		OutputClosed:  s.outputClosed,
	}
}

func (s *duplexProgressState) setStartedAt(startedAt time.Time) {
	s.mu.Lock()
	s.startedAt = startedAt
	s.mu.Unlock()
}

func (s *duplexProgressState) elapsed() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.elapsedLocked()
}

func (s *duplexProgressState) elapsedLocked() time.Duration {
	if s.startedAt.IsZero() {
		return 0
	}
	return time.Since(s.startedAt)
}

func (s *duplexProgressState) noteInputSegment() {
	s.mu.Lock()
	s.inputSegments++
	close(s.changed)
	s.changed = make(chan struct{})
	s.mu.Unlock()
}

func (s *duplexProgressState) noteInput(data []byte) {
	s.mu.Lock()
	s.inputBytes += int64(len(data))
	s.inputFrames++
	close(s.changed)
	s.changed = make(chan struct{})
	s.mu.Unlock()
}

func (s *duplexProgressState) noteOutput(event DuplexOutputEvent) {
	s.mu.Lock()
	s.outputBytes += int64(event.Bytes)
	s.outputReads++
	event.Total = s.outputBytes
	event.Read = s.outputReads
	s.output = append(s.output, event)
	close(s.changed)
	s.changed = make(chan struct{})
	s.mu.Unlock()
}

func (s *duplexProgressState) waitForOutput(ctx context.Context, minimum int64, reads bool) error {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		s.mu.Lock()
		met := s.outputBytes >= minimum
		if reads {
			met = int64(s.outputReads) >= minimum
		}
		if met {
			s.mu.Unlock()
			return nil
		}
		changed := s.changed
		s.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (s *duplexProgressState) outputEvents() []DuplexOutputEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]DuplexOutputEvent(nil), s.output...)
}

func (s *duplexProgressState) waitForChange(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	if s.outputClosed {
		s.mu.Unlock()
		return nil
	}
	changed := s.changed
	s.mu.Unlock()
	select {
	case <-changed:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *duplexProgressState) noteOutputClosed() {
	s.mu.Lock()
	if !s.outputClosed {
		s.outputClosed = true
		close(s.changed)
		s.changed = make(chan struct{})
	}
	s.mu.Unlock()
}

func (s *duplexProgressState) outputIsClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.outputClosed
}
