package cli

import (
	"fmt"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

func isDoctorPageToolsUnverified(err error) bool {
	if err == nil {
		return false
	}
	if classifiedErr, ok := err.(*webmcp.ClassifiedError); ok && classifiedErr != nil && classifiedErr.Details != nil {
		if classifiedErr.Details["reason_code"] == "page_tools_unverified" || classifiedErr.Details["page_tools"] == "unverified" {
			return true
		}
	}
	switch unwrapped := err.(type) {
	case interface{ Unwrap() error }:
		return isDoctorPageToolsUnverified(unwrapped.Unwrap())
	case interface{ Unwrap() []error }:
		for _, cause := range unwrapped.Unwrap() {
			if isDoctorPageToolsUnverified(cause) {
				return true
			}
		}
	}
	return false
}

func doctorPageToolsUnverifiedError(cause error, target *webmcp.Target, generation uint64) error {
	details := map[string]any{
		"phase":                 "catalog",
		"reason_code":           "page_tools_unverified",
		"webmcp_domain":         "supported",
		"page_tools":            "unverified",
		"catalog":               "unverified",
		"tested_browser_row":    doctorTestedChromeRow,
		"required_launch_flags": doctorTestedChromeFlags,
		"required_page_policy":  "Permissions-Policy: tools=(self)",
		"evidence_needed":       "affirmative page producer/catalog-ready observation",
	}
	if target != nil {
		details["browser_id"] = string(target.BrowserID)
		details["target_id"] = string(target.ID)
	}
	if generation > 0 {
		details["generation"] = generation
	}
	message := fmt.Sprintf("the CDP WebMCP domain is supported, but the selected page did not provide affirmative page-tool catalog evidence; test %s with %s and ensure the page grants Permissions-Policy: tools=(self)", doctorTestedChromeRow, doctorTestedChromeFlags)
	classified := webmcp.NewClassifiedError(webmcp.ErrorBrowserProtocol, message, details)
	classified.Cause = cause
	return classified
}

func markDoctorTargetSelected(report *WebMCPDoctorReport, target *webmcp.Target, attached bool) {
	if report == nil || target == nil {
		return
	}
	selected := doctorTargetFromTarget(*target)
	selected.Selected = true
	selected.Attached = attached
	report.SelectedPage = &selected
	for index := range report.Targets {
		if report.Targets[index].BrowserID == selected.BrowserID && report.Targets[index].TargetID == selected.TargetID {
			report.Targets[index].Selected = true
			report.Targets[index].Attached = attached
		}
	}
}
