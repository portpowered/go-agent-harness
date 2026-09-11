package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/selfplay"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

type regressionInferencer struct {
	mu     sync.Mutex
	closed int
}

func (*regressionInferencer) ConnectSession(context.Context) (messages.Session, error) {
	return nil, nil
}

func (i *regressionInferencer) Close() error {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.closed++
	return nil
}

func (i *regressionInferencer) closeCount() int {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.closed
}

type regressionInput struct {
	mu       sync.Mutex
	received [][]byte
	onSend   func()
}

func (i *regressionInput) SendAudioInput(ctx context.Context, pcm []byte) error {
	if ctx != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
	}
	i.mu.Lock()
	i.received = append(i.received, append([]byte(nil), pcm...))
	i.mu.Unlock()
	if i.onSend != nil {
		i.onSend()
	}
	return nil
}

func (i *regressionInput) bytes() []byte {
	i.mu.Lock()
	defer i.mu.Unlock()
	var result []byte
	for _, chunk := range i.received {
		result = append(result, chunk...)
	}
	return result
}

type regressionRunner struct {
	mu              sync.Mutex
	inputs          [2]*regressionInput
	turnBarrier     [2]*sync.WaitGroup
	failSide        int
	failErr         error
	deliveryEnabled bool
	deliveryMu      sync.Mutex
	deliveries      int
	deliveryWake    chan struct{}
}

func (r *regressionRunner) recordDelivery() {
	r.deliveryMu.Lock()
	r.deliveries++
	if r.deliveryWake != nil {
		close(r.deliveryWake)
		r.deliveryWake = make(chan struct{})
	}
	r.deliveryMu.Unlock()
}

func (r *regressionRunner) waitForDeliveries(target int) {
	for {
		r.deliveryMu.Lock()
		if r.deliveries >= target {
			r.deliveryMu.Unlock()
			return
		}
		wake := r.deliveryWake
		r.deliveryMu.Unlock()
		<-wake
	}
}

func (r *regressionRunner) Run(_ context.Context, _ messages.SessionInferencer, options selfplay.SessionRunOptions) error {
	side := 1
	if options.Prompt != "" {
		side = 0
	}
	input := &regressionInput{}
	if r.deliveryEnabled {
		input.onSend = r.recordDelivery
	}
	r.mu.Lock()
	r.inputs[side] = input
	failSide, failErr := r.failSide, r.failErr
	r.mu.Unlock()
	if options.Ready != nil {
		options.Ready <- input
	}
	if side == failSide && failErr != nil {
		return failErr
	}
	for turn := 0; turn < 2; turn++ {
		if turn == 0 && options.ObserveStream != nil {
			options.ObserveStream(messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("not bridged")})
			options.ObserveStream(messages.StreamMessage{Type: messages.StreamTypeToolCallStart, Role: messages.RoleAssistant, Value: messages.NewToolCallStartValue("call", "not_bridged")})
			options.ObserveStream(messages.StreamMessage{Type: messages.StreamTypeToolCallDelta, Role: messages.RoleAssistant, Value: messages.NewToolCallDeltaValue(`{"value":true}`)})
			options.ObserveStream(messages.StreamMessage{Type: messages.StreamTypeToolCallEnd, Role: messages.RoleAssistant, Value: messages.NewToolCallEndValue("call", "not_bridged", `{"value":true}`)})
			options.ObserveStream(messages.StreamMessage{Type: messages.StreamTypeAudioDelta, Role: messages.RoleUser, Value: messages.NewAudioDeltaValue([]byte{9, 10})})
		}
		if options.ObserveStream != nil {
			base := byte(1 + side*4 + turn*2)
			options.ObserveStream(messages.StreamMessage{Type: messages.StreamTypeAudioDelta, Role: messages.RoleAssistant, Value: messages.NewAudioDeltaValue([]byte{base, base + 1})})
		}
		if barrier := r.turnBarrier[turn]; barrier != nil {
			barrier.Done()
			barrier.Wait()
		}
		if r.deliveryEnabled {
			r.waitForDeliveries((turn + 1) * 2)
		}
		if options.AdmitTurn != nil && !options.AdmitTurn(messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})}) {
			break
		}
	}
	if options.ObserveDiagnostic != nil {
		options.ObserveDiagnostic(selfplay.Diagnostic{Event: "session_terminal", Fields: map[string]string{"classification": "clean"}})
	}
	if options.Done != nil {
		<-options.Done
	}
	return nil
}

type regressionFactory struct {
	mu       sync.Mutex
	requests []selfplay.SessionRequest
	sessions []*regressionInferencer
}

func (f *regressionFactory) NewSession(_ context.Context, request selfplay.SessionRequest) (messages.SessionInferencer, error) {
	session := &regressionInferencer{}
	f.mu.Lock()
	f.requests = append(f.requests, request)
	f.sessions = append(f.sessions, session)
	f.mu.Unlock()
	return session, nil
}

func (f *regressionFactory) snapshot() ([]selfplay.SessionRequest, []*regressionInferencer) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]selfplay.SessionRequest(nil), f.requests...), append([]*regressionInferencer(nil), f.sessions...)
}

func regressionService(factory selfplay.SessionFactory, runner selfplay.SessionRunner) selfplay.Service {
	return New(selfplay.Dependencies{Clock: clock.Real{}, ModelAdmission: testAdmission{}, Sessions: factory, Runner: runner})
}

func regressionOptions(destination string) selfplay.RunOptions {
	return selfplay.RunOptions{APIKey: "test-secret", OutputDir: destination, Provider: " OPENAI ", Model: " gpt-realtime ", MaxDuration: time.Second, MaxTurns: 2}
}

func TestConversationBridgesOnlyOrderedAssistantPCMAndFinalizesArtifacts(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "bundle")
	factory := &regressionFactory{}
	runner := &regressionRunner{failSide: -1, deliveryEnabled: true, deliveryWake: make(chan struct{})}
	for turn := range runner.turnBarrier {
		barrier := &sync.WaitGroup{}
		barrier.Add(2)
		runner.turnBarrier[turn] = barrier
	}
	var presentation bytes.Buffer
	result, err := regressionService(factory, runner).RunWithResult(context.Background(), &presentation, regressionOptions(destination))
	assertConversationResult(t, result, err, presentation.Bytes())
	requests, sessions := factory.snapshot()
	assertConversationSessions(t, requests, sessions)
	assertConversationBridges(t, runner)
	assertConversationArtifacts(t, destination)
	assertConversationManifest(t, destination)
}

func assertConversationResult(t *testing.T, result selfplay.Result, err error, presentation []byte) {
	t.Helper()
	if err != nil {
		t.Fatalf("self-play conversation: %v", err)
	}
	if result != (selfplay.Result{StopReason: selfplay.StopTurnTarget, CustomerTurns: 2, AssistantTurns: 2}) {
		t.Fatalf("result = %+v", result)
	}
	if !bytes.Contains(presentation, []byte("reason=turn_target customer_turns=2 assistant_turns=2")) {
		t.Fatalf("presentation = %q", presentation)
	}
}

func assertConversationSessions(t *testing.T, requests []selfplay.SessionRequest, sessions []*regressionInferencer) {
	t.Helper()
	if len(requests) != 2 || len(sessions) != 2 || sessions[0] == sessions[1] {
		t.Fatalf("factory construction = requests:%d sessions:%d distinct:%t", len(requests), len(sessions), len(sessions) == 2 && sessions[0] != sessions[1])
	}
	if requests[0].Prompt != selfplay.SelfPlayOpeningSeed || requests[1].Prompt != "" || requests[0].Persona != selfplay.SelfPlayCustomerPersona || requests[1].Persona != selfplay.SelfPlayAssistantPersona {
		t.Fatalf("session requests = %+v", requests)
	}
	if requests[0].Provider != selfplay.SelfPlayDefaultProvider || requests[0].Model != selfplay.SelfPlayDefaultModel || requests[0].Clock == nil || requests[1].Clock == nil {
		t.Fatalf("normalized session requests = %+v", requests)
	}
	for index, session := range sessions {
		if got := session.closeCount(); got != 1 {
			t.Fatalf("session %d close count = %d, want 1", index, got)
		}
	}
}

func assertConversationBridges(t *testing.T, runner *regressionRunner) {
	t.Helper()
	runner.mu.Lock()
	inputs := [2]*regressionInput{runner.inputs[0], runner.inputs[1]}
	runner.mu.Unlock()
	if got, want := inputs[0].bytes(), []byte{5, 6, 7, 8}; !bytes.Equal(got, want) {
		t.Fatalf("customer bridge bytes = %v, want %v", got, want)
	}
	if got, want := inputs[1].bytes(), []byte{1, 2, 3, 4}; !bytes.Equal(got, want) {
		t.Fatalf("assistant bridge bytes = %v, want %v", got, want)
	}
}

func assertConversationArtifacts(t *testing.T, destination string) {
	t.Helper()
	for _, name := range []string{selfplay.AgentAWAVPath, selfplay.AgentBWAVPath, selfplay.AgentADiagnosticsPath, selfplay.AgentBDiagnosticsPath, selfplay.AgentAStreamDeltasPath, selfplay.AgentBStreamDeltasPath, selfplay.ManifestPath} {
		if _, err := os.Stat(filepath.Join(destination, name)); err != nil {
			t.Fatalf("artifact %s: %v", name, err)
		}
	}
	for path, want := range map[string][]byte{
		selfplay.AgentAWAVPath: {1, 2, 3, 4},
		selfplay.AgentBWAVPath: {5, 6, 7, 8},
	} {
		data, err := os.ReadFile(filepath.Join(destination, path))
		if err != nil {
			t.Fatal(err)
		}
		if len(data) < 44 || !bytes.Equal(data[44:], want) {
			t.Fatalf("%s PCM = %v, want %v", path, data, want)
		}
	}
}

func assertConversationManifest(t *testing.T, destination string) {
	t.Helper()
	var manifest struct {
		StopReason selfplay.StopReason `json:"stop_reason"`
		Agents     map[string]struct {
			CompletedTurns int  `json:"completed_turns"`
			TerminalClean  bool `json:"terminal_clean"`
		} `json:"agents"`
	}
	data, err := os.ReadFile(filepath.Join(destination, selfplay.ManifestPath))
	if err != nil || json.Unmarshal(data, &manifest) != nil {
		t.Fatalf("manifest: %v", err)
	}
	if manifest.StopReason != selfplay.StopTurnTarget || manifest.Agents["agent-a"].CompletedTurns != 2 || manifest.Agents["agent-b"].CompletedTurns != 2 || !manifest.Agents["agent-a"].TerminalClean || !manifest.Agents["agent-b"].TerminalClean {
		t.Fatalf("manifest = %+v", manifest)
	}
}

func TestConversationSideFailureCancelsPeerAndClosesBothSessions(t *testing.T) {
	wantErr := errors.New("customer dial failed")
	factory := &regressionFactory{}
	runner := &regressionRunner{failSide: 0, failErr: wantErr}
	result, err := regressionService(factory, runner).RunWithResult(context.Background(), io.Discard, regressionOptions(filepath.Join(t.TempDir(), "bundle")))
	if err == nil || !errors.Is(err, wantErr) || result.StopReason != selfplay.StopFailure {
		t.Fatalf("side failure result = %+v, err=%v", result, err)
	}
	_, sessions := factory.snapshot()
	if len(sessions) != 2 {
		t.Fatalf("sessions = %d, want 2", len(sessions))
	}
	for index, session := range sessions {
		if session.closeCount() != 1 {
			t.Fatalf("session %d close count = %d, want 1", index, session.closeCount())
		}
	}
}

func TestConversationRejectsOddPCMBeforeBridge(t *testing.T) {
	factory := &regressionFactory{}
	// Replace the runner with a one-shot source that emits malformed PCM on the
	// customer side and otherwise follows the runtime stop boundary.
	malformed := selfplay.SessionRunnerFunc(func(_ context.Context, _ messages.SessionInferencer, options selfplay.SessionRunOptions) error {
		if options.Prompt != "" {
			if options.Ready != nil {
				options.Ready <- &regressionInput{}
			}
			if options.ObserveStream != nil {
				options.ObserveStream(messages.StreamMessage{Type: messages.StreamTypeAudioDelta, Role: messages.RoleAssistant, Value: messages.NewAudioDeltaValue([]byte{1, 2, 3})})
			}
		}
		if options.Done != nil {
			<-options.Done
		}
		return nil
	})
	result, err := regressionService(factory, malformed).RunWithResult(context.Background(), io.Discard, regressionOptions(filepath.Join(t.TempDir(), "bundle")))
	if err == nil || result.StopReason != selfplay.StopFailure || !bytes.Contains([]byte(err.Error()), []byte("odd PCM16")) {
		t.Fatalf("odd PCM result = %+v, err=%v", result, err)
	}
	_, sessions := factory.snapshot()
	if len(sessions) != 2 {
		t.Fatalf("sessions = %d, want 2", len(sessions))
	}
	for index, session := range sessions {
		if session.closeCount() != 1 {
			t.Fatalf("session %d close count = %d, want 1", index, session.closeCount())
		}
	}
}

func TestConversationEvidenceQuotaFailsBeforePCMBridge(t *testing.T) {
	factory := &regressionFactory{}
	runner := &regressionRunner{failSide: -1}
	options := regressionOptions(filepath.Join(t.TempDir(), "bundle"))
	options.EvidenceLimits = selfplay.EvidenceLimits{WAVBytes: 1}
	result, err := regressionService(factory, runner).RunWithResult(context.Background(), io.Discard, options)
	if err == nil || !errors.Is(err, selfplay.ErrEvidenceQuota) || result.StopReason != selfplay.StopFailure {
		t.Fatalf("quota result = %+v, err=%v", result, err)
	}
	runner.mu.Lock()
	inputs := [2]*regressionInput{runner.inputs[0], runner.inputs[1]}
	runner.mu.Unlock()
	for index, input := range inputs {
		if input != nil && len(input.bytes()) != 0 {
			t.Fatalf("side %d received PCM after evidence quota failure: %v", index, input.bytes())
		}
	}
}

func TestTerminalSnapshotPublishesFirstTransitionOnce(t *testing.T) {
	const target = 2
	stop := newStopState(nil)
	if !stop.recordTurn(0, target) || !stop.recordTurn(1, target) {
		t.Fatal("failed to establish the terminal barrier")
	}
	if !stop.recordTurn(1, target) {
		t.Fatal("failed to establish the assistant lead")
	}
	start := make(chan struct{})
	final := make(chan bool, 1)
	contender := make(chan bool, 1)
	go func() {
		<-start
		final <- stop.recordTurn(1, target)
	}()
	go func() {
		<-start
		contender <- stop.stop(selfplay.StopMaxDuration, nil)
	}()
	close(start)
	finalAccepted, contenderAccepted := <-final, <-contender
	result, err := stop.snapshot()
	if finalAccepted == contenderAccepted || result.CustomerTurns < 1 || result.CustomerTurns > target || result.AssistantTurns != target {
		t.Fatalf("transition final=%t contender=%t result=%+v err=%v", finalAccepted, contenderAccepted, result, err)
	}
	if finalAccepted && result.StopReason != selfplay.StopTurnTarget {
		t.Fatalf("target-first reason = %q", result.StopReason)
	}
	if contenderAccepted && result.StopReason != selfplay.StopMaxDuration {
		t.Fatalf("contender-first reason = %q", result.StopReason)
	}
	for range 8 {
		repeated, repeatedErr := stop.snapshot()
		if repeated != result || (repeatedErr == nil) != (err == nil) {
			t.Fatalf("terminal snapshot changed: first=%+v/%v later=%+v/%v", result, err, repeated, repeatedErr)
		}
	}
	if stop.recordTurn(1, target) {
		t.Fatal("post-terminal turn was admitted")
	}
}
