package agentruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
)

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

type failingSessionRecordingInferencer struct{ err error }

func (i *failingSessionRecordingInferencer) ConnectSession(context.Context) (messages.Session, error) {
	return nil, i.err
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

func assertGoroutinesSettled(t *testing.T, baseline int, operation string) {
	t.Helper()
	deadline := time.Now().Add(750 * time.Millisecond)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= baseline+2 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("goroutines after %s = %d, baseline = %d; lifecycle did not settle", operation, runtime.NumGoroutine(), baseline)
}

func TestSessionDirectoryRecordingCloseDrainsPendingProviderOutput(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "pending-output")
	recording := newSessionDirectoryRecording(destination, sessionRuntimePlan{provider: sessionProviderOpenAI}, SessionRunOptions{ModelCatalog: testModelCatalog(), Model: "gpt-realtime"})
	inner := newSessionRecordingTestSession()
	ctx := context.Background()
	wrapper := &sessionDirectoryRecordingSession{
		inner:     inner,
		recording: recording,
		ctx:       ctx,
		receive:   messages.NewTypedBuffer[messages.StreamMessage](8),
		done:      make(chan struct{}),
	}
	close(wrapper.done)

	if !wrapper.Send(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeAudioDelta,
		Role:  messages.RoleUser,
		Value: messages.NewAudioDeltaValue([]byte{1, 0}),
	}) || !wrapper.Send(ctx, messages.StreamMessage{Type: messages.StreamTypeMessageEnd}) {
		t.Fatal("recording wrapper rejected input turn")
	}
	if !inner.receive.Write(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeAudioDelta,
		Role:  messages.RoleAssistant,
		Value: messages.NewAudioDeltaValue([]byte{2, 0}),
	}) || !inner.receive.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeMessageEnd}) {
		t.Fatal("test session rejected pending provider output")
	}
	if err := wrapper.Close(); err != nil {
		t.Fatalf("close recording wrapper: %v", err)
	}
	if err := recording.Finalize(); err != nil {
		t.Fatalf("finalize recording: %v", err)
	}

	output, err := os.ReadFile(filepath.Join(destination, "audio", "out-000.pcm"))
	if err != nil {
		t.Fatalf("read drained provider output: %v", err)
	}
	if !bytes.Equal(output, []byte{2, 0}) {
		t.Fatalf("drained provider output = %x, want 0200", output)
	}
	sessionLog, err := os.ReadFile(filepath.Join(destination, "session-log.jsonl"))
	if err != nil {
		t.Fatalf("read drained provider session log: %v", err)
	}
	var entry struct {
		Response struct {
			AudioBytes uint64 `json:"audio_bytes"`
		} `json:"response"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(sessionLog), &entry); err != nil {
		t.Fatalf("decode drained provider session log: %v", err)
	}
	if entry.Response.AudioBytes != 2 {
		t.Fatalf("drained provider session log audio bytes = %d, want 2", entry.Response.AudioBytes)
	}
}
