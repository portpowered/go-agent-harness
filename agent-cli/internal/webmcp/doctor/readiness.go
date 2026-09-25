package doctor

import (
	"errors"
	"fmt"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

const reasonPageToolsUnverified = "page_tools_unverified"

// isPageToolsUnverified reports whether err, or any error it wraps or
// joins, is a classified page-tools-unverified error.
func isPageToolsUnverified(err error) bool {
	if err == nil {
		return false
	}
	var classifiedErr *webmcp.ClassifiedError
	if errors.As(err, &classifiedErr) && classifiedErr != nil && classifiedErr.Details != nil {
		if classifiedErr.Details["reason_code"] == reasonPageToolsUnverified || classifiedErr.Details[keyPageTools] == ValueUnverified {
			return true
		}
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, cause := range joined.Unwrap() {
			if isPageToolsUnverified(cause) {
				return true
			}
		}
		return false
	}
	return isPageToolsUnverified(errors.Unwrap(err))
}

func pageToolsUnverifiedError(cause error, target *webmcp.Target, generation uint64) error {
	details := map[string]any{
		keyPhase:                checkCatalog,
		"reason_code":           reasonPageToolsUnverified,
		"webmcp_domain":         ValueSupported,
		keyPageTools:            ValueUnverified,
		"catalog":               ValueUnverified,
		"tested_browser_row":    TestedChromeRow,
		"required_launch_flags": TestedChromeFlags,
		"required_page_policy":  "Permissions-Policy: tools=(self)",
		"evidence_needed":       "affirmative page producer/catalog-ready observation",
	}
	if target != nil {
		details[keyBrowserID] = string(target.BrowserID)
		details[keyTargetID] = string(target.ID)
	}
	if generation > 0 {
		details["generation"] = generation
	}
	message := fmt.Sprintf("the CDP WebMCP domain is supported, but the selected page did not provide affirmative page-tool catalog evidence; test %s with %s and ensure the page grants Permissions-Policy: tools=(self)", TestedChromeRow, TestedChromeFlags)
	classified := webmcp.NewClassifiedError(webmcp.ErrorBrowserProtocol, message, details)
	classified.Cause = cause
	return classified
}
