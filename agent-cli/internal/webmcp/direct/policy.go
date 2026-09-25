package direct

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/production/normalize"
)

const (
	// PolicyDeniedOrigins names the denied-origin policy in error details.
	PolicyDeniedOrigins = "denied_origins"
	// PolicyAllowedOrigins names the allowed-origin policy in error details.
	PolicyAllowedOrigins = "allowed_origins"

	detailOriginDigest = "origin_digest"
	detailPolicy       = "policy"
)

// TargetPolicyError reports whether target's origin is denied by, or absent
// from, the configured origin policy. The origin itself never crosses the
// error boundary; only its digest does.
func TargetPolicyError(target webmcp.Target, browser config.BrowserConfig) error {
	origin := normalize.RedactedOrigin(target.Origin)
	if DeniedOrigin(origin, browser.Policy) {
		return webmcp.NewClassifiedError(webmcp.ErrorOriginDenied, "the selected page origin is denied by policy", map[string]any{
			detailOriginDigest: OriginDigest(origin),
			detailPolicy:       PolicyDeniedOrigins,
		})
	}
	if len(browser.Policy.AllowedOrigins) > 0 && !AllowedOrigin(origin, browser.Policy.AllowedOrigins) {
		return webmcp.NewClassifiedError(webmcp.ErrorOriginDenied, "the selected page origin is not in the allowed policy", map[string]any{
			detailOriginDigest: OriginDigest(origin),
			detailPolicy:       PolicyAllowedOrigins,
		})
	}
	return nil
}

// OriginDeniedError is the doctor-style origin denial, naming whichever
// policy excluded origin.
func OriginDeniedError(origin string, policy config.BrowserPolicyConfig) error {
	return webmcp.NewClassifiedError(webmcp.ErrorOriginDenied, "the selected page origin is denied by policy", map[string]any{
		detailOriginDigest: OriginDigest(origin),
		detailPolicy:       OriginPolicyName(origin, policy),
	})
}

// DeniedOrigin reports whether the redacted origin is on the deny list.
func DeniedOrigin(origin string, policy config.BrowserPolicyConfig) bool {
	for _, denied := range policy.DeniedOrigins {
		if normalize.RedactedOrigin(denied) == origin {
			return true
		}
	}
	return false
}

// AllowedOrigin reports whether the redacted origin is on the allow list.
func AllowedOrigin(origin string, allowed []string) bool {
	for _, value := range allowed {
		if normalize.RedactedOrigin(value) == origin {
			return true
		}
	}
	return false
}

// OriginPolicyName names the policy that excluded origin.
func OriginPolicyName(origin string, policy config.BrowserPolicyConfig) string {
	if DeniedOrigin(origin, policy) {
		return PolicyDeniedOrigins
	}
	return PolicyAllowedOrigins
}

// OriginDigest is the hex SHA-256 of origin, used in place of the origin in
// policy error details.
func OriginDigest(origin string) string {
	digest := sha256.Sum256([]byte(origin))
	return hex.EncodeToString(digest[:])
}

// EndpointKind names the configured endpoint lane for error details.
func EndpointKind(browser config.BrowserConfig) string {
	switch {
	case browser.Connection.CDPURL != "":
		return "http"
	case browser.Connection.WSEndpoint != "":
		return "websocket"
	case browser.Connection.UserDataDir != "":
		return "profile"
	default:
		return "discovery"
	}
}
