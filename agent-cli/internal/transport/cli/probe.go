package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/probe/replay"
	probescenario "github.com/portpowered/go-agent-harness/agent-cli/internal/probe/scenario"
	serviceDevices "github.com/portpowered/go-agent-harness/agent-cli/internal/services/devices"
	serviceprobes "github.com/portpowered/go-agent-harness/agent-cli/internal/services/probes"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
	runtimeReplay "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	"github.com/spf13/cobra"
)

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
	replayService       runtimeReplay.Service
}

// DeviceProbeExecFunc runs one validated scenario against the selected device
// snapshot. It is the narrow command seam used by hermetic command tests; the
// production constructor installs the live registry/WebRTC/session executor.
type DeviceProbeExecFunc func(context.Context, probe.Scenario, serviceDevices.DeviceProbeAvailability) (probe.ObservationSnapshot, error)

// NewProbeRunCommandWithDeviceService constructs the probe transport with the
// injected device service while retaining the gateway registry only for the
// runtime's private device lease boundary.
func NewProbeRunCommandWithDeviceService(service serviceDevices.DeviceService, probeService serviceDevices.DeviceProbeService, metricsCollector serviceprobes.MetricsCollector, replayService runtimeReplay.Service) *ProbeRunCommand {
	return newProbeRunCommand(service, probeService, metricsCollector, replayService)
}

func newProbeRunCommand(service serviceDevices.DeviceService, probeService serviceDevices.DeviceProbeService, metricsCollector serviceprobes.MetricsCollector, replayService runtimeReplay.Service) *ProbeRunCommand {
	command := &ProbeRunCommand{
		deviceService:       service,
		deviceProbeService:  probeService,
		Provider:            "openai",
		CaptureTime:         serviceDevices.DefaultDeviceProbeCaptureDuration,
		deviceProbeDeadline: probescenario.DefaultDeadline,
		BrowserExecutorMode: ProbeScenarioV2BrowserExecutorHermetic,
		browserFlags:        flags.NewBrowserFlags(),
		browserFactory:      NewProductionWebMCPDoctorFactory(),
		replayService:       replayService,
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
		return c.runDevices(cmd, positional)
	}
	selections := probescenario.Selections(positional, c.Scenarios)
	if hasV2, err := probescenario.ContainsV2(selections); err != nil {
		return err
	} else if hasV2 {
		return c.runScenarioV2(cmd, selections)
	}
	return c.runReplay(cmd, selections)
}

func (c *ProbeRunCommand) runReplay(cmd *cobra.Command, selections []string) error {
	if strings.TrimSpace(c.Replay) == "" {
		return fmt.Errorf("--replay <fixture-path-or-dir> is required to select recorded fixtures")
	}
	fixtures, err := replay.LoadFixtures(c.Replay)
	if err != nil {
		return err
	}
	executor := replay.Executor{Service: c.replayService, Fixtures: fixtures, Metrics: c.metricsCollector, Corpus: probeCorpus()}
	scenarios, exec, err := probescenario.ReplayPlan(cmd.Context(), selections, executor)
	if err != nil {
		return err
	}
	return c.runScenarios(cmd, scenarios, probescenario.Deadguard(exec, probescenario.DefaultDeadline))
}

func (c *ProbeRunCommand) runDevices(cmd *cobra.Command, positional []string) error {
	if c.Devices != "real" {
		return fmt.Errorf("unsupported --devices value %q; want real", c.Devices)
	}
	selections := probescenario.Selections(positional, c.Scenarios)
	if len(selections) == 0 {
		return errors.New(probescenario.NoSelectionMessage)
	}
	availability, err := c.probeDeviceAvailability(cmd.Context())
	if err != nil {
		return fmt.Errorf("device probe availability: %w", err)
	}
	if availability.Status == serviceDevices.DeviceProbeStatusSkip {
		return c.writeDeviceProbeSkip(cmd, selections, availability)
	}
	if configDir, getErr := cmd.Flags().GetString("config-dir"); getErr == nil {
		c.ConfigDir = configDir
	}
	scenarios, err := probescenario.ResolveAll(selections)
	if err != nil {
		return err
	}
	return c.runScenarios(cmd, scenarios, probescenario.Deadguard(func(ctx context.Context, scenario probe.Scenario) (probe.ObservationSnapshot, error) {
		return c.deviceProbeExec(ctx, scenario, availability)
	}, c.deviceProbeDeadline))
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

func (c *ProbeRunCommand) runScenarios(cmd *cobra.Command, scenarios []probe.Scenario, exec probe.ExecFunc) (err error) {
	resultsOut, summaryOut, closeOutputs, err := c.openProbeOutputs(cmd)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := closeOutputs(); err == nil {
			err = closeErr
		}
	}()
	runner := probescenario.NewRunner(exec, &probescenario.ResultRouter{Results: resultsOut, Summary: summaryOut})
	summary, err := runner.Run(cmd.Context(), scenarios)
	if err != nil {
		return err
	}
	return c.reportProbeSummary(cmd, summary)
}

// probeCorpus resolves committed audio corpus files from the process working
// directory.
func probeCorpus() replay.Corpus {
	return replay.Corpus{WorkingDir: os.Getwd} //nolint:forbidigo // The CLI transport is the host boundary that injects the working directory.
}
