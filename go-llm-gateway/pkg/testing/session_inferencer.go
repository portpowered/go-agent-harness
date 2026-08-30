package testing

import (
	"context"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

var _ messages.SessionInferencer = (*RecordingSessionInferencer)(nil)
var _ messages.SessionInferencer = (*ReplaySessionInferencer)(nil)

// RecordingSessionInferencer wraps a messages.SessionInferencer and records
// all bidirectional traffic for every session it creates.
// After the session completes, call Recorder().FlushToFile to persist.
type RecordingSessionInferencer struct {
	inner    messages.SessionInferencer
	recorder *SessionRecorder
}

// NewRecordingSessionInferencer wraps the given inferencer so that every
// session it produces is intercepted by a SessionRecorder.
func NewRecordingSessionInferencer(inner messages.SessionInferencer) *RecordingSessionInferencer {
	return &RecordingSessionInferencer{inner: inner}
}

// ConnectSession delegates to the inner inferencer and wraps the returned
// session with a SessionRecorder. Only a single session may be recorded;
// calling ConnectSession a second time returns an error.
func (r *RecordingSessionInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	if r.recorder != nil {
		return nil, fmt.Errorf("RecordingSessionInferencer: ConnectSession called more than once; only single-session recording is supported")
	}
	sess, err := r.inner.ConnectSession(ctx)
	if err != nil {
		return nil, err
	}
	r.recorder = NewSessionRecorder(sess, WithSessionRelayContext(ctx))
	return r.recorder, nil
}

// Recorder returns the SessionRecorder for the most recently connected session.
// Returns nil if ConnectSession has not been called.
func (r *RecordingSessionInferencer) Recorder() *SessionRecorder {
	return r.recorder
}

// SendMessage forwards complete rich messages to the wrapped provider session.
// Recording must preserve optional multimodal capabilities so a recorded
// image session behaves like its unwrapped session while the provider capture
// remains owned by this wrapper.
func (r *SessionRecorder) SendMessage(ctx context.Context, msg messages.Message) bool {
	sender, ok := r.inner.(interface {
		SendMessage(context.Context, messages.Message) bool
	})
	return ok && sender.SendMessage(ctx, msg)
}

// SendMessageWithoutResponse forwards a complete message without requesting a
// response. This is required for image turns whose following scheduled audio
// owns the response boundary.
func (r *SessionRecorder) SendMessageWithoutResponse(ctx context.Context, msg messages.Message) bool {
	sender, ok := r.inner.(interface {
		SendMessageWithoutResponse(context.Context, messages.Message) bool
	})
	return ok && sender.SendMessageWithoutResponse(ctx, msg)
}

// SupportsCompleteMessages preserves the wrapped session's optional
// multimodal capability declaration through the recording decorator.
func (r *SessionRecorder) SupportsCompleteMessages() bool {
	capabilities, ok := r.inner.(interface{ SupportsCompleteMessages() bool })
	return ok && capabilities.SupportsCompleteMessages()
}

// SupportsCompleteMessagesWithoutResponse preserves the wrapped session's
// deferred multimodal capability declaration through the recording decorator.
func (r *SessionRecorder) SupportsCompleteMessagesWithoutResponse() bool {
	capabilities, ok := r.inner.(interface {
		SupportsCompleteMessagesWithoutResponse() bool
	})
	return ok && capabilities.SupportsCompleteMessagesWithoutResponse()
}

// ReplaySessionInferencer implements messages.SessionInferencer by returning a
// SessionReplayer instead of connecting to a live provider.
type ReplaySessionInferencer struct {
	path string
	opts []SessionReplayerOption
}

// NewReplaySessionInferencer creates a ReplaySessionInferencer that will
// replay the capture at path for every ConnectSession call.
func NewReplaySessionInferencer(path string, opts ...SessionReplayerOption) *ReplaySessionInferencer {
	return &ReplaySessionInferencer{path: path, opts: opts}
}

// ConnectSession returns a SessionReplayer loaded from the capture file.
func (r *ReplaySessionInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	opts := append([]SessionReplayerOption{}, r.opts...)
	opts = append(opts, WithReplayContext(ctx))
	return NewSessionReplayer(r.path, opts...)
}
