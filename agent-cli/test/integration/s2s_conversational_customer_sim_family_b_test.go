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

// TestShippedSessionProcessFamilyBCorrection drives one persistent shipped
// session through an original tool action, an output-time correction, and an
// independently verified replacement action. The correction audio is gated
// on streamed output bytes while the original response is intentionally left
// open, so the test proves the interruption at the public PCM boundary.
func TestShippedSessionProcessFamilyBCorrection(t *testing.T) {
	scenario := loadFamilyBScenario(t)
	fixture := newFamilyBProviderFixture(scenario)
	defer fixture.Close()

	sandbox := filepath.Join(t.TempDir(), "sandbox")
	if err := os.MkdirAll(sandbox, 0o700); err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	oracle, err := probe.NewFilesystemOracle(sandbox)
	if err != nil {
		t.Fatalf("NewFilesystemOracle: %v", err)
	}
	configDir := filepath.Join(t.TempDir(), "config")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("create config directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte("tools:\n  exec:\n    enable_deny_patterns: true\n"), 0o600); err != nil {
		t.Fatalf("write hermetic session config: %v", err)
	}

	startedAt := time.Now()
	fixture.SetStartedAt(startedAt)
	var checkpointMu sync.Mutex
	checkpoints := make([]probe.FilesystemCheckpoint, 0, 2)
	captureOriginal := func(ctx context.Context, _ *probe.DuplexProgress) error {
		checkpoint, checkpointErr := oracle.Checkpoint(
			"checkpoint-original",
			probe.FamilyBOriginalActionID,
			time.Since(startedAt),
			scenario.Actions[0].Oracle.Checkpoints,
		)
		checkpointMu.Lock()
		checkpoints = append(checkpoints, checkpoint)
		checkpointMu.Unlock()
		return checkpointErr
	}

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
		MaxDuration:      scenario.Deadline,
		FrameDuration:    5 * time.Millisecond,
		AdditionalArgs:   []string{"--wait-for-close"},
		Segments: []probe.DuplexAudioSegment{
			{ID: "original-request", PCM16: familyBFrame(1)},
			{ID: "correction-request", PCM16: familyBFrame(2), WaitForOutputBytes: 8, Before: captureOriginal},
			{ID: "correction-silence", SilenceFor: 5 * time.Millisecond},
		},
	})
	observation := fixture.Snapshot()
	if runErr != nil || observation.ProtocolError != "" {
		t.Fatalf("Family B shipped-process run failed: run=%v provider=%+v\nresult=%+v\nstdout=%x\nstderr=%s", runErr, observation, result, result.Stdout, result.Stderr)
	}

	if observation.ConnectionCount != 1 || observation.SessionUpdates != 1 {
		t.Fatalf("provider lifecycle = connections:%d session_updates:%d, want one open session and one update", observation.ConnectionCount, observation.SessionUpdates)
	}
	if len(observation.CustomerTranscript) != 2 || observation.CustomerTranscript[1].At <= observation.CustomerTranscript[0].At {
		t.Fatalf("customer correction transcript = %+v, want two ordered turns", observation.CustomerTranscript)
	}
	if len(observation.FunctionCalls) != 2 || observation.FunctionCalls[0].ActionID != probe.FamilyBOriginalActionID || observation.FunctionCalls[1].ActionID != probe.FamilyBReplacementActionID {
		t.Fatalf("provider function calls = %+v, want original then replacement", observation.FunctionCalls)
	}
	if len(observation.ToolObservations) != 2 || observation.ToolObservations[0].Status != "completed" || observation.ToolObservations[1].Status != "completed" {
		t.Fatalf("tool observations = %+v, want two completed results; client events = %v; result = %+v; stdout = %x; stderr = %s", observation.ToolObservations, observation.ClientEvents, result, result.Stdout, result.Stderr)
	}
	if got, want := strings.Join(observation.ResponseTerminalStatuses, ","), "cancelled,completed"; got != want {
		t.Fatalf("response terminal statuses = %q, want %q", got, want)
	}
	correction := observation.Correction
	if correction.OriginalResponseStatus != "cancelled" || correction.ReplacementResponseStatus != "completed" {
		t.Fatalf("correction response statuses = %+v, want cancelled then completed", correction)
	}
	if !(correction.OriginalResponseStartedAt < correction.CorrectionStartedAt && correction.CorrectionStartedAt < correction.OriginalResponseEndedAt) {
		t.Fatalf("correction timing = %+v, want correction inside original output interval", correction)
	}
	if !(correction.CancellationSentAt < correction.CorrectionStartedAt) {
		t.Fatalf("cancellation timing = %+v, want cancellation before correction audio reaches provider", correction)
	}

	if result.ExitCode != 0 || !result.ChildWaited || !result.InputFinished || !result.InputClosed || !result.StdoutClosed || !result.StderrClosed {
		t.Fatalf("process lifecycle result = %+v, want fully reaped normal run", result)
	}
	if len(result.Input) < 3 || len(result.Output) < 2 {
		t.Fatalf("stream evidence input=%d output_reads=%d, want correction on one open paced stream", len(result.Input), len(result.Output))
	}
	for marker := byte(1); marker <= 2; marker++ {
		if !bytes.Contains(result.Stdout, []byte{marker, 0x42, 0x52, 0x42}) {
			t.Fatalf("captured stdout = %x, missing response audio marker %x", result.Stdout, []byte{marker, 0x42, 0x52, 0x42})
		}
	}
	if strings.Contains(result.Command, "hermetic-key") || strings.Contains(strings.Join(result.SanitizedArgs, "\x00"), "hermetic-key") {
		t.Fatalf("API key leaked into process evidence: command=%q args=%q", result.Command, result.SanitizedArgs)
	}
	for _, forbidden := range []string{"--audio-in-turn", "--api-key"} {
		if containsIntegrationString(result.SanitizedArgs, forbidden) {
			t.Fatalf("runner unexpectedly used forbidden boundary/credential argument %q: %v", forbidden, result.SanitizedArgs)
		}
	}

	finalCheckpoint, err := oracle.Checkpoint(
		"checkpoint-replacement",
		probe.FamilyBReplacementActionID,
		time.Since(startedAt),
		scenario.Actions[1].Oracle.Checkpoints,
	)
	if err != nil {
		t.Fatalf("capture replacement checkpoint: %v", err)
	}
	checkpointMu.Lock()
	checkpoints = append(checkpoints, finalCheckpoint)
	checkpointCopy := append([]probe.FilesystemCheckpoint(nil), checkpoints...)
	checkpointMu.Unlock()
	if len(checkpointCopy) != 2 {
		t.Fatalf("filesystem checkpoints = %d, want original and replacement boundaries", len(checkpointCopy))
	}

	actionResults := []probe.ActionResult{
		{
			ActionID:           probe.FamilyBOriginalActionID,
			TurnID:             "turn-1",
			Confirmed:          true,
			ConfirmedAt:        observation.ProductTranscript[0].At,
			Disposition:        probe.DispositionCancelled,
			OutcomeReason:      "correction interrupted the original action after the draft write; preserved draft state was recorded",
			EvidenceRefs:       []string{"filesystem-checkpoints.jsonl", "tool-observations.jsonl", "transcripts/product.jsonl"},
			CheckpointIDs:      []string{"checkpoint-original"},
			ToolObservationIDs: []string{observation.ToolObservations[0].ID},
		},
		{
			ActionID:           probe.FamilyBReplacementActionID,
			TurnID:             "turn-2",
			Confirmed:          true,
			ConfirmedAt:        observation.ProductTranscript[1].At,
			Disposition:        probe.DispositionCompleted,
			EvidenceRefs:       []string{"filesystem-checkpoints.jsonl", "tool-observations.jsonl", "transcripts/product.jsonl"},
			CheckpointIDs:      []string{"checkpoint-replacement"},
			ToolObservationIDs: []string{observation.ToolObservations[1].ID},
		},
	}
	process := &probe.ProcessFacts{
		PID:                result.PID,
		ExitCode:           result.ExitCode,
		ExitClassification: "normal",
		ChildWaited:        result.ChildWaited,
		InputClosed:        result.InputClosed,
		OutputClosed:       result.StdoutClosed && result.StderrClosed,
		StartedAt:          0,
		EndedAt:            result.Duration,
	}
	correction.Process = process
	mechanical, err := probe.EvaluateCustomerSimulationCorrection(
		scenario,
		actionResults,
		checkpointCopy,
		observation.ToolObservations,
		observation.ProductTranscript,
		correction,
	)
	if err != nil {
		t.Fatalf("Family B mechanical evaluation: %v", err)
	}
	if !mechanical.Pass || len(mechanical.Findings) != 0 {
		t.Fatalf("Family B mechanical verdict = %+v, want pass without findings", mechanical)
	}
}

// TestRunCustomerSimulationSuiteFamilyBUsesRecordedCorrectionBoundaries is
// the production-path regression for the suite builder. It intentionally
// runs through RunCustomerSimulationSuite rather than assembling the
// correction ledger in the test, so response IDs, raw record timestamps, the
// input gate, and the finalized validator bundle are all exercised together.
func TestRunCustomerSimulationSuiteFamilyBUsesRecordedCorrectionBoundaries(t *testing.T) {
	scenario := loadFamilyBScenario(t)
	fixture := newFamilyBProviderFixture(scenario)
	defer fixture.Close()
	fixture.SetStartedAt(time.Now())

	validator := probe.CustomerSimulationValidatorAgentFunc(func(_ context.Context, request probe.CustomerSimulationValidatorRequest) ([]byte, error) {
		if !request.Input.Mechanical.Pass {
			return nil, fmt.Errorf("production Family B mechanical verdict failed: %+v", request.Input.Mechanical)
		}
		return json.Marshal(probe.ValidatorVerdict{
			Verdict:      probe.ValidatorWorked,
			Summary:      "The correction interrupted the recorded original response and the replacement was independently completed.",
			EvidenceRefs: append([]string(nil), request.Input.EvidenceRefs...),
		})
	})

	script := probe.FamilyBSpokenScript()
	result, runErr := probe.RunCustomerSimulationSuite(context.Background(), probe.CustomerSimulationSuiteOptions{
		BinaryPath: buildAgentBinary(t), RunRoot: filepath.Join(t.TempDir(), "runs"), Provider: "openai", Model: "gpt-realtime",
		BaseURL: fixture.WebSocketURL(), APIKey: "hermetic-key", SystemPrompt: scenario.TextSeed,
		Runs:      []probe.CustomerSimulationRunSpec{{Scenario: scenario, Script: script, Audio: [][]byte{familyBFrame(1), familyBFrame(2)}}},
		Validator: validator, MaxDuration: scenario.Deadline, FrameDuration: 5 * time.Millisecond, SilenceDuration: 5 * time.Millisecond, ShutdownGrace: time.Second,
	})
	if runErr != nil {
		t.Fatalf("RunCustomerSimulationSuite: %v", runErr)
	}
	if len(result.Runs) != 1 {
		t.Fatalf("suite runs = %d, want 1", len(result.Runs))
	}
	run := result.Runs[0]
	if !run.Mechanical.Pass || !run.Validator.Pass() {
		t.Fatalf("suite Family B result = %+v, want mechanical and validator pass", run)
	}

	data, err := os.ReadFile(filepath.Join(run.BundleRoot, "events", "correction.json"))
	if err != nil {
		t.Fatalf("read correction evidence: %v", err)
	}
	var correction probe.CorrectionEvidence
	if err := json.Unmarshal(data, &correction); err != nil {
		t.Fatalf("decode correction evidence: %v", err)
	}
	if correction.OriginalResponseID != "response-original-output" {
		t.Fatalf("original response ID = %q, want recorded active response ID", correction.OriginalResponseID)
	}
	if correction.OriginalResponseStatus != "cancelled" || correction.ReplacementResponseStatus != "completed" {
		t.Fatalf("response statuses = %q/%q, want cancelled/completed", correction.OriginalResponseStatus, correction.ReplacementResponseStatus)
	}
	if !(correction.OriginalResponseStartedAt < correction.CorrectionStartedAt && correction.CorrectionStartedAt < correction.OriginalResponseEndedAt) {
		t.Fatalf("correction timing = %+v, want correction inside original response interval", correction)
	}
	if !(correction.CancellationSentAt < correction.CorrectionStartedAt) {
		t.Fatalf("cancellation timing = %+v, want cancellation before correction input", correction)
	}
	if _, err := probe.VerifyCustomerEvidenceBundle(run.BundleRoot); err != nil {
		t.Fatalf("VerifyCustomerEvidenceBundle(%q): %v", run.BundleRoot, err)
	}
}

func loadFamilyBScenario(t *testing.T) probe.CustomerScenario {
	t.Helper()
	path := filepath.Join(agentCLIRoot(t), "testdata", "customer-simulation", "family-b.scenario.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read Family B scenario: %v", err)
	}
	scenario, err := probe.ParseCustomerScenario(data)
	if err != nil {
		t.Fatalf("parse Family B scenario: %v", err)
	}
	return scenario
}

type familyBFunctionCall struct {
	ID       string
	ActionID string
	Name     string
	Args     string
}

type familyBProviderObservation struct {
	ConnectionCount          int
	SessionUpdates           int
	ClientEvents             []string
	FunctionCalls            []familyBFunctionCall
	ToolObservations         []probe.ToolObservation
	CustomerTranscript       []probe.TranscriptEvent
	ProductTranscript        []probe.TranscriptEvent
	Correction               probe.CorrectionEvidence
	ResponseTerminalStatuses []string
	ProtocolError            string
}

type familyBProviderFixture struct {
	server   *httptest.Server
	upgrader websocket.Upgrader
	scenario probe.CustomerScenario

	mu                        sync.Mutex
	startedAt                 time.Time
	connectionCount           int
	sessionUpdates            int
	clientEvents              []string
	functionCalls             []familyBFunctionCall
	toolObservations          []probe.ToolObservation
	customerTranscript        []probe.TranscriptEvent
	productTranscript         []probe.TranscriptEvent
	responseTerminalStatuses  []string
	protocolError             string
	utteranceIndex            int
	originalToolStarted       time.Duration
	replacementToolStarted    time.Duration
	originalOutputStarted     time.Duration
	originalOutputEnded       time.Duration
	replacementOutputStarted  time.Duration
	replacementOutputEnded    time.Duration
	cancellationSent          time.Duration
	cancellationEventRecorded bool
	cancellationResponseID    string
	correctionStarted         time.Duration
	activeResponse            string
	cancelPending             bool
	originalResultSeen        bool
	replacementResultSeen     bool
}

func newFamilyBProviderFixture(scenario probe.CustomerScenario) *familyBProviderFixture {
	fixture := &familyBProviderFixture{
		upgrader: websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }},
		scenario: scenario,
	}
	fixture.server = httptest.NewServer(http.HandlerFunc(fixture.handle))
	return fixture
}

func (f *familyBProviderFixture) SetStartedAt(startedAt time.Time) {
	f.mu.Lock()
	f.startedAt = startedAt
	f.mu.Unlock()
}

func (f *familyBProviderFixture) WebSocketURL() string {
	return strings.Replace(f.server.URL, "http://", "ws://", 1)
}

func (f *familyBProviderFixture) Close() {
	if f.server != nil {
		f.server.Close()
	}
}

func (f *familyBProviderFixture) Snapshot() familyBProviderObservation {
	f.mu.Lock()
	defer f.mu.Unlock()
	return familyBProviderObservation{
		ConnectionCount:    f.connectionCount,
		SessionUpdates:     f.sessionUpdates,
		ClientEvents:       append([]string(nil), f.clientEvents...),
		FunctionCalls:      append([]familyBFunctionCall(nil), f.functionCalls...),
		ToolObservations:   append([]probe.ToolObservation(nil), f.toolObservations...),
		CustomerTranscript: append([]probe.TranscriptEvent(nil), f.customerTranscript...),
		ProductTranscript:  append([]probe.TranscriptEvent(nil), f.productTranscript...),
		Correction: probe.CorrectionEvidence{
			OriginalActionID:             probe.FamilyBOriginalActionID,
			ReplacementActionID:          probe.FamilyBReplacementActionID,
			OriginalTurnID:               "turn-1",
			CorrectionTurnID:             "turn-2",
			OriginalResponseID:           "response-original-output",
			OriginalResponseStartedAt:    f.originalOutputStarted,
			CorrectionStartedAt:          f.correctionStarted,
			CancellationSentAt:           f.cancellationSent,
			OriginalResponseEndedAt:      f.originalOutputEnded,
			ReplacementResponseStartedAt: f.replacementOutputStarted,
			ReplacementResponseEndedAt:   f.replacementOutputEnded,
			CancellationEventRecorded:    f.cancellationEventRecorded,
			CancellationResponseID:       f.cancellationResponseID,
			OriginalResponseStatus:       "cancelled",
			ReplacementResponseStatus:    "completed",
		},
		ResponseTerminalStatuses: append([]string(nil), f.responseTerminalStatuses...),
		ProtocolError:            f.protocolError,
	}
}

func (f *familyBProviderFixture) handle(writer http.ResponseWriter, request *http.Request) {
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
		f.mu.Lock()
		f.clientEvents = append(f.clientEvents, event.Type)
		f.mu.Unlock()
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
			if familyBSilent(audio) {
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
			if err := f.handleToolResult(connection, event.Item.CallID, event.Item.Output); err != nil {
				f.failProtocol(err.Error())
				return
			}
		case "response.cancel":
			f.mu.Lock()
			f.cancelPending = true
			f.cancellationSent = f.elapsedLocked()
			f.cancellationEventRecorded = true
			f.cancellationResponseID = f.activeResponse
			f.mu.Unlock()
		case "input_audio_buffer.commit", "response.create":
			// The fixture models the two customer turns from the continuously
			// open stream and accepts the client's explicit end-of-input controls.
		default:
			// Optional provider metadata is not relevant to this filesystem and
			// interruption proof.
		}
	}
}

func (f *familyBProviderFixture) handleCustomerUtterance(connection *websocket.Conn) error {
	f.mu.Lock()
	index := f.utteranceIndex
	f.utteranceIndex++
	now := f.elapsedLocked()
	f.mu.Unlock()
	if index > 1 {
		return fmt.Errorf("received unexpected third customer utterance")
	}
	turnID := fmt.Sprintf("turn-%d", index+1)
	script := probe.FamilyBSpokenScript()[index]
	f.recordCustomerTranscript(probe.TranscriptEvent{
		ID: "customer-" + turnID, TurnID: turnID, Speaker: probe.TranscriptCustomer,
		Text: script.Text, At: now, Final: true,
	})

	if index == 0 {
		call := familyBFunctionCall{
			ID:       "call-family-b-original",
			ActionID: probe.FamilyBOriginalActionID,
			Name:     "write_file",
			Args:     familyBToolArguments("draft/brief.md", probe.FamilyBOriginalReleaseNote),
		}
		f.mu.Lock()
		f.originalToolStarted = now
		f.functionCalls = append(f.functionCalls, call)
		f.mu.Unlock()
		return f.sendToolCall(connection, "response-original-tool", call)
	}

	f.mu.Lock()
	cancelPending := f.cancelPending
	activeResponse := f.activeResponse
	f.correctionStarted = now
	f.mu.Unlock()
	if !cancelPending || activeResponse == "" {
		return fmt.Errorf("correction arrived without a cancellation of the active original response")
	}
	if err := f.finishCancelledOriginalResponse(connection); err != nil {
		return err
	}
	call := familyBFunctionCall{
		ID:       "call-family-b-replacement",
		ActionID: probe.FamilyBReplacementActionID,
		Name:     "write_file",
		Args:     familyBToolArguments("final/brief.md", probe.FamilyBReplacementReleaseNote),
	}
	f.mu.Lock()
	f.replacementToolStarted = now
	f.functionCalls = append(f.functionCalls, call)
	f.mu.Unlock()
	return f.sendToolCall(connection, "response-replacement-tool", call)
}

func (f *familyBProviderFixture) sendToolCall(connection *websocket.Conn, responseID string, call familyBFunctionCall) error {
	if err := f.send(connection, map[string]any{
		"type":     "response.created",
		"response": map[string]string{"id": responseID},
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
		"response": map[string]string{"id": responseID, "status": "completed"},
	})
}

func (f *familyBProviderFixture) handleToolResult(connection *websocket.Conn, callID, output string) error {
	f.mu.Lock()
	var expected string
	var actionID string
	var turnID string
	var toolStarted time.Duration
	switch callID {
	case "call-family-b-original":
		expected = "File written: draft/brief.md"
		actionID = probe.FamilyBOriginalActionID
		turnID = "turn-1"
		toolStarted = f.originalToolStarted
	case "call-family-b-replacement":
		expected = "File written: final/brief.md"
		actionID = probe.FamilyBReplacementActionID
		turnID = "turn-2"
		toolStarted = f.replacementToolStarted
	default:
		f.mu.Unlock()
		return fmt.Errorf("unexpected function call output %q", callID)
	}
	if output != expected {
		f.mu.Unlock()
		return fmt.Errorf("tool result for %q = %q, want %q", callID, output, expected)
	}
	now := f.elapsedLocked()
	observation := probe.ToolObservation{
		ID: "tool-" + strings.TrimPrefix(callID, "call-family-b-"), ActionID: actionID,
		TurnID: turnID, Tool: "write_file", Status: "completed", At: toolStarted,
		Duration: now - toolStarted, ResultSeen: true, Summary: output,
	}
	f.toolObservations = append(f.toolObservations, observation)
	if callID == "call-family-b-original" {
		f.originalResultSeen = true
	} else {
		f.replacementResultSeen = true
	}
	f.mu.Unlock()

	if callID == "call-family-b-original" {
		return f.sendOriginalOutput(connection)
	}
	return f.sendReplacementOutput(connection)
}

func (f *familyBProviderFixture) sendOriginalOutput(connection *websocket.Conn) error {
	// Complete the response that owns the original tool call first. The
	// session is explicitly held open below so the correction can target a
	// distinct assistant response without leaving the tool continuation
	// observer in a partial state.
	if err := f.send(connection, map[string]any{
		"type":     "response.created",
		"response": map[string]string{"id": "response-original-tool-continuation"},
	}); err != nil {
		return err
	}
	if err := f.send(connection, map[string]any{
		"type": "response.output_audio.delta", "delta": base64.StdEncoding.EncodeToString([]byte{9, 0x42, 0x52, 0x42}), "format": "pcm16",
	}); err != nil {
		return err
	}
	if err := f.send(connection, map[string]any{
		"type":     "response.output_audio.done",
		"response": map[string]string{"id": "response-original-tool-continuation"},
	}); err != nil {
		return err
	}
	if err := f.send(connection, map[string]any{
		"type":     "response.done",
		"response": map[string]string{"id": "response-original-tool-continuation", "status": "completed"},
	}); err != nil {
		return err
	}

	f.mu.Lock()
	startedAt := f.elapsedLocked()
	f.originalOutputStarted = startedAt
	f.activeResponse = "response-original-output"
	f.mu.Unlock()
	if err := f.send(connection, map[string]string{"type": "input_audio_buffer.speech_started"}); err != nil {
		return err
	}
	text := "Created draft/brief.md and kept the original draft while I explained the next step."
	f.recordProductTranscript(probe.TranscriptEvent{ID: "product-turn-1", TurnID: "turn-1", Speaker: probe.TranscriptProduct, Text: text, At: startedAt, Final: true})
	if err := f.send(connection, map[string]any{
		"type":     "response.created",
		"response": map[string]string{"id": "response-original-output"},
	}); err != nil {
		return err
	}
	if err := f.send(connection, map[string]string{"type": "response.output_audio_transcript.delta", "delta": text}); err != nil {
		return err
	}
	if err := f.send(connection, map[string]string{"type": "response.output_audio_transcript.done", "transcript": text}); err != nil {
		return err
	}
	audio := []byte{1, 0x42, 0x52, 0x42}
	return f.send(connection, map[string]any{
		"type": "response.output_audio.delta", "delta": base64.StdEncoding.EncodeToString(audio), "format": "pcm16",
	})
}

func (f *familyBProviderFixture) finishCancelledOriginalResponse(connection *websocket.Conn) error {
	f.mu.Lock()
	responseID := f.activeResponse
	endedAt := f.elapsedLocked()
	f.originalOutputEnded = endedAt
	f.activeResponse = ""
	f.cancelPending = false
	f.mu.Unlock()
	if err := f.send(connection, map[string]any{
		"type":     "response.output_audio.done",
		"response": map[string]string{"id": responseID},
	}); err != nil {
		return err
	}
	if err := f.send(connection, map[string]any{
		"type":     "response.done",
		"response": map[string]string{"id": responseID, "status": "cancelled"},
	}); err != nil {
		return err
	}
	f.recordResponseTerminal("cancelled")
	return nil
}

func (f *familyBProviderFixture) sendReplacementOutput(connection *websocket.Conn) error {
	f.mu.Lock()
	startedAt := f.elapsedLocked()
	f.replacementOutputStarted = startedAt
	f.mu.Unlock()
	text := "Created final/brief.md as the corrected release note."
	f.recordProductTranscript(probe.TranscriptEvent{ID: "product-turn-2", TurnID: "turn-2", Speaker: probe.TranscriptProduct, Text: text, At: startedAt, Final: true})
	if err := f.send(connection, map[string]any{
		"type":     "response.created",
		"response": map[string]string{"id": "response-replacement-output"},
	}); err != nil {
		return err
	}
	if err := f.send(connection, map[string]string{"type": "response.output_audio_transcript.delta", "delta": text}); err != nil {
		return err
	}
	if err := f.send(connection, map[string]string{"type": "response.output_audio_transcript.done", "transcript": text}); err != nil {
		return err
	}
	audio := []byte{2, 0x42, 0x52, 0x42}
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
		"response": map[string]string{"id": "response-replacement-output", "status": "completed"},
	}); err != nil {
		return err
	}
	f.mu.Lock()
	f.replacementOutputEnded = f.elapsedLocked()
	f.mu.Unlock()
	f.recordResponseTerminal("completed")
	time.Sleep(25 * time.Millisecond)
	return f.send(connection, map[string]string{"type": "session.closed", "reason": "family_b_correction_complete"})
}

func (f *familyBProviderFixture) sendSessionReady(connection *websocket.Conn) error {
	if err := f.send(connection, map[string]any{
		"type":    "session.created",
		"session": map[string]string{"id": "family-b", "model": "gpt-realtime"},
	}); err != nil {
		return err
	}
	return f.send(connection, map[string]any{
		"type":    "session.updated",
		"session": map[string]string{"id": "family-b"},
	})
}

func (f *familyBProviderFixture) send(connection *websocket.Conn, event any) error {
	return connection.WriteJSON(event)
}

func (f *familyBProviderFixture) recordCustomerTranscript(event probe.TranscriptEvent) {
	f.mu.Lock()
	f.customerTranscript = append(f.customerTranscript, event)
	f.mu.Unlock()
}

func (f *familyBProviderFixture) recordProductTranscript(event probe.TranscriptEvent) {
	f.mu.Lock()
	f.productTranscript = append(f.productTranscript, event)
	f.mu.Unlock()
}

func (f *familyBProviderFixture) recordResponseTerminal(status string) {
	f.mu.Lock()
	f.responseTerminalStatuses = append(f.responseTerminalStatuses, status)
	f.mu.Unlock()
}

func (f *familyBProviderFixture) elapsedLocked() time.Duration {
	if f.startedAt.IsZero() {
		return 0
	}
	return time.Since(f.startedAt)
}

func (f *familyBProviderFixture) failProtocol(message string) {
	f.mu.Lock()
	if f.protocolError == "" {
		f.protocolError = message
	}
	f.mu.Unlock()
}

func familyBToolArguments(path, content string) string {
	data, err := json.Marshal(map[string]string{"path": path, "content": content})
	if err != nil {
		panic("marshal Family B tool arguments: " + err.Error())
	}
	return string(data)
}

func familyBFrame(seed byte) []byte {
	frame := make([]byte, probe.DefaultDuplexFrameSamples*2)
	for index := range frame {
		frame[index] = seed
	}
	return frame
}

func familyBSilent(audio []byte) bool {
	if len(audio) == 0 {
		return true
	}
	for _, value := range audio {
		if value != 0 {
			return false
		}
	}
	return true
}
