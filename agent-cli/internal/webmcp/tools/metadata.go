package tools

import (
	"net/url"
	"strconv"
	"strings"
)

// safePageMetadata re-applies the discovery boundary before page metadata is
// emitted. Discovery.Service already returns normalized values, but the tool
// seam is intentionally injectable and must not turn an untrusted fake or
// future adapter into a credential/query leak.
func safePageMetadata(rawURL, rawOrigin string) (string, string) {
	if strings.TrimSpace(rawURL) != "" {
		return normalizeToolPageURL(rawURL)
	}
	if strings.TrimSpace(rawOrigin) == "" {
		return "", ""
	}
	_, origin := normalizeToolPageURL(rawOrigin)
	return "", origin
}

func normalizeToolPageURL(raw string) (string, string) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", ""
	}
	if len(trimmed) > 4096 || hasToolControl(trimmed) {
		return "redacted", ""
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed == nil {
		return "redacted", ""
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if parsed.Scheme != "http" && parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil {
		return "redacted", ""
	}
	if port := parsed.Port(); port != "" {
		value, err := strconv.Atoi(port)
		if err != nil || value < 1 || value > 65535 {
			return "redacted", ""
		}
	}
	host := strings.ToLower(parsed.Hostname())
	port := parsed.Port()
	if port == "" || parsed.Scheme == "http" && port == "80" || parsed.Scheme == "https" && port == "443" {
		port = ""
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	parsed.Host = host
	if port != "" {
		parsed.Host += ":" + port
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	safeURL := parsed.String()
	if safeURL == "" || len(safeURL) > 4096 || hasToolControl(safeURL) {
		return "redacted", ""
	}
	return safeURL, parsed.Scheme + "://" + parsed.Host
}

func hasToolControl(value string) bool {
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}
