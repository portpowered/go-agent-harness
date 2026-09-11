package artifacts

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	duration "github.com/portpowered/go-agent-harness/go-agent-runtime/services/duration"
)

func TestSetWritesPCMTranscriptAndTerminalRecorder(t *testing.T) {
	root := t.TempDir()
	audioPath := filepath.Join(root, "session.wav")
	transcriptPath := filepath.Join(root, "session.jsonl")
	recorder := &terminalRecorder{}
	artifacts, err := New(duration.ArtifactPaths{AudioPath: audioPath, TranscriptPath: transcriptPath})
	if err != nil {
		t.Fatal(err)
	}
	wrapped := WithTerminalRecorder(artifacts, recorder)
	for _, msg := range []messages.StreamMessage{
		{Type: messages.StreamTypeAudioDelta, Value: messages.NewAudioDeltaValue([]byte{1, 0, 2, 0})},
		{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("frozen")},
		providerTerminal(),
	} {
		if err := wrapped.Accept(msg); err != nil {
			t.Fatal(err)
		}
	}
	if err := wrapped.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := wrapped.Close(); err != nil {
		t.Fatal(err)
	}
	audioBytes, err := os.ReadFile(audioPath)
	if err != nil || len(audioBytes) < 44 || string(audioBytes[:4]) != "RIFF" || string(audioBytes[8:12]) != "WAVE" {
		t.Fatalf("WAV header err=%v bytes=%d", err, len(audioBytes))
	}
	transcriptBytes, err := os.ReadFile(transcriptPath)
	if err != nil || strings.Count(string(transcriptBytes), "\n") != 3 {
		t.Fatalf("transcript err=%v bytes=%q", err, transcriptBytes)
	}
	var payloads []string
	for _, line := range strings.Split(strings.TrimSpace(string(transcriptBytes)), "\n") {
		var record struct {
			Payload string `json:"payload"`
		}
		if json.Unmarshal([]byte(line), &record) == nil {
			decoded, decodeErr := base64.StdEncoding.DecodeString(record.Payload)
			if decodeErr == nil {
				payloads = append(payloads, string(decoded))
			}
		}
	}
	if !strings.Contains(strings.Join(payloads, "\n"), "frozen") || len(recorder.summaries) != 1 {
		t.Fatalf("transcript/terminal effects payloads=%q summaries=%d", payloads, len(recorder.summaries))
	}
}

func TestSetJoinsSinkFailuresAndRejectsMalformedPCM(t *testing.T) {
	flushErr := errors.New("flush identity")
	closeErr := errors.New("close identity")
	set := NewWithSinks(&audioSink{flushErr: flushErr, closeErr: closeErr}, &transcriptSink{flushErr: flushErr, closeErr: closeErr})
	if !errors.Is(set.Flush(), flushErr) || !errors.Is(set.Close(), closeErr) {
		t.Fatal("Flush/Close lost sink identity")
	}
	malformed := NewWithSinks(&audioSink{}, nil)
	err := malformed.Accept(messages.StreamMessage{Type: messages.StreamTypeAudioDelta, Value: messages.NewAudioDeltaValue([]byte{1})})
	if err == nil {
		t.Fatal("odd PCM was accepted")
	}
}

type audioSink struct {
	samples            []int16
	flushErr, closeErr error
}

func (s *audioSink) WriteSamples(samples []int16) error {
	s.samples = append(s.samples, samples...)
	return nil
}
func (s *audioSink) Flush() error { return s.flushErr }
func (s *audioSink) Close() error { return s.closeErr }

type transcriptSink struct {
	records            []transcript.Record
	flushErr, closeErr error
}

func (s *transcriptSink) Write(record transcript.Record) error {
	s.records = append(s.records, record)
	return nil
}
func (s *transcriptSink) Flush() error { return s.flushErr }
func (s *transcriptSink) Close() error { return s.closeErr }

type terminalRecorder struct {
	summaries []transcript.RecordingTerminalSummary
}

func (r *terminalRecorder) RecordTerminalSummary(summary transcript.RecordingTerminalSummary) error {
	r.summaries = append(r.summaries, summary)
	return nil
}

func providerTerminal() messages.StreamMessage {
	return messages.StreamMessage{Type: messages.StreamTypeSessionClose, Value: messages.NewSessionCloseValueWithTerminal("artifacts", "provider-close", "provider_close", messages.TerminalReasonProviderClose, messages.TerminalProvenanceProvider, messages.TerminalOutputPartial)}
}
