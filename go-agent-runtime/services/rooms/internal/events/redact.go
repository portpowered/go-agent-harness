package events

import "strings"

// RedactedMarker replaces every credential occurrence, matching the marker
// room evidence writes.
const RedactedMarker = "[REDACTED]"

// Redactor replaces resolved credential values in projected text.
type Redactor struct{ secrets []string }

// NewRedactor keeps the non-empty secrets in the supplied order; the planner
// orders them longest first so a secret containing another is never left
// partially visible.
func NewRedactor(secrets []string) Redactor {
	kept := make([]string, 0, len(secrets))
	for _, secret := range secrets {
		if secret != "" {
			kept = append(kept, secret)
		}
	}
	return Redactor{secrets: kept}
}

// Redact returns value with every secret replaced by RedactedMarker.
func (r Redactor) Redact(value string) string {
	for _, secret := range r.secrets {
		value = strings.ReplaceAll(value, secret, RedactedMarker)
	}
	return value
}
