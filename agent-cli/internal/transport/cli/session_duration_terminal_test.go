package cli

import servicetest "github.com/portpowered/go-agent-harness/agent-cli/internal/services/servicetest"

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

func TestSessionCommandMaxDurationMatrixPreservesPartialArtifacts(t *testing.T) {
	for _, waitForClose := range []bool{false, true} {
		t.Run(map[bool]string{false: "without-wait-for-close", true: "with-wait-for-close"}[waitForClose], func(t *testing.T) {
			artifactRoot := t.TempDir()
			recordPath := filepath.Join(artifactRoot, "cutoff.json")
			recordingDir := filepath.Join(artifactRoot, "recording")
			inferencer := newCLIDurationInferencer(cliDurationPartialEvents())
			root := newTestRootCommandWithProbeFleetCommand(NewProbeFleetCommand(nil, nil, newReplayRuntimeServiceForTest()), inferencer)
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
			assertDurationRecordingBundle(t, recordingDir)
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

// assertDurationRecordingBundle checks the live evidence bundle's terminal
// summary and hashed transcripts. A text-only live session has no playback
// device, so the bundle carries no rendered output audio.
func assertDurationRecordingBundle(t *testing.T, recordingDir string) {
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

func cliDurationCompleteEvents() []messages.StreamMessage {
	events := cliDurationPartialEvents()
	for index := range events {
		if events[index].Role == messages.RoleAssistant {
			events[index].ResponseID = "duration-cli-response"
		}
	}
	return append(events, messages.StreamMessage{
		Type:       messages.StreamTypeMessageEnd,
		Role:       messages.RoleAssistant,
		ResponseID: "duration-cli-response",
		Value:      messages.NewMessageEndValue(messages.TokenUsage{}),
	})
}

type cliDurationInferencer struct {
	events             []messages.StreamMessage
	waitForAudioCommit bool
	audioCommits       atomic.Int32
}

func newCLIDurationInferencer(events []messages.StreamMessage) *cliDurationInferencer {
	return &cliDurationInferencer{events: events}
}

func newCLIAudioInputSuccessInferencer(events []messages.StreamMessage) *cliDurationInferencer {
	return &cliDurationInferencer{events: events, waitForAudioCommit: true}
}

func (i *cliDurationInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	responseEvents := i.events
	initialEvents := append([]messages.StreamMessage(nil), i.events...)
	if i.waitForAudioCommit {
		initialEvents = nil
		if len(i.events) > 0 && i.events[0].Type == messages.StreamTypeSessionOpen {
			initialEvents = append(initialEvents, i.events[0])
			responseEvents = i.events[1:]
		}
	}
	session := &cliDurationSession{
		receive:            messages.NewTypedBuffer[messages.StreamMessage](64),
		done:               make(chan struct{}),
		responseEvents:     responseEvents,
		waitForAudioCommit: i.waitForAudioCommit,
		audioCommits:       &i.audioCommits,
	}
	for _, event := range initialEvents {
		if !session.receive.Write(ctx, event) {
			return nil, ctx.Err()
		}
	}
	return session, nil
}

type cliDurationSession struct {
	receive            *messages.TypedBuffer[messages.StreamMessage]
	done               chan struct{}
	responseEvents     []messages.StreamMessage
	waitForAudioCommit bool
	audioCommits       *atomic.Int32
	responseOnce       sync.Once
	once               sync.Once
}

func (s *cliDurationSession) Send(ctx context.Context, message messages.StreamMessage) bool {
	if message.Type == messages.StreamTypeMessageEnd && s.audioCommits != nil {
		s.audioCommits.Add(1)
	}
	if !s.waitForAudioCommit || message.Type != messages.StreamTypeMessageEnd {
		return true
	}
	succeeded := true
	s.responseOnce.Do(func() {
		for _, event := range s.responseEvents {
			if !s.receive.Write(ctx, event) {
				succeeded = false
				return
			}
		}
	})
	return succeeded
}

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
