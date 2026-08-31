package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/probe/fleet"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/spf13/cobra"
)

// FleetLiveSessionRunner is the production session-runtime seam used by live
// fleet entries. Tests may replace it with a recorder-backed function without
// changing the command's transport dispatch contract.
type FleetLiveSessionRunner func(context.Context, io.Writer, services.SessionRunOptions, services.SessionAudioInput) error

// ProbeFleetCommand executes every entry in a validated fleet manifest.
// Executor is injectable for hermetic transport and concurrency tests; the
// default executor dispatches replay entries through the existing offline
// probe path and live entries through the existing live session runtime.
type ProbeFleetCommand struct {
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
		// SilenceErrors: cmd/agent's main.go prints "Error: %s" for every
		// non-nil error returned from Execute(). Without this, Cobra also
		// prints its own "Error: %s" first, so a probe fleet failure showed
		// up twice.
		SilenceErrors: true,
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
	var replayExecutor fleet.EntryExecutor
	var liveExecutor fleet.EntryExecutor
	var err error
	if manifestHasTransport(manifest, fleet.TransportReplay) {
		replayExecutor, err = c.newReplayExecutor(manifest)
		if err != nil {
			return nil, err
		}
	}
	if manifestHasTransport(manifest, fleet.TransportLive) {
		liveExecutor = c.newLiveExecutor(cmd)
	}
	return func(ctx context.Context, entry fleet.Entry) (fleet.EntryOutcome, error) {
		switch entry.Transport {
		case fleet.TransportReplay:
			return replayExecutor(ctx, entry)
		case fleet.TransportLive:
			return liveExecutor(ctx, entry)
		default:
			return fleet.EntryOutcome{}, fmt.Errorf("fleet entry %q has unsupported transport %q", entry.ID, entry.Transport)
		}
	}, nil
}

func manifestHasTransport(manifest fleet.Manifest, want fleet.Transport) bool {
	for _, entry := range manifest.Entries {
		if entry.Transport == want {
			return true
		}
	}
	return false
}

func (c *ProbeFleetCommand) newReplayExecutor(manifest fleet.Manifest) (fleet.EntryExecutor, error) {
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

func (c *ProbeFleetCommand) newLiveExecutor(cmd *cobra.Command) fleet.EntryExecutor {
	runner := c.LiveSessionRunner
	if runner == nil {
		runner = runLiveSession
	}
	options := services.SessionRunOptions{
		Provider:      c.Provider,
		Model:         c.Model,
		ModelProvided: cmd.Flags().Changed("model"),
		APIKey:        c.APIKey,
		BaseURL:       c.BaseURL,
		ConfigDir:     commandFlagValue(cmd, "config-dir"),
	}
	return func(ctx context.Context, entry fleet.Entry) (fleet.EntryOutcome, error) {
		return runLiveFleetEntry(ctx, entry, options, runner)
	}
}

func commandFlagValue(cmd *cobra.Command, name string) string {
	for current := cmd; current != nil; current = current.Parent() {
		if flag := current.Flags().Lookup(name); flag != nil {
			return flag.Value.String()
		}
	}
	return ""
}

func runLiveSession(ctx context.Context, out io.Writer, options services.SessionRunOptions, input services.SessionAudioInput) error {
	if input.Present {
		return services.RunSessionWithInstructionsAndAudioInputAndOutputAndTextSeedAndMaxDuration(
			ctx, out, options, "", probeScenarioDeadline, services.SessionTextSeed{}, input, "",
		)
	}
	return services.RunSession(ctx, out, options)
}

func runLiveFleetEntry(ctx context.Context, entry fleet.Entry, options services.SessionRunOptions, runSession FleetLiveSessionRunner) (fleet.EntryOutcome, error) {
	scenario, err := loadFleetScenario(entry)
	if err != nil {
		return fleet.EntryOutcome{}, err
	}

	var output bytes.Buffer
	runner := &probe.Runner{
		Exec: deadguardExec(func(ctx context.Context, scenario probe.Scenario) (probe.ObservationSnapshot, error) {
			return executeLiveScenario(ctx, scenario, options, runSession)
		}, probeScenarioDeadline),
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

func executeLiveScenario(ctx context.Context, scenario probe.Scenario, baseOptions services.SessionRunOptions, runSession FleetLiveSessionRunner) (probe.ObservationSnapshot, error) {
	prompt, audioPath, err := liveScenarioInputs(scenario)
	if err != nil {
		return probe.ObservationSnapshot{}, err
	}
	captureDir, err := os.MkdirTemp("", "agent-probe-fleet-live-")
	if err != nil {
		return probe.ObservationSnapshot{}, fmt.Errorf("create live fleet capture directory: %w", err)
	}
	defer os.RemoveAll(captureDir)

	capturePath := filepath.Join(captureDir, "session.json")
	options := baseOptions
	options.RecordPath = capturePath
	options.Prompt = prompt
	audioInput := services.SessionAudioInput{Path: audioPath, Present: audioPath != ""}
	if err := runSession(ctx, io.Discard, options, audioInput); err != nil {
		return probe.ObservationSnapshot{}, fmt.Errorf("run live fleet scenario %q: %w", scenarioName(scenario), err)
	}
	capture, err := gatewaytesting.LoadSessionCapture(capturePath)
	if err != nil {
		return probe.ObservationSnapshot{}, fmt.Errorf("load live fleet capture %q: %w", capturePath, err)
	}
	return observationFromSessionCapture(ctx, scenario, capture, capturePath, false)
}

func liveScenarioInputs(scenario probe.Scenario) (prompt, audioPath string, err error) {
	audioSeen := false
	inputSeen := false
	for index, step := range scenario.Steps {
		kind := step.Kind
		if kind == "" {
			kind = step.Type
		}
		switch kind {
		case probe.StepSendText:
			if audioSeen {
				return "", "", fmt.Errorf("live fleet scenario %q step %d sends text after audio; the live session path supports text before one audio input", scenarioName(scenario), index)
			}
			if inputSeen {
				return "", "", fmt.Errorf("live fleet scenario %q step %d has more than one input", scenarioName(scenario), index)
			}
			prompt = step.Text
			inputSeen = true
		case probe.StepSendAudio:
			if audioSeen {
				return "", "", fmt.Errorf("live fleet scenario %q step %d has more than one audio input", scenarioName(scenario), index)
			}
			corpusID := step.CorpusID
			if corpusID == "" {
				corpusID = step.Corpus.CorpusID
			}
			audioPath, err = replayCorpusPath(corpusID)
			if err != nil {
				return "", "", fmt.Errorf("live fleet scenario %q step %d: %w", scenarioName(scenario), index, err)
			}
			audioSeen = true
			inputSeen = true
		case probe.StepClose:
			// The existing session runtime closes after the response for the
			// supported one-turn text/audio shape.
		default:
			return "", "", fmt.Errorf("live fleet scenario %q step %d uses %q; the existing live session path supports send_text, send_audio, and close", scenarioName(scenario), index, kind)
		}
	}
	if !inputSeen {
		return "", "", fmt.Errorf("live fleet scenario %q has no send_text or send_audio input", scenarioName(scenario))
	}
	return prompt, audioPath, nil
}

func loadFleetScenario(entry fleet.Entry) (probe.Scenario, error) {
	scenario, err := loadProbeScenarioFile(entry.ScenarioPath)
	if err != nil {
		return probe.Scenario{}, fmt.Errorf("load scenario %q: %w", entry.ScenarioPath, err)
	}
	if !scenarioMatchesEntry(scenario, entry) {
		return probe.Scenario{}, fmt.Errorf("scenario %q does not match manifest entry %q", entry.ScenarioPath, entry.ID)
	}
	return scenario, nil
}

func runReplayFleetEntry(ctx context.Context, entry fleet.Entry, exec probe.ExecFunc) (fleet.EntryOutcome, error) {
	scenario, err := loadFleetScenario(entry)
	if err != nil {
		return fleet.EntryOutcome{}, err
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
