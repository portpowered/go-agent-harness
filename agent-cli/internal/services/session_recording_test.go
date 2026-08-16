package services

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

func TestSessionDirectoryRecordingCapturesBothPerspectivesAndExactPCM(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "nested", "capture")
	plan := sessionRuntimePlan{provider: sessionProviderOpenAI}
	recording := newSessionDirectoryRecording(destination, plan, SessionRunOptions{Model: "gpt-realtime"})
	inner := newSessionRecordingTestSession()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wrapper := newSessionDirectoryRecordingSession(ctx, inner, recording)

	inputSegments := [][]byte{{0x01, 0x00, 0xff, 0x7f}, {0x02, 0x00}}
	outputSegments := [][]byte{{0x10, 0x00}, {0x11, 0x00, 0x12, 0x00}}
	for _, segment := range inputSegments {
		if !wrapper.Send(ctx, messages.StreamMessage{Type: messages.StreamTypeAudioDelta, Value: messages.NewAudioDeltaValue(segment)}) {
			t.Fatal("recording wrapper rejected input audio")
		}
	}
	for _, segment := range outputSegments {
		if !inner.receive.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeAudioDelta, Value: messages.NewAudioDeltaValue(segment)}) {
			t.Fatal("test session rejected output audio")
		}
	}
	for range outputSegments {
		if _, ok := wrapper.Receive().ReadBlocking(ctx.Done()); !ok {
			t.Fatal("recording wrapper did not forward output audio")
		}
	}
	if err := wrapper.Close(); err != nil {
		t.Fatalf("close recording wrapper: %v", err)
	}
	if err := recording.Finalize(); err != nil {
		t.Fatalf("finalize recording: %v", err)
	}

	entries := recordingEntries(t, destination)
	wantEntries := []string{
		"agent.transcript.jsonl",
		"audio",
		"audio/in-000.pcm",
		"audio/in-001.pcm",
		"audio/out-000.pcm",
		"audio/out-001.pcm",
		"client.transcript.jsonl",
		"manifest.json",
	}
	if !reflect.DeepEqual(entries, wantEntries) {
		t.Fatalf("recording entries = %v, want %v", entries, wantEntries)
	}
	for index, want := range inputSegments {
		path := filepath.Join(destination, "audio", "in-"+threeRecordingDigits(index)+".pcm")
		if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, want) {
			t.Fatalf("input segment %s = %x, err %v, want %x", path, got, err, want)
		}
	}
	for index, want := range outputSegments {
		path := filepath.Join(destination, "audio", "out-"+threeRecordingDigits(index)+".pcm")
		if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, want) {
			t.Fatalf("output segment %s = %x, err %v, want %x", path, got, err, want)
		}
	}

	for _, side := range []string{"client.transcript.jsonl", "agent.transcript.jsonl"} {
		records := readSessionRecordingTranscript(t, filepath.Join(destination, side))
		if len(records) != len(inputSegments)+len(outputSegments) {
			t.Fatalf("%s records = %d, want %d", side, len(records), len(inputSegments)+len(outputSegments))
		}
		for _, record := range records {
			if len(record.Payload) == 0 || record.Stream != transcript.StreamWebSocket {
				t.Fatalf("%s has invalid record: %+v", side, record)
			}
			if _, err := gwtesting.UnmarshalStreamMessage(record.Payload); err != nil {
				t.Fatalf("%s payload is not a stream frame: %v", side, err)
			}
		}
	}

	manifestBytes, err := os.ReadFile(filepath.Join(destination, "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var manifest transcript.RecordingManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	if manifest.Transport != "websocket" || manifest.Model != "gpt-realtime" || manifest.ClockBase == "" {
		t.Fatalf("manifest metadata = %+v", manifest)
	}
	if len(manifest.Artifacts) != 6 {
		t.Fatalf("manifest artifacts = %d, want 6", len(manifest.Artifacts))
	}
	for _, artifact := range manifest.Artifacts {
		data, err := os.ReadFile(filepath.Join(destination, filepath.FromSlash(artifact.Path)))
		if err != nil {
			t.Fatalf("read artifact %s: %v", artifact.Path, err)
		}
		digest := sha256.Sum256(data)
		if got := hex.EncodeToString(digest[:]); got != artifact.SHA256 {
			t.Fatalf("hash for %s = %s, want %s", artifact.Path, got, artifact.SHA256)
		}
	}
}

func TestRunSessionWithRecordingDirectoryRejectsNonEmptyDestinationBeforeConnect(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "capture")
	if err := os.MkdirAll(destination, 0o755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(destination, "customer.txt")
	want := []byte("keep this file")
	if err := os.WriteFile(sentinel, want, 0o644); err != nil {
		t.Fatal(err)
	}
	inferencer := &countingSessionRecordingInferencer{}
	err := RunSessionWithRecordingDirectory(context.Background(), io.Discard, SessionRunOptions{
		Provider:          config.ProviderOpenAI,
		Model:             "gpt-realtime",
		APIKey:            "test-key",
		ConfigDir:         t.TempDir(),
		SessionInferencer: inferencer,
	}, destination)
	if !errors.Is(err, transcript.ErrRecordingDestinationNotEmpty) || !errors.Is(err, transcript.ErrRecordingDestination) {
		t.Fatalf("error = %v, want destination identities", err)
	}
	if inferencer.connects != 0 {
		t.Fatalf("connects = %d, want zero", inferencer.connects)
	}
	if got, readErr := os.ReadFile(sentinel); readErr != nil || !bytes.Equal(got, want) {
		t.Fatalf("sentinel = %q, err %v, want %q", got, readErr, want)
	}
}

type sessionRecordingTestSession struct {
	receive *messages.TypedBuffer[messages.StreamMessage]
	sent    *messages.TypedBuffer[messages.StreamMessage]
	done    chan struct{}
}

func newSessionRecordingTestSession() *sessionRecordingTestSession {
	return &sessionRecordingTestSession{
		receive: messages.NewTypedBuffer[messages.StreamMessage](32),
		sent:    messages.NewTypedBuffer[messages.StreamMessage](32),
		done:    make(chan struct{}),
	}
}

func (s *sessionRecordingTestSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	return s.sent.Write(ctx, msg)
}

func (s *sessionRecordingTestSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.receive
}

func (s *sessionRecordingTestSession) Done() <-chan struct{} { return s.done }

func (s *sessionRecordingTestSession) Close() error {
	select {
	case <-s.done:
	default:
		close(s.done)
	}
	return nil
}

type countingSessionRecordingInferencer struct{ connects int }

func (i *countingSessionRecordingInferencer) ConnectSession(context.Context) (messages.Session, error) {
	i.connects++
	return newSessionRecordingTestSession(), nil
}

func recordingEntries(t *testing.T, root string) []string {
	t.Helper()
	var entries []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		entries = append(entries, filepath.ToSlash(relative))
		return nil
	})
	if err != nil {
		t.Fatalf("walk recording: %v", err)
	}
	sort.Strings(entries)
	return entries
}

func readSessionRecordingTranscript(t *testing.T, path string) []transcript.Record {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read transcript %s: %v", path, err)
	}
	var records []transcript.Record
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		record, err := transcript.Decode(line)
		if err != nil {
			t.Fatalf("decode transcript %s: %v", path, err)
		}
		records = append(records, record)
	}
	return records
}

func threeRecordingDigits(index int) string {
	return fmt.Sprintf("%03d", index)
}

var _ messages.Session = (*sessionRecordingTestSession)(nil)
var _ messages.SessionInferencer = (*countingSessionRecordingInferencer)(nil)
