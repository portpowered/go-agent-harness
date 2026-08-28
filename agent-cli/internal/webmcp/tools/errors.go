package tools

import (
	"sort"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/discovery"
)

func ambiguousBrowserError(candidates []discovery.BrowserCandidate) error {
	ids := make([]string, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		id := safeID(candidate.ID)
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return &discovery.DiscoveryError{
		Code:      discovery.CodeAmbiguousBrowser,
		Message:   "multiple browsers matched; an exact browser ID is required",
		Retryable: true,
		Details:   map[string]any{"candidate_browser_ids": ids},
	}
}
