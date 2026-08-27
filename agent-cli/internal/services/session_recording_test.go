package services

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

func TestSessionDirectoryRecordingCapturesBothPerspectivesAndExactPCM(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "nested", "capture")
	plan := sessionRuntimePlan{provider: sessionProviderOpenAI}
	recording := newSessionDirectoryRecording(destination, plan, SessionRunOptions{Model: "gpt-realtime"})
	recording.metadata.InputDevice = transcript.DeviceMetadata{
		ID: "input-test", Name: "Deterministic microphone", Driver: "test", SampleRateHz: 16000, Channels: 1,
	}
	recording.metadata.OutputDevice = transcript.DeviceMetadata{
		ID: "output-test", Name: "Deterministic speaker", Driver: "test", SampleRateHz: 16000, Channels: 1,
	}
	inner := newSessionRecordingTestSession()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wrapper := newSessionDirectoryRecordingSession(ctx, inner, recording)

	inputSegments := [][]byte{{0x01, 0x00, 0xff, 0x7f}, {0x02, 0x00}}
	outputSegments := [][]byte{{0x10, 0x00}, {0x11, 0x00, 0x12, 0x00}}
	inputMessages := []messages.StreamMessage{
		{
			Type:               messages.StreamTypeAudioDelta,
			Role:               messages.RoleUser,
			GlobalIndex:        11,
			ActorProvidedID:    "client-input-0",
			ActorProvidedIndex: 3,
			ActorStreamID:      "client-audio",
			ActorID:            messages.User,
			LoopPassID:         7,
			Value:              messages.NewAudioDeltaValue(inputSegments[0]),
		},
		{
			Type:               messages.StreamTypeAudioDelta,
			Role:               messages.RoleUser,
			GlobalIndex:        12,
			ActorProvidedID:    "client-input-1",
			ActorProvidedIndex: 4,
			ActorStreamID:      "client-audio",
			ActorID:            messages.User,
			LoopPassID:         7,
			Value:              messages.NewAudioDeltaValue(inputSegments[1]),
		},
	}
	outputMessages := []messages.StreamMessage{
		{
			Type:               messages.StreamTypeAudioDelta,
			Role:               messages.RoleAssistant,
			GlobalIndex:        21,
			ActorProvidedID:    "agent-output-0",
			ActorProvidedIndex: 8,
			ActorStreamID:      "agent-audio",
			ActorID:            messages.Model,
			LoopPassID:         9,
			Value:              messages.NewAudioDeltaValue(outputSegments[0]),
		},
		{
			Type:               messages.StreamTypeAudioDelta,
			Role:               messages.RoleAssistant,
			GlobalIndex:        22,
			ActorProvidedID:    "agent-output-1",
			ActorProvidedIndex: 9,
			ActorStreamID:      "agent-audio",
			ActorID:            messages.Model,
			LoopPassID:         9,
			Value:              messages.NewAudioDeltaValue(outputSegments[1]),
		},
	}
	for _, msg := range inputMessages {
		if !wrapper.Send(ctx, msg) {
			t.Fatal("recording wrapper rejected input audio")
		}
	}
	for _, msg := range outputMessages {
		if !inner.receive.Write(ctx, msg) {
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
		"session-log.jsonl",
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
		for index, record := range records {
			wantMessage := append(append([]messages.StreamMessage{}, inputMessages...), outputMessages...)[index]
			wantPayload, err := gwtesting.MarshalStreamMessage(wantMessage)
			if err != nil {
				t.Fatalf("marshal expected %s frame %d: %v", side, index, err)
			}
			if !bytes.Equal(record.Payload, wantPayload) {
				t.Fatalf("%s payload %d = %s, want %s", side, index, record.Payload, wantPayload)
			}
			wantPeer, wantDirection := transcript.PeerClient, transcript.DirectionOut
			if side == "agent.transcript.jsonl" {
				wantPeer, wantDirection = transcript.PeerAgent, transcript.DirectionIn
			}
			if index >= len(inputMessages) {
				wantPeer, wantDirection = transcript.PeerClient, transcript.DirectionIn
				if side == "agent.transcript.jsonl" {
					wantPeer, wantDirection = transcript.PeerAgent, transcript.DirectionOut
				}
			}
			wantTimestamp := sessionRecordingClockBase.Add(time.Duration(index+1) * time.Nanosecond).Format(time.RFC3339Nano)
			if record.Version != transcript.FormatVersion || record.Tick != uint64(index+1) || record.Timestamp != wantTimestamp || record.Peer != wantPeer || record.Direction != wantDirection || record.Stream != transcript.StreamWebSocket {
				t.Fatalf("%s record %d = %+v, want tick=%d timestamp=%s peer=%s direction=%s stream=%s", side, index, record, index+1, wantTimestamp, wantPeer, wantDirection, transcript.StreamWebSocket)
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
	if manifest.Transport != "websocket" || manifest.Model != "gpt-realtime" || manifest.ClockBase != sessionRecordingClockBase.Format(time.RFC3339Nano) {
		t.Fatalf("manifest metadata = %+v", manifest)
	}
	if !reflect.DeepEqual(manifest.InputDevice, recording.metadata.InputDevice) || !reflect.DeepEqual(manifest.OutputDevice, recording.metadata.OutputDevice) {
		t.Fatalf("manifest devices = input:%+v output:%+v, want input:%+v output:%+v", manifest.InputDevice, manifest.OutputDevice, recording.metadata.InputDevice, recording.metadata.OutputDevice)
	}
	if len(manifest.Artifacts) != 7 {
		t.Fatalf("manifest artifacts = %d, want 7", len(manifest.Artifacts))
	}
	wantArtifacts := []string{
		"client.transcript.jsonl",
		"agent.transcript.jsonl",
		"session-log.jsonl",
		"audio/in-000.pcm",
		"audio/in-001.pcm",
		"audio/out-000.pcm",
		"audio/out-001.pcm",
	}
	seenArtifacts := make(map[string]bool, len(manifest.Artifacts))
	for _, artifact := range manifest.Artifacts {
		if seenArtifacts[artifact.Path] {
			t.Fatalf("manifest references artifact %q more than once", artifact.Path)
		}
		seenArtifacts[artifact.Path] = true
		data, err := os.ReadFile(filepath.Join(destination, filepath.FromSlash(artifact.Path)))
		if err != nil {
			t.Fatalf("read artifact %s: %v", artifact.Path, err)
		}
		digest := sha256.Sum256(data)
		if got := hex.EncodeToString(digest[:]); got != artifact.SHA256 {
			t.Fatalf("hash for %s = %s, want %s", artifact.Path, got, artifact.SHA256)
		}
	}
	gotArtifacts := make([]string, 0, len(seenArtifacts))
	for path := range seenArtifacts {
		gotArtifacts = append(gotArtifacts, path)
	}
	sort.Strings(gotArtifacts)
	sort.Strings(wantArtifacts)
	if !reflect.DeepEqual(gotArtifacts, wantArtifacts) {
		t.Fatalf("manifest artifact paths = %v, want %v", gotArtifacts, wantArtifacts)
	}
}

func TestSessionDirectoryRecordingPersistsOneAuthoritativeTerminalSummary(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "terminal-summary")
	recording := newSessionDirectoryRecording(destination, sessionRuntimePlan{provider: sessionProviderOpenAI}, SessionRunOptions{Model: "gpt-realtime"})
	recording.client.WriteString("client\n")
	recording.agent.WriteString("agent\n")
	want := transcript.RecordingTerminalSummary{
		Reason:             "max_duration",
		Classification:     "max_duration",
		TerminalReason:     messages.TerminalReason("max_duration"),
		TerminalProvenance: messages.TerminalProvenanceLoop,
		OutputState:        messages.TerminalOutputPartial,
	}
	if err := recording.RecordTerminalSummary(want); err != nil {
		t.Fatalf("record terminal summary: %v", err)
	}
	if err := recording.RecordTerminalSummary(want); err != nil {
		t.Fatalf("repeat identical terminal summary: %v", err)
	}
	if recording.terminal == nil || *recording.terminal != want {
		t.Fatalf("recording terminal summary = %+v, want %+v", recording.terminal, want)
	}
	if err := recording.Finalize(); err != nil {
		t.Fatalf("finalize recording: %v", err)
	}

	manifestBytes, err := os.ReadFile(filepath.Join(destination, "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var manifest transcript.RecordingManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	if manifest.Terminal == nil || *manifest.Terminal != want {
		t.Fatalf("manifest terminal summary = %+v, want %+v", manifest.Terminal, want)
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

func TestRunSessionWithRecordingDirectoryPreservesProviderErrorOverEmptyRecording(t *testing.T) {
	authErr := errors.New("openai realtime authentication failed")
	destination := filepath.Join(t.TempDir(), "auth-failure")
	err := RunSessionWithRecordingDirectory(context.Background(), io.Discard, SessionRunOptions{
		Provider:          config.ProviderOpenAI,
		Model:             "gpt-realtime",
		APIKey:            "invalid-test-key",
		ConfigDir:         t.TempDir(),
		SessionInferencer: &failingSessionRecordingInferencer{err: authErr},
	}, destination)
	if !errors.Is(err, authErr) {
		t.Fatalf("error = %v, want provider authentication error", err)
	}
	if errors.Is(err, transcript.ErrInvalidRecording) || strings.Contains(err.Error(), "at least one segment is required") {
		t.Fatalf("empty recording validation masked provider error: %v", err)
	}
}

func TestRunSessionWithRecordingDirectoryUsesProductionAudioInput(t *testing.T) {
	inputPath := sessionRecordingAudioFixturePath(t, "utterance.wav")
	destination := filepath.Join(t.TempDir(), "nested", "capture")
	inferencer := newSessionRecordingRunnerInferencerAfterAudioEnd([]messages.StreamMessage{
		{Type: messages.StreamTypeAudioStart, Role: messages.RoleAssistant, Value: messages.NewAudioStartValue()},
		{Type: messages.StreamTypeAudioDelta, Role: messages.RoleAssistant, Value: messages.NewAudioDeltaValue([]byte{0x10, 0x00, 0x11, 0x00})},
		{Type: messages.StreamTypeAudioEnd, Role: messages.RoleAssistant, Value: messages.NewAudioEndValue()},
		{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()},
		{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("recorded response")},
		{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
	})

	err := RunSessionWithRecordingDirectoryAndInstructionsAndAudioInputAndOutputAndTextSeedAndMaxDuration(
		context.Background(),
		io.Discard,
		SessionRunOptions{
			Provider:          config.ProviderOpenAI,
			Model:             "gpt-realtime",
			APIKey:            "test-key",
			ConfigDir:         t.TempDir(),
			SessionInferencer: inferencer,
		},
		destination,
		"",
		0,
		SessionTextSeed{},
		SessionAudioInput{Path: inputPath, Present: true},
		"",
	)
	if err != nil {
		t.Fatalf("recorded audio-input replay: %v", err)
	}

	inputEntries, err := os.ReadDir(filepath.Join(destination, "audio"))
	if err != nil {
		t.Fatalf("read recorded audio directory: %v", err)
	}
	if len(inputEntries) == 0 {
		t.Fatal("recorded audio directory is empty; production input did not reach the recorder")
	}
	var inputCount int
	for _, entry := range inputEntries {
		if !strings.HasPrefix(entry.Name(), "in-") {
			continue
		}
		inputCount++
		data, readErr := os.ReadFile(filepath.Join(destination, "audio", entry.Name()))
		if readErr != nil {
			t.Fatalf("read recorded input %s: %v", entry.Name(), readErr)
		}
		if len(data) == 0 {
			t.Fatalf("recorded input %s is empty", entry.Name())
		}
	}
	if inputCount == 0 {
		t.Fatal("recorded audio directory has no input segments")
	}

	clientRecords := readSessionRecordingTranscript(t, filepath.Join(destination, "client.transcript.jsonl"))
	agentRecords := readSessionRecordingTranscript(t, filepath.Join(destination, "agent.transcript.jsonl"))
	if len(clientRecords) == 0 || len(agentRecords) == 0 {
		t.Fatalf("transcript records = client %d, agent %d; want both perspectives", len(clientRecords), len(agentRecords))
	}
	var clientAudio bool
	for _, record := range clientRecords {
		msg, unmarshalErr := gwtesting.UnmarshalStreamMessage(record.Payload)
		if unmarshalErr != nil {
			t.Fatalf("decode recorded client frame: %v", unmarshalErr)
		}
		if audio, ok := msg.Value.(*messages.AudioDeltaValue); ok && len(audio.Content) > 0 {
			clientAudio = true
			break
		}
	}
	if !clientAudio {
		t.Fatal("recorded client transcript has no non-empty audio input frame")
	}
}

func TestRunSessionWithRecordingDirectoryAudioFilesKeepsOnePersistentConversation(t *testing.T) {
	audioPaths := make([]string, 0, 3)
	for index := 0; index < 3; index++ {
		pcm := make([]byte, audio.FrameSize*2)
		for byteIndex := range pcm {
			pcm[byteIndex] = byte((index+1)*31 + byteIndex%127)
		}
		audioPaths = append(audioPaths, writeSessionRecordingInputFixture(t, [][]byte{pcm}))
	}

	assistantAudio := []byte{0x20, 0x00, 0x21, 0x00}
	events := []messages.StreamMessage{
		{Type: messages.StreamTypeTranscriptDelta, Role: messages.RoleUser, Value: messages.NewTranscriptDeltaValue("first ")},
		{Type: messages.StreamTypeTranscriptEnd, Role: messages.RoleUser, Value: messages.NewTranscriptEndValue("first utterance")},
		{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()},
		{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("first reply")},
		{Type: messages.StreamTypeAudioDelta, Role: messages.RoleAssistant, Value: messages.NewAudioDeltaValue(assistantAudio)},
		{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
		{Type: messages.StreamTypeTranscriptDelta, Role: messages.RoleUser, Value: messages.NewTranscriptDeltaValue("second ")},
		{Type: messages.StreamTypeTranscriptEnd, Role: messages.RoleUser, Value: messages.NewTranscriptEndValue("second utterance")},
		{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()},
		{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("second reply")},
		{Type: messages.StreamTypeAudioDelta, Role: messages.RoleAssistant, Value: messages.NewAudioDeltaValue(assistantAudio)},
		{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
		{Type: messages.StreamTypeTranscriptDelta, Role: messages.RoleUser, Value: messages.NewTranscriptDeltaValue("third ")},
		{Type: messages.StreamTypeTranscriptEnd, Role: messages.RoleUser, Value: messages.NewTranscriptEndValue("third utterance")},
		{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()},
		{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("third reply")},
		{Type: messages.StreamTypeAudioDelta, Role: messages.RoleAssistant, Value: messages.NewAudioDeltaValue(assistantAudio)},
		{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
		{Type: messages.StreamTypeSessionClose, Value: messages.NewSessionCloseValue("persistent-session", "done")},
	}
	inferencer := newPersistentSessionRecordingInferencer([][]messages.StreamMessage{
		events[0:6],
		events[6:12],
		events[12:18],
	})
	destination := filepath.Join(t.TempDir(), "persistent", "capture")
	err := RunSessionWithRecordingDirectoryAndInstructionsAndAudioFilesAndOutputAndTextSeedAndMaxDuration(
		context.Background(),
		io.Discard,
		SessionRunOptions{
			Provider:          config.ProviderOpenAI,
			Model:             "gpt-realtime",
			APIKey:            "test-key",
			ConfigDir:         t.TempDir(),
			SessionInferencer: inferencer,
		},
		destination,
		"",
		0,
		SessionTextSeed{},
		audioPaths,
		"",
	)
	if err != nil {
		t.Fatalf("persistent audio-file session: %v", err)
	}
	if inferencer.connects != 1 {
		t.Fatalf("connects = %d, want one persistent session", inferencer.connects)
	}

	logBytes, err := os.ReadFile(filepath.Join(destination, "session-log.jsonl"))
	if err != nil {
		t.Fatalf("read persistent session log: %v", err)
	}
	var entries []sessionConversationLogEntry
	for _, line := range bytes.Split(bytes.TrimSpace(logBytes), []byte("\n")) {
		var entry sessionConversationLogEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			t.Fatalf("decode persistent session log line: %v", err)
		}
		entries = append(entries, entry)
	}
	if len(entries) != len(audioPaths) {
		t.Fatalf("session log entries = %d, want %d in one bundle", len(entries), len(audioPaths))
	}
	wantInputs := []string{"first utterance", "second utterance", "third utterance"}
	wantReplies := []string{"first reply", "second reply", "third reply"}
	for index, entry := range entries {
		if entry.TurnIndex != index+1 || entry.Input.Text != wantInputs[index] || entry.Response.Text != wantReplies[index] {
			t.Errorf("entry %d = %+v, want turn=%d input=%q response=%q", index, entry, index+1, wantInputs[index], wantReplies[index])
		}
		if !entry.Input.Committed || entry.Input.AudioBytes == 0 || !entry.Response.Complete {
			t.Errorf("entry %d completeness = %+v, want committed audio and complete response", index+1, entry)
		}
	}
}

func TestRunSessionWithImagesAndRecordingDirectoryPreservesImageTurn(t *testing.T) {
	dir := t.TempDir()
	imagePath := filepath.Join(dir, "fixture.png")
	var imageData bytes.Buffer
	if err := png.Encode(&imageData, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatalf("encode image fixture: %v", err)
	}
	if err := os.WriteFile(imagePath, imageData.Bytes(), 0o600); err != nil {
		t.Fatalf("write image fixture: %v", err)
	}
	inputSegments := sessionRecordingInputSegments()
	inputPath := writeSessionRecordingInputFixture(t, inputSegments)

	inferencer := newSessionRecordingRunnerInferencer([]messages.StreamMessage{
		{Type: messages.StreamTypeAudioStart, Role: messages.RoleAssistant, Value: messages.NewAudioStartValue()},
		{Type: messages.StreamTypeAudioDelta, Role: messages.RoleAssistant, Value: messages.NewAudioDeltaValue([]byte{0x10, 0x00, 0x11, 0x00})},
		{Type: messages.StreamTypeAudioEnd, Role: messages.RoleAssistant, Value: messages.NewAudioEndValue()},
		{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()},
		{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("image response")},
		{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
		{Type: messages.StreamTypeSessionClose, Value: messages.NewSessionCloseValue("image-session", "done")},
	}, false, true)
	destination := filepath.Join(dir, "nested", "image-recording")
	err := RunSessionWithImagesAndRecordingDirectoryAndAudioInput(context.Background(), io.Discard, SessionImageRunOptions{
		SessionRunOptions: SessionRunOptions{
			Provider:          config.ProviderOpenAI,
			Model:             "gpt-realtime",
			APIKey:            "test-key",
			ConfigDir:         filepath.Join(dir, "config"),
			Prompt:            "describe the image",
			SessionInferencer: inferencer,
		},
		ImagePaths: []string{imagePath},
	}, destination, SessionAudioInput{Path: inputPath, Present: true})
	if err != nil {
		t.Fatalf("image recording run: %v", err)
	}
	if inferencer.connects != 1 || len(inferencer.sessions) != 1 {
		t.Fatalf("connects/sessions = %d/%d, want one each", inferencer.connects, len(inferencer.sessions))
	}
	imageMessages := inferencer.sessions[0].imageMessagesCopy()
	if len(imageMessages) != 1 {
		t.Fatalf("provider image messages = %d, want one", len(imageMessages))
	}
	if requests := inferencer.sessions[0].imageResponseRequestsCopy(); len(requests) != 1 || requests[0] {
		t.Fatalf("audio-enabled image turn response requests = %v, want one item-only request", requests)
	}
	if imageMessages[0].TextContent() != "describe the image" || len(imageMessages[0].ContentParts) != 2 {
		t.Fatalf("provider image message = %#v, want text plus image", imageMessages[0])
	}
	part, ok := imageMessages[0].ContentParts[1].(messages.ImagePart)
	if !ok || !bytes.Equal(part.Bytes, imageData.Bytes()) || part.MediaType != "image/png" {
		t.Fatalf("provider image part = %#v, want fixture bytes and image/png", imageMessages[0].ContentParts[1])
	}
	for _, name := range []string{"client.transcript.jsonl", "agent.transcript.jsonl", "manifest.json"} {
		data, readErr := os.ReadFile(filepath.Join(destination, name))
		if readErr != nil {
			t.Fatalf("read %s: %v", name, readErr)
		}
		if len(data) == 0 {
			t.Fatalf("recording artifact %s is empty", name)
		}
	}
}

func sessionRecordingAudioFixturePath(t *testing.T, name string) string {
	t.Helper()
	_, sourcePath, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(sourcePath), "testdata", "session-audio-input", name)
}

func sessionRecordingInputSegments() [][]byte {
	segments := make([][]byte, 2)
	for index := range segments {
		segment := make([]byte, audio.FrameSize*2)
		for offset := range segment {
			segment[offset] = byte((offset+index*17)%251 + 1)
		}
		segments[index] = segment
	}
	return segments
}

func writeSessionRecordingInputFixture(t *testing.T, segments [][]byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "input.pcm")
	if err := os.WriteFile(path, bytes.Join(segments, nil), 0o600); err != nil {
		t.Fatalf("write session audio input fixture: %v", err)
	}
	return path
}

func TestRunSessionWithRecordingDirectoryUsesRunnerAndPreservesPairedOutput(t *testing.T) {
	inputSegments := sessionRecordingInputSegments()
	inputPath := writeSessionRecordingInputFixture(t, inputSegments)
	outputSegments := [][]byte{{0x10, 0x00}, {0x11, 0x00, 0x12, 0x00}}
	events := []messages.StreamMessage{
		{Type: messages.StreamTypeSessionCreated, Value: messages.NewSessionCreatedValue("runner-session", "gpt-realtime")},
		{Type: messages.StreamTypeAudioStart, Role: messages.RoleAssistant, Value: messages.NewAudioStartValue()},
		{Type: messages.StreamTypeAudioDelta, Role: messages.RoleAssistant, Value: messages.NewAudioDeltaValue(outputSegments[0])},
		{Type: messages.StreamTypeAudioDelta, Role: messages.RoleAssistant, Value: messages.NewAudioDeltaValue(outputSegments[1])},
		{Type: messages.StreamTypeAudioEnd, Role: messages.RoleAssistant, Value: messages.NewAudioEndValue()},
		{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()},
		{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("paired runner response")},
		{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
	}

	plainInferencer := newSessionRecordingRunnerInferencerAfterAudioEnd(events)
	var plainOutput bytes.Buffer
	plainPlan, err := planSessionRuntime(SessionRunOptions{
		Provider:          config.ProviderOpenAI,
		Model:             "gpt-realtime",
		APIKey:            "test-key",
		ConfigDir:         t.TempDir(),
		Prompt:            "",
		SessionInferencer: plainInferencer,
	})
	if err != nil {
		t.Fatalf("plan unrecorded session: %v", err)
	}
	plainSource, err := openSessionAudioInput(SessionAudioInput{Path: inputPath, Present: true})
	if err != nil {
		t.Fatalf("open unrecorded audio input: %v", err)
	}
	defer plainSource.Close()
	plainPlan.loop.CloseAfterOpen = false
	plainPlan.loop.AudioIn = plainSource
	if err := plainPlan.run(context.Background(), &plainOutput); err != nil {
		t.Fatalf("run unrecorded session: %v", err)
	}

	destination := filepath.Join(t.TempDir(), "missing", "parents", "capture")
	recordedInferencer := newSessionRecordingRunnerInferencerAfterAudioEnd(events)
	var recordedOutput bytes.Buffer
	if err := RunSessionWithRecordingDirectoryAndInstructionsAndAudioInputAndOutputAndTextSeedAndMaxDuration(context.Background(), &recordedOutput, SessionRunOptions{
		Provider:          config.ProviderOpenAI,
		Model:             "gpt-realtime",
		APIKey:            "test-key",
		ConfigDir:         t.TempDir(),
		Prompt:            "paired prompt",
		SessionInferencer: recordedInferencer,
	}, destination, "", 0, SessionTextSeed{}, SessionAudioInput{Path: inputPath, Present: true}, ""); err != nil {
		t.Fatalf("run recorded session: %v", err)
	}
	if recordedOutput.String() != plainOutput.String() {
		t.Fatalf("recorded output = %q, unrecorded output = %q", recordedOutput.String(), plainOutput.String())
	}
	if recordedInferencer.connects != 1 || plainInferencer.connects != 1 {
		t.Fatalf("connect counts = recorded:%d unrecorded:%d, want one each", recordedInferencer.connects, plainInferencer.connects)
	}

	wantEntries := []string{
		"agent.transcript.jsonl",
		"audio",
		"audio/in-000.pcm",
		"audio/in-001.pcm",
		"audio/out-000.pcm",
		"audio/out-001.pcm",
		"client.transcript.jsonl",
		"manifest.json",
		"session-log.jsonl",
	}
	if got := recordingEntries(t, destination); !reflect.DeepEqual(got, wantEntries) {
		t.Fatalf("runner recording entries = %v, want %v", got, wantEntries)
	}
	for index, want := range inputSegments {
		path := filepath.Join(destination, "audio", "in-"+threeRecordingDigits(index)+".pcm")
		if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, want) {
			t.Fatalf("runner input segment %s = %x, err %v, want %x", path, got, err, want)
		}
	}
	for index, want := range outputSegments {
		path := filepath.Join(destination, "audio", "out-"+threeRecordingDigits(index)+".pcm")
		if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, want) {
			t.Fatalf("runner output segment %s = %x, err %v, want %x", path, got, err, want)
		}
	}

	recordedSession := recordedInferencer.sessions[0]
	sent := recordedSession.sentMessagesCopy()
	observedSent := sent
	if len(observedSent) > 0 && observedSent[0].Type == messages.StreamTypeSessionUpdate {
		// The injected session's initial provider configuration is sent while
		// ConnectSession is still establishing the inner session, before the
		// directory observer can wrap it.
		observedSent = observedSent[1:]
	}
	server := append([]messages.StreamMessage{{
		Type:  messages.StreamTypeSessionOpen,
		Value: messages.NewSessionOpenValue("runner-session", "session"),
	}}, events...)
	for _, side := range []string{"client.transcript.jsonl", "agent.transcript.jsonl"} {
		records := readSessionRecordingTranscript(t, filepath.Join(destination, side))
		if len(records) != len(observedSent)+len(server) {
			t.Fatalf("runner %s records = %d, want %d", side, len(records), len(observedSent)+len(server))
		}
		sentIndex, serverIndex := 0, 0
		for index, record := range records {
			wantPeer := transcript.PeerClient
			wantDirection := transcript.DirectionIn
			outbound := (side == "client.transcript.jsonl" && record.Direction == transcript.DirectionOut) || (side == "agent.transcript.jsonl" && record.Direction == transcript.DirectionIn)
			var wantMessage messages.StreamMessage
			if outbound {
				wantMessage = observedSent[sentIndex]
				sentIndex++
				wantDirection = transcript.DirectionOut
			} else {
				wantMessage = server[serverIndex]
				serverIndex++
			}
			if side == "agent.transcript.jsonl" {
				wantPeer = transcript.PeerAgent
				if outbound {
					wantDirection = transcript.DirectionIn
				} else {
					wantDirection = transcript.DirectionOut
				}
			}
			wantPayload, err := gwtesting.MarshalStreamMessage(wantMessage)
			if err != nil {
				t.Fatalf("marshal runner expected frame %d: %v", index, err)
			}
			if !bytes.Equal(record.Payload, wantPayload) {
				t.Fatalf("runner %s payload %d = %s, want %s", side, index, record.Payload, wantPayload)
			}
			wantTimestamp := sessionRecordingClockBase.Add(time.Duration(index+1) * time.Nanosecond).Format(time.RFC3339Nano)
			if record.Tick != uint64(index+1) || record.Timestamp != wantTimestamp || record.Peer != wantPeer || record.Direction != wantDirection {
				t.Fatalf("runner %s record %d = %+v, want tick=%d timestamp=%s peer=%s direction=%s", side, index, record, index+1, wantTimestamp, wantPeer, wantDirection)
			}
		}
		if sentIndex != len(observedSent) || serverIndex != len(server) {
			t.Fatalf("runner %s consumed sent=%d/%d server=%d/%d transcript frames", side, sentIndex, len(observedSent), serverIndex, len(server))
		}
	}

	manifestBytes, err := os.ReadFile(filepath.Join(destination, "manifest.json"))
	if err != nil {
		t.Fatalf("read runner manifest: %v", err)
	}
	var manifest transcript.RecordingManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatalf("decode runner manifest: %v", err)
	}
	if manifest.ClockBase != sessionRecordingClockBase.Format(time.RFC3339Nano) || manifest.Transport != "websocket" || manifest.Model != "gpt-realtime" {
		t.Fatalf("runner manifest metadata = %+v", manifest)
	}
	assertManifestArtifacts(t, destination, manifest, []string{
		"client.transcript.jsonl",
		"agent.transcript.jsonl",
		"session-log.jsonl",
		"audio/in-000.pcm",
		"audio/in-001.pcm",
		"audio/out-000.pcm",
		"audio/out-001.pcm",
	})
}

func TestRunSessionWithRecordingDirectoryRejectsUnwritableDestinationBeforeConnect(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "readonly")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o755) })
	if err := os.Chmod(parent, 0o555); err != nil {
		t.Skipf("filesystem does not support permission setup: %v", err)
	}
	probe, probeErr := os.CreateTemp(parent, ".permission-probe-")
	if probeErr == nil {
		_ = probe.Close()
		_ = os.Remove(probe.Name())
		t.Skip("filesystem permits writes to the mode-restricted test directory")
	}

	destination := filepath.Join(parent, "capture")
	inferencer := &countingSessionRecordingInferencer{}
	err := RunSessionWithRecordingDirectory(context.Background(), io.Discard, SessionRunOptions{
		Provider:          config.ProviderOpenAI,
		Model:             "gpt-realtime",
		APIKey:            "test-key",
		ConfigDir:         t.TempDir(),
		SessionInferencer: inferencer,
	}, destination)
	if err == nil || !errors.Is(err, transcript.ErrRecordingDestination) || !strings.Contains(err.Error(), destination) {
		t.Fatalf("unwritable destination error = %v, want path-qualified destination error", err)
	}
	if inferencer.connects != 0 {
		t.Fatalf("connects = %d, want zero", inferencer.connects)
	}
	if _, statErr := os.Stat(destination); !os.IsNotExist(statErr) {
		t.Fatalf("unwritable destination stat error = %v, want not exist", statErr)
	}
}

func TestSessionRecordingFlagsRemainIndependentAndComposable(t *testing.T) {
	input := sessionRecordingInputSegments()
	inputPath := writeSessionRecordingInputFixture(t, input)
	events := []messages.StreamMessage{
		{Type: messages.StreamTypeAudioStart, Role: messages.RoleAssistant, Value: messages.NewAudioStartValue()},
		{Type: messages.StreamTypeAudioDelta, Role: messages.RoleAssistant, Value: messages.NewAudioDeltaValue([]byte{0x10, 0x00, 0x11, 0x00})},
		{Type: messages.StreamTypeAudioEnd, Role: messages.RoleAssistant, Value: messages.NewAudioEndValue()},
		{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()},
		{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("composable response")},
		{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
	}
	baseOptions := func(inferencer messages.SessionInferencer) SessionRunOptions {
		return SessionRunOptions{
			Provider:          config.ProviderOpenAI,
			Model:             "gpt-realtime",
			APIKey:            "test-key",
			ConfigDir:         t.TempDir(),
			Prompt:            "",
			SessionInferencer: inferencer,
		}
	}

	directoryOnly := filepath.Join(t.TempDir(), "directory-only")
	directoryInferencer := newSessionRecordingRunnerInferencerAfterAudioEnd(events)
	var directoryOutput bytes.Buffer
	if err := RunSessionWithRecordingDirectoryAndInstructionsAndAudioInputAndOutputAndTextSeedAndMaxDuration(context.Background(), &directoryOutput, baseOptions(directoryInferencer), directoryOnly, "", 0, SessionTextSeed{}, SessionAudioInput{Path: inputPath, Present: true}, ""); err != nil {
		t.Fatalf("directory-only run: %v", err)
	}

	fixtureOnly := filepath.Join(t.TempDir(), "fixture-only.json")
	fixtureInferencer := newSessionRecordingRunnerInferencerAfterAudioEnd(events)
	var fixtureOutput bytes.Buffer
	fixtureOptions := baseOptions(fixtureInferencer)
	fixtureOptions.RecordPath = fixtureOnly
	fixturePlan, fixtureCleanup, err := planSessionForDirectoryRecording(fixtureOptions)
	if err != nil {
		t.Fatalf("plan fixture-only run: %v", err)
	}
	fixtureSource, err := openSessionAudioInput(SessionAudioInput{Path: inputPath, Present: true})
	if err != nil {
		t.Fatalf("open fixture-only audio input: %v", err)
	}
	defer fixtureSource.Close()
	fixturePlan.loop.CloseAfterOpen = false
	fixturePlan.loop.AudioIn = fixtureSource
	if err := fixturePlan.run(context.Background(), &fixtureOutput); err != nil {
		fixtureCleanup()
		t.Fatalf("fixture-only run: %v", err)
	}
	fixtureCleanup()

	combinedDirectory := filepath.Join(t.TempDir(), "combined-directory")
	combinedFixture := filepath.Join(t.TempDir(), "combined-fixture.json")
	combinedInferencer := newSessionRecordingRunnerInferencerAfterAudioEnd(events)
	var combinedOutput bytes.Buffer
	combinedOptions := baseOptions(combinedInferencer)
	combinedOptions.RecordPath = combinedFixture
	if err := RunSessionWithRecordingDirectoryAndInstructionsAndAudioInputAndOutputAndTextSeedAndMaxDuration(context.Background(), &combinedOutput, combinedOptions, combinedDirectory, "", 0, SessionTextSeed{}, SessionAudioInput{Path: inputPath, Present: true}, ""); err != nil {
		t.Fatalf("combined run: %v", err)
	}
	if directoryOutput.String() != combinedOutput.String() || fixtureOutput.String() != combinedOutput.String() {
		t.Fatalf("flag outputs differ: directory=%q fixture=%q combined=%q", directoryOutput.String(), fixtureOutput.String(), combinedOutput.String())
	}

	directoryFiles := recordingFileBytes(t, directoryOnly)
	combinedFiles := recordingFileBytes(t, combinedDirectory)
	if !reflect.DeepEqual(directoryFiles, combinedFiles) {
		t.Fatalf("directory-only and combined directory artifacts differ")
	}
	fixtureCapture, err := gwtesting.LoadSessionCapture(fixtureOnly)
	if err != nil {
		t.Fatalf("load fixture-only capture: %v", err)
	}
	combinedCapture, err := gwtesting.LoadSessionCapture(combinedFixture)
	if err != nil {
		t.Fatalf("load combined capture: %v", err)
	}
	assertSessionCaptureEquivalent(t, fixtureCapture, combinedCapture)
}

func TestSessionDirectoryRecordingFinalizePreservesWriteFailureAndNoPartialBundle(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "capture")
	writeErr := errors.New("injected recording write failure")
	recording := newSessionDirectoryRecording(destination, sessionRuntimePlan{provider: sessionProviderOpenAI}, SessionRunOptions{Model: "gpt-realtime"})
	recording.client.Write([]byte("client\n"))
	recording.agent.Write([]byte("agent\n"))
	recording.input = [][]byte{{0x01, 0x00}}
	recording.output = [][]byte{{0x02, 0x00}}
	recording.writeFile = func(string, []byte, os.FileMode) (int, error) {
		return 0, writeErr
	}

	err := recording.Finalize()
	if !errors.Is(err, writeErr) || !errors.Is(err, transcript.ErrRecordingWrite) {
		t.Fatalf("finalize error = %v, want injected and recording-write identities", err)
	}
	if !strings.Contains(err.Error(), filepath.Join(destination, "client.transcript.jsonl")) {
		t.Fatalf("finalize error = %v, want affected artifact path", err)
	}
	if _, statErr := os.Stat(destination); !os.IsNotExist(statErr) {
		t.Fatalf("failed destination stat error = %v, want not exist", statErr)
	}
}

func recordingFileBytes(t *testing.T, root string) map[string][]byte {
	t.Helper()
	files := make(map[string][]byte)
	for _, relative := range recordingEntries(t, root) {
		if relative == "audio" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
		if err != nil {
			t.Fatalf("read recording file %s: %v", relative, err)
		}
		files[relative] = data
	}
	return files
}

func assertSessionCaptureEquivalent(t *testing.T, want, got gwtesting.SessionCapture) {
	t.Helper()
	if want.Version != got.Version || !reflect.DeepEqual(want.Provider, got.Provider) || len(want.Records) != len(got.Records) {
		t.Fatalf("capture envelopes differ: want=%+v got=%+v", want, got)
	}
	want.Session.StartedAtUTC = ""
	got.Session.StartedAtUTC = ""
	if !reflect.DeepEqual(want.Session, got.Session) {
		t.Fatalf("capture session metadata differs: want=%+v got=%+v", want.Session, got.Session)
	}
	for index := range want.Records {
		left, right := want.Records[index], got.Records[index]
		left.TimestampMs = 0
		right.TimestampMs = 0
		if !reflect.DeepEqual(left, right) {
			t.Fatalf("capture record %d differs: want=%+v got=%+v", index, left, right)
		}
	}
}

func assertManifestArtifacts(t *testing.T, destination string, manifest transcript.RecordingManifest, wantPaths []string) {
	t.Helper()
	seen := make(map[string]struct{}, len(manifest.Artifacts))
	for _, artifact := range manifest.Artifacts {
		if _, ok := seen[artifact.Path]; ok {
			t.Fatalf("manifest references %q more than once", artifact.Path)
		}
		seen[artifact.Path] = struct{}{}
		if filepath.IsAbs(artifact.Path) || filepath.Clean(filepath.FromSlash(artifact.Path)) != filepath.FromSlash(artifact.Path) {
			t.Fatalf("manifest artifact path is not relative and normalized: %q", artifact.Path)
		}
		data, err := os.ReadFile(filepath.Join(destination, filepath.FromSlash(artifact.Path)))
		if err != nil {
			t.Fatalf("read manifest artifact %q: %v", artifact.Path, err)
		}
		digest := sha256.Sum256(data)
		if got := hex.EncodeToString(digest[:]); got != artifact.SHA256 {
			t.Fatalf("manifest hash for %q = %s, want %s", artifact.Path, got, artifact.SHA256)
		}
	}
	gotPaths := make([]string, 0, len(seen))
	for path := range seen {
		gotPaths = append(gotPaths, path)
	}
	sort.Strings(gotPaths)
	want := append([]string(nil), wantPaths...)
	sort.Strings(want)
	if !reflect.DeepEqual(gotPaths, want) {
		t.Fatalf("manifest artifact paths = %v, want %v", gotPaths, want)
	}
}

type sessionRecordingRunnerInferencer struct {
	events                   []messages.StreamMessage
	waitForInput             bool
	waitForPrompt            bool
	waitForAudioEndAfterOpen bool
	connects                 int
	sessions                 []*sessionRecordingRunnerSession
}

func newSessionRecordingRunnerInferencer(events []messages.StreamMessage, waitForInput, waitForPrompt bool) *sessionRecordingRunnerInferencer {
	return &sessionRecordingRunnerInferencer{
		events:        append([]messages.StreamMessage(nil), events...),
		waitForInput:  waitForInput,
		waitForPrompt: waitForPrompt,
	}
}

func newSessionRecordingRunnerInferencerAfterAudioEnd(events []messages.StreamMessage) *sessionRecordingRunnerInferencer {
	return &sessionRecordingRunnerInferencer{
		events:                   append([]messages.StreamMessage(nil), events...),
		waitForAudioEndAfterOpen: true,
	}
}

func (i *sessionRecordingRunnerInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	i.connects++
	session := &sessionRecordingRunnerSession{
		receive:      messages.NewTypedBuffer[messages.StreamMessage](64),
		done:         make(chan struct{}),
		inputSeen:    make(chan struct{}),
		audioEndSeen: make(chan struct{}),
		promptSeen:   make(chan struct{}),
	}
	i.sessions = append(i.sessions, session)
	go func() {
		if i.waitForAudioEndAfterOpen {
			if !session.receive.Write(ctx, messages.StreamMessage{
				Type:  messages.StreamTypeSessionOpen,
				Value: messages.NewSessionOpenValue("runner-session", "session"),
			}) {
				return
			}
			select {
			case <-session.audioEndSeen:
			case <-ctx.Done():
				return
			}
		} else if i.waitForInput {
			select {
			case <-session.inputSeen:
			case <-ctx.Done():
				return
			}
			session.receive.Write(ctx, messages.StreamMessage{
				Type:  messages.StreamTypeSessionOpen,
				Value: messages.NewSessionOpenValue("runner-session", "session"),
			})
		} else if !session.receive.Write(ctx, messages.StreamMessage{
			Type:  messages.StreamTypeSessionOpen,
			Value: messages.NewSessionOpenValue("runner-session", "session"),
		}) {
			return
		}
		if i.waitForPrompt {
			select {
			case <-session.promptSeen:
			case <-ctx.Done():
				return
			}
		}
		for _, event := range i.events {
			if !session.receive.Write(ctx, event) {
				return
			}
		}
	}()
	return session, nil
}

type sessionRecordingRunnerSession struct {
	receive *messages.TypedBuffer[messages.StreamMessage]
	done    chan struct{}
	once    sync.Once

	inputSeen             chan struct{}
	inputOnce             sync.Once
	audioEndSeen          chan struct{}
	audioEndOnce          sync.Once
	promptSeen            chan struct{}
	promptOnce            sync.Once
	sentMu                sync.Mutex
	sent                  []messages.StreamMessage
	imageMessages         []messages.Message
	imageResponseRequests []bool
	sendHook              func(context.Context, messages.StreamMessage)
}

func (s *sessionRecordingRunnerSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	s.sentMu.Lock()
	s.sent = append(s.sent, msg)
	s.sentMu.Unlock()
	if msg.Type == messages.StreamTypeAudioDelta {
		s.inputOnce.Do(func() { close(s.inputSeen) })
	}
	if msg.Type == messages.StreamTypeMessageEnd {
		s.audioEndOnce.Do(func() { close(s.audioEndSeen) })
	}
	if msg.Type == messages.StreamTypeTextDelta {
		s.promptOnce.Do(func() { close(s.promptSeen) })
	}
	if s.sendHook != nil {
		s.sendHook(ctx, msg)
	}
	return true
}

func (s *sessionRecordingRunnerSession) SendMessage(_ context.Context, msg messages.Message) bool {
	return s.sendImageMessage(msg, true)
}

func (s *sessionRecordingRunnerSession) SendMessageWithoutResponse(_ context.Context, msg messages.Message) bool {
	return s.sendImageMessage(msg, false)
}

func (s *sessionRecordingRunnerSession) sendImageMessage(msg messages.Message, requestResponse bool) bool {
	s.sentMu.Lock()
	s.imageMessages = append(s.imageMessages, msg)
	s.imageResponseRequests = append(s.imageResponseRequests, requestResponse)
	s.sentMu.Unlock()
	s.promptOnce.Do(func() { close(s.promptSeen) })
	return true
}

func (s *sessionRecordingRunnerSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.receive
}

func (s *sessionRecordingRunnerSession) Done() <-chan struct{} { return s.done }

func (s *sessionRecordingRunnerSession) Close() error {
	s.once.Do(func() { close(s.done) })
	return nil
}

func (s *sessionRecordingRunnerSession) sentMessagesCopy() []messages.StreamMessage {
	s.sentMu.Lock()
	defer s.sentMu.Unlock()
	return append([]messages.StreamMessage(nil), s.sent...)
}

func (s *sessionRecordingRunnerSession) imageMessagesCopy() []messages.Message {
	s.sentMu.Lock()
	defer s.sentMu.Unlock()
	return append([]messages.Message(nil), s.imageMessages...)
}

func (s *sessionRecordingRunnerSession) imageResponseRequestsCopy() []bool {
	s.sentMu.Lock()
	defer s.sentMu.Unlock()
	return append([]bool(nil), s.imageResponseRequests...)
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

type persistentSessionRecordingInferencer struct {
	turns    [][]messages.StreamMessage
	connects int
}

func newPersistentSessionRecordingInferencer(turns [][]messages.StreamMessage) *persistentSessionRecordingInferencer {
	return &persistentSessionRecordingInferencer{turns: turns}
}

func (i *persistentSessionRecordingInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	i.connects++
	session := &persistentSessionRecordingSession{
		turns:   i.turns,
		receive: messages.NewTypedBuffer[messages.StreamMessage](64),
		done:    make(chan struct{}),
	}
	if !session.receive.Write(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeSessionOpen,
		Value: messages.NewSessionOpenValue("persistent-session", "session"),
	}) {
		return nil, ctx.Err()
	}
	if !session.receive.Write(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeSessionUpdated,
		Value: messages.NewSessionUpdatedValue("persistent-session"),
	}) {
		return nil, ctx.Err()
	}
	return session, nil
}

type persistentSessionRecordingSession struct {
	turns   [][]messages.StreamMessage
	receive *messages.TypedBuffer[messages.StreamMessage]
	done    chan struct{}
	once    sync.Once
	turn    int
}

func (s *persistentSessionRecordingSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	if msg.Type != messages.StreamTypeMessageEnd || s.turn >= len(s.turns) {
		return true
	}
	for _, event := range s.turns[s.turn] {
		if !s.receive.Write(ctx, event) {
			return false
		}
	}
	s.turn++
	if s.turn == len(s.turns) {
		return s.receive.Write(ctx, messages.StreamMessage{
			Type:  messages.StreamTypeSessionClose,
			Value: messages.NewSessionCloseValue("persistent-session", "done"),
		})
	}
	return true
}

func (s *persistentSessionRecordingSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.receive
}

func (s *persistentSessionRecordingSession) Done() <-chan struct{} {
	return s.done
}

func (s *persistentSessionRecordingSession) Close() error {
	s.once.Do(func() { close(s.done) })
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
var _ messages.SessionInferencer = (*persistentSessionRecordingInferencer)(nil)
