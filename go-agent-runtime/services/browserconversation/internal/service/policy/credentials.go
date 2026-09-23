package policy

import "strings"

const RedactedText = "[redacted]"

func ContainsCredentialMarker(value string) bool {
	lower := strings.ToLower(value)
	for _, marker := range []string{"authorization:", "bearer ", "api_key", "api-key", "access_token", "refresh_token", "client_secret", "password", "-----begin ", "sk-"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}
