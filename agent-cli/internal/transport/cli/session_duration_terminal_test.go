package cli

import servicetest "github.com/portpowered/go-agent-harness/agent-cli/internal/services/servicetest"

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

func TestSessionCommandMaxDurationMatrixPreservesPartialArtifacts(t *testing.T) {
	for _, waitForClose := range []bool{false, true} {
		t.Run(map[bool]string{false: "without-wait-for-close", true: "with-wait-for-close"}[waitForClose], func(t *testing.T) {
			artifactRoot := t.TempDir()
			recordPath := filepath.Join(artifactRoot, "cutoff.json")
			recordingDir := filepath.Join(artifactRoot, "recording")
			inferencer := newCLIDurationInferencer(cliDurationPartialEvents())
			root := newTestRootCommandWithProbeFleetCommand(NewProbeFleetCommand(nil, nil), inferencer)
			var stdout, stderr bytes.Buffer
			root.SetOut(&stdout)
			root.SetErr(&stderr)
			args := []string{
				"--config-dir", filepath.Join(artifactRoot, "config"),
				"session", "hold this session open",
				"--provider", config.ProviderOpenAI,
				"--model", servicetest.DefaultOpenAIRealtimeModel,
				"--api-key", "test-key",
				"--record", recordPath,
				"--record-dir", recordingDir,
				"--max-duration", "40ms",
			}
			if waitForClose {
				args = append(args, "--wait-for-close")
			}
			root.SetArgs(args)

			if err := root.Execute(); err != nil {
				t.Fatalf("session command: %v\nstdout=%q\nstderr=%q", err, stdout.String(), stderr.String())
			}
			assertSuccessfulDurationCommandOutput(t, stdout.String(), stderr.String())
			assertDurationSidecarArtifacts(t, recordPath, "accepted partial transcript", []int16{1, 2})
			assertDurationRecordingBundle(t, recordingDir, []byte{1, 0, 2, 0})
			capture, err := gwtesting.LoadSessionCapture(recordPath)
			if err != nil {
				t.Fatalf("load finalized session capture: %v", err)
			}
			if len(capture.Records) == 0 {
				t.Fatal("finalized session capture has no records")
			}
		})
	}
}

func TestSessionCommandMaxDurationRejectsInvalidPartialArtifact(t *testing.T) {
	artifactRoot := t.TempDir()
	recordPath := filepath.Join(artifactRoot, "invalid.json")
	recordingDir := filepath.Join(artifactRoot, "recording")
	root := newTestRootCommandWithProbeFleetCommand(NewProbeFleetCommand(nil, nil), newCLIDurationInferencer(cliDurationInvalidAudioEvents()))
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{
		"--config-dir", filepath.Join(artifactRoot, "config"),
		"session", "hold this session open",
		"--provider", config.ProviderOpenAI,
		"--model", servicetest.DefaultOpenAIRealtimeModel,
		"--api-key", "test-key",
		"--record", recordPath,
		"--record-dir", recordingDir,
		"--max-duration", "40ms",
	})

	err := root.Execute()
	if err == nil {
		t.Fatalf("invalid duration artifact returned success\nstdout=%q\nstderr=%q", stdout.String(), stderr.String())
	}
	if !errors.Is(err, codec.ErrPCM16OddLength) {
		t.Fatalf("invalid duration artifact error = %v, want odd PCM16 failure", err)
	}
	if strings.Count(stdout.String(), "[session terminal:") != 1 {
		t.Fatalf("invalid artifact terminal count = %d; stdout=%q", strings.Count(stdout.String(), "[session terminal:"), stdout.String())
	}
	if !strings.Contains(stdout.String(), "terminal_reason=terminal_failure") || strings.Contains(stdout.String(), "terminal_reason=max_duration") {
		t.Fatalf("invalid artifact terminal output = %q", stdout.String())
	}
}

func assertSuccessfulDurationCommandOutput(t *testing.T, stdout, stderr string) {
	t.Helper()
	if got := strings.Count(stdout, "[session terminal:"); got != 1 {
		t.Fatalf("terminal block count = %d, want 1; stdout=%q", got, stdout)
	}
	if got := strings.Count(stdout, "terminal_reason=max_duration"); got != 1 {
		t.Fatalf("max-duration reason count = %d, want 1; stdout=%q", got, stdout)
	}
	for _, want := range []string{
		"accepted partial transcript",
		"terminal_reason=max_duration",
		"output_state=partial",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout missing %q: %q", want, stdout)
		}
	}
	if stderr != "" {
		t.Fatalf("successful duration command wrote stderr: %q", stderr)
	}
	if strings.Contains(stdout, "terminal_reason=terminal_failure") || strings.Contains(stdout, "Usage:") {
		t.Fatalf("successful duration command emitted fatal/usage output: %q", stdout)
	}
}

func assertDurationSidecarArtifacts(t *testing.T, recordPath, wantText string, wantSamples []int16) {
	t.Helper()
	wavPath := strings.TrimSuffix(recordPath, filepath.Ext(recordPath)) + ".wav"
	transcriptPath := strings.TrimSuffix(recordPath, filepath.Ext(recordPath)) + ".jsonl"
	wavFile, err := os.Open(wavPath)
	if err != nil {
		t.Fatalf("open duration WAV: %v", err)
	}
	rate, samples, readErr := wavio.Read(wavFile)
	closeErr := wavFile.Close()
	if readErr != nil {
		t.Fatalf("read duration WAV: %v", readErr)
	}
	if closeErr != nil {
		t.Fatalf("close duration WAV: %v", closeErr)
	}
	if rate != wavio.Rate16kHz || !bytes.Equal(int16Bytes(samples), int16Bytes(wantSamples)) {
		t.Fatalf("duration WAV = rate %d samples %v, want rate %d samples %v", rate, samples, wavio.Rate16kHz, wantSamples)
	}

	data, err := os.ReadFile(transcriptPath)
	if err != nil {
		t.Fatalf("read duration transcript: %v", err)
	}
	sawText, sawMaxDuration := false, false
	sawTerminal := false
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		record, decodeErr := transcript.Decode(line)
		if decodeErr != nil {
			t.Fatalf("decode duration transcript record: %v", decodeErr)
		}
		var event struct {
			Type  messages.StreamMessageType `json:"type"`
			Value json.RawMessage            `json:"value"`
		}
		if err := json.Unmarshal(record.Payload, &event); err != nil {
			t.Fatalf("decode duration transcript payload: %v", err)
		}
		if event.Type == messages.StreamTypeTextDelta {
			var value messages.TextDeltaValue
			if err := json.Unmarshal(event.Value, &value); err != nil {
				t.Fatalf("decode duration text delta: %v", err)
			}
			sawText = sawText || value.Content == wantText
		}
		if event.Type == messages.StreamTypeSessionClose {
			sawTerminal = true
			var value messages.SessionCloseValue
			if err := json.Unmarshal(event.Value, &value); err != nil {
				t.Fatalf("decode duration session close: %v", err)
			}
			sawMaxDuration = value.TerminalReason == servicetest.SessionMaxDurationReason
		}
	}
	if !sawText || !sawMaxDuration {
		t.Fatalf("duration transcript omitted accepted output or terminal reason: %s", data)
	}
	if !sawTerminal {
		t.Fatal("duration transcript has no finalized session close")
	}
}

func assertDurationRecordingBundle(t *testing.T, recordingDir string, wantAudio []byte) {
	t.Helper()
	manifestData, err := os.ReadFile(filepath.Join(recordingDir, "manifest.json"))
	if err != nil {
		t.Fatalf("read recording manifest: %v", err)
	}
	var manifest transcript.RecordingManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatalf("decode recording manifest: %v", err)
	}
	if manifest.Terminal == nil || manifest.Terminal.TerminalReason != servicetest.SessionMaxDurationReason || manifest.Terminal.OutputState != messages.TerminalOutputPartial {
		t.Fatalf("recording terminal summary = %+v, want max_duration/partial", manifest.Terminal)
	}
	wantPaths := map[string]bool{
		"agent.transcript.jsonl":  false,
		"client.transcript.jsonl": false,
		"audio/out-000.pcm":       false,
	}
	for _, artifact := range manifest.Artifacts {
		if _, ok := wantPaths[artifact.Path]; ok {
			wantPaths[artifact.Path] = true
		}
		data, err := os.ReadFile(filepath.Join(recordingDir, filepath.FromSlash(artifact.Path)))
		if err != nil {
			t.Fatalf("read manifest artifact %q: %v", artifact.Path, err)
		}
		digest := sha256.Sum256(data)
		if got := hex.EncodeToString(digest[:]); got != artifact.SHA256 {
			t.Fatalf("manifest hash for %q = %s, want %s", artifact.Path, got, artifact.SHA256)
		}
	}
	for path, present := range wantPaths {
		if !present {
			t.Fatalf("recording manifest omitted %q: %+v", path, manifest.Artifacts)
		}
	}
	gotAudio, err := os.ReadFile(filepath.Join(recordingDir, "audio", "out-000.pcm"))
	if err != nil {
		t.Fatalf("read retained recording audio: %v", err)
	}
	if !bytes.Equal(gotAudio, wantAudio) {
		t.Fatalf("retained recording audio = %x, want %x", gotAudio, wantAudio)
	}
}

func int16Bytes(samples []int16) []byte {
	data := make([]byte, len(samples)*2)
	for index, sample := range samples {
		data[index*2] = byte(sample)
		data[index*2+1] = byte(sample >> 8)
	}
	return data
}

func cliDurationPartialEvents() []messages.StreamMessage {
	return []messages.StreamMessage{
		{Type: messages.StreamTypeSessionOpen, Value: messages.NewSessionOpenValue("duration-cli", "test")},
		{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()},
		{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("accepted partial transcript")},
		{Type: messages.StreamTypeAudioStart, Role: messages.RoleAssistant, Value: messages.NewAudioStartValue()},
		{Type: messages.StreamTypeAudioDelta, Role: messages.RoleAssistant, Value: messages.NewAudioDeltaValue([]byte{1, 0, 2, 0})},
		{Type: messages.StreamTypeTranscriptStart, Role: messages.RoleAssistant, Value: messages.NewTranscriptStartValue()},
		{Type: messages.StreamTypeTranscriptDelta, Role: messages.RoleAssistant, Value: messages.NewTranscriptDeltaValue("accepted partial transcript")},
	}
}

func cliDurationInvalidAudioEvents() []messages.StreamMessage {
	return []messages.StreamMessage{
		{Type: messages.StreamTypeSessionOpen, Value: messages.NewSessionOpenValue("duration-cli-invalid", "test")},
		{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()},
		{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("before invalid artifact")},
		{Type: messages.StreamTypeAudioDelta, Role: messages.RoleAssistant, Value: messages.NewAudioDeltaValue([]byte{1})},
	}
}

type cliDurationInferencer struct {
	events []messages.StreamMessage
}

func newCLIDurationInferencer(events []messages.StreamMessage) *cliDurationInferencer {
	return &cliDurationInferencer{events: events}
}

func (i *cliDurationInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	session := &cliDurationSession{
		receive: messages.NewTypedBuffer[messages.StreamMessage](64),
		done:    make(chan struct{}),
	}
	for _, event := range i.events {
		if !session.receive.Write(ctx, event) {
			return nil, ctx.Err()
		}
	}
	return session, nil
}

type cliDurationSession struct {
	receive *messages.TypedBuffer[messages.StreamMessage]
	done    chan struct{}
	once    sync.Once
}

func (s *cliDurationSession) Send(context.Context, messages.StreamMessage) bool { return true }

func (s *cliDurationSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.receive
}

func (s *cliDurationSession) Done() <-chan struct{} { return s.done }

func (s *cliDurationSession) Close() error {
	s.once.Do(func() { close(s.done) })
	return nil
}

var _ messages.SessionInferencer = (*cliDurationInferencer)(nil)
var _ messages.Session = (*cliDurationSession)(nil)
