package probe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

func runCustomerSimulation(ctx context.Context, suiteRoot string, index int, spec CustomerSimulationRunSpec, options CustomerSimulationSuiteOptions) (CustomerSimulationRunResult, error) {
	run := newCustomerSimulationRun(suiteRoot, index, spec, options)
	if result, err := run.prepare(); err != nil {
		return result, err
	}
	duplexResult, processErr := RunDuplexSession(ctx, run.duplexConfig())

	checkpointSnapshot := run.checkpointSnapshot()
	if processErr == nil && len(spec.Scenario.Actions) > 0 {
		lastAction := len(spec.Scenario.Actions) - 1
		if err := run.captureCheckpoint(lastAction, duplexResult.Duration); err != nil {
			processErr = errors.Join(processErr, err)
		}
		checkpointSnapshot = run.checkpointSnapshot()
	}

	recordingFacts, recordingErr := readCustomerSimulationRecording(run.recordRoot, spec.Scenario, options.ReplayService)
	correction, evidenceErr := run.populateEvidence(duplexResult, checkpointSnapshot, recordingFacts)
	if evidenceErr != nil {
		processErr = errors.Join(processErr, evidenceErr)
	}
	if err := registerCustomerSimulationEvidenceRefs(run.bundle, correction); err != nil {
		processErr = errors.Join(processErr, err)
	}

	validatorResult, finalizeErr := run.bundle.FinalizeWithValidator(ctx, options.Validator, options.ValidatorTimeout)
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
		RunID: run.runID, ScenarioID: spec.Scenario.ID, Family: spec.Scenario.Family, Termination: spec.Scenario.Termination,
		BundleRoot: run.bundleRoot, RecordRoot: run.recordRoot, WorkspaceRoot: run.workspaceRoot,
		Duration: duplexResult.Duration, Process: run.bundle.Process, Mechanical: *run.bundle.MechanicalVerdict, Validator: validatorResult,
	}
	if processErr != nil {
		runResult.Error = customerSimulationSafeError(processErr, options.APIKey)
	}
	return runResult, processErr
}

// Private permissions for the isolated run directories and seeded config.
const (
	customerSimulationDirMode  = 0o700
	customerSimulationFileMode = 0o600
)

// customerSimulationRun is the per-run state shared by the duplex gates,
// checkpoint capture, and evidence assembly.
type customerSimulationRun struct {
	spec          CustomerSimulationRunSpec
	options       CustomerSimulationSuiteOptions
	runID         string
	runRoot       string
	workspaceRoot string
	recordRoot    string
	configRoot    string
	bundleRoot    string
	script        []CustomerScriptTurn
	bundle        *CustomerEvidenceBundle
	oracle        *FilesystemOracle
	started       time.Time

	checkpointMu sync.Mutex
	checkpoints  []FilesystemCheckpoint

	patienceController          *PatienceController
	patienceOutputIndex         int
	patienceRepromptOutputIndex int
}

func newCustomerSimulationRun(suiteRoot string, index int, spec CustomerSimulationRunSpec, options CustomerSimulationSuiteOptions) *customerSimulationRun {
	runID := fmt.Sprintf("%s-%03d", customerSimulationSlug(spec.Scenario.ID), index+1)
	runRoot := filepath.Join(suiteRoot, runID)
	script := spec.Script
	if len(script) == 0 {
		script = CustomerSimulationScenarioScript(spec.Scenario)
	}
	return &customerSimulationRun{
		spec:                        spec,
		options:                     options,
		runID:                       runID,
		runRoot:                     runRoot,
		workspaceRoot:               filepath.Join(runRoot, "workspace"),
		recordRoot:                  filepath.Join(runRoot, "product-record"),
		configRoot:                  filepath.Join(runRoot, "config"),
		bundleRoot:                  filepath.Join(runRoot, "evidence"),
		script:                      script,
		checkpoints:                 make([]FilesystemCheckpoint, 0, len(spec.Scenario.Actions)),
		patienceRepromptOutputIndex: -1,
	}
}

// fail returns the retained failed result for a setup error together with
// the typed run error.
func (r *customerSimulationRun) fail(failure error, format string, cause error) (CustomerSimulationRunResult, error) {
	return failedCustomerSimulationResult(r.runID, r.spec.Scenario, r.bundleRoot, r.recordRoot, r.workspaceRoot, failure), fmt.Errorf("%w %q: "+format, ErrCustomerSimulationRun, r.spec.Scenario.ID, cause)
}

func (r *customerSimulationRun) prepare() (CustomerSimulationRunResult, error) {
	if _, err := os.Lstat(r.runRoot); err == nil {
		failure := fmt.Errorf("run directory %q already exists; use a fresh --run-root", r.runRoot)
		return r.fail(failure, "%v", failure)
	} else if !errors.Is(err, os.ErrNotExist) {
		return r.fail(err, "inspect run directory: %v", err)
	}
	for _, path := range []string{r.workspaceRoot, r.recordRoot, r.configRoot, r.bundleRoot} {
		if err := os.MkdirAll(path, customerSimulationDirMode); err != nil {
			return r.fail(fmt.Errorf("create run directory: %w", err), "%v", err)
		}
	}
	// The shipped CLI reports this safety setting on stdout when its config is
	// absent. Since --audio-out - is the runner's binary PCM boundary, seed the
	// isolated config with the same explicit deny-pattern setting used by the
	// hermetic shipped-process fixtures instead of allowing a warning to look
	// like product audio progress.
	if err := os.WriteFile(filepath.Join(r.configRoot, "config.yaml"), []byte("tools:\n  exec:\n    enable_deny_patterns: true\n"), customerSimulationFileMode); err != nil {
		failure := fmt.Errorf("write isolated session config: %w", err)
		return r.fail(failure, "%v", failure)
	}
	bundle, bundleErr := NewCustomerEvidenceBundle(r.bundleRoot, r.spec.Scenario, r.runID, r.options.APIKey)
	if bundleErr != nil {
		return r.fail(bundleErr, "create evidence bundle: %v", bundleErr)
	}
	r.bundle = bundle
	oracle, oracleErr := NewFilesystemOracle(r.workspaceRoot)
	if oracleErr != nil {
		return r.fail(oracleErr, "create filesystem oracle: %v", oracleErr)
	}
	r.oracle = oracle
	r.started = time.Now()
	if r.spec.Scenario.Family == ScenarioFamilyE {
		controller, controllerErr := NewPatienceController(r.spec.Scenario, FamilyEActionID, FamilyETurnID, RealPatienceClock{})
		if controllerErr != nil {
			return r.fail(controllerErr, "create patience controller: %v", controllerErr)
		}
		r.patienceController = controller
	}
	return CustomerSimulationRunResult{}, nil
}

func (r *customerSimulationRun) captureCheckpoint(actionIndex int, at time.Duration) error {
	if actionIndex < 0 || actionIndex >= len(r.spec.Scenario.Actions) {
		return fmt.Errorf("checkpoint action index %d is out of range", actionIndex)
	}
	action := r.spec.Scenario.Actions[actionIndex]
	checkpointID := fmt.Sprintf("checkpoint-%02d-%s", actionIndex+1, customerSimulationSlug(action.ID))
	checkpoint, err := r.oracle.CaptureCheckpoint(checkpointID, action.ID, at, action.Oracle.Checkpoints)
	if err != nil {
		return err
	}
	r.checkpointMu.Lock()
	defer r.checkpointMu.Unlock()
	for _, existing := range r.checkpoints {
		if existing.ID == checkpoint.ID {
			return nil
		}
	}
	r.checkpoints = append(r.checkpoints, checkpoint)
	return nil
}

func (r *customerSimulationRun) checkpointSnapshot() []FilesystemCheckpoint {
	r.checkpointMu.Lock()
	defer r.checkpointMu.Unlock()
	sortFilesystemCheckpoints(r.checkpoints)
	return append([]FilesystemCheckpoint(nil), r.checkpoints...)
}

func (r *customerSimulationRun) segments() []DuplexAudioSegment {
	spec, silence := r.spec, r.options.SilenceDuration
	if spec.Scenario.Family == ScenarioFamilyE {
		return []DuplexAudioSegment{
			{ID: "customer-request", PCM16: append([]byte(nil), spec.Audio[0]...), SilenceFor: silence},
			{
				ID: "patience-reprompt-1", PCM16: append([]byte(nil), spec.PatienceRepromptAudio...), SilenceFor: silence,
				Before: func(ctx context.Context, progress *DuplexProgress) error {
					return waitForCustomerSimulationPatienceReprompt(ctx, r.patienceController, progress, &r.patienceOutputIndex, &r.patienceRepromptOutputIndex)
				},
			},
		}
	}
	segments := make([]DuplexAudioSegment, len(spec.Audio))
	for index, audio := range spec.Audio {
		segment := DuplexAudioSegment{ID: r.script[index].ActionID, PCM16: append([]byte(nil), audio...), SilenceFor: silence}
		if index > 0 {
			actionIndex := index - 1
			segment.Before = func(_ context.Context, _ *DuplexProgress) error {
				return r.captureCheckpoint(actionIndex, time.Since(r.started))
			}
		}
		if spec.Scenario.Family == ScenarioFamilyB && index == 1 {
			// A tool-only continuation can be delivered or suppressed separately.
			// Gate on the original response's marker itself.
			segment.WaitForOutputSequence = []byte{1, 0x08, 0x52, 0x08}
		}
		segments[index] = segment
	}
	return segments
}

func (r *customerSimulationRun) duplexConfig() DuplexSessionConfig {
	scenario, options := r.spec.Scenario, r.options
	terminationBytes := int64(0)
	if scenario.Termination == TerminationSIGINT {
		terminationBytes = 1
	}
	maxDuration := options.MaxDuration
	if maxDuration <= 0 || (scenario.Deadline > 0 && scenario.Deadline < maxDuration) {
		maxDuration = scenario.Deadline
	}
	return DuplexSessionConfig{
		BinaryPath:       options.BinaryPath,
		RecordDir:        r.recordRoot,
		WorkingDirectory: r.workspaceRoot,
		ConfigDir:        r.configRoot,
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
		OnStart:        r.startPatienceListening,
		FrameDuration:  options.FrameDuration,
		Segments:       r.segments(),
		BeforeInputClose: func(ctx context.Context, progress *DuplexProgress) error {
			if scenario.Family != ScenarioFamilyE {
				return nil
			}
			return waitForCustomerSimulationPatienceCompletion(ctx, r.patienceController, progress, &r.patienceOutputIndex, r.patienceRepromptOutputIndex)
		},
		Termination:                 scenario.Termination,
		TerminationAfterOutputBytes: terminationBytes,
		Output:                      options.CaptureOutputSink,
		ErrorOutput:                 options.CaptureErrorSink,
		ShutdownGrace:               options.ShutdownGrace,
	}
}

// startPatienceListening anchors the patience clock at child start. A
// listening failure is recorded by the controller's own ledger checks, so the
// start hook itself has no error to report.
func (r *customerSimulationRun) startPatienceListening(startedAt time.Time) {
	if r.patienceController == nil {
		return
	}
	r.patienceController.startedAt = startedAt
	if err := r.patienceController.StartListening(); err != nil {
		return
	}
}

// populateEvidence fills the bundle from the finished run and returns the
// Family B correction ledger, if any, plus a product-record copy failure.
func (r *customerSimulationRun) populateEvidence(duplexResult DuplexRunResult, checkpoints []FilesystemCheckpoint, facts customerSimulationRecordingFacts) (*CorrectionEvidence, error) {
	scenario, bundle := r.spec.Scenario, r.bundle
	transcripts := buildCustomerSimulationTranscripts(scenario, r.script, duplexResult, facts)
	toolObservations := facts.tools
	process := ProcessFactsFromDuplexResult(duplexResult)
	if process.ExitClassification == "" {
		process.ExitClassification = duplexExitFailed
	}
	actionResults := customerSimulationActionResults(scenario, transcripts.Product, checkpoints, toolObservations, process, facts)

	bundle.Transcripts = transcripts
	bundle.AudioTurnEvents = customerSimulationAudioEvents(scenario, duplexResult, r.options.FrameDuration, facts)
	bundle.ToolObservations = toolObservations
	bundle.FilesystemCheckpoints = checkpoints
	bundle.Process = process
	var patience *PatienceEvidence
	if scenario.Family == ScenarioFamilyE {
		value := customerSimulationPatienceEvidence(scenario, transcripts.Product, process, duplexResult, toolObservations, facts, r.patienceController)
		patience = &value
	}
	mechanical := customerSimulationMechanicalVerdict(scenario, actionResults, checkpoints, toolObservations, transcripts.Product, facts, process, duplexResult, patience)
	bundle.MechanicalVerdict = &mechanical
	if scenario.Family == ScenarioFamilyC {
		mixed := customerSimulationMixedModalEvidence(scenario, transcripts, duplexResult)
		bundle.MixedModal = &mixed
	}
	if scenario.Family == ScenarioFamilyD {
		termination := customerSimulationTerminationEvidence(scenario, transcripts.Product, process, duplexResult, facts)
		bundle.Termination = &termination
	}
	if patience != nil {
		bundle.Patience = patience
	}
	var correction *CorrectionEvidence
	if scenario.Family == ScenarioFamilyB {
		value := customerSimulationCorrectionEvidence(scenario, transcripts.Product, process, facts)
		correction = &value
	}
	recordErr := addCustomerSimulationProductRecord(bundle, r.recordRoot)
	if recordErr != nil {
		mechanical.Pass = false
		mechanical.Summary = mechanicalSummary(len(mechanical.Findings)+1, len(scenario.Actions))
		mechanical.Findings = append(mechanical.Findings, MechanicalFinding{
			Code: "missing_product_record", Message: "the product record directory was unavailable; the run is not independently reviewable",
			EvidenceRefs: []string{"product-record-dir/index.json", "process.json"},
		})
	}
	return correction, recordErr
}

func failedCustomerSimulationResult(runID string, scenario CustomerScenario, bundleRoot, recordRoot, workspaceRoot string, err error) CustomerSimulationRunResult {
	return CustomerSimulationRunResult{
		RunID: runID, ScenarioID: scenario.ID, Family: scenario.Family, Termination: scenario.Termination,
		BundleRoot: bundleRoot, RecordRoot: recordRoot, WorkspaceRoot: workspaceRoot,
		Process: ProcessFacts{PID: -1, ExitClassification: duplexExitFailed}, Error: customerSimulationSafeError(err, ""),
	}
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

func customerSimulationCorrectionEvidence(scenario CustomerScenario, product []TranscriptEvent, process ProcessFacts, facts customerSimulationRecordingFacts) CorrectionEvidence {
	original := customerSimulationRecordedResponse(facts, 0)
	replacement := customerSimulationRecordedResponse(facts, 1)
	originalStart, originalEnd := customerSimulationResponseOutputBoundaries(original)
	replacementStart, replacementEnd := customerSimulationResponseOutputBoundaries(replacement)
	// All three boundaries come from the copied agent transcript's logical
	// clock: response output audio, the first non-silent correction frame, and
	// the actual provider-boundary RESPONSE.CANCEL. Do not substitute a parent
	// process PCM read, a next response, or a terminal marker for any of them.
	correctionAt := customerSimulationRecordedInputStart(facts, 1)
	cancelAt := time.Duration(0)
	if facts.cancelObserved {
		cancelAt = facts.cancelAt
	}

	originalStatus := customerSimulationResponseStatus(original)
	if originalStatus == customerSimulationResponseIncomplete && facts.cancelObserved && facts.cancelResponseID == original.ID {
		originalStatus = string(DispositionCancelled)
	}
	replacementStatus := customerSimulationResponseStatus(replacement)
	originalResponseID := original.ID
	if originalResponseID == "" && len(product) > 0 {
		// Keep a visible placeholder for the malformed/missing-record case. The
		// contract still rejects an empty ID, and the evaluator reports the
		// action-specific failure instead of fabricating a passing interval.
		originalResponseID = "unobserved-original-response"
	}
	return CorrectionEvidence{
		OriginalActionID: FamilyBOriginalActionID, ReplacementActionID: FamilyBReplacementActionID,
		OriginalTurnID: customerSimulationTurnID(scenario, 0), CorrectionTurnID: customerSimulationTurnID(scenario, 1), OriginalResponseID: originalResponseID,
		OriginalResponseStartedAt: originalStart, CorrectionStartedAt: correctionAt, CancellationSentAt: cancelAt, OriginalResponseEndedAt: originalEnd,
		ReplacementResponseStartedAt: replacementStart, ReplacementResponseEndedAt: replacementEnd,
		CancellationEventRecorded: facts.cancelObserved, CancellationResponseID: facts.cancelResponseID,
		OriginalResponseStatus: originalStatus, ReplacementResponseStatus: replacementStatus, Process: &process,
	}
}
