package chatgptauth

import (
	"bytes"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestPKCEChallengeMatchesTheRFC7636Vector(t *testing.T) {
	if got := challengeS256("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"); got != "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM" {
		t.Fatalf("challenge = %q", got)
	}
	pkce, err := NewPKCE(bytes.NewReader(bytes.Repeat([]byte{7}, pkceVerifierBytes)))
	if err != nil {
		t.Fatalf("NewPKCE: %v", err)
	}
	if len(pkce.Verifier) != 86 || pkce.Challenge != challengeS256(pkce.Verifier) {
		t.Fatalf("pkce = %+v", pkce)
	}
	if _, err := NewPKCE(bytes.NewReader(nil)); err == nil {
		t.Fatal("NewPKCE with no entropy succeeded")
	}
	if _, err := NewState(bytes.NewReader(nil)); err == nil {
		t.Fatal("NewState with no entropy succeeded")
	}
}

func TestAuthorizeURLCarriesTheCodexLoginParameters(t *testing.T) {
	client := NewClient(Config{Issuer: "https://issuer.test/", Originator: "tester"})
	raw := client.AuthorizeURL("http://localhost:1455/auth/callback", PKCE{Challenge: "chal"}, "st")
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed.Scheme+"://"+parsed.Host+parsed.Path != "https://issuer.test/oauth/authorize" {
		t.Fatalf("authorize endpoint = %q", raw)
	}
	want := url.Values{
		"response_type": {"code"}, "client_id": {DefaultClientID}, "redirect_uri": {"http://localhost:1455/auth/callback"},
		"scope": {DefaultScope}, "code_challenge": {"chal"}, "code_challenge_method": {"S256"},
		"id_token_add_organizations": {"true"}, "codex_cli_simplified_flow": {"true"}, "state": {"st"}, "originator": {"tester"},
	}
	if got := parsed.Query(); got.Encode() != want.Encode() {
		t.Fatalf("query = %v\nwant %v", got, want)
	}
}

func TestRefreshRotatesTokensAndKeepsWhatTheIssuerOmits(t *testing.T) {
	clock := newVirtualClock()
	issuer := newFakeIssuer(t)
	access := testJWT(t, identityClaims("", "acct_2", "pro", testEpoch().Add(2*time.Hour)))
	issuer.queueToken(issuerReply{status: http.StatusOK, body: map[string]any{"access_token": access}})
	previous := Credential{ClientID: "client-old", IDToken: "id-old", AccessToken: "a-old", RefreshToken: "r-old", Email: "old@example.com"}

	cred, err := issuer.client(clock).Refresh(t.Context(), previous)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	form := issuer.forms()[0]
	if form.Get("grant_type") != "refresh_token" || form.Get("refresh_token") != "r-old" || form.Get("client_id") != "client-old" {
		t.Fatalf("refresh form = %v", form)
	}
	want := Credential{
		Issuer: issuer.server.URL, ClientID: "client-old", IDToken: "id-old", AccessToken: access, RefreshToken: "r-old",
		AccountID: "acct_2", Email: "old@example.com", PlanType: "pro", ExpiresAt: testEpoch().Add(2 * time.Hour), LastRefresh: testEpoch(),
	}
	if cred != want {
		t.Fatalf("refreshed = %+v\nwant %+v", cred, want)
	}
}

func TestRefreshFailureClassification(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		body      any
		permanent bool
	}{
		{name: "reused", status: http.StatusBadRequest, body: map[string]any{"error": map[string]string{"code": "refresh_token_reused", "message": "used"}}, permanent: true},
		{name: "expired", status: http.StatusBadRequest, body: map[string]string{"error": "refresh_token_expired"}, permanent: true},
		{name: "invalidated", status: http.StatusBadRequest, body: map[string]string{"code": "refresh_token_invalidated"}, permanent: true},
		{name: "invalid grant", status: http.StatusBadRequest, body: map[string]string{"error": "invalid_grant", "error_description": "bad"}, permanent: true},
		{name: "unauthorized", status: http.StatusUnauthorized, body: map[string]string{}, permanent: true},
		{name: "server error", status: http.StatusBadGateway, body: map[string]string{"message": "upstream"}},
		{name: "rate limited", status: http.StatusTooManyRequests, body: map[string]string{"error": "rate_limited"}},
		{name: "missing access token", status: http.StatusOK, body: map[string]string{"refresh_token": "r"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issuer := newFakeIssuer(t)
			issuer.queueToken(issuerReply{status: tt.status, body: tt.body})
			_, err := issuer.client(newVirtualClock()).Refresh(t.Context(), Credential{RefreshToken: "r"})
			var tokenErr *TokenError
			if !errors.As(err, &tokenErr) {
				t.Fatalf("error = %v, want *TokenError", err)
			}
			if errors.Is(err, ErrReauthRequired) != tt.permanent {
				t.Fatalf("errors.Is(%v, ErrReauthRequired) = %v, want %v", err, !tt.permanent, tt.permanent)
			}
		})
	}
	_, err := NewClient(Config{}).Refresh(t.Context(), Credential{AccessToken: "a"})
	if !errors.Is(err, ErrReauthRequired) {
		t.Fatalf("refresh without refresh token = %v, want ErrReauthRequired", err)
	}
}

func TestExchangeRejectsIncompleteOrInvalidTokenResponses(t *testing.T) {
	tests := []struct {
		name  string
		reply issuerReply
		want  string
	}{
		{name: "no refresh token", reply: issuerReply{status: http.StatusOK, body: map[string]string{"access_token": "a"}}, want: "missing access_token or refresh_token"},
		{name: "not json", reply: issuerReply{status: http.StatusOK, body: "text"}, want: "not valid JSON"},
		{name: "bad code", reply: issuerReply{status: http.StatusBadRequest, body: map[string]string{"error": "invalid_grant"}}, want: "exchange failed (HTTP 400): invalid_grant"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issuer := newFakeIssuer(t)
			issuer.queueToken(tt.reply)
			_, err := issuer.client(newVirtualClock()).ExchangeCode(t.Context(), "c", "v", "http://localhost:1455/auth/callback")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
			if errors.Is(err, ErrReauthRequired) {
				t.Fatalf("an exchange failure must not be classified as a refresh failure: %v", err)
			}
		})
	}
}

func TestRevokeSendsTheRefreshTokenOrFallsBackToTheAccessToken(t *testing.T) {
	issuer := newFakeIssuer(t)
	client := issuer.client(newVirtualClock())
	if err := client.Revoke(t.Context(), Credential{RefreshToken: "r", AccessToken: "a"}); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if err := client.Revoke(t.Context(), Credential{AccessToken: "a"}); err != nil {
		t.Fatalf("Revoke access: %v", err)
	}
	if err := client.Revoke(t.Context(), Credential{}); err != nil {
		t.Fatalf("Revoke empty: %v", err)
	}
	if len(issuer.revokeBodies) != 2 {
		t.Fatalf("revoke requests = %v, want two", issuer.revokeBodies)
	}
	first, second := issuer.revokeBodies[0], issuer.revokeBodies[1]
	if first["token"] != "r" || first["token_type_hint"] != "refresh_token" || first["client_id"] != DefaultClientID {
		t.Fatalf("refresh revoke = %v", first)
	}
	if second["token"] != "a" || second["token_type_hint"] != "access_token" {
		t.Fatalf("access revoke = %v", second)
	}
	issuer.revokeStatus = http.StatusServiceUnavailable
	if err := client.Revoke(t.Context(), Credential{RefreshToken: "r"}); err == nil || !strings.Contains(err.Error(), "revoke failed (HTTP 503)") {
		t.Fatalf("failed revoke = %v", err)
	}
}

func TestCredentialRefreshPolicy(t *testing.T) {
	now := testEpoch()
	tests := []struct {
		name string
		cred Credential
		want bool
	}{
		{name: "fresh", cred: Credential{AccessToken: "a", ExpiresAt: now.Add(RefreshWindow + time.Second)}},
		{name: "inside the window", cred: Credential{AccessToken: "a", ExpiresAt: now.Add(RefreshWindow)}, want: true},
		{name: "unknown expiry, recent", cred: Credential{AccessToken: "a", LastRefresh: now.Add(-time.Hour)}},
		{name: "unknown expiry, stale", cred: Credential{AccessToken: "a", LastRefresh: now.Add(-StaleRefreshAge)}, want: true},
		{name: "no access token", cred: Credential{RefreshToken: "r"}, want: true},
	}
	for _, tt := range tests {
		if got := tt.cred.NeedsRefresh(now); got != tt.want {
			t.Errorf("%s: NeedsRefresh = %v, want %v", tt.name, got, tt.want)
		}
	}
	if (Credential{ExpiresAt: now}).Expired(now) != true || (Credential{}).Expired(now) {
		t.Fatal("Expired disagrees with the expiry")
	}
}

func TestClaimsParsingToleratesMalformedTokens(t *testing.T) {
	for _, token := range []string{"", "a.b", "a..c", "a.!!!.c", "a.bm90LWpzb24.c"} {
		if _, ok := parseClaims(token); ok {
			t.Errorf("parseClaims(%q) succeeded", token)
		}
	}
	claims, ok := parseClaims(testJWT(t, map[string]any{"https://api.openai.com/profile": map[string]string{"email": "p@example.com"}}))
	if !ok || claims.email() != "p@example.com" || !claims.expiry().IsZero() {
		t.Fatalf("profile claims = %+v (%v)", claims, ok)
	}
	if got := truncateDetail(strings.Repeat("é", maxErrorDetailRune+1)); !strings.HasSuffix(got, "...") {
		t.Fatalf("truncateDetail did not truncate: %q", got)
	}
}
