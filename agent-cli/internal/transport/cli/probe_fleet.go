package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/probe/fleet"
	probescenario "github.com/portpowered/go-agent-harness/agent-cli/internal/probe/scenario"
	serviceSession "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	serviceProbes "github.com/portpowered/go-agent-harness/agent-cli/internal/services/probes"
	runtimeReplay "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	"github.com/spf13/cobra"
)

// FleetLiveSessionRunner is the live session seam; tests can replace it without
// changing transport dispatch.
type FleetLiveSessionRunner = fleet.LiveSessionRunner

// ProbeFleetCommand executes every entry in a validated fleet manifest.
// Executor is injectable for hermetic transport and concurrency tests; the
// default executor dispatches replay entries through the existing offline
// probe path and live entries through the existing live session runtime.
type ProbeFleetCommand struct {
	metricsCollector  serviceProbes.MetricsCollector
	replayService     runtimeReplay.Service
	sessionService    serviceSession.SessionService
	ManifestPath      string
	Replay            string
	JSONOut           bool
	Provider          string
	Model             string
	APIKey            string
	BaseURL           string
	Executor          fleet.EntryExecutor
	LiveSessionRunner FleetLiveSessionRunner
}

// NewProbeFleetCommand wires the services and optional executor.
// The executor seam supports runs without network or device access.
func NewProbeFleetCommand(sessionService serviceSession.SessionService, metricsCollector serviceProbes.MetricsCollector, replayService runtimeReplay.Service, executor ...fleet.EntryExecutor) *ProbeFleetCommand {
	command := &ProbeFleetCommand{sessionService: sessionService, metricsCollector: metricsCollector, replayService: replayService}
	if len(executor) > 0 {
		command.Executor = executor[0]
	}
	return command
}

// Generate returns the Cobra command for `agent probe fleet`.
func (c *ProbeFleetCommand) Generate() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "fleet --manifest <file>",
		Short: "Execute every entry in a fleet manifest",
		Long: "Execute every entry in a fleet manifest.\n\nValidate a complete fleet manifest and execute every scenario/transport/repeat entry " +
			"with the manifest's bounded concurrency. Replay entries use the existing offline probe " +
			"runner; pass --replay with a fixture file or directory. The command never starts with a " +
			"partial manifest.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return c.run(cmd)
		},
	}
	cmd.Flags().StringVar(&c.ManifestPath, "manifest", "", "Path to a validated fleet manifest JSON file")
	cmd.Flags().StringVar(&c.Replay, "replay", "", "Replay fixture path or directory for replay transport entries")
	cmd.Flags().BoolVar(&c.JSONOut, "json", false, "Emit one aggregate JSON fleet result instead of human-readable lines")
	cmd.Flags().StringVar(&c.Provider, "provider", "", "Live session provider ID (use grok or openai; config is used when omitted)")
	cmd.Flags().StringVar(&c.Model, "model", "", "Live session model ID (config is used when omitted)")
	cmd.Flags().StringVar(&c.APIKey, "api-key", "", "Live session provider API key (config is used when omitted)")
	cmd.Flags().StringVar(&c.BaseURL, "base-url", "", "Live session provider base URL override")
	requireFlags(cmd, "manifest")
	return cmd
}

func (c *ProbeFleetCommand) run(cmd *cobra.Command) error {
	manifestPath := strings.TrimSpace(c.ManifestPath)
	if manifestPath == "" {
		return fmt.Errorf("--manifest <file> is required")
	}
	manifest, err := fleet.ReadManifest(manifestPath)
	if err != nil {
		return fmt.Errorf("read fleet manifest: %w", err)
	}

	executor := c.Executor
	if executor == nil {
		executor, err = c.newDefaultExecutor(cmd, manifest)
		if err != nil {
			return err
		}
	}
	execution, err := fleet.Execute(cmd.Context(), manifest, executor)
	if err != nil {
		return err
	}
	result := execution.Result()

	if c.JSONOut {
		if err := json.NewEncoder(cmd.OutOrStdout()).Encode(result); err != nil {
			return fmt.Errorf("write fleet JSON result: %w", err)
		}
	} else {
		if err := writeFleetLines(cmd.OutOrStdout(), execution.Results); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "fleet: %d/%d entries passed (%s)\n", result.Passed, result.Total, result.Status); err != nil {
			return fmt.Errorf("write fleet summary: %w", err)
		}
	}
	if result.Failed > 0 {
		return fmt.Errorf("%d of %d fleet entries failed", result.Failed, result.Total)
	}
	return nil
}

func (c *ProbeFleetCommand) newDefaultExecutor(cmd *cobra.Command, manifest fleet.Manifest) (fleet.EntryExecutor, error) {
	runner := c.LiveSessionRunner
	if runner == nil {
		runner = c.runLiveSession
	}
	return fleet.NewDefaultExecutor(cmd.Context(), manifest, fleet.DefaultExecutorConfig{
		ReplayPath: c.Replay,
		Replay:     c.replayService,
		Metrics:    c.metricsCollector,
		Corpus:     probeCorpus(),
		Live: fleet.LiveExecutor{
			Options: serviceSession.Request{
				Provider:      c.Provider,
				Model:         c.Model,
				ModelProvided: cmd.Flags().Changed("model"),
				APIKey:        c.APIKey,
				BaseURL:       c.BaseURL,
				ConfigDir:     commandFlagValue(cmd, "config-dir"),
			},
			Run: runner,
		},
	})
}

func commandFlagValue(cmd *cobra.Command, name string) string {
	for current := cmd; current != nil; current = current.Parent() {
		if flag := current.Flags().Lookup(name); flag != nil {
			return flag.Value.String()
		}
	}
	return ""
}

func (c *ProbeFleetCommand) runLiveSession(ctx context.Context, out io.Writer, request serviceSession.Request, input serviceSession.AudioInput) error {
	if c.sessionService == nil {
		return errors.New("fleet session service is required")
	}
	request.AudioInput = input
	if input.Present {
		request.MaxDuration = probescenario.DefaultDeadline
	}
	return c.sessionService.Run(ctx, out, request)
}

// Fleet and probe result status words shared by the line and JSON renderers.
const (
	probeStatusPass = "pass"
	probeStatusFail = "fail"
)

func writeFleetLines(out io.Writer, results []fleet.EntryResult) error {
	for _, result := range results {
		status := probeStatusPass
		if !result.Pass {
			status = probeStatusFail
		}
		if _, err := fmt.Fprintf(out, "fleet: %s scenario=%s transport=%s repeat=%d id=%s", status, result.ScenarioID, result.Transport, result.RepeatIndex, result.ID); err != nil {
			return fmt.Errorf("write fleet entry %q: %w", result.ID, err)
		}
		if result.Error != "" {
			if _, err := fmt.Fprintf(out, " error=%s", result.Error); err != nil {
				return fmt.Errorf("write fleet entry %q: %w", result.ID, err)
			}
		}
		if _, err := fmt.Fprintln(out); err != nil {
			return fmt.Errorf("write fleet entry %q: %w", result.ID, err)
		}
	}
	return nil
}
