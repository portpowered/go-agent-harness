package cli

import (
	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

// directNoEligibleTabError keeps the direct selection path aligned with the
// C0 no_eligible_tab envelope. The broker target list is the complete
// enumeration at this boundary, so its length is the useful candidate count
// even when eligibility filtering removes every target.
func directNoEligibleTabError(browserID string, browser config.BrowserConfig, candidateCount int, reason string) error {
	if candidateCount < 0 {
		candidateCount = 0
	}
	filters := map[string]any{
		"eligible_only":           true,
		"include_zero_tool_pages": true,
	}
	if origin := safeOrigin(browser.Selection.Origin); origin != "" {
		filters["origin"] = origin
	}
	details := map[string]any{
		"browser_id":      browserID,
		"filters":         filters,
		"candidate_count": candidateCount,
	}
	if reason != "" {
		details["reason"] = boundedDirectReason(reason)
	}
	return webmcp.NewClassifiedError(webmcp.ErrorNoEligibleTab, "no eligible WebMCP target was found", details)
}
