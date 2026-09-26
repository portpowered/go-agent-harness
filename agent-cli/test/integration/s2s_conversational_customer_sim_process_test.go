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

// TestShippedSessionProcessDuplexConversation drives the built agent binary
// through the public session command. The local websocket server is a small
// Realtime-shaped provider: it keeps response one open, waits for the shipped
// client to cancel it when correction audio arrives, then completes two more
// responses. The runner's output gates make the ordering observable without
// buffering the conversation or restarting the child.
func TestShippedSessionProcessDuplexConversation(t *testing.T) {
	fixture := newCustomerSimulationFixture(t)
	defer fixture.Close()
	recordDir := filepath.Join(t.TempDir(), "record")

	result, err := probe.RunDuplexSession(context.Background(), probe.DuplexSessionConfig{
		BinaryPath:       buildAgentBinary(t),
		RecordDir:        recordDir,
		WorkingDirectory: filepath.Join(t.TempDir(), "work"),
		ConfigDir:        filepath.Join(t.TempDir(), "config"),
		Provider:         "openai",
		Model:            "gpt-realtime",
		BaseURL:          fixture.WebSocketURL(),
		APIKey:           "hermetic-key",
		MaxDuration:      8 * time.Second,
		FrameDuration:    5 * time.Millisecond,
		BeforeInputClose: fixture.finishInput,
		Segments: []probe.DuplexAudioSegment{
			{ID: "first-speech", PCM16: customerSimulationFrame(1)},
			{ID: "first-silence", SilenceFor: 5 * time.Millisecond, WaitForOutputBytes: 4},
			{ID: "correction-speech", PCM16: append(customerSimulationFrame(customerSimulationCorrectionSeed), customerSimulationFrame(customerSimulationCorrectionSeed)...), WaitForOutputBytes: 4},
			{ID: "second-silence", SilenceFor: 5 * time.Millisecond, WaitForOutputBytes: 8},
			{ID: "final-speech", PCM16: customerSimulationFrame(3), WaitForOutputBytes: 8},
		},
	})
	if err != nil {
		t.Fatalf("shipped session duplex run: %v\nprovider=%+v\nresult=%+v\nstdout=%x\nstderr=%s", err, fixture.Snapshot(), result, result.Stdout, result.Stderr)
	}

	observation := fixture.Snapshot()
	if observation.protocolError != "" {
		t.Fatalf("fake provider protocol error: %s", observation.protocolError)
	}
	if observation.connectionCount != 1 {
		t.Fatalf("provider connections = %d, want one process-owned session", observation.connectionCount)
	}
	if observation.sessionUpdates != 1 {
		t.Fatalf("session.update count = %d, want one handshake", observation.sessionUpdates)
	}
	if len(observation.appends) != 6 {
		t.Fatalf("provider audio appends = %d, want six frames across three speech/silence segments", len(observation.appends))
	}
	if observation.silentAppends != 2 || observation.committedTurns != 2 {
		t.Fatalf("provider VAD-shaped boundaries = silent appends %d, committed turns %d; want two of each", observation.silentAppends, observation.committedTurns)
	}
	if observation.nonSilentAppends != 4 || !observation.finalAppendAfterFirst {
		t.Fatalf("provider speech progression = non-silent appends %d, final-after-first=%t; want four later frames on one connection", observation.nonSilentAppends, observation.finalAppendAfterFirst)
	}
	if observation.cancelCount-observation.inactiveCancelCount != 1 {
		t.Fatalf("provider successful cancellation count = %d, want one interruption", observation.cancelCount-observation.inactiveCancelCount)
	}
	if !observation.firstOutputAt.Before(observation.correctionAt) || !observation.cancelAt.Before(observation.correctionAt) {
		t.Fatalf("barge-in ordering first_output=%s cancel=%s correction=%s", observation.firstOutputAt, observation.cancelAt, observation.correctionAt)
	}
	if got, want := strings.Join(observation.responseTerminalStatuses, ","), "cancelled,completed,completed"; got != want {
		t.Fatalf("provider response terminal statuses = %q, want %q", got, want)
	}

	if result.ExitCode != 0 || !result.ChildWaited || !result.InputFinished || !result.InputClosed || !result.StdoutClosed || !result.StderrClosed {
		t.Fatalf("process lifecycle result = %+v, want a completed, fully reaped child", result)
	}
	if len(result.Input) != 6 || len(result.Output) == 0 || len(result.Stdout) < 12 {
		t.Fatalf("stream evidence input=%d output_reads=%d stdout_bytes=%d, want five input frames and three audio responses", len(result.Input), len(result.Output), len(result.Stdout))
	}
	// Stdout is the PCM transport, so even a setup diagnostic corrupts audio
	// and can prematurely release the customer's output-gated interruption.
	wantAudio := []byte{1, 0x10, 0x20, 0x30, 2, 0x10, 0x20, 0x30, 3, 0x10, 0x20, 0x30}
	if !bytes.Equal(result.Stdout, wantAudio) {
		t.Fatalf("captured stdout = %x, want only ordered PCM %x", result.Stdout, wantAudio)
	}
	assertCustomerSimulationProcessArgs(t, result)

	entries, err := os.ReadDir(recordDir)
	if err != nil {
		t.Fatalf("read shipped session record directory: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("shipped session record directory is empty")
	}
}

func assertCustomerSimulationProcessArgs(t *testing.T, result probe.DuplexRunResult) {
	t.Helper()
	if strings.Contains(result.Command, "hermetic-key") || strings.Contains(strings.Join(result.SanitizedArgs, "\x00"), "hermetic-key") {
		t.Fatalf("API key leaked into process evidence: command=%q args=%q", result.Command, result.SanitizedArgs)
	}
	for _, forbidden := range []string{"--audio-in-turn", "--api-key"} {
		if containsIntegrationString(result.SanitizedArgs, forbidden) {
			t.Fatalf("runner unexpectedly used forbidden boundary/credential argument %q: %v", forbidden, result.SanitizedArgs)
		}
	}
	for _, required := range []string{"--audio-in", "--audio-out", "--record-dir", "--provider", "--model", "--max-duration"} {
		if !containsIntegrationString(result.SanitizedArgs, required) {
			t.Fatalf("sanitized process args = %v, missing required %q", result.SanitizedArgs, required)
		}
	}
}

type customerSimulationFixture struct {
	server     *httptest.Server
	upgrader   websocket.Upgrader
	closeReady chan struct{}
	closeOnce  sync.Once

	mu                       sync.Mutex
	connectionCount          int
	sessionUpdates           int
	appends                  []customerSimulationAppend
	silentAppends            int
	nonSilentAppends         int
	committedTurns           int
	cancelCount              int
	inactiveCancelCount      int
	firstOutputAt            time.Time
	cancelAt                 time.Time
	correctionAt             time.Time
	finalAppendAfterFirst    bool
	responseTerminalStatuses []string
	protocolError            string
}

type customerSimulationAppend struct {
	at     time.Time
	silent bool
}

type customerSimulationSnapshot struct {
	connectionCount          int
	sessionUpdates           int
	appends                  []customerSimulationAppend
	silentAppends            int
	nonSilentAppends         int
	committedTurns           int
	cancelCount              int
	inactiveCancelCount      int
	firstOutputAt            time.Time
	cancelAt                 time.Time
	correctionAt             time.Time
	finalAppendAfterFirst    bool
	responseTerminalStatuses []string
	protocolError            string
}

func newCustomerSimulationFixture(t testing.TB) *customerSimulationFixture {
	fixture := &customerSimulationFixture{
		upgrader:   websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }},
		closeReady: make(chan struct{}),
	}
	fixture.server = testnet.NewWANSegmentServer(t, http.HandlerFunc(fixture.handle))
	return fixture
}

func (f *customerSimulationFixture) WebSocketURL() string {
	return strings.Replace(f.server.URL, "http://", "ws://", 1)
}

func (f *customerSimulationFixture) Close() {
	f.ReleaseClose()
	if f.server != nil {
		f.server.Close()
	}
}

func (f *customerSimulationFixture) ReleaseClose() {
	f.closeOnce.Do(func() { close(f.closeReady) })
}

func (f *customerSimulationFixture) Snapshot() customerSimulationSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return customerSimulationSnapshot{
		connectionCount:          f.connectionCount,
		sessionUpdates:           f.sessionUpdates,
		appends:                  append([]customerSimulationAppend(nil), f.appends...),
		silentAppends:            f.silentAppends,
		nonSilentAppends:         f.nonSilentAppends,
		committedTurns:           f.committedTurns,
		cancelCount:              f.cancelCount,
		inactiveCancelCount:      f.inactiveCancelCount,
		firstOutputAt:            f.firstOutputAt,
		cancelAt:                 f.cancelAt,
		correctionAt:             f.correctionAt,
		finalAppendAfterFirst:    f.finalAppendAfterFirst,
		responseTerminalStatuses: append([]string(nil), f.responseTerminalStatuses...),
		protocolError:            f.protocolError,
	}
}

func (f *customerSimulationFixture) handle(writer http.ResponseWriter, request *http.Request) {
	if request.Header.Get("Authorization") != rtAuthorizationHeader {
		f.failProtocol("authorization header did not arrive through the child environment")
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

	state := &customerSimulationTurnState{}
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
			if err := f.handshake(connection); err != nil {
				return
			}
		case rtEventInputAudioAppend:
			if !f.handleInputAudio(connection, event.Audio, state) {
				return
			}
		case rtEventResponseCancel:
			var cancelErr error
			state.cancelPending, cancelErr = f.receiveCancel(connection, state.activeResponse)
			if cancelErr != nil {
				return
			}
		case rtEventInputAudioCommit:
			// The fixture models server-VAD-shaped committed turns from the
			// open stdin stream. Wait for the final PCM acknowledgment AND the
			// product's EOF commit before closing this completed conversation.
			// Abrupt provider-close durability is a separate contract.
			if state.responseNumber == 3 {
				<-f.closeReady
				if err := f.send(connection, map[string]string{"type": rtEventSessionClosed, "reason": "customer_simulation_complete"}); err != nil {
					return
				}
			}
		default:
			// session.created and provider metadata are server-to-client only;
			// unknown client events are harmless for this focused fixture.
		}
	}
}

// customerSimulationTurnState is the per-connection response bookkeeping.
type customerSimulationTurnState struct {
	speaking       bool // consecutive non-silent appends continue one utterance
	activeResponse string
	cancelPending  bool
	responseNumber int
}

// handleInputAudio records one appended frame and answers it. It reports
// whether the session should keep reading; a write failure means the child
// already hung up.
func (f *customerSimulationFixture) handleInputAudio(connection *websocket.Conn, encoded string, state *customerSimulationTurnState) bool {
	audio, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		f.failProtocol("decode input audio: " + err.Error())
		return false
	}
	silent := customerSimulationSilent(audio)
	continuing := !silent && state.speaking
	state.speaking = !silent
	if f.recordAppend(silent) {
		return f.send(connection, map[string]string{"type": "input_audio_buffer.speech_stopped"}) == nil &&
			f.send(connection, map[string]string{"type": "input_audio_buffer.committed"}) == nil
	}
	if continuing {
		return true // a later frame of the same utterance
	}
	if state.cancelPending {
		if !f.finishCancelledResponse(connection, state.activeResponse) {
			return false
		}
		state.cancelPending = false
	}
	state.responseNumber++
	state.activeResponse = fmt.Sprintf("response-%d", state.responseNumber)
	complete := state.responseNumber > 1
	if err := f.sendResponse(connection, state.activeResponse, state.responseNumber, complete); err != nil {
		return false
	}
	if complete {
		state.activeResponse = ""
	}
	return true
}

// recordAppend stores one append's timing facts and returns whether it was
// silent.
func (f *customerSimulationFixture) recordAppend(silent bool) bool {
	now := time.Now()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.appends = append(f.appends, customerSimulationAppend{at: now, silent: silent})
	if silent {
		f.silentAppends++
		f.committedTurns++
		return true
	}
	f.nonSilentAppends++
	if f.nonSilentAppends == 2 {
		f.correctionAt = now
	}
	if f.nonSilentAppends >= 3 && len(f.appends) > 1 {
		f.finalAppendAfterFirst = f.appends[len(f.appends)-2].at.Before(now)
	}
	return false
}

// finishCancelledResponse closes the response the customer interrupted
// before the correction's response starts.
func (f *customerSimulationFixture) finishCancelledResponse(connection *websocket.Conn, responseID string) bool {
	if err := f.send(connection, map[string]any{
		"type":     "response.output_audio.done",
		"response": map[string]string{"id": responseID},
	}); err != nil {
		return false
	}
	if err := f.send(connection, map[string]any{
		"type":     rtEventResponseDone,
		"response": map[string]string{"id": responseID, "status": rtStatusCancelled},
	}); err != nil {
		return false
	}
	f.recordResponseTerminal(rtStatusCancelled)
	return true
}

func (f *customerSimulationFixture) finishInput(ctx context.Context, progress *probe.DuplexProgress) error {
	if err := progress.WaitForOutputSequence(ctx, []byte{3, 0x10, 0x20, 0x30}); err != nil {
		return err
	}
	f.ReleaseClose()
	return nil
}

func (f *customerSimulationFixture) handshake(connection *websocket.Conn) error {
	f.mu.Lock()
	f.sessionUpdates++
	f.mu.Unlock()
	if err := f.send(connection, map[string]any{"type": rtEventSessionCreated, "session": map[string]string{"id": "customer-simulation", "model": "gpt-realtime"}}); err != nil {
		return err
	}
	return f.send(connection, map[string]any{"type": "session.updated", "session": map[string]string{"id": "customer-simulation"}})
}

func (f *customerSimulationFixture) receiveCancel(connection *websocket.Conn, activeResponse string) (bool, error) {
	f.mu.Lock()
	f.cancelCount++
	if activeResponse != "" {
		f.cancelAt = time.Now()
	}
	f.mu.Unlock()
	if activeResponse == "" {
		return false, f.rejectInactiveCancel(connection)
	}
	return true, nil
}

// A provider terminal and a client interruption cross in flight. Model the
// real nonterminal rejection rather than closing the transport for that race.
func (f *customerSimulationFixture) rejectInactiveCancel(connection *websocket.Conn) error {
	f.mu.Lock()
	f.inactiveCancelCount++
	f.mu.Unlock()
	return f.send(connection, map[string]any{
		"type":  "error",
		"error": map[string]string{"type": "invalid_request_error", "code": "response_cancel_not_active", "param": rtEventResponseCancel, rtItemMessage: "Can only cancel an active response."},
	})
}

func (f *customerSimulationFixture) send(connection *websocket.Conn, event any) error {
	return connection.WriteJSON(event)
}

func (f *customerSimulationFixture) sendResponse(connection *websocket.Conn, responseID string, responseNumber int, complete bool) error {
	f.mu.Lock()
	if responseNumber == 1 {
		f.firstOutputAt = time.Now()
	}
	f.mu.Unlock()
	if err := f.send(connection, map[string]any{"type": "input_audio_buffer.speech_started"}); err != nil {
		return err
	}
	audio := []byte{byte(responseNumber), 0x10, 0x20, 0x30}
	if err := f.send(connection, map[string]any{
		"type":     rtEventResponseCreated,
		"response": map[string]string{"id": responseID},
	}); err != nil {
		return err
	}
	if err := f.send(connection, map[string]any{
		"type":   rtEventOutputAudioDelta,
		"delta":  base64.StdEncoding.EncodeToString(audio),
		"format": "pcm16",
	}); err != nil {
		return err
	}
	if !complete {
		return nil
	}
	if err := f.send(connection, map[string]any{
		"type":     "response.output_audio.done",
		"response": map[string]string{"id": responseID},
	}); err != nil {
		return err
	}
	if err := f.send(connection, map[string]any{
		"type":     rtEventResponseDone,
		"response": map[string]string{"id": responseID, "status": rtStatusCompleted},
	}); err != nil {
		return err
	}
	f.recordResponseTerminal(rtStatusCompleted)
	return nil
}

func (f *customerSimulationFixture) recordResponseTerminal(status string) {
	f.mu.Lock()
	f.responseTerminalStatuses = append(f.responseTerminalStatuses, status)
	f.mu.Unlock()
}

func (f *customerSimulationFixture) failProtocol(message string) {
	f.mu.Lock()
	if f.protocolError == "" {
		f.protocolError = message
	}
	f.mu.Unlock()
}

// customerSimulationCorrectionSeed makes the correction speech (RMS ~20.6k)
// clearly louder than the response it interrupts (RMS ~9.2k): the runner's
// barge-in ignores input that could be the agent's own echo.
const customerSimulationCorrectionSeed = 0x50

func customerSimulationFrame(seed byte) []byte {
	frame := make([]byte, probe.DefaultDuplexFrameSamples*2)
	for index := range frame {
		frame[index] = seed
	}
	return frame
}

func customerSimulationSilent(audio []byte) bool {
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

func containsIntegrationString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func assertFamilyAProcessEvidence(t *testing.T, result probe.DuplexRunResult) {
	t.Helper()
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
}

func evaluateFamilyARun(t *testing.T, scenario probe.CustomerScenario, observation familyAProviderObservation, checkpointCopy []probe.FilesystemCheckpoint) {
	t.Helper()
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
}
