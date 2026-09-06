package service

import (
	"context"
	"errors"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
	"sync"
)

// recordingSessionInferencer owns the recorder created at admission. The
// capture is flushed after the provider session closes, keeping the artifact
// lifecycle attached to the provider session rather than a CLI goroutine.
type recordingSessionInferencer struct {
	inner    messages.SessionInferencer
	recorder recording.Writer
	path     string

	mu           sync.Mutex
	flushErr     error
	flushDone    chan struct{}
	connecting   bool
	connected    bool
	flushStarted bool
	flushOnce    sync.Once
}

func (r *recordingSessionInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	if ctx == nil {
		return nil, errors.New("recording session requires a context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	if r.connecting || r.connected || r.flushStarted {
		r.mu.Unlock()
		return nil, errors.New("recording session inferencer already admitted")
	}
	r.connecting = true
	r.mu.Unlock()

	session, err := r.inner.ConnectSession(ctx)
	if err != nil {
		r.mu.Lock()
		r.connecting = false
		r.flushStarted = true
		r.mu.Unlock()
		r.finishFlush()
		return nil, err
	}
	if session == nil {
		r.mu.Lock()
		r.connecting = false
		r.flushStarted = true
		r.mu.Unlock()
		r.finishFlush()
		return nil, errors.New("recording session inferencer returned nil session")
	}
	done := session.Done()
	if done == nil {
		closeErr := session.Close()
		r.mu.Lock()
		r.connecting = false
		r.connected = true
		r.flushStarted = true
		r.mu.Unlock()
		r.finishFlush()
		return nil, errors.Join(errors.New("recording session has no termination signal"), closeErr)
	}
	r.mu.Lock()
	r.connecting = false
	r.connected = true
	r.flushStarted = true
	r.mu.Unlock()
	go func() {
		<-done
		r.finishFlush()
	}()
	return session, nil
}

func (r *recordingSessionInferencer) finishFlush() {
	if r == nil {
		return
	}
	r.flushOnce.Do(func() {
		err := r.recorder.FlushToFile(r.path)
		r.mu.Lock()
		r.flushErr = err
		r.mu.Unlock()
		close(r.flushDone)
	})
}

// FlushCapture is an optional lifecycle seam used by room owners that need
// to surface artifact write failures as part of terminal participant state.
// It is safe before session termination and waits for the provider session's
// close-triggered flush so callers can finalize their containing evidence
// bundle only after raw provider traffic is durable.
func (r *recordingSessionInferencer) FlushCapture() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	ready := r.connecting || r.connected || r.flushStarted
	flushDone := r.flushDone
	r.mu.Unlock()
	if ready && flushDone != nil {
		<-flushDone
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.flushErr
}
