package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
)

// Artifact lifecycle implementation for the sessionduration service.
// sessionDurationTerminalRecorder receives normalized terminal metadata that
// the duration controller emits. It is deliberately separate from the raw
// artifact lifecycle: a recording directory needs the controller-owned
// summary, but must not be given a fabricated provider frame.
type sessionDurationArtifactLifecycleWithTerminal struct {
	artifacts sessionduration.ArtifactLifecycle
	recorder  sessionduration.TerminalRecorder
}

func (a *sessionDurationArtifactLifecycleWithTerminal) Accept(msg messages.StreamMessage) error {
	if a == nil {
		return nil
	}
	if a.artifacts != nil {
		if err := a.artifacts.Accept(msg); err != nil {
			return err
		}
	}
	if a.recorder == nil || msg.Type != messages.StreamTypeSessionClose {
		return nil
	}
	summary, present, err := RecordingTerminalSummaryFromMessage(msg)
	if err != nil {
		return err
	}
	if !present {
		return nil
	}
	return a.recorder.RecordTerminalSummary(*summary)
}

func RecordingTerminalSummaryFromMessage(msg messages.StreamMessage) (*transcript.RecordingTerminalSummary, bool, error) {
	if msg.Type != messages.StreamTypeSessionClose {
		return nil, false, nil
	}
	value, ok := msg.Value.(*messages.SessionCloseValue)
	if !ok || value == nil {
		return nil, false, nil
	}
	if value.Classification == "" && value.TerminalReason == "" && value.TerminalProvenance == "" && value.OutputState == "" {
		return nil, false, nil
	}
	summary := &transcript.RecordingTerminalSummary{
		Reason:             value.Reason,
		Classification:     value.Classification,
		TerminalReason:     value.TerminalReason,
		TerminalProvenance: value.TerminalProvenance,
		OutputState:        value.OutputState,
	}
	if err := summary.Validate(); err != nil {
		return nil, false, err
	}
	return summary, true, nil
}

func (a *sessionDurationArtifactLifecycleWithTerminal) Flush() error {
	if a == nil || a.artifacts == nil {
		return nil
	}
	return a.artifacts.Flush()
}

func (a *sessionDurationArtifactLifecycleWithTerminal) Close() error {
	if a == nil || a.artifacts == nil {
		return nil
	}
	return a.artifacts.Close()
}

type sessionDurationArtifactsContextKey struct{}

type sessionDurationArtifactPathsContextKey struct{}

// WithSessionDurationArtifacts attaches production-owned output resources to a
// duration run. The duration controller flushes and closes them after the
// accepted loop output has drained, including the synthesized terminal record.
func WithSessionDurationArtifacts(ctx context.Context, artifacts sessionduration.ArtifactLifecycle) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, sessionDurationArtifactsContextKey{}, artifacts)
}

func ArtifactsFromContext(ctx context.Context) sessionduration.ArtifactLifecycle {
	if ctx == nil {
		return nil
	}
	artifacts, _ := ctx.Value(sessionDurationArtifactsContextKey{}).(sessionduration.ArtifactLifecycle)
	return artifacts
}

func WithTerminalRecorder(ctx context.Context, recorder sessionduration.TerminalRecorder) context.Context {
	if recorder == nil {
		return ctx
	}
	return WithSessionDurationArtifacts(ctx, &sessionDurationArtifactLifecycleWithTerminal{
		artifacts: ArtifactsFromContext(ctx),
		recorder:  recorder,
	})
}

// WithSessionDurationArtifactPaths asks the duration entry point to create
// the production-owned WAV and JSONL resources after validation and runtime
// planning. Existing lifecycle values take precedence, which keeps injected
// sinks useful for tests and other callers that already own their resources.
func WithSessionDurationArtifactPaths(ctx context.Context, paths sessionduration.SessionDurationArtifactPaths) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, sessionDurationArtifactPathsContextKey{}, paths)
}

func ArtifactPathsFromContext(ctx context.Context) (sessionduration.SessionDurationArtifactPaths, bool) {
	if ctx == nil {
		return sessionduration.SessionDurationArtifactPaths{}, false
	}
	paths, ok := ctx.Value(sessionDurationArtifactPathsContextKey{}).(sessionduration.SessionDurationArtifactPaths)
	return paths, ok
}

func PrepareArtifacts(ctx context.Context) (context.Context, error) {
	if ArtifactsFromContext(ctx) != nil {
		return ctx, nil
	}
	paths, ok := ArtifactPathsFromContext(ctx)
	if !ok || (paths.AudioPath == "" && paths.TranscriptPath == "") {
		return ctx, nil
	}
	if paths.AudioPath == "" || paths.TranscriptPath == "" {
		return nil, errors.New("session duration artifacts require both audio and transcript paths")
	}
	artifacts, err := NewSessionDurationArtifactSet(paths.AudioPath, paths.TranscriptPath)
	if err != nil {
		return nil, fmt.Errorf("open session duration artifacts: %w", err)
	}
	return WithSessionDurationArtifacts(ctx, artifacts), nil
}

// SessionDurationAudioSink accepts PCM16 samples and owns their final WAV
// encoding. It deliberately accepts a partial final frame so a cutoff between
// audio frames remains an exact, playable artifact.
type SessionDurationAudioSink interface {
	WriteSamples([]int16) error
	Flush() error
	Close() error
}

// SessionDurationTranscriptSink is the lifecycle subset implemented by the
// shared transcript.Writer.
type SessionDurationTranscriptSink interface {
	Write(transcript.Record) error
	Flush() error
	Close() error
}

// SessionDurationArtifactSet adapts the shared audio and transcript primitives
// to the ordered duration finalization boundary.
type SessionDurationArtifactSet struct {
	audio      SessionDurationAudioSink
	transcript SessionDurationTranscriptSink

	mu       sync.Mutex
	sequence uint64
	closed   bool
	closeErr error
}

// NewSessionDurationArtifactSet opens the WAV and JSONL resources used by a
// duration run. The returned set owns both resources and closes them exactly
// once when the duration controller finishes.
func NewSessionDurationArtifactSet(audioPath, transcriptPath string) (*SessionDurationArtifactSet, error) {
	audioSink, err := newSessionDurationWAVSink(audioPath)
	if err != nil {
		return nil, err
	}
	transcriptSink, err := newSessionDurationTranscriptSink(transcriptPath)
	if err != nil {
		_ = audioSink.Close()
		return nil, err
	}
	return NewSessionDurationArtifactSetWithSinks(audioSink, transcriptSink), nil
}

func newSessionDurationTranscriptSink(path string) (*transcript.Writer, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open duration transcript %q: %w", path, err)
	}
	writer, err := transcript.NewWriterOn(file)
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("create duration transcript %q: %w", path, err)
	}
	return writer, nil
}

// NewSessionDurationArtifactSetWithSinks builds the same production lifecycle
// around caller-provided resources. It is useful for non-filesystem sinks and
// for preserving underlying flush/close error identity.
func NewSessionDurationArtifactSetWithSinks(audioSink SessionDurationAudioSink, transcriptSink SessionDurationTranscriptSink) *SessionDurationArtifactSet {
	return &SessionDurationArtifactSet{audio: audioSink, transcript: transcriptSink}
}

type sessionDurationTranscriptEvent struct {
	Type  messages.StreamMessageType  `json:"type"`
	Role  messages.Role               `json:"role,omitempty"`
	Value messages.StreamMessageValue `json:"value,omitempty"`
}

func (a *SessionDurationArtifactSet) Accept(msg messages.StreamMessage) error {
	if a == nil {
		return nil
	}
	// LOOP.END is an internal agent-loop lifecycle marker emitted after the
	// session terminal record; it is not provider output and must not trail the
	// finalized transcript's terminal record.
	if msg.Type == messages.StreamTypeLoopEnd {
		return nil
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return errors.New("session duration artifacts are closed")
	}

	if audioValue, ok := msg.Value.(*messages.AudioDeltaValue); ok && a.audio != nil {
		samples, err := sessionDurationPCM16Samples(audioValue.Content)
		if err != nil {
			return err
		}
		if err := a.audio.WriteSamples(samples); err != nil {
			return fmt.Errorf("write duration audio: %w", err)
		}
	}

	if a.transcript == nil {
		return nil
	}
	payload, err := json.Marshal(sessionDurationTranscriptEvent{
		Type:  msg.Type,
		Role:  msg.Role,
		Value: msg.Value,
	})
	if err != nil {
		return fmt.Errorf("encode duration transcript event: %w", err)
	}
	sequence := a.sequence + 1
	record := transcript.NewRecord(
		sequence,
		time.Unix(0, int64(sequence)),
		transcript.PeerAgent,
		transcript.DirectionIn,
		transcript.StreamWebSocket,
		payload,
	)
	if err := a.transcript.Write(record); err != nil {
		return fmt.Errorf("write duration transcript: %w", err)
	}
	a.sequence = sequence
	return nil
}

func (a *SessionDurationArtifactSet) Flush() error {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return a.closeErr
	}
	var flushErrs []error
	if a.audio != nil {
		if err := a.audio.Flush(); err != nil {
			flushErrs = append(flushErrs, fmt.Errorf("flush duration audio: %w", err))
		}
	}
	if a.transcript != nil {
		if err := a.transcript.Flush(); err != nil {
			flushErrs = append(flushErrs, fmt.Errorf("flush duration transcript: %w", err))
		}
	}
	return errors.Join(flushErrs...)
}

func (a *SessionDurationArtifactSet) Close() error {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return a.closeErr
	}
	a.closed = true

	var closeErrs []error
	if a.audio != nil {
		if err := a.audio.Close(); err != nil {
			closeErrs = append(closeErrs, fmt.Errorf("close duration audio: %w", err))
		}
	}
	if a.transcript != nil {
		if err := a.transcript.Close(); err != nil {
			closeErrs = append(closeErrs, fmt.Errorf("close duration transcript: %w", err))
		}
	}
	a.closeErr = errors.Join(closeErrs...)
	return a.closeErr
}

func sessionDurationPCM16Samples(content []byte) ([]int16, error) {
	if len(content) == 0 {
		return nil, nil
	}
	// Duration artifacts may span an entire session and therefore exceed the
	// per-provider-payload default bound; the file/sink boundary has already
	// bounded admission and owns the larger aggregate size policy.
	samples, err := codec.DecodePCM16WithLimit(content, len(content))
	if err != nil {
		return nil, fmt.Errorf("duration audio: %w", err)
	}
	return samples, nil
}

func FinalizeArtifacts(artifacts sessionduration.ArtifactLifecycle) error {
	if artifacts == nil {
		return nil
	}
	return errors.Join(
		artifacts.Flush(),
		artifacts.Close(),
	)
}
