package browser

import (
	"net/url"
	"sort"
	"strconv"
	"strings"
)

const (
	maxAmbiguityCandidates = 32
	maxAmbiguityTitle      = 160
	maxAmbiguityOrigin     = 256
)

// withAmbiguityRecovery adds bounded, model-visible instructions at the
// result boundary. Ambiguity is retryable only after the customer supplies a
// new choice; repeating the same selector-free call cannot make the result
// more specific.
func withAmbiguityRecovery(code ErrorCode, details map[string]any) map[string]any {
	if code != ErrorAmbiguousBrowser && code != ErrorAmbiguousTab {
		return details
	}
	result := sanitizeAmbiguityDetails(code, details)
	if result == nil {
		result = map[string]any{}
	}
	instruction := "Ask the customer which browser they mean, then retry once with its exact browser ID; do not repeat this call until the customer provides a choice."
	if code == ErrorAmbiguousTab {
		instruction = "Ask the customer which named page they mean, then retry once with its exact target ID; do not repeat this call until the customer provides a choice."
	}
	result["recovery"] = map[string]any{
		"action":      "ask_customer",
		"retry_after": "customer_input",
		"instruction": instruction,
	}
	return result
}

func sanitizeAmbiguityDetails(code ErrorCode, details map[string]any) map[string]any {
	result := cloneDetails(details)
	if result == nil {
		result = map[string]any{}
	}
	switch code {
	case ErrorAmbiguousBrowser:
		result["candidate_browser_ids"] = boundedAmbiguityIDs(anyAmbiguityIDs(result["candidate_browser_ids"]))
	case ErrorAmbiguousTab:
		sanitizeAmbiguousTabDetails(result)
	}
	return result
}

func sanitizeAmbiguousTabDetails(result map[string]any) {
	browserID := safeAmbiguityID(stringValue(result["browser_id"]))
	ids := boundedAmbiguityIDs(anyAmbiguityIDs(result["candidate_target_ids"]))
	result["browser_id"] = browserID
	result["candidate_target_ids"] = ids
	choices := safeAmbiguityChoices(result["candidate_choices"], browserID, ids)
	if len(choices) > 0 {
		result["candidate_choices"] = choices
		return
	}
	delete(result, "candidate_choices")
}

func anyAmbiguityIDs(value any) []string {
	values := ambiguityIDValues(value)
	ids := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		normalized := safeAmbiguityID(value)
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

func ambiguityIDValues(value any) []string {
	values := make([]string, 0)
	switch typed := value.(type) {
	case []string:
		return append(values, typed...)
	case []any:
		for _, item := range typed {
			if text, ok := item.(string); ok {
				values = append(values, text)
			}
		}
	}
	return values
}

func boundedAmbiguityIDs(ids []string) []string {
	if len(ids) > maxAmbiguityCandidates {
		return ids[:maxAmbiguityCandidates]
	}
	return ids
}

func safeAmbiguityID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 64 {
		return ""
	}
	for _, character := range value {
		if isAmbiguityIDCharacter(character) {
			continue
		}
		return ""
	}
	return value
}

func isAmbiguityIDCharacter(character rune) bool {
	return character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '_' || character == '-'
}

type ambiguityMetadata struct {
	browserID string
	targetID  string
	title     string
	origin    string
}

func safeAmbiguityChoices(value any, fallbackBrowserID string, candidateIDs []string) []map[string]any {
	items := collectAmbiguityChoices(value, fallbackBrowserID)
	sortAmbiguityChoices(items)
	ids := ambiguityCandidateIDs(candidateIDs, items)
	if len(ids) == 0 {
		return nil
	}
	return renderAmbiguityChoices(ids, items, fallbackBrowserID)
}

func collectAmbiguityChoices(value any, fallbackBrowserID string) []ambiguityMetadata {
	items := make([]ambiguityMetadata, 0)
	appendChoice := func(choice map[string]any) {
		item, ok := ambiguityMetadataFromChoice(choice, fallbackBrowserID)
		if ok {
			items = append(items, item)
		}
	}
	switch typed := value.(type) {
	case []map[string]any:
		for _, choice := range typed {
			appendChoice(choice)
		}
	case []any:
		for _, item := range typed {
			if choice, ok := item.(map[string]any); ok {
				appendChoice(choice)
			}
		}
	}
	return items
}

func ambiguityMetadataFromChoice(choice map[string]any, fallbackBrowserID string) (ambiguityMetadata, bool) {
	targetID := safeAmbiguityID(stringValue(choice["target_id"]))
	if targetID == "" {
		return ambiguityMetadata{}, false
	}
	browserID := safeAmbiguityID(stringValue(choice["browser_id"]))
	if browserID == "" {
		browserID = safeAmbiguityID(fallbackBrowserID)
	}
	return ambiguityMetadata{
		browserID: browserID,
		targetID:  targetID,
		title:     safeAmbiguityTitle(stringValue(choice["title"])),
		origin:    safeAmbiguityOrigin(stringValue(choice["origin"])),
	}, true
}

func sortAmbiguityChoices(items []ambiguityMetadata) {
	sort.SliceStable(items, func(i, j int) bool {
		left, right := items[i], items[j]
		if left.targetID != right.targetID {
			return left.targetID < right.targetID
		}
		if left.browserID != right.browserID {
			return left.browserID < right.browserID
		}
		if left.title != right.title {
			return left.title < right.title
		}
		return left.origin < right.origin
	})
}

func ambiguityCandidateIDs(candidateIDs []string, items []ambiguityMetadata) []string {
	ids := append([]string(nil), candidateIDs...)
	if len(ids) == 0 {
		for _, item := range items {
			ids = append(ids, item.targetID)
		}
	}
	return boundedAmbiguityIDs(anyAmbiguityIDs(ids))
}

func renderAmbiguityChoices(ids []string, items []ambiguityMetadata, fallbackBrowserID string) []map[string]any {
	byID := make(map[string]ambiguityMetadata, len(items))
	for _, item := range items {
		if _, exists := byID[item.targetID]; !exists {
			byID[item.targetID] = item
		}
	}
	result := make([]map[string]any, 0, len(ids))
	for _, targetID := range ids {
		result = append(result, renderAmbiguityChoice(targetID, byID[targetID], fallbackBrowserID))
	}
	return result
}

func renderAmbiguityChoice(targetID string, item ambiguityMetadata, fallbackBrowserID string) map[string]any {
	browserID := item.browserID
	if browserID == "" {
		browserID = safeAmbiguityID(fallbackBrowserID)
	}
	choice := map[string]any{"browser_id": browserID, "target_id": targetID}
	if item.title != "" {
		choice["title"] = item.title
	}
	if item.origin != "" {
		choice["origin"] = item.origin
	}
	return choice
}

func safeAmbiguityTitle(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if len(value) > maxAmbiguityTitle {
		value = value[:maxAmbiguityTitle]
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return "redacted"
		}
	}
	if strings.Contains(value, "://") || strings.ContainsAny(value, "?#@") {
		return "redacted"
	}
	return value
}

func safeAmbiguityOrigin(value string) string {
	parsed, ok := parseAmbiguityOrigin(value)
	if !ok {
		return ""
	}
	scheme, ok := ambiguityOriginScheme(parsed)
	if !ok {
		return ""
	}
	port, ok := ambiguityOriginPort(parsed, scheme)
	if !ok {
		return ""
	}
	host := ambiguityOriginHost(parsed)
	origin := scheme + "://" + host
	if port != "" {
		origin += ":" + port
	}
	if len(origin) > maxAmbiguityOrigin {
		return ""
	}
	return origin
}

func parseAmbiguityOrigin(value string) (*url.URL, bool) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 4096 {
		return nil, false
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed == nil || parsed.User != nil {
		return nil, false
	}
	return parsed, true
}

func ambiguityOriginScheme(parsed *url.URL) (string, bool) {
	scheme := strings.ToLower(parsed.Scheme)
	if (scheme != "http" && scheme != "https") || parsed.Hostname() == "" {
		return "", false
	}
	return scheme, true
}

func ambiguityOriginPort(parsed *url.URL, scheme string) (string, bool) {
	port := parsed.Port()
	if port == "" {
		return "", true
	}
	value, err := strconv.Atoi(port)
	if err != nil || value < 1 || value > 65535 {
		return "", false
	}
	if scheme == "http" && port == "80" || scheme == "https" && port == "443" {
		return "", true
	}
	return port, true
}

func ambiguityOriginHost(parsed *url.URL) string {
	host := strings.ToLower(parsed.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return host
}

func stringValue(value any) string {
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return text
}
