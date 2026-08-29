package integration

import (
	"bufio"
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

type familyEShippedMode string

const (
	familyEShippedNormal   familyEShippedMode = "normal"
	familyEShippedRecovery familyEShippedMode = "recovery"
	familyEShippedDeadAir  familyEShippedMode = "dead-air"
)

// TestShippedSessionProcessFamilyEPatienceNormal exercises the production
// suite builder through the shipped child for a response that completes before
// the customer needs to check in.
func TestShippedSessionProcessFamilyEPatienceNormal(t *testing.T) {
	run, runErr, observation := runFamilyEShipped(t, familyEShippedNormal)
	if runErr != nil {
		t.Fatalf("normal Family E run: %v", runErr)
	}
	patience := readFamilyEPatience(t, run)
	if !run.Mechanical.Pass || !run.Validator.Pass() {
		t.Fatalf("normal Family E result = %+v, want accepted WORKED", run)
	}
	if patience.Outcome != probe.PatienceOutcomeCompleted || patience.RepromptCount != 0 {
		t.Fatalf("normal patience evidence = %+v, want completed without a re-prompt", patience)
	}
	if observation.UtteranceCount != 1 || len(observation.ProductTranscript) != 1 {
		t.Fatalf("normal provider observation = %+v, want one customer turn and one response", observation)
	}
}

// TestShippedSessionProcessFamilyEPatienceRecovery proves that the same child
// receives an actual incremental check-in after the policy threshold and that
// later product audio completes the original turn. The test never calls the
// runtime or provider adapter directly; all coordination is through the
// shipped PCM boundaries and observable stdout.
func TestShippedSessionProcessFamilyEPatienceRecovery(t *testing.T) {
	run, runErr, observation := runFamilyEShipped(t, familyEShippedRecovery)
	if runErr != nil {
		t.Fatalf("recovery Family E run: %v", runErr)
	}
	patience := readFamilyEPatience(t, run)
	if !run.Mechanical.Pass || !run.Validator.Pass() {
		t.Fatalf("recovery Family E result = %+v, want accepted WORKED", run)
	}
	if patience.Outcome != probe.PatienceOutcomeCompleted || patience.RepromptCount != 1 || len(patience.Reprompts) != 1 {
		t.Fatalf("recovery patience evidence = %+v, want one bounded re-prompt and completion", patience)
	}
	if observation.UtteranceCount != 2 || len(observation.ProductTranscript) != 2 {
		t.Fatalf("recovery provider observation = %+v, want two customer turns and two responses", observation)
	}
	audioEvents := readFamilyEAudioEvents(t, run)
	if countFamilyEAudioEvents(audioEvents, "input") < 3 || countFamilyEAudioEvents(audioEvents, "output") < 2 {
		t.Fatalf("recovery audio events = %+v, want incremental re-prompt input and post-re-prompt output", audioEvents)
	}
}

// TestShippedSessionProcessFamilyEPatienceInitialOutputThenStall proves that
// initial product audio does not get stretched across the later wait. After a
// bounded re-prompt the fixture stays silent, so the production controller
// must report dead air and the bundle must contain no fabricated completion.
func TestShippedSessionProcessFamilyEPatienceInitialOutputThenStall(t *testing.T) {
	run, runErr, observation := runFamilyEShipped(t, familyEShippedDeadAir)
	if runErr == nil {
		t.Fatal("stalled Family E run error = nil, want dead-air failure")
	}
	patience := readFamilyEPatience(t, run)
	if run.Mechanical.Pass || run.Validator.Pass() {
		t.Fatalf("stalled Family E result = %+v, want BROKEN", run)
	}
	if patience.Outcome != probe.PatienceOutcomeDeadAir || patience.RepromptCount != 1 || patience.DeadAirAt <= patience.Reprompts[0].At {
		t.Fatalf("stalled patience evidence = %+v, want one re-prompt followed by dead air", patience)
	}
	if observation.UtteranceCount != 2 || len(observation.ProductTranscript) != 1 {
		t.Fatalf("stalled provider observation = %+v, want two customer turns and only initial product output", observation)
	}
	product := readFamilyEProductTranscript(t, run)
	if len(product) != 1 || !strings.Contains(product[0].Text, "still working") || strings.Contains(strings.ToLower(product[0].Text), "request is complete") {
		t.Fatalf("stalled product transcript = %+v, want only the initial progress response", product)
	}
}

func runFamilyEShipped(t *testing.T, mode familyEShippedMode) (probe.CustomerSimulationRunResult, error, familyEProviderObservation) {
	t.Helper()
	scenario := familyEShippedScenario()
	fixture := newFamilyEProviderFixture(scenario, mode)
	fixture.SetStartedAt(time.Now())

	validator := probe.CustomerSimulationValidatorAgentFunc(func(_ context.Context, request probe.CustomerSimulationValidatorRequest) ([]byte, error) {
		refs := append([]string(nil), request.Input.EvidenceRefs...)
		if request.Input.Mechanical.Pass {
			return json.Marshal(probe.ValidatorVerdict{
				Verdict:      probe.ValidatorWorked,
				Summary:      "The customer received observable progress and a truthful terminal response within the declared patience policy.",
				EvidenceRefs: refs,
			})
		}
		return json.Marshal(probe.ValidatorVerdict{
			Verdict:          probe.ValidatorBroken,
			FirstFailingTurn: probe.FamilyETurnID,
			Behavior:         "The product emitted initial progress, then stopped producing observable progress after the customer check-in.",
			Violation:        "the patience controller declared dead air",
			EvidenceRefs:     refs,
			CustomerImpact:   "The customer could not rely on the stalled response to finish.",
		})
	})

	result, runErr := probe.RunCustomerSimulationSuite(context.Background(), probe.CustomerSimulationSuiteOptions{
		BinaryPath: buildAgentBinary(t), RunRoot: filepath.Join(t.TempDir(), "runs"), Provider: "openai", Model: "gpt-realtime",
		BaseURL: fixture.WebSocketURL(), APIKey: "hermetic-key", SystemPrompt: scenario.TextSeed,
		Runs: []probe.CustomerSimulationRunSpec{{
			Scenario: scenario, Script: probe.FamilyESpokenScript(), Audio: [][]byte{familyEFrame(1)},
			PatienceRepromptAudio: familyEFrame(2),
		}},
		Validator: validator, MaxDuration: scenario.Deadline, FrameDuration: time.Millisecond, SilenceDuration: 5 * time.Millisecond, ShutdownGrace: time.Second,
	})
	observation := fixture.Snapshot()
	fixture.Close()
	if len(result.Runs) != 1 {
		t.Fatalf("Family E run count = %d, want one", len(result.Runs))
	}
	return result.Runs[0], runErr, observation
}

func familyEShippedScenario() probe.CustomerScenario {
	scenario := probe.NewFamilyEScenario()
	scenario.ID = "family-e-shipped-child"
	scenario.Name = "Shipped child patience test"
	scenario.Patience = probe.PatienceThresholds{
		ListenBeforeFollowUp: time.Millisecond,
		ResponseStart:        2 * time.Millisecond,
		InProgressWork:       4 * time.Millisecond,
		Reprompt:             35 * time.Millisecond,
		AbsoluteDeadAir:      90 * time.Millisecond,
		MaxReprompts:         1,
	}
	scenario.Deadline = 500 * time.Millisecond
	return scenario
}

func readFamilyEPatience(t *testing.T, run probe.CustomerSimulationRunResult) probe.PatienceEvidence {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(run.BundleRoot, probe.FamilyEPatienceEventPath))
	if err != nil {
		t.Fatalf("read Family E patience evidence: %v", err)
	}
	var evidence probe.PatienceEvidence
	if err := json.Unmarshal(data, &evidence); err != nil {
		t.Fatalf("decode Family E patience evidence: %v", err)
	}
	return evidence
}

func readFamilyEProductTranscript(t *testing.T, run probe.CustomerSimulationRunResult) []probe.TranscriptEvent {
	t.Helper()
	return readFamilyEJSONL[probe.TranscriptEvent](t, filepath.Join(run.BundleRoot, "transcripts", "product.jsonl"))
}

func readFamilyEAudioEvents(t *testing.T, run probe.CustomerSimulationRunResult) []probe.AudioTurnEvent {
	t.Helper()
	return readFamilyEJSONL[probe.AudioTurnEvent](t, filepath.Join(run.BundleRoot, "events", "audio-turn-events.jsonl"))
}

func readFamilyEJSONL[T any](t *testing.T, path string) []T {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer file.Close()
	var values []T
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var value T
		if err := json.Unmarshal(scanner.Bytes(), &value); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		values = append(values, value)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return values
}

func countFamilyEAudioEvents(events []probe.AudioTurnEvent, direction string) int {
	count := 0
	for _, event := range events {
		if event.Direction == direction {
			count++
		}
	}
	return count
}

type familyEProviderObservation struct {
	ConnectionCount    int
	SessionUpdates     int
	UtteranceCount     int
	CustomerTranscript []probe.TranscriptEvent
	ProductTranscript  []probe.TranscriptEvent
	ProtocolError      string
}

type familyEProviderFixture struct {
	server   *httptest.Server
	upgrader websocket.Upgrader
	scenario probe.CustomerScenario
	mode     familyEShippedMode

	mu                 sync.Mutex
	startedAt          time.Time
	connectionCount    int
	sessionUpdates     int
	utteranceCount     int
	customerTranscript []probe.TranscriptEvent
	productTranscript  []probe.TranscriptEvent
	protocolError      string
}

func newFamilyEProviderFixture(scenario probe.CustomerScenario, mode familyEShippedMode) *familyEProviderFixture {
	fixture := &familyEProviderFixture{
		upgrader: websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }},
		scenario: scenario,
		mode:     mode,
	}
	fixture.server = httptest.NewServer(http.HandlerFunc(fixture.handle))
	return fixture
}

func (f *familyEProviderFixture) SetStartedAt(startedAt time.Time) {
	f.mu.Lock()
	f.startedAt = startedAt
	f.mu.Unlock()
}

func (f *familyEProviderFixture) WebSocketURL() string {
	return strings.Replace(f.server.URL, "http://", "ws://", 1)
}

func (f *familyEProviderFixture) Close() {
	if f.server != nil {
		f.server.Close()
	}
}

func (f *familyEProviderFixture) Snapshot() familyEProviderObservation {
	f.mu.Lock()
	defer f.mu.Unlock()
	return familyEProviderObservation{
		ConnectionCount:    f.connectionCount,
		SessionUpdates:     f.sessionUpdates,
		UtteranceCount:     f.utteranceCount,
		CustomerTranscript: append([]probe.TranscriptEvent(nil), f.customerTranscript...),
		ProductTranscript:  append([]probe.TranscriptEvent(nil), f.productTranscript...),
		ProtocolError:      f.protocolError,
	}
}

func (f *familyEProviderFixture) handle(writer http.ResponseWriter, request *http.Request) {
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
		case "input_audio_buffer.append":
			audio, decodeErr := base64.StdEncoding.DecodeString(event.Audio)
			if decodeErr != nil {
				f.failProtocol("decode input audio: " + decodeErr.Error())
				return
			}
			if familyESilent(audio) {
				if err := f.send(connection, map[string]string{"type": "input_audio_buffer.speech_stopped"}); err != nil {
					return
				}
				if err := f.send(connection, map[string]string{"type": "input_audio_buffer.committed"}); err != nil {
					return
				}
				continue
			}
			if err := f.handleUtterance(connection); err != nil {
				f.failProtocol(err.Error())
				return
			}
		case "input_audio_buffer.commit", "response.create", "response.cancel":
			// The fixture observes the audio and stdout boundaries. Explicit
			// client-owned control events are accepted without adding a second
			// runtime seam to the test.
		default:
		}
	}
}

func (f *familyEProviderFixture) handleUtterance(connection *websocket.Conn) error {
	f.mu.Lock()
	index := f.utteranceCount
	f.utteranceCount++
	now := f.elapsedLocked()
	f.mu.Unlock()
	if index > 1 {
		return fmt.Errorf("received unexpected third customer utterance")
	}
	text := probe.FamilyESpokenScript()[0].Text
	if index == 1 {
		text = probe.FamilyEReprompt(0)
	}
	f.mu.Lock()
	f.customerTranscript = append(f.customerTranscript, probe.TranscriptEvent{
		ID: fmt.Sprintf("customer-family-e-%d", index+1), TurnID: probe.FamilyETurnID, Speaker: probe.TranscriptCustomer, Text: text, At: now, Final: true,
	})
	f.mu.Unlock()

	switch {
	case index == 0 && f.mode == familyEShippedNormal:
		return f.sendProductResponse(connection, "response-family-e-initial", "The request is complete.", []byte{1, 0x45, 0x50, 0x41}, true)
	case index == 0:
		return f.sendProductResponse(connection, "response-family-e-initial", "I am still working on the request.", []byte{1, 0x45, 0x50, 0x41}, false)
	case f.mode == familyEShippedRecovery:
		return f.sendProductResponse(connection, "response-family-e-reprompt", "The request is complete.", []byte{2, 0x45, 0x50, 0x41}, true)
	default:
		// The dead-air mode deliberately produces no second response after the
		// bounded re-prompt; the production controller must record that gap.
		return nil
	}
}

func (f *familyEProviderFixture) sendProductResponse(connection *websocket.Conn, responseID, text string, audio []byte, closeSession bool) error {
	startedAt := f.elapsed()
	f.mu.Lock()
	f.productTranscript = append(f.productTranscript, probe.TranscriptEvent{
		ID: "product-" + responseID, TurnID: probe.FamilyETurnID, Speaker: probe.TranscriptProduct, Text: text, At: startedAt, Final: true,
	})
	f.mu.Unlock()
	if err := f.send(connection, map[string]any{"type": "response.created", "response": map[string]string{"id": responseID}}); err != nil {
		return err
	}
	if err := f.send(connection, map[string]string{"type": "response.output_audio_transcript.delta", "delta": text}); err != nil {
		return err
	}
	if err := f.send(connection, map[string]string{"type": "response.output_audio_transcript.done", "transcript": text}); err != nil {
		return err
	}
	if err := f.send(connection, map[string]any{
		"type": "response.output_audio.delta", "delta": base64.StdEncoding.EncodeToString(audio), "format": "pcm16",
	}); err != nil {
		return err
	}
	if err := f.send(connection, map[string]any{"type": "response.output_audio.done", "response": map[string]string{"id": responseID}}); err != nil {
		return err
	}
	if err := f.send(connection, map[string]any{"type": "response.done", "response": map[string]string{"id": responseID, "status": "completed"}}); err != nil {
		return err
	}
	if closeSession {
		return f.send(connection, map[string]string{"type": "session.closed", "reason": "family_e_response_complete"})
	}
	return nil
}

func (f *familyEProviderFixture) sendSessionReady(connection *websocket.Conn) error {
	if err := f.send(connection, map[string]any{
		"type": "session.created", "session": map[string]string{"id": "family-e", "model": "gpt-realtime"},
	}); err != nil {
		return err
	}
	return f.send(connection, map[string]any{
		"type": "session.updated", "session": map[string]string{"id": "family-e"},
	})
}

func (f *familyEProviderFixture) send(connection *websocket.Conn, event any) error {
	return connection.WriteJSON(event)
}

func (f *familyEProviderFixture) failProtocol(message string) {
	f.mu.Lock()
	if f.protocolError == "" {
		f.protocolError = message
	}
	f.mu.Unlock()
}

func (f *familyEProviderFixture) elapsed() time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.elapsedLocked()
}

func (f *familyEProviderFixture) elapsedLocked() time.Duration {
	if f.startedAt.IsZero() {
		return 0
	}
	return time.Since(f.startedAt)
}

func familyEFrame(marker byte) []byte {
	frame := make([]byte, probe.DefaultDuplexFrameSamples*2)
	frame[0] = marker
	frame[1] = 0x45
	return frame
}

func familyESilent(audio []byte) bool {
	for _, value := range audio {
		if value != 0 {
			return false
		}
	}
	return true
}
