package direct

import (
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/production/normalize"
)

const (
	maxAmbiguityTitle  = 160
	maxAmbiguityOrigin = 256
	maxRawOriginLength = 4096

	choiceBrowserID = "browser_id"
	choiceTargetID  = "target_id"
	choiceTitle     = "title"
	choiceOrigin    = "origin"

	redactedTitle = "redacted"
)

// choiceMetadata is one bounded, redacted candidate choice.
type choiceMetadata struct {
	browserID string
	targetID  string
	title     string
	origin    string
}

func (c choiceMetadata) toMap() map[string]any {
	choice := map[string]any{choiceBrowserID: c.browserID, choiceTargetID: c.targetID}
	if c.title != "" {
		choice[choiceTitle] = c.title
	}
	if c.origin != "" {
		choice[choiceOrigin] = c.origin
	}
	return choice
}

// CandidateChoicesForTargets builds the bounded, redacted ambiguity choices
// for targets of one browser, ordered by target ID, origin, then title, with
// duplicate or invalid target IDs dropped.
func CandidateChoicesForTargets(browserID string, targets []webmcp.Target) []map[string]any {
	ordered := append([]webmcp.Target(nil), targets...)
	sort.SliceStable(ordered, func(i, j int) bool {
		leftID := NormalizeOpaqueID(string(ordered[i].ID))
		rightID := NormalizeOpaqueID(string(ordered[j].ID))
		if leftID != rightID {
			return leftID < rightID
		}
		leftOrigin := candidateOrigin(ordered[i])
		rightOrigin := candidateOrigin(ordered[j])
		if leftOrigin != rightOrigin {
			return leftOrigin < rightOrigin
		}
		return candidateTitle(ordered[i].Title) < candidateTitle(ordered[j].Title)
	})

	choices := make([]map[string]any, 0, len(ordered))
	seen := make(map[string]struct{}, len(ordered))
	for _, target := range ordered {
		targetID := NormalizeOpaqueID(string(target.ID))
		if targetID == "" {
			continue
		}
		if _, exists := seen[targetID]; exists {
			continue
		}
		seen[targetID] = struct{}{}
		item := choiceMetadata{
			browserID: NormalizeOpaqueID(browserID),
			targetID:  targetID,
			title:     candidateTitle(target.Title),
			origin:    candidateOrigin(target),
		}
		choices = append(choices, item.toMap())
		if len(choices) == MaxAmbiguityCandidates {
			break
		}
	}
	return choices
}

// SafeCandidateChoices re-sanitizes candidate choices received from an
// error detail value ([]map[string]any or []any). The result follows
// candidateIDs when given, otherwise the IDs of the choices themselves, and
// is bounded to MaxAmbiguityCandidates entries.
func SafeCandidateChoices(value any, fallbackBrowserID string, candidateIDs []string) []map[string]any {
	fallback := NormalizeOpaqueID(fallbackBrowserID)
	items := collectChoiceMetadata(value, fallback)
	sortChoiceMetadata(items)
	ids := choiceIDs(candidateIDs, items)
	if len(ids) == 0 {
		return nil
	}
	byID := make(map[string]choiceMetadata, len(items))
	for _, item := range items {
		if _, exists := byID[item.targetID]; !exists {
			byID[item.targetID] = item
		}
	}
	result := make([]map[string]any, 0, len(ids))
	for _, targetID := range ids {
		item := byID[targetID]
		item.targetID = targetID
		if item.browserID == "" {
			item.browserID = fallback
		}
		result = append(result, item.toMap())
	}
	return result
}

func collectChoiceMetadata(value any, fallbackBrowserID string) []choiceMetadata {
	var raw []map[string]any
	switch choices := value.(type) {
	case []map[string]any:
		raw = choices
	case []any:
		for _, entry := range choices {
			if choice, ok := entry.(map[string]any); ok {
				raw = append(raw, choice)
			}
		}
	}
	items := make([]choiceMetadata, 0, len(raw))
	for _, choice := range raw {
		if item, ok := choiceMetadataFromMap(choice, fallbackBrowserID); ok {
			items = append(items, item)
		}
	}
	return items
}

func choiceMetadataFromMap(choice map[string]any, fallbackBrowserID string) (choiceMetadata, bool) {
	targetID := NormalizeOpaqueID(detailString(choice[choiceTargetID]))
	if targetID == "" {
		return choiceMetadata{}, false
	}
	browserID := NormalizeOpaqueID(detailString(choice[choiceBrowserID]))
	if browserID == "" {
		browserID = fallbackBrowserID
	}
	return choiceMetadata{
		browserID: browserID,
		targetID:  targetID,
		title:     candidateTitle(detailString(choice[choiceTitle])),
		origin:    canonicalCandidateOrigin(detailString(choice[choiceOrigin])),
	}, true
}

func sortChoiceMetadata(items []choiceMetadata) {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].targetID != items[j].targetID {
			return items[i].targetID < items[j].targetID
		}
		if items[i].browserID != items[j].browserID {
			return items[i].browserID < items[j].browserID
		}
		if items[i].title != items[j].title {
			return items[i].title < items[j].title
		}
		return items[i].origin < items[j].origin
	})
}

func choiceIDs(candidateIDs []string, items []choiceMetadata) []string {
	ids := append([]string(nil), candidateIDs...)
	if len(ids) == 0 {
		for _, item := range items {
			ids = append(ids, item.targetID)
		}
	}
	ids = sortedUniqueIDs(ids)
	if len(ids) > MaxAmbiguityCandidates {
		ids = ids[:MaxAmbiguityCandidates]
	}
	return ids
}

func detailString(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

func candidateTitle(value string) string {
	value = normalize.BoundedText(value, maxAmbiguityTitle)
	if value == "" {
		return ""
	}
	if containsControl(value) || strings.Contains(value, "://") || strings.ContainsAny(value, "?#@") {
		return redactedTitle
	}
	return value
}

func candidateOrigin(target webmcp.Target) string {
	for _, value := range []string{target.Origin, target.URL} {
		if origin := canonicalCandidateOrigin(value); origin != "" {
			return origin
		}
	}
	return ""
}

func canonicalCandidateOrigin(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > maxRawOriginLength || containsControl(raw) {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed == nil || parsed.User != nil {
		return ""
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != schemeHTTP && scheme != schemeHTTPS || parsed.Hostname() == "" {
		return ""
	}
	port, ok := canonicalPort(scheme, parsed.Port())
	if !ok {
		return ""
	}
	host := strings.ToLower(parsed.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port != "" {
		host += ":" + port
	}
	origin := scheme + "://" + host
	if len(origin) > maxAmbiguityOrigin {
		return ""
	}
	return origin
}

// canonicalPort validates port and drops it when it is the scheme default.
func canonicalPort(scheme, port string) (string, bool) {
	if port == "" {
		return "", true
	}
	value, err := strconv.Atoi(port)
	if err != nil || value < 1 || value > maxPort {
		return "", false
	}
	if scheme == schemeHTTP && port == "80" || scheme == schemeHTTPS && port == "443" {
		return "", true
	}
	return port, true
}

func containsControl(value string) bool {
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}
