package agentruntime_test

import sessionclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"

import sessionservicewire "github.com/portpowered/go-agent-harness/agent-cli/internal/services/wire"

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	agentruntime "github.com/portpowered/go-agent-harness/agent-cli/internal/services/internal/agentruntime"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/transport/cli"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

func TestSessionCommandImageAndAudioInputUsesSingleTurn(t *testing.T) {
	dir := t.TempDir()
	imagePath := copySessionImageFixture(t, dir, "fixture.png")
	audioPath := filepath.Join(dir, "question.raw")
	samples := make([]int16, audio.FrameSize)
	samples[0] = 1200
	if err := os.WriteFile(audioPath, pcm16Bytes(samples), 0o600); err != nil {
		t.Fatalf("write question PCM: %v", err)
	}

	session := newRecordingSessionImageSession()
	var responseOnce sync.Once
	session.onEvent = func(ctx context.Context, event messages.StreamMessage) {
		if event.Type != messages.StreamTypeMessageEnd {
			return
		}
		responseOnce.Do(func() {
			session.recv.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()})
			session.recv.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("answer")})
			session.recv.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})})
			session.recv.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeSessionClose, Value: messages.NewSessionCloseValue("cli-image-audio", "done")})
		})
	}
	inferencer := &countingSessionImageInferencer{session: session}
	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = filepath.Join(dir, "config")
	command := cli.NewSessionCommand(flags.NewAskFlags(), globalFlags, newInjectedSessionService(sessionservicewire.SessionDependencies{Clock: sessionclock.Real{}, SessionInferencer: inferencer}), nil).Generate()
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	command.SetArgs([]string{
		"--record", filepath.Join(dir, "capture.json"),
		"--provider", "openai",
		"--model", "gpt-realtime",
		"--api-key", "test-key",
		"--image", imagePath,
		"--audio-in", audioPath,
	})
	if err := command.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute image-plus-audio command: %v", err)
	}

	if inferencer.connects != 1 || len(session.messages) != 1 {
		t.Fatalf("connects/messages = %d/%d, want one connected image turn", inferencer.connects, len(session.messages))
	}
	if len(session.messages[0].ContentParts) != 2 || !strings.Contains(session.messages[0].TextContent(), "attached image") {
		t.Fatalf("image turn content = %#v, want one deferred-image instruction and one image", session.messages[0].ContentParts)
	}
	audioDeltas, messageEnds := 0, 0
	for _, event := range session.events {
		switch event.Type {
		case messages.StreamTypeAudioDelta:
			audioDeltas++
		case messages.StreamTypeMessageEnd:
			messageEnds++
		}
	}
	if audioDeltas != 1 || messageEnds != 1 {
		t.Fatalf("CLI audio boundary events = audio:%d message_end:%d, want 1/1", audioDeltas, messageEnds)
	}
}

func TestSessionCommandImageAndScheduledAudioDirectoryWithoutRecord(t *testing.T) {
	dir := t.TempDir()
	imagePath := copySessionImageFixture(t, dir, "fixture.png")
	audioPath := filepath.Join(dir, "question.raw")
	samples := make([]int16, audio.FrameSize)
	samples[0] = 1200
	if err := os.WriteFile(audioPath, pcm16Bytes(samples), 0o600); err != nil {
		t.Fatalf("write question PCM: %v", err)
	}

	session := newRecordingSessionImageSession()
	session.onEvent = func(ctx context.Context, event messages.StreamMessage) {
		if event.Type != messages.StreamTypeMessageEnd {
			return
		}
		session.recv.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()})
		session.recv.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("scheduled answer")})
		session.recv.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})})
		session.recv.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeSessionClose, Value: messages.NewSessionCloseValue("cli-image-scheduled", "done")})
	}
	inferencer := &scheduledSessionImageInferencer{session: session}
	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = filepath.Join(dir, "config")
	command := cli.NewSessionCommand(flags.NewAskFlags(), globalFlags, newInjectedSessionService(sessionservicewire.SessionDependencies{Clock: sessionclock.Real{}, SessionInferencer: inferencer}), nil).Generate()
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	command.SetArgs([]string{
		"--record-dir", filepath.Join(dir, "recording"),
		"--provider", "openai",
		"--model", "gpt-realtime",
		"--api-key", "test-key",
		"--image", imagePath,
		"--audio-in-turn", audioPath,
	})
	if err := command.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute image-plus-scheduled-audio command: %v", err)
	}
	if inferencer.connects != 1 || len(session.messages) != 1 {
		t.Fatalf("connects/messages = %d/%d, want one connected image turn", inferencer.connects, len(session.messages))
	}
	if len(session.events) == 0 {
		t.Fatal("scheduled audio did not reach the scripted provider")
	}
	for _, name := range []string{"client.transcript.jsonl", "agent.transcript.jsonl", "manifest.json"} {
		data, err := os.ReadFile(filepath.Join(dir, "recording", name))
		if err != nil {
			t.Fatalf("read recording artifact %q: %v", name, err)
		}
		if len(data) == 0 {
			t.Fatalf("recording artifact %q is empty", name)
		}
	}
}

func TestRunSessionWithImagesAndAudioInputRequiresAssistantOutput(t *testing.T) {
	dir := t.TempDir()
	imagePath := copySessionImageFixture(t, dir, "fixture.png")
	audioPath := filepath.Join(dir, "question.raw")
	if err := os.WriteFile(audioPath, pcm16Bytes([]int16{1200}), 0o600); err != nil {
		t.Fatalf("write question PCM: %v", err)
	}

	err := agentruntime.RunSessionWithImagesAndAudioInput(context.Background(), io.Discard, agentruntime.SessionImageRunOptions{
		SessionRunOptions: agentruntime.SessionRunOptions{
			RecordPath:      filepath.Join(dir, "capture.json"),
			Provider:        "openai",
			Model:           "gpt-realtime-2.1-mini",
			APIKey:          "test-key",
			ConfigDir:       filepath.Join(dir, "config"),
			WebSocketDialer: newScriptedRealtimeServer(true),
		},
		ImagePaths:  []string{imagePath},
		MaxDuration: 100 * time.Millisecond,
	}, agentruntime.SessionAudioInput{Path: audioPath, Present: true})
	if err == nil || !errors.Is(err, agentruntime.ErrSessionAudioResponseIncomplete) {
		t.Fatalf("image-plus-audio run without assistant output error = %v, want ErrSessionAudioResponseIncomplete", err)
	}
}

func TestRunSessionWithImagesAndAudioInputUsesOneDeferredTurn(t *testing.T) {
	dir := t.TempDir()
	imagePath := copySessionImageFixture(t, dir, "fixture.png")
	audioPath := filepath.Join(dir, "question.wav")
	samples := make([]int16, audio.FrameSize)
	samples[0] = 1200
	var encoded bytes.Buffer
	if err := wavio.Write(&encoded, wavio.Rate16kHz, samples); err != nil {
		t.Fatalf("encode question WAV: %v", err)
	}
	if err := os.WriteFile(audioPath, encoded.Bytes(), 0o600); err != nil {
		t.Fatalf("write question WAV: %v", err)
	}

	server := newScriptedRealtimeServer(false)
	var output bytes.Buffer
	err := agentruntime.RunSessionWithImagesAndAudioInput(context.Background(), &output, agentruntime.SessionImageRunOptions{
		SessionRunOptions: agentruntime.SessionRunOptions{
			RecordPath:      filepath.Join(dir, "capture.json"),
			Provider:        "openai",
			Model:           "gpt-realtime-2.1-mini",
			APIKey:          "test-key",
			ConfigDir:       filepath.Join(dir, "config"),
			WebSocketDialer: server,
		},
		ImagePaths:   []string{imagePath},
		AudioOutPath: filepath.Join(dir, "response.wav"),
		MaxDuration:  5 * time.Second,
	}, agentruntime.SessionAudioInput{Path: audioPath, Present: true})
	if err != nil {
		t.Fatalf("image-plus-audio session: %v", err)
	}

	writes := server.writesSnapshot()
	imageIndex, appendIndex, commitIndex, responseIndex := -1, -1, -1, -1
	appends, commits, responses := 0, 0, 0
	for index, writeType := range writes {
		switch writeType {
		case "conversation.item.create":
			if imageIndex == -1 {
				imageIndex = index
			}
		case "input_audio_buffer.append":
			appends++
			if appendIndex == -1 {
				appendIndex = index
			}
		case "input_audio_buffer.commit":
			commits++
			if commitIndex == -1 {
				commitIndex = index
			}
		case "response.create":
			responses++
			if responseIndex == -1 {
				responseIndex = index
			}
		}
	}
	if imageIndex < 0 || appendIndex < 0 || commitIndex < 0 || responseIndex < 0 || !(imageIndex < appendIndex && appendIndex < commitIndex && commitIndex < responseIndex) {
		t.Fatalf("combined turn wire order = %v, want image, append, commit, response.create", writes)
	}
	if appends == 0 || commits != 1 || responses != 1 {
		t.Fatalf("combined turn counts = appends:%d commits:%d responses:%d, want nonzero/1/1", appends, commits, responses)
	}
	if !strings.Contains(strings.Join(writes, "\n"), `IN:{"type":"response.done"`) {
		t.Fatalf("wire trace = %v, want terminal response.done", writes)
	}
	if info, err := os.Stat(filepath.Join(dir, "capture.json")); err != nil {
		t.Fatalf("recorded capture: %v", err)
	} else if info.Size() == 0 {
		t.Fatal("recorded capture is empty")
	}
	if info, err := os.Stat(filepath.Join(dir, "response.wav")); err != nil {
		t.Fatalf("assistant audio output: %v", err)
	} else if info.Size() <= 44 {
		t.Fatalf("assistant audio output size = %d, want WAV header plus audio", info.Size())
	}
}

func TestRunSessionWithImagesAndScheduledAudioDirectoryWithoutRecord(t *testing.T) {
	dir := t.TempDir()
	imagePath := copySessionImageFixture(t, dir, "fixture.png")
	audioPath := filepath.Join(dir, "question.raw")
	samples := make([]int16, audio.FrameSize)
	samples[0] = 1200
	if err := os.WriteFile(audioPath, pcm16Bytes(samples), 0o600); err != nil {
		t.Fatalf("write question PCM: %v", err)
	}

	destination := filepath.Join(dir, "recording")
	server := newScheduledAudioLifecycleServer()
	err := agentruntime.RunSessionWithImagesAndRecordingDirectoryAndAudioFilesAndOutputAndTextSeedAndMaxDuration(
		context.Background(),
		io.Discard,
		agentruntime.SessionImageRunOptions{
			SessionRunOptions: agentruntime.SessionRunOptions{
				Provider:        "openai",
				Model:           "gpt-realtime-2.1-mini",
				APIKey:          "test-key",
				ConfigDir:       filepath.Join(dir, "config"),
				WebSocketDialer: server,
			},
			ImagePaths: []string{imagePath},
		},
		destination,
		"",
		5*time.Second,
		agentruntime.SessionTextSeed{},
		[]string{audioPath},
		"",
	)
	if err != nil {
		t.Fatalf("image-plus-scheduled-audio directory run: %v", err)
	}

	manifestBytes, err := os.ReadFile(filepath.Join(destination, "manifest.json"))
	if err != nil {
		t.Fatalf("read recording manifest: %v", err)
	}
	var manifest transcript.RecordingManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatalf("decode recording manifest: %v", err)
	}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("validate recording manifest: %v", err)
	}
	if len(manifest.Artifacts) == 0 {
		t.Fatalf("recording manifest = %+v, want artifacts", manifest)
	}
	if writes := server.writesSnapshot(); countSessionWireType(writes, "conversation.item.create") != 1 || countSessionWireType(writes, "response.create") != 1 {
		t.Fatalf("scheduled image/audio wire = %v, want one image item and one response", writes)
	}
	for _, path := range []string{"audio/in-000.pcm", "audio/out-000.pcm"} {
		info, statErr := os.Stat(filepath.Join(destination, filepath.FromSlash(path)))
		if statErr != nil || info.Size() == 0 {
			t.Fatalf("recorded audio %q = %v, stat error %v; want non-empty artifact", path, info, statErr)
		}
	}
}

func countSessionWireType(writes []string, want string) int {
	count := 0
	for _, write := range writes {
		if write == want {
			count++
		}
	}
	return count
}

type scheduledSessionImageInferencer struct {
	connects int
	session  *recordingSessionImageSession
}

func (i *scheduledSessionImageInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	i.connects++
	if !i.session.recv.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeSessionOpen, Value: messages.NewSessionOpenValue("cli-image-scheduled", "gpt-realtime")}) {
		return nil, ctx.Err()
	}
	if !i.session.recv.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeSessionUpdated, Value: messages.NewSessionUpdatedValue("cli-image-scheduled")}) {
		return nil, ctx.Err()
	}
	return i.session, nil
}
