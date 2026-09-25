package normalize

import (
	"net/url"
	"strings"
)

// maxFallbackOriginLength bounds an origin value that is not a parseable
// URL once its query and fragment have been removed.
const maxFallbackOriginLength = 200

// RedactedOrigin is the display and persistence origin redaction shared by
// the doctor report, direct commands, and the selection store. A parseable
// URL reduces to its lower-cased scheme://host, while any other value loses
// its query and fragment and is bounded to printable text.
func RedactedOrigin(raw string) string {
	parsed, err := url.Parse(raw)
	if err == nil && parsed.Scheme != "" && parsed.Host != "" {
		return strings.ToLower(parsed.Scheme) + "://" + strings.ToLower(parsed.Host)
	}
	cleaned := raw
	if index := strings.IndexAny(cleaned, "?#"); index >= 0 {
		cleaned = cleaned[:index]
	}
	return BoundedText(cleaned, maxFallbackOriginLength)
}

// BoundedText removes control characters other than newline, carriage
// return, and tab, then truncates value to at most limit bytes. A
// non-positive limit leaves the length unbounded.
func BoundedText(value string, limit int) string {
	value = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' || r >= ' ' {
			return r
		}
		return -1
	}, value)
	if limit > 0 && len(value) > limit {
		return value[:limit]
	}
	return value
}
