package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

func TestPrepareArtifactsPersistsPlayableAudioAndTranscript(t *testing.T) {
	root := t.TempDir()
	audioPath := filepath.Join(root, "session.wav")
	transcriptPath := filepath.Join(root, "session.jsonl")
	ctx := WithSessionDurationArtifactPaths(context.Background(), sessionduration.SessionDurationArtifactPaths{
		AudioPath:      audioPath,
		TranscriptPath: transcriptPath,
	})
	prepared, err := PrepareArtifacts(ctx)
	if err != nil {
		t.Fatalf("PrepareArtifacts() error = %v", err)
	}
	artifacts := ArtifactsFromContext(prepared)
	if artifacts == nil {
		t.Fatal("PrepareArtifacts() did not attach the file artifact lifecycle")
	}
	wantSamples := []int16{-1200, -1, 0, 1, 1200}
	if err := artifacts.Accept(messages.StreamMessage{
		Type:  messages.StreamTypeAudioDelta,
		Role:  messages.RoleAssistant,
		Value: messages.NewAudioDeltaValue(codec.EncodePCM16(wantSamples)),
	}); err != nil {
		t.Fatalf("Accept(audio) error = %v", err)
	}
	if err := artifacts.Accept(messages.StreamMessage{
		Type:  messages.StreamTypeTextDelta,
		Role:  messages.RoleAssistant,
		Value: messages.NewTextDeltaValue("spoken words"),
	}); err != nil {
		t.Fatalf("Accept(transcript) error = %v", err)
	}
	if err := artifacts.Flush(); err != nil {
		t.Fatalf("Flush() error = %v", err)
	}
	if err := artifacts.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := artifacts.Close(); err != nil {
		t.Fatalf("repeated Close() error = %v", err)
	}

	audio, err := os.ReadFile(audioPath)
	if err != nil {
		t.Fatalf("ReadFile(audio) error = %v", err)
	}
	rate, samples, err := wavio.Read(bytes.NewReader(audio))
	if err != nil {
		t.Fatalf("decode finalized WAV: %v", err)
	}
	if rate != wavio.Rate16kHz || !reflect.DeepEqual(samples, wantSamples) {
		t.Fatalf("finalized WAV = %d Hz %v, want %d Hz %v", rate, samples, wavio.Rate16kHz, wantSamples)
	}
	transcriptData, err := os.ReadFile(transcriptPath)
	if err != nil {
		t.Fatalf("ReadFile(transcript) error = %v", err)
	}
	scanner := bufio.NewScanner(bytes.NewReader(transcriptData))
	wantTypes := []messages.StreamMessageType{messages.StreamTypeAudioDelta, messages.StreamTypeTextDelta}
	for index, wantType := range wantTypes {
		if !scanner.Scan() {
			t.Fatalf("finalized transcript has no record %d: %v", index+1, scanner.Err())
		}
		var record transcript.Record
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			t.Fatalf("decode transcript record %d: %v", index+1, err)
		}
		var event struct {
			Type messages.StreamMessageType `json:"type"`
		}
		if err := json.Unmarshal(record.Payload, &event); err != nil {
			t.Fatalf("decode transcript event %d: %v", index+1, err)
		}
		if record.Tick != uint64(index+1) || event.Type != wantType {
			t.Fatalf("transcript record %d = tick %d type %q, want tick %d type %q", index+1, record.Tick, event.Type, index+1, wantType)
		}
	}
	if scanner.Scan() || scanner.Err() != nil {
		t.Fatalf("finalized transcript has extra records or a read error: %v", scanner.Err())
	}
}
