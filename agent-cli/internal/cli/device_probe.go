package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
)

// deviceProbeSkipResult is intentionally distinct from the replay runner's
// pass/fail result. A device-tier skip is neither a pass nor a failure and
// carries a stable reason code for acceptance tooling.
type deviceProbeSkipResult struct {
	Name       string                    `json:"name"`
	Status     audio.DeviceProbeStatus   `json:"status"`
	ReasonCode audio.DeviceProbeSkipCode `json:"reason_code"`
	Reason     string                    `json:"reason"`
}

type deviceProbeSkipSummary struct {
	Total   int                     `json:"total"`
	Passed  int                     `json:"passed"`
	Failed  int                     `json:"failed"`
	Skipped int                     `json:"skipped"`
	Status  audio.DeviceProbeStatus `json:"status"`
}

func (c *ProbeRunCommand) writeDeviceProbeSkip(cmd interface {
	OutOrStdout() io.Writer
	ErrOrStderr() io.Writer
}, positional []string, availability audio.DeviceProbeAvailability) error {
	selections := probeSelections(positional, c.Scenarios)
	if len(selections) == 0 {
		return fmt.Errorf("no probe scenarios selected; pass scenario paths as arguments or repeat --scenario")
	}

	resultsOut := cmd.OutOrStdout()
	var resultsFile *os.File
	if c.OutPath != "" {
		file, err := os.Create(c.OutPath)
		if err != nil {
			return fmt.Errorf("open --out %q: %w", c.OutPath, err)
		}
		resultsFile, resultsOut = file, file
		defer resultsFile.Close()
	}
	summaryOut := cmd.ErrOrStderr()
	var summaryFile *os.File
	if c.SummaryPath != "" {
		file, err := os.Create(c.SummaryPath)
		if err != nil {
			return fmt.Errorf("open --summary %q: %w", c.SummaryPath, err)
		}
		summaryFile, summaryOut = file, file
		defer summaryFile.Close()
	}

	for _, selection := range selections {
		result := deviceProbeSkipResult{
			Name:       deviceProbeSelectionName(selection),
			Status:     availability.Status,
			ReasonCode: availability.ReasonCode,
			Reason:     availability.Reason,
		}
		if err := writeDeviceProbeJSON(resultsOut, result); err != nil {
			return fmt.Errorf("write device probe result: %w", err)
		}
	}
	summary := deviceProbeSkipSummary{
		Total: len(selections), Skipped: len(selections), Status: audio.DeviceProbeStatusSkip,
	}
	if err := writeDeviceProbeJSON(summaryOut, summary); err != nil {
		return fmt.Errorf("write device probe summary: %w", err)
	}
	if !c.JSONOut {
		fmt.Fprintf(cmd.ErrOrStderr(), "probe: 0/%d scenarios passed (%d skipped, %s)\n", len(selections), len(selections), summary.Status)
	}
	return nil
}

func writeDeviceProbeJSON(out io.Writer, value any) error {
	return json.NewEncoder(out).Encode(value)
}

func deviceProbeSelectionName(selection string) string {
	data, err := os.ReadFile(selection)
	if err != nil {
		return selection
	}
	var document struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if json.Unmarshal(data, &document) != nil {
		return selection
	}
	if strings.TrimSpace(document.Name) != "" {
		return document.Name
	}
	if strings.TrimSpace(document.ID) != "" {
		return document.ID
	}
	return selection
}
