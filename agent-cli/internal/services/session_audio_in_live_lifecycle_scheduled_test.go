package services_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLiveRecordRuntimeAudioInCompletesRoundTrip(t *testing.T) {
	server := newScriptedRealtimeServer(false)
	recordPath := filepath.Join(t.TempDir(), "capture.json")
	outputPath := filepath.Join(t.TempDir(), "response.wav")
	toolDefinitions := []messages.ToolDefinition{
		{Name: "read_file", Description: "Read a UTF-8 file."},
		{Name: "exec", Description: "Execute a command."},
	}

	runErr := make(chan error, 1)
	go func() {
		runErr <- services.RunSessionWithInstructionsAndAudioInputAndOutputAndTextSeedAndMaxDuration(
			context.Background(),
			os.Stdout,
			func() services.SessionRunOptions {
				opts := liveAudioInRunOptions(t, server, recordPath)
				opts.ToolExecutor = &messages.DefaultToolExecutor{}
				opts.ToolDefinitions = toolDefinitions
				return opts
			}(),
			outputPath,
			15*time.Second,
			services.SessionTextSeed{},
			services.SessionAudioInput{
				Path:    committedSessionAudioInputWAVPath(t),
				Present: true,
			},
			"",
		)
	}()

	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("live-mode audio-in session error = %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("timed out waiting for live-mode audio-in session")
	}

	writes := server.writesSnapshot()
	commitIndex, createIndex := -1, -1
	for index, writeType := range writes {
		switch writeType {
		case "input_audio_buffer.commit":
			commitIndex = index
		case "response.create":
			createIndex = index
		}
	}
	if commitIndex < 0 || createIndex < 0 {
		t.Fatalf("wire capture missing commit/response.create: %v", writes)
	}
	if commitIndex > createIndex {
		t.Fatalf("commit must precede response.create: %v", writes)
	}
	appends := 0
	for _, writeType := range writes[:commitIndex] {
		if writeType == "input_audio_buffer.append" {
			appends++
		}
	}
	if appends == 0 {
		t.Fatalf("no input_audio_buffer.append preceded the commit: %v", writes)
	}
	var sessionUpdate struct {
		Instructions string `json:"instructions"`
		Tools        []struct {
			Name string `json:"name"`
		} `json:"tools"`
		Audio map[string]json.RawMessage `json:"audio"`
	}
	if err := json.Unmarshal(server.sessionUpdateSnapshot(), &sessionUpdate); err != nil {
		t.Fatalf("decode streamed-audio session.update: %v", err)
	}
	var audioInput map[string]json.RawMessage
	if err := json.Unmarshal(sessionUpdate.Audio["input"], &audioInput); err != nil {
		t.Fatalf("decode streamed-audio audio.input: %v", err)
	}
	if got := string(audioInput["turn_detection"]); got != "null" {
		t.Fatalf("streamed-audio turn detection = %s, want explicit null", got)
	}
	if strings.Count(sessionUpdate.Instructions, "Tool-grounding requirements:") != 1 {
		t.Fatalf("streamed-audio grounding policy count = %d, want 1; instructions=%q", strings.Count(sessionUpdate.Instructions, "Tool-grounding requirements:"), sessionUpdate.Instructions)
	}
	if strings.Contains(sessionUpdate.Instructions, "No tools are currently registered") {
		t.Fatalf("streamed-audio instructions contradict advertised tools: %q", sessionUpdate.Instructions)
	}
	if len(sessionUpdate.Tools) != len(toolDefinitions) {
		t.Fatalf("streamed-audio advertised tools = %#v, want %#v", sessionUpdate.Tools, toolDefinitions)
	}
	for index, definition := range toolDefinitions {
		if sessionUpdate.Tools[index].Name != definition.Name {
			t.Fatalf("streamed-audio tool %d = %#v, want %q", index, sessionUpdate.Tools[index], definition.Name)
		}
	}
	info, statErr := os.Stat(outputPath)
	if statErr != nil {
		t.Fatalf("stat recorded response audio: %v (writes=%v)", statErr, server.writesSnapshot())
	}
	if info.Size() <= 44 {
		t.Fatalf("recorded response audio = %d bytes; want non-empty assistant audio", info.Size())
	}
}

func TestLiveRecordRuntimeScheduledAudioCompletesWithoutCapturedSessionClose(t *testing.T) {
	server := newScheduledAudioLifecycleServer()
	destination := filepath.Join(t.TempDir(), "recording")
	recordPath := filepath.Join(t.TempDir(), "capture.json")
	audioPath := committedSessionAudioInputWAVPath(t)
	toolDefinitions := []messages.ToolDefinition{
		{Name: "read_file", Description: "Read a UTF-8 file."},
		{Name: "exec", Description: "Execute a command."},
	}

	result := make(chan error, 1)
	go func() {
		result <- services.RunSessionWithRecordingDirectoryAndInstructionsAndAudioFilesAndOutputAndTextSeedAndMaxDuration(
			context.Background(),
			io.Discard,
			services.SessionRunOptions{
				RecordPath:      recordPath,
				Provider:        "openai",
				Model:           "gpt-realtime",
				APIKey:          "test-key",
				ConfigDir:       t.TempDir(),
				WebSocketDialer: server,
				ToolExecutor:    &messages.DefaultToolExecutor{},
				ToolDefinitions: toolDefinitions,
			},
			destination,
			"",
			0,
			services.SessionTextSeed{},
			[]string{audioPath, audioPath},
			"",
		)
	}()

	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("scheduled live-mode session error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("scheduled live-mode session did not close after its final response")
	}

	writes := server.writesSnapshot()
	commitCount, responseCount, appendCount := 0, 0, 0
	firstResponseDone := -1
	secondAppend := -1
	for index, writeType := range writes {
		switch writeType {
		case "input_audio_buffer.append":
			appendCount++
			if responseCount == 1 && secondAppend < 0 {
				secondAppend = index
			}
		case "input_audio_buffer.commit":
			commitCount++
		case "response.create":
			responseCount++
		case "IN:response.done":
			if responseCount == 1 {
				firstResponseDone = index
			}
		}
	}
	if appendCount < 2 || commitCount != 2 || responseCount != 2 {
		t.Fatalf("scheduled live wire events = %v, want two appended turns, commits, and responses", writes)
	}
	if secondAppend < 0 || firstResponseDone < 0 || secondAppend <= firstResponseDone {
		t.Fatalf("second input was not dispatched after first response completion: %v", writes)
	}
	var sessionUpdate map[string]json.RawMessage
	if err := json.Unmarshal(server.sessionUpdateSnapshot(), &sessionUpdate); err != nil {
		t.Fatalf("decode scheduled session.update: %v", err)
	}
	var audio map[string]json.RawMessage
	if err := json.Unmarshal(sessionUpdate["audio"], &audio); err != nil {
		t.Fatalf("decode scheduled audio config: %v", err)
	}
	var input map[string]json.RawMessage
	if err := json.Unmarshal(audio["input"], &input); err != nil {
		t.Fatalf("decode scheduled audio input config: %v", err)
	}
	var instructions string
	if err := json.Unmarshal(sessionUpdate["instructions"], &instructions); err != nil {
		t.Fatalf("decode scheduled instructions: %v", err)
	}
	if strings.Count(instructions, "Tool-grounding requirements:") != 1 {
		t.Fatalf("scheduled grounding policy count = %d, want 1; instructions=%q", strings.Count(instructions, "Tool-grounding requirements:"), instructions)
	}
	var advertisedTools []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(sessionUpdate["tools"], &advertisedTools); err != nil {
		t.Fatalf("decode scheduled tools: %v", err)
	}
	if len(advertisedTools) != len(toolDefinitions) {
		t.Fatalf("scheduled advertised tools = %#v, want %#v", advertisedTools, toolDefinitions)
	}
	for index, definition := range toolDefinitions {
		if advertisedTools[index].Name != definition.Name {
			t.Fatalf("scheduled tool %d = %#v, want %q", index, advertisedTools[index], definition.Name)
		}
	}
	if detection := input["turn_detection"]; string(detection) != "null" {
		t.Fatalf("scheduled session.update turn detection = %s, want explicit null", detection)
	}
	if countWireEvent(writes, "IN:input_audio_buffer.speech_started") != 2 || countWireEvent(writes, "IN:input_audio_buffer.speech_stopped") != 2 {
		t.Fatalf("scheduled VAD observations = %v, want two speech-start and speech-stop events", writes)
	}

	manifestBytes, err := os.ReadFile(filepath.Join(destination, "manifest.json"))
	if err != nil {
		t.Fatalf("read finalized recording manifest: %v", err)
	}
	var manifest struct {
		Artifacts []struct {
			Path string `json:"path"`
		} `json:"artifacts"`
	}
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatalf("decode finalized recording manifest: %v", err)
	}
	inputArtifacts, outputArtifacts := 0, 0
	for _, artifact := range manifest.Artifacts {
		switch {
		case strings.HasPrefix(artifact.Path, "audio/in-"):
			inputArtifacts++
		case strings.HasPrefix(artifact.Path, "audio/out-"):
			outputArtifacts++
		}
	}
	if inputArtifacts != 2 || outputArtifacts != 2 {
		t.Fatalf("finalized audio artifacts = input:%d output:%d, want 2 each", inputArtifacts, outputArtifacts)
	}
}

// TestLiveRecordRuntimeScheduledAudioContinuesAfterEmptyDirectoryResult drives
// the real live session composition through three scheduled spoken turns. The
// middle response requests list_directory, whose successful result is empty;
// the third input is allowed onto the wire only after the tool continuation
// has received its terminal response.done.
func TestLiveRecordRuntimeScheduledAudioContinuesAfterEmptyDirectoryResult(t *testing.T) {
	server := newScheduledEmptyToolLifecycleServer()
	defer server.shutdown()
	destination := filepath.Join(t.TempDir(), "recording")
	recordPath := filepath.Join(t.TempDir(), "capture.json")
	audioPath := committedSessionAudioInputWAVPath(t)
	executor := &emptyDirectoryToolExecutor{}
	diagnostics := &scheduledTurnDiagnosticSink{}
	toolDefinitions := []messages.ToolDefinition{{
		Name:        "list_directory",
		Description: "List directory entries.",
	}}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- services.RunSessionWithRecordingDirectoryAndInstructionsAndAudioFilesAndOutputAndTextSeedAndMaxDuration(
			ctx,
			io.Discard,
			services.SessionRunOptions{
				RecordPath:      recordPath,
				Provider:        "openai",
				Model:           "gpt-realtime",
				APIKey:          "test-key",
				ConfigDir:       t.TempDir(),
				WebSocketDialer: server,
				ToolExecutor:    executor,
				ToolDefinitions: toolDefinitions,
				Diagnostics:     diagnostics,
			},
			destination,
			"",
			0,
			services.SessionTextSeed{},
			[]string{audioPath, audioPath, audioPath},
			"",
		)
	}()

	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("three-turn empty-tool scheduled session error = %v", err)
		}
	case <-ctx.Done():
		t.Fatalf("three-turn empty-tool scheduled session did not complete: %v; writes=%v", ctx.Err(), server.writesSnapshot())
	}

	writes := server.writesSnapshot()
	appends := wireEventIndexes(writes, "input_audio_buffer.append")
	commits := wireEventIndexes(writes, "input_audio_buffer.commit")
	responseCreates := wireEventIndexes(writes, "response.create")
	responseDones := wireEventIndexes(writes, "IN:response.done")
	conversationItems := wireEventIndexes(writes, "conversation.item.create")
	if len(appends) != 3 || len(commits) != 3 || len(responseCreates) != 4 || len(responseDones) != 4 || len(conversationItems) != 1 {
		t.Fatalf("three-turn empty-tool wire counts = appends:%d commits:%d response.create:%d response.done:%d conversation.item.create:%d; events=%v", len(appends), len(commits), len(responseCreates), len(responseDones), len(conversationItems), writes)
	}
	if !(appends[0] < commits[0] && commits[0] < responseCreates[0] && responseCreates[0] < responseDones[0] &&
		responseDones[0] < appends[1] && appends[1] < commits[1] && commits[1] < responseCreates[1] && responseCreates[1] < responseDones[1] &&
		responseDones[1] < conversationItems[0] && conversationItems[0] < responseCreates[2] && responseCreates[2] < responseDones[2] &&
		responseDones[2] < appends[2] && appends[2] < commits[2] && commits[2] < responseCreates[3] && responseCreates[3] < responseDones[3]) {
		t.Fatalf("three-turn empty-tool wire order = %v, want turn 3 after the middle continuation", writes)
	}

	var emptyOutputs []struct {
		CallID string
		Output string
	}
	for _, payload := range server.clientMessagesSnapshot() {
		var frame struct {
			Type string `json:"type"`
			Item struct {
				Type   string `json:"type"`
				CallID string `json:"call_id"`
				Output string `json:"output"`
			} `json:"item"`
		}
		if err := json.Unmarshal(payload, &frame); err != nil {
			t.Fatalf("decode client wire frame %q: %v", payload, err)
		}
		if frame.Type == "conversation.item.create" && frame.Item.Type == "function_call_output" {
			emptyOutputs = append(emptyOutputs, struct {
				CallID string
				Output string
			}{CallID: frame.Item.CallID, Output: frame.Item.Output})
		}
	}
	if len(emptyOutputs) != 1 || emptyOutputs[0].CallID != "call_empty_directory" || emptyOutputs[0].Output != "" {
		t.Fatalf("empty directory tool outputs = %#v, want one correlated empty result", emptyOutputs)
	}
	if calls := executor.callsSnapshot(); len(calls) != 1 || calls[0].ID != "call_empty_directory" || calls[0].Name != "list_directory" || calls[0].Arguments != "{}" {
		t.Fatalf("executed directory calls = %#v, want one call with the provider correlation", calls)
	}

	var turnRecords []services.SessionDiagnosticRecord
	for _, record := range diagnostics.recordsSnapshot() {
		if record.Event == services.SessionDiagnosticEventTurn {
			turnRecords = append(turnRecords, record)
		}
	}
	if len(turnRecords) != 3 {
		t.Fatalf("completed turn diagnostics = %#v, want exactly three terminal assistant turns", turnRecords)
	}
	for index, record := range turnRecords {
		if got := record.Fields["turn_index"]; got != []string{"1", "2", "3"}[index] {
			t.Fatalf("completed turn diagnostic %d = %#v, want turn_index %d", index, record.Fields, index+1)
		}
	}

	manifestBytes, err := os.ReadFile(filepath.Join(destination, "manifest.json"))
	if err != nil {
		t.Fatalf("read finalized three-turn recording manifest: %v", err)
	}
	var manifest struct {
		Artifacts []struct {
			Path string `json:"path"`
		} `json:"artifacts"`
	}
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatalf("decode finalized three-turn recording manifest: %v", err)
	}
	inputArtifacts, outputArtifacts := 0, 0
	for _, artifact := range manifest.Artifacts {
		switch {
		case strings.HasPrefix(artifact.Path, "audio/in-"):
			inputArtifacts++
		case strings.HasPrefix(artifact.Path, "audio/out-"):
			outputArtifacts++
		}
	}
	if inputArtifacts != 3 || outputArtifacts != 3 {
		t.Fatalf("finalized three-turn audio artifacts = input:%d output:%d, want 3 each", inputArtifacts, outputArtifacts)
	}
}

func TestLiveRecordRuntimeScheduledAudioBargeInUsesActiveResponseBoundary(t *testing.T) {
	server := newBargeInScheduledAudioLifecycleServer()
	destination := filepath.Join(t.TempDir(), "recording")
	recordPath := filepath.Join(t.TempDir(), "capture.json")
	audioPath := committedSessionAudioInputWAVPath(t)
	var observed []messages.StreamMessage
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := make(chan error, 1)
	go func() {
		result <- services.RunSessionWithRecordingDirectoryAndInstructionsAndAudioFilesAndOutputAndTextSeedAndMaxDuration(
			ctx,
			io.Discard,
			services.SessionRunOptions{
				RecordPath:       recordPath,
				Provider:         "openai",
				Model:            "gpt-realtime",
				APIKey:           "test-key",
				ConfigDir:        t.TempDir(),
				WebSocketDialer:  server,
				AudioInTurnBarge: true,
				StreamObserver: func(msg messages.StreamMessage) {
					observed = append(observed, msg)
				},
			},
			destination,
			"",
			0,
			services.SessionTextSeed{},
			[]string{audioPath, audioPath},
			"",
		)
	}()

	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("barge-in scheduled live-mode session error = %v", err)
		}
	case <-ctx.Done():
		t.Fatalf("barge-in scheduled live-mode session did not complete: %v", ctx.Err())
	}

	writes := server.writesSnapshot()
	if got := countWireEvent(writes, "response.cancel"); got != 1 {
		t.Fatalf("scheduled barge-in cancellations = %d, want exactly one: %v", got, writes)
	}
	if got := countWireEvent(writes, "input_audio_buffer.append"); got != 2 {
		t.Fatalf("scheduled barge-in appends = %d, want one append group per turn: %v", got, writes)
	}
	if got := countWireEvent(writes, "input_audio_buffer.commit"); got != 2 {
		t.Fatalf("scheduled barge-in commits = %d, want one per turn: %v", got, writes)
	}
	if got := countWireEvent(writes, "response.create"); got != 2 {
		t.Fatalf("scheduled barge-in responses = %d, want one per turn: %v", got, writes)
	}

	cancelIndex := indexOfWireEvent(writes, "response.cancel", 0)
	secondAppendIndex := indexOfWireEvent(writes, "input_audio_buffer.append", 1)
	firstResponseIndex := indexOfWireEvent(writes, "IN:response.created", 0)
	if firstResponseIndex < 0 || cancelIndex <= firstResponseIndex || cancelIndex >= secondAppendIndex {
		t.Fatalf("scheduled barge-in wire order = %v, want response.created < response.cancel < second append", writes)
	}

	seenSecondAudio, seenStaleAudio := false, false
	for _, msg := range observed {
		if msg.Type != messages.StreamTypeAudioDelta {
			continue
		}
		value, ok := msg.Value.(*messages.AudioDeltaValue)
		if !ok || value == nil {
			t.Fatalf("observed scheduled audio delta value = %T, want *AudioDeltaValue", msg.Value)
		}
		switch string(value.Content) {
		case string([]byte{1, 2, 3}):
			// The active boundary is response.created. Depending on which
			// already-queued provider delta the model runner drains first, the
			// first response's output may or may not cross before cancellation.
		case string([]byte{2, 0, 12, 0}):
			seenSecondAudio = true
		case "cancel-stale":
			seenStaleAudio = true
		}
	}
	if !seenSecondAudio {
		t.Fatalf("replacement response audio was not observed; stream=%#v", observed)
	}
	if seenStaleAudio {
		t.Fatalf("stale provider audio crossed the cancellation boundary: %#v", observed)
	}
}

func TestLiveRecordRuntimeScheduledAudioBargeInWaitsForPromptResponse(t *testing.T) {
	server := newPromptBargeInScheduledAudioLifecycleServer()
	defer server.closeOnce.Do(func() { close(server.closed) })
	destination := filepath.Join(t.TempDir(), "recording")
	recordPath := filepath.Join(t.TempDir(), "capture.json")
	audioPath := committedSessionAudioInputWAVPath(t)
	diagnostics := &scheduledTurnDiagnosticSink{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := make(chan error, 1)
	go func() {
		result <- services.RunSessionWithRecordingDirectoryAndInstructionsAndAudioFilesAndOutputAndTextSeedAndMaxDuration(
			ctx,
			io.Discard,
			services.SessionRunOptions{
				RecordPath:       recordPath,
				Provider:         "openai",
				Model:            "gpt-realtime",
				APIKey:           "test-key",
				ConfigDir:        t.TempDir(),
				WebSocketDialer:  server,
				Prompt:           "initial prompt",
				AudioInTurnBarge: true,
				Diagnostics:      diagnostics,
			},
			destination,
			"",
			0,
			services.SessionTextSeed{},
			[]string{audioPath, audioPath},
			"",
		)
	}()

	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("prompt-seeded scheduled barge-in session error = %v", err)
		}
	case <-ctx.Done():
		t.Fatalf("prompt-seeded scheduled barge-in session did not complete: writes=%v", server.writesSnapshot())
	}

	writes := server.writesSnapshot()
	if got := countWireEvent(writes, "response.cancel"); got != 1 {
		t.Fatalf("prompt-seeded scheduled cancellations = %d, want exactly one: %v", got, writes)
	}
	if got := countWireEvent(writes, "input_audio_buffer.append"); got != 2 {
		t.Fatalf("prompt-seeded scheduled appends = %d, want two: %v", got, writes)
	}
	responseCreates := wireEventIndexes(writes, "response.create")
	appends := wireEventIndexes(writes, "input_audio_buffer.append")
	promptItem := indexOfWireEvent(writes, "conversation.item.create", 0)
	initialDone := indexOfWireEvent(writes, "IN:response.done", 0)
	cancelIndex := indexOfWireEvent(writes, "response.cancel", 0)
	if len(responseCreates) != 3 || len(appends) != 2 || promptItem < 0 || initialDone < 0 ||
		!(promptItem < responseCreates[0] && responseCreates[0] < initialDone && initialDone < appends[0] &&
			appends[0] < responseCreates[1] && responseCreates[1] < cancelIndex && cancelIndex < appends[1]) {
		t.Fatalf("prompt/seed scheduled wire order = %v, want prompt response before first append and cancellation only during second response", writes)
	}

	var terminalMetrics []services.SessionDiagnosticRecord
	for _, record := range diagnostics.recordsSnapshot() {
		if record.Event == services.SessionDiagnosticEventMetrics {
			terminalMetrics = append(terminalMetrics, record)
		}
	}
	if len(terminalMetrics) != 1 {
		t.Fatalf("prompt/seed terminal metrics = %#v, want exactly one", terminalMetrics)
	}
	for field, want := range map[string]string{
		services.SessionDiagnosticFieldCompletedTurnCount:   "2",
		services.SessionDiagnosticFieldDispatchedInputCount: "2",
		services.SessionDiagnosticFieldScheduledInputCount:  "2",
	} {
		if got := terminalMetrics[0].Fields[field]; got != want {
			t.Fatalf("prompt/seed terminal metric %q = %q, want %q; fields=%v", field, got, want, terminalMetrics[0].Fields)
		}
	}
}

func TestLiveRecordRuntimeScheduledAudioWaitsForSessionUpdated(t *testing.T) {
	server := newDelayedScheduledAudioLifecycleServer()
	destination := filepath.Join(t.TempDir(), "recording")
	recordPath := filepath.Join(t.TempDir(), "capture.json")
	audioPath := committedSessionAudioInputWAVPath(t)

	result := make(chan error, 1)
	go func() {
		result <- services.RunSessionWithRecordingDirectoryAndInstructionsAndAudioFilesAndOutputAndTextSeedAndMaxDuration(
			context.Background(),
			io.Discard,
			services.SessionRunOptions{
				RecordPath:      recordPath,
				Provider:        "openai",
				Model:           "gpt-realtime",
				APIKey:          "test-key",
				ConfigDir:       t.TempDir(),
				WebSocketDialer: server,
			},
			destination,
			"",
			0,
			services.SessionTextSeed{},
			[]string{audioPath},
			"",
		)
	}()

	select {
	case <-server.sessionCreated:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for SESSION.OPEN fixture")
	}

	for _, writeType := range server.writesSnapshot() {
		switch writeType {
		case "input_audio_buffer.append", "input_audio_buffer.commit", "response.create":
			t.Fatalf("scheduled turn crossed provider boundary before session.updated: %v", server.writesSnapshot())
		}
	}

	server.releaseSessionUpdated()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("delayed-ack scheduled session error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("delayed-ack scheduled session did not complete after session.updated")
	}

	writes := server.writesSnapshot()
	updatedIndex, appendIndex, commitIndex, responseIndex := -1, -1, -1, -1
	for index, writeType := range writes {
		switch writeType {
		case "IN:session.updated":
			if updatedIndex < 0 {
				updatedIndex = index
			}
		case "input_audio_buffer.append":
			if appendIndex < 0 {
				appendIndex = index
			}
		case "input_audio_buffer.commit":
			if commitIndex < 0 {
				commitIndex = index
			}
		case "response.create":
			if responseIndex < 0 {
				responseIndex = index
			}
		}
	}
	if updatedIndex < 0 || appendIndex < 0 || commitIndex < 0 || responseIndex < 0 {
		t.Fatalf("delayed-ack wire events = %v, want session.updated and one turn boundary", writes)
	}
	if !(updatedIndex < appendIndex && appendIndex < commitIndex && commitIndex < responseIndex) {
		t.Fatalf("delayed-ack wire order = %v, want session.updated < append < commit < response.create", writes)
	}
	if countWireEvent(writes, "input_audio_buffer.append") != 1 || countWireEvent(writes, "input_audio_buffer.commit") != 1 || countWireEvent(writes, "response.create") != 1 {
		t.Fatalf("delayed-ack wire boundary duplicated: %v", writes)
	}
}

func TestLiveRecordRuntimeScheduledAudioConfigTimeoutSendsNoTurn(t *testing.T) {
	server := newDelayedScheduledAudioLifecycleServer()
	destination := filepath.Join(t.TempDir(), "recording")
	recordPath := filepath.Join(t.TempDir(), "capture.json")
	audioPath := committedSessionAudioInputWAVPath(t)

	result := make(chan error, 1)
	go func() {
		result <- services.RunSessionWithRecordingDirectoryAndInstructionsAndAudioFilesAndOutputAndTextSeedAndMaxDuration(
			context.Background(),
			io.Discard,
			services.SessionRunOptions{
				RecordPath:            recordPath,
				Provider:              "openai",
				Model:                 "gpt-realtime",
				APIKey:                "test-key",
				ConfigDir:             t.TempDir(),
				WebSocketDialer:       server,
				SessionUpdatedTimeout: 25 * time.Millisecond,
			},
			destination,
			"",
			0,
			services.SessionTextSeed{},
			[]string{audioPath},
			"",
		)
	}()

	select {
	case <-server.sessionCreated:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for SESSION.OPEN fixture")
	}

	select {
	case err := <-result:
		if !errors.Is(err, services.ErrSessionScheduledAudioConfigTimeout) {
			t.Fatalf("config-timeout error = %v, want ErrSessionScheduledAudioConfigTimeout", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("scheduled session did not fail at its configuration acknowledgement timeout")
	}

	writes := server.writesSnapshot()
	for _, writeType := range []string{"input_audio_buffer.append", "input_audio_buffer.commit", "response.create"} {
		if countWireEvent(writes, writeType) != 0 {
			t.Fatalf("config timeout sent %s: %v", writeType, writes)
		}
	}
}

func countWireEvent(writes []string, want string) int {
	count := 0
	for _, writeType := range writes {
		if writeType == want {
			count++
		}
	}
	return count
}

func wireEventIndexes(writes []string, want string) []int {
	indexes := make([]int, 0)
	for index, writeType := range writes {
		if writeType == want {
			indexes = append(indexes, index)
		}
	}
	return indexes
}

func indexOfWireEvent(writes []string, want string, occurrence int) int {
	seen := 0
	for index, writeType := range writes {
		if writeType != want {
			continue
		}
		if seen == occurrence {
			return index
		}
		seen++
	}
	return -1
}

// TestLiveRecordRuntimeAudioInCancellationDuringAwaitSurfacesError proves a
// cancellation while the session waits for the model response after
// end-of-turn surfaces as an explicit error instead of a clean exit.
