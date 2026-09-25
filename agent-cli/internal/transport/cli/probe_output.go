package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	probescenario "github.com/portpowered/go-agent-harness/agent-cli/internal/probe/scenario"
	serviceDevices "github.com/portpowered/go-agent-harness/agent-cli/internal/services/devices"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
)

// probeOutputCommand is the output surface of a cobra command.
type probeOutputCommand interface {
	OutOrStdout() io.Writer
	ErrOrStderr() io.Writer
}

// openProbeOutputs opens the result and summary streams. The returned close
// function releases any files it opened and reports their close failures.
func (c *ProbeRunCommand) openProbeOutputs(cmd probeOutputCommand) (io.Writer, io.Writer, func() error, error) {
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

// reportProbeSummary prints the human-readable pass line unless --json is
// set and turns any failed scenario into a non-zero exit.
func (c *ProbeRunCommand) reportProbeSummary(cmd probeOutputCommand, summary probe.RunSummary) error {
	if !c.JSONOut {
		if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "probe: %d/%d scenarios passed (%s)\n", summary.Passed, summary.Total, summary.Status); err != nil {
			return fmt.Errorf("write probe summary: %w", err)
		}
	}
	if summary.Failed > 0 {
		return fmt.Errorf("%d of %d probe scenarios failed", summary.Failed, summary.Total)
	}
	return nil
}

// writeDeviceProbeSkip reports a device-tier run on a host without both
// audio directions: one skip line per selection and one skip summary.
func (c *ProbeRunCommand) writeDeviceProbeSkip(cmd probeOutputCommand, selections []string, availability serviceDevices.DeviceProbeAvailability) (err error) {
	resultsOut, summaryOut, closeOutputs, err := c.openProbeOutputs(cmd)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := closeOutputs(); err == nil {
			err = closeErr
		}
	}()
	results, summary := probescenario.DeviceSkip(selections, availability)
	for _, result := range results {
		if err := json.NewEncoder(resultsOut).Encode(result); err != nil {
			return fmt.Errorf("write device probe result: %w", err)
		}
	}
	if err := json.NewEncoder(summaryOut).Encode(summary); err != nil {
		return fmt.Errorf("write device probe summary: %w", err)
	}
	if !c.JSONOut {
		if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "probe: 0/%d scenarios passed (%d skipped, %s)\n", len(selections), len(selections), summary.Status); err != nil {
			return fmt.Errorf("write device probe summary: %w", err)
		}
	}
	return nil
}
