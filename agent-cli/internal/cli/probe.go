package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/spf13/cobra"
)

// probeScenarioDeadline is the deadguard bound applied to every scenario
// execution: a session that never terminates within this window yields a
// failed result with a deadguard indication instead of blocking the runner.
const probeScenarioDeadline = 30 * time.Second

// ProbeCommand is the probe group (parent command); subcommands are wired in routes.go.
type ProbeCommand struct{}

// NewProbeCommand returns the probe group command constructor.
func NewProbeCommand() *ProbeCommand {
	return &ProbeCommand{}
}

// Generate returns the cobra command for the probe group.
func (c *ProbeCommand) Generate() *cobra.Command {
	return &cobra.Command{
		Use:   "probe",
		Short: "Run deterministic offline probes",
		Long: "Run deterministic offline probes against recorded fixtures.\n\n" +
			"Use the run subcommand to execute probe scenarios through the JSONL probe runner without network access.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
}

// ProbeRunCommand runs selected probe scenarios over recorded fixtures.
type ProbeRunCommand struct {
	Scenarios   []string
	Record      string
	Replay      string
	Devices     string
	Provider    string
	Model       string
	APIKey      string
	BaseURL     string
	CaptureTime time.Duration
	ConfigDir   string
	OutPath     string
	SummaryPath string
	JSONOut     bool
	// RecordingRoot is the parent directory for v2 evidence bundles. An empty
	// value creates a run-scoped temporary parent and exposes its path in each
	// result for inspection.
	RecordingRoot string
	// BrowserExecutorMode is deliberately hermetic by default. Real browser
	// execution is admitted only when a v2 scenario explicitly selects it.
	BrowserExecutorMode ProbeScenarioV2BrowserExecutorMode

	deviceRegistry      audio.DeviceRegistry
	deviceProbeExec     DeviceProbeExecFunc
	deviceProbeDeadline time.Duration
	globalFlags         *flags.GlobalFlags
	browserFlags        *flags.BrowserFlags
	browserFactory      WebMCPDoctorFactory
}

// DeviceProbeExecFunc runs one validated scenario against the selected device
// snapshot. It is the narrow command seam used by hermetic command tests; the
// production constructor installs the live registry/WebRTC/session executor.
type DeviceProbeExecFunc func(context.Context, probe.Scenario, audio.DeviceProbeAvailability) (probe.ObservationSnapshot, error)

// NewProbeRunCommand returns the probe run command constructor.
func NewProbeRunCommand(registries ...audio.DeviceRegistry) *ProbeRunCommand {
	registry := newDefaultDeviceRegistry()
	if len(registries) > 0 && registries[0] != nil {
		registry = registries[0]
	}
	command := &ProbeRunCommand{
		deviceRegistry:      registry,
		Provider:            "openai",
		CaptureTime:         deviceProbeDefaultCaptureDuration,
		deviceProbeDeadline: probeScenarioDeadline,
		BrowserExecutorMode: ProbeScenarioV2BrowserExecutorHermetic,
		browserFlags:        flags.NewBrowserFlags(),
		browserFactory:      NewProductionWebMCPDoctorFactory(),
	}
	command.deviceProbeExec = func(ctx context.Context, scenario probe.Scenario, availability audio.DeviceProbeAvailability) (probe.ObservationSnapshot, error) {
		return runDeviceProbeScenario(ctx, scenario, availability, command.deviceRegistry, deviceProbeRuntimeOptions{
			Provider:    command.Provider,
			Model:       command.Model,
			APIKey:      command.APIKey,
			BaseURL:     command.BaseURL,
			CaptureTime: command.CaptureTime,
			ConfigDir:   command.ConfigDir,
		})
	}
	return command
}

// SetGlobalFlags connects probe's real-mode configuration resolution to the
// root command's persistent config directory.
func (c *ProbeRunCommand) SetGlobalFlags(globalFlags *flags.GlobalFlags) {
	if c != nil {
		c.globalFlags = globalFlags
	}
}

// SetBrowserExecutorFactory installs the real browser composition at the
// probe boundary. The factory is ignored while the mode is hermetic.
func (c *ProbeRunCommand) SetBrowserExecutorFactory(factory WebMCPDoctorFactory) {
	if c != nil {
		c.browserFactory = factory
	}
}

// Generate returns the cobra command for probe run.
func (c *ProbeRunCommand) Generate() *cobra.Command {
	if c.BrowserExecutorMode == "" {
		c.BrowserExecutorMode = ProbeScenarioV2BrowserExecutorHermetic
	}
	if c.browserFlags == nil {
		c.browserFlags = flags.NewBrowserFlags()
	}
	cmd := &cobra.Command{
		Use:   "run [scenario-path...]",
		Short: "Run probe scenarios against recorded fixtures or device hardware",
		Long: "Load probe scenarios and execute them through the JSONL probe runner over recorded\n" +
			"session fixtures. Execution never dials the network. One JSON result line per scenario\n" +
			"is written to --out (default stdout) followed by one summary line to --summary (default stderr).\n\n" +
			"For the T2 device tier, pass --devices real to enumerate the shared audio device registry\n" +
			"before execution; hosts without both directions receive a machine-readable SKIP result.\n\n" +
			"The command exits non-zero when any scenario fails.",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return c.run(cmd, args)
		},
	}
	cmd.Flags().StringArrayVar(&c.Scenarios, "scenario", nil, "Scenario file path (repeatable)")
	cmd.Flags().StringVar(&c.Record, "record", "", "Record fixtures to path (recording is unsupported for offline probe runs)")
	cmd.Flags().StringVar(&c.Replay, "replay", "", "Replay fixture path or directory of recorded session fixtures")
	cmd.Flags().StringVar(&c.Devices, "devices", "", "Run the device-tier probe against real audio devices")
	cmd.Flags().StringVar(&c.Provider, "provider", c.Provider, "Realtime session provider for --devices real (openai or grok)")
	cmd.Flags().StringVar(&c.Model, "model", c.Model, "Realtime session model for --devices real")
	cmd.Flags().StringVar(&c.APIKey, "api-key", c.APIKey, "Realtime session API key for --devices real")
	cmd.Flags().StringVar(&c.BaseURL, "base-url", c.BaseURL, "Realtime session WebSocket base URL for --devices real")
	cmd.Flags().DurationVar(&c.CaptureTime, "capture-duration", c.CaptureTime, "Microphone capture duration for --devices real")
	cmd.Flags().StringVar(&c.OutPath, "out", "", "Path for per-scenario JSONL result lines (default stdout)")
	cmd.Flags().StringVar(&c.SummaryPath, "summary", "", "Path for the summary artifact (default stderr)")
	cmd.Flags().BoolVar(&c.JSONOut, "json", false, "Emit pure machine-readable output without human-readable decoration")
	cmd.Flags().StringVar(&c.RecordingRoot, "recording-root", "", "Parent directory for finalized v2 evidence bundles")
	cmd.Flags().StringVar(&c.RecordingRoot, "evidence-root", "", "Alias for --recording-root")
	cmd.Flags().Var(&probeScenarioV2BrowserExecutorModeValue{target: &c.BrowserExecutorMode}, "browser-executor", "Browser executor for probe.scenario.v2: hermetic or real")
	cmd.Flags().Var(&probeScenarioV2BrowserExecutorModeValue{target: &c.BrowserExecutorMode}, "browser-mode", "Alias for --browser-executor")
	registerSessionBrowserFlags(cmd, c.browserFlags)
	return cmd
}

func (c *ProbeRunCommand) run(cmd *cobra.Command, positional []string) error {
	if c.Record != "" {
		return fmt.Errorf("--record is not supported for offline probe runs; use --replay with recorded fixtures")
	}
	if c.Devices != "" {
		if c.Devices != "real" {
			return fmt.Errorf("unsupported --devices value %q; want real", c.Devices)
		}
		if len(probeSelections(positional, c.Scenarios)) == 0 {
			return fmt.Errorf("no probe scenarios selected; pass scenario paths as arguments or repeat --scenario")
		}
		availability, err := audio.ProbeDeviceAvailability(c.deviceRegistry)
		if err != nil {
			return fmt.Errorf("device probe availability: %w", err)
		}
		if availability.Status == audio.DeviceProbeStatusSkip {
			return c.writeDeviceProbeSkip(cmd, positional, availability)
		}
		if configDir, getErr := cmd.Flags().GetString("config-dir"); getErr == nil {
			c.ConfigDir = configDir
		}
		scenarios, err := buildDeviceProbePlan(positional, c.Scenarios)
		if err != nil {
			return err
		}
		return c.runScenarios(cmd, scenarios, deadguardExec(func(ctx context.Context, scenario probe.Scenario) (probe.ObservationSnapshot, error) {
			return c.deviceProbeExec(ctx, scenario, availability)
		}, c.deviceProbeDeadline))
	}
	selections := probeSelections(positional, c.Scenarios)
	if hasV2, err := probeSelectionsContainV2(selections); err != nil {
		return err
	} else if hasV2 {
		return c.runScenarioV2(cmd, selections)
	}
	if strings.TrimSpace(c.Replay) == "" {
		return fmt.Errorf("--replay <fixture-path-or-dir> is required to select recorded fixtures")
	}

	fixtures, err := loadReplayFixtures(c.Replay)
	if err != nil {
		return err
	}

	scenarios, exec, err := buildProbePlan(positional, c.Scenarios, fixtures)
	if err != nil {
		return err
	}

	return c.runScenarios(cmd, scenarios, deadguardExec(exec, probeScenarioDeadline))
}

func probeSelectionsContainV2(selections []string) (bool, error) {
	containsV2 := false
	for _, selection := range selections {
		isV2, err := probeScenarioFileIsV2(selection)
		if err != nil {
			return false, err
		}
		containsV2 = containsV2 || isV2
	}
	return containsV2, nil
}

func (c *ProbeRunCommand) runScenarios(cmd *cobra.Command, scenarios []probe.Scenario, exec probe.ExecFunc) error {
	resultsOut := cmd.OutOrStdout()
	if c.OutPath != "" {
		file, openErr := os.Create(c.OutPath)
		if openErr != nil {
			return fmt.Errorf("open --out %q: %w", c.OutPath, openErr)
		}
		defer file.Close()
		resultsOut = file
	}
	summaryOut := io.Writer(cmd.ErrOrStderr())
	if c.SummaryPath != "" {
		file, openErr := os.Create(c.SummaryPath)
		if openErr != nil {
			return fmt.Errorf("open --summary %q: %w", c.SummaryPath, openErr)
		}
		defer file.Close()
		summaryOut = file
	}

	runner := &probe.Runner{
		Exec:          exec,
		Out:           &resultRouter{results: resultsOut, summary: summaryOut},
		CorpusLookups: []probe.CorpusLookup{replayCorpusLookup{}},
	}
	summary, runErr := runner.Run(cmd.Context(), scenarios)
	if runErr != nil {
		return runErr
	}
	if !c.JSONOut {
		fmt.Fprintf(cmd.ErrOrStderr(), "probe: %d/%d scenarios passed (%s)\n", summary.Passed, summary.Total, summary.Status)
	}
	if summary.Failed > 0 {
		return fmt.Errorf("%d of %d probe scenarios failed", summary.Failed, summary.Total)
	}
	return nil
}

func buildDeviceProbePlan(positional []string, flags []string) ([]probe.Scenario, error) {
	selections := probeSelections(positional, flags)
	if len(selections) == 0 {
		return nil, fmt.Errorf("no probe scenarios selected; pass scenario paths as arguments or repeat --scenario")
	}
	seen := make(map[string]struct{}, len(selections))
	scenarios := make([]probe.Scenario, 0, len(selections))
	for _, selection := range selections {
		resolved, err := resolveProbeSelection(selection)
		if err != nil {
			return nil, err
		}
		for _, scenario := range resolved {
			key := scenario.ID + "\x00" + scenario.Name
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			scenarios = append(scenarios, scenario)
		}
	}
	return scenarios, nil
}

func probeSelections(positional, flags []string) []string {
	raw := append(append([]string{}, positional...), flags...)
	selections := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, selection := range raw {
		if _, exists := seen[selection]; exists {
			continue
		}
		seen[selection] = struct{}{}
		selections = append(selections, selection)
	}
	return selections
}

// resultRouter routes each verbatim runner line either to the results
// destination or, when it decodes as a run summary, to the summary destination.
type resultRouter struct {
	results io.Writer
	summary io.Writer
}

func (r *resultRouter) Write(p []byte) (int, error) {
	var candidate probe.RunSummary
	if json.Unmarshal(p, &candidate) == nil && candidate.Status != "" {
		return r.summary.Write(p)
	}
	return r.results.Write(p)
}

// loadReplayFixtures resolves --replay into named session fixture paths.
func loadReplayFixtures(replay string) (map[string]string, error) {
	info, statErr := os.Stat(replay)
	if statErr != nil {
		return nil, fmt.Errorf("replay fixture %q is missing or unreadable: %w", replay, statErr)
	}
	fixtures := map[string]string{}
	if !info.IsDir() {
		fixtures[fixtureStem(replay)] = replay
		return fixtures, nil
	}
	entries, readErr := os.ReadDir(replay)
	if readErr != nil {
		return nil, fmt.Errorf("read replay fixture directory %q: %w", replay, readErr)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".session.json") {
			continue
		}
		path := filepath.Join(replay, entry.Name())
		fixtures[fixtureStem(path)] = path
	}
	if len(fixtures) == 0 {
		return nil, fmt.Errorf("replay fixture directory %q contains no recorded session fixtures", replay)
	}
	return fixtures, nil
}

func fixtureStem(path string) string {
	base := filepath.Base(path)
	base = strings.TrimSuffix(base, ".session.json")
	return strings.TrimSuffix(base, ".json")
}

// buildProbePlan loads selected scenarios and resolves the fixture-backed
// execution function. Every unknown selection is reported by name.
//
// A selection is resolved in order: (1) a scenario file on disk, (2) an exact
// match against a registered scenario's ID or name, (3) a suite prefix match
// that expands to every registered scenario whose ID extends the selection
// with "-" (e.g. s2s-v6a-error-auth selects both of its cases).
func buildProbePlan(positional []string, flags []string, fixtures map[string]string) ([]probe.Scenario, probe.ExecFunc, error) {
	selections := append(append([]string{}, positional...), flags...)
	if len(selections) == 0 {
		return nil, nil, fmt.Errorf("no probe scenarios selected; pass scenario paths as arguments or repeat --scenario")
	}
	seen := map[string]bool{}
	scenarios := make([]probe.Scenario, 0, len(selections))
	for _, selection := range selections {
		resolved, err := resolveProbeSelection(selection)
		if err != nil {
			return nil, nil, err
		}
		for _, scenario := range resolved {
			key := scenario.ID + "\x00" + scenario.Name
			if seen[key] {
				continue
			}
			seen[key] = true
			scenarios = append(scenarios, scenario)
		}
	}
	for _, fixture := range fixtures {
		validationErrs := gatewaytesting.ValidateSessionCaptureFile(fixture)
		if len(validationErrs) == 0 {
			continue
		}
		messages := make([]string, 0, len(validationErrs))
		for _, validationErr := range validationErrs {
			messages = append(messages, validationErr.Error())
		}
		return nil, nil, fmt.Errorf("invalid replay fixture %q: %s", fixture, strings.Join(messages, "; "))
	}
	return scenarios, replayExecFunc(fixtures), nil
}

// resolveProbeSelection resolves one selection into zero or more scenarios,
// preferring on-disk scenario files over the registered scenario set.
func resolveProbeSelection(selection string) ([]probe.Scenario, error) {
	if _, statErr := os.Stat(selection); statErr == nil {
		scenario, loadErr := loadProbeScenarioFile(selection)
		if loadErr != nil {
			return nil, fmt.Errorf("load probe scenario %q: %w", selection, loadErr)
		}
		return []probe.Scenario{scenario}, nil
	}
	registered := probe.Scenarios()
	for _, scenario := range registered {
		if scenario.ID == selection || scenarioName(scenario) == selection {
			return []probe.Scenario{scenario}, nil
		}
	}
	suite := make([]probe.Scenario, 0)
	for _, scenario := range registered {
		if strings.HasPrefix(scenario.ID, selection+"-") {
			suite = append(suite, scenario)
		}
	}
	sort.Slice(suite, func(i, j int) bool { return suite[i].ID < suite[j].ID })
	if len(suite) > 0 {
		return suite, nil
	}
	return nil, fmt.Errorf("unknown probe scenario %q: no such file and no registered scenario matches", selection)
}

// scenarioDocument is the on-disk scenario JSON envelope.
type scenarioDocument struct {
	ID               string             `json:"id"`
	Name             string             `json:"name"`
	Description      string             `json:"description"`
	Steps            []probeStep        `json:"steps"`
	Expectations     []probeExpectation `json:"expectations"`
	ExpectedBehavior []probeExpectation `json:"expected_behavior"`
	Expected         []probeExpectation `json:"expected"`
}

type probeStep struct {
	Type       string          `json:"type"`
	Text       string          `json:"text"`
	CorpusID   string          `json:"corpus_id"`
	ToolCallID string          `json:"tool_call_id"`
	ToolName   string          `json:"tool_name"`
	Result     json.RawMessage `json:"result"`
	At         int64           `json:"at"`
	Duration   int64           `json:"duration"`
}

type probeExpectation struct {
	Type       string          `json:"type"`
	Text       string          `json:"text"`
	Value      string          `json:"value"`
	Count      int             `json:"count"`
	At         int64           `json:"at"`
	CorpusID   string          `json:"corpus_id"`
	ToolCallID string          `json:"tool_call_id"`
	ToolName   string          `json:"tool_name"`
	Result     json.RawMessage `json:"result"`
}

var stepKindAliases = map[string]probe.StepKind{
	"send_text":        probe.StepSendText,
	"send_audio":       probe.StepSendAudio,
	"send_tool_result": probe.StepSendToolResult,
	"advance_to":       probe.StepAdvanceTo,
	"wait":             probe.StepWait,
	"close":            probe.StepClose,
}

var expectationKindAliases = map[string]probe.ExpectationKind{
	"frame_count":              probe.ExpectFrameCount,
	"frame-count":              probe.ExpectFrameCount,
	"transcript_contains":      probe.ExpectTranscriptContains,
	"transcript-contains":      probe.ExpectTranscriptContains,
	"tool_called":              probe.ExpectToolCalled,
	"tool-called":              probe.ExpectToolCalled,
	"terminal_reason":          probe.ExpectTerminalReason,
	"terminal-reason":          probe.ExpectTerminalReason,
	"terminal_provenance":      probe.ExpectTerminalProvenance,
	"terminal-provenance":      probe.ExpectTerminalProvenance,
	"output_state":             probe.ExpectOutputState,
	"output-state":             probe.ExpectOutputState,
	"terminal_output_state":    probe.ExpectOutputState,
	"terminal-output-state":    probe.ExpectOutputState,
	"latency_within_ticks":     probe.ExpectLatencyWithinTicks,
	"latency-within-ticks":     probe.ExpectLatencyWithinTicks,
	"audio_energy":             probe.ExpectAudioEnergy,
	"audio-energy":             probe.ExpectAudioEnergy,
	"tool_result_delivered":    probe.ExpectToolResultDelivered,
	"tool-result-delivered":    probe.ExpectToolResultDelivered,
	"tool_result_discarded":    probe.ExpectToolResultDiscarded,
	"tool-result-discarded":    probe.ExpectToolResultDiscarded,
	"no_orphaned_tool_result":  probe.ExpectNoOrphanedToolResult,
	"no-orphaned-tool-result":  probe.ExpectNoOrphanedToolResult,
	"buffer_disposition":       probe.ExpectBufferDisposition,
	"buffer-disposition":       probe.ExpectBufferDisposition,
	"barge_in_cancel_once":     probe.ExpectBargeInCancelOnce,
	"barge-in-cancel-once":     probe.ExpectBargeInCancelOnce,
	"message_counts_reconcile": probe.ExpectMessageCountsReconcile,
	"message-counts-reconcile": probe.ExpectMessageCountsReconcile,
	"response_cancel":          probe.ExpectResponseCancel,
	"response-cancel":          probe.ExpectResponseCancel,
}

func measurableExpectationKind(name string) (probe.ExpectationKind, bool) {
	kind, ok := expectationKindAliases[strings.ToLower(strings.TrimSpace(name))]
	return kind, ok
}

// loadProbeScenario parses a scenario JSON document into a validated
// probe.Scenario. Expectations use the runner's measurable vocabulary.
func loadProbeScenario(data []byte) (probe.Scenario, error) {
	var document scenarioDocument
	if err := json.Unmarshal(data, &document); err != nil {
		return probe.Scenario{}, fmt.Errorf("malformed scenario JSON: %w", err)
	}
	scenario := probe.Scenario{ID: document.ID, Name: document.Name, Description: document.Description}
	if len(document.Steps) == 0 {
		return probe.Scenario{}, fmt.Errorf("scenario must contain at least one step")
	}
	for _, raw := range document.Steps {
		kind, ok := stepKindAliases[raw.Type]
		if !ok {
			return probe.Scenario{}, fmt.Errorf("unknown step variant %q", raw.Type)
		}
		step := probe.Step{Type: kind, Kind: kind, Text: raw.Text, CorpusID: raw.CorpusID,
			ToolCallID: raw.ToolCallID, ToolName: raw.ToolName, Result: raw.Result,
			At: probe.LogicalTime(raw.At), Time: probe.LogicalTime(raw.At),
			Duration: probe.LogicalTime(raw.Duration)}
		scenario.Steps = append(scenario.Steps, step)
	}
	expectations := document.Expectations
	if expectations == nil {
		expectations = document.ExpectedBehavior
	}
	if expectations == nil {
		expectations = document.Expected
	}
	if len(expectations) == 0 {
		return probe.Scenario{}, fmt.Errorf("at least one expected behavior is required")
	}
	for _, raw := range expectations {
		kind, ok := measurableExpectationKind(raw.Type)
		if !ok {
			return probe.Scenario{}, fmt.Errorf("unknown expectation variant %q", raw.Type)
		}
		expectation := probe.ExpectedBehavior{
			Type: kind, Kind: kind, Text: raw.Text, Value: raw.Value, Count: raw.Count,
			At: probe.LogicalTime(raw.At), Time: probe.LogicalTime(raw.At), HasAt: raw.At != 0,
			CorpusID: raw.CorpusID, ToolCallID: raw.ToolCallID, ToolName: raw.ToolName,
			Result: raw.Result,
		}
		scenario.Expectations = append(scenario.Expectations, expectation)
	}
	scenario.Expected = scenario.Expectations
	scenario.ExpectedBehavior = scenario.Expectations
	if err := scenario.Validate(replayCorpusLookup{}); err != nil {
		return probe.Scenario{}, err
	}
	return scenario, nil
}

// observationFromSessionCapture validates and derives the probe evidence from
// one captured session. Replay and live executions use the same observation
// contract; the live path passes its freshly recorded capture here without
// replacing its provider-produced audio frames.
func observationFromSessionCapture(ctx context.Context, scenario probe.Scenario, capture gatewaytesting.SessionCapture, sourcePath string, validateSource bool) (probe.ObservationSnapshot, error) {
	var (
		report gatewaytesting.SessionReplayProbeReport
		err    error
	)
	if validateSource {
		report, err = gatewaytesting.RunSessionReplayProbe(ctx, sourcePath)
	} else {
		report, err = gatewaytesting.RunSessionReplayProbeFromCapture(ctx, capture)
	}
	if err != nil {
		return probe.ObservationSnapshot{}, err
	}
	observation := probe.ObservationSnapshot{
		FrameCount:     len(report.Observations),
		ObservedTick:   probe.LogicalTime(report.OutboundTicks),
		TerminalReason: report.Provenance,
	}
	observation.HasObservedTick = true
	if report.EndsWithDisconnect {
		observation.TerminalReason = "disconnect"
	}
	if classification := replayErrorClassificationFromCapture(capture); classification != "" {
		observation.TerminalReason = "error:" + classification
	}
	if scenarioDeclaresTerminalMetadata(scenario) {
		terminalReason, terminalProvenance, outputState := replayTerminalTriple(capture)
		observation.TerminalReason = terminalReason
		observation.TerminalProvenance = terminalProvenance
		observation.OutputState = outputState
	}
	observation.Transcript = replayTranscriptFromCapture(capture)
	if deriveErr := deriveToolResultObservationFromCapture(capture, &observation); deriveErr != nil {
		return probe.ObservationSnapshot{}, deriveErr
	}
	if deriveErr := deriveBargeInObservationFromCapture(capture, &observation); deriveErr != nil {
		return probe.ObservationSnapshot{}, deriveErr
	}
	if deriveErr := deriveResponseCancelObservationFromCapture(capture, &observation); deriveErr != nil {
		return probe.ObservationSnapshot{}, deriveErr
	}
	observation.BufferDisposition = replayBufferDispositionFromCapture(capture)
	if scenarioDeclaresMetricsReconciliation(scenario) {
		metricsSeries, metricsErr := collectReplayMetricsEvidence(ctx, sourcePath, scenarioSendText(scenario))
		if metricsErr != nil {
			return probe.ObservationSnapshot{}, fmt.Errorf("collect metrics evidence: %w", metricsErr)
		}
		if scenario.ID == probe.ScenarioIDS2SV7AMetricsModalityOvercount {
			injectMetricsOvercount(metricsSeries)
		}
		observation.Metrics = metricsSeries
	}
	return observation, nil
}

// deriveResponseCancelObservationFromCapture scans the capture for
// RESPONSE.CANCEL frames on the outbound client-to-provider path and fills
// the observation's barge-in cancel fields. A frame's logical tick is its
// 1-based ordinal among all client-to-server frames, matching the replay
// probe's outbound tick counting. The first observed cancel wins; later
// duplicates do not move the recorded tick.
func deriveResponseCancelObservationFromCapture(capture gatewaytesting.SessionCapture, observation *probe.ObservationSnapshot) error {
	tick := probe.LogicalTime(0)
	for _, record := range capture.Records {
		if record.Direction != gatewaytesting.DirectionClientToServer {
			continue
		}
		tick++
		if !observation.HasInterruptTick && isInputAudioAppendEventType(record.Type) {
			observation.HasInterruptTick = true
			observation.InterruptTick = tick
		}
		if observation.HasResponseCancel || !isResponseCancelEventType(record.Type) {
			continue
		}
		observation.HasResponseCancel = true
		observation.ResponseCancelTick = tick
	}
	return nil
}

func isInputAudioAppendEventType(eventType string) bool {
	return eventType == "input_audio_buffer.append"
}

// isResponseCancelEventType reports whether a fixture event type encodes a
// RESPONSE.CANCEL on either wire spelling: the raw provider websocket type or
// the stream-message type.
func isResponseCancelEventType(eventType string) bool {
	switch eventType {
	case "response.cancel", "RESPONSE.CANCEL":
		return true
	default:
		return false
	}
}

// scenarioDeclaresTerminalMetadata reports whether the scenario asks for the
// provider-authored terminal triple. Keeping this opt-in preserves the
// established terminal-reason vocabulary for older probe scenarios whose
// fixture provenance is their intentional fallback observation.
func scenarioDeclaresTerminalMetadata(scenario probe.Scenario) bool {
	expectationSets := [][]probe.ExpectedBehavior{scenario.Expectations, scenario.ExpectedBehavior, scenario.Expected}
	for _, expectations := range expectationSets {
		for _, expectation := range expectations {
			kind := expectation.Type
			if kind == "" {
				kind = expectation.Kind
			}
			switch kind {
			case probe.ExpectTerminalProvenance, probe.ExpectOutputState:
				return true
			}
		}
	}
	return false
}

// replayTerminalTriple derives the stable probe-surface terminal vocabulary
// from a sanitized provider-wire fixture. The provider-close transport seam
// is exposed as "disconnect" at the probe layer; explicit response.done is
// exposed as "complete". Output state is based on whether a non-empty output
// delta was observed before the terminal boundary.
func replayTerminalTriple(capture gatewaytesting.SessionCapture) (reason, provenance, outputState string) {
	hasOutput, hasCompletion := false, false
	for _, record := range capture.Records {
		if record.Direction != gatewaytesting.DirectionServerToClient {
			continue
		}
		switch record.Type {
		case "response.text.delta", "response.output_text.delta", "response.audio_transcript.delta", "response.audio.delta",
			"response.output_audio.delta":
			var payload struct {
				Delta string `json:"delta"`
			}
			if json.Unmarshal(replayRecordPayload(record), &payload) == nil && payload.Delta != "" {
				hasOutput = true
			}
		case "response.done":
			hasCompletion = true
		}
	}

	if classification := replayErrorClassificationFromCapture(capture); classification != "" {
		return "error:" + classification, "provider", replayOutputState(hasOutput)
	}
	if capture.EndsWithDisconnect {
		return "disconnect", "provider", replayOutputState(hasOutput)
	}
	if hasCompletion {
		return "complete", "provider", "complete"
	}
	return "", "", ""
}

func replayOutputState(hasOutput bool) string {
	if hasOutput {
		return "partial"
	}
	return "none"
}

// observable disposition of the buffered input audio: an acknowledged commit
// or an explicit discard. The empty string means the capture ends with the
// buffer uncommitted, which buffer-disposition expectations treat as a
// failure.
func replayBufferDispositionFromCapture(capture gatewaytesting.SessionCapture) string {
	for _, record := range capture.Records {
		if record.Direction != gatewaytesting.DirectionServerToClient {
			continue
		}
		switch record.Type {
		case "input_audio_buffer.committed":
			return probe.BufferDispositionCommitted
		case "input_audio_buffer.discarded":
			return probe.BufferDispositionDiscarded
		}
	}
	return ""
}

// deriveToolResultObservationFromCapture scans the capture for tool-call
// lifecycle events and fills the observation's barge-in/tool-result fields:
// issued calls (server function_call_arguments.done), delivered results
// (client conversation.item.create carrying a function_call_output), and
// explicitly discarded results (client tool.result.discarded events).
func deriveToolResultObservationFromCapture(capture gatewaytesting.SessionCapture, observation *probe.ObservationSnapshot) error {
	for _, record := range capture.Records {
		var payload struct {
			CallID string `json:"call_id"`
			Item   struct {
				Type   string `json:"type"`
				CallID string `json:"call_id"`
			} `json:"item"`
		}
		_ = json.Unmarshal(replayRecordPayload(record), &payload)
		switch {
		case record.Direction == gatewaytesting.DirectionServerToClient &&
			record.Type == "response.function_call_arguments.done" && payload.CallID != "":
			observation.ToolCalls = append(observation.ToolCalls, payload.CallID)
		case record.Direction == gatewaytesting.DirectionClientToServer &&
			record.Type == "conversation.item.create" &&
			payload.Item.Type == "function_call_output" && payload.Item.CallID != "":
			observation.ToolResultsDelivered = append(observation.ToolResultsDelivered, payload.Item.CallID)
		case record.Direction == gatewaytesting.DirectionClientToServer &&
			record.Type == "tool.result.discarded" && payload.CallID != "":
			observation.ToolResultsDiscarded = append(observation.ToolResultsDiscarded, payload.CallID)
		}
	}
	return nil
}

// deriveBargeInObservationFromCapture scans the capture for the
// repeated-barge-in lifecycle (s2s v3c) and fills the observation's
// reconciliation evidence: committed user turns (client
// input_audio_buffer.commit or a client conversation.item.create carrying a
// message item), created responses, delivered assistant turns (response.done
// on an uninterrupted response), cancellation events (client response.cancel),
// deltas leaking after their response was cancelled or outside any live
// response, and any response still streaming when the capture ends.
func deriveBargeInObservationFromCapture(capture gatewaytesting.SessionCapture, observation *probe.ObservationSnapshot) error {
	inFlight, cancelled := false, false
	for _, record := range capture.Records {
		switch {
		case record.Direction == gatewaytesting.DirectionClientToServer &&
			record.Type == "input_audio_buffer.commit":
			observation.UserTurnsCommitted++
		case record.Direction == gatewaytesting.DirectionClientToServer &&
			record.Type == "conversation.item.create":
			var payload struct {
				Item struct {
					Type string `json:"type"`
				} `json:"item"`
			}
			if json.Unmarshal(replayRecordPayload(record), &payload) == nil && payload.Item.Type == "message" {
				observation.UserTurnsCommitted++
			}
		case record.Direction == gatewaytesting.DirectionServerToClient &&
			record.Type == "response.created":
			observation.ResponsesCreated++
			inFlight, cancelled = true, false
		case record.Direction == gatewaytesting.DirectionServerToClient &&
			isTranscriptDeltaEventType(record.Type):
			if !inFlight || cancelled {
				observation.PostCancelDeltas++
			}
		case record.Direction == gatewaytesting.DirectionClientToServer &&
			record.Type == "response.cancel":
			observation.ResponseCancels++
			if !inFlight || cancelled {
				observation.SpuriousCancels++
			} else {
				observation.ResponsesCancelled++
				cancelled = true
			}
		case record.Direction == gatewaytesting.DirectionServerToClient &&
			record.Type == "response.done":
			if inFlight && !cancelled {
				observation.AssistantTurnsDelivered++
			}
			inFlight, cancelled = false, false
		}
	}
	observation.InFlightAtEnd = inFlight
	return nil
}

// isTranscriptDeltaEventType reports whether the wire event carries one
// streamed transcript delta of an in-flight response.
func isTranscriptDeltaEventType(eventType string) bool {
	switch eventType {
	case "response.text.delta", "response.audio_transcript.delta",
		"response.output_text.delta", "response.output_audio_transcript.delta":
		return true
	default:
		return false
	}
}

// replayErrorClassificationFromCapture classifies the first server-to-client
// failure record through the established provider error taxonomy. A
// server-to-client frame whose type carries the "malformed." prefix encodes an
// unparseable provider response and classifies as invalid_request — the same
// taxonomy class the gateway assigns when a live session parser rejects a
// provider event. Well-formed "error" records classify via their wire error
// type/code. It returns the empty string when the capture records no provider
// error or malformed frame, so healthy sessions keep their
// disconnect/provenance terminal reason.
func replayErrorClassificationFromCapture(capture gatewaytesting.SessionCapture) string {
	for _, record := range capture.Records {
		if record.Direction != gatewaytesting.DirectionServerToClient {
			continue
		}
		if strings.HasPrefix(record.Type, "malformed.") {
			return providers.ErrorClassInvalidRequest
		}
		if record.Type != "error" {
			continue
		}
		var payload struct {
			Error struct {
				Type string `json:"type"`
				Code string `json:"code"`
			} `json:"error"`
		}
		if json.Unmarshal(replayRecordPayload(record), &payload) != nil {
			continue
		}
		if classification := providers.SessionErrorClassification(payload.Error.Type, payload.Error.Code); classification != "" {
			return classification
		}
	}
	return ""
}

// deadguardExec bounds one scenario execution by a wall-clock deadline so a
// hung session yields a failed result carrying a deadguard indication instead
// of blocking the runner.
func deadguardExec(exec probe.ExecFunc, deadline time.Duration) probe.ExecFunc {
	return func(ctx context.Context, scenario probe.Scenario) (probe.ObservationSnapshot, error) {
		bounded, cancel := context.WithTimeout(ctx, deadline)
		defer cancel()
		type outcome struct {
			snapshot probe.ObservationSnapshot
			err      error
		}
		done := make(chan outcome, 1)
		go func() {
			snapshot, execErr := exec(bounded, scenario)
			done <- outcome{snapshot: snapshot, err: execErr}
		}()
		select {
		case result := <-done:
			return result.snapshot, result.err
		case <-bounded.Done():
			return probe.ObservationSnapshot{}, fmt.Errorf(
				"deadguard: scenario %q exceeded its %s deadline: %w",
				scenarioName(scenario), deadline, bounded.Err())
		}
	}
}

func scenarioName(scenario probe.Scenario) string {
	if scenario.Name != "" {
		return scenario.Name
	}
	return scenario.ID
}
