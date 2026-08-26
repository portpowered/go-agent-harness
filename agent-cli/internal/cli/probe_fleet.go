package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/probe/fleet"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/spf13/cobra"
)

// ProbeFleetCommand executes every entry in a validated fleet manifest.
// Executor is injectable for hermetic transport and concurrency tests; the
// default executor uses the existing offline probe runner and replay path.
type ProbeFleetCommand struct {
	ManifestPath string
	Replay       string
	JSONOut      bool
	Executor     fleet.EntryExecutor
}

// NewProbeFleetCommand returns the probe fleet command constructor. An
// optional executor replaces the default replay-backed executor, which lets
// callers test transport behavior without network or device dependencies.
func NewProbeFleetCommand(executor ...fleet.EntryExecutor) *ProbeFleetCommand {
	command := &ProbeFleetCommand{}
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
	_ = cmd.MarkFlagRequired("manifest")
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
		executor, err = c.newReplayExecutor(manifest)
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

func (c *ProbeFleetCommand) newReplayExecutor(manifest fleet.Manifest) (fleet.EntryExecutor, error) {
	for _, entry := range manifest.Entries {
		if entry.Transport != fleet.TransportReplay {
			return nil, fmt.Errorf("fleet transport %q is not supported by the offline probe runner; provide an entry executor for live entries", entry.Transport)
		}
	}
	if strings.TrimSpace(c.Replay) == "" {
		return nil, fmt.Errorf("--replay <fixture-path-or-dir> is required for replay fleet entries")
	}
	fixtures, err := loadReplayFixtures(c.Replay)
	if err != nil {
		return nil, err
	}
	for name, fixture := range fixtures {
		if _, err := gatewaytesting.LoadSessionCapture(fixture); err != nil {
			return nil, fmt.Errorf("invalid replay fixture %q (%s): %w", fixture, name, err)
		}
	}
	replayExec := replayExecFunc(fixtures)
	return func(ctx context.Context, entry fleet.Entry) (fleet.EntryOutcome, error) {
		return runReplayFleetEntry(ctx, entry, replayExec)
	}, nil
}

func runReplayFleetEntry(ctx context.Context, entry fleet.Entry, exec probe.ExecFunc) (fleet.EntryOutcome, error) {
	data, err := os.ReadFile(entry.ScenarioPath)
	if err != nil {
		return fleet.EntryOutcome{}, fmt.Errorf("read scenario %q: %w", entry.ScenarioPath, err)
	}
	scenario, err := loadProbeScenario(data)
	if err != nil {
		return fleet.EntryOutcome{}, fmt.Errorf("load scenario %q: %w", entry.ScenarioPath, err)
	}
	if !scenarioMatchesEntry(scenario, entry) {
		return fleet.EntryOutcome{}, fmt.Errorf("scenario %q does not match manifest entry %q", entry.ScenarioPath, entry.ID)
	}

	var output bytes.Buffer
	runner := &probe.Runner{
		Exec:          deadguardExec(exec, probeScenarioDeadline),
		Out:           &output,
		CorpusLookups: []probe.CorpusLookup{replayCorpusLookup{}},
	}
	summary, err := runner.Run(ctx, []probe.Scenario{scenario})
	if err != nil {
		return fleet.EntryOutcome{}, err
	}
	result, err := decodeSingleProbeResult(output.Bytes())
	if err != nil {
		return fleet.EntryOutcome{}, err
	}
	if summary.Total != 1 {
		return fleet.EntryOutcome{}, fmt.Errorf("probe runner returned total %d for fleet entry %q", summary.Total, entry.ID)
	}
	if result.Pass {
		return fleet.EntryOutcome{Pass: true}, nil
	}
	return fleet.EntryOutcome{Err: probeResultError(result)}, nil
}

func scenarioMatchesEntry(scenario probe.Scenario, entry fleet.Entry) bool {
	return strings.TrimSpace(scenario.ID) == entry.ScenarioID || strings.TrimSpace(scenario.Name) == entry.ScenarioID
}

func decodeSingleProbeResult(data []byte) (probe.ScenarioResult, error) {
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var result probe.ScenarioResult
		if err := json.Unmarshal([]byte(line), &result); err != nil {
			return probe.ScenarioResult{}, fmt.Errorf("decode fleet probe result: %w", err)
		}
		if result.Name == "" {
			continue
		}
		return result, nil
	}
	return probe.ScenarioResult{}, errors.New("probe runner returned no scenario result for fleet entry")
}

func probeResultError(result probe.ScenarioResult) error {
	if result.Error != "" {
		return errors.New(result.Error)
	}
	for _, outcome := range result.ScenarioExpectationOutcomes {
		if !outcome.Passed {
			if outcome.Error != "" {
				return errors.New(outcome.Error)
			}
			return fmt.Errorf("probe expectation %d failed", outcome.Index)
		}
	}
	return errors.New("probe expectations failed")
}

func writeFleetLines(out io.Writer, results []fleet.EntryResult) error {
	for _, result := range results {
		status := "pass"
		if !result.Pass {
			status = "fail"
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
