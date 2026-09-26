package integration

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/gorilla/websocket"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/probe"
	"github.com/portpowered/go-agent-harness/agent-cli/test/integration/testnet"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

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

	fixture := newFamilyAProviderFixture(t, scenario)
	defer fixture.Close()
	startedAt := time.Now()
	fixture.startedAt = startedAt
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
		BinaryPath:       buildAgentBinary(t),
		RecordDir:        filepath.Join(t.TempDir(), "record"),
		WorkingDirectory: sandbox,
		ConfigDir:        configDir,
		Provider:         "openai",
		Model:            "gpt-realtime",
		BaseURL:          fixture.WebSocketURL(),
		APIKey:           "hermetic-key",
		SystemPrompt:     scenario.TextSeed,
		MaxDuration:      scenario.Deadline, AdditionalArgs: []string{"--wait-for-close"},
		FrameDuration: 5 * time.Millisecond,
		Segments: []probe.DuplexAudioSegment{
			{ID: "turn-1-speech", PCM16: frame},
			{ID: "turn-1-silence", SilenceFor: 5 * time.Millisecond},
			{ID: "turn-2-speech", PCM16: familyAFrame(2), WaitForOutputBytes: 4, Before: captureCheckpoint(0)},
			{ID: "turn-2-silence", SilenceFor: 5 * time.Millisecond},
			{ID: "turn-3-speech", PCM16: familyAFrame(3), WaitForOutputBytes: 8, Before: captureCheckpoint(1)},
			{ID: "turn-3-silence", SilenceFor: 5 * time.Millisecond},
			{ID: "turn-4-speech", PCM16: familyAFrame(4), WaitForOutputBytes: 12, Before: captureFinalStateBoundaries},
			{ID: "turn-4-silence", SilenceFor: 5 * time.Millisecond, WaitForOutputBytes: 16},
		},
	})

	observation := fixture.Snapshot()
	if runErr != nil || observation.ProtocolError != "" {
		t.Fatalf("Family A shipped-process run failed: run=%v provider=%+v\nresult=%+v\nstdout=%x\nstderr=%s", runErr, observation, result, result.Stdout, result.Stderr)
	}
	assertFamilyAObservation(t, scenario, observation)

	assertFamilyAProcessEvidence(t, result)
	checkpointMu.Lock()
	checkpointCopy := append([]probe.FilesystemCheckpoint(nil), checkpoints...)
	checkpointMu.Unlock()
	evaluateFamilyARun(t, scenario, observation, checkpointCopy)
	if observation.FinalSummary != observation.ProductTranscript[len(observation.ProductTranscript)-1].Text {
		t.Fatalf("final summary evidence = %q, want final product transcript", observation.FinalSummary)
	}
}
func assertFamilyAObservation(t *testing.T, scenario probe.CustomerScenario, observation familyAProviderObservation) {
	t.Helper()
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
		if observation.ToolObservations[index].Tool != want || observation.ToolObservations[index].Status != rtStatusCompleted || !observation.ToolObservations[index].ResultSeen {
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
	ConnectionCount, SessionUpdates int
	FunctionCalls                   []familyAFunctionCall
	ToolObservations                []probe.ToolObservation
	CustomerTranscript              []probe.TranscriptEvent
	ProductTranscript               []probe.TranscriptEvent
	FinalSummary                    string
	ProtocolError                   string
}
type familyAProviderFixture struct {
	server                                                        *httptest.Server
	upgrader                                                      websocket.Upgrader
	scenario                                                      probe.CustomerScenario
	mu                                                            sync.Mutex
	startedAt                                                     time.Time
	connectionCount, sessionUpdates, inputAppends                 int
	closedAfterFinalInput, awaitingFinalSilence, finalSilenceSeen bool
	functionCalls                                                 []familyAFunctionCall
	toolObservations                                              []probe.ToolObservation
	customerTranscript, productTranscript                         []probe.TranscriptEvent
	finalSummary, protocolError                                   string
	actionIndex                                                   int
	pendingCall                                                   *familyAFunctionCall
	pendingCallStarted                                            time.Duration
	pendingResult                                                 bool
}

func newFamilyAProviderFixture(t testing.TB, scenario probe.CustomerScenario) *familyAProviderFixture {
	fixture := &familyAProviderFixture{
		upgrader: websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }},
		scenario: scenario,
	}
	fixture.server = testnet.NewWANSegmentServer(t, http.HandlerFunc(fixture.handle))
	return fixture
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
	if f.protocolError == "" && (f.inputAppends != 8 || !f.closedAfterFinalInput) {
		f.protocolError = fmt.Sprintf("final input ordering: appends=%d closed_after_final_input=%t", f.inputAppends, f.closedAfterFinalInput)
	}
	return familyAProviderObservation{
		ConnectionCount:    f.connectionCount,
		SessionUpdates:     f.sessionUpdates,
		FunctionCalls:      append([]familyAFunctionCall(nil), f.functionCalls...),
		ToolObservations:   append([]probe.ToolObservation(nil), f.toolObservations...),
		CustomerTranscript: append([]probe.TranscriptEvent(nil), f.customerTranscript...),
		ProductTranscript:  append([]probe.TranscriptEvent(nil), f.productTranscript...),
		FinalSummary:       f.finalSummary,
		ProtocolError:      f.protocolError,
	}
}
func (f *familyAProviderFixture) handle(writer http.ResponseWriter, request *http.Request) {
	if request.Header.Get("Authorization") != rtAuthorizationHeader {
		f.failProtocol("authorization header did not arrive through the supported child environment")
		writer.WriteHeader(http.StatusUnauthorized)
		return
	}
	connection, err := f.upgrader.Upgrade(writer, request, nil)
	if err != nil {
		f.failProtocol("upgrade websocket: " + err.Error())
		return
	}
	defer discardCloseError(connection)
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
		f.mu.Lock()
		closed := f.closedAfterFinalInput
		f.mu.Unlock()
		if closed {
			continue
		}
		switch event.Type {
		case rtEventSessionUpdate:
			f.mu.Lock()
			f.sessionUpdates++
			f.mu.Unlock()
			if err := f.sendSessionReady(connection); err != nil {
				f.failProtocol(err.Error())
				return
			}
		case rtEventInputAudioAppend:
			if err := f.handleAudioAppend(connection, event.Audio); err != nil {
				f.failProtocol(err.Error())
				return
			}
		case rtEventConversationItemCreate:
			if err := f.handleToolResultEvent(event.Item.Type, event.Item.CallID, event.Item.Output); err != nil {
				f.failProtocol(err.Error())
				return
			}
		case rtEventInputAudioCommit:
			f.handleAudioCommit(connection)
		case rtEventResponseCreate:
			if err := f.handleContinuation(connection); err != nil {
				f.failProtocol(err.Error())
				return
			}
		}
	}
}
func (f *familyAProviderFixture) handleAudioAppend(connection *websocket.Conn, encoded string) error {
	f.mu.Lock()
	f.inputAppends++
	f.mu.Unlock()
	audio, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return fmt.Errorf("decode input audio: %w", err)
	}
	if customerSimulationSilent(audio) {
		return f.handleAudioSilence(connection)
	}
	return f.handleCustomerUtterance(connection)
}
func (f *familyAProviderFixture) handleAudioSilence(connection *websocket.Conn) error {
	if err := f.send(connection, map[string]string{"type": "input_audio_buffer.speech_stopped"}); err != nil {
		return err
	}
	if err := f.send(connection, map[string]string{"type": "input_audio_buffer.committed"}); err != nil {
		return err
	}
	f.mu.Lock()
	f.finalSilenceSeen = f.finalSilenceSeen || f.awaitingFinalSilence
	f.awaitingFinalSilence = false
	f.mu.Unlock()
	return nil
}
func (f *familyAProviderFixture) handleAudioCommit(connection *websocket.Conn) {
	f.mu.Lock()
	finalSilenceSeen := f.finalSilenceSeen
	f.finalSilenceSeen = false
	f.closedAfterFinalInput = finalSilenceSeen
	f.mu.Unlock()
	if !finalSilenceSeen {
		return
	}
	if err := f.send(connection, map[string]string{"type": rtEventSessionClosed, "reason": "family_a_complete"}); err != nil {
		f.failProtocol(fmt.Errorf("send terminal session.closed after final input commit: %w", err).Error())
	}
}
func (f *familyAProviderFixture) handleToolResultEvent(itemType, callID, output string) error {
	if itemType != rtItemFunctionCallOutput {
		return nil
	}
	return f.handleToolResult(callID, output)
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
			"type":     rtEventResponseCreated,
			"response": map[string]string{"id": "response-" + turnID + "-tool"},
		}); err != nil {
			return err
		}
		if err := f.send(connection, map[string]any{
			"type": rtEventOutputItemAdded,
			"item": map[string]string{
				"type": rtItemFunctionCall, "id": call.ID, "call_id": call.ID,
				"name": call.Name, "arguments": "",
			},
		}); err != nil {
			return err
		}
		if err := f.send(connection, map[string]any{
			"type": rtEventFunctionCallArgumentsDone, "call_id": call.ID,
			"name": call.Name, "arguments": call.Args,
		}); err != nil {
			return err
		}
		return f.send(connection, map[string]any{
			"type":     rtEventResponseDone,
			"response": map[string]string{"id": "response-" + turnID + "-tool", "status": rtStatusCompleted},
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
		TurnID: fmt.Sprintf("turn-%d", index+1), Tool: pending.Name, Status: rtStatusCompleted,
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
		"type":    rtEventSessionCreated,
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
		"type":     rtEventResponseCreated,
		"response": map[string]string{"id": "response-" + turnID + "-confirmation"},
	}); err != nil {
		return err
	}
	transcript := []string{text}
	if marker < 4 {
		transcript = []string{text[:len(text)/2], text[len(text)/2:]}
	}
	for _, delta := range transcript {
		if err := f.send(connection, map[string]string{"type": rtEventOutputAudioTranscriptDelta, "delta": delta}); err != nil {
			return err
		}
	}
	if err := f.send(connection, map[string]string{"type": "response.output_audio_transcript.done", "transcript": text}); err != nil {
		return err
	}
	audio := []byte{marker, 0x41, 0x52, 0x50}
	if err := f.send(connection, map[string]any{
		"type": rtEventOutputAudioDelta, "delta": base64.StdEncoding.EncodeToString(audio), "format": "pcm16",
	}); err != nil {
		return err
	}
	if err := f.send(connection, map[string]string{"type": "response.output_audio.done"}); err != nil {
		return err
	}
	if marker == 4 {
		// Arm before publishing response.done: the peer may enqueue its final
		// silence as soon as that terminal response is observed.
		f.mu.Lock()
		f.awaitingFinalSilence = true
		f.mu.Unlock()
	}
	if err := f.send(connection, map[string]any{
		"type":     rtEventResponseDone,
		"response": map[string]string{"id": "response-" + turnID + "-confirmation", "status": rtStatusCompleted},
	}); err != nil {
		return err
	}
	return nil
}
func (f *familyAProviderFixture) send(connection *websocket.Conn, event any) error {
	return connection.WriteJSON(event)
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
