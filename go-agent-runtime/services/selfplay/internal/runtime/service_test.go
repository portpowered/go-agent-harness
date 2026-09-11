package runtime

import (
	"context"
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

type testAdmission struct{ err error }

func (a testAdmission) ValidateSessionModel(string, string) error { return a.err }

type testInferencer struct {
	mu     sync.Mutex
	closed int
}

func (i *testInferencer) ConnectSession(context.Context) (messages.Session, error) { return nil, nil }
func (i *testInferencer) Close() error {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.closed++
	return nil
}

type testInput struct{}

func (testInput) SendAudioInput(context.Context, []byte) error { return nil }

type testRunner struct{}

func (testRunner) Run(_ context.Context, _ messages.SessionInferencer, options selfplay.SessionRunOptions) error {
	if options.Ready != nil {
		options.Ready <- testInput{}
	}
	for index := 0; index < 3; index++ {
		if options.ObserveStream != nil {
			options.ObserveStream(messages.StreamMessage{
				Type: messages.StreamTypeAudioDelta, Role: messages.RoleAssistant,
				Value: messages.NewAudioDeltaValue([]byte{0, byte(index + 1)}),
			})
		}
		if options.AdmitTurn != nil && !options.AdmitTurn(messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant}) {
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

func testService(factory selfplay.SessionFactory, runner selfplay.SessionRunner) selfplay.Service {
	return New(selfplay.Dependencies{
		Clock: clock.Real{}, ModelAdmission: testAdmission{}, Sessions: factory, Runner: runner,
	})
}

func TestRunWithResultStopsAtSharedTurnTargetAndFinalizesEvidence(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "bundle")
	factory := selfplay.SessionFactoryFunc(func(context.Context, selfplay.SessionRequest) (messages.SessionInferencer, error) {
		return &testInferencer{}, nil
	})
	var output []byte
	result, err := testService(factory, testRunner{}).RunWithResult(context.Background(), writerFunc(func(p []byte) (int, error) {
		output = append(output, p...)
		return len(p), nil
	}), selfplay.RunOptions{OutputDir: destination, MaxDuration: time.Second, MaxTurns: 3})
	if err != nil {
		t.Fatalf("RunWithResult: %v", err)
	}
	if result.StopReason != selfplay.StopTurnTarget || result.CustomerTurns != 3 || result.AssistantTurns != 3 {
		t.Fatalf("result = %+v", result)
	}
	if len(output) == 0 {
		t.Fatal("missing presentation result")
	}
	for _, name := range []string{selfplay.AgentAWAVPath, selfplay.AgentBWAVPath, selfplay.AgentADiagnosticsPath, selfplay.AgentBDiagnosticsPath, selfplay.AgentAStreamDeltasPath, selfplay.AgentBStreamDeltasPath, selfplay.ManifestPath} {
		if _, err := os.Stat(filepath.Join(destination, name)); err != nil {
			t.Fatalf("artifact %s: %v", name, err)
		}
	}
	manifest, err := os.ReadFile(filepath.Join(destination, selfplay.ManifestPath))
	if err != nil || len(manifest) == 0 {
		t.Fatalf("manifest: %v", err)
	}
}

func TestRunWithResultClosesCustomerWhenAssistantConstructionFails(t *testing.T) {
	first := &testInferencer{}
	second := &testInferencer{}
	factory := selfplay.SessionFactoryFunc(func(_ context.Context, request selfplay.SessionRequest) (messages.SessionInferencer, error) {
		if request.Persona == selfplay.SelfPlayCustomerPersona {
			return first, nil
		}
		return second, errors.New("assistant construction failed")
	})
	_, err := testService(factory, testRunner{}).RunWithResult(context.Background(), io.Discard, selfplay.RunOptions{
		OutputDir: filepath.Join(t.TempDir(), "bundle"), MaxDuration: time.Second, MaxTurns: 1,
	})
	if err == nil {
		t.Fatal("expected assistant construction error")
	}
	if first.closed != 1 {
		t.Fatalf("customer close count = %d, want 1 (err=%v)", first.closed, err)
	}
	if second.closed != 1 {
		t.Fatalf("assistant partial close count = %d, want 1 (err=%v)", second.closed, err)
	}
}

func TestRunWithResultClosesBothSessionsAfterTerminalRun(t *testing.T) {
	var sessions []*testInferencer
	factory := selfplay.SessionFactoryFunc(func(context.Context, selfplay.SessionRequest) (messages.SessionInferencer, error) {
		session := &testInferencer{}
		sessions = append(sessions, session)
		return session, nil
	})
	result, err := testService(factory, testRunner{}).RunWithResult(context.Background(), io.Discard, selfplay.RunOptions{
		OutputDir: filepath.Join(t.TempDir(), "bundle"), MaxDuration: time.Second, MaxTurns: 1,
	})
	if err != nil || result.StopReason != selfplay.StopTurnTarget {
		t.Fatalf("terminal run = %+v, err=%v", result, err)
	}
	if len(sessions) != 2 {
		t.Fatalf("constructed sessions = %d, want 2", len(sessions))
	}
	for index, session := range sessions {
		if session.closed != 1 {
			t.Fatalf("session %d close count = %d, want 1", index, session.closed)
		}
	}
}

func TestRunWithResultClosesCustomerWhenCustomerConstructionReturnsError(t *testing.T) {
	partial := &testInferencer{}
	factory := selfplay.SessionFactoryFunc(func(context.Context, selfplay.SessionRequest) (messages.SessionInferencer, error) {
		return partial, errors.New("customer construction failed")
	})
	_, err := testService(factory, testRunner{}).RunWithResult(context.Background(), io.Discard, selfplay.RunOptions{
		OutputDir: filepath.Join(t.TempDir(), "bundle"), MaxDuration: time.Second, MaxTurns: 1,
	})
	if err == nil || partial.closed != 1 {
		t.Fatalf("customer partial construction = close:%d err:%v, want one close and error", partial.closed, err)
	}
}

func TestAdmissionRejectsMissingModelPort(t *testing.T) {
	called := false
	service := New(selfplay.Dependencies{
		Clock: clock.Real{}, Sessions: selfplay.SessionFactoryFunc(func(context.Context, selfplay.SessionRequest) (messages.SessionInferencer, error) {
			called = true
			return &testInferencer{}, nil
		}), Runner: testRunner{},
	})
	_, err := service.RunWithResult(context.Background(), io.Discard, selfplay.RunOptions{OutputDir: filepath.Join(t.TempDir(), "bundle"), MaxDuration: time.Second, MaxTurns: 1})
	if err == nil || called {
		t.Fatalf("missing admission error = %v, factory called=%v", err, called)
	}
}

func TestOutputTargetRejectsOccupiedDirectoryBeforeFactory(t *testing.T) {
	destination := t.TempDir()
	if err := os.WriteFile(filepath.Join(destination, "occupied"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	called := false
	service := New(selfplay.Dependencies{
		Clock: clock.Real{}, ModelAdmission: testAdmission{}, Runner: testRunner{},
		Sessions: selfplay.SessionFactoryFunc(func(context.Context, selfplay.SessionRequest) (messages.SessionInferencer, error) {
			called = true
			return &testInferencer{}, nil
		}),
	})
	_, err := service.RunWithResult(context.Background(), io.Discard, selfplay.RunOptions{OutputDir: destination, MaxDuration: time.Second, MaxTurns: 1})
	if err == nil || called {
		t.Fatalf("occupied output error = %v, factory called=%v", err, called)
	}
}

func TestFactoryRejectsNilInferencer(t *testing.T) {
	service := testService(selfplay.SessionFactoryFunc(func(context.Context, selfplay.SessionRequest) (messages.SessionInferencer, error) {
		return nil, nil
	}), testRunner{})
	_, err := service.RunWithResult(context.Background(), io.Discard, selfplay.RunOptions{OutputDir: filepath.Join(t.TempDir(), "bundle"), MaxDuration: time.Second, MaxTurns: 1})
	if err == nil {
		t.Fatal("nil inferencer was accepted")
	}
}

func TestCleanupRemovesPartialEvidenceBundle(t *testing.T) {
	destination := t.TempDir()
	if err := os.Mkdir(filepath.Join(destination, selfplay.AgentBWAVPath), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := newEvidence(destination, selfplay.RunOptions{Provider: selfplay.SelfPlayDefaultProvider, Model: selfplay.SelfPlayDefaultModel, MaxDuration: time.Second, MaxTurns: 1}, time.Now())
	if err == nil {
		t.Fatal("partial evidence setup unexpectedly succeeded")
	}
	for _, name := range []string{selfplay.AgentAWAVPath, selfplay.AgentADiagnosticsPath, selfplay.AgentAStreamDeltasPath} {
		if _, statErr := os.Stat(filepath.Join(destination, name)); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("partial artifact %s remains: %v", name, statErr)
		}
	}
}

type waitingRunner struct{}

func (waitingRunner) Run(_ context.Context, _ messages.SessionInferencer, options selfplay.SessionRunOptions) error {
	if options.Done != nil {
		<-options.Done
	}
	return nil
}

func TestRunWithResultStopsOnDuration(t *testing.T) {
	factory := selfplay.SessionFactoryFunc(func(context.Context, selfplay.SessionRequest) (messages.SessionInferencer, error) {
		return &testInferencer{}, nil
	})
	result, err := testService(factory, waitingRunner{}).RunWithResult(context.Background(), io.Discard, selfplay.RunOptions{
		OutputDir: filepath.Join(t.TempDir(), "bundle"), MaxDuration: 5 * time.Millisecond, MaxTurns: 1,
	})
	if err != nil || result.StopReason != selfplay.StopMaxDuration {
		t.Fatalf("duration result = %+v, err=%v", result, err)
	}
}

func TestRunWithResultStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	factory := selfplay.SessionFactoryFunc(func(context.Context, selfplay.SessionRequest) (messages.SessionInferencer, error) {
		return &testInferencer{}, nil
	})
	result, err := testService(factory, waitingRunner{}).RunWithResult(ctx, io.Discard, selfplay.RunOptions{
		OutputDir: filepath.Join(t.TempDir(), "bundle"), MaxDuration: time.Second, MaxTurns: 1,
	})
	if err == nil || result.StopReason != selfplay.StopFailure || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation result = %+v, err=%v", result, err)
	}
}

func TestRunDelegatesAndSmallLifecycleBranches(t *testing.T) {
	service := testService(selfplay.SessionFactoryFunc(func(context.Context, selfplay.SessionRequest) (messages.SessionInferencer, error) {
		return &testInferencer{}, nil
	}), waitingRunner{})
	if err := service.Run(context.Background(), io.Discard, selfplay.RunOptions{
		OutputDir: filepath.Join(t.TempDir(), "bundle"), MaxDuration: time.Second, MaxTurns: 1,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	var nilService *Service
	if err := nilService.Run(context.Background(), io.Discard, selfplay.RunOptions{}); err == nil {
		t.Fatal("nil service Run unexpectedly succeeded")
	}

	stop := newStopState(nil)
	want := errors.New("lifecycle failure")
	if !stop.fail(want) || !errors.Is(stop.doneErr(), want) || !stop.stopped() {
		t.Fatalf("failed stop state: done_err=%v stopped=%t", stop.doneErr(), stop.stopped())
	}
	if stop.stop(selfplay.StopMaxDuration, nil) {
		t.Fatal("terminal stop was accepted twice")
	}
	if !isCancellation(context.Canceled) || !isCancellation(context.DeadlineExceeded) || !isCancellation(io.ErrClosedPipe) || isCancellation(want) {
		t.Fatal("cancellation classification changed")
	}
	if !assistantAudioDelta(messages.StreamMessage{Role: ""}) || !assistantAudioDelta(messages.StreamMessage{Role: messages.RoleAssistant}) || assistantAudioDelta(messages.StreamMessage{Role: messages.RoleUser}) {
		t.Fatal("assistant audio role classification changed")
	}
	if turnIndex(map[string]string{"turn_index": "4"}) != 4 || turnIndex(map[string]string{"turn_index": "bad"}) != 0 {
		t.Fatal("turn index parsing changed")
	}
}

func TestEvidenceDiagnosticTurnBoundAndInputCompatibility(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "bundle")
	if err := os.MkdirAll(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	options := selfplay.RunOptions{Provider: selfplay.SelfPlayDefaultProvider, Model: selfplay.SelfPlayDefaultModel, MaxDuration: time.Second, MaxTurns: 1}
	evidence, err := newEvidence(destination, options, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := evidence.observeDiagnostic(0, selfplay.Diagnostic{Event: "session_turn_completed", Fields: map[string]string{"turn_index": "2"}}); err != nil {
		t.Fatalf("over-bound diagnostic: %v", err)
	}
	evidence.observeInput(0, []byte{1, 2})
	if err := evidence.observeDiagnostic(0, selfplay.Diagnostic{Event: "session_terminal", Fields: map[string]string{"classification": "clean"}}); err != nil {
		t.Fatalf("terminal diagnostic: %v", err)
	}
	if err := evidence.finalize(selfplay.Result{StopReason: selfplay.StopTurnTarget, CustomerTurns: 1, AssistantTurns: 1}, nil, time.Now()); err != nil {
		t.Fatalf("finalize: %v", err)
	}
}

func TestNormalizeRejectsInvalidOptions(t *testing.T) {
	valid := selfplay.RunOptions{Provider: selfplay.SelfPlayDefaultProvider, Model: selfplay.SelfPlayDefaultModel, MaxDuration: time.Second, MaxTurns: 1, OutputDir: filepath.Join(t.TempDir(), "bundle")}
	cases := []struct {
		name    string
		options selfplay.RunOptions
		deps    selfplay.Dependencies
	}{
		{name: "provider", options: func() selfplay.RunOptions { value := valid; value.Provider = "other"; return value }(), deps: selfplay.Dependencies{Clock: clock.Real{}, ModelAdmission: testAdmission{}}},
		{name: "admission", options: valid, deps: selfplay.Dependencies{Clock: clock.Real{}, ModelAdmission: testAdmission{err: errors.New("model rejected")}}},
		{name: "duration", options: func() selfplay.RunOptions { value := valid; value.MaxDuration = 0; return value }(), deps: selfplay.Dependencies{Clock: clock.Real{}, ModelAdmission: testAdmission{}}},
		{name: "turns", options: func() selfplay.RunOptions { value := valid; value.MaxTurns = 0; return value }(), deps: selfplay.Dependencies{Clock: clock.Real{}, ModelAdmission: testAdmission{}}},
		{name: "output", options: func() selfplay.RunOptions { value := valid; value.OutputDir = " "; return value }(), deps: selfplay.Dependencies{Clock: clock.Real{}, ModelAdmission: testAdmission{}}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if _, err := normalize(test.options, test.deps); err == nil {
				t.Fatal("normalize unexpectedly accepted invalid options")
			}
		})
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }
