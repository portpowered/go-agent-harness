package direct

import (
	"context"
	"sort"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/production/normalize"
)

const (
	targetTypePage = "page"

	maxReasonLength = 80
	reasonFallback  = "ineligible"

	detailBrowserID      = "browser_id"
	detailTargetID       = "target_id"
	detailCandidateCount = "candidate_count"
	detailReason         = "reason"

	messageNoEligibleTab   = "no eligible WebMCP target was found"
	messageBrowserNotFound = "the selected browser is no longer current"
)

// NormalizeBrowserCandidates drops candidates without a valid opaque ID,
// deduplicates by ID, and sorts the result by ID.
func NormalizeBrowserCandidates(candidates []webmcp.BrowserCandidate) []webmcp.BrowserCandidate {
	normalized := make([]webmcp.BrowserCandidate, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		id := NormalizeOpaqueID(string(candidate.ID))
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		candidate.ID = webmcp.BrowserID(id)
		seen[id] = struct{}{}
		normalized = append(normalized, candidate)
	}
	sort.SliceStable(normalized, func(i, j int) bool { return normalized[i].ID < normalized[j].ID })
	return normalized
}

// PageTargetCandidates is the shared direct-CLI discovery boundary. Chrome
// exposes browser-owned UI surfaces through the same CDP target list as
// documents, but only an exact page target is selectable by the CLI.
func PageTargetCandidates(targets []webmcp.Target) []webmcp.Target {
	pages := make([]webmcp.Target, 0, len(targets))
	for _, target := range targets {
		if target.Type != targetTypePage {
			continue
		}
		pages = append(pages, target)
	}
	return pages
}

// EligibleTargetMatches returns the eligible page targets that satisfy the
// configured origin filter and origin policy, with normalized unique IDs,
// sorted by ID.
func EligibleTargetMatches(targets []webmcp.Target, browser config.BrowserConfig) []webmcp.Target {
	matches := make([]webmcp.Target, 0, len(targets))
	seen := make(map[string]struct{}, len(targets))
	for _, possible := range PageTargetCandidates(targets) {
		id := NormalizeOpaqueID(string(possible.ID))
		if id == "" || !possible.Eligible {
			continue
		}
		if browser.Selection.Origin != "" && normalize.RedactedOrigin(possible.Origin) != normalize.RedactedOrigin(browser.Selection.Origin) {
			continue
		}
		if err := TargetPolicyError(possible, browser); err != nil {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		possible.ID = webmcp.TargetID(id)
		seen[id] = struct{}{}
		matches = append(matches, possible)
	}
	sort.SliceStable(matches, func(i, j int) bool { return matches[i].ID < matches[j].ID })
	return matches
}

// NoEligibleTabError keeps the direct selection path aligned with the C0
// no_eligible_tab envelope. The direct page candidate list is the complete
// enumeration at this boundary, so its length is the useful candidate count
// even when eligibility filtering removes every page.
func NoEligibleTabError(browserID string, browser config.BrowserConfig, candidateCount int, reason string) error {
	if candidateCount < 0 {
		candidateCount = 0
	}
	filters := map[string]any{
		"eligible_only":           true,
		"include_zero_tool_pages": true,
	}
	if origin := normalize.RedactedOrigin(browser.Selection.Origin); origin != "" {
		filters["origin"] = origin
	}
	details := map[string]any{
		detailBrowserID:      browserID,
		"filters":            filters,
		detailCandidateCount: candidateCount,
	}
	if reason != "" {
		details[detailReason] = BoundedReason(reason)
	}
	return webmcp.NewClassifiedError(webmcp.ErrorNoEligibleTab, messageNoEligibleTab, details)
}

// BoundedReason reduces an eligibility reason to at most 80 bytes of
// printable text; an empty or control-bearing reason becomes "ineligible".
func BoundedReason(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return reasonFallback
	}
	if len(value) > maxReasonLength {
		return value[:maxReasonLength]
	}
	if containsControl(value) {
		return reasonFallback
	}
	return value
}

// DiscoverBrowsers discovers the configured browsers and, when browserID is
// set, narrows the result to that exact browser or reports a stale
// selection.
func DiscoverBrowsers(ctx context.Context, broker webmcp.Broker, browser config.BrowserConfig, browserID string) ([]webmcp.BrowserCandidate, error) {
	candidates, err := DiscoverCandidates(ctx, broker, browser)
	if err != nil {
		return nil, err
	}
	if browserID == "" {
		return candidates, nil
	}
	for _, candidate := range candidates {
		if string(candidate.ID) == browserID {
			return []webmcp.BrowserCandidate{candidate}, nil
		}
	}
	return nil, StaleBrowserError(browserID)
}

// StaleBrowserError reports that the selected browser is no longer among
// the discovered candidates.
func StaleBrowserError(browserID string) error {
	return webmcp.NewClassifiedError(webmcp.ErrorStaleSelection, messageBrowserNotFound, map[string]any{
		detailBrowserID:       browserID,
		detailTargetID:        "",
		"selected_generation": uint64(0),
		detailReason:          "browser_not_found",
	})
}

// DiscoverCandidates runs broker discovery for the configured connection
// and returns the normalized candidates, or endpoint_not_found when none
// remain.
func DiscoverCandidates(ctx context.Context, broker webmcp.Broker, browser config.BrowserConfig) ([]webmcp.BrowserCandidate, error) {
	if broker == nil {
		return nil, RuntimeUnavailableError("discovery")
	}
	candidates, err := broker.Discover(ctx, webmcp.DiscoverOptions{
		ExplicitOnly:     browser.Connection.CDPURL != "" || browser.Connection.WSEndpoint != "",
		AllowProcessScan: browser.Connection.AllowProcessScan,
		AllowRemoteCDP:   browser.Connection.AllowRemoteCDP,
	})
	if err != nil {
		return nil, err
	}
	candidates = NormalizeBrowserCandidates(candidates)
	if len(candidates) == 0 {
		return nil, webmcp.NewClassifiedError(webmcp.ErrorEndpointNotFound, "browser endpoint was not found", map[string]any{
			"endpoint_kind": EndpointKind(browser),
			"source":        string(webmcp.DiscoverySourceConfigured),
		})
	}
	return candidates, nil
}
