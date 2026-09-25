package scenario

import (
	"encoding/json"
	"os"
	"strings"

	serviceDevices "github.com/portpowered/go-agent-harness/agent-cli/internal/services/devices"
)

// DeviceSkipResult is intentionally distinct from the replay runner's
// pass/fail result. A device-tier skip is neither a pass nor a failure and
// carries a stable reason code for acceptance tooling.
type DeviceSkipResult struct {
	Name       string                             `json:"name"`
	Status     serviceDevices.DeviceProbeStatus   `json:"status"`
	ReasonCode serviceDevices.DeviceProbeSkipCode `json:"reason_code"`
	Reason     string                             `json:"reason"`
}

// DeviceSkipSummary counts a device-tier run whose every scenario skipped.
type DeviceSkipSummary struct {
	Total   int                              `json:"total"`
	Passed  int                              `json:"passed"`
	Failed  int                              `json:"failed"`
	Skipped int                              `json:"skipped"`
	Status  serviceDevices.DeviceProbeStatus `json:"status"`
}

// DeviceSkip reports every selection as skipped with the host's reason.
func DeviceSkip(selections []string, availability serviceDevices.DeviceProbeAvailability) ([]DeviceSkipResult, DeviceSkipSummary) {
	results := make([]DeviceSkipResult, 0, len(selections))
	for _, selection := range selections {
		results = append(results, DeviceSkipResult{
			Name:       deviceSelectionName(selection),
			Status:     availability.Status,
			ReasonCode: availability.ReasonCode,
			Reason:     availability.Reason,
		})
	}
	summary := DeviceSkipSummary{Total: len(selections), Skipped: len(selections), Status: serviceDevices.DeviceProbeStatusSkip}
	return results, summary
}

// deviceSelectionName names a skipped selection by its document name or ID,
// falling back to the selection itself.
func deviceSelectionName(selection string) string {
	data, err := os.ReadFile(selection)
	if err != nil {
		return selection
	}
	var doc struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if json.Unmarshal(data, &doc) != nil {
		return selection
	}
	if strings.TrimSpace(doc.Name) != "" {
		return doc.Name
	}
	if strings.TrimSpace(doc.ID) != "" {
		return doc.ID
	}
	return selection
}
