package probe

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestDeadSessionGuardRunsEveryApplicableControlOnceWithFreshSubjects(t *testing.T) {
	registry := NewScenarioRegistry()
	scenario := Scenario{
		ID: "healthy-scenario",
		Steps: []Step{
			{Type: StepSendText, Text: "ping"},
			{Type: StepSendAudio, CorpusID: "fixture"},
			{Type: StepClose},
		},
		Expectations: []ExpectedBehavior{
			{Type: ExpectTranscriptContains, Text: "reply"},
			{Type: ExpectAudioEnergy},
		},
	}
	if err := registry.Register(scenario); err != nil {
		t.Fatal(err)
	}

	var subjects []*observedGuardSubject
	factory := SubjectFactory(func(control DeadSessionControl, scenario Scenario) (DeadSessionSubject, error) {
		inner, err := DefaultDeadSessionSubjectFactory(control, scenario)
		if err != nil {
			return nil, err
		}
		observed := &observedGuardSubject{inner: inner, control: control}
		subjects = append(subjects, observed)
		return observed, nil
	})
	guard := NewDeadSessionGuard(DeadSessionGuardConfig{
		Registry:       registry,
		SubjectFactory: factory,
	})

	result, err := guard.Run(context.Background())
	if err != nil {
		t.Fatalf("healthy negative controls failed: %v", err)
	}
	if !result.Passed() {
		t.Fatalf("healthy result has findings: %#v", result.Findings)
	}
	if got, want := result.RunCount(), 3; got != want {
		t.Fatalf("run count: got %d, want %d", got, want)
	}
	wantControls := []DeadSessionControl{ControlNull, ControlEcho, ControlSilence}
	for index, run := range result.Runs {
		if run.ScenarioID != scenario.ID || run.Control != wantControls[index] {
			t.Fatalf("run %d identity: got %s/%s, want %s/%s", index, run.ScenarioID, run.Control, scenario.ID, wantControls[index])
		}
		if run.Status != DeadSessionExpectedFailure {
			t.Fatalf("run %d status: got %s, want %s", index, run.Status, DeadSessionExpectedFailure)
		}
		if len(run.ExpectationResults) != len(scenario.Expectations) {
			t.Fatalf("run %d expectation evidence: got %d, want %d", index, len(run.ExpectationResults), len(scenario.Expectations))
		}
	}
	if got, want := len(subjects), 3; got != want {
		t.Fatalf("subject count: got %d, want %d", got, want)
	}
	for _, subject := range subjects {
		if subject.accepts != len(scenario.Steps) {
			t.Errorf("%s accepted %d steps, want %d", subject.control, subject.accepts, len(scenario.Steps))
		}
	}
	for left := range subjects {
		for right := left + 1; right < len(subjects); right++ {
			if subjects[left] == subjects[right] {
				t.Fatalf("control subjects share state: %d and %d", left, right)
			}
		}
	}
}

func TestDeadSessionGuardReadsLiveRegistryAtExecutionTime(t *testing.T) {
	ResetScenarioRegistry()
	t.Cleanup(ResetScenarioRegistry)
	guard := NewDeadSessionGuard()

	first := terminalScenario("first-live-entry")
	if err := RegisterScenario(first); err != nil {
		t.Fatal(err)
	}
	result, err := guard.Run(context.Background())
	if err != nil {
		t.Fatalf("first live snapshot failed: %v", err)
	}
	if got, want := result.RunCount(), 1; got != want {
		t.Fatalf("first snapshot run count: got %d, want %d", got, want)
	}
	if result.Runs[0].ScenarioID != first.ID {
		t.Fatalf("first snapshot scenario: got %q, want %q", result.Runs[0].ScenarioID, first.ID)
	}

	second := terminalScenario("second-live-entry")
	if err := RegisterScenario(second); err != nil {
		t.Fatal(err)
	}
	result, err = guard.Run(context.Background())
	if err != nil {
		t.Fatalf("second live snapshot failed: %v", err)
	}
	if got, want := result.RunCount(), 2; got != want {
		t.Fatalf("second snapshot run count: got %d, want %d", got, want)
	}
	if got := []string{result.Runs[0].ScenarioID, result.Runs[1].ScenarioID}; fmt.Sprint(got) != "[first-live-entry second-live-entry]" {
		t.Fatalf("stable live snapshot order: got %v", got)
	}
}

func TestDeadSessionGuardReportsNamedUnexpectedPassesInStableOrder(t *testing.T) {
	registry := NewScenarioRegistry()
	if err := registry.Register(Scenario{
		ID:           "z-silence",
		Steps:        []Step{{Type: StepSendAudio, CorpusID: "fixture"}, {Type: StepClose}},
		Expectations: []ExpectedBehavior{{Type: ExpectFrameCount, Count: 1}},
	}, ControlSilence); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(Scenario{
		ID:           "a-echo",
		Steps:        []Step{{Type: StepSendText, Text: "ping"}, {Type: StepClose}},
		Expectations: []ExpectedBehavior{{Type: ExpectTranscriptContains, Text: "ping"}},
	}, ControlEcho); err != nil {
		t.Fatal(err)
	}

	result, err := NewDeadSessionGuard(registry).Run(context.Background())
	if err == nil {
		t.Fatal("unexpectedly passing degenerate controls returned nil error")
	}
	if !errors.Is(err, ErrDeadSessionGuard) || !errors.Is(err, ErrDeadSessionUnexpectedPass) {
		t.Fatalf("guard error identity: %v", err)
	}
	var guardErr *DeadSessionGuardError
	if !errors.As(err, &guardErr) {
		t.Fatalf("guard error type: %T", err)
	}
	if got, want := len(result.Findings), 2; got != want {
		t.Fatalf("finding count: got %d, want %d", got, want)
	}
	want := []struct {
		id      string
		control DeadSessionControl
	}{
		{id: "a-echo", control: ControlEcho},
		{id: "z-silence", control: ControlSilence},
	}
	for index, finding := range result.Findings {
		if finding.ScenarioID != want[index].id || finding.Control != want[index].control {
			t.Errorf("finding %d: got %s/%s, want %s/%s", index, finding.ScenarioID, finding.Control, want[index].id, want[index].control)
		}
		if finding.Status != DeadSessionUnexpectedPass {
			t.Errorf("finding %d status: got %s, want %s", index, finding.Status, DeadSessionUnexpectedPass)
		}
	}
	message := err.Error()
	if strings.Index(message, `scenario "a-echo" control "echo"`) > strings.Index(message, `scenario "z-silence" control "silence"`) {
		t.Fatalf("findings are not reported in stable order: %s", message)
	}
}

func TestDeadSessionGuardKeepsExecutionErrorsDistinctFromExpectationFailures(t *testing.T) {
	registry := NewScenarioRegistry()
	scenario := terminalScenario("runner-error")
	if err := registry.Register(scenario); err != nil {
		t.Fatal(err)
	}
	runnerErr := errors.New("deterministic runner failure")
	runner := ScenarioRunnerFunc(func(context.Context, Scenario, DeadSessionSubject) (ScenarioRunResult, error) {
		return ScenarioRunResult{}, runnerErr
	})
	result, err := NewDeadSessionGuard(DeadSessionGuardConfig{Registry: registry, Runner: runner}).Run(context.Background())
	if err == nil {
		t.Fatal("execution failure unexpectedly returned nil error")
	}
	if !errors.Is(err, ErrDeadSessionExecution) || errors.Is(err, ErrDeadSessionUnexpectedPass) {
		t.Fatalf("execution error identity: %v", err)
	}
	if got, want := result.Runs[0].Status, DeadSessionExecutionFailure; got != want {
		t.Fatalf("run status: got %s, want %s", got, want)
	}
	if !strings.Contains(err.Error(), "runner-error") || !strings.Contains(err.Error(), "null") {
		t.Fatalf("execution error does not identify scenario/control: %v", err)
	}
}

func TestDeadSessionGuardEvaluatesTypedExpectationControls(t *testing.T) {
	full := ObservationSnapshot{
		PCM16Samples:    []int16{1000, -1000},
		Transcript:      "reply ping",
		ToolCalls:       []string{"calendar"},
		ObservedTick:    5,
		HasObservedTick: true,
		TerminalReason:  "complete",
		FrameCount:      2,
	}
	quiet := ObservationSnapshot{}
	cases := []struct {
		name        string
		expectation ExpectedBehavior
		observation ObservationSnapshot
		wantErr     error
	}{
		{name: "audio-energy-pass", expectation: ExpectedBehavior{Type: ExpectAudioEnergy}, observation: full},
		{name: "transcript-contains-pass", expectation: ExpectedBehavior{Type: ExpectTranscriptContains, Text: "reply"}, observation: full},
		{name: "tool-called-pass", expectation: ExpectedBehavior{Type: ExpectToolCalled, ToolName: "calendar"}, observation: full},
		{name: "latency-pass", expectation: ExpectedBehavior{Type: ExpectLatencyWithinTicks, At: 3, HasAt: true, Count: 2}, observation: full},
		{name: "terminal-reason-pass", expectation: ExpectedBehavior{Type: ExpectTerminalReason, Value: "complete"}, observation: full},
		{name: "frame-count-pass", expectation: ExpectedBehavior{Type: ExpectFrameCount, Count: 2}, observation: full},
		{name: "text-pass", expectation: ExpectedBehavior{Type: ExpectText, Text: "reply ping"}, observation: full},
		{name: "transcript-pass", expectation: ExpectedBehavior{Type: ExpectTranscript, Text: "reply ping"}, observation: full},
		{name: "contains-pass", expectation: ExpectedBehavior{Type: ExpectContains, Text: "ping"}, observation: full},
		{name: "audio-pass", expectation: ExpectedBehavior{Type: ExpectAudio}, observation: full},
		{name: "tool-call-pass", expectation: ExpectedBehavior{Type: ExpectToolCall, ToolCallID: "calendar"}, observation: full},
		{name: "close-pass", expectation: ExpectedBehavior{Type: ExpectClose}, observation: full},
		{name: "time-pass", expectation: ExpectedBehavior{Type: ExpectTime, At: 5, HasAt: true}, observation: full},
		{name: "audio-mismatch", expectation: ExpectedBehavior{Type: ExpectAudio}, observation: quiet, wantErr: ErrExpectationMismatch},
		{name: "text-mismatch", expectation: ExpectedBehavior{Type: ExpectText, Text: "other"}, observation: full, wantErr: ErrExpectationMismatch},
		{name: "close-mismatch", expectation: ExpectedBehavior{Type: ExpectClose}, observation: quiet, wantErr: ErrExpectationMismatch},
		{name: "time-mismatch", expectation: ExpectedBehavior{Type: ExpectTime, At: 4, HasAt: true}, observation: full, wantErr: ErrExpectationMismatch},
		{name: "event-mismatch", expectation: ExpectedBehavior{Type: ExpectEvent, Value: "ready"}, observation: full, wantErr: ErrExpectationMismatch},
		{name: "tool-call-mismatch", expectation: ExpectedBehavior{Type: ExpectToolCall, ToolCallID: "weather"}, observation: full, wantErr: ErrExpectationMismatch},
		{name: "tool-result-mismatch", expectation: ExpectedBehavior{Type: ExpectToolResult, ToolCallID: "calendar"}, observation: full, wantErr: ErrExpectationMismatch},
		{name: "unknown-expectation", expectation: ExpectedBehavior{Type: "unknown"}, observation: full, wantErr: ErrInvalidExpectation},
		{name: "tool-call-invalid", expectation: ExpectedBehavior{Type: ExpectToolCall}, observation: full, wantErr: ErrInvalidExpectation},
		{name: "time-invalid", expectation: ExpectedBehavior{Type: ExpectTime}, observation: full, wantErr: ErrInvalidExpectation},
		{name: "contains-alias-conflict", expectation: ExpectedBehavior{Type: ExpectContains, Text: "a", Value: "b"}, observation: full, wantErr: ErrInvalidExpectation},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			err := evaluateGuardExpectation(test.expectation, test.observation)
			if test.wantErr == nil {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("error identity: got %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestDeadSessionRegistryLifecycleAndGuardConstructionSeams(t *testing.T) {
	var nilRegistry *ScenarioRegistry
	if nilRegistry.Entries() != nil || len(nilRegistry.Snapshot()) != 0 {
		t.Fatal("nil registry unexpectedly returned a snapshot")
	}
	nilRegistry.Unregister("missing")
	nilRegistry.Clear()
	if err := nilRegistry.Register(terminalScenario("nil")); !errors.Is(err, ErrInvalidScenarioRegistration) {
		t.Fatalf("nil registry error: %v", err)
	}

	registry := &ScenarioRegistry{}
	scenario := terminalScenario("registry-entry")
	if err := registry.Register(Scenario{}); !errors.Is(err, ErrInvalidScenarioRegistration) {
		t.Fatalf("missing scenario ID error: %v", err)
	}
	if err := registry.RegisterScenario(scenario); err != nil {
		t.Fatal(err)
	}
	if got := registry.Snapshot(); len(got) != 1 || got[0].ID != scenario.ID {
		t.Fatalf("registry snapshot: %#v", got)
	}
	if err := registry.Register(scenario, DeadSessionControl("unknown")); !errors.Is(err, ErrInvalidScenarioRegistration) {
		t.Fatalf("unknown control error: %v", err)
	}
	registry.Unregister(scenario.ID)
	if got := registry.Snapshot(); len(got) != 0 {
		t.Fatalf("unregister snapshot: %#v", got)
	}
	if err := registry.Register(Scenario{Name: "name-is-stable", Steps: scenario.Steps, Expectations: scenario.Expectations}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(Scenario{
		ID:           "non-applicable",
		Steps:        []Step{{Type: StepSendText, Text: "input"}, {Type: StepSendAudio, CorpusID: "fixture"}, {Type: StepClose}},
		Expectations: []ExpectedBehavior{{Type: ExpectTerminalReason, Value: "complete"}},
	}, ControlNull, ControlNull); err != nil {
		t.Fatal(err)
	}
	registry.Clear()

	if LiveScenarioRegistry() != LiveRegistry || LiveScenarioRegistry() != DefaultScenarioRegistry {
		t.Fatal("live registry aliases diverged")
	}
	ResetScenarioRegistry()
	t.Cleanup(ResetScenarioRegistry)
	if err := RegisterScenario(terminalScenario("global-entry")); err != nil {
		t.Fatal(err)
	}
	if got := Scenarios(); len(got) != 1 || got[0].ID != "global-entry" {
		t.Fatalf("global scenarios: %#v", got)
	}
	UnregisterScenario("global-entry")
	if got := Scenarios(); len(got) != 0 {
		t.Fatalf("global unregister: %#v", got)
	}
	if result, err := RunDeadSessionGuard(context.Background()); err != nil || !result.Passed() {
		t.Fatalf("top-level guard run: result=%#v err=%v", result, err)
	}
	if err := CheckDeadSessionGuard(context.Background()); err != nil {
		t.Fatalf("top-level guard check: %v", err)
	}

	runner := ScenarioRunnerFunc(func(ctx context.Context, scenario Scenario, subject DeadSessionSubject) (ScenarioRunResult, error) {
		return ExpectationScenarioRunner{}.Run(ctx, scenario, subject)
	})
	factory := SubjectFactory(DefaultDeadSessionSubjectFactory)
	guard := NewDeadSessionGuard(
		WithScenarioRegistry(registry),
		WithScenarioRunner(runner),
		WithSubjectFactory(factory),
	)
	if err := guard.Check(context.Background()); err != nil {
		t.Fatalf("option guard check: %v", err)
	}
	if result, err := guard.Execute(context.Background()); err != nil || !result.Healthy() {
		t.Fatalf("option guard execute: result=%#v err=%v", result, err)
	}
	if result := (DeadSessionGuardResult{}).ControlResults(); result != nil {
		t.Fatalf("empty control results: %#v", result)
	}
	if NewDefaultScenarioRunner() == nil || NewDeadSessionGuardWithConfig(DeadSessionGuardConfig{Registry: registry}) == nil {
		t.Fatal("default constructors returned nil")
	}
	if _, err := DefaultDeadSessionSubjectFactory(DeadSessionControl("bad"), Scenario{}); err == nil {
		t.Fatal("unknown subject control unexpectedly succeeded")
	}
	if NewNullSubject() == nil || NewEchoSubject() == nil || NewSilenceSubject() == nil {
		t.Fatal("subject constructors returned nil")
	}
	echo := NewEchoSubject()
	if err := echo.Accept(context.Background(), Step{Kind: StepSendText, Text: "kind-alias"}); err != nil {
		t.Fatal(err)
	}
	if observation, err := echo.Snapshot(context.Background()); err != nil || observation.Transcript != "kind-alias" {
		t.Fatalf("kind alias echo: observation=%#v err=%v", observation, err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, subject := range []DeadSessionSubject{NewNullSubject(), NewEchoSubject(), NewSilenceSubject()} {
		if err := subject.Accept(canceled, Step{}); !errors.Is(err, context.Canceled) {
			t.Errorf("canceled subject accept: %v", err)
		}
		if _, err := subject.Snapshot(canceled); !errors.Is(err, context.Canceled) {
			t.Errorf("canceled subject snapshot: %v", err)
		}
	}
}

func TestDeadSessionGuardFailsClosedForSetupAndEvidenceFailures(t *testing.T) {
	scenario := terminalScenario("failure-case")
	newRegistry := func() *ScenarioRegistry {
		registry := NewScenarioRegistry()
		if err := registry.Register(scenario); err != nil {
			t.Fatal(err)
		}
		return registry
	}
	tests := []struct {
		name    string
		factory SubjectFactory
		runner  ScenarioRunner
	}{
		{name: "factory-error", factory: func(DeadSessionControl, Scenario) (DeadSessionSubject, error) {
			return nil, errors.New("factory failure")
		}},
		{name: "factory-panic", factory: func(DeadSessionControl, Scenario) (DeadSessionSubject, error) {
			panic("factory panic")
		}},
		{name: "runner-panic", runner: ScenarioRunnerFunc(func(context.Context, Scenario, DeadSessionSubject) (ScenarioRunResult, error) {
			panic("runner panic")
		})},
		{name: "no-evidence", runner: ScenarioRunnerFunc(func(context.Context, Scenario, DeadSessionSubject) (ScenarioRunResult, error) {
			return ScenarioRunResult{}, nil
		})},
		{name: "invalid-evidence", runner: ScenarioRunnerFunc(func(context.Context, Scenario, DeadSessionSubject) (ScenarioRunResult, error) {
			return ScenarioRunResult{ExpectationResults: []ExpectationResult{{Index: 0, Passed: false, Err: errors.New("invalid evidence")}}}, nil
		})},
		{name: "false-without-error", runner: ScenarioRunnerFunc(func(context.Context, Scenario, DeadSessionSubject) (ScenarioRunResult, error) {
			return ScenarioRunResult{Results: []ExpectationResult{{Index: 0, Passed: false}}}, nil
		})},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := DeadSessionGuardConfig{Registry: newRegistry(), SubjectFactory: DefaultDeadSessionSubjectFactory, Runner: test.runner}
			if test.factory != nil {
				config.SubjectFactory = test.factory
			}
			result, err := NewDeadSessionGuard(config).Run(context.Background())
			if err == nil || !errors.Is(err, ErrDeadSessionExecution) {
				t.Fatalf("failure identity: result=%#v err=%v", result, err)
			}
			if result.Runs[0].Status != DeadSessionExecutionFailure {
				t.Fatalf("failure status: %s", result.Runs[0].Status)
			}
		})
	}

	var nilGuard *DeadSessionGuard
	if _, err := nilGuard.Run(context.Background()); !errors.Is(err, ErrDeadSessionExecution) {
		t.Fatalf("nil guard error: %v", err)
	}
	if _, err := NewDeadSessionGuard(123).Run(context.Background()); !errors.Is(err, ErrDeadSessionExecution) {
		t.Fatalf("invalid option error: %v", err)
	}
	if !errors.Is((&DeadSessionGuardError{}), ErrDeadSessionGuard) || (&DeadSessionGuardError{}).Error() != ErrDeadSessionGuard.Error() {
		t.Fatal("empty guard error contract changed")
	}
	var nilError *DeadSessionGuardError
	if nilError.Error() != "<nil>" {
		t.Fatalf("nil error text: %q", nilError.Error())
	}
	if errors.Is(&DeadSessionGuardError{}, errors.New("unrelated")) {
		t.Fatal("unrelated error matched guard error")
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	var nilRunner ScenarioRunner = ScenarioRunnerFunc(nil)
	if _, err := nilRunner.Run(context.Background(), scenario, NewNullSubject()); !errors.Is(err, ErrDeadSessionExecution) {
		t.Fatalf("nil runner error: %v", err)
	}
	if _, err := (ExpectationScenarioRunner{}).Run(canceled, scenario, NewNullSubject()); !errors.Is(err, ErrDeadSessionExecution) {
		t.Fatalf("canceled runner error: %v", err)
	}
}

type observedGuardSubject struct {
	inner   DeadSessionSubject
	control DeadSessionControl
	accepts int
}

func (s *observedGuardSubject) Accept(ctx context.Context, step Step) error {
	s.accepts++
	return s.inner.Accept(ctx, step)
}

func (s *observedGuardSubject) Snapshot(ctx context.Context) (ObservationSnapshot, error) {
	return s.inner.Snapshot(ctx)
}

func terminalScenario(id string) Scenario {
	return Scenario{
		ID:           id,
		Steps:        []Step{{Type: StepClose}},
		Expectations: []ExpectedBehavior{{Type: ExpectTerminalReason, Value: "complete"}},
	}
}
