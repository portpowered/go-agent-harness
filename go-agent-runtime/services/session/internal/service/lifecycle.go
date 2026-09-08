package service

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	agent "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/internal/execution"
)

type handle struct {
	mu        sync.Mutex
	executor  *agent.Executor
	runData   *agent.RunData
	config    agent.Config
	ctx       context.Context
	cancel    context.CancelFunc
	closed    bool
	closeOnce sync.Once
	closeDone chan struct{}
	closeErr  error
	active    map[*ownedStream]struct{}
	starts    sync.WaitGroup
}

// ownedStream couples a turn to its Open lifetime. The lower-level stream
// owns its producer cleanup; this wrapper makes that cleanup part of the
// handle's Close contract and removes the stream from the handle exactly once.
type ownedStream struct {
	handle *handle
	stream agentloop.Stream
	cancel context.CancelFunc
	stop   func() bool

	closeOnce  sync.Once
	finishOnce sync.Once
	done       chan struct{}
	closeErr   error
}

func (h *handle) SessionID() string {
	if h == nil {
		return ""
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.runData == nil {
		return ""
	}
	return h.runData.SessionID
}

func (h *handle) Stream(ctx context.Context, input agentloop.ExecuteInput) (agentloop.Stream, error) {
	if h == nil {
		return nil, fmt.Errorf("session handle is closed")
	}
	if ctx == nil {
		return nil, fmt.Errorf("session stream context is required")
	}
	h.mu.Lock()
	if h.closed || h.runData == nil {
		h.mu.Unlock()
		return nil, fmt.Errorf("session handle is closed")
	}
	if err := h.ctx.Err(); err != nil {
		h.mu.Unlock()
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		h.mu.Unlock()
		return nil, err
	}
	executor := h.executor
	runData := h.runData
	config := h.config
	lifetimeCtx := h.ctx
	h.starts.Add(1)
	h.mu.Unlock()
	defer h.starts.Done()

	// A turn may have a shorter context than the handle, but it must never
	// outlive the handle's Open context. AfterFunc avoids a permanent watcher
	// goroutine for contexts that are never cancelled.
	turnCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(lifetimeCtx, cancel) //nolint:contextcheck // The separately owned handle lifetime must also cancel this caller-derived turn.
	if err := turnCtx.Err(); err != nil {
		stop()
		cancel()
		return nil, err
	}
	stream, err := executor.ExecuteStreamingTurn(turnCtx, runData, input, &config)
	if err != nil {
		stop()
		cancel()
		return nil, err
	}
	owned := &ownedStream{
		handle: h,
		stream: stream,
		cancel: cancel,
		stop:   stop,
		done:   make(chan struct{}),
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil, errors.Join(fmt.Errorf("session handle is closed"), owned.Close())
	}
	h.active[owned] = struct{}{}
	h.mu.Unlock()
	return owned, nil
}

func (h *handle) Save() error {
	if h == nil {
		return fmt.Errorf("session handle is closed")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || h.runData == nil {
		return fmt.Errorf("session handle is closed")
	}
	return h.executor.SaveSession(h.runData)
}

func (h *handle) Flush(recordPath string) error {
	if h == nil {
		return fmt.Errorf("session handle is closed")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || h.runData == nil {
		return fmt.Errorf("session handle is closed")
	}
	return h.executor.FlushRecorder(h.runData, recordPath)
}

func (h *handle) Close() error {
	if h == nil {
		return nil
	}
	h.closeOnce.Do(func() {
		h.mu.Lock()
		h.closed = true
		if h.cancel != nil {
			h.cancel()
		}
		streams := make([]*ownedStream, 0, len(h.active))
		for stream := range h.active {
			streams = append(streams, stream)
		}
		h.mu.Unlock()

		// Closing a stream is idempotent and drains the lower-level event source.
		// Do this outside the handle lock because stream Close may invoke the
		// removal callback below.
		for _, stream := range streams {
			h.closeErr = errors.Join(h.closeErr, stream.Close())
		}
		// A Stream call may have released the handle lock while constructing the
		// lower-level stream. Wait for that construction to publish or close its
		// stream before releasing the invocation logger and returning from Close.
		h.starts.Wait()
		close(h.closeDone)
	})
	if h.closeDone != nil {
		<-h.closeDone
	}
	return h.closeErr
}

func (h *handle) removeStream(stream *ownedStream) {
	if h == nil {
		return
	}
	h.mu.Lock()
	delete(h.active, stream)
	h.mu.Unlock()
}

func (s *ownedStream) HasNext() bool {
	if s == nil || s.stream == nil {
		return false
	}
	ok := s.stream.HasNext()
	if !ok {
		s.finish()
	}
	return ok
}

func (s *ownedStream) Response() agentloop.Response {
	if s == nil || s.stream == nil {
		return agentloop.Response{}
	}
	return s.stream.Response()
}

func (s *ownedStream) Err() error {
	if s == nil || s.stream == nil {
		return nil
	}
	return s.stream.Err()
}

func (s *ownedStream) Outcome() agentloop.StreamOutcome {
	if s == nil || s.stream == nil {
		return agentloop.StreamOutcome{}
	}
	return s.stream.Outcome()
}

func (s *ownedStream) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		if s.stop != nil {
			s.stop()
		}
		if s.cancel != nil {
			s.cancel()
		}
		if s.stream != nil {
			s.closeErr = s.stream.Close()
		}
		s.finish()
	})
	if s.done != nil {
		<-s.done
	}
	return s.closeErr
}

func (s *ownedStream) finish() {
	if s == nil {
		return
	}
	s.finishOnce.Do(func() {
		if s.stop != nil {
			s.stop()
		}
		if s.cancel != nil {
			s.cancel()
		}
		if s.handle != nil {
			s.handle.removeStream(s)
		}
		if s.done != nil {
			close(s.done)
		}
	})
}
