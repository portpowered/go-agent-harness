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
	"github.com/portpowered/go-agent-harness/agent-cli/test/integration/testnet"
)

// TestFamilyDTerminationShapesThroughShippedProcess runs the same declared
// customer request through the real session executable twice: once with the
// runner's output-gated SIGINT and once with provider-declared natural close.
// Both runs retain the paired evidence bundle, including the product's own
// record directory, before the test evaluates lifecycle truth.
func TestFamilyDTerminationShapesThroughShippedProcess(t *testing.T) {
	for _, method := range []probe.TerminationMethod{probe.TerminationSIGINT, probe.TerminationNatural} {
		t.Run(string(method), func(t *testing.T) {
			run := runFamilyDProcess(t, method)
			assertFamilyDProcessRun(t, run)
			finalizeFamilyDProcessEvidence(t, run)
		})
	}
}

type familyDProcessRun struct {
	scenario    probe.CustomerScenario
	sandbox     string
	recordDir   string
	result      probe.DuplexRunResult
	checkpoint  probe.FilesystemCheckpoint
	observation familyDProviderObservation
	process     probe.ProcessFacts
	termination probe.TerminationEvidence
	mechanical  probe.MechanicalVerdict
}

func runFamilyDProcess(t *testing.T, method probe.TerminationMethod) familyDProcessRun {
	t.Helper()
	scenarioPath := filepath.Join(agentCLIRoot(t), "testdata", "customer-simulation", "family-d-"+string(method)+".scenario.json")
	data, err := os.ReadFile(scenarioPath)
	if err != nil {
		t.Fatalf("read Family D %q scenario: %v", method, err)
	}
	scenario, err := probe.ParseCustomerScenario(data)
	if err != nil {
		t.Fatalf("parse Family D %q scenario: %v", method, err)
	}

	sandbox := filepath.Join(t.TempDir(), "sandbox")
	if err := os.MkdirAll(sandbox, 0o700); err != nil {
		t.Fatalf("create Family D sandbox: %v", err)
	}
	oracle, err := probe.NewFilesystemOracle(sandbox)
	if err != nil {
		t.Fatalf("NewFilesystemOracle: %v", err)
	}
	configDir := filepath.Join(t.TempDir(), "config")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("create Family D config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte("tools:\n  exec:\n    enable_deny_patterns: true\n"), 0o600); err != nil {
		t.Fatalf("write Family D config: %v", err)
	}

	fixture := newFamilyDProviderFixture(t, scenario, method)
	defer fixture.Close()
	startedAt := time.Now()
	fixture.SetStartedAt(startedAt)
	recordDir := filepath.Join(t.TempDir(), "record")
	config := probe.DuplexSessionConfig{
		BinaryPath:       buildAgentBinary(t),
		RecordDir:        recordDir,
		WorkingDirectory: sandbox,
		ConfigDir:        configDir,
		Provider:         "openai",
		Model:            "gpt-realtime",
		BaseURL:          fixture.WebSocketURL(),
		APIKey:           "hermetic-key",
		SystemPrompt:     scenario.TextSeed,
		MaxDuration:      5 * time.Second,
		FrameDuration:    5 * time.Millisecond,
		AdditionalArgs:   []string{"--wait-for-close"},
		Segments:         familyDSegments(method),
	}
	if method == probe.TerminationSIGINT {
		config.Termination = probe.TerminationSIGINT
		config.TerminationAfterOutputBytes = 4
	}
	result, runErr := probe.RunDuplexSession(context.Background(), config)
	if runErr != nil {
		t.Fatalf("Family D %q shipped-process run: %v\nresult=%+v\nstdout=%x\nstderr=%s", method, runErr, result, result.Stdout, result.Stderr)
	}
	observation := fixture.Snapshot()
	checkpoint, err := oracle.Checkpoint("checkpoint-termination", probe.FamilyDActionID, result.Duration, scenario.Actions[0].Oracle.Checkpoints)
	if err != nil {
		t.Fatalf("Family D %q filesystem checkpoint: %v", method, err)
	}
	process := probe.ProcessFactsFromDuplexResult(result)
	if observation.OutputEndedAt <= observation.OutputStartedAt {
		// SIGINT leaves the provider response open; retain a positive fixture
		// interval on the fixture's own clock (result.Duration starts later, at
		// process start, and could precede the fixture's output start).
		observation.OutputEndedAt = time.Since(startedAt)
	}
	termination, actionResult := familyDTerminationEvidence(t, method, result, process, checkpoint)
	mechanical, err := probe.EvaluateCustomerSimulationTermination(scenario, []probe.ActionResult{actionResult}, []probe.FilesystemCheckpoint{checkpoint}, nil, observation.ProductTranscript, termination)
	if err != nil {
		t.Fatalf("Family D %q mechanical evaluation: %v; result=%+v observation=%+v termination=%+v", method, err, result, observation, termination)
	}
	return familyDProcessRun{
		scenario: scenario, sandbox: sandbox, recordDir: recordDir, result: result, checkpoint: checkpoint,
		observation: observation, process: process, termination: termination, mechanical: mechanical,
	}
}

// familyDTerminationEvidence derives the termination evidence and the single
// action result for one Family D run. Provider fixture timestamps and runner
// timestamps have different clock origins, so the runner's observed output
// interval is used and SIGINT is checked against the same clock as SignalAt.
func familyDTerminationEvidence(t *testing.T, method probe.TerminationMethod, result probe.DuplexRunResult, process probe.ProcessFacts, checkpoint probe.FilesystemCheckpoint) (probe.TerminationEvidence, probe.ActionResult) {
	t.Helper()
	responseStatus := rtStatusCompleted
	terminationStatus := rtStatusCompleted
	confirmed := true
	disposition := probe.DispositionCompleted
	satisfactionDeclared := true
	activeResponseStartedAt := result.Output[0].At
	activeResponseEndedAt := result.Duration
	satisfactionAt := activeResponseEndedAt
	if method == probe.TerminationSIGINT {
		responseStatus = "interrupted"
		terminationStatus = "interrupted"
		confirmed = false
		disposition = probe.DispositionCancelled
		satisfactionDeclared = false
		satisfactionAt = 0
	}
	termination := probe.TerminationEvidence{
		Method:                  method,
		ActiveActionID:          probe.FamilyDActionID,
		ActiveTurnID:            probe.FamilyDActiveTurnID,
		ActiveResponseID:        probe.FamilyDActiveResponseID,
		ActiveResponseStatus:    terminationStatus,
		ActiveResponseStartedAt: activeResponseStartedAt,
		ActiveResponseEndedAt:   activeResponseEndedAt,
		SatisfactionDeclared:    satisfactionDeclared,
		SatisfactionAt:          satisfactionAt,
		SignalSent:              result.SignalSent,
		Signal:                  result.Signal,
		SignalAt:                result.SignalAt,
		Process:                 process,
		EvidenceRefs:            probe.FamilyDTerminationEvidenceRefs(),
	}
	if termination.ActiveResponseStatus != responseStatus {
		t.Fatalf("Family D %q response status = %q, want %q", method, termination.ActiveResponseStatus, responseStatus)
	}
	actionResult := probe.ActionResult{
		ActionID: probe.FamilyDActionID, TurnID: probe.FamilyDActiveTurnID, Confirmed: confirmed,
		Disposition: disposition, EvidenceRefs: probe.FamilyDTerminationEvidenceRefs(), CheckpointIDs: []string{checkpoint.ID},
	}
	if method == probe.TerminationNatural {
		actionResult.ConfirmedAt = activeResponseEndedAt
	} else {
		actionResult.OutcomeReason = "SIGINT interrupted the active response before natural satisfaction; no side effect was left behind"
	}
	return termination, actionResult
}

// familyDSegments scripts the customer's PCM. For SIGINT, stdin stays open
// until the runner's output-gated SIGINT ends the run: SIGINT must interrupt
// the open PCM stream, not a script that already reached EOF. The former fixed
// 2s silence tail only held stdin open if the child answered within 2s.
func familyDSegments(method probe.TerminationMethod) []probe.DuplexAudioSegment {
	segments := []probe.DuplexAudioSegment{{ID: "active-request", PCM16: familyDFrame(1), SilenceFor: 200 * time.Millisecond}}
	if method != probe.TerminationSIGINT {
		return segments
	}
	return append(segments, probe.DuplexAudioSegment{
		ID: "hold-open-until-sigint", SilenceFor: 5 * time.Millisecond,
		Before: func(ctx context.Context, _ *probe.DuplexProgress) error {
			<-ctx.Done()
			return ctx.Err()
		},
	})
}

func assertFamilyDProcessRun(t *testing.T, run familyDProcessRun) {
	t.Helper()
	if run.observation.ProtocolError != "" {
		t.Fatalf("Family D provider protocol error: %s", run.observation.ProtocolError)
	}
	if run.observation.ConnectionCount != 1 || run.observation.SessionUpdates != 1 {
		t.Fatalf("Family D provider lifecycle = connections:%d updates:%d, want one persistent session", run.observation.ConnectionCount, run.observation.SessionUpdates)
	}
	if len(run.observation.CustomerTranscript) != 1 || len(run.observation.ProductTranscript) != 1 {
		t.Fatalf("Family D transcripts = customer:%d product:%d, want one paired turn", len(run.observation.CustomerTranscript), len(run.observation.ProductTranscript))
	}
	if run.observation.OutputStartedAt >= run.observation.OutputEndedAt {
		t.Fatalf("Family D response interval = start:%s end:%s, want positive interval", run.observation.OutputStartedAt, run.observation.OutputEndedAt)
	}
	if run.process.PID <= 0 || !run.process.ChildWaited || run.process.WaitCount != 1 || run.process.DescendantsAlive {
		t.Fatalf("Family D process facts = %+v, want one reap and no descendants", run.process)
	}
	if !run.process.InputClosed || !run.process.OutputClosed {
		t.Fatalf("Family D stream facts = %+v, want both PCM directions closed", run.process)
	}
	if len(run.result.Input) == 0 || len(run.result.Output) == 0 || len(run.result.Stdout) < 4 {
		t.Fatalf("Family D stream evidence = input:%d output_reads:%d stdout:%d, want incremental input and output", len(run.result.Input), len(run.result.Output), len(run.result.Stdout))
	}
	if !bytes.Contains(run.result.Stdout, []byte{0xd1, 0x44, 0x44, 0x50}) {
		t.Fatalf("Family D stdout = %x, want provider response audio marker", run.result.Stdout)
	}
	assertFamilyDTerminationFacts(t, run)
	if !run.mechanical.Pass || len(run.mechanical.Findings) != 0 {
		t.Fatalf("Family D mechanical verdict = %+v, want pass without findings", run.mechanical)
	}
	entries, err := os.ReadDir(run.recordDir)
	if err != nil {
		t.Fatalf("read Family D record directory: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("Family D product record directory is empty")
	}
}

func assertFamilyDTerminationFacts(t *testing.T, run familyDProcessRun) {
	t.Helper()
	if run.scenario.Termination == probe.TerminationSIGINT {
		if run.result.ExitClassification != "sigint" || !run.process.SignalSent || run.process.Signal != probe.DuplexSIGINTName || run.process.SignalAt <= run.process.StartedAt {
			t.Fatalf("SIGINT result/process = %+v / %+v, want sent signal and sigint classification", run.result, run.process)
		}
		if run.process.InputFinished {
			t.Fatalf("SIGINT process facts = %+v, want input interrupted before script completion", run.process)
		}
		if run.observation.ResponseTerminals != 0 {
			t.Fatalf("SIGINT provider terminal count = %d, want active response interrupted before provider terminal", run.observation.ResponseTerminals)
		}
	} else {
		if run.result.ExitClassification != "normal" || run.process.SignalSent || run.process.Signal != "" || !run.process.InputFinished {
			t.Fatalf("natural result/process = %+v / %+v, want normal no-signal completed input", run.result, run.process)
		}
		if run.observation.ResponseTerminals != 1 || run.observation.SessionClosed != 1 {
			t.Fatalf("natural provider terminals = responses:%d sessions:%d, want one response and session close", run.observation.ResponseTerminals, run.observation.SessionClosed)
		}
	}
}

func finalizeFamilyDProcessEvidence(t *testing.T, run familyDProcessRun) {
	t.Helper()
	bundle, err := probe.NewCustomerEvidenceBundle(filepath.Join(t.TempDir(), "evidence"), run.scenario, "run-"+run.scenario.ID, "hermetic-key")
	if err != nil {
		t.Fatalf("NewCustomerEvidenceBundle: %v", err)
	}
	bundle.Transcripts = probe.PairedTranscripts{Customer: run.observation.CustomerTranscript, Product: run.observation.ProductTranscript}
	bundle.AudioTurnEvents = familyDAudioTurnEvents(run.result)
	bundle.ToolObservations = nil
	bundle.FilesystemCheckpoints = []probe.FilesystemCheckpoint{run.checkpoint}
	bundle.Process = run.process
	bundle.MechanicalVerdict = &run.mechanical
	bundle.Termination = &run.termination
	bundle.ValidatorInput = &probe.ValidatorInput{
		Scenario: run.scenario, CustomerTranscript: bundle.Transcripts.Customer, ProductTranscript: bundle.Transcripts.Product,
		AudioTurnEvents: bundle.AudioTurnEvents, ToolObservations: bundle.ToolObservations, FilesystemCheckpoints: bundle.FilesystemCheckpoints,
		Process: run.process, Mechanical: run.mechanical, Termination: &run.termination,
		EvidenceRefs: []string{"scenario.json", "transcripts/customer.jsonl", "transcripts/product.jsonl", "events/audio-turn-events.jsonl", "tool-observations.jsonl", "filesystem-checkpoints.jsonl", "process.json", "events/termination.json", "mechanical-verdict.json"},
	}
	bundle.ValidatorVerdict = &probe.ValidatorVerdict{
		Verdict:      probe.ValidatorWorked,
		Summary:      "The selected Family D termination completed with a truthful action disposition and clean process lifecycle.",
		EvidenceRefs: probe.FamilyDTerminationEvidenceRefs(),
	}
	if err := bundle.AddProductRecordDir(run.recordDir); err != nil {
		t.Fatalf("copy Family D product record directory: %v", err)
	}
	if err := bundle.Finalize(); err != nil {
		t.Fatalf("finalize Family D evidence: %v", err)
	}
	manifest, err := probe.VerifyCustomerEvidenceBundle(bundle.Root())
	if err != nil {
		t.Fatalf("verify Family D evidence bundle: %v", err)
	}
	if !manifest.Finalized || !manifest.MechanicalPass || manifest.ValidatorVerdict != probe.ValidatorWorked {
		t.Fatalf("Family D evidence manifest = %+v, want finalized WORKED/pass", manifest)
	}
	for _, path := range []string{"transcripts/customer.jsonl", "transcripts/product.jsonl", "events/audio-turn-events.jsonl", "tool-observations.jsonl", "filesystem-checkpoints.jsonl", "process.json", "events/termination.json", "mechanical-verdict.json", "validator-input.json", "validator-verdict.json"} {
		artifact := familyDArtifact(manifest.Artifacts, path)
		if artifact.State != probe.ArtifactStateAvailable || artifact.SHA256 == "" {
			t.Fatalf("Family D artifact %q = %+v, want hash-verified available artifact", path, artifact)
		}
	}
}

func familyDAudioTurnEvents(result probe.DuplexRunResult) []probe.AudioTurnEvent {
	events := make([]probe.AudioTurnEvent, 0, len(result.Input)+len(result.Output))
	for index, input := range result.Input {
		kind := "speech"
		if input.Silent {
			kind = "silence"
		}
		events = append(events, probe.AudioTurnEvent{ID: fmt.Sprintf("input-%d", index+1), TurnID: probe.FamilyDActiveTurnID, Direction: "input", Kind: kind, At: input.At, Duration: 5 * time.Millisecond, Bytes: input.Bytes})
	}
	for index, output := range result.Output {
		events = append(events, probe.AudioTurnEvent{ID: fmt.Sprintf("output-%d", index+1), TurnID: probe.FamilyDActiveTurnID, Direction: "output", Kind: "speech", At: output.At, Bytes: output.Bytes})
	}
	return events
}

func familyDArtifact(entries []probe.ArtifactEntry, path string) probe.ArtifactEntry {
	for _, entry := range entries {
		if entry.Path == path {
			return entry
		}
	}
	return probe.ArtifactEntry{}
}

type familyDProviderObservation struct {
	ConnectionCount    int
	SessionUpdates     int
	CustomerTranscript []probe.TranscriptEvent
	ProductTranscript  []probe.TranscriptEvent
	OutputStartedAt    time.Duration
	OutputEndedAt      time.Duration
	ResponseTerminals  int
	SessionClosed      int
	ProtocolError      string
}

type familyDProviderFixture struct {
	server   *httptest.Server
	upgrader websocket.Upgrader
	method   probe.TerminationMethod

	mu                 sync.Mutex
	startedAt          time.Time
	connectionCount    int
	sessionUpdates     int
	customerTranscript []probe.TranscriptEvent
	productTranscript  []probe.TranscriptEvent
	outputStartedAt    time.Duration
	outputEndedAt      time.Duration
	responseTerminals  int
	sessionClosed      int
	protocolError      string
	utteranceSeen      bool
}

func newFamilyDProviderFixture(t testing.TB, _ probe.CustomerScenario, method probe.TerminationMethod) *familyDProviderFixture {
	fixture := &familyDProviderFixture{upgrader: websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}, method: method}
	fixture.server = testnet.NewWANSegmentServer(t, http.HandlerFunc(fixture.handle))
	return fixture
}

func (f *familyDProviderFixture) SetStartedAt(startedAt time.Time) {
	f.mu.Lock()
	f.startedAt = startedAt
	f.mu.Unlock()
}

func (f *familyDProviderFixture) WebSocketURL() string {
	return strings.Replace(f.server.URL, "http://", "ws://", 1)
}

func (f *familyDProviderFixture) Close() {
	if f.server != nil {
		f.server.Close()
	}
}

func (f *familyDProviderFixture) Snapshot() familyDProviderObservation {
	f.mu.Lock()
	defer f.mu.Unlock()
	return familyDProviderObservation{
		ConnectionCount: f.connectionCount, SessionUpdates: f.sessionUpdates,
		CustomerTranscript: append([]probe.TranscriptEvent(nil), f.customerTranscript...),
		ProductTranscript:  append([]probe.TranscriptEvent(nil), f.productTranscript...),
		OutputStartedAt:    f.outputStartedAt, OutputEndedAt: f.outputEndedAt,
		ResponseTerminals: f.responseTerminals, SessionClosed: f.sessionClosed,
		ProtocolError: f.protocolError,
	}
}

func (f *familyDProviderFixture) handle(writer http.ResponseWriter, request *http.Request) {
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
		}
		if err := json.Unmarshal(payload, &event); err != nil {
			f.failProtocol("decode client event: " + err.Error())
			return
		}
		switch event.Type {
		case rtEventSessionUpdate:
			f.mu.Lock()
			f.sessionUpdates++
			f.mu.Unlock()
			if err := f.sendSessionReady(connection); err != nil {
				return
			}
		case rtEventInputAudioAppend:
			if !f.handleInputAudio(connection, event.Audio) {
				return
			}
		case rtEventInputAudioCommit:
			// The finite source's explicit end-of-turn is the natural completion
			// trigger. SIGINT runs close before this event reaches the fixture.
		case rtEventResponseCreate:
			if f.method == probe.TerminationNatural {
				if err := f.finishNatural(connection); err != nil {
					f.failProtocol(err.Error())
					return
				}
			}
		case rtEventResponseCancel:
			// A SIGINT run may emit a provider cancellation before the websocket
			// closes. Do not fabricate response.done for the interrupted response.
		default:
		}
	}
}

func (f *familyDProviderFixture) handleCustomerUtterance(connection *websocket.Conn) error {
	f.mu.Lock()
	if f.utteranceSeen {
		f.mu.Unlock()
		return fmt.Errorf("received an unexpected second customer utterance")
	}
	f.utteranceSeen = true
	startedAt := f.elapsedLocked()
	f.customerTranscript = append(f.customerTranscript, probe.TranscriptEvent{ID: "customer-turn-1", TurnID: probe.FamilyDActiveTurnID, Speaker: probe.TranscriptCustomer, Text: probe.FamilyDSpokenScript()[0].Text, At: startedAt, Final: true})
	f.outputStartedAt = startedAt
	f.mu.Unlock()
	if err := f.send(connection, map[string]any{"type": rtEventResponseCreated, "response": map[string]string{"id": probe.FamilyDActiveResponseID}}); err != nil {
		return err
	}
	text := probe.FamilyDResponseText
	f.mu.Lock()
	f.productTranscript = append(f.productTranscript, probe.TranscriptEvent{ID: "product-turn-1", TurnID: probe.FamilyDActiveTurnID, Speaker: probe.TranscriptProduct, Text: text, At: f.outputStartedAt, Final: true})
	f.mu.Unlock()
	if err := f.send(connection, map[string]string{"type": rtEventOutputAudioTranscriptDelta, "delta": text}); err != nil {
		return err
	}
	if err := f.send(connection, map[string]string{"type": "response.output_audio_transcript.done", "transcript": text}); err != nil {
		return err
	}
	if err := f.send(connection, map[string]any{"type": rtEventOutputAudioDelta, "delta": base64.StdEncoding.EncodeToString([]byte{0xd1, 0x44, 0x44, 0x50}), "format": "pcm16"}); err != nil {
		return err
	}
	if f.method == probe.TerminationNatural {
		// The first response is the one that produced the observed output. End
		// that response before accepting the client's next response.create; a
		// realtime provider cannot admit another default-conversation response
		// while this one is still active.
		f.mu.Lock()
		f.outputEndedAt = f.elapsedLocked()
		f.responseTerminals++
		f.mu.Unlock()
		if err := f.send(connection, map[string]string{"type": "response.output_audio.done"}); err != nil {
			return err
		}
		if err := f.send(connection, map[string]any{"type": rtEventResponseDone, "response": map[string]string{"id": probe.FamilyDActiveResponseID, "status": rtStatusCompleted}}); err != nil {
			return err
		}
	}
	return nil
}

func (f *familyDProviderFixture) finishNatural(connection *websocket.Conn) error {
	// response.create is the client's explicit close boundary after the
	// completed output response. It is safe to close the session now without
	// fabricating a second response terminal.
	f.mu.Lock()
	f.sessionClosed++
	f.mu.Unlock()
	return f.send(connection, map[string]string{"type": rtEventSessionClosed, "reason": "family_d_natural_satisfaction"})
}

func (f *familyDProviderFixture) sendSessionReady(connection *websocket.Conn) error {
	if err := f.send(connection, map[string]any{"type": rtEventSessionCreated, "session": map[string]string{"id": "family-d", "model": "gpt-realtime"}}); err != nil {
		return err
	}
	return f.send(connection, map[string]any{"type": "session.updated", "session": map[string]string{"id": "family-d"}})
}

func (f *familyDProviderFixture) send(connection *websocket.Conn, event any) error {
	return connection.WriteJSON(event)
}

func (f *familyDProviderFixture) elapsedLocked() time.Duration {
	if f.startedAt.IsZero() {
		return 0
	}
	return time.Since(f.startedAt)
}

func (f *familyDProviderFixture) failProtocol(message string) {
	f.mu.Lock()
	if f.protocolError == "" {
		f.protocolError = message
	}
	f.mu.Unlock()
}

func familyDFrame(seed byte) []byte {
	frame := make([]byte, probe.DefaultDuplexFrameSamples*2)
	for index := range frame {
		frame[index] = seed
	}
	return frame
}

// handleInputAudio answers one appended frame and reports whether the
// session should keep reading. Silent frames get the server-VAD boundary
// events; a write failure there means the child already hung up.
func (f *familyDProviderFixture) handleInputAudio(connection *websocket.Conn, encoded string) bool {
	audio, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		f.failProtocol("decode input audio: " + err.Error())
		return false
	}
	if customerSimulationSilent(audio) {
		return f.send(connection, map[string]string{"type": "input_audio_buffer.speech_stopped"}) == nil &&
			f.send(connection, map[string]string{"type": "input_audio_buffer.committed"}) == nil
	}
	if err := f.handleCustomerUtterance(connection); err != nil {
		f.failProtocol(err.Error())
		return false
	}
	return true
}
