package probe

// This file owns the opt-in customer-simulation process harness. The harness
// deliberately composes the shipped session CLI, the existing duplex runner,
// and the versioned evidence bundle; it does not add a second session runtime.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
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

	segments := make([]DuplexAudioSegment, len(spec.Audio))
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
			segment.WaitForOutputBytes = 1
		}
		segments[index] = segment
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
		BinaryPath:                  options.BinaryPath,
		RecordDir:                   recordRoot,
		WorkingDirectory:            workspaceRoot,
		ConfigDir:                   configRoot,
		Provider:                    options.Provider,
		Model:                       options.Model,
		BaseURL:                     options.BaseURL,
		APIKey:                      options.APIKey,
		SystemPrompt:                options.SystemPrompt,
		MaxDuration:                 maxDuration,
		FrameDuration:               options.FrameDuration,
		Segments:                    segments,
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
	audioEvents := customerSimulationAudioEvents(spec.Scenario, duplexResult, options.FrameDuration)
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
	mechanical := customerSimulationMechanicalVerdict(spec.Scenario, actionResults, checkpointSnapshot, toolObservations, transcripts.Product, recordingFacts, process)
	bundle.MechanicalVerdict = &mechanical
	if spec.Scenario.Family == ScenarioFamilyC {
		mixed := customerSimulationMixedModalEvidence(spec.Scenario, transcripts, duplexResult)
		bundle.MixedModal = &mixed
	}
	if spec.Scenario.Family == ScenarioFamilyD {
		termination := customerSimulationTerminationEvidence(spec.Scenario, transcripts.Product, process, duplexResult, recordingFacts)
		bundle.Termination = &termination
	}
	if spec.Scenario.Family == ScenarioFamilyE {
		patience := customerSimulationPatienceEvidence(spec.Scenario, transcripts.Product, process, duplexResult, toolObservations)
		bundle.Patience = &patience
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

func customerSimulationAudioEvents(scenario CustomerScenario, result DuplexRunResult, frameDuration time.Duration) []AudioTurnEvent {
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
	for index, output := range result.Output {
		turnIndexValue := index
		if turnIndexValue >= len(scenario.Actions) {
			turnIndexValue = len(scenario.Actions) - 1
		}
		if turnIndexValue < 0 {
			turnIndexValue = 0
		}
		events = append(events, AudioTurnEvent{
			ID: fmt.Sprintf("output-%06d", index+1), TurnID: customerSimulationTurnID(scenario, turnIndexValue), Direction: "output", Kind: "product_speech",
			At: output.At, Duration: 0, Bytes: output.Bytes,
		})
	}
	return events
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

	product := make([]TranscriptEvent, 0, len(facts.responses))
	for index, response := range facts.responses {
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

func customerSimulationMechanicalVerdict(scenario CustomerScenario, actions []ActionResult, checkpoints []FilesystemCheckpoint, tools []ToolObservation, product []TranscriptEvent, facts customerSimulationRecordingFacts, process ProcessFacts) MechanicalVerdict {
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
		patience := customerSimulationPatienceEvidence(scenario, product, process, DuplexRunResult{}, tools)
		verdict, err = EvaluateCustomerSimulationPatience(scenario, actions, checkpoints, tools, product, patience)
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

// customerSimulationRecordingFacts are derived only from the copied product
// record directory. They intentionally omit tool arguments and raw payloads.
type customerSimulationRecordingFacts struct {
	responses      []customerSimulationResponse
	tools          []ToolObservation
	cancelObserved bool
	cancelAt       time.Duration
}

type customerSimulationResponse struct {
	Text      string
	Start     time.Duration
	End       time.Duration
	Complete  bool
	Cancelled bool
}

type customerSimulationTool struct {
	ID       string
	Name     string
	Start    time.Duration
	End      time.Duration
	ResultAt time.Duration
	Result   bool
}

type customerSimulationSessionLogEntry struct {
	TurnIndex int `json:"turn_index"`
	Response  struct {
		Text     string `json:"text"`
		Complete bool   `json:"complete"`
	} `json:"response"`
}

func readCustomerSimulationRecording(recordRoot string, scenario CustomerScenario) (customerSimulationRecordingFacts, error) {
	var facts customerSimulationRecordingFacts
	var failures []error
	if data, err := os.ReadFile(filepath.Join(recordRoot, "session-log.jsonl")); err == nil {
		scanner := bufio.NewScanner(bytes.NewReader(data))
		for scanner.Scan() {
			var entry customerSimulationSessionLogEntry
			if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
				failures = append(failures, fmt.Errorf("decode session-log entry: %v", err))
				continue
			}
			facts.responses = append(facts.responses, customerSimulationResponse{Text: entry.Response.Text, Complete: entry.Response.Complete})
		}
		if err := scanner.Err(); err != nil {
			failures = append(failures, fmt.Errorf("read session-log: %v", err))
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		failures = append(failures, fmt.Errorf("read session-log: %v", err))
	}

	streamFacts, err := readCustomerSimulationStream(recordRoot, scenario, len(facts.responses))
	if err != nil {
		failures = append(failures, err)
	}
	if len(facts.responses) == 0 {
		facts.responses = streamFacts.responses
	} else {
		for index := range facts.responses {
			if index < len(streamFacts.responses) {
				if strings.TrimSpace(facts.responses[index].Text) == "" {
					facts.responses[index].Text = streamFacts.responses[index].Text
				}
				facts.responses[index].Start = streamFacts.responses[index].Start
				facts.responses[index].End = streamFacts.responses[index].End
				facts.responses[index].Cancelled = streamFacts.responses[index].Cancelled
			}
		}
	}
	facts.tools = streamFacts.tools
	facts.cancelObserved = streamFacts.cancelObserved
	facts.cancelAt = streamFacts.cancelAt
	return facts, errors.Join(failures...)
}

func readCustomerSimulationStream(recordRoot string, scenario CustomerScenario, knownResponses int) (customerSimulationRecordingFacts, error) {
	var facts customerSimulationRecordingFacts
	path := filepath.Join(recordRoot, "agent.transcript.jsonl")
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return facts, nil
		}
		return facts, fmt.Errorf("open product transcript: %v", err)
	}
	defer file.Close()
	type recordedMessage struct {
		message messages.StreamMessage
		at      time.Duration
	}
	var records []recordedMessage
	var base time.Time
	completedToolIDs := make(map[string]time.Duration)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 4<<20)
	for scanner.Scan() {
		record, decodeErr := transcript.Decode(scanner.Bytes())
		if decodeErr != nil {
			return facts, fmt.Errorf("decode product transcript: %v", decodeErr)
		}
		if record.Peer != transcript.PeerAgent {
			continue
		}
		if base.IsZero() {
			base, _ = time.Parse(time.RFC3339Nano, record.Timestamp)
		}
		at := time.Duration(0)
		if timestamp, parseErr := time.Parse(time.RFC3339Nano, record.Timestamp); parseErr == nil && !base.IsZero() && timestamp.After(base) {
			at = timestamp.Sub(base)
		}
		message, messageErr := gatewaytesting.UnmarshalStreamMessage(record.Payload)
		if messageErr != nil {
			// SYSTEM.FULL_MESSAGE was added after the generic test helper's
			// original switch. It is decoded below just for tool correlation;
			// unknown auxiliary frames do not erase the rest of the recording.
			if recordContainsStreamType(record.Payload, string(messages.StreamTypeSystemFullMessage)) {
				if toolID, ok := fullMessageToolID(record.Payload); ok {
					completedToolIDs[toolID] = at
				}
				continue
			}
			return facts, fmt.Errorf("decode product stream message: %v", messageErr)
		}
		records = append(records, recordedMessage{message: message, at: at})
	}
	if err := scanner.Err(); err != nil {
		return facts, fmt.Errorf("read product transcript: %v", err)
	}

	var current *customerSimulationResponse
	var text strings.Builder
	pending := make(map[string]*customerSimulationTool)
	responseIndex := 0
	flush := func(at time.Duration, complete, cancelled bool) {
		if current == nil {
			return
		}
		current.Text = text.String()
		current.End = at
		current.Complete = complete
		current.Cancelled = cancelled
		facts.responses = append(facts.responses, *current)
		current = nil
		text.Reset()
	}
	for _, record := range records {
		msg := record.message
		isAssistant := msg.Role == messages.RoleAssistant || msg.ActorID == messages.Model
		switch msg.Type {
		case messages.StreamTypeMessageStart:
			if isAssistant && current == nil {
				current = &customerSimulationResponse{Start: record.at}
				responseIndex++
			}
		case messages.StreamTypeTextDelta:
			if isAssistant {
				if current == nil {
					current = &customerSimulationResponse{Start: record.at}
				}
				if value, ok := msg.Value.(*messages.TextDeltaValue); ok && value != nil {
					text.WriteString(value.Content)
				}
			}
		case messages.StreamTypeTranscriptEnd:
			if isAssistant {
				if current == nil {
					current = &customerSimulationResponse{Start: record.at}
				}
				if value, ok := msg.Value.(*messages.TranscriptEndValue); ok && value != nil && text.Len() == 0 {
					text.WriteString(value.FullText)
				}
			}
		case messages.StreamTypeAudioDelta:
			if isAssistant && current == nil {
				current = &customerSimulationResponse{Start: record.at}
			}
		case messages.StreamTypeToolCallEnd:
			if isAssistant {
				if value, ok := msg.Value.(*messages.ToolCallEndValue); ok && value != nil && strings.TrimSpace(value.ToolCallID) != "" {
					pending[value.ToolCallID] = &customerSimulationTool{ID: value.ToolCallID, Name: value.Name, Start: record.at}
				}
			} else if value, ok := msg.Value.(*messages.ToolCallEndValue); ok && value != nil && strings.TrimSpace(value.ToolCallID) != "" {
				completedToolIDs[value.ToolCallID] = record.at
			}
		case messages.StreamTypeResponseCancel:
			facts.cancelObserved = true
			facts.cancelAt = record.at
			if current != nil {
				current.Cancelled = true
			}
		case messages.StreamTypeMessageEnd:
			if isAssistant {
				flush(record.at, true, current != nil && current.Cancelled)
			}
		}
		if responseIndex > len(scenario.Actions)+knownResponses+1 {
			break
		}
	}
	if current != nil {
		flush(current.Start, false, current.Cancelled)
	}
	toolIDs := make([]string, 0, len(pending))
	for id := range pending {
		toolIDs = append(toolIDs, id)
	}
	sort.Strings(toolIDs)
	for _, toolID := range toolIDs {
		tool := pending[toolID]
		actionIndex := len(facts.tools)
		if actionIndex >= len(scenario.Actions) {
			actionIndex = len(scenario.Actions) - 1
		}
		if actionIndex < 0 {
			actionIndex = 0
		}
		turnID := customerSimulationTurnID(scenario, actionIndex)
		resultAt, resultSeen := completedToolIDs[tool.ID]
		status := "started"
		duration := maxDuration(0, resultAt-tool.Start)
		if resultSeen {
			status = "completed"
		}
		facts.tools = append(facts.tools, ToolObservation{ID: tool.ID, ActionID: scenario.Actions[actionIndex].ID, TurnID: turnID, Tool: customerSimulationSlug(tool.Name), Status: status, At: tool.Start, Duration: duration, ResultSeen: resultSeen})
	}
	for index := range facts.responses {
		if facts.responses[index].End < facts.responses[index].Start {
			facts.responses[index].End = facts.responses[index].Start
		}
	}
	return facts, nil
}

func recordContainsStreamType(payload []byte, want string) bool {
	var envelope struct {
		Type string `json:"type"`
	}
	return json.Unmarshal(payload, &envelope) == nil && envelope.Type == want
}

func fullMessageToolID(payload []byte) (string, bool) {
	var envelope struct {
		ToolCallID string `json:"tool_call_id"`
		Value      struct {
			Message struct {
				ToolCallID string `json:"ToolCallID"`
			} `json:"message"`
		} `json:"value"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return "", false
	}
	if envelope.ToolCallID != "" {
		return envelope.ToolCallID, true
	}
	return envelope.Value.Message.ToolCallID, envelope.Value.Message.ToolCallID != ""
}

func maxDuration(left, right time.Duration) time.Duration {
	if right > left {
		return right
	}
	return left
}

func customerSimulationCorrectionEvidence(scenario CustomerScenario, product []TranscriptEvent, process ProcessFacts, facts customerSimulationRecordingFacts) CorrectionEvidence {
	originalStart, originalEnd := customerSimulationResponseInterval(product, 0)
	replacementStart, replacementEnd := customerSimulationResponseInterval(product, 1)
	correctionAt := time.Duration(0)
	if len(product) > 1 {
		correctionAt = product[1].At
	}
	if correctionAt <= originalStart {
		correctionAt = originalStart + time.Millisecond
	}
	cancelAt := facts.cancelAt
	if !facts.cancelObserved || cancelAt < originalStart || cancelAt >= correctionAt {
		cancelAt = correctionAt - time.Nanosecond
		if cancelAt < originalStart {
			cancelAt = originalStart
		}
	}
	if originalEnd < originalStart || originalEnd == 0 {
		originalEnd = cancelAt
	}
	if replacementStart <= correctionAt {
		replacementStart = correctionAt + time.Nanosecond
	}
	if replacementEnd < replacementStart {
		replacementEnd = replacementStart
	}
	originalStatus := "incomplete"
	if facts.cancelObserved {
		originalStatus = "cancelled"
	}
	replacementStatus := "incomplete"
	if len(product) > 1 && product[1].Final {
		replacementStatus = "completed"
	}
	return CorrectionEvidence{
		OriginalActionID: FamilyBOriginalActionID, ReplacementActionID: FamilyBReplacementActionID,
		OriginalTurnID: customerSimulationTurnID(scenario, 0), CorrectionTurnID: customerSimulationTurnID(scenario, 1), OriginalResponseID: "response-family-b-original",
		OriginalResponseStartedAt: originalStart, CorrectionStartedAt: correctionAt, CancellationSentAt: cancelAt, OriginalResponseEndedAt: originalEnd,
		ReplacementResponseStartedAt: replacementStart, ReplacementResponseEndedAt: replacementEnd,
		OriginalResponseStatus: originalStatus, ReplacementResponseStatus: replacementStatus, Process: &process,
	}
}

func customerSimulationResponseInterval(product []TranscriptEvent, index int) (time.Duration, time.Duration) {
	if index < 0 || index >= len(product) {
		return 0, 0
	}
	start := product[index].At
	end := start + time.Millisecond
	if index+1 < len(product) && product[index+1].At > end {
		end = product[index+1].At
	}
	return start, end
}

func customerSimulationMixedModalEvidence(scenario CustomerScenario, transcripts PairedTranscripts, result DuplexRunResult) MixedModalEvidence {
	priorAt := time.Duration(0)
	if len(transcripts.Product) > 1 {
		priorAt = transcripts.Product[1].At
	}
	customerAt := priorAt + time.Millisecond
	return MixedModalEvidence{
		ImageEventID: FamilyCImageEventID, PriorActionID: FamilyCTextActionID, PriorTurnID: customerSimulationTurnID(scenario, 1), ImageTurnID: customerSimulationTurnID(scenario, 2),
		PriorActionCompletedAt: priorAt, CustomerTurnStartedAt: customerAt, ImageObserved: false, ExpectedSHA256: FamilyCImageFixtureSHA256,
		Delivery: MixedModalDeliveryUnsupported, Supported: false, ImageMeaningInCustomerSpeech: false, ProductGapCode: FamilyCMidSessionImageGapCode, ProductGap: FamilyCMidSessionImageGap,
		EvidenceRefs: []string{"events/mixed-modal.json", "transcripts/product.jsonl", "process.json"},
	}
}

func customerSimulationTerminationEvidence(scenario CustomerScenario, product []TranscriptEvent, process ProcessFacts, result DuplexRunResult, facts customerSimulationRecordingFacts) TerminationEvidence {
	start, end := customerSimulationResponseInterval(product, 0)
	if start == 0 && len(result.Output) > 0 {
		start = result.Output[0].At
	}
	status := "incomplete"
	if scenario.Termination == TerminationSIGINT {
		if process.SignalSent {
			status = "interrupted"
			if facts.cancelObserved {
				status = "cancelled"
			}
		}
	} else if len(product) > 0 && product[0].Final {
		status = "completed"
	}
	if status != "incomplete" {
		if end <= start {
			end = start + time.Millisecond
		}
		if process.SignalSent && process.SignalAt > end {
			end = process.SignalAt
		}
	}
	satisfaction := status == "completed" && scenario.Termination == TerminationNatural
	satisfactionAt := time.Duration(0)
	if satisfaction {
		satisfactionAt = end + time.Nanosecond
	}
	return TerminationEvidence{
		Method: scenario.Termination, ActiveActionID: FamilyDActionID, ActiveTurnID: FamilyDActiveTurnID, ActiveResponseID: FamilyDActiveResponseID,
		ActiveResponseStatus: status, ActiveResponseStartedAt: start, ActiveResponseEndedAt: end, SatisfactionDeclared: satisfaction, SatisfactionAt: satisfactionAt,
		SignalSent: process.SignalSent, Signal: process.Signal, SignalAt: process.SignalAt, Process: process,
		OutstandingToolIDs: factsOutstandingToolIDs(facts), EvidenceRefs: FamilyDTerminationEvidenceRefs(),
	}
}

func factsOutstandingToolIDs(facts customerSimulationRecordingFacts) []string {
	var result []string
	for _, tool := range facts.tools {
		if tool.Status != "completed" || !tool.ResultSeen {
			result = append(result, tool.ID)
		}
	}
	return result
}

func customerSimulationPatienceEvidence(scenario CustomerScenario, product []TranscriptEvent, process ProcessFacts, result DuplexRunResult, tools []ToolObservation) PatienceEvidence {
	turnID := FamilyETurnID
	terminal := process.EndedAt
	if terminal <= 0 {
		terminal = time.Millisecond
	}
	responseStart := time.Duration(0)
	if len(product) > 0 {
		responseStart = product[0].At
	}
	if responseStart == 0 && len(result.Output) > 0 {
		responseStart = result.Output[0].At
	}
	if responseStart > terminal {
		terminal = responseStart + time.Millisecond
	}
	events := []PatienceEvent{{ID: "listen-started", TurnID: turnID, Kind: PatienceEventListenStarted, At: 0}}
	outcome := PatienceOutcomeDeadAir
	state := PatienceActivityDeadAir
	deadAirAt := terminal
	deadAirDuration := terminal
	firstProgress := time.Duration(0)
	lastProgress := time.Duration(0)
	if responseStart > 0 || len(product) > 0 {
		outcome = PatienceOutcomeCompleted
		state = PatienceActivityCompleted
		if responseStart == 0 {
			responseStart = time.Nanosecond
		}
		events = append(events, PatienceEvent{ID: "response-started", TurnID: turnID, Kind: PatienceEventResponseStarted, At: responseStart})
		progressDuration := terminal - responseStart
		if progressDuration <= 0 {
			progressDuration = time.Nanosecond
			terminal = responseStart + progressDuration
		}
		events = append(events, PatienceEvent{ID: "product-speech", TurnID: turnID, Kind: PatienceEventProductSpeech, At: responseStart, Duration: progressDuration})
		firstProgress = responseStart
		lastProgress = responseStart + progressDuration
		events = append(events, PatienceEvent{ID: "response-completed", TurnID: turnID, Kind: PatienceEventResponseCompleted, At: terminal})
		deadAirAt = 0
		deadAirDuration = 0
	}
	if outcome == PatienceOutcomeDeadAir {
		events = append(events, PatienceEvent{ID: "dead-air", TurnID: turnID, Kind: PatienceEventDeadAir, At: terminal, Duration: deadAirDuration, Detail: "no product speech was observed before the process ended"})
	}
	return PatienceEvidence{
		ActionID: FamilyEActionID, TurnID: turnID, ListenStartedAt: 0, ResponseStartedAt: responseStart, FirstProgressAt: firstProgress, LastProgressAt: lastProgress,
		TerminalAt: terminal, Outcome: outcome, ActivityState: state, Events: events, DeadAirAt: deadAirAt, DeadAirDuration: deadAirDuration, Process: process,
		OutstandingToolIDs: toolObservationIDsNotComplete(tools), CustomerImpact: "The customer could not rely on a timely, observable response.", EvidenceRefs: FamilyEPatienceEvidenceRefs(),
	}
}

func toolObservationIDsNotComplete(tools []ToolObservation) []string {
	var result []string
	for _, tool := range tools {
		if tool.Status != "completed" || !tool.ResultSeen {
			result = append(result, tool.ID)
		}
	}
	return result
}
