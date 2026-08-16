package services

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
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

func TestRunSessionWithAudioOut_S14ReplayMatchesWAVGoldenAndEnergy(t *testing.T) {
	wantSamples := []int16{0, 1, -1, 32767, -32768, 1234, -2345}
	replayPath := filepath.Join(t.TempDir(), "s14-audio.session.json")
	writeSessionAudioReplayFixture(t, replayPath, []messages.StreamMessage{
		{Type: messages.StreamTypeSessionOpen, Value: messages.NewSessionOpenValue("s14", "replay")},
		{Type: messages.StreamTypeAudioDelta, Role: messages.RoleAssistant, Value: messages.NewAudioDeltaValue(pcm16Bytes(wantSamples[:3]))},
		{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("ignored")},
		{Type: messages.StreamTypeAudioDelta, Role: messages.RoleAssistant, Value: messages.NewAudioDeltaValue(pcm16Bytes(wantSamples[3:]))},
		{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
	})

	path := filepath.Join(t.TempDir(), "s14-response.wav")
	err := RunSessionWithAudioOut(context.Background(), io.Discard, SessionRunOptions{
		ReplayPath:        replayPath,
		SessionInferencer: gwtesting.NewReplaySessionInferencer(replayPath),
	}, path)
	if err != nil {
		t.Fatalf("S14 replay: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	goldenPath := sessionAudioWAVGoldenPath(t)
	wantGolden, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read committed WAV golden %s: %v", goldenPath, err)
	}
	if !bytes.Equal(data, wantGolden) {
		t.Fatalf("S14 WAV differs from committed golden %s", goldenPath)
	}

	rate, gotSamples, err := wavio.Read(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("read S14 WAV: %v", err)
	}
	if rate != audio.SampleRate || !equalInt16(gotSamples, wantSamples) {
		t.Fatalf("S14 WAV = rate %d samples %v, want rate %d samples %v", rate, gotSamples, audio.SampleRate, wantSamples)
	}
	rms := sessionAudioRMS(gotSamples)
	if rms <= audio.DefaultVADConfig.EnergyThreshold {
		t.Fatalf("S14 replay RMS = %.2f, want above VAD threshold %.2f", rms, audio.DefaultVADConfig.EnergyThreshold)
	}
}

func TestRunSessionWithAudioOut_PreservesNonFrameAlignedSplitDeltas(t *testing.T) {
	wantSamples := make([]int16, audio.FrameSize+7)
	for index := range wantSamples {
		wantSamples[index] = int16(index*17 - 4000)
	}
	inf := &scriptedSessionInferencer{events: []messages.StreamMessage{
		{Type: messages.StreamTypeAudioDelta, Role: messages.RoleAssistant, Value: messages.NewAudioDeltaValue(pcm16Bytes(wantSamples[:7]))},
		{Type: messages.StreamTypeAudioDelta, Role: messages.RoleAssistant, Value: messages.NewAudioDeltaValue(pcm16Bytes(wantSamples[7:]))},
		{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
	}}
	path := filepath.Join(t.TempDir(), "split-response.raw")

	if err := RunSessionWithAudioOut(context.Background(), io.Discard, SessionRunOptions{
		ReplayPath:        "synthetic.json",
		SessionInferencer: inf,
	}, path); err != nil {
		t.Fatalf("split-delta session: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, pcm16Bytes(wantSamples)) {
		t.Fatalf("split-delta raw output = %d bytes, want exact %d-byte PCM16 stream", len(data), len(wantSamples)*2)
	}
}

func TestRunSessionWithAudioOut_GrowsAndParsesRegularWAVBeforeCompletion(t *testing.T) {
	first := []int16{1200, 1201, 1202}
	second := sessionAudioFrame(-1400)
	release := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	path := filepath.Join(t.TempDir(), "streaming-response.wav")
	inf := &gatedSessionAudioInferencer{
		first:   pcm16Bytes(first),
		second:  pcm16Bytes(second),
		release: release,
	}
	errCh := make(chan error, 1)
	go func() {
		errCh <- RunSessionWithAudioOut(context.Background(), io.Discard, SessionRunOptions{
			ReplayPath:        "synthetic.json",
			SessionInferencer: inf,
		}, path)
	}()

	data := waitForSessionAudioWAVSamples(t, path, first)
	rate, samples, err := wavio.Read(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("read streaming WAV before session completion: %v", err)
	}
	if rate != audio.SampleRate || !equalInt16(samples, first) {
		t.Fatalf("streaming WAV before completion = rate %d samples %d, want first %d-sample delta", rate, len(samples), len(first))
	}

	close(release)
	released = true
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("streaming WAV session: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("streaming WAV session did not finish after release")
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	_, samples, err = wavio.Read(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("read finalized streaming WAV: %v", err)
	}
	if !equalInt16(samples, append(first, second...)) {
		t.Fatalf("final streaming WAV has %d samples, want exact ordered deltas", len(samples))
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

func TestRunSessionWithAudioOut_UnwritableFileFailsBeforeSessionConnect(t *testing.T) {
	path := filepath.Join(t.TempDir(), "readonly.wav")
	if err := os.WriteFile(path, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o444); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chmod(path, 0o600); err != nil {
			t.Errorf("restore writable mode: %v", err)
		}
	}()
	inf := &scriptedSessionInferencer{}

	err := RunSessionWithAudioOut(context.Background(), io.Discard, SessionRunOptions{
		ReplayPath:        "synthetic.json",
		SessionInferencer: inf,
	}, path)
	if err == nil || !strings.Contains(err.Error(), "--audio-out") || !strings.Contains(err.Error(), path) {
		t.Fatalf("unwritable target error = %v, want --audio-out and target path", err)
	}
	if inf.connected {
		t.Fatal("unwritable audio output target connected to the session")
	}
}

func TestRunSessionWithAudioOut_DoesNotTruncateWhenSessionOptionsAreInvalid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preserved.wav")
	want := []byte("do not truncate")
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatal(err)
	}
	inf := &scriptedSessionInferencer{}

	err := RunSessionWithAudioOut(context.Background(), io.Discard, SessionRunOptions{
		RecordPath:        "record.json",
		ReplayPath:        "replay.json",
		SessionInferencer: inf,
	}, path)
	if err == nil || !strings.Contains(err.Error(), "--record") || !strings.Contains(err.Error(), "--replay") {
		t.Fatalf("invalid session options error = %v, want both capture flags", err)
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("invalid session options changed output target to %q", got)
	}
	if inf.connected {
		t.Fatal("invalid session options connected to the session")
	}
}

func TestRunSessionWithAudioOut_GrowsBeforeSessionCompletes(t *testing.T) {
	first := []int16{1200, 1201, 1202}
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

func TestRunSessionWithAudioOut_FinalizesOnCleanInterrupt(t *testing.T) {
	first := sessionAudioFrame(800)
	release := make(chan struct{})
	path := filepath.Join(t.TempDir(), "interrupted-response.wav")
	inf := &gatedSessionAudioInferencer{
		first:   pcm16Bytes(first),
		second:  pcm16Bytes(sessionAudioFrame(-900)),
		release: release,
	}
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- RunSessionWithAudioOut(ctx, io.Discard, SessionRunOptions{
			ReplayPath:        "synthetic.json",
			SessionInferencer: inf,
		}, path)
	}()

	_ = waitForSessionAudioFileGrowth(t, path, sessionAudioWAVHeaderSize+len(first)*2)
	cancel()
	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("clean interrupt error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("clean interrupt did not finalize the session")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	_, samples, err := wavio.Read(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("read interrupted WAV: %v", err)
	}
	if !equalInt16(samples, first) {
		t.Fatalf("interrupted WAV samples = %d, want complete first delta", len(samples))
	}
}

func TestRunSessionWithAudioOut_FinalizesOnMaxDuration(t *testing.T) {
	first := sessionAudioFrame(1000)
	release := make(chan struct{})
	defer close(release)
	path := filepath.Join(t.TempDir(), "timed-response.wav")
	inf := &gatedSessionAudioInferencer{
		first:   pcm16Bytes(first),
		second:  pcm16Bytes(sessionAudioFrame(-1100)),
		release: release,
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- RunSessionWithAudioOutAndTextSeedAndMaxDuration(context.Background(), io.Discard, SessionRunOptions{
			ReplayPath:        "synthetic.json",
			SessionInferencer: inf,
		}, path, 50*time.Millisecond, SessionTextSeed{})
	}()

	_ = waitForSessionAudioFileGrowth(t, path, sessionAudioWAVHeaderSize+len(first)*2)
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("max-duration session: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("max-duration session did not finalize")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	_, samples, err := wavio.Read(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("read max-duration WAV: %v", err)
	}
	if !equalInt16(samples, first) {
		t.Fatalf("max-duration WAV samples = %d, want complete first delta", len(samples))
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

func sessionAudioRMS(samples []int16) float64 {
	if len(samples) == 0 {
		return 0
	}
	var sum float64
	for _, sample := range samples {
		value := float64(sample)
		sum += value * value
	}
	return math.Sqrt(sum / float64(len(samples)))
}

func sessionAudioWAVGoldenPath(t *testing.T) string {
	t.Helper()
	_, sourcePath, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(sourcePath), "..", "..", "..", "go-llm-gateway", "pkg", "wavio", "testdata", "pcm16-mono-16000.wav")
}

func writeSessionAudioReplayFixture(t *testing.T, path string, events []messages.StreamMessage) {
	t.Helper()
	records := make([]gwtesting.CapturedSessionEvent, len(events))
	for index, event := range events {
		payload, err := gwtesting.MarshalStreamMessage(event)
		if err != nil {
			t.Fatalf("marshal replay event %d: %v", index, err)
		}
		records[index] = gwtesting.CapturedSessionEvent{
			Sequence:    index + 1,
			Direction:   gwtesting.DirectionServerToClient,
			TimestampMs: int64(index),
			Type:        string(event.Type),
			PayloadType: gwtesting.SessionPayloadTypeStreamMessage,
			Payload:     payload,
		}
	}
	capture, err := json.Marshal(gwtesting.SessionCapture{
		Version:  gwtesting.SessionCaptureVersion,
		Provider: gwtesting.SessionProviderMetadata{Name: "synthetic", Model: "s14"},
		Session:  gwtesting.SessionMetadata{ID: "s14", FixtureProvenance: "synthetic"},
		Records:  records,
	})
	if err != nil {
		t.Fatalf("marshal replay fixture: %v", err)
	}
	if err := os.WriteFile(path, capture, 0o600); err != nil {
		t.Fatalf("write replay fixture: %v", err)
	}
}

func waitForSessionAudioFileGrowth(t *testing.T, path string, wantSize int) []byte {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		data, err := os.ReadFile(path)
		if err == nil && len(data) >= wantSize {
			return data
		}
		select {
		case <-deadline.C:
			t.Fatalf("audio output %q did not grow to %d bytes", path, wantSize)
		case <-ticker.C:
		}
	}
}

func waitForSessionAudioWAVSamples(t *testing.T, path string, want []int16) []byte {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		data, err := os.ReadFile(path)
		if err == nil && len(data) >= sessionAudioWAVHeaderSize+len(want)*2 {
			_, samples, readErr := wavio.Read(bytes.NewReader(data))
			if readErr == nil && equalInt16(samples, want) {
				return data
			}
		}
		select {
		case <-deadline.C:
			t.Fatalf("audio output %q did not expose %d parseable samples", path, len(want))
		case <-ticker.C:
		}
	}
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
