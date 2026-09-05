package probe

// This file owns the opt-in customer-simulation process harness. The harness
// deliberately composes the shipped session CLI, the existing duplex runner,
// and the versioned evidence bundle; it does not add a second session runtime.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultCustomerSimulationMaxDuration is the per-child deadguard. It is
	// intentionally no larger than the declared Family A/B/D deadline.
	DefaultCustomerSimulationMaxDuration = 30 * time.Second
	DefaultCustomerSimulationSilence     = 500 * time.Millisecond
	DefaultCustomerSimulationFrame       = DefaultDuplexFrameDuration
	DefaultCustomerSimulationShutdown    = DefaultDuplexShutdownGrace
)

var (
	ErrCustomerSimulationSelection = errors.New("invalid customer simulation selection")
	ErrCustomerSimulationAudio     = errors.New("invalid customer simulation audio")
	ErrCustomerSimulationRun       = errors.New("customer simulation run failed")
)

// CustomerSimulationRunSpec binds one declarative scenario to the ordered
// PCM16 turns that will be sent to the shipped process. PCM16 is intentionally
// passed as bytes so file formats and credential handling remain outside the
// process-boundary package.
type CustomerSimulationRunSpec struct {
	Scenario CustomerScenario
	Script   []CustomerScriptTurn
	Audio    [][]byte

	// PatienceRepromptAudio is the separately recorded natural check-in used
	// by Family E after its observable re-prompt threshold. It is deliberately
	// not folded into the action audio so the second utterance remains visible
	// as an incremental input event on the same child process.
	PatienceRepromptAudio []byte
}

// CustomerSimulationSuiteOptions configures one explicitly selected suite.
// APIKey is held only in memory and is passed to the child through the
// provider's supported AGENT_MODEL__... environment variable by DuplexRunner.
type CustomerSimulationSuiteOptions struct {
	BinaryPath string
	RunRoot    string

	Provider     string
	Model        string
	BaseURL      string
	APIKey       string
	SystemPrompt string

	Runs              []CustomerSimulationRunSpec
	Validator         CustomerSimulationValidatorAgent
	ValidatorTimeout  time.Duration
	MaxDuration       time.Duration
	FrameDuration     time.Duration
	SilenceDuration   time.Duration
	ShutdownGrace     time.Duration
	CaptureOutputSink io.Writer
	CaptureErrorSink  io.Writer
}

// CustomerSimulationSuiteResult is safe to marshal as a report. It contains
// scenario identity, timing, dispositions, process cleanup facts, and the
// parsed validator verdict, but never contains raw child output or secrets.
type CustomerSimulationSuiteResult struct {
	Root string                        `json:"root"`
	Runs []CustomerSimulationRunResult `json:"runs"`
}

type CustomerSimulationRunResult struct {
	RunID         string                            `json:"run_id"`
	ScenarioID    string                            `json:"scenario_id"`
	Family        ScenarioFamily                    `json:"family"`
	Termination   TerminationMethod                 `json:"termination"`
	BundleRoot    string                            `json:"bundle_root"`
	RecordRoot    string                            `json:"record_root"`
	WorkspaceRoot string                            `json:"workspace_root"`
	Duration      time.Duration                     `json:"duration"`
	Process       ProcessFacts                      `json:"process"`
	Mechanical    MechanicalVerdict                 `json:"mechanical"`
	Validator     CustomerSimulationValidatorResult `json:"validator"`
	Error         string                            `json:"error,omitempty"`
}

// BuiltInCustomerSimulationScenarios returns the selectable live families in
// stable order. D expands to both termination shapes; --required is defined
// by the CLI as A, B, D-SIGINT, and D-natural.
func BuiltInCustomerSimulationScenarios() []CustomerScenario {
	return []CustomerScenario{
		NewFamilyAScenario(),
		NewFamilyBScenario(),
		NewFamilyCScenario(),
		NewFamilyDScenario(TerminationSIGINT),
		NewFamilyDScenario(TerminationNatural),
		NewFamilyEScenario(),
	}
}

// CustomerSimulationScenarioScript returns the natural-language script for a
// built-in scenario. Custom scenarios use their action intents as a visible
// fallback only when a caller supplies no richer script.
func CustomerSimulationScenarioScript(scenario CustomerScenario) []CustomerScriptTurn {
	var script []CustomerScriptTurn
	switch scenario.ID {
	case FamilyAScenarioID:
		script = FamilyASpokenScript()
	case FamilyBScenarioID:
		script = FamilyBSpokenScript()
	case FamilyCScenarioID:
		script = FamilyCSpokenScript()
	case FamilyDScenarioSIGINTID, FamilyDScenarioNaturalID:
		script = FamilyDSpokenScript()
	case FamilyEScenarioID:
		script = FamilyESpokenScript()
	}
	if len(script) == len(scenario.Actions) {
		return script
	}
	script = make([]CustomerScriptTurn, 0, len(scenario.Actions))
	for _, action := range scenario.Actions {
		text := strings.TrimSpace(action.Description)
		if text == "" {
			text = strings.TrimSpace(action.Intent)
		}
		if text == "" {
			text = action.ID
		}
		script = append(script, CustomerScriptTurn{ActionID: action.ID, Text: text})
	}
	return script
}

// CustomerSimulationScenariosForSelectors resolves built-in family names.
// Selectors are case-insensitive and may be A, B, C, D, D-SIGINT, D-NATURAL,
// or E. Duplicate scenario IDs are rejected so one report cannot silently
// contain fewer runs than the operator selected.
func CustomerSimulationScenariosForSelectors(selectors ...string) ([]CustomerScenario, error) {
	if len(selectors) == 0 {
		return nil, fmt.Errorf("%w: at least one family or scenario selector is required", ErrCustomerSimulationSelection)
	}
	byID := make(map[string]CustomerScenario)
	for _, scenario := range BuiltInCustomerSimulationScenarios() {
		byID[scenario.ID] = scenario
	}
	seen := make(map[string]struct{}, len(selectors))
	result := make([]CustomerScenario, 0, len(selectors))
	appendScenario := func(scenario CustomerScenario) error {
		if err := scenario.Validate(); err != nil {
			return err
		}
		if _, duplicate := seen[scenario.ID]; duplicate {
			return fmt.Errorf("%w: scenario %q was selected more than once", ErrCustomerSimulationSelection, scenario.ID)
		}
		seen[scenario.ID] = struct{}{}
		result = append(result, scenario)
		return nil
	}
	for _, raw := range selectors {
		selector := strings.ToLower(strings.TrimSpace(raw))
		switch selector {
		case "a", "family-a", FamilyAScenarioID:
			if err := appendScenario(byID[FamilyAScenarioID]); err != nil {
				return nil, err
			}
		case "b", "family-b", FamilyBScenarioID:
			if err := appendScenario(byID[FamilyBScenarioID]); err != nil {
				return nil, err
			}
		case "c", "family-c", FamilyCScenarioID:
			if err := appendScenario(byID[FamilyCScenarioID]); err != nil {
				return nil, err
			}
		case "d", "family-d":
			if err := appendScenario(byID[FamilyDScenarioSIGINTID]); err != nil {
				return nil, err
			}
			if err := appendScenario(byID[FamilyDScenarioNaturalID]); err != nil {
				return nil, err
			}
		case "d-sigint", FamilyDScenarioSIGINTID:
			if err := appendScenario(byID[FamilyDScenarioSIGINTID]); err != nil {
				return nil, err
			}
		case "d-natural", FamilyDScenarioNaturalID:
			if err := appendScenario(byID[FamilyDScenarioNaturalID]); err != nil {
				return nil, err
			}
		case "e", "family-e", FamilyEScenarioID:
			if err := appendScenario(byID[FamilyEScenarioID]); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("%w: unknown family selector %q", ErrCustomerSimulationSelection, raw)
		}
	}
	return result, nil
}

// RunCustomerSimulationSuite runs every selected scenario, retaining a
// finalized evidence bundle even when a child, oracle, or validator fails.
// The returned error is aggregate-only; callers should inspect each run's
// structured verdict for the reviewable diagnosis.
func RunCustomerSimulationSuite(ctx context.Context, options CustomerSimulationSuiteOptions) (CustomerSimulationSuiteResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateCustomerSimulationOptions(options); err != nil {
		return CustomerSimulationSuiteResult{}, err
	}
	root, cleanup, err := customerSimulationRunRoot(options.RunRoot)
	if err != nil {
		return CustomerSimulationSuiteResult{}, err
	}
	defer cleanup()

	result := CustomerSimulationSuiteResult{Root: root, Runs: make([]CustomerSimulationRunResult, 0, len(options.Runs))}
	var failures []error
	for index, spec := range options.Runs {
		runResult, runErr := runCustomerSimulation(ctx, root, index, spec, options)
		result.Runs = append(result.Runs, runResult)
		if runErr != nil {
			failures = append(failures, runErr)
		}
	}
	return result, errors.Join(failures...)
}

func validateCustomerSimulationOptions(options CustomerSimulationSuiteOptions) error {
	if strings.TrimSpace(options.BinaryPath) == "" {
		return fmt.Errorf("%w: binary path is required", ErrCustomerSimulationSelection)
	}
	if len(options.Runs) == 0 {
		return fmt.Errorf("%w: no scenarios selected", ErrCustomerSimulationSelection)
	}
	if strings.TrimSpace(options.Provider) == "" || strings.TrimSpace(options.Model) == "" {
		return fmt.Errorf("%w: provider and model are required", ErrCustomerSimulationSelection)
	}
	if !strings.EqualFold(options.Provider, "openai") && !strings.EqualFold(options.Provider, "grok") {
		return fmt.Errorf("%w: live session provider %q is unsupported; want openai or grok", ErrCustomerSimulationSelection, options.Provider)
	}
	for index, spec := range options.Runs {
		if err := spec.Scenario.Validate(); err != nil {
			return fmt.Errorf("%w: scenario %d: %v", ErrCustomerSimulationSelection, index+1, err)
		}
		script := spec.Script
		if len(script) == 0 {
			script = CustomerSimulationScenarioScript(spec.Scenario)
		}
		if len(script) != len(spec.Scenario.Actions) || len(spec.Audio) != len(script) {
			return fmt.Errorf("%w: scenario %q needs one PCM16 turn per declared action (%d), got script=%d audio=%d", ErrCustomerSimulationAudio, spec.Scenario.ID, len(spec.Scenario.Actions), len(script), len(spec.Audio))
		}
		for audioIndex, audio := range spec.Audio {
			if len(audio) == 0 || len(audio)%2 != 0 {
				return fmt.Errorf("%w: scenario %q turn %d must contain non-empty even-length PCM16", ErrCustomerSimulationAudio, spec.Scenario.ID, audioIndex+1)
			}
			if strings.TrimSpace(script[audioIndex].ActionID) == "" || strings.TrimSpace(script[audioIndex].Text) == "" {
				return fmt.Errorf("%w: scenario %q turn %d needs a visible action ID and customer wording", ErrCustomerSimulationAudio, spec.Scenario.ID, audioIndex+1)
			}
		}
		if spec.Scenario.Family == ScenarioFamilyE {
			if len(spec.PatienceRepromptAudio) == 0 || len(spec.PatienceRepromptAudio)%2 != 0 {
				return fmt.Errorf("%w: Family E scenario %q needs non-empty even-length patience re-prompt PCM16", ErrCustomerSimulationAudio, spec.Scenario.ID)
			}
		} else if len(spec.PatienceRepromptAudio) > 0 {
			return fmt.Errorf("%w: patience re-prompt audio is only valid for Family E scenario %q", ErrCustomerSimulationAudio, spec.Scenario.ID)
		}
	}
	return nil
}

func customerSimulationRunRoot(raw string) (string, func(), error) {
	if strings.TrimSpace(raw) == "" {
		root, err := os.MkdirTemp("", "agent-customer-simulation-")
		if err != nil {
			return "", func() {}, fmt.Errorf("%w: create isolated run root: %v", ErrCustomerSimulationRun, err)
		}
		return root, func() {}, nil
	}
	root, err := filepath.Abs(raw)
	if err != nil {
		return "", func() {}, fmt.Errorf("%w: resolve run root: %v", ErrCustomerSimulationRun, err)
	}
	if info, statErr := os.Lstat(root); statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", func() {}, fmt.Errorf("%w: run root must be a non-symlink directory", ErrCustomerSimulationRun)
		}
	} else if errors.Is(statErr, os.ErrNotExist) {
		if err := os.MkdirAll(root, 0o700); err != nil {
			return "", func() {}, fmt.Errorf("%w: create run root: %v", ErrCustomerSimulationRun, err)
		}
	} else {
		return "", func() {}, fmt.Errorf("%w: inspect run root: %v", ErrCustomerSimulationRun, statErr)
	}
	return root, func() {}, nil
}

func runCustomerSimulation(ctx context.Context, suiteRoot string, index int, spec CustomerSimulationRunSpec, options CustomerSimulationSuiteOptions) (CustomerSimulationRunResult, error) {
	runID := fmt.Sprintf("%s-%03d", customerSimulationSlug(spec.Scenario.ID), index+1)
	runRoot := filepath.Join(suiteRoot, runID)
	workspaceRoot := filepath.Join(runRoot, "workspace")
	recordRoot := filepath.Join(runRoot, "product-record")
	configRoot := filepath.Join(runRoot, "config")
	bundleRoot := filepath.Join(runRoot, "evidence")
	if _, err := os.Lstat(runRoot); err == nil {
		failure := fmt.Errorf("run directory %q already exists; use a fresh --run-root", runRoot)
		return failedCustomerSimulationResult(runID, spec.Scenario, bundleRoot, recordRoot, workspaceRoot, failure), fmt.Errorf("%w %q: %v", ErrCustomerSimulationRun, spec.Scenario.ID, failure)
	} else if !errors.Is(err, os.ErrNotExist) {
		return failedCustomerSimulationResult(runID, spec.Scenario, bundleRoot, recordRoot, workspaceRoot, err), fmt.Errorf("%w %q: inspect run directory: %v", ErrCustomerSimulationRun, spec.Scenario.ID, err)
	}
	for _, path := range []string{workspaceRoot, recordRoot, configRoot, bundleRoot} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return failedCustomerSimulationResult(runID, spec.Scenario, bundleRoot, recordRoot, workspaceRoot, fmt.Errorf("create run directory: %v", err)), fmt.Errorf("%w %q: %v", ErrCustomerSimulationRun, spec.Scenario.ID, err)
		}
	}
	// The shipped CLI reports this safety setting on stdout when its config is
	// absent. Since --audio-out - is the runner's binary PCM boundary, seed the
	// isolated config with the same explicit deny-pattern setting used by the
	// hermetic shipped-process fixtures instead of allowing a warning to look
	// like product audio progress.
	if err := os.WriteFile(filepath.Join(configRoot, "config.yaml"), []byte("tools:\n  exec:\n    enable_deny_patterns: true\n"), 0o600); err != nil {
		failure := fmt.Errorf("write isolated session config: %v", err)
		return failedCustomerSimulationResult(runID, spec.Scenario, bundleRoot, recordRoot, workspaceRoot, failure), fmt.Errorf("%w %q: %v", ErrCustomerSimulationRun, spec.Scenario.ID, failure)
	}
	bundle, bundleErr := NewCustomerEvidenceBundle(bundleRoot, spec.Scenario, runID, options.APIKey)
	if bundleErr != nil {
		return failedCustomerSimulationResult(runID, spec.Scenario, bundleRoot, recordRoot, workspaceRoot, bundleErr), fmt.Errorf("%w %q: create evidence bundle: %v", ErrCustomerSimulationRun, spec.Scenario.ID, bundleErr)
	}
	oracle, oracleErr := NewFilesystemOracle(workspaceRoot)
	if oracleErr != nil {
		return failedCustomerSimulationResult(runID, spec.Scenario, bundleRoot, recordRoot, workspaceRoot, oracleErr), fmt.Errorf("%w %q: create filesystem oracle: %v", ErrCustomerSimulationRun, spec.Scenario.ID, oracleErr)
	}

	script := spec.Script
	if len(script) == 0 {
		script = CustomerSimulationScenarioScript(spec.Scenario)
	}
	started := time.Now()
	var checkpointMu sync.Mutex
	checkpoints := make([]FilesystemCheckpoint, 0, len(spec.Scenario.Actions))
	captureCheckpoint := func(actionIndex int, at time.Duration) error {
		if actionIndex < 0 || actionIndex >= len(spec.Scenario.Actions) {
			return fmt.Errorf("checkpoint action index %d is out of range", actionIndex)
		}
		action := spec.Scenario.Actions[actionIndex]
		checkpointID := fmt.Sprintf("checkpoint-%02d-%s", actionIndex+1, customerSimulationSlug(action.ID))
		checkpoint, err := oracle.CaptureCheckpoint(checkpointID, action.ID, at, action.Oracle.Checkpoints)
		if err != nil {
			return err
		}
		checkpointMu.Lock()
		defer checkpointMu.Unlock()
		for _, existing := range checkpoints {
			if existing.ID == checkpoint.ID {
				return nil
			}
		}
		checkpoints = append(checkpoints, checkpoint)
		return nil
	}

	var patienceController *PatienceController
	patienceOutputIndex := 0
	patienceRepromptOutputIndex := -1
	if spec.Scenario.Family == ScenarioFamilyE {
		var controllerErr error
		patienceController, controllerErr = NewPatienceController(spec.Scenario, FamilyEActionID, FamilyETurnID, RealPatienceClock{})
		if controllerErr != nil {
			return failedCustomerSimulationResult(runID, spec.Scenario, bundleRoot, recordRoot, workspaceRoot, controllerErr), fmt.Errorf("%w %q: create patience controller: %v", ErrCustomerSimulationRun, spec.Scenario.ID, controllerErr)
		}
	}

	segments := make([]DuplexAudioSegment, 0, len(spec.Audio)+1)
	if spec.Scenario.Family == ScenarioFamilyE {
		segments = append(segments, DuplexAudioSegment{
			ID: "customer-request", PCM16: append([]byte(nil), spec.Audio[0]...), SilenceFor: options.SilenceDuration,
		})
		segments = append(segments, DuplexAudioSegment{
			ID: "patience-reprompt-1", PCM16: append([]byte(nil), spec.PatienceRepromptAudio...), SilenceFor: options.SilenceDuration,
			Before: func(ctx context.Context, progress *DuplexProgress) error {
				if err := waitForCustomerSimulationPatienceReprompt(ctx, patienceController, progress, &patienceOutputIndex, &patienceRepromptOutputIndex); err != nil {
					return err
				}
				return nil
			},
		})
	} else {
		segments = make([]DuplexAudioSegment, len(spec.Audio))
		for index, audio := range spec.Audio {
			segment := DuplexAudioSegment{ID: script[index].ActionID, PCM16: append([]byte(nil), audio...), SilenceFor: options.SilenceDuration}
			if index > 0 {
				segment.Before = func(actionIndex int) DuplexSegmentGate {
					return func(_ context.Context, _ *DuplexProgress) error {
						return captureCheckpoint(actionIndex-1, time.Since(started))
					}
				}(index)
			}
			if spec.Scenario.Family == ScenarioFamilyB && index == 1 {
				// The first provider response may be a tool-only continuation with
				// its own audio marker. Require the recorded original response's
				// four-byte PCM marker as well before admitting the correction.
				segment.WaitForOutputBytes = 8
			}
			segments[index] = segment
		}
	}

	terminationBytes := int64(0)
	if spec.Scenario.Termination == TerminationSIGINT {
		terminationBytes = 1
	}
	maxDuration := options.MaxDuration
	if maxDuration <= 0 || (spec.Scenario.Deadline > 0 && spec.Scenario.Deadline < maxDuration) {
		maxDuration = spec.Scenario.Deadline
	}
	duplexResult, processErr := RunDuplexSession(ctx, DuplexSessionConfig{
		BinaryPath:       options.BinaryPath,
		RecordDir:        recordRoot,
		WorkingDirectory: workspaceRoot,
		ConfigDir:        configRoot,
		Provider:         options.Provider,
		Model:            options.Model,
		BaseURL:          options.BaseURL,
		APIKey:           options.APIKey,
		SystemPrompt:     options.SystemPrompt,
		MaxDuration:      maxDuration,
		// Patience and correction gates may need to keep the same provider
		// session open after an otherwise terminal response, so all suite runs
		// explicitly retain the shipped session until the provider closes it.
		AdditionalArgs: []string{"--wait-for-close"},
		OnStart: func(startedAt time.Time) {
			if patienceController == nil {
				return
			}
			patienceController.startedAt = startedAt
			_ = patienceController.StartListening()
		},
		FrameDuration: options.FrameDuration,
		Segments:      segments,
		BeforeInputClose: func(ctx context.Context, progress *DuplexProgress) error {
			if spec.Scenario.Family != ScenarioFamilyE {
				return nil
			}
			return waitForCustomerSimulationPatienceCompletion(ctx, patienceController, progress, &patienceOutputIndex, patienceRepromptOutputIndex)
		},
		Termination:                 spec.Scenario.Termination,
		TerminationAfterOutputBytes: terminationBytes,
		Output:                      options.CaptureOutputSink,
		ErrorOutput:                 options.CaptureErrorSink,
		ShutdownGrace:               options.ShutdownGrace,
	})

	checkpointMu.Lock()
	sortFilesystemCheckpoints(checkpoints)
	checkpointSnapshot := append([]FilesystemCheckpoint(nil), checkpoints...)
	checkpointMu.Unlock()
	if processErr == nil && len(spec.Scenario.Actions) > 0 {
		lastAction := len(spec.Scenario.Actions) - 1
		if err := captureCheckpoint(lastAction, duplexResult.Duration); err != nil {
			processErr = errors.Join(processErr, err)
		}
		checkpointMu.Lock()
		sortFilesystemCheckpoints(checkpoints)
		checkpointSnapshot = append([]FilesystemCheckpoint(nil), checkpoints...)
		checkpointMu.Unlock()
	}

	recordingFacts, recordingErr := readCustomerSimulationRecording(recordRoot, spec.Scenario)
	transcripts := buildCustomerSimulationTranscripts(spec.Scenario, script, duplexResult, recordingFacts)
	audioEvents := customerSimulationAudioEvents(spec.Scenario, duplexResult, options.FrameDuration, recordingFacts)
	toolObservations := recordingFacts.tools
	process := ProcessFactsFromDuplexResult(duplexResult)
	if process.ExitClassification == "" {
		process.ExitClassification = "failed"
	}
	actionResults := customerSimulationActionResults(spec.Scenario, transcripts.Product, checkpointSnapshot, toolObservations, process, recordingFacts)

	bundle.Transcripts = transcripts
	bundle.AudioTurnEvents = audioEvents
	bundle.ToolObservations = toolObservations
	bundle.FilesystemCheckpoints = checkpointSnapshot
	bundle.Process = process
	var patience *PatienceEvidence
	if spec.Scenario.Family == ScenarioFamilyE {
		value := customerSimulationPatienceEvidence(spec.Scenario, transcripts.Product, process, duplexResult, toolObservations, recordingFacts, patienceController)
		patience = &value
	}
	mechanical := customerSimulationMechanicalVerdict(spec.Scenario, actionResults, checkpointSnapshot, toolObservations, transcripts.Product, recordingFacts, process, duplexResult, patience)
	bundle.MechanicalVerdict = &mechanical
	if spec.Scenario.Family == ScenarioFamilyC {
		mixed := customerSimulationMixedModalEvidence(spec.Scenario, transcripts, duplexResult)
		bundle.MixedModal = &mixed
	}
	if spec.Scenario.Family == ScenarioFamilyD {
		termination := customerSimulationTerminationEvidence(spec.Scenario, transcripts.Product, process, duplexResult, recordingFacts)
		bundle.Termination = &termination
	}
	if patience != nil {
		bundle.Patience = patience
	}
	var correction *CorrectionEvidence
	if spec.Scenario.Family == ScenarioFamilyB {
		value := customerSimulationCorrectionEvidence(spec.Scenario, transcripts.Product, process, recordingFacts)
		correction = &value
	}
	if recordErr := addCustomerSimulationProductRecord(bundle, recordRoot); recordErr != nil {
		processErr = errors.Join(processErr, recordErr)
		mechanical.Pass = false
		mechanical.Summary = mechanicalSummary(len(mechanical.Findings)+1, len(spec.Scenario.Actions))
		mechanical.Findings = append(mechanical.Findings, MechanicalFinding{
			Code: "missing_product_record", Message: "the product record directory was unavailable; the run is not independently reviewable",
			EvidenceRefs: []string{"product-record-dir/index.json", "process.json"},
		})
	}
	if err := registerCustomerSimulationEvidenceRefs(bundle, correction); err != nil {
		processErr = errors.Join(processErr, err)
	}

	validatorResult, finalizeErr := bundle.FinalizeWithValidator(ctx, options.Validator, options.ValidatorTimeout)
	if finalizeErr != nil {
		processErr = errors.Join(processErr, finalizeErr)
	}
	if recordingErr != nil {
		processErr = errors.Join(processErr, recordingErr)
	}
	if processErr == nil && !validatorResult.Pass() {
		processErr = fmt.Errorf("validator returned %s", validatorResult.Status)
	}

	runResult := CustomerSimulationRunResult{
		RunID: runID, ScenarioID: spec.Scenario.ID, Family: spec.Scenario.Family, Termination: spec.Scenario.Termination,
		BundleRoot: bundleRoot, RecordRoot: recordRoot, WorkspaceRoot: workspaceRoot,
		Duration: duplexResult.Duration, Process: process, Mechanical: *bundle.MechanicalVerdict, Validator: validatorResult,
	}
	if processErr != nil {
		runResult.Error = customerSimulationSafeError(processErr, options.APIKey)
	}
	return runResult, processErr
}

func failedCustomerSimulationResult(runID string, scenario CustomerScenario, bundleRoot, recordRoot, workspaceRoot string, err error) CustomerSimulationRunResult {
	return CustomerSimulationRunResult{
		RunID: runID, ScenarioID: scenario.ID, Family: scenario.Family, Termination: scenario.Termination,
		BundleRoot: bundleRoot, RecordRoot: recordRoot, WorkspaceRoot: workspaceRoot,
		Process: ProcessFacts{PID: -1, ExitClassification: "failed"}, Error: customerSimulationSafeError(err, ""),
	}
}

func customerSimulationSlug(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	var builder strings.Builder
	for _, r := range raw {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			builder.WriteRune(r)
		} else {
			builder.WriteByte('-')
		}
	}
	slug := strings.Trim(builder.String(), "-_")
	if slug == "" {
		return "scenario"
	}
	return slug
}

func sortFilesystemCheckpoints(checkpoints []FilesystemCheckpoint) {
	// Checkpoints are already captured in causal order. This helper keeps a
	// stable order when the last action is appended after a gate callback.
	for i := 1; i < len(checkpoints); i++ {
		value := checkpoints[i]
		j := i - 1
		for j >= 0 && checkpoints[j].At > value.At {
			checkpoints[j+1] = checkpoints[j]
			j--
		}
		checkpoints[j+1] = value
	}
}

func customerSimulationTurnID(scenario CustomerScenario, index int) string {
	if scenario.Family == ScenarioFamilyD {
		return FamilyDActiveTurnID
	}
	if scenario.Family == ScenarioFamilyE {
		return FamilyETurnID
	}
	return fmt.Sprintf("turn-%d", index+1)
}

func customerSimulationAudioEvents(scenario CustomerScenario, result DuplexRunResult, frameDuration time.Duration, facts customerSimulationRecordingFacts) []AudioTurnEvent {
	if frameDuration <= 0 {
		frameDuration = DefaultDuplexFrameDuration
	}
	turnIndex := make(map[string]int)
	for index, action := range scenario.Actions {
		turnIndex[action.ID] = index
	}
	events := make([]AudioTurnEvent, 0, len(result.Input)+len(result.Output))
	for index, input := range result.Input {
		actionIndex, ok := turnIndex[input.SegmentID]
		if !ok {
			actionIndex = 0
		}
		kind := "speech"
		if input.Silent {
			kind = "silence"
		}
		events = append(events, AudioTurnEvent{
			ID: fmt.Sprintf("input-%06d", index+1), TurnID: customerSimulationTurnID(scenario, actionIndex), Direction: "input", Kind: kind,
			At: input.At, Duration: frameDuration, Bytes: input.Bytes,
		})
	}
	responseRanges := customerSimulationResponseAudioRanges(scenario, facts.responses)
	var previousOutputTotal int64
	for index, output := range result.Output {
		end := output.Total
		if end <= previousOutputTotal || end < int64(output.Bytes) {
			end = previousOutputTotal + int64(output.Bytes)
		}
		start := end - int64(output.Bytes)
		if start < previousOutputTotal {
			start = previousOutputTotal
		}
		previousOutputTotal = end

		parts := customerSimulationOutputPartsForRanges(start, end, responseRanges)
		if len(parts) == 0 {
			if actionIndex, ok := customerSimulationResponseForOutputTimestamp(scenario, facts.responses, output.Timestamp); ok {
				parts = []customerSimulationOutputPart{{TurnID: customerSimulationTurnID(scenario, actionIndex), Bytes: output.Bytes}}
			} else {
				parts = []customerSimulationOutputPart{{TurnID: "unattributed-output", Bytes: output.Bytes, Unattributed: true}}
			}
		}
		for partIndex, part := range parts {
			kind := "product_speech"
			if part.Unattributed {
				kind = "product_speech_unattributed"
			}
			events = append(events, AudioTurnEvent{
				ID: fmt.Sprintf("output-%06d-part-%02d", index+1, partIndex+1), TurnID: part.TurnID, Direction: "output", Kind: kind,
				At: output.At, Duration: customerSimulationPCM16Duration(part.Bytes), Bytes: part.Bytes,
			})
		}
	}
	return events
}

type customerSimulationResponseAudioRange struct {
	ResponseID string
	TurnID     string
	Start      int64
	End        int64
}

type customerSimulationOutputPart struct {
	TurnID       string
	Bytes        int
	Unattributed bool
}

func customerSimulationResponseAudioRanges(scenario CustomerScenario, responses []customerSimulationResponse) []customerSimulationResponseAudioRange {
	ranges := make([]customerSimulationResponseAudioRange, 0, len(responses))
	turnIndices := customerSimulationResponseTurnIndices(scenario, responses)
	var cursor int64
	for index, response := range responses {
		if response.AudioBytes <= 0 {
			continue
		}
		end := cursor + int64(response.AudioBytes)
		actionIndex := 0
		if index < len(turnIndices) {
			actionIndex = turnIndices[index]
		}
		ranges = append(ranges, customerSimulationResponseAudioRange{ResponseID: response.ID, TurnID: customerSimulationTurnID(scenario, actionIndex), Start: cursor, End: end})
		cursor = end
	}
	return ranges
}

// customerSimulationResponseTurnIndices maps raw response boundaries to
// action turns. Realtime tool continuations can be separate responses without
// transcript text; they belong to the next spoken response boundary (or the
// final spoken boundary), rather than shifting every later audio read to a
// new action turn.
func customerSimulationResponseTurnIndices(scenario CustomerScenario, responses []customerSimulationResponse) []int {
	indices := make([]int, len(responses))
	if len(scenario.Actions) == 0 {
		return indices
	}
	textAction := make([]int, len(responses))
	textCount := 0
	for index, response := range responses {
		if strings.TrimSpace(response.Text) != "" {
			textAction[index] = textCount
			textCount++
		}
	}
	for index := range responses {
		if strings.TrimSpace(responses[index].Text) != "" {
			indices[index] = minInt(textAction[index], len(scenario.Actions)-1)
			continue
		}
		nextText := -1
		for candidate := index + 1; candidate < len(responses); candidate++ {
			if strings.TrimSpace(responses[candidate].Text) != "" {
				nextText = candidate
				break
			}
		}
		actionIndex := 0
		if nextText >= 0 {
			actionIndex = textAction[nextText]
		} else if index > 0 {
			actionIndex = textCount - 1
		}
		if actionIndex < 0 {
			actionIndex = 0
		}
		indices[index] = minInt(actionIndex, len(scenario.Actions)-1)
	}
	return indices
}

func customerSimulationOutputPartsForRanges(start, end int64, ranges []customerSimulationResponseAudioRange) []customerSimulationOutputPart {
	if end <= start {
		return nil
	}
	parts := make([]customerSimulationOutputPart, 0, 1)
	for _, response := range ranges {
		overlapStart := maxInt64(start, response.Start)
		overlapEnd := minInt64(end, response.End)
		if overlapEnd <= overlapStart {
			continue
		}
		parts = append(parts, customerSimulationOutputPart{TurnID: response.TurnID, Bytes: int(overlapEnd - overlapStart)})
	}
	return parts
}

func customerSimulationResponseForOutputTimestamp(scenario CustomerScenario, responses []customerSimulationResponse, timestamp time.Time) (int, bool) {
	if timestamp.IsZero() {
		return 0, false
	}
	turnIndices := customerSimulationResponseTurnIndices(scenario, responses)
	for index, response := range responses {
		if response.WallStart.IsZero() {
			continue
		}
		if timestamp.Before(response.WallStart) {
			continue
		}
		if response.WallEnd.IsZero() || !timestamp.After(response.WallEnd) {
			actionIndex := 0
			if index < len(turnIndices) {
				actionIndex = turnIndices[index]
			}
			return actionIndex, true
		}
	}
	return 0, false
}

func customerSimulationPCM16Duration(bytes int) time.Duration {
	if bytes <= 0 {
		return 0
	}
	return time.Duration(bytes) * time.Second / (2 * DefaultDuplexSampleRate)
}

func maxInt64(left, right int64) int64 {
	if right > left {
		return right
	}
	return left
}

func minInt64(left, right int64) int64 {
	if right < left {
		return right
	}
	return left
}

const customerSimulationPatienceWakeInterval = 25 * time.Millisecond

// waitForCustomerSimulationPatienceReprompt keeps the input pump at the
// correction boundary while stdout remains observable. Once the shared
// policy permits a check-in, it records the customer re-prompt and returns so
// the next PCM segment is delivered on the same open stdin pipe.
func waitForCustomerSimulationPatienceReprompt(
	ctx context.Context,
	controller *PatienceController,
	progress *DuplexProgress,
	outputIndex *int,
	repromptOutputIndex *int,
) error {
	if controller == nil || progress == nil || outputIndex == nil || repromptOutputIndex == nil {
		return fmt.Errorf("%w: patience runner is incomplete", ErrCustomerSimulationRun)
	}
	for {
		if err := observeCustomerSimulationOutput(controller, progress, outputIndex); err != nil {
			return err
		}
		if progress.OutputClosed() {
			if err := completeCustomerSimulationPatience(controller); err != nil {
				return err
			}
			// A normal child boundary is a valid end to the one-turn patience
			// conversation. Stop the not-yet-delivered re-prompt segment without
			// misclassifying the intentionally short input script as premature EOF.
			return errDuplexInputComplete
		}
		decision, err := controller.Decision()
		if err != nil {
			return err
		}
		switch decision.Kind {
		case PatienceDecisionReprompt:
			if _, err := controller.Reprompt(FamilyEReprompt(decision.RepromptCount)); err != nil {
				return err
			}
			*repromptOutputIndex = len(progress.OutputEvents())
			return nil
		case PatienceDecisionDeadAir:
			if err := controller.DeclareDeadAir(); err != nil {
				return err
			}
			return fmt.Errorf("family E patience dead air: no observable progress for %s", decision.SinceLastProgress)
		}
		if err := waitForCustomerSimulationPatienceChange(ctx, progress); err != nil {
			// A shipped child may close its provider stream immediately after a
			// finite response. The runner cancels its context while reaping that
			// already-completed child, so take the closed-output boundary as the
			// authoritative terminal signal before classifying the wait as a
			// customer cancellation. This final observation also captures output
			// bytes that raced the stdout pump's EOF notification.
			if progress.OutputClosed() {
				if observeErr := observeCustomerSimulationOutput(controller, progress, outputIndex); observeErr != nil {
					return observeErr
				}
				if completeErr := completeCustomerSimulationPatience(controller); completeErr != nil {
					return completeErr
				}
				return errDuplexInputComplete
			}
			return finishCustomerSimulationPatienceOnContext(controller, ctx, err)
		}
	}
}

// waitForCustomerSimulationPatienceCompletion waits for a terminal stdout
// boundary after the re-prompt. A close without any post-re-prompt product
// output is recorded as cancellation, preventing an earlier response from
// being reused as a false success.
func waitForCustomerSimulationPatienceCompletion(
	ctx context.Context,
	controller *PatienceController,
	progress *DuplexProgress,
	outputIndex *int,
	repromptOutputIndex int,
) error {
	if controller == nil || progress == nil || outputIndex == nil {
		return fmt.Errorf("%w: patience runner is incomplete", ErrCustomerSimulationRun)
	}
	for {
		before := *outputIndex
		if err := observeCustomerSimulationOutput(controller, progress, outputIndex); err != nil {
			return err
		}
		if repromptOutputIndex >= 0 && *outputIndex > repromptOutputIndex {
			// A post-re-prompt product audio boundary is the terminal response
			// signal for Family E's one-response script. Close the owned input
			// stream now so the shipped child can deliver its end-of-turn and
			// provider-close controls; waiting for stdout to close first would
			// deadlock when --wait-for-close is enabled.
			if err := completeCustomerSimulationPatience(controller); err != nil {
				return err
			}
			return errDuplexInputComplete
		}
		if progress.OutputClosed() {
			if repromptOutputIndex >= 0 && *outputIndex <= repromptOutputIndex && before == *outputIndex {
				if err := controller.Cancel(); err != nil {
					return err
				}
				return fmt.Errorf("family E patience ended without post-re-prompt product output")
			}
			return completeCustomerSimulationPatience(controller)
		}
		decision, err := controller.Decision()
		if err != nil {
			return err
		}
		if decision.Kind == PatienceDecisionDeadAir {
			if err := controller.DeclareDeadAir(); err != nil {
				return err
			}
			// Close the input boundary after recording the policy breach. This
			// lets the shipped session flush its product record and terminate at
			// its normal end-of-input boundary; the finalized patience evidence
			// remains BROKEN, so graceful process cleanup cannot hide the dead air.
			return errDuplexInputComplete
		}
		if err := waitForCustomerSimulationPatienceChange(ctx, progress); err != nil {
			if progress.OutputClosed() {
				if observeErr := observeCustomerSimulationOutput(controller, progress, outputIndex); observeErr != nil {
					return observeErr
				}
				if repromptOutputIndex >= 0 && *outputIndex <= repromptOutputIndex {
					if cancelErr := controller.Cancel(); cancelErr != nil {
						return cancelErr
					}
					return fmt.Errorf("family E patience ended without post-re-prompt product output")
				}
				if completeErr := completeCustomerSimulationPatience(controller); completeErr != nil {
					return completeErr
				}
				return errDuplexInputComplete
			}
			return finishCustomerSimulationPatienceOnContext(controller, ctx, err)
		}
	}
}

func observeCustomerSimulationOutput(controller *PatienceController, progress *DuplexProgress, outputIndex *int) error {
	events := progress.OutputEvents()
	if *outputIndex > len(events) {
		*outputIndex = len(events)
	}
	for *outputIndex < len(events) {
		event := events[*outputIndex]
		*outputIndex = *outputIndex + 1
		if event.Bytes <= 0 {
			continue
		}
		if !controller.responseStarted {
			if err := controller.ObserveResponseStart(fmt.Sprintf("stdout read %d crossed the product audio boundary", event.Read)); err != nil {
				return err
			}
		}
		if err := controller.ObserveProductSpeech(0, fmt.Sprintf("stdout read %d carried %d product PCM bytes", event.Read, event.Bytes)); err != nil {
			return err
		}
	}
	return nil
}

func waitForCustomerSimulationPatienceChange(ctx context.Context, progress *DuplexProgress) error {
	waitContext, cancel := context.WithTimeout(ctx, customerSimulationPatienceWakeInterval)
	defer cancel()
	err := progress.WaitForChange(waitContext)
	if errors.Is(err, context.DeadlineExceeded) {
		return nil
	}
	return err
}

func completeCustomerSimulationPatience(controller *PatienceController) error {
	if controller.outcome == "" {
		return controller.Complete()
	}
	return nil
}

func finishCustomerSimulationPatienceOnContext(controller *PatienceController, ctx context.Context, waitErr error) error {
	if ctx.Err() != nil && controller.outcome == "" {
		var terminalErr error
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			terminalErr = controller.Timeout()
		} else {
			terminalErr = controller.Cancel()
		}
		return errors.Join(waitErr, terminalErr)
	}
	return waitErr
}

func buildCustomerSimulationTranscripts(scenario CustomerScenario, script []CustomerScriptTurn, result DuplexRunResult, facts customerSimulationRecordingFacts) PairedTranscripts {
	customer := make([]TranscriptEvent, 0, len(script))
	for index, turn := range script {
		at := time.Duration(0)
		for _, input := range result.Input {
			if input.SegmentID == turn.ActionID {
				at = input.At
				break
			}
		}
		if index > 0 && at < customer[len(customer)-1].At {
			at = customer[len(customer)-1].At
		}
		customer = append(customer, TranscriptEvent{ID: fmt.Sprintf("customer-%02d", index+1), TurnID: customerSimulationTurnID(scenario, index), Speaker: TranscriptCustomer, Text: turn.Text, At: at, Final: true})
	}
	if scenario.Family == ScenarioFamilyE {
		for _, input := range result.Input {
			if input.SegmentID != "patience-reprompt-1" {
				continue
			}
			at := input.At
			if len(customer) > 0 && at < customer[len(customer)-1].At {
				at = customer[len(customer)-1].At
			}
			customer = append(customer, TranscriptEvent{
				ID: "customer-patience-reprompt-1", TurnID: FamilyETurnID, Speaker: TranscriptCustomer,
				Text: FamilyEReprompt(0), At: at, Final: true,
			})
			break
		}
	}

	recordedResponses := make([]customerSimulationResponse, 0, len(facts.responses))
	for _, response := range facts.responses {
		if strings.TrimSpace(response.Text) != "" {
			recordedResponses = append(recordedResponses, response)
		}
	}
	if len(recordedResponses) == 0 {
		// Preserve an audio-only response as an explicit empty transcript. The
		// mechanical confirmation oracle will fail it closed, but the timing and
		// audio evidence remain reviewable instead of disappearing.
		recordedResponses = customerSimulationResponseCandidates(facts.responses)
	}
	product := make([]TranscriptEvent, 0, len(recordedResponses))
	for index, response := range recordedResponses {
		at := response.Start
		if at == 0 && index < len(result.Output) {
			at = result.Output[index].At
		}
		if index > 0 && len(product) > 0 && at < product[len(product)-1].At {
			at = product[len(product)-1].At
		}
		product = append(product, TranscriptEvent{ID: fmt.Sprintf("product-%02d", index+1), TurnID: customerSimulationTurnID(scenario, index), Speaker: TranscriptProduct, Text: response.Text, At: at, Final: response.Complete})
	}
	if len(product) == 0 && len(result.Output) > 0 {
		product = append(product, TranscriptEvent{ID: "product-01", TurnID: customerSimulationTurnID(scenario, 0), Speaker: TranscriptProduct, Text: "", At: result.Output[0].At, Final: false})
	}
	return PairedTranscripts{Customer: customer, Product: product}
}

func customerSimulationActionResults(scenario CustomerScenario, product []TranscriptEvent, checkpoints []FilesystemCheckpoint, tools []ToolObservation, process ProcessFacts, facts customerSimulationRecordingFacts) []ActionResult {
	results := make([]ActionResult, 0, len(scenario.Actions))
	for index, action := range scenario.Actions {
		turnID := customerSimulationTurnID(scenario, index)
		text := transcriptTextForTurn(product, turnID)
		checkpointIDs := make([]string, 0)
		for _, checkpoint := range checkpoints {
			if checkpoint.ActionID == action.ID {
				checkpointIDs = append(checkpointIDs, checkpoint.ID)
			}
		}
		toolIDs := make([]string, 0)
		for _, observation := range tools {
			if observation.ActionID == action.ID {
				toolIDs = append(toolIDs, observation.ID)
			}
		}
		completed := process.ExitClassification == "normal" && process.ChildWaited && process.WaitCount == 1 && process.InputFinished
		if scenario.Family == ScenarioFamilyD && scenario.Termination == TerminationSIGINT {
			completed = false
		}
		if action.PartialSideEffectPolicy != PartialSideEffectsForbid && len(toolIDs) == 0 {
			completed = false
		}
		if action.Oracle.RequireConfirmation {
			for _, required := range action.Oracle.RequiredText {
				if !strings.Contains(strings.ToLower(text), strings.ToLower(required)) {
					completed = false
				}
			}
		}
		if scenario.Family == ScenarioFamilyC && action.ID == FamilyCImageActionID {
			completed = false
		}
		disposition := DispositionFailed
		reason := "the shipped session did not produce a complete, independently evidenced action"
		if completed {
			disposition = DispositionCompleted
			reason = ""
		}
		if scenario.Family == ScenarioFamilyB && action.ID == FamilyBOriginalActionID && facts.cancelObserved && len(checkpointIDs) > 0 {
			disposition = DispositionCancelled
			reason = "the original response was cancelled by the customer's correction after its side effect was observed"
			completed = false
		}
		if scenario.Family == ScenarioFamilyD && scenario.Termination == TerminationSIGINT {
			disposition = DispositionCancelled
			reason = "the active response was interrupted by the selected SIGINT termination"
		}
		confirmed := false
		confirmedAt := time.Duration(0)
		if action.Oracle.RequireConfirmation {
			confirmed = true
			for _, required := range action.Oracle.RequiredText {
				if !strings.Contains(strings.ToLower(text), strings.ToLower(required)) {
					confirmed = false
				}
			}
			if confirmed && index < len(product) {
				confirmedAt = product[index].At
			}
		}
		refs := customerSimulationActionEvidenceRefs(scenario)
		results = append(results, ActionResult{ActionID: action.ID, TurnID: turnID, Confirmed: confirmed, ConfirmedAt: confirmedAt, Disposition: disposition, OutcomeReason: reason, EvidenceRefs: refs, CheckpointIDs: checkpointIDs, ToolObservationIDs: toolIDs})
	}
	return results
}

func customerSimulationActionEvidenceRefs(scenario CustomerScenario) []string {
	refs := []string{"transcripts/customer.jsonl", "transcripts/product.jsonl", "events/audio-turn-events.jsonl", "tool-observations.jsonl", "filesystem-checkpoints.jsonl", "process.json"}
	switch scenario.Family {
	case ScenarioFamilyB:
		refs = append(refs, "events/correction.json")
	case ScenarioFamilyC:
		refs = append(refs, "events/mixed-modal.json")
	case ScenarioFamilyD:
		refs = append(refs, "events/termination.json")
	case ScenarioFamilyE:
		refs = append(refs, FamilyEPatienceEventPath)
	}
	return refs
}

func customerSimulationMechanicalVerdict(scenario CustomerScenario, actions []ActionResult, checkpoints []FilesystemCheckpoint, tools []ToolObservation, product []TranscriptEvent, facts customerSimulationRecordingFacts, process ProcessFacts, result DuplexRunResult, patience *PatienceEvidence) MechanicalVerdict {
	var verdict MechanicalVerdict
	var err error
	switch scenario.Family {
	case ScenarioFamilyB:
		correction := customerSimulationCorrectionEvidence(scenario, product, process, facts)
		verdict, err = EvaluateCustomerSimulationCorrection(scenario, actions, checkpoints, tools, product, correction)
	case ScenarioFamilyC:
		mixed := customerSimulationMixedModalEvidence(scenario, PairedTranscripts{Product: product}, DuplexRunResult{})
		verdict, err = EvaluateCustomerSimulationMixedModal(scenario, actions, checkpoints, tools, product, mixed)
	case ScenarioFamilyD:
		termination := customerSimulationTerminationEvidence(scenario, product, process, DuplexRunResult{}, facts)
		verdict, err = EvaluateCustomerSimulationTermination(scenario, actions, checkpoints, tools, product, termination)
	case ScenarioFamilyE:
		if patience == nil {
			value := customerSimulationPatienceEvidence(scenario, product, process, result, tools, facts, nil)
			patience = &value
		}
		verdict, err = EvaluateCustomerSimulationPatience(scenario, actions, checkpoints, tools, product, *patience)
	default:
		verdict, err = EvaluateCustomerSimulation(scenario, actions, checkpoints, tools, product)
	}
	if err == nil {
		return verdict
	}
	// A malformed or incomplete observation must still leave a typed verdict
	// for FinalizeWithValidator. The normal evaluator has already emitted the
	// more useful finding whenever it could construct one.
	return MechanicalVerdict{
		Pass: false, Summary: fmt.Sprintf("mechanical evaluation failed: %s", customerSimulationSafeError(err, "")),
		ActionResults: actions,
		Findings:      []MechanicalFinding{{Code: "mechanical_evaluation_failed", Message: customerSimulationSafeError(err, ""), EvidenceRefs: []string{"scenario.json", "process.json", "mechanical-verdict.json"}}},
	}
}

func registerCustomerSimulationEvidenceRefs(bundle *CustomerEvidenceBundle, correction *CorrectionEvidence) error {
	if bundle == nil {
		return ErrMissingEvidence
	}
	refs := []struct {
		path string
		kind ArtifactKind
	}{
		{"scenario.json", ArtifactKindScenario},
		{"transcripts/customer.jsonl", ArtifactKindCustomerTranscript},
		{"transcripts/product.jsonl", ArtifactKindProductTranscript},
		{"events/audio-turn-events.jsonl", ArtifactKindAudioTurnEvents},
		{"tool-observations.jsonl", ArtifactKindToolObservations},
		{"filesystem-checkpoints.jsonl", ArtifactKindFilesystemCheckpoints},
		{"process.json", ArtifactKindProcessFacts},
		{"mechanical-verdict.json", ArtifactKindMechanicalVerdict},
		{"validator-input.json", ArtifactKindValidatorInput},
		{"validator-verdict.json", ArtifactKindValidatorVerdict},
	}
	if bundle.Scenario.Family == ScenarioFamilyC {
		refs = append(refs, struct {
			path string
			kind ArtifactKind
		}{"events/mixed-modal.json", ArtifactKindMixedModalEvidence})
	}
	if bundle.Scenario.Family == ScenarioFamilyD {
		refs = append(refs, struct {
			path string
			kind ArtifactKind
		}{"events/termination.json", ArtifactKindTerminationEvidence})
	}
	if bundle.Scenario.Family == ScenarioFamilyE {
		refs = append(refs, struct {
			path string
			kind ArtifactKind
		}{FamilyEPatienceEventPath, ArtifactKindPatienceEvidence})
	}
	evidenceRefs := make([]string, 0, len(refs))
	for _, ref := range refs {
		if err := bundle.RegisterArtifact(ref.path, ref.kind, true); err != nil {
			return err
		}
		evidenceRefs = append(evidenceRefs, ref.path)
	}
	if bundle.Scenario.Family == ScenarioFamilyB {
		if correction == nil {
			value := customerSimulationCorrectionEvidence(bundle.Scenario, bundle.Transcripts.Product, bundle.Process, customerSimulationRecordingFacts{})
			correction = &value
		}
		if err := bundle.AddArtifactBytes("events/correction.json", ArtifactKindCorrectionEvidence, mustCustomerSimulationJSON(correction), true); err != nil {
			return err
		}
		evidenceRefs = append(evidenceRefs, "events/correction.json")
	}
	bundle.ValidatorInput = &ValidatorInput{
		Scenario: bundle.Scenario, CustomerTranscript: append([]TranscriptEvent(nil), bundle.Transcripts.Customer...), ProductTranscript: append([]TranscriptEvent(nil), bundle.Transcripts.Product...),
		AudioTurnEvents: append([]AudioTurnEvent(nil), bundle.AudioTurnEvents...), ToolObservations: append([]ToolObservation(nil), bundle.ToolObservations...), FilesystemCheckpoints: append([]FilesystemCheckpoint(nil), bundle.FilesystemCheckpoints...),
		Process: bundle.Process, Mechanical: *bundle.MechanicalVerdict, EvidenceRefs: evidenceRefs,
	}
	if bundle.MixedModal != nil {
		bundle.ValidatorInput.MixedModal = bundle.MixedModal
	}
	if bundle.Termination != nil {
		bundle.ValidatorInput.Termination = bundle.Termination
	}
	if bundle.Patience != nil {
		bundle.ValidatorInput.Patience = bundle.Patience
	}
	return nil
}

func addCustomerSimulationProductRecord(bundle *CustomerEvidenceBundle, recordRoot string) error {
	if bundle == nil {
		return ErrMissingEvidence
	}
	err := bundle.AddProductRecordDir(recordRoot)
	if err == nil && customerSimulationProductRecordFileCount(bundle) > 0 {
		return nil
	}
	if err == nil {
		err = fmt.Errorf("%w: product record directory contained no files", ErrMissingEvidence)
	}
	// A failed child may not have created a record directory. Preserve a
	// hash-verified, explicit absence marker so the bundle remains readable and
	// the mechanical/validator verdict can report missing product evidence.
	if markerErr := bundle.AddArtifactBytes("product-record-dir/index.json", ArtifactKindProductRecordDir, []byte(`{"source_registered":false,"files":[],"reason":"product record directory was unavailable"}`+"\n"), true); markerErr != nil {
		return errors.Join(err, markerErr)
	}
	return fmt.Errorf("%w: product record directory was unavailable", ErrMissingEvidence)
}

func customerSimulationProductRecordFileCount(bundle *CustomerEvidenceBundle) int {
	if bundle == nil {
		return 0
	}
	count := 0
	for _, artifact := range bundle.Artifacts {
		if artifact.Kind == ArtifactKindProductRecordDir && strings.HasPrefix(artifact.Path, "product-record-dir/") && artifact.Path != "product-record-dir/index.json" && artifact.State == ArtifactStateAvailable {
			count++
		}
	}
	return count
}

func mustCustomerSimulationJSON(value any) []byte {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return []byte(`{"error":"could not encode evidence"}` + "\n")
	}
	return append(data, '\n')
}

func customerSimulationSafeError(err error, secret string) string {
	if err == nil {
		return ""
	}
	detail := strings.TrimSpace(err.Error())
	if secret != "" {
		detail = strings.ReplaceAll(detail, secret, "<redacted>")
	}
	return safeValidatorFailureDetail(errors.New(detail))
}
