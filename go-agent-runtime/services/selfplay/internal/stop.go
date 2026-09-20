package internal

import (
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/selfplay"
)

type terminalSnapshot struct {
	reason selfplay.StopReason
	turns  [2]int
	err    error
}

type stopState struct {
	mu       sync.Mutex
	done     chan struct{}
	terminal *terminalSnapshot
	turns    [2]int
	seen     [2]map[string]struct{}
	target   int
	onStop   func()
}

func newStopState(target int, onStop func()) *stopState {
	return &stopState{done: make(chan struct{}), target: target, onStop: onStop,
		seen: [2]map[string]struct{}{make(map[string]struct{}), make(map[string]struct{})}}
}

func (s *stopState) commit(reason selfplay.StopReason, err error) bool {
	if reason == "" {
		reason = selfplay.StopFailure
	}
	s.mu.Lock()
	accepted := s.commitLocked(reason, err)
	s.mu.Unlock()
	if accepted && s.onStop != nil {
		s.onStop()
	}
	return accepted
}

func (s *stopState) commitLocked(reason selfplay.StopReason, err error) bool {
	if s.terminal != nil {
		return false
	}
	s.terminal = &terminalSnapshot{reason: reason, turns: s.turns, err: err}
	close(s.done)
	return true
}

func (s *stopState) fail(err error) bool {
	if err == nil {
		err = errors.New("self-play side failed")
	}
	return s.commit(selfplay.StopFailure, err)
}

func (s *stopState) recordTurn(side int, responseID string) (int, bool) {
	s.mu.Lock()
	if s.terminal != nil || side < 0 || side >= len(s.turns) || s.turns[side] >= s.target {
		s.mu.Unlock()
		return 0, false
	}
	if responseID != "" {
		if _, exists := s.seen[side][responseID]; exists {
			s.mu.Unlock()
			return s.turns[side], false
		}
		s.seen[side][responseID] = struct{}{}
	}
	s.turns[side]++
	turns := s.turns[side]
	s.mu.Unlock()
	return turns, true
}

func (s *stopState) commitTurnTarget() bool {
	s.mu.Lock()
	accepted := s.terminal == nil && s.turns[0] == s.target && s.turns[1] == s.target && s.commitLocked(selfplay.StopTurnTarget, nil)
	s.mu.Unlock()
	if accepted && s.onStop != nil {
		s.onStop()
	}
	return accepted
}

func (s *stopState) stopped() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.terminal != nil
}

func (s *stopState) snapshot() terminalSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.terminal == nil {
		return terminalSnapshot{reason: selfplay.StopFailure, turns: s.turns, err: errors.New("self-play ended without a terminal result")}
	}
	return *s.terminal
}

type pcmBridge struct {
	reader *io.PipeReader
	writer *io.PipeWriter
	once   sync.Once
}

func newPCMBridge() *pcmBridge {
	reader, writer := io.Pipe()
	return &pcmBridge{reader: reader, writer: writer}
}

func (b *pcmBridge) write(pcm []byte) error {
	if len(pcm) == 0 {
		return nil
	}
	written, err := b.writer.Write(pcm)
	if err != nil {
		return err
	}
	if written != len(pcm) {
		return fmt.Errorf("%w: bridged %d of %d PCM bytes", io.ErrShortWrite, written, len(pcm))
	}
	return nil
}

func (b *pcmBridge) close() {
	if b == nil {
		return
	}
	b.once.Do(func() {
		_ = b.writer.CloseWithError(io.ErrClosedPipe)
		_ = b.reader.CloseWithError(io.ErrClosedPipe)
	})
}
