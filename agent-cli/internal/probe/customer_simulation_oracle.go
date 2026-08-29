package probe

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

var (
	ErrFilesystemOracleInvalidRoot = errors.New("filesystem oracle root is invalid")
	ErrFilesystemOracleMismatch    = errors.New("filesystem oracle expectation mismatch")
	ErrFilesystemOracleObservation = errors.New("filesystem oracle observation failed")
)

// FilesystemOracle reads only the declared sandbox. It never mutates the
// sandbox and captures the exact facts that a scenario declared, including
// explicit absence facts. A checkpoint is intended to be taken at the
// confirmation boundary for one action; callers should retain every returned
// checkpoint instead of replacing an earlier one with a later snapshot.
type FilesystemOracle struct {
	root string
}

// NewFilesystemOracle creates an oracle rooted at an existing, non-symlink
// directory. Requiring the directory up front prevents a typo from silently
// turning an action checkpoint into an empty temporary tree.
func NewFilesystemOracle(root string) (*FilesystemOracle, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("%w: root is empty", ErrFilesystemOracleInvalidRoot)
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("%w: resolve root: %v", ErrFilesystemOracleInvalidRoot, err)
	}
	info, err := os.Lstat(absRoot)
	if err != nil {
		return nil, fmt.Errorf("%w: inspect root: %v", ErrFilesystemOracleInvalidRoot, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, fmt.Errorf("%w: root %q is not a non-symlink directory", ErrFilesystemOracleInvalidRoot, absRoot)
	}
	return &FilesystemOracle{root: absRoot}, nil
}

// Root returns the absolute sandbox path owned by the oracle.
func (o *FilesystemOracle) Root() string {
	if o == nil {
		return ""
	}
	return o.root
}

// CaptureCheckpoint records the declared expectations as observed at one
// point in the run. It returns the checkpoint even when an observation fails,
// allowing the caller to preserve partial evidence for a BROKEN verdict.
func (o *FilesystemOracle) CaptureCheckpoint(id, actionID string, at time.Duration, expectations []FilesystemExpectation) (FilesystemCheckpoint, error) {
	checkpoint := FilesystemCheckpoint{ID: id, ActionID: actionID, At: at, Entries: []FilesystemCheckpointEntry{}}
	if o == nil || o.root == "" {
		return checkpoint, fmt.Errorf("%w: oracle is not configured", ErrFilesystemOracleInvalidRoot)
	}
	if strings.TrimSpace(id) == "" || strings.TrimSpace(actionID) == "" {
		return checkpoint, fmt.Errorf("%w: checkpoint ID and action ID are required", ErrFilesystemOracleObservation)
	}
	if at < 0 {
		return checkpoint, fmt.Errorf("%w: checkpoint time must not be negative", ErrFilesystemOracleObservation)
	}
	if len(expectations) == 0 {
		return checkpoint, fmt.Errorf("%w: checkpoint %q has no declared expectations", ErrFilesystemOracleObservation, id)
	}

	seen := make(map[string]struct{}, len(expectations))
	var observationErrors []error
	for index, expectation := range expectations {
		field := fmt.Sprintf("expectations[%d]", index)
		if err := expectation.validate(field); err != nil {
			observationErrors = append(observationErrors, err)
			continue
		}
		if _, exists := seen[expectation.Path]; exists {
			observationErrors = append(observationErrors, fmt.Errorf("%w: duplicate path %q", ErrFilesystemOracleObservation, expectation.Path))
			continue
		}
		seen[expectation.Path] = struct{}{}

		entry, err := o.observe(expectation.Path)
		if err != nil {
			observationErrors = append(observationErrors, err)
			continue
		}
		checkpoint.Entries = append(checkpoint.Entries, entry)
	}
	sort.Slice(checkpoint.Entries, func(i, j int) bool { return checkpoint.Entries[i].Path < checkpoint.Entries[j].Path })
	if err := checkpoint.validate("filesystem_checkpoint"); err != nil {
		observationErrors = append(observationErrors, err)
	}
	return checkpoint, errors.Join(observationErrors...)
}

// Checkpoint captures and immediately verifies one declared action oracle.
// The checkpoint is returned on mismatch so a caller can still write it to
// paired evidence before classifying the action as broken.
func (o *FilesystemOracle) Checkpoint(id, actionID string, at time.Duration, expectations []FilesystemExpectation) (FilesystemCheckpoint, error) {
	checkpoint, captureErr := o.CaptureCheckpoint(id, actionID, at, expectations)
	if captureErr != nil {
		return checkpoint, captureErr
	}
	return checkpoint, VerifyFilesystemExpectations(expectations, checkpoint)
}

// CaptureFilesystemCheckpoint is the one-shot form of CaptureCheckpoint.
func CaptureFilesystemCheckpoint(root, id, actionID string, at time.Duration, expectations []FilesystemExpectation) (FilesystemCheckpoint, error) {
	oracle, err := NewFilesystemOracle(root)
	if err != nil {
		return FilesystemCheckpoint{}, err
	}
	return oracle.CaptureCheckpoint(id, actionID, at, expectations)
}

// CheckFilesystemCheckpoint is the one-shot form of Checkpoint.
func CheckFilesystemCheckpoint(root, id, actionID string, at time.Duration, expectations []FilesystemExpectation) (FilesystemCheckpoint, error) {
	oracle, err := NewFilesystemOracle(root)
	if err != nil {
		return FilesystemCheckpoint{}, err
	}
	return oracle.Checkpoint(id, actionID, at, expectations)
}

func (o *FilesystemOracle) observe(relative string) (FilesystemCheckpointEntry, error) {
	if err := validateRelativePath("filesystem.path", relative, false); err != nil {
		return FilesystemCheckpointEntry{}, err
	}
	absPath, err := safeFilesystemPath(o.root, relative)
	if err != nil {
		return FilesystemCheckpointEntry{}, err
	}
	info, err := os.Lstat(absPath)
	if errors.Is(err, os.ErrNotExist) {
		return FilesystemCheckpointEntry{Path: relative, Type: FileTypeAbsent}, nil
	}
	if err != nil {
		return FilesystemCheckpointEntry{}, fmt.Errorf("%w: inspect %q: %v", ErrFilesystemOracleObservation, relative, err)
	}

	switch {
	case info.Mode()&os.ModeSymlink != 0:
		target, err := os.Readlink(absPath)
		if err != nil {
			return FilesystemCheckpointEntry{}, fmt.Errorf("%w: read symlink %q: %v", ErrFilesystemOracleObservation, relative, err)
		}
		return FilesystemCheckpointEntry{
			Path: relative, Type: FileTypeSymlink, Size: int64(len(target)),
			SHA256: sha256HexBytes([]byte(target)), Target: target,
		}, nil
	case info.IsDir():
		digest, err := filesystemDirectorySHA256(absPath)
		if err != nil {
			return FilesystemCheckpointEntry{}, fmt.Errorf("%w: fingerprint directory %q: %v", ErrFilesystemOracleObservation, relative, err)
		}
		return FilesystemCheckpointEntry{Path: relative, Type: FileTypeDirectory, SHA256: digest}, nil
	case info.Mode().IsRegular():
		data, err := os.ReadFile(absPath)
		if err != nil {
			return FilesystemCheckpointEntry{}, fmt.Errorf("%w: read file %q: %v", ErrFilesystemOracleObservation, relative, err)
		}
		return FilesystemCheckpointEntry{
			Path: relative, Type: FileTypeFile, Size: int64(len(data)), SHA256: sha256HexBytes(data),
		}, nil
	default:
		return FilesystemCheckpointEntry{}, fmt.Errorf("%w: %q is not a regular file, directory, or symlink", ErrFilesystemOracleObservation, relative)
	}
}

func safeFilesystemPath(root, relative string) (string, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("%w: resolve root: %v", ErrFilesystemOracleInvalidRoot, err)
	}
	path, err := filepath.Abs(filepath.Join(absRoot, filepath.FromSlash(relative)))
	if err != nil {
		return "", fmt.Errorf("%w: resolve path %q: %v", ErrFilesystemOracleObservation, relative, err)
	}
	rel, err := filepath.Rel(absRoot, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: path %q escapes root", ErrFilesystemOracleObservation, relative)
	}
	return path, nil
}

// FilesystemDirectorySHA256 returns the deterministic fingerprint used for
// directory checkpoint entries. It includes the relative type of every
// descendant and the content hash of regular files, without following
// symlinks. A directory checkpoint therefore proves more than just a path
// type while remaining stable across machines.
func FilesystemDirectorySHA256(root string) (string, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("%w: resolve directory: %v", ErrFilesystemOracleInvalidRoot, err)
	}
	info, err := os.Lstat(absRoot)
	if err != nil {
		return "", fmt.Errorf("%w: inspect directory: %v", ErrFilesystemOracleInvalidRoot, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("%w: %q is not a non-symlink directory", ErrFilesystemOracleInvalidRoot, absRoot)
	}
	return filesystemDirectorySHA256(absRoot)
}

func filesystemDirectorySHA256(root string) (string, error) {
	records := []string{"d ."}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			records = append(records, fmt.Sprintf("l %s %q", relative, target))
		case info.IsDir():
			records = append(records, "d "+relative)
		case info.Mode().IsRegular():
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			records = append(records, fmt.Sprintf("f %s %s", relative, sha256HexBytes(data)))
		default:
			return fmt.Errorf("unsupported filesystem entry %q", relative)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(records)
	return sha256HexBytes([]byte(strings.Join(records, "\n") + "\n")), nil
}

// FilesystemOracleMismatch identifies one falsifiable mismatch between a
// declared expectation and the checkpoint captured at that action boundary.
type FilesystemOracleMismatch struct {
	Path     string
	Expected string
	Actual   string
}

func (e *FilesystemOracleMismatch) Error() string {
	if e == nil {
		return ErrFilesystemOracleMismatch.Error()
	}
	return fmt.Sprintf("filesystem expectation %q: expected %s, observed %s: %v", e.Path, e.Expected, e.Actual, ErrFilesystemOracleMismatch)
}

func (e *FilesystemOracleMismatch) Unwrap() error { return ErrFilesystemOracleMismatch }

// VerifyFilesystemExpectations compares a checkpoint with exactly the facts
// declared by an action. It deliberately does not inspect a later snapshot,
// so a delayed side effect cannot repair an incorrect intermediate history.
func VerifyFilesystemExpectations(expectations []FilesystemExpectation, checkpoint FilesystemCheckpoint) error {
	if len(expectations) == 0 {
		return fmt.Errorf("%w: no expectations declared", ErrFilesystemOracleMismatch)
	}
	entries := make(map[string]FilesystemCheckpointEntry, len(checkpoint.Entries))
	for _, entry := range checkpoint.Entries {
		entries[entry.Path] = entry
	}
	var mismatches []error
	for _, expectation := range expectations {
		actual, ok := entries[expectation.Path]
		if !ok {
			mismatches = append(mismatches, &FilesystemOracleMismatch{Path: expectation.Path, Expected: filesystemExpectationDescription(expectation), Actual: "missing observation"})
			continue
		}
		if actual.Type != expectation.Type {
			mismatches = append(mismatches, &FilesystemOracleMismatch{Path: expectation.Path, Expected: filesystemExpectationDescription(expectation), Actual: filesystemCheckpointDescription(actual)})
			continue
		}
		if expectation.Type == FileTypeAbsent {
			continue
		}
		expectedHash := expectation.SHA256
		if expectedHash == "" && expectation.Content != "" {
			expectedHash = sha256HexBytes([]byte(expectation.Content))
		}
		if actual.SHA256 != expectedHash {
			mismatches = append(mismatches, &FilesystemOracleMismatch{Path: expectation.Path, Expected: filesystemExpectationDescription(expectation), Actual: filesystemCheckpointDescription(actual)})
			continue
		}
		if expectation.Type == FileTypeFile && expectation.Content != "" && actual.Size != int64(len(expectation.Content)) {
			mismatches = append(mismatches, &FilesystemOracleMismatch{Path: expectation.Path, Expected: filesystemExpectationDescription(expectation), Actual: filesystemCheckpointDescription(actual)})
		}
	}
	return errors.Join(mismatches...)
}

func filesystemExpectationDescription(expectation FilesystemExpectation) string {
	description := string(expectation.Type)
	if expectation.SHA256 != "" {
		description += " sha256=" + expectation.SHA256
	}
	return description
}

func filesystemCheckpointDescription(entry FilesystemCheckpointEntry) string {
	description := string(entry.Type)
	if entry.SHA256 != "" {
		description += " sha256=" + entry.SHA256
	}
	if entry.Size != 0 {
		description += fmt.Sprintf(" size=%d", entry.Size)
	}
	return description
}

func sha256HexBytes(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

const (
	filesystemCheckpointEvidenceRef = "filesystem-checkpoints.jsonl"
	toolObservationEvidenceRef      = "tool-observations.jsonl"
	productTranscriptEvidenceRef    = "transcripts/product.jsonl"
)

// EvaluateCustomerSimulation applies the mechanical action, tool, checkpoint,
// and spoken-summary oracles to one completed run. It emits one result for
// every declared action in scenario order and retains findings for missing or
// malformed observations instead of converting them into an apparent pass.
func EvaluateCustomerSimulation(
	scenario CustomerScenario,
	actionResults []ActionResult,
	checkpoints []FilesystemCheckpoint,
	toolObservations []ToolObservation,
	productTranscript []TranscriptEvent,
) (MechanicalVerdict, error) {
	if err := scenario.Validate(); err != nil {
		return MechanicalVerdict{}, err
	}

	checkpointByID := make(map[string]FilesystemCheckpoint, len(checkpoints))
	for _, checkpoint := range checkpoints {
		if _, exists := checkpointByID[checkpoint.ID]; exists {
			return MechanicalVerdict{}, fmt.Errorf("%w: duplicate checkpoint %q", ErrInvalidCustomerEvidence, checkpoint.ID)
		}
		checkpointByID[checkpoint.ID] = checkpoint
	}
	toolByID := make(map[string]ToolObservation, len(toolObservations))
	for _, observation := range toolObservations {
		if _, exists := toolByID[observation.ID]; exists {
			return MechanicalVerdict{}, fmt.Errorf("%w: duplicate tool observation %q", ErrInvalidCustomerEvidence, observation.ID)
		}
		toolByID[observation.ID] = observation
	}
	actionByID := make(map[string]ActionResult, len(actionResults))
	scenarioActionIDs := make(map[string]struct{}, len(scenario.Actions))
	for _, action := range scenario.Actions {
		scenarioActionIDs[action.ID] = struct{}{}
	}
	var findings []MechanicalFinding
	addFinding := func(code, actionID, turnID, message string) {
		findings = append(findings, MechanicalFinding{
			Code: code, ActionID: actionID, TurnID: turnID, Message: message,
			EvidenceRefs: []string{filesystemCheckpointEvidenceRef, toolObservationEvidenceRef, productTranscriptEvidenceRef},
		})
	}
	for index, result := range actionResults {
		if _, exists := actionByID[result.ActionID]; exists {
			return MechanicalVerdict{}, fmt.Errorf("%w: duplicate action result %q", ErrDuplicateActionIntent, result.ActionID)
		}
		actionByID[result.ActionID] = result
		if _, known := scenarioActionIDs[result.ActionID]; !known {
			addFinding("unknown_action", result.ActionID, result.TurnID, "action result is not declared by the scenario")
		}
		if index < len(scenario.Actions) && result.ActionID != scenario.Actions[index].ID {
			addFinding("action_order_mismatch", result.ActionID, result.TurnID, fmt.Sprintf("action appeared at position %d; expected %q", index+1, scenario.Actions[index].ID))
		}
	}

	results := make([]ActionResult, 0, len(scenario.Actions))
	for _, action := range scenario.Actions {
		result, observed := actionByID[action.ID]
		if !observed {
			result = ActionResult{
				ActionID:      action.ID,
				Disposition:   fallbackDisposition(action),
				OutcomeReason: "no terminal action observation was recorded",
				EvidenceRefs:  defaultActionEvidenceRefs(),
			}
			addFinding("missing_action", action.ID, "", "the simulator did not record a terminal disposition")
		}
		if len(result.EvidenceRefs) == 0 {
			result.EvidenceRefs = defaultActionEvidenceRefs()
		}
		if !result.Disposition.valid() {
			addFinding("invalid_disposition", action.ID, result.TurnID, fmt.Sprintf("disposition %q is not terminal", result.Disposition))
		} else if !dispositionAllowed(action, result.Disposition) {
			addFinding("disallowed_disposition", action.ID, result.TurnID, fmt.Sprintf("disposition %q is not allowed by the action", result.Disposition))
		}
		if result.ConfirmedAt < 0 {
			addFinding("invalid_confirmation_time", action.ID, result.TurnID, "confirmation timestamp is negative")
		}
		if result.Disposition != DispositionCompleted {
			addFinding("action_not_completed", action.ID, result.TurnID, fmt.Sprintf("action ended with %q: %s", result.Disposition, result.OutcomeReason))
		}
		if action.Oracle.RequireConfirmation && !result.Confirmed {
			addFinding("missing_confirmation", action.ID, result.TurnID, "the action oracle requires a customer-visible confirmation")
		}

		var actionCheckpoints []FilesystemCheckpoint
		for _, checkpoint := range checkpoints {
			if checkpoint.ActionID == action.ID {
				actionCheckpoints = append(actionCheckpoints, checkpoint)
			}
		}
		var selectedCheckpoint *FilesystemCheckpoint
		for _, checkpointID := range result.CheckpointIDs {
			checkpoint, ok := checkpointByID[checkpointID]
			if !ok {
				addFinding("missing_checkpoint", action.ID, result.TurnID, fmt.Sprintf("checkpoint %q was referenced but not recorded", checkpointID))
				continue
			}
			if checkpoint.ActionID != action.ID {
				addFinding("checkpoint_action_mismatch", action.ID, result.TurnID, fmt.Sprintf("checkpoint %q belongs to action %q", checkpointID, checkpoint.ActionID))
				continue
			}
			if selectedCheckpoint == nil {
				copyOfCheckpoint := checkpoint
				selectedCheckpoint = &copyOfCheckpoint
			}
		}
		if len(action.Oracle.Checkpoints) > 0 {
			if selectedCheckpoint == nil {
				addFinding("missing_checkpoint", action.ID, result.TurnID, "the action has filesystem expectations but no referenced checkpoint")
			} else if err := VerifyFilesystemExpectations(action.Oracle.Checkpoints, *selectedCheckpoint); err != nil {
				addFinding("filesystem_checkpoint_mismatch", action.ID, result.TurnID, err.Error())
				if result.Confirmed {
					addFinding("confirmation_without_matching_side_effect", action.ID, result.TurnID, "confirmation was recorded for a checkpoint that does not satisfy the action oracle")
				}
			}
		} else if len(result.CheckpointIDs) > 0 && len(actionCheckpoints) == 0 {
			addFinding("unexpected_checkpoint", action.ID, result.TurnID, "the result references a checkpoint for an action with no filesystem oracle")
		}

		if action.PartialSideEffectPolicy != PartialSideEffectsForbid && result.Disposition == DispositionCompleted && len(result.ToolObservationIDs) == 0 {
			addFinding("missing_tool_evidence", action.ID, result.TurnID, "a side-effecting completed action has no tool observation")
		}
		seenToolIDs := map[string]struct{}{}
		for _, toolID := range result.ToolObservationIDs {
			if _, duplicate := seenToolIDs[toolID]; duplicate {
				addFinding("duplicate_tool_evidence", action.ID, result.TurnID, fmt.Sprintf("tool observation %q was referenced twice", toolID))
				continue
			}
			seenToolIDs[toolID] = struct{}{}
			observation, ok := toolByID[toolID]
			if !ok {
				addFinding("missing_tool_evidence", action.ID, result.TurnID, fmt.Sprintf("tool observation %q was not recorded", toolID))
				continue
			}
			if observation.ActionID != action.ID || (result.TurnID != "" && observation.TurnID != result.TurnID) {
				addFinding("tool_action_mismatch", action.ID, result.TurnID, fmt.Sprintf("tool observation %q is correlated to action %q / turn %q", toolID, observation.ActionID, observation.TurnID))
			}
			if observation.Status != "completed" || !observation.ResultSeen {
				addFinding("tool_result_incomplete", action.ID, result.TurnID, fmt.Sprintf("tool observation %q is status=%q result_seen=%t", toolID, observation.Status, observation.ResultSeen))
			}
			if result.Confirmed && observation.At+observation.Duration > result.ConfirmedAt {
				addFinding("confirmation_before_tool_result", action.ID, result.TurnID, fmt.Sprintf("confirmation at %s preceded tool result at %s", result.ConfirmedAt, observation.At+observation.Duration))
			}
			if selectedCheckpoint != nil && observation.At+observation.Duration > selectedCheckpoint.At {
				addFinding("checkpoint_before_tool_result", action.ID, result.TurnID, fmt.Sprintf("checkpoint at %s precedes tool result at %s", selectedCheckpoint.At, observation.At+observation.Duration))
			}
		}

		text := transcriptTextForTurn(productTranscript, result.TurnID)
		for _, requiredText := range action.Oracle.RequiredText {
			if !strings.Contains(strings.ToLower(text), strings.ToLower(requiredText)) {
				addFinding("summary_missing_fact", action.ID, result.TurnID, fmt.Sprintf("product transcript does not contain required fact %q", requiredText))
			}
		}
		for _, forbiddenText := range action.Oracle.ForbiddenText {
			if strings.Contains(strings.ToLower(text), strings.ToLower(forbiddenText)) {
				addFinding("summary_claims_absent_fact", action.ID, result.TurnID, fmt.Sprintf("product transcript contains forbidden or stale fact %q", forbiddenText))
			}
		}
		results = append(results, result)
	}

	for _, checkpoint := range checkpoints {
		if _, known := scenarioActionIDs[checkpoint.ActionID]; !known {
			addFinding("unknown_checkpoint_action", checkpoint.ActionID, "", fmt.Sprintf("checkpoint %q names an undeclared action", checkpoint.ID))
		}
	}

	verdict := MechanicalVerdict{
		Pass:          len(findings) == 0,
		Summary:       mechanicalSummary(len(findings), len(scenario.Actions)),
		ActionResults: results,
		Findings:      findings,
	}
	if err := verdict.validate(scenario, "mechanical_verdict"); err != nil {
		return verdict, err
	}
	return verdict, nil
}

// EvaluateCustomerSimulationCorrection adds the Family B correction ledger
// to the ordinary action/tool/filesystem oracle. It permits an explicitly
// cancelled original action only when the response was actually interrupted;
// a replacement still has to complete against its own tool and filesystem
// evidence.
func EvaluateCustomerSimulationCorrection(
	scenario CustomerScenario,
	actionResults []ActionResult,
	checkpoints []FilesystemCheckpoint,
	toolObservations []ToolObservation,
	productTranscript []TranscriptEvent,
	correction CorrectionEvidence,
) (MechanicalVerdict, error) {
	if err := scenario.Validate(); err != nil {
		return MechanicalVerdict{}, err
	}
	if err := correction.Validate(scenario); err != nil {
		return MechanicalVerdict{}, err
	}

	mechanical, err := EvaluateCustomerSimulation(scenario, actionResults, checkpoints, toolObservations, productTranscript)
	if err != nil {
		return mechanical, err
	}

	actions := make(map[string]ActionIntent, len(scenario.Actions))
	actionOrder := make(map[string]int, len(scenario.Actions))
	for index, action := range scenario.Actions {
		actions[action.ID] = action
		actionOrder[action.ID] = index
	}
	_, originalKnown := actions[correction.OriginalActionID]
	replacementAction, replacementKnown := actions[correction.ReplacementActionID]
	findings := append([]MechanicalFinding(nil), mechanical.Findings...)
	addFinding := func(code, actionID, turnID, message string) {
		findings = append(findings, MechanicalFinding{
			Code: code, ActionID: actionID, TurnID: turnID, Message: message,
			EvidenceRefs: []string{filesystemCheckpointEvidenceRef, toolObservationEvidenceRef, productTranscriptEvidenceRef},
		})
	}

	if !originalKnown {
		addFinding("unknown_original_action", correction.OriginalActionID, correction.OriginalTurnID, "correction names an undeclared original action")
	}
	if !replacementKnown {
		addFinding("unknown_replacement_action", correction.ReplacementActionID, correction.CorrectionTurnID, "correction names an undeclared replacement action")
	}
	if originalKnown && replacementKnown && actionOrder[correction.OriginalActionID] >= actionOrder[correction.ReplacementActionID] {
		addFinding("correction_action_order", correction.OriginalActionID, correction.OriginalTurnID, "replacement action must follow the original action")
	}
	if scenario.Interruption.Kind != InterruptionDuringOutput {
		addFinding("interruption_trigger_mismatch", correction.OriginalActionID, correction.OriginalTurnID, fmt.Sprintf("Family B requires during_output interruption, got %q", scenario.Interruption.Kind))
	}
	if scenario.Interruption.ActionID != correction.OriginalActionID {
		addFinding("interruption_action_mismatch", correction.OriginalActionID, correction.OriginalTurnID, fmt.Sprintf("scenario interruption targets %q", scenario.Interruption.ActionID))
	}

	resultByID := make(map[string]ActionResult, len(actionResults))
	for _, result := range actionResults {
		resultByID[result.ActionID] = result
	}
	originalResult, originalResultObserved := resultByID[correction.OriginalActionID]
	replacementResult, replacementResultObserved := resultByID[correction.ReplacementActionID]

	// Generic evaluation deliberately treats every non-completed action as a
	// failure. Family B is the one scenario where cancellation is an intended
	// terminal disposition, but only with a matching provider cancellation.
	if originalResultObserved && originalResult.Disposition == DispositionCancelled && isCorrectionCancelledStatus(correction.OriginalResponseStatus) {
		filtered := findings[:0]
		for _, finding := range findings {
			if finding.Code == "action_not_completed" && finding.ActionID == correction.OriginalActionID {
				continue
			}
			filtered = append(filtered, finding)
		}
		findings = filtered
	}

	if !originalResultObserved {
		addFinding("original_action_unresolved", correction.OriginalActionID, correction.OriginalTurnID, "the original action has no terminal disposition")
	} else if originalResult.TurnID != correction.OriginalTurnID {
		addFinding("original_turn_mismatch", correction.OriginalActionID, originalResult.TurnID, fmt.Sprintf("correction ledger names turn %q", correction.OriginalTurnID))
	}
	if !replacementResultObserved {
		addFinding("replacement_not_verified", correction.ReplacementActionID, correction.CorrectionTurnID, "the replacement action has no independently recorded terminal result")
	} else {
		if replacementResult.TurnID != correction.CorrectionTurnID {
			addFinding("replacement_turn_mismatch", correction.ReplacementActionID, replacementResult.TurnID, fmt.Sprintf("correction ledger names turn %q", correction.CorrectionTurnID))
		}
		if replacementResult.Disposition != DispositionCompleted {
			addFinding("replacement_not_completed", correction.ReplacementActionID, replacementResult.TurnID, fmt.Sprintf("replacement ended with %q", replacementResult.Disposition))
		}
		if len(replacementResult.CheckpointIDs) == 0 {
			addFinding("replacement_not_verified", correction.ReplacementActionID, replacementResult.TurnID, "replacement completion has no filesystem checkpoint")
		}
		if len(replacementResult.ToolObservationIDs) == 0 && replacementAction.PartialSideEffectPolicy != PartialSideEffectsForbid {
			addFinding("replacement_not_independent", correction.ReplacementActionID, replacementResult.TurnID, "replacement completion has no tool evidence distinct from the original work")
		}
	}

	if !isCorrectionCancelledStatus(correction.OriginalResponseStatus) {
		addFinding("correction_ignored", correction.OriginalActionID, correction.OriginalTurnID, fmt.Sprintf("original response ended with status %q instead of cancelled", correction.OriginalResponseStatus))
	}
	if !isCorrectionCompletedStatus(correction.ReplacementResponseStatus) {
		addFinding("replacement_response_incomplete", correction.ReplacementActionID, correction.CorrectionTurnID, fmt.Sprintf("replacement response ended with status %q", correction.ReplacementResponseStatus))
	}
	if !correction.CancellationEventRecorded {
		addFinding("cancellation_event_missing", correction.OriginalActionID, correction.CorrectionTurnID, "the copied product recording has no outbound RESPONSE.CANCEL event")
	} else if strings.TrimSpace(correction.CancellationResponseID) == "" {
		addFinding("cancellation_response_missing", correction.OriginalActionID, correction.CorrectionTurnID, "the recorded RESPONSE.CANCEL event is not associated with an original response")
	} else if correction.CancellationResponseID != correction.OriginalResponseID {
		addFinding("cancellation_response_mismatch", correction.OriginalActionID, correction.CorrectionTurnID, fmt.Sprintf("recorded RESPONSE.CANCEL targets %q, original response is %q", correction.CancellationResponseID, correction.OriginalResponseID))
	}
	if correction.OriginalResponseStartedAt >= correction.CorrectionStartedAt {
		addFinding("correction_not_after_output_start", correction.OriginalActionID, correction.OriginalTurnID, "correction speech did not begin after original output started")
	}
	if correction.CorrectionStartedAt >= correction.OriginalResponseEndedAt {
		addFinding("correction_after_response", correction.OriginalActionID, correction.CorrectionTurnID, "correction speech began after the original response had already ended")
	}
	if correction.CancellationSentAt < correction.OriginalResponseStartedAt || correction.CancellationSentAt >= correction.CorrectionStartedAt {
		addFinding("cancellation_boundary_missing", correction.OriginalActionID, correction.CorrectionTurnID, "response cancellation was not observed between original output start and correction speech")
	}
	if correction.ReplacementResponseStartedAt < correction.CorrectionStartedAt {
		addFinding("replacement_started_before_correction", correction.ReplacementActionID, correction.CorrectionTurnID, "replacement response started before the correction utterance")
	}
	if correction.ReplacementResponseEndedAt <= correction.ReplacementResponseStartedAt {
		addFinding("replacement_response_unfinished", correction.ReplacementActionID, correction.CorrectionTurnID, "replacement response has no positive completed interval")
	}

	productTurns := map[string]struct{}{}
	for _, event := range productTranscript {
		if strings.TrimSpace(event.Text) != "" {
			productTurns[event.TurnID] = struct{}{}
		}
	}
	if _, ok := productTurns[correction.OriginalTurnID]; !ok {
		addFinding("original_confirmation_missing", correction.OriginalActionID, correction.OriginalTurnID, "no product transcript evidence was recorded for the original action")
	}
	if _, ok := productTurns[correction.CorrectionTurnID]; !ok {
		addFinding("correction_confirmation_missing", correction.ReplacementActionID, correction.CorrectionTurnID, "no product transcript evidence was recorded for the corrected request")
	}

	for _, toolID := range correction.OutstandingToolIDs {
		if strings.TrimSpace(toolID) == "" {
			addFinding("unresolved_tool", correction.OriginalActionID, correction.OriginalTurnID, "an outstanding tool ledger entry has an empty ID")
			continue
		}
		addFinding("unresolved_tool", correction.OriginalActionID, correction.OriginalTurnID, fmt.Sprintf("tool %q was still outstanding at session termination", toolID))
	}
	for _, actionID := range correction.UnresolvedActionIDs {
		addFinding("unresolved_action", actionID, correction.OriginalTurnID, "an action remained unresolved at session termination")
	}
	for _, observation := range toolObservations {
		if (observation.ActionID == correction.OriginalActionID || observation.ActionID == correction.ReplacementActionID) && (observation.Status == "started" || !observation.ResultSeen) {
			addFinding("unresolved_tool", observation.ActionID, observation.TurnID, fmt.Sprintf("tool observation %q has status=%q result_seen=%t", observation.ID, observation.Status, observation.ResultSeen))
		}
	}

	if correction.Process != nil {
		process := correction.Process
		if process.DescendantsAlive {
			addFinding("orphan_process", correction.ReplacementActionID, correction.CorrectionTurnID, "a descendant process remained alive after the corrected run")
		}
		if !process.ChildWaited {
			addFinding("child_not_reaped", correction.ReplacementActionID, correction.CorrectionTurnID, "the shipped child was not reaped")
		}
		if !process.InputClosed || !process.OutputClosed {
			addFinding("stream_not_closed", correction.ReplacementActionID, correction.CorrectionTurnID, fmt.Sprintf("process streams closed input=%t output=%t", process.InputClosed, process.OutputClosed))
		}
		if process.ExitClassification != "normal" {
			addFinding("unclean_process_termination", correction.ReplacementActionID, correction.CorrectionTurnID, fmt.Sprintf("corrected run exit classification was %q", process.ExitClassification))
		}
	}

	mechanical.Findings = findings
	mechanical.Pass = len(findings) == 0
	mechanical.Summary = mechanicalSummary(len(findings), len(scenario.Actions))
	if err := mechanical.validate(scenario, "mechanical_verdict"); err != nil {
		return mechanical, err
	}
	return mechanical, nil
}

func isCorrectionCancelledStatus(status string) bool {
	return status == "cancelled" || status == "canceled"
}

func isCorrectionCompletedStatus(status string) bool {
	return status == "completed"
}

func defaultActionEvidenceRefs() []string {
	return []string{filesystemCheckpointEvidenceRef, toolObservationEvidenceRef, productTranscriptEvidenceRef}
}

func fallbackDisposition(action ActionIntent) TerminalDisposition {
	for _, candidate := range action.AllowedDispositions {
		if candidate == DispositionFailed || candidate == DispositionCancelled {
			return candidate
		}
	}
	if len(action.AllowedDispositions) > 0 {
		return action.AllowedDispositions[0]
	}
	return DispositionFailed
}

func dispositionAllowed(action ActionIntent, disposition TerminalDisposition) bool {
	for _, candidate := range action.AllowedDispositions {
		if candidate == disposition {
			return true
		}
	}
	return false
}

func transcriptTextForTurn(events []TranscriptEvent, turnID string) string {
	var parts []string
	for _, event := range events {
		if event.TurnID == turnID {
			parts = append(parts, event.Text)
		}
	}
	return strings.Join(parts, " ")
}

func mechanicalSummary(findingCount, actionCount int) string {
	if findingCount == 0 {
		return fmt.Sprintf("all %d ordered actions have terminal, truth-checked observations", actionCount)
	}
	return fmt.Sprintf("mechanical oracle found %d finding(s) across %d ordered actions", findingCount, actionCount)
}
