package probe

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// DeadSessionControl identifies a deterministic subject that must be rejected
// by a real probe scenario.
type DeadSessionControl string

const (
	ControlNull    DeadSessionControl = "null"
	ControlEcho    DeadSessionControl = "echo"
	ControlSilence DeadSessionControl = "silence"

	// These aliases keep the control vocabulary readable at call sites that
	// describe a subject rather than a guard control.
	NullControl    = ControlNull
	EchoControl    = ControlEcho
	SilenceControl = ControlSilence
)

type NegativeControl = DeadSessionControl

// DeadSessionRunStatus is the classification of one scenario/control pair.
// ExpectedFailure is the only healthy outcome. An execution failure is never
// treated as evidence that the scenario rejected the subject.
type DeadSessionRunStatus string

const (
	DeadSessionExpectedFailure  DeadSessionRunStatus = "expected_failure"
	DeadSessionUnexpectedPass   DeadSessionRunStatus = "unexpected_pass"
	DeadSessionExecutionFailure DeadSessionRunStatus = "execution_failure"

	ExpectedFailure  = DeadSessionExpectedFailure
	UnexpectedPass   = DeadSessionUnexpectedPass
	ExecutionFailure = DeadSessionExecutionFailure
)

type GuardRunOutcome = DeadSessionRunStatus

var (
	ErrDeadSessionGuard            = errors.New("dead-session guard failed")
	ErrDeadSessionUnexpectedPass   = errors.New("dead-session control unexpectedly passed")
	ErrDeadSessionExecution        = errors.New("dead-session guard execution failed")
	ErrDeadSessionExpectation      = errors.New("dead-session scenario expectation failed")
	ErrNoExpectationEvidence       = errors.New("scenario produced no expectation evidence")
	ErrNoRegisteredScenarios       = errors.New("dead-session guard has no registered scenarios")
	ErrInvalidScenarioRegistration = errors.New("invalid probe scenario registration")
)

// DeadSessionSubject is the narrow deterministic subject seam used by the
// normal scenario runner. Accept must consume every supported input step;
// Snapshot returns the subject's observable output without waiting or doing
// external I/O.
type DeadSessionSubject interface {
	Accept(context.Context, Step) error
	Snapshot(context.Context) (ObservationSnapshot, error)
}

// Subject is a short alias for callers that use the guard as a generic
// negative-control harness.
type Subject = DeadSessionSubject

// SubjectFactory creates a fresh subject for one scenario/control pair. The
// guard invokes it for every pair, so a factory must not return shared state.
type SubjectFactory func(DeadSessionControl, Scenario) (DeadSessionSubject, error)

// ScenarioRunResult is the normal runner's observable result. Expectation
// results are kept in declaration order and must include one result per
// expectation for the guard to classify a run as evidence.
type ScenarioRunResult struct {
	Observation        ObservationSnapshot
	ExpectationResults []ExpectationResult
	// Results is retained as a convenient spelling for custom runners. When
	// ExpectationResults is nil, the guard reads Results.
	Results []ExpectationResult
}

// ScenarioRunner executes one scenario against one freshly-created subject.
// A runner error describes harness/execution failure; normal expectation
// mismatches belong in ScenarioRunResult.ExpectationResults.
type ScenarioRunner interface {
	Run(context.Context, Scenario, DeadSessionSubject) (ScenarioRunResult, error)
}

// ScenarioRunnerFunc adapts a function to ScenarioRunner.
type ScenarioRunnerFunc func(context.Context, Scenario, DeadSessionSubject) (ScenarioRunResult, error)

func (f ScenarioRunnerFunc) Run(ctx context.Context, scenario Scenario, subject DeadSessionSubject) (ScenarioRunResult, error) {
	if f == nil {
		return ScenarioRunResult{}, fmt.Errorf("%w: nil scenario runner", ErrDeadSessionExecution)
	}
	return f(ctx, scenario, subject)
}

// ExpectationScenarioRunner is the default runner. It feeds every declared
// step through the subject and then uses the package expectation evaluator.
type ExpectationScenarioRunner struct{}

type DefaultScenarioRunner = ExpectationScenarioRunner

func NewDefaultScenarioRunner() ScenarioRunner { return ExpectationScenarioRunner{} }

func (ExpectationScenarioRunner) Run(ctx context.Context, scenario Scenario, subject DeadSessionSubject) (ScenarioRunResult, error) {
	if subject == nil {
		return ScenarioRunResult{}, fmt.Errorf("%w: nil subject", ErrDeadSessionExecution)
	}
	for index, step := range scenario.Steps {
		if err := contextErr(ctx); err != nil {
			return ScenarioRunResult{}, fmt.Errorf("%w: step %d: %v", ErrDeadSessionExecution, index, err)
		}
		if err := subject.Accept(ctx, step); err != nil {
			return ScenarioRunResult{}, fmt.Errorf("%w: step %d: %v", ErrDeadSessionExecution, index, err)
		}
	}
	if err := contextErr(ctx); err != nil {
		return ScenarioRunResult{}, fmt.Errorf("%w: snapshot: %v", ErrDeadSessionExecution, err)
	}
	observation, err := subject.Snapshot(ctx)
	if err != nil {
		return ScenarioRunResult{}, fmt.Errorf("%w: snapshot: %v", ErrDeadSessionExecution, err)
	}
	return ScenarioRunResult{
		Observation:        observation,
		ExpectationResults: evaluateGuardExpectations(scenario, observation),
	}, nil
}

// RegisteredScenario is a snapshot entry. Controls are copied when the
// entry is registered and when it is returned, so a guard run cannot observe
// later mutations to registry-owned state.
type RegisteredScenario struct {
	Scenario Scenario
	Controls []DeadSessionControl
}

// ScenarioRegistry is the live registry seam used by the guard. Registering a
// scenario with no explicit controls derives echo/silence applicability from
// its typed steps and expectations. Null is always included.
type ScenarioRegistry struct {
	mu        sync.RWMutex
	scenarios map[string]RegisteredScenario
}

func NewScenarioRegistry() *ScenarioRegistry {
	return &ScenarioRegistry{scenarios: make(map[string]RegisteredScenario)}
}

func (r *ScenarioRegistry) Register(scenario Scenario, controls ...DeadSessionControl) error {
	if r == nil {
		return fmt.Errorf("%w: nil registry", ErrInvalidScenarioRegistration)
	}
	if strings.TrimSpace(scenario.ID) == "" {
		scenario.ID = strings.TrimSpace(scenario.Name)
	}
	if scenario.ID == "" {
		return fmt.Errorf("%w: scenario ID is required", ErrInvalidScenarioRegistration)
	}
	resolved, err := resolveControls(scenario, controls)
	if err != nil {
		return err
	}
	entry := RegisteredScenario{Scenario: cloneScenario(scenario), Controls: resolved}
	r.mu.Lock()
	if r.scenarios == nil {
		r.scenarios = make(map[string]RegisteredScenario)
	}
	r.scenarios[scenario.ID] = entry
	r.mu.Unlock()
	return nil
}

func (r *ScenarioRegistry) RegisterScenario(scenario Scenario, controls ...DeadSessionControl) error {
	return r.Register(scenario, controls...)
}

func (r *ScenarioRegistry) Unregister(id string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	delete(r.scenarios, id)
	r.mu.Unlock()
}

func (r *ScenarioRegistry) Clear() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.scenarios = make(map[string]RegisteredScenario)
	r.mu.Unlock()
}

// Entries returns a stable, detached snapshot for one guard invocation.
func (r *ScenarioRegistry) Entries() []RegisteredScenario {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	entries := make([]RegisteredScenario, 0, len(r.scenarios))
	for _, entry := range r.scenarios {
		entries = append(entries, RegisteredScenario{
			Scenario: cloneScenario(entry.Scenario),
			Controls: append([]DeadSessionControl(nil), entry.Controls...),
		})
	}
	r.mu.RUnlock()
	sort.Slice(entries, func(i, j int) bool {
		return stableScenarioID(entries[i].Scenario) < stableScenarioID(entries[j].Scenario)
	})
	return entries
}

// Snapshot returns only scenarios for callers that do not need control
// metadata. It is still a live snapshot, not a cached registry list.
func (r *ScenarioRegistry) Snapshot() []Scenario {
	entries := r.Entries()
	scenarios := make([]Scenario, len(entries))
	for index, entry := range entries {
		scenarios[index] = entry.Scenario
	}
	return scenarios
}

var liveScenarioRegistry = NewScenarioRegistry()

// LiveRegistry and DefaultScenarioRegistry are aliases to the package's
// ordinary live registry seam. Guard construction captures the pointer, not
// its contents; registration after construction is visible on the next run.
var LiveRegistry = liveScenarioRegistry
var DefaultScenarioRegistry = liveScenarioRegistry

func LiveScenarioRegistry() *ScenarioRegistry { return liveScenarioRegistry }

func RegisterScenario(scenario Scenario, controls ...DeadSessionControl) error {
	return liveScenarioRegistry.Register(scenario, controls...)
}

func UnregisterScenario(id string) { liveScenarioRegistry.Unregister(id) }

func ResetScenarioRegistry() { liveScenarioRegistry.Clear() }

func Scenarios() []Scenario { return liveScenarioRegistry.Snapshot() }

// DeadSessionGuardConfig customizes the registry, runner, or subject factory.
// Nil fields use the package defaults.
type DeadSessionGuardConfig struct {
	Registry       *ScenarioRegistry
	Runner         ScenarioRunner
	SubjectFactory SubjectFactory
}

type DeadSessionGuardOption func(*DeadSessionGuard)

func WithScenarioRegistry(registry *ScenarioRegistry) DeadSessionGuardOption {
	return func(guard *DeadSessionGuard) { guard.registry = registry }
}

func WithScenarioRunner(runner ScenarioRunner) DeadSessionGuardOption {
	return func(guard *DeadSessionGuard) { guard.runner = runner }
}

func WithSubjectFactory(factory SubjectFactory) DeadSessionGuardOption {
	return func(guard *DeadSessionGuard) { guard.subjectFactory = factory }
}

// DeadSessionGuard checks a fresh registry snapshot on each Run call.
type DeadSessionGuard struct {
	registry         *ScenarioRegistry
	runner           ScenarioRunner
	subjectFactory   SubjectFactory
	configurationErr error
}

// NewDeadSessionGuard accepts zero or more options. It also accepts the
// config, registry, runner, and factory values directly to keep the public
// seam useful to small package-level tests without a second constructor.
func NewDeadSessionGuard(args ...any) *DeadSessionGuard {
	guard := &DeadSessionGuard{
		registry:       liveScenarioRegistry,
		runner:         ExpectationScenarioRunner{},
		subjectFactory: DefaultDeadSessionSubjectFactory,
	}
	for _, arg := range args {
		switch value := arg.(type) {
		case DeadSessionGuardOption:
			if value != nil {
				value(guard)
			}
		case DeadSessionGuardConfig:
			guard.applyConfig(value)
		case *DeadSessionGuardConfig:
			if value == nil {
				continue
			}
			guard.applyConfig(*value)
		case *ScenarioRegistry:
			guard.registry = value
		case ScenarioRunner:
			guard.runner = value
		case SubjectFactory:
			guard.subjectFactory = value
		case func(DeadSessionControl, Scenario) (DeadSessionSubject, error):
			guard.subjectFactory = SubjectFactory(value)
		case nil:
			// Nil optional arguments intentionally leave defaults intact.
		default:
			guard.configurationErr = fmt.Errorf("%w: unsupported guard option %T", ErrDeadSessionExecution, arg)
		}
	}
	guard.setDefaults()
	return guard
}

func NewDeadSessionGuardWithConfig(config DeadSessionGuardConfig) *DeadSessionGuard {
	return NewDeadSessionGuard(config)
}

func (g *DeadSessionGuard) applyConfig(config DeadSessionGuardConfig) {
	if config.Registry != nil {
		g.registry = config.Registry
	}
	if config.Runner != nil {
		g.runner = config.Runner
	}
	if config.SubjectFactory != nil {
		g.subjectFactory = config.SubjectFactory
	}
}

func (g *DeadSessionGuard) setDefaults() {
	if g.registry == nil {
		g.registry = liveScenarioRegistry
	}
	if g.runner == nil {
		g.runner = ExpectationScenarioRunner{}
	}
	if g.subjectFactory == nil {
		g.subjectFactory = DefaultDeadSessionSubjectFactory
	}
}

// DeadSessionRun is the explicit state for one scenario/control pair.
type DeadSessionRun struct {
	ScenarioID         string
	ScenarioName       string
	Control            DeadSessionControl
	Status             DeadSessionRunStatus
	Outcome            DeadSessionRunStatus
	Observation        ObservationSnapshot
	ExpectationResults []ExpectationResult
	Results            []ExpectationResult
	Err                error
}

type ControlResult = DeadSessionRun

// DeadSessionFinding is emitted for any run that did not produce a normal
// expectation mismatch, including harness errors and panics.
type DeadSessionFinding struct {
	ScenarioID   string
	ScenarioName string
	Control      DeadSessionControl
	Status       DeadSessionRunStatus
	Outcome      DeadSessionRunStatus
	Err          error
}

// DeadSessionGuardResult contains every attempted pair in deterministic
// scenario/control order and every non-healthy finding.
type DeadSessionGuardResult struct {
	Runs     []DeadSessionRun
	Findings []DeadSessionFinding
}

func (r DeadSessionGuardResult) Passed() bool  { return len(r.Findings) == 0 }
func (r DeadSessionGuardResult) Healthy() bool { return r.Passed() }
func (r DeadSessionGuardResult) RunCount() int { return len(r.Runs) }
func (r DeadSessionGuardResult) ControlResults() []DeadSessionRun {
	return append([]DeadSessionRun(nil), r.Runs...)
}

// DeadSessionGuardError is returned when a guard run has any unexpected pass
// or execution failure. Findings are sorted before the error is built.
type DeadSessionGuardError struct {
	Findings []DeadSessionFinding
}

func (e *DeadSessionGuardError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if len(e.Findings) == 0 {
		return ErrDeadSessionGuard.Error()
	}
	var builder strings.Builder
	builder.WriteString(ErrDeadSessionGuard.Error())
	for _, finding := range e.Findings {
		builder.WriteString("\n- scenario ")
		builder.WriteString(strconvQuote(stableFindingName(finding)))
		builder.WriteString(" control ")
		builder.WriteString(strconvQuote(string(finding.Control)))
		builder.WriteString(": ")
		if finding.Err != nil {
			builder.WriteString(finding.Err.Error())
		} else {
			builder.WriteString("unexpected pass")
		}
	}
	return builder.String()
}

func (e *DeadSessionGuardError) Unwrap() error { return ErrDeadSessionGuard }

func (e *DeadSessionGuardError) Is(target error) bool {
	if target == ErrDeadSessionGuard {
		return true
	}
	if target != ErrDeadSessionUnexpectedPass && target != ErrDeadSessionExecution && target != ErrNoRegisteredScenarios {
		return false
	}
	for _, finding := range e.Findings {
		if errors.Is(finding.Err, target) {
			return true
		}
		if target == ErrDeadSessionUnexpectedPass && finding.Status == DeadSessionUnexpectedPass {
			return true
		}
		if target == ErrDeadSessionExecution && finding.Status == DeadSessionExecutionFailure {
			return true
		}
	}
	return false
}

func (g *DeadSessionGuard) Run(ctx context.Context) (DeadSessionGuardResult, error) {
	var result DeadSessionGuardResult
	if g == nil {
		finding := DeadSessionFinding{ScenarioID: "guard", ScenarioName: "guard", Control: ControlNull, Status: DeadSessionExecutionFailure, Outcome: DeadSessionExecutionFailure, Err: ErrDeadSessionExecution}
		result.Findings = []DeadSessionFinding{finding}
		return result, &DeadSessionGuardError{Findings: result.Findings}
	}
	g.setDefaults()
	if g.configurationErr != nil {
		finding := DeadSessionFinding{ScenarioID: "guard", ScenarioName: "guard", Control: ControlNull, Status: DeadSessionExecutionFailure, Outcome: DeadSessionExecutionFailure, Err: g.configurationErr}
		result.Findings = []DeadSessionFinding{finding}
		return result, &DeadSessionGuardError{Findings: result.Findings}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	entries := g.registry.Entries()
	if len(entries) == 0 {
		finding := DeadSessionFinding{
			ScenarioID: "registry", ScenarioName: "registry", Control: ControlNull,
			Status: DeadSessionExecutionFailure, Outcome: DeadSessionExecutionFailure,
			Err: ErrNoRegisteredScenarios,
		}
		result.Findings = []DeadSessionFinding{finding}
		return result, &DeadSessionGuardError{Findings: result.Findings}
	}
	for _, entry := range entries {
		for _, control := range entry.Controls {
			run := g.runOne(ctx, entry.Scenario, control)
			result.Runs = append(result.Runs, run)
			if run.Status != DeadSessionExpectedFailure {
				result.Findings = append(result.Findings, DeadSessionFinding{
					ScenarioID: run.ScenarioID, ScenarioName: run.ScenarioName,
					Control: run.Control, Status: run.Status, Outcome: run.Outcome, Err: run.Err,
				})
			}
		}
	}
	sort.SliceStable(result.Runs, func(i, j int) bool { return runLess(result.Runs[i], result.Runs[j]) })
	sort.SliceStable(result.Findings, func(i, j int) bool { return findingLess(result.Findings[i], result.Findings[j]) })
	if len(result.Findings) != 0 {
		return result, &DeadSessionGuardError{Findings: append([]DeadSessionFinding(nil), result.Findings...)}
	}
	return result, nil
}

func (g *DeadSessionGuard) Check(ctx context.Context) error {
	_, err := g.Run(ctx)
	return err
}

func (g *DeadSessionGuard) Execute(ctx context.Context) (DeadSessionGuardResult, error) {
	return g.Run(ctx)
}

func RunDeadSessionGuard(ctx context.Context) (DeadSessionGuardResult, error) {
	return NewDeadSessionGuard().Run(ctx)
}

func CheckDeadSessionGuard(ctx context.Context) error {
	return NewDeadSessionGuard().Check(ctx)
}

func (g *DeadSessionGuard) runOne(ctx context.Context, scenario Scenario, control DeadSessionControl) (run DeadSessionRun) {
	run.ScenarioID = stableScenarioID(scenario)
	run.ScenarioName = scenarioDisplayName(scenario)
	run.Control = control
	run.Status = DeadSessionExecutionFailure
	run.Outcome = DeadSessionExecutionFailure

	subject, err := callSubjectFactory(g.subjectFactory, control, scenario)
	if err != nil {
		run.Err = wrapExecutionError(run.ScenarioID, control, err)
		return run
	}
	if subject == nil {
		run.Err = wrapExecutionError(run.ScenarioID, control, fmt.Errorf("nil subject"))
		return run
	}
	runResult, err := callScenarioRunner(g.runner, ctx, scenario, subject)
	run.Observation = runResult.Observation
	run.ExpectationResults = append([]ExpectationResult(nil), runResult.expectations()...)
	run.Results = append([]ExpectationResult(nil), run.ExpectationResults...)
	status, classificationErr := classifyScenarioRun(scenario, runResult, err)
	run.Status = status
	run.Outcome = status
	if classificationErr != nil {
		run.Err = classificationErr
	}
	return run
}

func callSubjectFactory(factory SubjectFactory, control DeadSessionControl, scenario Scenario) (subject DeadSessionSubject, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("%w: subject factory panic: %v", ErrDeadSessionExecution, recovered)
		}
	}()
	return factory(control, scenario)
}

func callScenarioRunner(runner ScenarioRunner, ctx context.Context, scenario Scenario, subject DeadSessionSubject) (result ScenarioRunResult, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("%w: scenario runner panic: %v", ErrDeadSessionExecution, recovered)
		}
	}()
	return runner.Run(ctx, scenario, subject)
}

func classifyScenarioRun(scenario Scenario, run ScenarioRunResult, runErr error) (DeadSessionRunStatus, error) {
	if runErr != nil {
		if errors.Is(runErr, ErrExpectationMismatch) || errors.Is(runErr, ErrDeadSessionExpectation) {
			return DeadSessionExpectedFailure, nil
		}
		return DeadSessionExecutionFailure, wrapExecutionError(stableScenarioID(scenario), "runner", runErr)
	}
	expectations := run.expectations()
	if len(expectations) == 0 {
		return DeadSessionExecutionFailure, fmt.Errorf("%w: scenario %q", ErrNoExpectationEvidence, stableScenarioID(scenario))
	}
	hasMismatch := false
	for _, expectation := range expectations {
		if expectation.Err != nil {
			if errors.Is(expectation.Err, ErrExpectationMismatch) || errors.Is(expectation.Err, ErrDeadSessionExpectation) {
				hasMismatch = true
				continue
			}
			return DeadSessionExecutionFailure, fmt.Errorf("%w: scenario %q expectation %d: %v", ErrDeadSessionExecution, stableScenarioID(scenario), expectation.Index, expectation.Err)
		}
		if !expectation.Passed {
			return DeadSessionExecutionFailure, fmt.Errorf("%w: scenario %q expectation %d returned false without an error", ErrDeadSessionExecution, stableScenarioID(scenario), expectation.Index)
		}
	}
	if hasMismatch {
		return DeadSessionExpectedFailure, nil
	}
	return DeadSessionUnexpectedPass, nil
}

func (r ScenarioRunResult) expectations() []ExpectationResult {
	if r.ExpectationResults != nil {
		return r.ExpectationResults
	}
	return r.Results
}

func wrapExecutionError(scenarioID string, control any, err error) error {
	return fmt.Errorf("%w: scenario %q control %q: %v", ErrDeadSessionExecution, scenarioID, control, err)
}

// DefaultDeadSessionSubjectFactory creates one isolated deterministic
// subject. It is intentionally free of clocks, providers, devices, and files.
func DefaultDeadSessionSubjectFactory(control DeadSessionControl, _ Scenario) (DeadSessionSubject, error) {
	switch control {
	case ControlNull, ControlEcho, ControlSilence:
		return &deterministicDeadSessionSubject{control: control}, nil
	default:
		return nil, fmt.Errorf("unknown dead-session control %q", control)
	}
}

func NewNullSubject() DeadSessionSubject {
	subject, _ := DefaultDeadSessionSubjectFactory(ControlNull, Scenario{})
	return subject
}

func NewEchoSubject() DeadSessionSubject {
	subject, _ := DefaultDeadSessionSubjectFactory(ControlEcho, Scenario{})
	return subject
}

func NewSilenceSubject() DeadSessionSubject {
	subject, _ := DefaultDeadSessionSubjectFactory(ControlSilence, Scenario{})
	return subject
}

type deterministicDeadSessionSubject struct {
	control    DeadSessionControl
	transcript strings.Builder
}

func (s *deterministicDeadSessionSubject) Accept(ctx context.Context, step Step) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	if s.control == ControlEcho && guardStepKind(step) == StepSendText {
		s.transcript.WriteString(step.Text)
	}
	return nil
}

func (s *deterministicDeadSessionSubject) Snapshot(ctx context.Context) (ObservationSnapshot, error) {
	if err := contextErr(ctx); err != nil {
		return ObservationSnapshot{}, err
	}
	observation := ObservationSnapshot{}
	if s.control == ControlEcho {
		observation.Transcript = s.transcript.String()
	}
	if s.control == ControlSilence {
		// A silent control is structurally valid output with zero energy. The
		// frame makes frame-count-only scenarios observable as weak controls.
		observation.PCM16Samples = []int16{0}
		observation.FrameCount = 1
	}
	return observation, nil
}

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

func resolveControls(scenario Scenario, requested []DeadSessionControl) ([]DeadSessionControl, error) {
	controls := []DeadSessionControl{ControlNull}
	if requested == nil {
		if scenarioSupportsEcho(scenario) {
			controls = append(controls, ControlEcho)
		}
		if scenarioSupportsSilence(scenario) {
			controls = append(controls, ControlSilence)
		}
		return controls, nil
	}
	controls = append(controls, requested...)
	seen := make(map[DeadSessionControl]bool, len(controls))
	resolved := make([]DeadSessionControl, 0, len(controls))
	for _, control := range controls {
		if control != ControlNull && control != ControlEcho && control != ControlSilence {
			return nil, fmt.Errorf("%w: unknown control %q", ErrInvalidScenarioRegistration, control)
		}
		if !seen[control] {
			seen[control] = true
			resolved = append(resolved, control)
		}
	}
	sort.SliceStable(resolved, func(i, j int) bool { return controlRank(resolved[i]) < controlRank(resolved[j]) })
	return resolved, nil
}

func scenarioSupportsEcho(scenario Scenario) bool {
	hasTextInput := false
	for _, step := range scenario.Steps {
		if guardStepKind(step) == StepSendText {
			hasTextInput = true
			break
		}
	}
	if !hasTextInput {
		return false
	}
	for _, expectation := range scenario.expectedValues() {
		switch declaredKind(expectation) {
		case ExpectText, ExpectContains, ExpectTranscript, ExpectTranscriptContains:
			return true
		}
	}
	return false
}

func scenarioSupportsSilence(scenario Scenario) bool {
	hasAudioInput := false
	for _, step := range scenario.Steps {
		if guardStepKind(step) == StepSendAudio {
			hasAudioInput = true
			break
		}
	}
	for _, expectation := range scenario.expectedValues() {
		switch declaredKind(expectation) {
		case ExpectAudioEnergy, ExpectAudio:
			return true
		case ExpectFrameCount:
			if hasAudioInput {
				return true
			}
		}
	}
	return false
}
func evaluateGuardExpectations(scenario Scenario, observation ObservationSnapshot) []ExpectationResult {
	expectations := scenario.expectedValues()
	results := make([]ExpectationResult, len(expectations))
	for index, expectation := range expectations {
		err := evaluateGuardExpectation(expectation, observation)
		results[index] = ExpectationResult{
			Index: index, Kind: declaredKind(expectation), Expectation: expectation,
			Passed: err == nil, Err: err,
		}
	}
	return results
}

func evaluateGuardExpectation(expectation ExpectedBehavior, observation ObservationSnapshot) error {
	switch declaredKind(expectation) {
	case ExpectAudioEnergy, ExpectTranscriptContains, ExpectToolCalled,
		ExpectLatencyWithinTicks, ExpectTerminalReason, ExpectFrameCount,
		ExpectToolResultDelivered, ExpectToolResultDiscarded, ExpectNoOrphanedToolResult,
		ExpectBufferDisposition, ExpectMetricsReconcile,
		ExpectBargeInCancelOnce, ExpectMessageCountsReconcile:
		return Evaluate(expectation, observation)
	case ExpectText, ExpectTranscript:
		want, err := aliasString(expectation, declaredKind(expectation), "text", expectation.Text, expectation.Value)
		if err != nil {
			return err
		}
		if observation.Transcript != want {
			return mismatch(expectation, declaredKind(expectation), want, observation.Transcript)
		}
	case ExpectContains:
		want, err := aliasString(expectation, declaredKind(expectation), "text", expectation.Text, expectation.Value)
		if err != nil {
			return err
		}
		if !strings.Contains(observation.Transcript, want) {
			return mismatch(expectation, declaredKind(expectation), want, observation.Transcript)
		}
	case ExpectAudio:
		if pcm16RMS(observation.PCM16Samples) <= AudioEnergyThreshold {
			return mismatch(expectation, declaredKind(expectation), "non-silent audio", pcm16RMS(observation.PCM16Samples))
		}
	case ExpectToolCall:
		want := expectation.ToolCallID
		if want == "" {
			want = expectation.ToolName
		}
		if want == "" {
			want = expectation.Value
		}
		if want == "" {
			return invalid(expectation, declaredKind(expectation), "tool_call_id", "expected tool identity must not be empty")
		}
		for _, call := range observation.ToolCalls {
			if call == want {
				return nil
			}
		}
		return mismatch(expectation, declaredKind(expectation), want, observation.ToolCalls)
	case ExpectToolResult:
		return mismatch(expectation, declaredKind(expectation), "tool result", nil)
	case ExpectClose:
		if observation.TerminalReason == "" {
			return mismatch(expectation, declaredKind(expectation), "terminal event", "no terminal event")
		}
	case ExpectTime:
		want := expectation.At
		if expectation.HasAt || expectation.At != 0 {
			if !observation.HasObservedTick && observation.ObservedTick == 0 {
				return mismatch(expectation, declaredKind(expectation), want, "missing observed tick")
			}
			if observation.ObservedTick != want {
				return mismatch(expectation, declaredKind(expectation), want, observation.ObservedTick)
			}
			return nil
		}
		return invalid(expectation, declaredKind(expectation), "at", "expected logical time is required")
	case ExpectEvent:
		return mismatch(expectation, declaredKind(expectation), expectation.Value, "no event")
	default:
		return invalid(expectation, declaredKind(expectation), "type", "unknown measurable expectation")
	}
	return nil
}

func controlRank(control DeadSessionControl) int {
	switch control {
	case ControlNull:
		return 0
	case ControlEcho:
		return 1
	case ControlSilence:
		return 2
	default:
		return 3
	}
}

func stableScenarioID(scenario Scenario) string {
	if strings.TrimSpace(scenario.ID) != "" {
		return scenario.ID
	}
	return scenario.Name
}

func scenarioDisplayName(scenario Scenario) string {
	if strings.TrimSpace(scenario.Name) != "" {
		return scenario.Name
	}
	return stableScenarioID(scenario)
}

func stableFindingName(finding DeadSessionFinding) string {
	if finding.ScenarioID != "" {
		return finding.ScenarioID
	}
	return finding.ScenarioName
}

func runLess(left, right DeadSessionRun) bool {
	if left.ScenarioID != right.ScenarioID {
		return left.ScenarioID < right.ScenarioID
	}
	if controlRank(left.Control) != controlRank(right.Control) {
		return controlRank(left.Control) < controlRank(right.Control)
	}
	return string(left.Control) < string(right.Control)
}

func findingLess(left, right DeadSessionFinding) bool {
	if left.ScenarioID != right.ScenarioID {
		return left.ScenarioID < right.ScenarioID
	}
	if controlRank(left.Control) != controlRank(right.Control) {
		return controlRank(left.Control) < controlRank(right.Control)
	}
	return string(left.Control) < string(right.Control)
}

func guardStepKind(step Step) StepKind {
	if step.Type != "" {
		return step.Type
	}
	return step.Kind
}

func strconvQuote(value string) string {
	return fmt.Sprintf("%q", value)
}

func cloneScenario(scenario Scenario) Scenario {
	clone := scenario
	clone.Steps = append([]Step(nil), scenario.Steps...)
	for index := range clone.Steps {
		clone.Steps[index].ToolResult = append([]byte(nil), scenario.Steps[index].ToolResult...)
		clone.Steps[index].Result = append([]byte(nil), scenario.Steps[index].Result...)
	}
	clone.Expectations = cloneExpectations(scenario.Expectations)
	clone.Expected = cloneExpectations(scenario.Expected)
	clone.ExpectedBehavior = cloneExpectations(scenario.ExpectedBehavior)
	return clone
}

func cloneExpectations(expectations []ExpectedBehavior) []ExpectedBehavior {
	clone := append([]ExpectedBehavior(nil), expectations...)
	for index := range clone {
		clone[index].Result = append([]byte(nil), clone[index].Result...)
	}
	return clone
}
