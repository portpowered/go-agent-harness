package chatgptauth

import (
	"errors"
	"time"
)

const (
	// RefreshWindow is how long before expiry a credential is refreshed
	// (Codex refreshes access tokens that expire within five minutes).
	RefreshWindow = 5 * time.Minute
	// StaleRefreshAge refreshes a credential whose expiry is unknown once its
	// last refresh is this old (Codex's eight-day fallback).
	StaleRefreshAge = 8 * 24 * time.Hour
)

var (
	// ErrNotLoggedIn reports that no ChatGPT credential is stored.
	ErrNotLoggedIn = errors.New("not signed in with ChatGPT: run `yui auth chatgpt`")
	// ErrReauthRequired reports that the stored refresh token can no longer
	// be used (expired, reused or revoked) and the user must sign in again.
	ErrReauthRequired = errors.New("ChatGPT sign-in expired: run `yui auth chatgpt` again")
	// ErrLoginCancelled reports that a login was cancelled before it
	// produced an authorization code.
	ErrLoginCancelled = errors.New("ChatGPT login was cancelled")
)

// Credential is a ChatGPT OAuth credential and the identity it carries.
type Credential struct {
	// Issuer and ClientID are the OAuth client the tokens belong to; refresh
	// and revoke must use the same client.
	Issuer   string
	ClientID string

	IDToken      string
	AccessToken  string
	RefreshToken string

	// AccountID is the chatgpt_account_id claim, sent as the
	// chatgpt-account-id header on ChatGPT backend requests.
	AccountID string
	Email     string
	PlanType  string

	// ExpiresAt is the access-token expiry; zero when unknown.
	ExpiresAt time.Time
	// LastRefresh is when the tokens were last issued.
	LastRefresh time.Time
}

// Expired reports whether the access token is past its known expiry.
func (c Credential) Expired(now time.Time) bool {
	return !c.ExpiresAt.IsZero() && !now.Before(c.ExpiresAt)
}

// NeedsRefresh reports whether the credential should be refreshed at now:
// within RefreshWindow of a known expiry, or, with no known expiry, once the
// last refresh is older than StaleRefreshAge.
func (c Credential) NeedsRefresh(now time.Time) bool {
	if c.AccessToken == "" {
		return true
	}
	if !c.ExpiresAt.IsZero() {
		return !now.Before(c.ExpiresAt.Add(-RefreshWindow))
	}
	return c.LastRefresh.IsZero() || now.Sub(c.LastRefresh) >= StaleRefreshAge
}
