package chatgptauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func testEpoch() time.Time { return time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC) }

// virtualClock is the test clock: it moves only when a test or a virtual
// sleeper advances it.
type virtualClock struct {
	mu  sync.Mutex
	now time.Time
}

func newVirtualClock() *virtualClock { return &virtualClock{now: testEpoch()} }

func (c *virtualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *virtualClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// sleeper advances the clock instead of waiting and records each wait.
func (c *virtualClock) sleeper(waits *[]time.Duration) Sleeper {
	return func(ctx context.Context, d time.Duration) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		*waits = append(*waits, d)
		c.Advance(d)
		return nil
	}
}

// testJWT builds an unsigned JWT-shaped token carrying claims.
func testJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("encode claims: %v", err)
	}
	return "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

func identityClaims(email, account, plan string, exp time.Time) map[string]any {
	claims := map[string]any{
		"email": email,
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": account,
			"chatgpt_plan_type":  plan,
		},
	}
	if !exp.IsZero() {
		claims["exp"] = exp.Unix()
	}
	return claims
}

// issuerReply is one scripted token-endpoint answer.
type issuerReply struct {
	status int
	body   any
}

// fakeIssuer is an httptest OAuth issuer that records requests and answers
// from per-endpoint scripts. No real network is involved.
type fakeIssuer struct {
	t      *testing.T
	server *httptest.Server

	mu            sync.Mutex
	tokenForms    []url.Values
	tokenReplies  []issuerReply
	revokeBodies  []map[string]string
	revokeStatus  int
	deviceReplies []issuerReply
	userCodeReply issuerReply
	devicePolls   int
	wantVerifier  string
}

func newFakeIssuer(t *testing.T) *fakeIssuer {
	t.Helper()
	f := &fakeIssuer{t: t, revokeStatus: http.StatusOK}
	mux := http.NewServeMux()
	mux.HandleFunc(tokenPath, f.handleToken)
	mux.HandleFunc(revokePath, f.handleRevoke)
	mux.HandleFunc(deviceUserCodePath, f.handleUserCode)
	mux.HandleFunc(deviceTokenPath, f.handleDeviceToken)
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeIssuer) client(clock *virtualClock) *Client {
	return NewClient(Config{Issuer: f.server.URL, HTTPClient: f.server.Client(), Now: clock.Now})
}

func (f *fakeIssuer) queueToken(replies ...issuerReply) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokenReplies = append(f.tokenReplies, replies...)
}

func (f *fakeIssuer) forms() []url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]url.Values(nil), f.tokenForms...)
}

func (f *fakeIssuer) handleToken(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get(contentTypeHeader) != formContentType {
		f.t.Errorf("token request content type = %q", r.Header.Get(contentTypeHeader))
	}
	if err := r.ParseForm(); err != nil {
		f.t.Errorf("parse token form: %v", err)
	}
	f.mu.Lock()
	f.tokenForms = append(f.tokenForms, r.PostForm)
	if f.wantVerifier != "" && r.PostForm.Get("code_verifier") != f.wantVerifier {
		f.mu.Unlock()
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
		return
	}
	if len(f.tokenReplies) == 0 {
		f.mu.Unlock()
		f.t.Errorf("unexpected token request %v", r.PostForm)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "unscripted"})
		return
	}
	reply := f.tokenReplies[0]
	f.tokenReplies = f.tokenReplies[1:]
	f.mu.Unlock()
	writeJSON(w, reply.status, reply.body)
}

func (f *fakeIssuer) handleRevoke(w http.ResponseWriter, r *http.Request) {
	var body map[string]string
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		f.t.Errorf("decode revoke body: %v", err)
	}
	f.mu.Lock()
	f.revokeBodies = append(f.revokeBodies, body)
	status := f.revokeStatus
	f.mu.Unlock()
	writeJSON(w, status, map[string]string{})
}

func (f *fakeIssuer) handleUserCode(w http.ResponseWriter, r *http.Request) {
	payload, err := io.ReadAll(r.Body)
	if err != nil || !strings.Contains(string(payload), DefaultClientID) {
		f.t.Errorf("user code body = %q (%v), want the client id", payload, err)
	}
	f.mu.Lock()
	reply := f.userCodeReply
	f.mu.Unlock()
	writeJSON(w, reply.status, reply.body)
}

func (f *fakeIssuer) handleDeviceToken(w http.ResponseWriter, r *http.Request) {
	var body map[string]string
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		f.t.Errorf("decode device poll: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.devicePolls++
	if body["device_auth_id"] != "dev_123" || body["user_code"] != "ABCD-1234" {
		f.t.Errorf("device poll body = %v", body)
	}
	reply := issuerReply{status: http.StatusForbidden, body: map[string]string{"status": "pending"}}
	if len(f.deviceReplies) > 0 {
		reply = f.deviceReplies[0]
		f.deviceReplies = f.deviceReplies[1:]
	}
	writeJSON(w, reply.status, reply.body)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set(contentTypeHeader, jsonContentType)
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		return
	}
}

// tokenBody is a successful token-endpoint body for the given identity.
func tokenBody(t *testing.T, access, refresh string, expiresIn int) map[string]any {
	t.Helper()
	body := map[string]any{
		"access_token": access,
		"id_token":     testJWT(t, identityClaims("user@example.com", "acct_1", "plus", time.Time{})),
		"expires_in":   expiresIn,
	}
	if refresh != "" {
		body["refresh_token"] = refresh
	}
	return body
}
