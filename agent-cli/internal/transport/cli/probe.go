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

	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	serviceDevices "github.com/portpowered/go-agent-harness/agent-cli/internal/services/devices"
	serviceprobes "github.com/portpowered/go-agent-harness/agent-cli/internal/services/probes"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/spf13/cobra"
)

// probeScenarioDeadline is the deadguard bound applied to every scenario
// execution: a session that never terminates within this window yields a
// failed result with a deadguard indication instead of blocking the runner.
const probeScenarioDeadline = 30 * time.Second

// ProbeCommand is the probe group (parent command); subcommands are wired in core_router.go.
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
		Example: "  yui probe run ./scenario.json --replay ./capture.session.json",
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

	deviceService       serviceDevices.DeviceService
	deviceProbeService  serviceDevices.DeviceProbeService
	deviceProbeExec     DeviceProbeExecFunc
	deviceProbeDeadline time.Duration
	globalFlags         *flags.GlobalFlags
	browserFlags        *flags.BrowserFlags
	browserFactory      WebMCPDoctorFactory
	metricsCollector    serviceprobes.MetricsCollector
}

// DeviceProbeExecFunc runs one validated scenario against the selected device
// snapshot. It is the narrow command seam used by hermetic command tests; the
// production constructor installs the live registry/WebRTC/session executor.
type DeviceProbeExecFunc func(context.Context, probe.Scenario, serviceDevices.DeviceProbeAvailability) (probe.ObservationSnapshot, error)

// NewProbeRunCommandWithDeviceService constructs the probe transport with the
// injected device service while retaining the gateway registry only for the
// runtime's private device lease boundary.
func NewProbeRunCommandWithDeviceService(service serviceDevices.DeviceService, probeService serviceDevices.DeviceProbeService, metricsCollector serviceprobes.MetricsCollector) *ProbeRunCommand {
	return newProbeRunCommand(service, probeService, metricsCollector)
}

func newProbeRunCommand(service serviceDevices.DeviceService, probeService serviceDevices.DeviceProbeService, metricsCollector serviceprobes.MetricsCollector) *ProbeRunCommand {
	command := &ProbeRunCommand{
		deviceService:       service,
		deviceProbeService:  probeService,
		Provider:            "openai",
		CaptureTime:         serviceDevices.DefaultDeviceProbeCaptureDuration,
		deviceProbeDeadline: probeScenarioDeadline,
		BrowserExecutorMode: ProbeScenarioV2BrowserExecutorHermetic,
		browserFlags:        flags.NewBrowserFlags(),
		browserFactory:      NewProductionWebMCPDoctorFactory(),
	}
	command.metricsCollector = metricsCollector
	command.deviceProbeExec = func(ctx context.Context, scenario probe.Scenario, _ serviceDevices.DeviceProbeAvailability) (probe.ObservationSnapshot, error) {
		if command.deviceProbeService == nil {
			return probe.ObservationSnapshot{}, fmt.Errorf("device probe service is not configured")
		}
		return command.deviceProbeService.Run(ctx, serviceDevices.DeviceProbeRequest{
			Scenario:    scenario,
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
		availability, err := c.probeDeviceAvailability(cmd.Context())
		if err != nil {
			return fmt.Errorf("device probe availability: %w", err)
		}
		if availability.Status == serviceDevices.DeviceProbeStatusSkip {
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

	scenarios, exec, err := buildProbePlan(positional, c.Scenarios, fixtures, c.metricsCollector)
	if err != nil {
		return err
	}

	return c.runScenarios(cmd, scenarios, deadguardExec(exec, probeScenarioDeadline))
}

func (c *ProbeRunCommand) probeDeviceAvailability(ctx context.Context) (serviceDevices.DeviceProbeAvailability, error) {
	if c.deviceService == nil {
		return serviceDevices.DeviceProbeAvailability{}, fmt.Errorf("device service is not configured")
	}
	availability, err := c.deviceService.ProbeAvailability(ctx)
	if err != nil {
		return serviceDevices.DeviceProbeAvailability{}, err
	}
	return availability, nil
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
func buildProbePlan(positional []string, flags []string, fixtures map[string]string, collectors ...serviceprobes.MetricsCollector) ([]probe.Scenario, probe.ExecFunc, error) {
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
	return scenarios, replayExecFunc(fixtures, collectors...), nil
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
