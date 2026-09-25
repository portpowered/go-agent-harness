package probe

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	runtimeReplay "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
)

// This file owns the opt-in customer-simulation process harness. The harness
// deliberately composes the shipped session CLI, the existing duplex runner,
// and the versioned evidence bundle; it does not add a second session runtime.

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

// CustomerSimulationRunSpec binds a scenario to ordered PCM16 turns, keeping
// file formats and credentials at the process boundary.
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

// CustomerSimulationSuiteOptions configures a selected suite; APIKey stays in
// memory and reaches the child through DuplexRunner's provider environment.
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
	ReplayService     runtimeReplay.StreamMessageCodec
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
	for _, raw := range selectors {
		ids, ok := customerSimulationSelectorScenarioIDs(strings.ToLower(strings.TrimSpace(raw)))
		if !ok {
			return nil, fmt.Errorf("%w: unknown family selector %q", ErrCustomerSimulationSelection, raw)
		}
		for _, id := range ids {
			scenario := byID[id]
			if err := scenario.Validate(); err != nil {
				return nil, err
			}
			if _, duplicate := seen[scenario.ID]; duplicate {
				return nil, fmt.Errorf("%w: scenario %q was selected more than once", ErrCustomerSimulationSelection, scenario.ID)
			}
			seen[scenario.ID] = struct{}{}
			result = append(result, scenario)
		}
	}
	return result, nil
}

// customerSimulationSelectorScenarioIDs maps one normalized selector to the
// built-in scenario IDs it selects, in run order.
func customerSimulationSelectorScenarioIDs(selector string) ([]string, bool) {
	switch selector {
	case "a", "family-a", FamilyAScenarioID:
		return []string{FamilyAScenarioID}, true
	case "b", "family-b", FamilyBScenarioID:
		return []string{FamilyBScenarioID}, true
	case "c", "family-c", FamilyCScenarioID:
		return []string{FamilyCScenarioID}, true
	case "d", "family-d":
		return []string{FamilyDScenarioSIGINTID, FamilyDScenarioNaturalID}, true
	case "d-sigint", FamilyDScenarioSIGINTID:
		return []string{FamilyDScenarioSIGINTID}, true
	case "d-natural", FamilyDScenarioNaturalID:
		return []string{FamilyDScenarioNaturalID}, true
	case "e", "family-e", FamilyEScenarioID:
		return []string{FamilyEScenarioID}, true
	default:
		return nil, false
	}
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
		if err := validateCustomerSimulationRunSpec(index, spec); err != nil {
			return err
		}
	}
	return nil
}

func validateCustomerSimulationRunSpec(index int, spec CustomerSimulationRunSpec) error {
	if err := spec.Scenario.Validate(); err != nil {
		return fmt.Errorf("%w: scenario %d: %w", ErrCustomerSimulationSelection, index+1, err)
	}
	script := spec.Script
	if len(script) == 0 {
		script = CustomerSimulationScenarioScript(spec.Scenario)
	}
	if len(script) != len(spec.Scenario.Actions) || len(spec.Audio) != len(script) {
		return fmt.Errorf("%w: scenario %q needs one PCM16 turn per declared action (%d), got script=%d audio=%d", ErrCustomerSimulationAudio, spec.Scenario.ID, len(spec.Scenario.Actions), len(script), len(spec.Audio))
	}
	if err := validateCustomerSimulationTurnAudio(spec, script); err != nil {
		return err
	}
	if spec.Scenario.Family == ScenarioFamilyE {
		if len(spec.PatienceRepromptAudio) == 0 || len(spec.PatienceRepromptAudio)%2 != 0 {
			return fmt.Errorf("%w: Family E scenario %q needs non-empty even-length patience re-prompt PCM16", ErrCustomerSimulationAudio, spec.Scenario.ID)
		}
	} else if len(spec.PatienceRepromptAudio) > 0 {
		return fmt.Errorf("%w: patience re-prompt audio is only valid for Family E scenario %q", ErrCustomerSimulationAudio, spec.Scenario.ID)
	}
	return nil
}

func validateCustomerSimulationTurnAudio(spec CustomerSimulationRunSpec, script []CustomerScriptTurn) error {
	for audioIndex, audio := range spec.Audio {
		if len(audio) == 0 || len(audio)%2 != 0 {
			return fmt.Errorf("%w: scenario %q turn %d must contain non-empty even-length PCM16", ErrCustomerSimulationAudio, spec.Scenario.ID, audioIndex+1)
		}
		if strings.TrimSpace(script[audioIndex].ActionID) == "" || strings.TrimSpace(script[audioIndex].Text) == "" {
			return fmt.Errorf("%w: scenario %q turn %d needs a visible action ID and customer wording", ErrCustomerSimulationAudio, spec.Scenario.ID, audioIndex+1)
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
			indices[index] = min(textAction[index], len(scenario.Actions)-1)
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
		indices[index] = min(actionIndex, len(scenario.Actions)-1)
	}
	return indices
}

func customerSimulationOutputPartsForRanges(start, end int64, ranges []customerSimulationResponseAudioRange) []customerSimulationOutputPart {
	if end <= start {
		return nil
	}
	parts := make([]customerSimulationOutputPart, 0, 1)
	for _, response := range ranges {
		overlapStart := max(start, response.Start)
		overlapEnd := min(end, response.End)
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
		if done, err := stepCustomerSimulationPatienceReprompt(controller, progress, outputIndex, repromptOutputIndex); done {
			return err
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

// stepCustomerSimulationPatienceReprompt performs one observation and policy
// decision before the re-prompt. done reports that the wait has ended with
// the returned result.
func stepCustomerSimulationPatienceReprompt(controller *PatienceController, progress *DuplexProgress, outputIndex, repromptOutputIndex *int) (bool, error) {
	if err := observeCustomerSimulationOutput(controller, progress, outputIndex); err != nil {
		return true, err
	}
	if progress.OutputClosed() {
		if err := completeCustomerSimulationPatience(controller); err != nil {
			return true, err
		}
		// A normal child boundary is a valid end to the one-turn patience
		// conversation. Stop the not-yet-delivered re-prompt segment without
		// misclassifying the intentionally short input script as premature EOF.
		return true, errDuplexInputComplete
	}
	decision, err := controller.Decision()
	if err != nil {
		return true, err
	}
	switch decision.Kind {
	case PatienceDecisionReprompt:
		if _, err := controller.Reprompt(FamilyEReprompt(decision.RepromptCount)); err != nil {
			return true, err
		}
		*repromptOutputIndex = len(progress.OutputEvents())
		return true, nil
	case PatienceDecisionDeadAir:
		if err := controller.DeclareDeadAir(); err != nil {
			return true, err
		}
		return true, fmt.Errorf("family E patience dead air: no observable progress for %s", decision.SinceLastProgress)
	case PatienceDecisionWait, PatienceDecisionComplete:
	}
	return false, nil
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
		if done, err := stepCustomerSimulationPatienceCompletion(controller, progress, outputIndex, repromptOutputIndex); done {
			return err
		}
		if err := waitForCustomerSimulationPatienceChange(ctx, progress); err != nil {
			if progress.OutputClosed() {
				return settleCustomerSimulationPatienceClose(controller, progress, outputIndex, repromptOutputIndex)
			}
			return finishCustomerSimulationPatienceOnContext(controller, ctx, err)
		}
	}
}

// stepCustomerSimulationPatienceCompletion performs one observation and
// policy decision after the re-prompt. done reports that the wait has ended
// with the returned result.
func stepCustomerSimulationPatienceCompletion(controller *PatienceController, progress *DuplexProgress, outputIndex *int, repromptOutputIndex int) (bool, error) {
	before := *outputIndex
	if err := observeCustomerSimulationOutput(controller, progress, outputIndex); err != nil {
		return true, err
	}
	if repromptOutputIndex >= 0 && *outputIndex > repromptOutputIndex {
		// A post-re-prompt product audio boundary is the terminal response
		// signal for Family E's one-response script. Close the owned input
		// stream now so the shipped child can deliver its end-of-turn and
		// provider-close controls; waiting for stdout to close first would
		// deadlock when --wait-for-close is enabled.
		if err := completeCustomerSimulationPatience(controller); err != nil {
			return true, err
		}
		return true, errDuplexInputComplete
	}
	if progress.OutputClosed() {
		if repromptOutputIndex >= 0 && *outputIndex <= repromptOutputIndex && before == *outputIndex {
			return true, cancelCustomerSimulationPatienceWithoutOutput(controller)
		}
		return true, completeCustomerSimulationPatience(controller)
	}
	decision, err := controller.Decision()
	if err != nil {
		return true, err
	}
	if decision.Kind == PatienceDecisionDeadAir {
		if err := controller.DeclareDeadAir(); err != nil {
			return true, err
		}
		// Close the input boundary after recording the policy breach. This
		// lets the shipped session flush its product record and terminate at
		// its normal end-of-input boundary; the finalized patience evidence
		// remains BROKEN, so graceful process cleanup cannot hide the dead air.
		return true, errDuplexInputComplete
	}
	return false, nil
}

// settleCustomerSimulationPatienceClose classifies a closed stdout boundary
// observed while waiting for post-re-prompt output.
func settleCustomerSimulationPatienceClose(controller *PatienceController, progress *DuplexProgress, outputIndex *int, repromptOutputIndex int) error {
	if observeErr := observeCustomerSimulationOutput(controller, progress, outputIndex); observeErr != nil {
		return observeErr
	}
	if repromptOutputIndex >= 0 && *outputIndex <= repromptOutputIndex {
		return cancelCustomerSimulationPatienceWithoutOutput(controller)
	}
	if completeErr := completeCustomerSimulationPatience(controller); completeErr != nil {
		return completeErr
	}
	return errDuplexInputComplete
}

func cancelCustomerSimulationPatienceWithoutOutput(controller *PatienceController) error {
	if err := controller.Cancel(); err != nil {
		return err
	}
	return errors.New("family E patience ended without post-re-prompt product output")
}

func buildCustomerSimulationTranscripts(scenario CustomerScenario, script []CustomerScriptTurn, result DuplexRunResult, facts customerSimulationRecordingFacts) PairedTranscripts {
	return PairedTranscripts{
		Customer: buildCustomerSimulationCustomerTranscript(scenario, script, result),
		Product:  buildCustomerSimulationProductTranscript(scenario, result, facts),
	}
}

func buildCustomerSimulationCustomerTranscript(scenario CustomerScenario, script []CustomerScriptTurn, result DuplexRunResult) []TranscriptEvent {
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
	if scenario.Family != ScenarioFamilyE {
		return customer
	}
	return appendCustomerSimulationRepromptTranscript(customer, result)
}

// appendCustomerSimulationRepromptTranscript records the Family E patience
// re-prompt, if it was delivered, after the scripted customer turns.
func appendCustomerSimulationRepromptTranscript(customer []TranscriptEvent, result DuplexRunResult) []TranscriptEvent {
	for _, input := range result.Input {
		if input.SegmentID != "patience-reprompt-1" {
			continue
		}
		at := input.At
		if len(customer) > 0 && at < customer[len(customer)-1].At {
			at = customer[len(customer)-1].At
		}
		return append(customer, TranscriptEvent{
			ID: "customer-patience-reprompt-1", TurnID: FamilyETurnID, Speaker: TranscriptCustomer,
			Text: FamilyEReprompt(0), At: at, Final: true,
		})
	}
	return customer
}

func buildCustomerSimulationProductTranscript(scenario CustomerScenario, result DuplexRunResult, facts customerSimulationRecordingFacts) []TranscriptEvent {
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
	return product
}

func customerSimulationActionResults(scenario CustomerScenario, product []TranscriptEvent, checkpoints []FilesystemCheckpoint, tools []ToolObservation, process ProcessFacts, facts customerSimulationRecordingFacts) []ActionResult {
	results := make([]ActionResult, 0, len(scenario.Actions))
	processCompleted := process.ExitClassification == duplexExitNormal && process.ChildWaited && process.WaitCount == 1 && process.InputFinished
	sigintTermination := scenario.Family == ScenarioFamilyD && scenario.Termination == TerminationSIGINT
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
		textConfirmed := customerSimulationTextConfirmsAction(action, text)
		completed := processCompleted && !sigintTermination &&
			(action.PartialSideEffectPolicy == PartialSideEffectsForbid || len(toolIDs) > 0) &&
			(!action.Oracle.RequireConfirmation || textConfirmed) &&
			(scenario.Family != ScenarioFamilyC || action.ID != FamilyCImageActionID)
		disposition := DispositionFailed
		reason := "the shipped session did not produce a complete, independently evidenced action"
		if completed {
			disposition = DispositionCompleted
			reason = ""
		}
		if scenario.Family == ScenarioFamilyB && action.ID == FamilyBOriginalActionID && facts.cancelObserved && len(checkpointIDs) > 0 {
			disposition = DispositionCancelled
			reason = "the original response was cancelled by the customer's correction after its side effect was observed"
		}
		if sigintTermination {
			disposition = DispositionCancelled
			reason = "the active response was interrupted by the selected SIGINT termination"
		}
		confirmed := action.Oracle.RequireConfirmation && textConfirmed
		confirmedAt := time.Duration(0)
		if confirmed && index < len(product) {
			confirmedAt = product[index].At
		}
		refs := customerSimulationActionEvidenceRefs(scenario)
		results = append(results, ActionResult{ActionID: action.ID, TurnID: turnID, Confirmed: confirmed, ConfirmedAt: confirmedAt, Disposition: disposition, OutcomeReason: reason, EvidenceRefs: refs, CheckpointIDs: checkpointIDs, ToolObservationIDs: toolIDs})
	}
	return results
}

// customerSimulationTextConfirmsAction reports whether the product text
// contains every fact the action oracle requires.
func customerSimulationTextConfirmsAction(action ActionIntent, text string) bool {
	for _, required := range action.Oracle.RequiredText {
		if !strings.Contains(strings.ToLower(text), strings.ToLower(required)) {
			return false
		}
	}
	return true
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
