package chatgptauth

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"
)

const jwtParts = 3

// tokenClaims is the subset of OpenAI token claims the credential records.
// Signatures are not verified: the token is only presented back to OpenAI
// as a bearer, and the claims are used for display and the account header.
type tokenClaims struct {
	Exp     int64  `json:"exp"`
	Email   string `json:"email"`
	Profile struct {
		Email string `json:"email"`
	} `json:"https://api.openai.com/profile"`
	Auth struct {
		AccountID string `json:"chatgpt_account_id"`
		PlanType  string `json:"chatgpt_plan_type"`
	} `json:"https://api.openai.com/auth"`
}

func parseClaims(token string) (tokenClaims, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != jwtParts || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return tokenClaims{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return tokenClaims{}, false
	}
	var claims tokenClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return tokenClaims{}, false
	}
	return claims, true
}

func (c tokenClaims) email() string {
	if c.Email != "" {
		return c.Email
	}
	return c.Profile.Email
}

func (c tokenClaims) expiry() time.Time {
	if c.Exp <= 0 {
		return time.Time{}
	}
	return time.Unix(c.Exp, 0).UTC()
}

// withClaims fills identity fields from the id token first and the access
// token second (Codex reads the id token, OpenClaw the access token), and the
// expiry from the access token when the token response gave none.
func withClaims(c Credential) Credential {
	idClaims, idOK := parseClaims(c.IDToken)
	accessClaims, accessOK := parseClaims(c.AccessToken)
	for _, claims := range []struct {
		ok     bool
		claims tokenClaims
	}{{idOK, idClaims}, {accessOK, accessClaims}} {
		if !claims.ok {
			continue
		}
		c.AccountID = firstNonEmpty(c.AccountID, claims.claims.Auth.AccountID)
		c.Email = firstNonEmpty(c.Email, claims.claims.email())
		c.PlanType = firstNonEmpty(c.PlanType, claims.claims.Auth.PlanType)
	}
	if c.ExpiresAt.IsZero() && accessOK {
		c.ExpiresAt = accessClaims.expiry()
	}
	return c
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
