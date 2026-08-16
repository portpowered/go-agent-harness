package services

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/wavio"
)

func TestRunSessionWithAudioOut_RoutesAssistantDeltasToRawStdout(t *testing.T) {
	first := sessionAudioFrame(100)
	second := sessionAudioFrame(-200)
	user := sessionAudioFrame(900)
	inf := &scriptedSessionInferencer{events: []messages.StreamMessage{
		{Type: messages.StreamTypeAudioDelta, Role: messages.RoleAssistant, Value: messages.NewAudioDeltaValue(pcm16Bytes(first))},
		{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("not audio")},
		{Type: messages.StreamTypeAudioDelta, Role: messages.RoleUser, Value: messages.NewAudioDeltaValue(pcm16Bytes(user))},
		{Type: messages.StreamTypeAudioDelta, Value: messages.NewAudioDeltaValue(pcm16Bytes(second))},
		{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
	}}

	var stdout bytes.Buffer
	err := RunSessionWithAudioOut(context.Background(), &stdout, SessionRunOptions{
		ReplayPath:        "synthetic.json",
		SessionInferencer: inf,
	}, "-")
	if err != nil {
		t.Fatalf("RunSessionWithAudioOut: %v", err)
	}

	want := append(pcm16Bytes(first), pcm16Bytes(second)...)
	if !bytes.Equal(stdout.Bytes(), want) {
		t.Fatalf("raw stdout = %d bytes, want exact assistant PCM16 bytes", stdout.Len())
	}
	if !inf.connected {
		t.Fatal("session inferencer was not connected")
	}
}

func TestRunSessionWithAudioOut_FinalizesPlayableWAV(t *testing.T) {
	first := sessionAudioFrame(300)
	second := sessionAudioFrame(-500)
	path := filepath.Join(t.TempDir(), "response.wav")
	inf := &scriptedSessionInferencer{events: []messages.StreamMessage{
		{Type: messages.StreamTypeAudioDelta, Role: messages.RoleAssistant, Value: messages.NewAudioDeltaValue(pcm16Bytes(first))},
		{Type: messages.StreamTypeAudioDelta, Role: messages.RoleAssistant, Value: messages.NewAudioDeltaValue(pcm16Bytes(second))},
		{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
	}}

	if err := RunSessionWithAudioOut(context.Background(), io.Discard, SessionRunOptions{
		ReplayPath:        "synthetic.json",
		SessionInferencer: inf,
	}, path); err != nil {
		t.Fatalf("RunSessionWithAudioOut: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	rate, samples, err := wavio.Read(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("wavio.Read: %v", err)
	}
	if rate != audio.SampleRate {
		t.Fatalf("sample rate = %d, want %d", rate, audio.SampleRate)
	}
	want := append(first, second...)
	if !equalInt16(samples, want) {
		t.Fatalf("WAV samples = %d samples, want exact ordered response", len(samples))
	}
}

func TestRunSessionWithAudioOut_NoAudioRemovesEmptyWAV(t *testing.T) {
	path := filepath.Join(t.TempDir(), "silent.wav")
	inf := &scriptedSessionInferencer{events: []messages.StreamMessage{
		{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("silence")},
		{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
	}}

	if err := RunSessionWithAudioOut(context.Background(), io.Discard, SessionRunOptions{
		ReplayPath:        "synthetic.json",
		SessionInferencer: inf,
	}, path); err != nil {
		t.Fatalf("silent session: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("silent WAV stat error = %v, want no corrupt output file", err)
	}
}

func TestRunSessionWithAudioOut_PreflightsPathBeforeSessionConnect(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "response.wav")
	inf := &scriptedSessionInferencer{}

	err := RunSessionWithAudioOut(context.Background(), io.Discard, SessionRunOptions{
		ReplayPath:        "synthetic.json",
		SessionInferencer: inf,
	}, path)
	if err == nil || !strings.Contains(err.Error(), "--audio-out") || !strings.Contains(err.Error(), path) {
		t.Fatalf("preflight error = %v, want --audio-out and target path", err)
	}
	if inf.connected {
		t.Fatal("invalid audio output path connected to the session")
	}
}

func TestRunSessionWithAudioOut_PreflightsDirectoryTargetBeforeSessionConnect(t *testing.T) {
	path := filepath.Join(t.TempDir(), "response.wav")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	inf := &scriptedSessionInferencer{}

	err := RunSessionWithAudioOut(context.Background(), io.Discard, SessionRunOptions{
		ReplayPath:        "synthetic.json",
		SessionInferencer: inf,
	}, path)
	if err == nil || !strings.Contains(err.Error(), "--audio-out") {
		t.Fatalf("directory target error = %v, want --audio-out context", err)
	}
	var streamErr *audio.StreamError
	if !errors.As(err, &streamErr) || streamErr.Path != path || streamErr.Operation != "open" {
		t.Fatalf("directory target error = %v, want typed open StreamError for %q", err, path)
	}
	if inf.connected {
		t.Fatal("directory audio output target connected to the session")
	}
}

func TestRunSessionWithAudioOut_GrowsBeforeSessionCompletes(t *testing.T) {
	first := sessionAudioFrame(1200)
	second := sessionAudioFrame(-1400)
	firstBytes := pcm16Bytes(first)
	release := make(chan struct{})
	firstWritten := make(chan struct{})
	writer := &growingSessionAudioWriter{firstWritten: firstWritten}
	inf := &gatedSessionAudioInferencer{
		first:   firstBytes,
		second:  pcm16Bytes(second),
		release: release,
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- RunSessionWithAudioOut(context.Background(), writer, SessionRunOptions{
			ReplayPath:        "synthetic.json",
			SessionInferencer: inf,
		}, "-")
	}()

	select {
	case <-firstWritten:
	case <-time.After(2 * time.Second):
		t.Fatal("first audio delta did not reach stdout while session remained open")
	}
	if got := writer.snapshot(); !bytes.Equal(got, firstBytes) {
		t.Fatalf("stdout after first delta = %d bytes, want exactly first delta", len(got))
	}
	close(release)

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("RunSessionWithAudioOut: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("session did not terminate after releasing the second delta")
	}
	want := append(firstBytes, pcm16Bytes(second)...)
	if got := writer.snapshot(); !bytes.Equal(got, want) {
		t.Fatalf("completed stdout = %d bytes, want ordered multi-delta PCM16", len(got))
	}
}

func TestRunSessionWithAudioOut_TruncatesExistingRawFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "response.raw")
	if err := os.WriteFile(path, []byte("stale output"), 0o600); err != nil {
		t.Fatal(err)
	}
	inf := &scriptedSessionInferencer{events: []messages.StreamMessage{
		{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
	}}

	if err := RunSessionWithAudioOut(context.Background(), io.Discard, SessionRunOptions{
		ReplayPath:        "synthetic.json",
		SessionInferencer: inf,
	}, path); err != nil {
		t.Fatalf("silent raw session: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 0 {
		t.Fatalf("existing raw file retained %d stale bytes", len(data))
	}
}

func TestRunSessionWithAudioOut_PreservesSinkWriteError(t *testing.T) {
	wantErr := errors.New("stdout write failed")
	inf := &scriptedSessionInferencer{events: []messages.StreamMessage{
		{Type: messages.StreamTypeAudioDelta, Role: messages.RoleAssistant, Value: messages.NewAudioDeltaValue(pcm16Bytes(sessionAudioFrame(700)))},
		{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
	}}

	err := RunSessionWithAudioOut(context.Background(), sessionAudioErrorWriter{err: wantErr}, SessionRunOptions{
		ReplayPath:        "synthetic.json",
		SessionInferencer: inf,
	}, "-")
	if !errors.Is(err, wantErr) {
		t.Fatalf("write error = %v, want underlying error", err)
	}
}

func sessionAudioFrame(seed int16) []int16 {
	frame := make([]int16, audio.FrameSize)
	for index := range frame {
		frame[index] = seed + int16(index%31)
	}
	return frame
}

func pcm16Bytes(samples []int16) []byte {
	data := make([]byte, len(samples)*2)
	for index, sample := range samples {
		binary.LittleEndian.PutUint16(data[index*2:], uint16(sample))
	}
	return data
}

func equalInt16(got, want []int16) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

type sessionAudioErrorWriter struct {
	err error
}

func (w sessionAudioErrorWriter) Write([]byte) (int, error) {
	return 0, w.err
}

type growingSessionAudioWriter struct {
	mu           sync.Mutex
	data         bytes.Buffer
	firstWritten chan struct{}
	once         sync.Once
}

func (w *growingSessionAudioWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.data.Write(data)
	if w.data.Len() > 0 {
		w.once.Do(func() { close(w.firstWritten) })
	}
	return n, err
}

func (w *growingSessionAudioWriter) snapshot() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]byte(nil), w.data.Bytes()...)
}

type gatedSessionAudioInferencer struct {
	first   []byte
	second  []byte
	release <-chan struct{}
}

func (i *gatedSessionAudioInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	session := newScriptedSession()
	go func() {
		session.recv.Write(ctx, messages.StreamMessage{
			Type:  messages.StreamTypeSessionOpen,
			Value: messages.NewSessionOpenValue("gated-session", "session"),
		})
		session.recv.Write(ctx, messages.StreamMessage{
			Type:  messages.StreamTypeAudioDelta,
			Role:  messages.RoleAssistant,
			Value: messages.NewAudioDeltaValue(i.first),
		})
		select {
		case <-i.release:
		case <-ctx.Done():
			return
		}
		session.recv.Write(ctx, messages.StreamMessage{
			Type:  messages.StreamTypeAudioDelta,
			Role:  messages.RoleAssistant,
			Value: messages.NewAudioDeltaValue(i.second),
		})
		session.recv.Write(ctx, messages.StreamMessage{
			Type:  messages.StreamTypeMessageEnd,
			Role:  messages.RoleAssistant,
			Value: messages.NewMessageEndValue(messages.TokenUsage{}),
		})
	}()
	return session, nil
}
