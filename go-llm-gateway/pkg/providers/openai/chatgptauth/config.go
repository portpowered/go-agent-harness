// Package chatgptauth signs a user in with a ChatGPT account and keeps the
// resulting OAuth credential fresh.
//
// The flow is the one Codex CLI and OpenClaw use: OAuth 2.0 authorization
// code with PKCE (S256) against auth.openai.com, a loopback redirect on port
// 1455 (fallback 1457), a device-code fallback for headless hosts, and
// rotating refresh tokens. The credential authorizes the ChatGPT backend
// (https://chatgpt.com/backend-api/codex), not the Platform API. See
// docs/architecture/chatgpt-oauth.md.
package chatgptauth

import (
	"net/http"
	"strings"
	"time"
)

const (
	// DefaultIssuer is the OpenAI OAuth issuer.
	DefaultIssuer = "https://auth.openai.com"
	// DefaultClientID is the public Codex CLI OAuth client id, which is the
	// one client whose redirect allow-list includes the loopback ports.
	DefaultClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
	// DefaultScope requests identity plus a refresh token, nothing more.
	DefaultScope = "openid profile email offline_access"
	// DefaultOriginator names this client to the issuer, as OpenClaw does
	// with its own name instead of reusing Codex's originator.
	DefaultOriginator = "yui"
	// DefaultCallbackPort is the registered loopback redirect port.
	DefaultCallbackPort = 1455
	// FallbackCallbackPort is the second registered loopback redirect port.
	FallbackCallbackPort = 1457

	defaultHTTPTimeout = 30 * time.Second
	authorizePath      = "/oauth/authorize"
	tokenPath          = "/oauth/token"
	revokePath         = "/oauth/revoke"
	deviceUserCodePath = "/api/accounts/deviceauth/usercode"
	deviceTokenPath    = "/api/accounts/deviceauth/token"
	deviceVerifyPath   = "/codex/device"
	deviceCallbackPath = "/deviceauth/callback"
)

// Config selects the issuer, client and effects a Client uses. The zero
// value is valid: every empty field takes its default.
type Config struct {
	// Issuer is the OAuth issuer base URL (default DefaultIssuer).
	Issuer string
	// ClientID is the OAuth client id (default DefaultClientID).
	ClientID string
	// Scope is the space-separated scope list (default DefaultScope).
	Scope string
	// Originator is sent as the authorize originator parameter (default
	// DefaultOriginator).
	Originator string
	// HTTPClient performs issuer requests (default: a 30 s timeout client).
	HTTPClient *http.Client
	// Now is the clock used for expiry and deadlines (default time.Now).
	Now func() time.Time
}

func (c Config) withDefaults() Config {
	if strings.TrimSpace(c.Issuer) == "" {
		c.Issuer = DefaultIssuer
	}
	c.Issuer = strings.TrimRight(c.Issuer, "/")
	if strings.TrimSpace(c.ClientID) == "" {
		c.ClientID = DefaultClientID
	}
	if strings.TrimSpace(c.Scope) == "" {
		c.Scope = DefaultScope
	}
	if strings.TrimSpace(c.Originator) == "" {
		c.Originator = DefaultOriginator
	}
	if c.HTTPClient == nil {
		c.HTTPClient = &http.Client{Timeout: defaultHTTPTimeout}
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return c
}
