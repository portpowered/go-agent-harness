package artifacts

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	duration "github.com/portpowered/go-agent-harness/go-agent-runtime/services/duration"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

type Set struct {
	audio      duration.AudioSink
	transcript duration.TranscriptSink
	mu         sync.Mutex
	sequence   uint64
	closed     bool
	closeErr   error
}

func New(paths duration.ArtifactPaths) (duration.ArtifactLifecycle, error) {
	if paths.AudioPath == "" || paths.TranscriptPath == "" {
		return nil, errors.New("duration artifacts require both audio and transcript paths")
	}
	audio, err := newWAVSink(paths.AudioPath)
	if err != nil {
		return nil, err
	}
	transcriptSink, err := newTranscriptSink(paths.TranscriptPath)
	if err != nil {
		_ = audio.Close()
		return nil, err
	}
	return NewWithSinks(audio, transcriptSink), nil
}

func NewWithSinks(audio duration.AudioSink, transcriptSink duration.TranscriptSink) *Set {
	return &Set{audio: audio, transcript: transcriptSink}
}

type transcriptEvent struct {
	Type  messages.StreamMessageType  `json:"type"`
	Role  messages.Role               `json:"role,omitempty"`
	Value messages.StreamMessageValue `json:"value,omitempty"`
}

func (a *Set) Accept(msg messages.StreamMessage) error {
	if a == nil || msg.Type == messages.StreamTypeLoopEnd {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return errors.New("duration artifacts are closed")
	}
	if value, ok := msg.Value.(*messages.AudioDeltaValue); ok && a.audio != nil {
		samples, err := codec.DecodePCM16WithLimit(value.Content, len(value.Content))
		if err != nil {
			return fmt.Errorf("duration audio: %w", err)
		}
		if err := a.audio.WriteSamples(samples); err != nil {
			return fmt.Errorf("write duration audio: %w", err)
		}
	}
	if a.transcript == nil {
		return nil
	}
	payload, err := json.Marshal(transcriptEvent{Type: msg.Type, Role: msg.Role, Value: msg.Value})
	if err != nil {
		return fmt.Errorf("encode duration transcript event: %w", err)
	}
	seq := a.sequence + 1
	if err := a.transcript.Write(transcript.NewRecord(seq, time.Unix(0, int64(seq)), transcript.PeerAgent, transcript.DirectionIn, transcript.StreamWebSocket, payload)); err != nil {
		return fmt.Errorf("write duration transcript: %w", err)
	}
	a.sequence = seq
	return nil
}

func (a *Set) Flush() error {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return a.closeErr
	}
	return errors.Join(flushAudio(a.audio), flushTranscript(a.transcript))
}

func (a *Set) Close() error {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return a.closeErr
	}
	a.closed = true
	a.closeErr = errors.Join(closeAudio(a.audio), closeTranscript(a.transcript))
	return a.closeErr
}

func WithTerminalRecorder(artifacts duration.ArtifactLifecycle, recorder duration.TerminalRecorder) duration.ArtifactLifecycle {
	if recorder == nil {
		return artifacts
	}
	return &terminalArtifacts{artifacts: artifacts, recorder: recorder}
}

type terminalArtifacts struct {
	artifacts duration.ArtifactLifecycle
	recorder  duration.TerminalRecorder
}

func (a *terminalArtifacts) Accept(msg messages.StreamMessage) error {
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
	summary, present, err := (duration.TerminalSummaryDecoder{}).FromMessage(msg)
	if err != nil || !present {
		return err
	}
	return a.recorder.RecordTerminalSummary(*summary)
}

func (a *terminalArtifacts) Flush() error {
	if a == nil || a.artifacts == nil {
		return nil
	}
	return a.artifacts.Flush()
}

func (a *terminalArtifacts) Close() error {
	if a == nil || a.artifacts == nil {
		return nil
	}
	return a.artifacts.Close()
}

type wavSink struct {
	mu       sync.Mutex
	path     string
	file     *os.File
	writer   *wavio.StreamWriter
	closed   bool
	closeErr error
}

func newWAVSink(path string) (*wavSink, error) {
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
	return &wavSink{path: path, file: file, writer: writer}, nil
}

func (s *wavSink) WriteSamples(samples []int16) error {
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

func (s *wavSink) Flush() error {
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

func (s *wavSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return s.closeErr
	}
	s.closed = true
	var errs []error
	if err := s.writer.Close(); err != nil {
		errs = append(errs, fmt.Errorf("write duration audio %q: %w", s.path, err))
	} else if err := s.file.Sync(); err != nil {
		errs = append(errs, fmt.Errorf("flush duration audio %q: %w", s.path, err))
	}
	if err := s.file.Close(); err != nil {
		errs = append(errs, fmt.Errorf("close duration audio %q: %w", s.path, err))
	}
	s.closeErr = errors.Join(errs...)
	return s.closeErr
}

func newTranscriptSink(path string) (*transcript.Writer, error) {
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

func flushAudio(s duration.AudioSink) error {
	if s == nil {
		return nil
	}
	if err := s.Flush(); err != nil {
		return fmt.Errorf("flush duration audio: %w", err)
	}
	return nil
}

func flushTranscript(s duration.TranscriptSink) error {
	if s == nil {
		return nil
	}
	if err := s.Flush(); err != nil {
		return fmt.Errorf("flush duration transcript: %w", err)
	}
	return nil
}

func closeAudio(s duration.AudioSink) error {
	if s == nil {
		return nil
	}
	if err := s.Close(); err != nil {
		return fmt.Errorf("close duration audio: %w", err)
	}
	return nil
}

func closeTranscript(s duration.TranscriptSink) error {
	if s == nil {
		return nil
	}
	if err := s.Close(); err != nil {
		return fmt.Errorf("close duration transcript: %w", err)
	}
	return nil
}

var _ duration.ArtifactLifecycle = (*Set)(nil)
