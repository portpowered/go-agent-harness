package service

import (
	"context"
	"errors"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/inference"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

// injectedSessionCapture keeps the capture wrapper and destination claim in
// the recording service for host-supplied sessions. It has no provider policy;
// it only preserves the ordered session traffic and publishes it once the
// wrapped session has terminated.
type injectedSessionCapture struct {
	source    messages.SessionInferencer
	inner     *gatewaytesting.RecordingSessionInferencer
	path      string
	claim     recording.DestinationClaim
	done      <-chan struct{}
	flushed   chan struct{}
	flushOnce sync.Once

	mu       sync.Mutex
	flushErr error
}

func (r *injectedSessionCapture) ConnectSession(ctx context.Context) (messages.Session, error) {
	if r == nil || r.inner == nil {
		return nil, errors.New("injected recording session is unavailable")
	}
	session, err := r.inner.ConnectSession(ctx)
	if err != nil {
		return nil, err
	}
	if session == nil || session.Done() == nil {
		return nil, errors.New("injected recording session has no termination signal")
	}
	r.mu.Lock()
	r.done = session.Done()
	r.mu.Unlock()
	go func(done <-chan struct{}) {
		<-done
		_ = r.FlushCapture() //nolint:errcheck // FlushCapture latches its result for the caller's bounded finalization.
	}(session.Done())
	return session, nil
}

func (r *injectedSessionCapture) FlushCapture() error {
	if r == nil {
		return nil
	}
	r.flushOnce.Do(func() {
		r.mu.Lock()
		done := r.done
		r.mu.Unlock()
		if done != nil {
			<-done
		}
		capture := r.inner.Recorder()
		if capture == nil {
			r.setFlushError(errors.New("injected recording session did not connect"))
			return
		}
		var err error
		if r.claim != nil {
			err = r.claim.Publish(capture.FlushToFile)
			err = errors.Join(err, r.claim.Release())
		} else {
			err = capture.FlushToFile(r.path)
		}
		r.setFlushError(err)
	})
	<-r.flushed
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.flushErr
}

func (r *injectedSessionCapture) FlushToFile(path string) error {
	if r == nil || r.inner == nil {
		return errors.New("injected recording session is unavailable")
	}
	if err := r.FlushCapture(); err != nil {
		return err
	}
	if path == r.path {
		return nil
	}
	capture := r.inner.Recorder()
	if capture == nil {
		return errors.New("injected recording session did not connect")
	}
	return capture.FlushToFile(path)
}

func (r *injectedSessionCapture) setFlushError(err error) {
	r.mu.Lock()
	r.flushErr = err
	r.mu.Unlock()
	close(r.flushed)
}

func (r *injectedSessionCapture) Request() inference.SessionRequest {
	if requester, ok := r.source.(interface {
		Request() inference.SessionRequest
	}); ok {
		return requester.Request()
	}
	return inference.SessionRequest{}
}

func (r *injectedSessionCapture) SetSessionAudioOutput(format models.AudioFormat, rate models.SampleRate) {
	if configurer, ok := r.source.(interface {
		SetSessionAudioOutput(models.AudioFormat, models.SampleRate)
	}); ok {
		configurer.SetSessionAudioOutput(format, rate)
	}
}

func (r *injectedSessionCapture) SetSessionAudioInput(format models.AudioFormat, rate models.SampleRate) {
	if configurer, ok := r.source.(interface {
		SetSessionAudioInput(models.AudioFormat, models.SampleRate)
	}); ok {
		configurer.SetSessionAudioInput(format, rate)
	}
}

var _ recording.SessionCapture = (*injectedSessionCapture)(nil)
