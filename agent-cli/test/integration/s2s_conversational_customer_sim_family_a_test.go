package integration

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/probe"
)

// TestFamilyAIterativeBuildUpThroughShippedProcess is the whole-family
// hermetic proof for the customer simulator's first scenario. Four natural
// customer utterances cross one continuously open PCM16 stream into the
// shipped executable. The fake Realtime provider requests the real process's
// exec, write_file, and edit_file tools, and gates each next utterance on the
// prior confirmation audio. The filesystem oracle snapshots the sandbox from
// those gates, before later turns are allowed to run.
func TestFamilyAIterativeBuildUpThroughShippedProcess(t *testing.T) {
	scenario := loadFamilyAScenario(t)
	sandbox := filepath.Join(t.TempDir(), "sandbox")
	if err := os.MkdirAll(sandbox, 0o700); err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	oracle, err := probe.NewFilesystemOracle(sandbox)
	if err != nil {
		t.Fatalf("NewFilesystemOracle: %v", err)
	}

	fixture := newFamilyAProviderFixture(scenario)
	defer fixture.Close()
	binaryPath := buildAgentBinary(t)
	t.Logf("Family A candidate source_revision=%q executable=%s executable_sha256=%s", os.Getenv("C07_SOURCE_REVISION"), binaryPath, fileSHA256(t, binaryPath))
	startedAt := time.Now()
	fixture.SetStartedAt(startedAt)
	configDir := filepath.Join(t.TempDir(), "config")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("create config directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte("tools:\n  exec:\n    enable_deny_patterns: true\n"), 0o600); err != nil {
		t.Fatalf("write hermetic session config: %v", err)
	}

	var checkpointMu sync.Mutex
	checkpoints := make([]probe.FilesystemCheckpoint, 0, len(scenario.Actions))
	captureCheckpoint := func(actionIndex int) probe.DuplexSegmentGate {
		return func(_ context.Context, _ *probe.DuplexProgress) error {
			action := scenario.Actions[actionIndex]
			checkpoint, err := oracle.Checkpoint(
				"checkpoint-"+action.ID,
				action.ID,
				time.Since(startedAt),
				action.Oracle.Checkpoints,
			)
			checkpointMu.Lock()
			checkpoints = append(checkpoints, checkpoint)
			checkpointMu.Unlock()
			return err
		}
	}
	captureFinalStateBoundaries := func(ctx context.Context, progress *probe.DuplexProgress) error {
		if err := captureCheckpoint(2)(ctx, progress); err != nil {
			return err
		}
		return captureCheckpoint(3)(ctx, progress)
	}

	frame := familyAFrame(1)
	result, runErr := probe.RunDuplexSession(context.Background(), probe.DuplexSessionConfig{
		BinaryPath:       binaryPath,
		RecordDir:        filepath.Join(t.TempDir(), "record"),
		WorkingDirectory: sandbox,
		ConfigDir:        configDir,
		Provider:         "openai",
		Model:            "gpt-realtime",
		BaseURL:          fixture.WebSocketURL(),
		APIKey:           "hermetic-key",
		SystemPrompt:     scenario.TextSeed,
		MaxDuration:      scenario.Deadline,
		FrameDuration:    5 * time.Millisecond,
		Segments: []probe.DuplexAudioSegment{
			{ID: "turn-1-speech", PCM16: frame},
			{ID: "turn-1-silence", SilenceFor: 5 * time.Millisecond},
			{ID: "turn-2-speech", PCM16: familyAFrame(2), WaitForOutputBytes: 4, Before: captureCheckpoint(0)},
			{ID: "turn-2-silence", SilenceFor: 5 * time.Millisecond},
			{ID: "turn-3-speech", PCM16: familyAFrame(3), WaitForOutputBytes: 8, Before: captureCheckpoint(1)},
			{ID: "turn-3-silence", SilenceFor: 5 * time.Millisecond},
			{ID: "turn-4-speech", PCM16: familyAFrame(4), WaitForOutputBytes: 12, Before: captureFinalStateBoundaries},
			{ID: "turn-4-silence", SilenceFor: 5 * time.Millisecond},
		},
	})

	observation := fixture.Snapshot()
	t.Logf("Family A pass source_revision=%q client_event_counts=%v server_event_counts=%v event_trace=%v functions=%d tools=%d customer_transcripts=%d product_transcripts=%d stdout=%x input_frames=%d exit=%d child_waited=%t input_finished=%t input_closed=%t stdout_closed=%t stderr_closed=%t", os.Getenv("C07_SOURCE_REVISION"), observation.ClientEventCounts, observation.ServerEventCounts, observation.EventTrace, len(observation.FunctionCalls), len(observation.ToolObservations), len(observation.CustomerTranscript), len(observation.ProductTranscript), result.Stdout, len(result.Input), result.ExitCode, result.ChildWaited, result.InputFinished, result.InputClosed, result.StdoutClosed, result.StderrClosed)
	if runErr != nil || observation.ProtocolError != "" {
		t.Fatalf("Family A shipped-process run failed: run=%v provider=%+v client_event_counts=%v server_event_counts=%v event_trace=%v\nresult=%+v\nstdout=%x\nstderr=%s", runErr, observation, observation.ClientEventCounts, observation.ServerEventCounts, observation.EventTrace, result, result.Stdout, result.Stderr)
	}
	if observation.ConnectionCount != 1 || observation.SessionUpdates != 1 {
		t.Fatalf("provider lifecycle = connections:%d session_updates:%d, want one open session and one update", observation.ConnectionCount, observation.SessionUpdates)
	}
	if got := len(observation.CustomerTranscript); got != len(scenario.Actions) {
		t.Fatalf("customer transcript events = %d, want %d ordered utterances", got, len(scenario.Actions))
	}
	if got := len(observation.ProductTranscript); got != len(scenario.Actions) {
		t.Fatalf("product transcript events = %d, want one confirmation per action", got)
	}
	if got := len(observation.ToolObservations); got != 3 {
		t.Fatalf("tool observations = %d, want exec/write/edit for the first three actions", got)
	}
	wantTools := []string{"exec", "write_file", "edit_file"}
	for index, want := range wantTools {
		if observation.ToolObservations[index].Tool != want || observation.ToolObservations[index].Status != "completed" || !observation.ToolObservations[index].ResultSeen {
			t.Fatalf("tool observation %d = %+v, want completed %q with a result", index, observation.ToolObservations[index], want)
		}
	}
	if len(observation.FunctionCalls) != 3 {
		t.Fatalf("provider function calls = %d, want three side-effecting calls", len(observation.FunctionCalls))
	}
	for index, call := range observation.FunctionCalls {
		if call.ActionID != scenario.Actions[index].ID {
			t.Fatalf("function call %d action = %q, want %q", index, call.ActionID, scenario.Actions[index].ID)
		}
	}

	if result.ExitCode != 0 || !result.ChildWaited || !result.InputFinished || !result.InputClosed || !result.StdoutClosed || !result.StderrClosed {
		t.Fatalf("process lifecycle result = %+v, want a fully reaped normal run", result)
	}
	if len(result.Input) != 8 {
		t.Fatalf("input frame evidence = %d, want speech and silence for all four turns", len(result.Input))
	}
	if len(result.Output) == 0 || len(result.Stdout) < 16 {
		t.Fatalf("output evidence reads=%d bytes=%d, want four streamed confirmation audio markers", len(result.Output), len(result.Stdout))
	}
	for marker := byte(1); marker <= 4; marker++ {
		if !bytes.Contains(result.Stdout, []byte{marker, 0x41, 0x52, 0x50}) {
			t.Fatalf("captured stdout = %x, missing confirmation marker %x", result.Stdout, []byte{marker, 0x41, 0x52, 0x50})
		}
	}

	checkpointMu.Lock()
	checkpointCopy := append([]probe.FilesystemCheckpoint(nil), checkpoints...)
	checkpointMu.Unlock()
	if len(checkpointCopy) != len(scenario.Actions) {
		t.Fatalf("filesystem checkpoints = %d, want one per action", len(checkpointCopy))
	}
	for index, checkpoint := range checkpointCopy {
		if checkpoint.ActionID != scenario.Actions[index].ID {
			t.Fatalf("checkpoint %d action = %q, want %q", index, checkpoint.ActionID, scenario.Actions[index].ID)
		}
	}

	actionResults := make([]probe.ActionResult, 0, len(scenario.Actions))
	for index, action := range scenario.Actions {
		productEvent := observation.ProductTranscript[index]
		result := probe.ActionResult{
			ActionID:      action.ID,
			TurnID:        productEvent.TurnID,
			Confirmed:     true,
			ConfirmedAt:   productEvent.At,
			Disposition:   probe.DispositionCompleted,
			EvidenceRefs:  []string{"filesystem-checkpoints.jsonl", "tool-observations.jsonl", "transcripts/product.jsonl"},
			CheckpointIDs: []string{checkpointCopy[index].ID},
		}
		if index < len(observation.ToolObservations) {
			result.ToolObservationIDs = []string{observation.ToolObservations[index].ID}
		}
		actionResults = append(actionResults, result)
	}
	mechanical, err := probe.EvaluateCustomerSimulation(
		scenario,
		actionResults,
		checkpointCopy,
		observation.ToolObservations,
		observation.ProductTranscript,
	)
	if err != nil {
		t.Fatalf("mechanical oracle evaluation: %v", err)
	}
	if !mechanical.Pass || len(mechanical.Findings) != 0 {
		t.Fatalf("Family A mechanical verdict = %+v, want pass without findings", mechanical)
	}
	if observation.FinalSummary != observation.ProductTranscript[len(observation.ProductTranscript)-1].Text {
		t.Fatalf("final summary evidence = %q, want final product transcript", observation.FinalSummary)
	}
}

func loadFamilyAScenario(t *testing.T) probe.CustomerScenario {
	t.Helper()
	path := filepath.Join(agentCLIRoot(t), "testdata", "customer-simulation", "family-a.scenario.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read Family A scenario: %v", err)
	}
	scenario, err := probe.ParseCustomerScenario(data)
	if err != nil {
		t.Fatalf("parse Family A scenario: %v", err)
	}
	return scenario
}

type familyAFunctionCall struct {
	ID       string
	ActionID string
	Name     string
	Args     string
}

type familyAProviderObservation struct {
	ConnectionCount    int
	SessionUpdates     int
	ClientEventCounts  map[string]int
	ServerEventCounts  map[string]int
	EventTrace         []string
	FunctionCalls      []familyAFunctionCall
	ToolObservations   []probe.ToolObservation
	CustomerTranscript []probe.TranscriptEvent
	ProductTranscript  []probe.TranscriptEvent
	FinalSummary       string
	ProtocolError      string
}

type familyAProviderFixture struct {
	server   *httptest.Server
	upgrader websocket.Upgrader
	scenario probe.CustomerScenario

	mu                 sync.Mutex
	startedAt          time.Time
	connectionCount    int
	sessionUpdates     int
	clientEventCounts  map[string]int
	serverEventCounts  map[string]int
	eventTrace         []string
	functionCalls      []familyAFunctionCall
	toolObservations   []probe.ToolObservation
	customerTranscript []probe.TranscriptEvent
	productTranscript  []probe.TranscriptEvent
	finalSummary       string
	protocolError      string
	actionIndex        int
	pendingCall        *familyAFunctionCall
	pendingCallStarted time.Duration
	pendingResult      bool
}

func newFamilyAProviderFixture(scenario probe.CustomerScenario) *familyAProviderFixture {
	fixture := &familyAProviderFixture{
		upgrader:          websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }},
		clientEventCounts: make(map[string]int),
		serverEventCounts: make(map[string]int),
		scenario:          scenario,
	}
	fixture.server = httptest.NewServer(http.HandlerFunc(fixture.handle))
	return fixture
}

func (f *familyAProviderFixture) SetStartedAt(startedAt time.Time) {
	f.mu.Lock()
	f.startedAt = startedAt
	f.mu.Unlock()
}

func (f *familyAProviderFixture) WebSocketURL() string {
	return strings.Replace(f.server.URL, "http://", "ws://", 1)
}

func (f *familyAProviderFixture) Close() {
	if f.server != nil {
		f.server.Close()
	}
}

func (f *familyAProviderFixture) Snapshot() familyAProviderObservation {
	f.mu.Lock()
	defer f.mu.Unlock()
	return familyAProviderObservation{
		ConnectionCount:    f.connectionCount,
		SessionUpdates:     f.sessionUpdates,
		ClientEventCounts:  cloneEventCounts(f.clientEventCounts),
		ServerEventCounts:  cloneEventCounts(f.serverEventCounts),
		EventTrace:         append([]string(nil), f.eventTrace...),
		FunctionCalls:      append([]familyAFunctionCall(nil), f.functionCalls...),
		ToolObservations:   append([]probe.ToolObservation(nil), f.toolObservations...),
		CustomerTranscript: append([]probe.TranscriptEvent(nil), f.customerTranscript...),
		ProductTranscript:  append([]probe.TranscriptEvent(nil), f.productTranscript...),
		FinalSummary:       f.finalSummary,
		ProtocolError:      f.protocolError,
	}
}

func (f *familyAProviderFixture) handle(writer http.ResponseWriter, request *http.Request) {
	if request.Header.Get("Authorization") != "Bearer hermetic-key" {
		f.failProtocol("authorization header did not arrive through the supported child environment")
		writer.WriteHeader(http.StatusUnauthorized)
		return
	}
	connection, err := f.upgrader.Upgrade(writer, request, nil)
	if err != nil {
		f.failProtocol("upgrade websocket: " + err.Error())
		return
	}
	defer connection.Close()
	f.mu.Lock()
	f.connectionCount++
	f.mu.Unlock()

	for {
		_, payload, readErr := connection.ReadMessage()
		if readErr != nil {
			return
		}
		var event struct {
			Type  string `json:"type"`
			Audio string `json:"audio"`
			Item  struct {
				Type   string `json:"type"`
				CallID string `json:"call_id"`
				Output string `json:"output"`
			} `json:"item"`
		}
		if err := json.Unmarshal(payload, &event); err != nil {
			f.failProtocol("decode client event: " + err.Error())
			return
		}
		f.recordEvent("client", event.Type)
		switch event.Type {
		case "session.update":
			f.mu.Lock()
			f.sessionUpdates++
			f.mu.Unlock()
			if err := f.sendSessionReady(connection); err != nil {
				return
			}
		case "input_audio_buffer.append":
			audio, decodeErr := base64.StdEncoding.DecodeString(event.Audio)
			if decodeErr != nil {
				f.failProtocol("decode input audio: " + decodeErr.Error())
				return
			}
			if customerSimulationSilent(audio) {
				if err := f.send(connection, map[string]string{"type": "input_audio_buffer.speech_stopped"}); err != nil {
					return
				}
				if err := f.send(connection, map[string]string{"type": "input_audio_buffer.committed"}); err != nil {
					return
				}
				continue
			}
			if err := f.handleCustomerUtterance(connection); err != nil {
				f.failProtocol(err.Error())
				return
			}
		case "conversation.item.create":
			if event.Item.Type != "function_call_output" {
				continue
			}
			if err := f.handleToolResult(event.Item.CallID, event.Item.Output); err != nil {
				f.failProtocol(err.Error())
				return
			}
		case "response.create":
			if err := f.handleContinuation(connection); err != nil {
				f.failProtocol(err.Error())
				return
			}
		case "input_audio_buffer.commit", "response.cancel":
			// The fixture accepts the product's normal end-of-input and
			// cancellation controls. Family A has no intended interruption.
		default:
			// Provider metadata and other optional client controls are not
			// relevant to the filesystem assertions.
		}
	}
}

func (f *familyAProviderFixture) handleCustomerUtterance(connection *websocket.Conn) error {
	f.mu.Lock()
	index := f.actionIndex
	f.actionIndex++
	startedAt := f.elapsedLocked()
	f.mu.Unlock()
	if index >= len(f.scenario.Actions) {
		return fmt.Errorf("received an unexpected fifth customer utterance")
	}
	action := f.scenario.Actions[index]
	turnID := fmt.Sprintf("turn-%d", index+1)
	f.recordCustomerTranscript(probe.TranscriptEvent{
		ID: "customer-" + turnID, TurnID: turnID, Speaker: probe.TranscriptCustomer,
		Text: probe.FamilyASpokenScript()[index].Text, At: startedAt, Final: true,
	})
	if index < 3 {
		call := familyAFunctionCall{
			ID:       fmt.Sprintf("call-family-a-%d", index+1),
			ActionID: action.ID,
			Name:     familyAToolName(index),
			Args:     familyAToolArguments(index),
		}
		f.mu.Lock()
		f.pendingCall = &call
		f.pendingCallStarted = startedAt
		f.pendingResult = false
		f.functionCalls = append(f.functionCalls, call)
		f.mu.Unlock()
		if err := f.send(connection, map[string]any{
			"type":     "response.created",
			"response": map[string]string{"id": "response-" + turnID + "-tool"},
		}); err != nil {
			return err
		}
		if err := f.send(connection, map[string]any{
			"type": "response.output_item.added",
			"item": map[string]string{
				"type": "function_call", "id": call.ID, "call_id": call.ID,
				"name": call.Name, "arguments": "",
			},
		}); err != nil {
			return err
		}
		if err := f.send(connection, map[string]any{
			"type": "response.function_call_arguments.done", "call_id": call.ID,
			"name": call.Name, "arguments": call.Args,
		}); err != nil {
			return err
		}
		return f.send(connection, map[string]any{
			"type":     "response.done",
			"response": map[string]string{"id": "response-" + turnID + "-tool", "status": "completed"},
		})
	}

	return f.sendConfirmation(connection, turnID, "The final project contains project/README.md with status ready for review; no other files were created.", 4)
}

func (f *familyAProviderFixture) handleToolResult(callID, output string) error {
	f.mu.Lock()
	pending := f.pendingCall
	startedAt := f.pendingCallStarted
	if pending == nil || pending.ID != callID {
		f.mu.Unlock()
		return fmt.Errorf("tool result %q arrived without the expected pending call", callID)
	}
	index := f.actionIndex - 1
	expectedOutput := familyAToolOutput(index)
	if output != expectedOutput {
		f.mu.Unlock()
		return fmt.Errorf("tool result for %q = %q, want %q", callID, output, expectedOutput)
	}
	f.pendingResult = true
	toolObservation := probe.ToolObservation{
		ID: "tool-" + fmt.Sprintf("%d", index+1), ActionID: pending.ActionID,
		TurnID: fmt.Sprintf("turn-%d", index+1), Tool: pending.Name, Status: "completed",
		At: startedAt, Duration: f.elapsedLocked() - startedAt, ResultSeen: true, Summary: output,
	}
	f.toolObservations = append(f.toolObservations, toolObservation)
	f.mu.Unlock()
	return nil
}

func (f *familyAProviderFixture) handleContinuation(connection *websocket.Conn) error {
	f.mu.Lock()
	pending := f.pendingCall
	ready := f.pendingResult
	f.mu.Unlock()
	if pending == nil || !ready {
		return nil
	}
	index := f.actionIndex - 1
	turnID := fmt.Sprintf("turn-%d", index+1)
	text := []string{
		"Created the project directory.",
		"Added the README content.",
		"Updated project/README.md: status is ready for review.",
	}[index]
	if err := f.sendConfirmation(connection, turnID, text, byte(index+1)); err != nil {
		return err
	}
	f.mu.Lock()
	f.pendingCall = nil
	f.pendingResult = false
	f.mu.Unlock()
	return nil
}

func (f *familyAProviderFixture) sendSessionReady(connection *websocket.Conn) error {
	if err := f.send(connection, map[string]any{
		"type":    "session.created",
		"session": map[string]string{"id": "family-a", "model": "gpt-realtime"},
	}); err != nil {
		return err
	}
	return f.send(connection, map[string]any{
		"type":    "session.updated",
		"session": map[string]string{"id": "family-a"},
	})
}

func (f *familyAProviderFixture) sendConfirmation(connection *websocket.Conn, turnID, text string, marker byte) error {
	f.recordProductTranscript(probe.TranscriptEvent{
		ID: "product-" + turnID, TurnID: turnID, Speaker: probe.TranscriptProduct,
		Text: text, At: f.elapsed(), Final: true,
	})
	if marker == 4 {
		f.mu.Lock()
		f.finalSummary = text
		f.mu.Unlock()
	}
	if err := f.send(connection, map[string]any{
		"type":     "response.created",
		"response": map[string]string{"id": "response-" + turnID + "-confirmation"},
	}); err != nil {
		return err
	}
	transcript := []string{text}
	if marker < 4 {
		transcript = []string{text[:len(text)/2], text[len(text)/2:]}
	}
	for _, delta := range transcript {
		if err := f.send(connection, map[string]string{"type": "response.output_audio_transcript.delta", "delta": delta}); err != nil {
			return err
		}
	}
	if err := f.send(connection, map[string]string{"type": "response.output_audio_transcript.done", "transcript": text}); err != nil {
		return err
	}
	audio := []byte{marker, 0x41, 0x52, 0x50}
	if err := f.send(connection, map[string]any{
		"type": "response.output_audio.delta", "delta": base64.StdEncoding.EncodeToString(audio), "format": "pcm16",
	}); err != nil {
		return err
	}
	if err := f.send(connection, map[string]string{"type": "response.output_audio.done"}); err != nil {
		return err
	}
	if err := f.send(connection, map[string]any{
		"type":     "response.done",
		"response": map[string]string{"id": "response-" + turnID + "-confirmation", "status": "completed"},
	}); err != nil {
		return err
	}
	if marker == 4 {
		// Give the final input pump enough time to deliver its last silence
		// frame and close stdin before the provider closes the session.
		time.Sleep(25 * time.Millisecond)
		return f.send(connection, map[string]string{"type": "session.closed", "reason": "family_a_complete"})
	}
	return nil
}

func (f *familyAProviderFixture) send(connection *websocket.Conn, event any) error {
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	var envelope struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return err
	}
	f.recordEvent("server", envelope.Type)
	return connection.WriteJSON(event)
}

func (f *familyAProviderFixture) recordEvent(direction, eventType string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if direction == "client" {
		f.clientEventCounts[eventType]++
	} else {
		f.serverEventCounts[eventType]++
	}
	if len(f.eventTrace) < 16 {
		f.eventTrace = append(f.eventTrace, direction+":"+eventType)
	}
}

func cloneEventCounts(counts map[string]int) map[string]int {
	clone := make(map[string]int, len(counts))
	for eventType, count := range counts {
		clone[eventType] = count
	}
	return clone
}

func (f *familyAProviderFixture) recordCustomerTranscript(event probe.TranscriptEvent) {
	f.mu.Lock()
	f.customerTranscript = append(f.customerTranscript, event)
	f.mu.Unlock()
}

func (f *familyAProviderFixture) recordProductTranscript(event probe.TranscriptEvent) {
	f.mu.Lock()
	f.productTranscript = append(f.productTranscript, event)
	f.mu.Unlock()
}

func (f *familyAProviderFixture) elapsed() time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.elapsedLocked()
}

func (f *familyAProviderFixture) elapsedLocked() time.Duration {
	if f.startedAt.IsZero() {
		return 0
	}
	return time.Since(f.startedAt)
}

func (f *familyAProviderFixture) failProtocol(message string) {
	f.mu.Lock()
	if f.protocolError == "" {
		f.protocolError = message
	}
	f.mu.Unlock()
}

func familyAFrame(seed byte) []byte {
	frame := make([]byte, probe.DefaultDuplexFrameSamples*2)
	for index := range frame {
		frame[index] = seed
	}
	return frame
}

func familyAToolName(index int) string {
	return []string{"exec", "write_file", "edit_file"}[index]
}

func familyAToolArguments(index int) string {
	arguments := []map[string]string{
		{"command": "mkdir -p project"},
		{"path": "project/README.md", "content": probe.FamilyAInitialREADME},
		{"path": "project/README.md", "old_text": probe.FamilyAInitialREADME, "new_text": probe.FamilyAFinalREADME},
	}
	data, err := json.Marshal(arguments[index])
	if err != nil {
		panic("marshal Family A tool arguments: " + err.Error())
	}
	return string(data)
}

func familyAToolOutput(index int) string {
	return []string{"(no output)", "File written: project/README.md", "File edited: project/README.md"}[index]
}
