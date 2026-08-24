package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/spf13/cobra"
)

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
	OutPath     string
	SummaryPath string
	JSONOut     bool
}

// NewProbeRunCommand returns the probe run command constructor.
func NewProbeRunCommand() *ProbeRunCommand {
	return &ProbeRunCommand{}
}

// Generate returns the cobra command for probe run.
func (c *ProbeRunCommand) Generate() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run [scenario-path...]",
		Short: "Run probe scenarios against recorded fixtures",
		Long: "Load probe scenarios and execute them through the JSONL probe runner over recorded\n" +
			"session fixtures. Execution never dials the network. One JSON result line per scenario\n" +
			"is written to --out (default stdout) followed by one summary line to --summary (default stderr).\n\n" +
			"The command exits non-zero when any scenario fails.",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return c.run(cmd, args)
		},
	}
	cmd.Flags().StringArrayVar(&c.Scenarios, "scenario", nil, "Scenario file path (repeatable)")
	cmd.Flags().StringVar(&c.Record, "record", "", "Record fixtures to path (recording is unsupported for offline probe runs)")
	cmd.Flags().StringVar(&c.Replay, "replay", "", "Replay fixture path or directory of recorded session fixtures")
	cmd.Flags().StringVar(&c.OutPath, "out", "", "Path for per-scenario JSONL result lines (default stdout)")
	cmd.Flags().StringVar(&c.SummaryPath, "summary", "", "Path for the summary artifact (default stderr)")
	cmd.Flags().BoolVar(&c.JSONOut, "json", false, "Emit pure machine-readable output without human-readable decoration")
	return cmd
}

func (c *ProbeRunCommand) run(cmd *cobra.Command, positional []string) error {
	if c.Record != "" {
		return fmt.Errorf("--record is not supported for offline probe runs; use --replay with recorded fixtures")
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
		Exec: exec,
		Out:  &resultRouter{results: resultsOut, summary: summaryOut},
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
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
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
func buildProbePlan(positional []string, flags []string, fixtures map[string]string) ([]probe.Scenario, probe.ExecFunc, error) {
	selections := append(append([]string{}, positional...), flags...)
	if len(selections) == 0 {
		return nil, nil, fmt.Errorf("no probe scenarios selected; pass scenario paths as arguments or repeat --scenario")
	}
	seen := map[string]bool{}
	scenarios := make([]probe.Scenario, 0, len(selections))
	for _, selection := range selections {
		if seen[selection] {
			continue
		}
		seen[selection] = true
		if scenario, ok := lookupRegisteredScenario(selection); ok {
			scenarios = append(scenarios, scenario)
			continue
		}
		data, readErr := os.ReadFile(selection)
		if readErr != nil {
			return nil, nil, fmt.Errorf("unknown probe scenario %q: %w", selection, readErr)
		}
		scenario, loadErr := loadProbeScenario(data)
		if loadErr != nil {
			return nil, nil, fmt.Errorf("load probe scenario %q: %w", selection, loadErr)
		}
		scenarios = append(scenarios, scenario)
	}
	for _, fixture := range fixtures {
		if _, err := gatewaytesting.LoadSessionCapture(fixture); err != nil {
			return nil, nil, fmt.Errorf("invalid replay fixture %q: %w", fixture, err)
		}
	}
	return scenarios, replayExecFunc(fixtures), nil
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
	"frame_count":          probe.ExpectFrameCount,
	"frame-count":          probe.ExpectFrameCount,
	"transcript_contains":  probe.ExpectTranscriptContains,
	"transcript-contains":  probe.ExpectTranscriptContains,
	"tool_called":          probe.ExpectToolCalled,
	"tool-called":          probe.ExpectToolCalled,
	"terminal_reason":      probe.ExpectTerminalReason,
	"terminal-reason":      probe.ExpectTerminalReason,
	"latency_within_ticks": probe.ExpectLatencyWithinTicks,
	"latency-within-ticks": probe.ExpectLatencyWithinTicks,
	"audio_energy":         probe.ExpectAudioEnergy,
	"audio-energy":         probe.ExpectAudioEnergy,
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
	if err := scenario.Validate(); err != nil {
		return probe.Scenario{}, err
	}
	return scenario, nil
}

// replayExecFunc returns a network-free ExecFunc that replays the recorded
// session fixture matching the scenario name or ID.
func replayExecFunc(fixtures map[string]string) probe.ExecFunc {
	return func(ctx context.Context, scenario probe.Scenario) (probe.ObservationSnapshot, error) {
		fixture, ok := fixtures[scenario.Name]
		if !ok {
			fixture, ok = fixtures[scenario.ID]
		}
		if !ok && len(fixtures) == 1 {
			for _, only := range fixtures {
				fixture, ok = only, true
			}
		}
		if !ok {
			return probe.ObservationSnapshot{}, fmt.Errorf("no recorded fixture matches scenario %q", scenarioName(scenario))
		}
		report, err := gatewaytesting.RunSessionReplayProbe(ctx, fixture)
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
		return observation, nil
	}
}

func scenarioName(scenario probe.Scenario) string {
	if scenario.Name != "" {
		return scenario.Name
	}
	return scenario.ID
}

// lookupRegisteredScenario resolves a selection against the live probe
// scenario registry by ID or name, so registered scenarios can be run
// without an on-disk scenario document.
func lookupRegisteredScenario(selection string) (probe.Scenario, bool) {
	trimmed := strings.TrimSpace(selection)
	if trimmed == "" {
		return probe.Scenario{}, false
	}
	for _, scenario := range probe.LiveScenarioRegistry().Snapshot() {
		if scenario.ID == trimmed || scenario.Name == trimmed {
			return scenario, true
		}
	}
	return probe.Scenario{}, false
}
