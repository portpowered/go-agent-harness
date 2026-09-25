package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/probe/scenariov2"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
	runtimeReplay "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	"github.com/spf13/cobra"
)

// runScenarioV2 executes browser-aware probe.scenario.v2 selections. The
// scenariov2 package owns execution and evidence; this command owns flag
// resolution and the result/summary output streams.
func (c *ProbeRunCommand) runScenarioV2(cmd *cobra.Command, selections []string) (err error) {
	entries, err := scenariov2.LoadSelections(selections, replayCorpusLookup{})
	if err != nil {
		return err
	}
	browserOptions, err := c.probeScenarioV2BrowserExecutorOptions(cmd)
	if err != nil {
		return err
	}
	resultsOut, summaryOut, closeOutputs, err := c.openProbeOutputs(cmd)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := closeOutputs(); err == nil {
			err = closeErr
		}
	}()
	recordingRoot, err := scenariov2.PrepareRecordingRoot(c.RecordingRoot, len(entries))
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	runner := scenariov2.Runner{Replay: c.replayService, Analyze: analyzeProbeScenarioV2Provider, Deadline: probeScenarioDeadline}
	emit := func(result scenariov2.Result) error { return writeProbeScenarioV2Result(resultsOut, result) }
	summary, err := runner.Run(ctx, entries, recordingRoot, emit, browserOptions.Options()...)
	if err != nil {
		return err
	}
	return c.writeProbeScenarioV2Summary(cmd, summaryOut, summary)
}

func analyzeProbeScenarioV2Provider(
	ctx context.Context,
	replayService runtimeReplay.Service,
	scenario probe.Scenario,
	request runtimeReplay.CaptureProbeRequest,
) (runtimeReplay.CaptureProbeObservation, probe.ObservationSnapshot, error) {
	return analyzeCaptureProbe(ctx, replayService, scenario, request)
}

func writeProbeScenarioV2Result(out io.Writer, result scenariov2.Result) error {
	encoded, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("encode result for scenario %q: %w", result.Name, err)
	}
	if _, err := fmt.Fprintf(out, "%s\n", encoded); err != nil {
		return fmt.Errorf("write result for scenario %q: %w", result.Name, err)
	}
	return nil
}

func (c *ProbeRunCommand) writeProbeScenarioV2Summary(cmd *cobra.Command, out io.Writer, summary probe.RunSummary) error {
	encoded, err := json.Marshal(summary)
	if err != nil {
		return fmt.Errorf("encode v2 probe summary: %w", err)
	}
	if _, err := fmt.Fprintf(out, "%s\n", encoded); err != nil {
		return fmt.Errorf("write v2 probe summary: %w", err)
	}
	if !c.JSONOut {
		fmt.Fprintf(cmd.ErrOrStderr(), "probe: %d/%d scenarios passed (%s)\n", summary.Passed, summary.Total, summary.Status)
	}
	if summary.Failed > 0 {
		return fmt.Errorf("%d of %d probe scenarios failed", summary.Failed, summary.Total)
	}
	return nil
}

// openProbeOutputs opens the result and summary streams. The returned close
// function releases any files it opened and reports their close failures.
func (c *ProbeRunCommand) openProbeOutputs(cmd interface {
	OutOrStdout() io.Writer
	ErrOrStderr() io.Writer
}) (io.Writer, io.Writer, func() error, error) {
	noop := func() error { return nil }
	resultsOut := cmd.OutOrStdout()
	var resultFile *os.File
	if c.OutPath != "" {
		file, err := os.Create(c.OutPath)
		if err != nil {
			return nil, nil, noop, fmt.Errorf("open --out %q: %w", c.OutPath, err)
		}
		resultFile = file
		resultsOut = file
	}
	summaryOut := cmd.ErrOrStderr()
	var summaryFile *os.File
	if c.SummaryPath != "" {
		file, err := os.Create(c.SummaryPath)
		if err != nil {
			return nil, nil, noop, fmt.Errorf("open --summary %q: %w", c.SummaryPath, errors.Join(err, closeProbeOutputFile(resultFile)))
		}
		summaryFile = file
		summaryOut = file
	}
	closeOutputs := func() error {
		return errors.Join(closeProbeOutputFile(summaryFile), closeProbeOutputFile(resultFile))
	}
	return resultsOut, summaryOut, closeOutputs, nil
}

func closeProbeOutputFile(file *os.File) error {
	if file == nil {
		return nil
	}
	return file.Close()
}
