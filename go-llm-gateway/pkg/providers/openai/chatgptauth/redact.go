package chatgptauth

import (
	"fmt"
	"time"
)

// Every type that holds a token, code, verifier or state formats itself
// without it, so %v, %+v, %s and %#v never leak a secret into logs or test
// failures. Identity fields (account id, email, plan) stay visible.

const redactedMark = "[REDACTED]"

func redacted(secret string) string {
	if secret == "" {
		return `""`
	}
	return redactedMark
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	return t.UTC().Format(time.RFC3339)
}

// String describes the credential with every token redacted.
func (c Credential) String() string {
	return fmt.Sprintf("Credential{Issuer:%q ClientID:%q AccountID:%q Email:%q PlanType:%q ExpiresAt:%s LastRefresh:%s IDToken:%s AccessToken:%s RefreshToken:%s}",
		c.Issuer, c.ClientID, c.AccountID, c.Email, c.PlanType, formatTime(c.ExpiresAt), formatTime(c.LastRefresh),
		redacted(c.IDToken), redacted(c.AccessToken), redacted(c.RefreshToken))
}

// GoString is String, so %#v redacts too.
func (c Credential) GoString() string { return "chatgptauth." + c.String() }

// String describes the PKCE pair with the verifier redacted. The challenge
// is public: it travels in the authorize URL.
func (p PKCE) String() string {
	return fmt.Sprintf("PKCE{Verifier:%s Challenge:%q}", redacted(p.Verifier), p.Challenge)
}

// GoString is String, so %#v redacts too.
func (p PKCE) GoString() string { return "chatgptauth." + p.String() }

// String describes the device code with its device authorization id
// redacted. The user code is shown to the user by design.
func (d DeviceCode) String() string {
	return fmt.Sprintf("DeviceCode{VerificationURL:%q UserCode:%q Interval:%s Deadline:%s deviceAuthID:%s}",
		d.VerificationURL, d.UserCode, d.Interval, formatTime(d.Deadline), redacted(d.deviceAuthID))
}

// GoString is String, so %#v redacts too.
func (d DeviceCode) GoString() string { return "chatgptauth." + d.String() }

// String describes the listener without its OAuth state.
func (s *CallbackServer) String() string {
	return fmt.Sprintf("CallbackServer{RedirectURI:%q state:%s}", s.RedirectURI(), redacted(s.state))
}

// GoString is String, so %#v redacts too.
func (s *CallbackServer) GoString() string { return "chatgptauth." + s.String() }

func (r callbackResult) String() string {
	return fmt.Sprintf("callbackResult{code:%s err:%v}", redacted(r.code), r.err)
}

func (r callbackResult) GoString() string { return r.String() }

func (r tokenResponse) String() string {
	return fmt.Sprintf("tokenResponse{IDToken:%s AccessToken:%s RefreshToken:%s ExpiresIn:%d}",
		redacted(r.IDToken), redacted(r.AccessToken), redacted(r.RefreshToken), r.ExpiresIn)
}

func (r tokenResponse) GoString() string { return r.String() }

func (r deviceUserCodeResponse) String() string {
	return fmt.Sprintf("deviceUserCodeResponse{DeviceAuthID:%s UserCode:%q}", redacted(r.DeviceAuthID), firstNonEmpty(r.UserCode, r.UserCodeAlt))
}

func (r deviceUserCodeResponse) GoString() string { return r.String() }

func (r deviceTokenResponse) String() string {
	return fmt.Sprintf("deviceTokenResponse{AuthorizationCode:%s CodeVerifier:%s}", redacted(r.AuthorizationCode), redacted(r.CodeVerifier))
}

func (r deviceTokenResponse) GoString() string { return r.String() }

func (t storedTokens) String() string {
	return fmt.Sprintf("storedTokens{IDToken:%s AccessToken:%s RefreshToken:%s}", redacted(t.IDToken), redacted(t.AccessToken), redacted(t.RefreshToken))
}

func (t storedTokens) GoString() string { return t.String() }

func (s storedCredential) String() string { return "stored" + s.credential().String() }

func (s storedCredential) GoString() string { return s.String() }
