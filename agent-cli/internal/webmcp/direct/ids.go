// Package direct owns the browser-neutral rules behind the direct WebMCP
// commands and the doctor: opaque-ID normalization, bounded candidate
// choices, page-target eligibility and origin policy, replacement-browser
// detection, and the bounded runtime lifecycle. Command wiring, flag parsing,
// and output rendering stay in the CLI transport.
package direct

import (
	"sort"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/production/normalize"
)

// MaxAmbiguityCandidates bounds the candidate IDs and choices carried by one
// ambiguity error.
const MaxAmbiguityCandidates = 32

// NormalizeOpaqueID accepts the same bounded public-ID alphabet used by
// discovery. Direct commands receive normalized IDs from their broker, but
// keeping the output boundary defensive prevents malformed adapter values from
// becoming endpoint-shaped or otherwise unsafe candidate details. An invalid
// value normalizes to the empty string.
func NormalizeOpaqueID(value string) string {
	value = strings.TrimSpace(value)
	if !normalize.OpaqueID(value) {
		return ""
	}
	return value
}

func sortedUniqueIDs(values []string) []string {
	ids := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		normalized := NormalizeOpaqueID(value)
		if normalized == "" {
			continue
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		ids = append(ids, normalized)
	}
	sort.Strings(ids)
	return ids
}

// SafeIDList extracts the valid opaque IDs from a []string or []any detail
// value, deduplicated and sorted.
func SafeIDList(value any) []string {
	values := make([]string, 0)
	switch typed := value.(type) {
	case []string:
		values = append(values, typed...)
	case []any:
		for _, item := range typed {
			if text, ok := item.(string); ok {
				values = append(values, text)
			}
		}
	}
	return sortedUniqueIDs(values)
}

// BrowserCandidateIDs returns the valid, deduplicated, sorted browser IDs.
func BrowserCandidateIDs(candidates []webmcp.BrowserCandidate) []string {
	ids := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		ids = append(ids, string(candidate.ID))
	}
	return sortedUniqueIDs(ids)
}

func targetCandidateIDs(targets []webmcp.Target) []string {
	ids := make([]string, 0, len(targets))
	for _, target := range targets {
		ids = append(ids, string(target.ID))
	}
	return sortedUniqueIDs(ids)
}

// AmbiguityTargetIDs returns the sorted target IDs reported by an ambiguity
// error, bounded to MaxAmbiguityCandidates.
func AmbiguityTargetIDs(targets []webmcp.Target) []string {
	ids := targetCandidateIDs(targets)
	if len(ids) > MaxAmbiguityCandidates {
		return ids[:MaxAmbiguityCandidates]
	}
	return ids
}
