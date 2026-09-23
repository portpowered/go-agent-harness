package agentruntime

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
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

// SessionDurationArtifactLifecycle receives the stream messages that crossed
// the duration admission boundary and owns their finalization. Callers can
// attach the existing audio/transcript resources through the context without
// making the CLI responsible for session cleanup.
type SessionDurationArtifactLifecycle interface {
	Accept(messages.StreamMessage) error
	Flush() error
	Close() error
}

// sessionDurationTerminalRecorder receives normalized terminal metadata that
// the duration controller emits. It is deliberately separate from the raw
// artifact lifecycle: a recording directory needs the controller-owned
// summary, but must not be given a fabricated provider frame.
type sessionDurationTerminalRecorder interface {
	RecordTerminalSummary(transcript.RecordingTerminalSummary) error
}

type sessionDurationArtifactLifecycleWithTerminal struct {
	artifacts SessionDurationArtifactLifecycle
	recorder  sessionDurationTerminalRecorder
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
	summary, present, err := recordingTerminalSummaryFromMessage(msg)
	if err != nil {
		return err
	}
	if !present {
		return nil
	}
	return a.recorder.RecordTerminalSummary(*summary)
}

func recordingTerminalSummaryFromMessage(msg messages.StreamMessage) (*transcript.RecordingTerminalSummary, bool, error) {
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

// SessionDurationArtifactPaths identifies the production-owned files that a
// positive duration run should finalize. The CLI supplies these paths while
// the services layer retains ownership of opening, flushing, and closing the
// resources.
type SessionDurationArtifactPaths struct {
	AudioPath      string
	TranscriptPath string
}

type sessionDurationArtifactPathsContextKey struct{}

// WithSessionDurationArtifacts attaches production-owned output resources to a
// duration run. The duration controller flushes and closes them after the
// accepted loop output has drained, including the synthesized terminal record.
func WithSessionDurationArtifacts(ctx context.Context, artifacts SessionDurationArtifactLifecycle) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, sessionDurationArtifactsContextKey{}, artifacts)
}

func sessionDurationArtifactsFromContext(ctx context.Context) SessionDurationArtifactLifecycle {
	if ctx == nil {
		return nil
	}
	artifacts, _ := ctx.Value(sessionDurationArtifactsContextKey{}).(SessionDurationArtifactLifecycle)
	return artifacts
}

func withSessionDurationTerminalRecorder(ctx context.Context, recorder sessionDurationTerminalRecorder) context.Context {
	if recorder == nil {
		return ctx
	}
	return WithSessionDurationArtifacts(ctx, &sessionDurationArtifactLifecycleWithTerminal{
		artifacts: sessionDurationArtifactsFromContext(ctx),
		recorder:  recorder,
	})
}

// WithSessionDurationArtifactPaths asks the duration entry point to create
// the production-owned WAV and JSONL resources after validation and runtime
// planning. Existing lifecycle values take precedence, which keeps injected
// sinks useful for tests and other callers that already own their resources.
func WithSessionDurationArtifactPaths(ctx context.Context, paths SessionDurationArtifactPaths) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, sessionDurationArtifactPathsContextKey{}, paths)
}

func sessionDurationArtifactPathsFromContext(ctx context.Context) (SessionDurationArtifactPaths, bool) {
	if ctx == nil {
		return SessionDurationArtifactPaths{}, false
	}
	paths, ok := ctx.Value(sessionDurationArtifactPathsContextKey{}).(SessionDurationArtifactPaths)
	return paths, ok
}

func prepareSessionDurationArtifacts(ctx context.Context) (context.Context, error) {
	if sessionDurationArtifactsFromContext(ctx) != nil {
		return ctx, nil
	}
	paths, ok := sessionDurationArtifactPathsFromContext(ctx)
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
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
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

func finalizeSessionDurationArtifacts(artifacts SessionDurationArtifactLifecycle) error {
	if artifacts == nil {
		return nil
	}
	return errors.Join(
		wrapSessionPhaseError("flush duration artifacts", invokeSessionFinalizer(artifacts.Flush)),
		wrapSessionPhaseError("close duration artifacts", invokeSessionFinalizer(artifacts.Close)),
	)
}
