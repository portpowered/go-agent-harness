package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
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

// TestFamilyCMixedModalProcessReportsUnsupportedPublicBoundary exercises the
// real shipped process with two completed spoken/text iterations followed by
// a third spoken request whose image cannot be injected through the current
// public --audio-in - boundary. The run remains useful: it records the
// completed text side effects and returns an exact BROKEN product-gap verdict.
func TestFamilyCMixedModalProcessReportsUnsupportedPublicBoundary(t *testing.T) {
	run := runFamilyCProcess(t, "")
	assertFamilyCProcessLifecycle(t, run)
	if len(run.observation.ImageEvents) != 0 {
		t.Fatalf("unsupported run observed image events = %+v, want none", run.observation.ImageEvents)
	}
	if len(run.observation.CustomerTranscript) != 3 || len(run.observation.ProductTranscript) != 3 {
		t.Fatalf("mixed-modal transcripts = customer:%d product:%d, want three ordered turns", len(run.observation.CustomerTranscript), len(run.observation.ProductTranscript))
	}
	assertFamilyCTranscriptOrder(t, run.observation.CustomerTranscript)
	if len(run.checkpoints) != 3 {
		t.Fatalf("filesystem checkpoints = %d, want one boundary checkpoint per action", len(run.checkpoints))
	}
	brief, err := os.ReadFile(filepath.Join(run.sandbox, "mixed-modal", "brief.md"))
	if err != nil {
		t.Fatalf("read completed text brief: %v", err)
	}
	if string(brief) != probe.FamilyCTextBrief {
		t.Fatalf("text brief = %q, want the completed text iteration", brief)
	}
	if _, err := os.Stat(filepath.Join(run.sandbox, "mixed-modal", "image-fact.txt")); !os.IsNotExist(err) {
		t.Fatalf("unsupported image side effect stat = %v, want no image fact", err)
	}

	actionResults := familyCProcessActionResults(run)
	evidence := familyCProcessMixedModalEvidence(run)
	verdict, err := probe.EvaluateCustomerSimulationMixedModal(run.scenario, actionResults, run.checkpoints, run.observation.ToolObservations, run.observation.ProductTranscript, evidence)
	if err != nil {
		t.Fatalf("evaluate unsupported Family C process run: %v", err)
	}
	if verdict.Pass || !familyCProcessFindingContains(verdict, "unsupported_mid_session_image") {
		t.Fatalf("unsupported Family C process verdict = %+v, want BROKEN unsupported-image finding", verdict)
	}
	finding := familyCProcessFinding(verdict, "unsupported_mid_session_image")
	if !strings.Contains(finding.Message, probe.FamilyCMidSessionImageGapCode) || !strings.Contains(finding.Message, "--audio-in -") {
		t.Fatalf("unsupported Family C finding = %+v, want exact public boundary gap", finding)
	}
}

// TestFamilyCMixedModalProcessRejectsPreloadedAndWrongImageControls keeps the
// same shipped-process fake-provider path but exercises the two tempting
// shortcuts the scenario must reject: attaching the expected image at
// startup, and attaching a different valid image. The provider sees both
// image payloads, so rejection is based on observed ordering and digest, not
// on a source-layout scan.
func TestFamilyCMixedModalProcessRejectsPreloadedAndWrongImageControls(t *testing.T) {
	expectedPath := filepath.Join(agentCLIRoot(t), "testdata", "images", "fixture.png")
	if _, err := os.Stat(expectedPath); err != nil {
		t.Fatalf("expected Family C image fixture: %v", err)
	}
	wrongPath := writeFamilyCWrongImage(t)
	for _, testCase := range []struct {
		name      string
		imagePath string
		finding   string
		wantHash  string
	}{
		{name: "preloaded expected image", imagePath: expectedPath, finding: "image_preloaded_before_prior_turn", wantHash: probe.FamilyCImageFixtureSHA256},
		{name: "wrong image payload", imagePath: wrongPath, finding: "wrong_image_payload", wantHash: ""},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			run := runFamilyCProcess(t, testCase.imagePath)
			assertFamilyCProcessLifecycle(t, run)
			if len(run.observation.ImageEvents) != 1 {
				t.Fatalf("provider image events = %+v, want one startup image event", run.observation.ImageEvents)
			}
			if run.observation.ImageEvents[0].At >= run.observation.CustomerTranscript[0].At {
				t.Fatalf("startup image at %s was not observed before first spoken turn at %s", run.observation.ImageEvents[0].At, run.observation.CustomerTranscript[0].At)
			}
			if testCase.wantHash != "" && run.observation.ImageEvents[0].SHA256 != testCase.wantHash {
				t.Fatalf("provider image hash = %q, want %q", run.observation.ImageEvents[0].SHA256, testCase.wantHash)
			}
			actionResults := familyCProcessActionResults(run)
			evidence := familyCProcessMixedModalEvidence(run)
			verdict, err := probe.EvaluateCustomerSimulationMixedModal(run.scenario, actionResults, run.checkpoints, run.observation.ToolObservations, run.observation.ProductTranscript, evidence)
			if err != nil {
				t.Fatalf("evaluate %s control: %v", testCase.name, err)
			}
			if verdict.Pass || !familyCProcessFindingContains(verdict, testCase.finding) {
				t.Fatalf("%s verdict = %+v, want %q finding", testCase.name, verdict, testCase.finding)
			}
		})
	}
}

type familyCProcessRun struct {
	scenario    probe.CustomerScenario
	sandbox     string
	result      probe.DuplexRunResult
	runErr      error
	observation familyCProviderObservation
	checkpoints []probe.FilesystemCheckpoint
}

type familyCImageObservation struct {
	At     time.Duration
	SHA256 string
	Bytes  int
}

type familyCFunctionCall struct {
	ID       string
	ActionID string
	Name     string
	Args     string
	Started  time.Duration
}

type familyCProviderObservation struct {
	ConnectionCount    int
	SessionUpdates     int
	ImageEvents        []familyCImageObservation
	FunctionCalls      []familyCFunctionCall
	ToolObservations   []probe.ToolObservation
	CustomerTranscript []probe.TranscriptEvent
	ProductTranscript  []probe.TranscriptEvent
	ProtocolError      string
}

type familyCProviderFixture struct {
	server   *httptest.Server
	upgrader websocket.Upgrader
	scenario probe.CustomerScenario

	mu                 sync.Mutex
	startedAt          time.Time
	connectionCount    int
	sessionUpdates     int
	imageEvents        []familyCImageObservation
	functionCalls      []familyCFunctionCall
	toolObservations   []probe.ToolObservation
	customerTranscript []probe.TranscriptEvent
	productTranscript  []probe.TranscriptEvent
	protocolError      string
	actionIndex        int
	pendingCall        *familyCFunctionCall
	pendingResult      bool
}

func runFamilyCProcess(t *testing.T, imagePath string) familyCProcessRun {
	t.Helper()
	scenario := loadFamilyCScenario(t)
	sandbox := filepath.Join(t.TempDir(), "sandbox")
	if err := os.MkdirAll(sandbox, 0o700); err != nil {
		t.Fatalf("create Family C sandbox: %v", err)
	}
	oracle, err := probe.NewFilesystemOracle(sandbox)
	if err != nil {
		t.Fatalf("NewFilesystemOracle: %v", err)
	}
	fixture := newFamilyCProviderFixture(scenario)
	defer fixture.Close()
	startedAt := time.Now()
	fixture.SetStartedAt(startedAt)
	configDir := filepath.Join(t.TempDir(), "config")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("create Family C config directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte("tools:\n  exec:\n    enable_deny_patterns: true\n"), 0o600); err != nil {
		t.Fatalf("write Family C config: %v", err)
	}

	var checkpointMu sync.Mutex
	checkpoints := make([]probe.FilesystemCheckpoint, 0, len(scenario.Actions))
	captureCheckpoint := func(actionIndex int) probe.DuplexSegmentGate {
		return func(_ context.Context, _ *probe.DuplexProgress) error {
			action := scenario.Actions[actionIndex]
			checkpoint, _ := oracle.CaptureCheckpoint(
				"checkpoint-"+action.ID,
				action.ID,
				time.Since(startedAt),
				action.Oracle.Checkpoints,
			)
			checkpointMu.Lock()
			checkpoints = append(checkpoints, checkpoint)
			checkpointMu.Unlock()
			return nil
		}
	}

	additionalArgs := []string(nil)
	if imagePath != "" {
		additionalArgs = []string{"--image", imagePath}
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
		AdditionalArgs:   additionalArgs,
		Segments: []probe.DuplexAudioSegment{
			{ID: "turn-1-speech", PCM16: familyCFrame(1)},
			{ID: "turn-1-silence", SilenceFor: 5 * time.Millisecond},
			{ID: "turn-2-speech", PCM16: familyCFrame(2), WaitForOutputBytes: 4, Before: captureCheckpoint(0)},
			{ID: "turn-2-silence", SilenceFor: 5 * time.Millisecond},
			{ID: "turn-3-speech", PCM16: familyCFrame(3), WaitForOutputBytes: 8, Before: captureCheckpoint(1)},
			{ID: "turn-3-silence", SilenceFor: 5 * time.Millisecond, WaitForOutputBytes: 12, Before: captureCheckpoint(2)},
		},
	})
	checkpointMu.Lock()
	checkpointCopy := append([]probe.FilesystemCheckpoint(nil), checkpoints...)
	checkpointMu.Unlock()
	return familyCProcessRun{
		scenario: scenario, sandbox: sandbox, result: result, runErr: runErr,
		observation: fixture.Snapshot(), checkpoints: checkpointCopy,
	}
}

func loadFamilyCScenario(t *testing.T) probe.CustomerScenario {
	t.Helper()
	path := filepath.Join(agentCLIRoot(t), "testdata", "customer-simulation", "family-c.scenario.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read Family C scenario: %v", err)
	}
	scenario, err := probe.ParseCustomerScenario(data)
	if err != nil {
		t.Fatalf("parse Family C scenario: %v", err)
	}
	return scenario
}

func newFamilyCProviderFixture(scenario probe.CustomerScenario) *familyCProviderFixture {
	fixture := &familyCProviderFixture{
		upgrader: websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }},
		scenario: scenario,
	}
	fixture.server = httptest.NewServer(http.HandlerFunc(fixture.handle))
	return fixture
}

func (f *familyCProviderFixture) SetStartedAt(startedAt time.Time) {
	f.mu.Lock()
	f.startedAt = startedAt
	f.mu.Unlock()
}

func (f *familyCProviderFixture) WebSocketURL() string {
	return strings.Replace(f.server.URL, "http://", "ws://", 1)
}

func (f *familyCProviderFixture) Close() {
	if f.server != nil {
		f.server.Close()
	}
}

func (f *familyCProviderFixture) Snapshot() familyCProviderObservation {
	f.mu.Lock()
	defer f.mu.Unlock()
	return familyCProviderObservation{
		ConnectionCount:    f.connectionCount,
		SessionUpdates:     f.sessionUpdates,
		ImageEvents:        append([]familyCImageObservation(nil), f.imageEvents...),
		FunctionCalls:      append([]familyCFunctionCall(nil), f.functionCalls...),
		ToolObservations:   append([]probe.ToolObservation(nil), f.toolObservations...),
		CustomerTranscript: append([]probe.TranscriptEvent(nil), f.customerTranscript...),
		ProductTranscript:  append([]probe.TranscriptEvent(nil), f.productTranscript...),
		ProtocolError:      f.protocolError,
	}
}

func (f *familyCProviderFixture) handle(writer http.ResponseWriter, request *http.Request) {
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
				Type    string `json:"type"`
				CallID  string `json:"call_id"`
				Output  string `json:"output"`
				Content []struct {
					Type     string `json:"type"`
					ImageURL string `json:"image_url"`
				} `json:"content"`
			} `json:"item"`
		}
		if err := json.Unmarshal(payload, &event); err != nil {
			f.failProtocol("decode client event: " + err.Error())
			return
		}
		switch event.Type {
		case "session.update":
			f.mu.Lock()
			f.sessionUpdates++
			f.mu.Unlock()
			if err := f.sendSessionReady(connection); err != nil {
				return
			}
		case "conversation.item.create":
			if event.Item.Type == "function_call_output" {
				if err := f.handleToolResult(connection, event.Item.CallID, event.Item.Output); err != nil {
					f.failProtocol(err.Error())
					return
				}
				continue
			}
			if err := f.handleImage(event.Item.Content); err != nil {
				f.failProtocol(err.Error())
				return
			}
		case "input_audio_buffer.append":
			audio, decodeErr := base64.StdEncoding.DecodeString(event.Audio)
			if decodeErr != nil {
				f.failProtocol("decode input audio: " + decodeErr.Error())
				return
			}
			if familyCSilent(audio) {
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
		case "response.create":
			if err := f.handleContinuation(connection); err != nil {
				f.failProtocol(err.Error())
				return
			}
		case "input_audio_buffer.commit", "response.cancel":
			// The fixture models the server-VAD-shaped commit and accepts the
			// normal client controls without creating a second response.
		default:
			// session.created and provider metadata are server-to-client only;
			// optional client events are irrelevant to this image-boundary proof.
		}
	}
}

func (f *familyCProviderFixture) handleImage(content []struct {
	Type     string `json:"type"`
	ImageURL string `json:"image_url"`
}) error {
	for _, part := range content {
		if part.Type != "input_image" {
			continue
		}
		comma := strings.IndexByte(part.ImageURL, ',')
		if comma < 0 {
			return fmt.Errorf("image content has no data URL separator")
		}
		data, err := base64.StdEncoding.DecodeString(part.ImageURL[comma+1:])
		if err != nil {
			return fmt.Errorf("decode image data URL: %w", err)
		}
		digest := sha256.Sum256(data)
		f.mu.Lock()
		f.imageEvents = append(f.imageEvents, familyCImageObservation{At: f.elapsedLocked(), SHA256: hex.EncodeToString(digest[:]), Bytes: len(data)})
		f.mu.Unlock()
	}
	return nil
}

func (f *familyCProviderFixture) handleCustomerUtterance(connection *websocket.Conn) error {
	f.mu.Lock()
	index := f.actionIndex
	f.actionIndex++
	startedAt := f.elapsedLocked()
	f.mu.Unlock()
	script := probe.FamilyCSpokenScript()
	if index >= len(script) {
		return fmt.Errorf("received unexpected customer utterance %d", index+1)
	}
	turnID := fmt.Sprintf("turn-%d", index+1)
	f.recordCustomerTranscript(probe.TranscriptEvent{ID: "customer-" + turnID, TurnID: turnID, Speaker: probe.TranscriptCustomer, Text: script[index].Text, At: startedAt, Final: true})

	if index == 2 {
		f.mu.Lock()
		imageAvailable := len(f.imageEvents) > 0
		f.mu.Unlock()
		if !imageAvailable {
			return f.sendConfirmation(connection, turnID, "I cannot apply the image-grounded request because the public mid-session image boundary is unsupported.", 3)
		}
	}

	call := familyCFunctionCall{
		ID:       fmt.Sprintf("call-family-c-%d", index+1),
		ActionID: f.scenario.Actions[index].ID,
		Name:     familyCToolName(index),
		Args:     familyCToolArguments(index),
		Started:  startedAt,
	}
	f.mu.Lock()
	f.pendingCall = &call
	f.pendingResult = false
	f.functionCalls = append(f.functionCalls, call)
	f.mu.Unlock()
	return f.sendToolCall(connection, "response-turn-tool-"+fmt.Sprint(index+1), call)
}

func (f *familyCProviderFixture) handleToolResult(connection *websocket.Conn, callID, output string) error {
	f.mu.Lock()
	pending := f.pendingCall
	if pending == nil || pending.ID != callID {
		f.mu.Unlock()
		return fmt.Errorf("tool result %q arrived without the expected pending call", callID)
	}
	expected := familyCToolOutput(pending.ActionID)
	if output != expected {
		f.mu.Unlock()
		return fmt.Errorf("tool result for %q = %q, want %q", callID, output, expected)
	}
	now := f.elapsedLocked()
	f.toolObservations = append(f.toolObservations, probe.ToolObservation{
		ID: "tool-" + pending.ID, ActionID: pending.ActionID, TurnID: fmt.Sprintf("turn-%d", f.actionIndex), Tool: pending.Name,
		Status: "completed", At: pending.Started, Duration: now - pending.Started, ResultSeen: true, Summary: output,
	})
	f.pendingResult = true
	f.mu.Unlock()
	return nil
}

func (f *familyCProviderFixture) handleContinuation(connection *websocket.Conn) error {
	f.mu.Lock()
	pending := f.pendingCall
	ready := f.pendingResult
	f.mu.Unlock()
	if pending == nil || !ready {
		return nil
	}
	var text string
	switch pending.ActionID {
	case probe.FamilyCCreateActionID:
		text = "Created mixed-modal/brief.md."
	case probe.FamilyCTextActionID:
		text = "Updated mixed-modal/brief.md with audience: engineers and a concise tone."
	case probe.FamilyCImageActionID:
		text = "Recorded indigo (#4f46e5) in mixed-modal/image-fact.txt."
	default:
		return fmt.Errorf("unknown Family C action %q", pending.ActionID)
	}
	marker := byte(f.actionIndex)
	if err := f.sendConfirmation(connection, fmt.Sprintf("turn-%d", f.actionIndex), text, marker); err != nil {
		return err
	}
	f.mu.Lock()
	f.pendingCall = nil
	f.pendingResult = false
	f.mu.Unlock()
	return nil
}

func (f *familyCProviderFixture) sendToolCall(connection *websocket.Conn, responseID string, call familyCFunctionCall) error {
	if err := f.send(connection, map[string]any{"type": "response.created", "response": map[string]string{"id": responseID}}); err != nil {
		return err
	}
	if err := f.send(connection, map[string]any{
		"type": "response.output_item.added",
		"item": map[string]string{"type": "function_call", "id": call.ID, "call_id": call.ID, "name": call.Name, "arguments": ""},
	}); err != nil {
		return err
	}
	if err := f.send(connection, map[string]any{"type": "response.function_call_arguments.done", "call_id": call.ID, "name": call.Name, "arguments": call.Args}); err != nil {
		return err
	}
	return f.send(connection, map[string]any{"type": "response.done", "response": map[string]string{"id": responseID, "status": "completed"}})
}

func (f *familyCProviderFixture) sendConfirmation(connection *websocket.Conn, turnID, text string, marker byte) error {
	at := f.elapsed()
	f.recordProductTranscript(probe.TranscriptEvent{ID: "product-" + turnID, TurnID: turnID, Speaker: probe.TranscriptProduct, Text: text, At: at, Final: true})
	if err := f.send(connection, map[string]any{"type": "response.created", "response": map[string]string{"id": "response-" + turnID}}); err != nil {
		return err
	}
	if err := f.send(connection, map[string]string{"type": "response.output_audio_transcript.delta", "delta": text}); err != nil {
		return err
	}
	if err := f.send(connection, map[string]string{"type": "response.output_audio_transcript.done", "transcript": text}); err != nil {
		return err
	}
	audio := []byte{marker, 0x43, 0x4d, 0x43}
	if err := f.send(connection, map[string]any{"type": "response.output_audio.delta", "delta": base64.StdEncoding.EncodeToString(audio), "format": "pcm16"}); err != nil {
		return err
	}
	if err := f.send(connection, map[string]string{"type": "response.output_audio.done"}); err != nil {
		return err
	}
	if err := f.send(connection, map[string]any{"type": "response.done", "response": map[string]string{"id": "response-" + turnID, "status": "completed"}}); err != nil {
		return err
	}
	if marker == 3 {
		time.Sleep(25 * time.Millisecond)
		return f.send(connection, map[string]string{"type": "session.closed", "reason": "family_c_complete"})
	}
	return nil
}

func (f *familyCProviderFixture) sendSessionReady(connection *websocket.Conn) error {
	if err := f.send(connection, map[string]any{"type": "session.created", "session": map[string]string{"id": "family-c", "model": "gpt-realtime"}}); err != nil {
		return err
	}
	return f.send(connection, map[string]any{"type": "session.updated", "session": map[string]string{"id": "family-c"}})
}

func (f *familyCProviderFixture) send(connection *websocket.Conn, event any) error {
	return connection.WriteJSON(event)
}

func (f *familyCProviderFixture) recordCustomerTranscript(event probe.TranscriptEvent) {
	f.mu.Lock()
	f.customerTranscript = append(f.customerTranscript, event)
	f.mu.Unlock()
}

func (f *familyCProviderFixture) recordProductTranscript(event probe.TranscriptEvent) {
	f.mu.Lock()
	f.productTranscript = append(f.productTranscript, event)
	f.mu.Unlock()
}

func (f *familyCProviderFixture) elapsed() time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.elapsedLocked()
}

func (f *familyCProviderFixture) elapsedLocked() time.Duration {
	if f.startedAt.IsZero() {
		return 0
	}
	return time.Since(f.startedAt)
}

func (f *familyCProviderFixture) failProtocol(message string) {
	f.mu.Lock()
	if f.protocolError == "" {
		f.protocolError = message
	}
	f.mu.Unlock()
}

func familyCProcessActionResults(run familyCProcessRun) []probe.ActionResult {
	checkpointIDs := make(map[string]string, len(run.checkpoints))
	for _, checkpoint := range run.checkpoints {
		checkpointIDs[checkpoint.ActionID] = checkpoint.ID
	}
	toolIDs := make(map[string]string, len(run.observation.ToolObservations))
	for _, observation := range run.observation.ToolObservations {
		toolIDs[observation.ActionID] = observation.ID
	}
	productByTurn := make(map[string]probe.TranscriptEvent, len(run.observation.ProductTranscript))
	for _, event := range run.observation.ProductTranscript {
		productByTurn[event.TurnID] = event
	}
	results := make([]probe.ActionResult, 0, len(run.scenario.Actions))
	for index, action := range run.scenario.Actions {
		turnID := fmt.Sprintf("turn-%d", index+1)
		result := probe.ActionResult{
			ActionID: action.ID, TurnID: turnID, EvidenceRefs: familyCProcessEvidenceRefs(),
			CheckpointIDs: []string{checkpointIDs[action.ID]},
		}
		if event, ok := productByTurn[turnID]; ok {
			result.Confirmed = true
			result.ConfirmedAt = event.At
		}
		if toolID, ok := toolIDs[action.ID]; ok {
			result.ToolObservationIDs = []string{toolID}
		}
		if action.ID == probe.FamilyCImageActionID && len(result.ToolObservationIDs) == 0 {
			result.Disposition = probe.DispositionFailed
			result.OutcomeReason = probe.FamilyCMidSessionImageGap
			result.Confirmed = false
		} else {
			result.Disposition = probe.DispositionCompleted
		}
		results = append(results, result)
	}
	return results
}

func familyCProcessMixedModalEvidence(run familyCProcessRun) probe.MixedModalEvidence {
	priorCompletedAt := time.Duration(0)
	if len(run.checkpoints) > 1 {
		priorCompletedAt = run.checkpoints[1].At
	}
	customerTurnAt := time.Duration(0)
	if len(run.observation.CustomerTranscript) > 2 {
		customerTurnAt = run.observation.CustomerTranscript[2].At
	}
	evidence := probe.MixedModalEvidence{
		ImageEventID: probe.FamilyCImageEventID, PriorActionID: probe.FamilyCTextActionID,
		PriorTurnID: "turn-2", ImageTurnID: "turn-3", PriorActionCompletedAt: priorCompletedAt,
		CustomerTurnStartedAt: customerTurnAt, ExpectedSHA256: run.scenario.ImageEvents[0].SHA256,
		EvidenceRefs:                 []string{"scenario.json", "transcripts/customer.jsonl", "transcripts/product.jsonl", "events/audio-turn-events.jsonl", "events/mixed-modal.json"},
		ImageMeaningInCustomerSpeech: false,
	}
	if len(run.observation.ImageEvents) == 0 {
		evidence.Delivery = probe.MixedModalDeliveryUnsupported
		evidence.ProductGapCode = probe.FamilyCMidSessionImageGapCode
		evidence.ProductGap = probe.FamilyCMidSessionImageGap
		return evidence
	}
	image := run.observation.ImageEvents[0]
	evidence.ImageObserved = true
	evidence.Supported = true
	evidence.ImageSentAt = image.At
	evidence.ObservedSHA256 = image.SHA256
	if image.SHA256 == run.scenario.ImageEvents[0].SHA256 {
		evidence.Delivery = probe.MixedModalDeliveryPreloaded
	} else {
		evidence.Delivery = probe.MixedModalDeliveryWrongImage
	}
	return evidence
}

func familyCProcessEvidenceRefs() []string {
	return []string{"filesystem-checkpoints.jsonl", "tool-observations.jsonl", "transcripts/product.jsonl"}
}

func familyCProcessFindingContains(verdict probe.MechanicalVerdict, code string) bool {
	for _, finding := range verdict.Findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}

func familyCProcessFinding(verdict probe.MechanicalVerdict, code string) probe.MechanicalFinding {
	for _, finding := range verdict.Findings {
		if finding.Code == code {
			return finding
		}
	}
	return probe.MechanicalFinding{}
}

func assertFamilyCProcessLifecycle(t *testing.T, run familyCProcessRun) {
	t.Helper()
	if run.runErr != nil || run.observation.ProtocolError != "" {
		t.Fatalf("Family C shipped-process run failed: run=%v provider=%+v result=%+v stdout=%x stderr=%s", run.runErr, run.observation, run.result, run.result.Stdout, run.result.Stderr)
	}
	if run.observation.ConnectionCount != 1 || run.observation.SessionUpdates != 1 {
		t.Fatalf("provider lifecycle = connections:%d session_updates:%d, want one open session and one update", run.observation.ConnectionCount, run.observation.SessionUpdates)
	}
	if run.result.ExitCode != 0 || !run.result.ChildWaited || !run.result.InputFinished || !run.result.InputClosed || !run.result.StdoutClosed || !run.result.StderrClosed {
		t.Fatalf("process lifecycle result = %+v, want fully reaped normal run", run.result)
	}
	if len(run.result.Input) != 6 || len(run.result.Output) == 0 {
		t.Fatalf("duplex evidence = input frames:%d output reads:%d, want six incremental input frames and streamed output", len(run.result.Input), len(run.result.Output))
	}
}

func assertFamilyCTranscriptOrder(t *testing.T, transcript []probe.TranscriptEvent) {
	t.Helper()
	for index := 1; index < len(transcript); index++ {
		if transcript[index].At <= transcript[index-1].At {
			t.Fatalf("customer transcript timestamps = %v, want strict turn order", transcript)
		}
		if transcript[index].TurnID != fmt.Sprintf("turn-%d", index+1) {
			t.Fatalf("customer transcript turn %d = %q, want turn-%d", index, transcript[index].TurnID, index+1)
		}
	}
}

func familyCFrame(seed byte) []byte {
	frame := make([]byte, probe.DefaultDuplexFrameSamples*2)
	for index := range frame {
		frame[index] = seed
	}
	return frame
}

func familyCSilent(audio []byte) bool {
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

func familyCToolName(index int) string {
	return []string{"write_file", "edit_file", "write_file"}[index]
}

func familyCToolArguments(index int) string {
	arguments := []map[string]string{
		{"path": "mixed-modal/brief.md", "content": probe.FamilyCInitialBrief},
		{"path": "mixed-modal/brief.md", "old_text": probe.FamilyCInitialBrief, "new_text": probe.FamilyCTextBrief},
		{"path": "mixed-modal/image-fact.txt", "content": probe.FamilyCImageFact},
	}
	data, err := json.Marshal(arguments[index])
	if err != nil {
		panic("marshal Family C tool arguments: " + err.Error())
	}
	return string(data)
}

func familyCToolOutput(actionID string) string {
	switch actionID {
	case probe.FamilyCCreateActionID:
		return "File written: mixed-modal/brief.md"
	case probe.FamilyCTextActionID:
		return "File edited: mixed-modal/brief.md"
	case probe.FamilyCImageActionID:
		return "File written: mixed-modal/image-fact.txt"
	default:
		return ""
	}
}

func writeFamilyCWrongImage(t *testing.T) string {
	t.Helper()
	var data bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{R: 0xee, G: 0x44, B: 0x44, A: 0xff})
	if err := png.Encode(&data, img); err != nil {
		t.Fatalf("encode wrong Family C image: %v", err)
	}
	path := filepath.Join(t.TempDir(), "wrong.png")
	if err := os.WriteFile(path, data.Bytes(), 0o600); err != nil {
		t.Fatalf("write wrong Family C image: %v", err)
	}
	return path
}
