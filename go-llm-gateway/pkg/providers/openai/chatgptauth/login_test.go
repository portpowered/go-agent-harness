package chatgptauth

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeBrowser plays the user's browser: it checks the authorize URL, then
// follows the issuer's redirect to the loopback callback with the query the
// test chooses.
type fakeBrowser struct {
	t        *testing.T
	client   *http.Client
	redirect func(authorize url.Values) url.Values
	statuses []int
}

func (b *fakeBrowser) open(ctx context.Context, rawURL string) error {
	b.t.Helper()
	authorize, err := url.Parse(rawURL)
	if err != nil {
		return err
	}
	query := authorize.Query()
	redirect, err := url.Parse(query.Get("redirect_uri"))
	if err != nil {
		return err
	}
	// Reach the listener on its bound address; the redirect URI names
	// localhost, which is what the issuer allow-list holds.
	redirect.Host = net.JoinHostPort("127.0.0.1", redirect.Port())
	for _, extra := range []url.Values{{"state": {"forged"}, "code": {"stolen"}}, b.redirect(query)} {
		redirect.RawQuery = extra.Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, redirect.String(), nil)
		if err != nil {
			return err
		}
		resp, err := b.client.Do(req)
		if err != nil {
			return err
		}
		b.statuses = append(b.statuses, resp.StatusCode)
		if err := resp.Body.Close(); err != nil {
			return err
		}
	}
	return nil
}

func approveWithCode(code string) func(url.Values) url.Values {
	return func(authorize url.Values) url.Values {
		return url.Values{"code": {code}, "state": {authorize.Get("state")}}
	}
}

func TestBrowserLoginExchangesTheCallbackCodeWithItsVerifierAndIgnoresForgedState(t *testing.T) {
	clock := newVirtualClock()
	issuer := newFakeIssuer(t)
	issuer.queueToken(issuerReply{status: http.StatusOK, body: tokenBody(t, "access-1", "refresh-1", 3600)})
	browser := &fakeBrowser{t: t, client: &http.Client{}, redirect: approveWithCode("code-xyz")}
	var out bytes.Buffer

	cred, err := issuer.client(clock).LoginBrowser(t.Context(), BrowserLogin{Out: &out, OpenBrowser: browser.open, Ports: []int{0}})
	if err != nil {
		t.Fatalf("LoginBrowser: %v", err)
	}
	if got, want := browser.statuses, []int{http.StatusBadRequest, http.StatusOK}; len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("callback statuses = %v, want forged state rejected then success %v", got, want)
	}
	forms := issuer.forms()
	if len(forms) != 1 {
		t.Fatalf("token requests = %d, want 1", len(forms))
	}
	form := forms[0]
	if form.Get("grant_type") != "authorization_code" || form.Get("code") != "code-xyz" || form.Get("client_id") != DefaultClientID {
		t.Fatalf("exchange form = %v", form)
	}
	if !strings.HasPrefix(form.Get("redirect_uri"), "http://localhost:") || !strings.HasSuffix(form.Get("redirect_uri"), callbackPath) {
		t.Fatalf("redirect_uri = %q, want the localhost callback", form.Get("redirect_uri"))
	}
	if challengeS256(form.Get("code_verifier")) == "" || len(form.Get("code_verifier")) != 86 {
		t.Fatalf("code_verifier = %q, want an 86-character base64url verifier", form.Get("code_verifier"))
	}
	want := Credential{
		Issuer: issuer.server.URL, ClientID: DefaultClientID, AccessToken: "access-1", RefreshToken: "refresh-1",
		AccountID: "acct_1", Email: "user@example.com", PlanType: "plus",
		ExpiresAt: testEpoch().Add(time.Hour), LastRefresh: testEpoch(),
	}
	cred.IDToken = ""
	if cred != want {
		t.Fatalf("credential = %+v, want %+v", cred, want)
	}
	if !strings.Contains(out.String(), "/oauth/authorize?") {
		t.Fatalf("prompt = %q, want the authorize URL", out.String())
	}
}

func TestBrowserLoginSendsTheVerifierMatchingTheAuthorizeChallenge(t *testing.T) {
	clock := newVirtualClock()
	issuer := newFakeIssuer(t)
	issuer.queueToken(issuerReply{status: http.StatusOK, body: tokenBody(t, "access-1", "refresh-1", 60)})
	var challenge string
	browser := &fakeBrowser{t: t, client: &http.Client{}, redirect: func(authorize url.Values) url.Values {
		challenge = authorize.Get("code_challenge")
		if authorize.Get("code_challenge_method") != "S256" || authorize.Get("originator") != DefaultOriginator || authorize.Get("scope") != DefaultScope {
			t.Errorf("authorize query = %v", authorize)
		}
		return url.Values{"code": {"c"}, "state": {authorize.Get("state")}}
	}}
	if _, err := issuer.client(clock).LoginBrowser(t.Context(), BrowserLogin{OpenBrowser: browser.open, Ports: []int{0}}); err != nil {
		t.Fatalf("LoginBrowser: %v", err)
	}
	if got := challengeS256(issuer.forms()[0].Get("code_verifier")); got != challenge {
		t.Fatalf("S256(verifier) = %q, want the authorize challenge %q", got, challenge)
	}
}

func TestBrowserLoginReportsIssuerErrorsWithoutExchanging(t *testing.T) {
	tests := []struct {
		name        string
		description string
		want        string
	}{
		{name: "denied", description: "user said no", want: "ChatGPT sign-in failed: access_denied: user said no"},
		{name: "no codex entitlement", description: "Missing_Codex_Entitlement", want: "codex is not enabled for this ChatGPT workspace"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issuer := newFakeIssuer(t)
			browser := &fakeBrowser{t: t, client: &http.Client{}, redirect: func(authorize url.Values) url.Values {
				return url.Values{"error": {"access_denied"}, "error_description": {tt.description}, "state": {authorize.Get("state")}}
			}}
			_, err := issuer.client(newVirtualClock()).LoginBrowser(t.Context(), BrowserLogin{OpenBrowser: browser.open, Ports: []int{0}})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
			if len(issuer.forms()) != 0 {
				t.Fatalf("token requests = %v, want none", issuer.forms())
			}
		})
	}
}

func TestBrowserLoginStillWaitsWhenTheBrowserCannotOpen(t *testing.T) {
	issuer := newFakeIssuer(t)
	ctx, cancel := context.WithCancel(t.Context())
	var out bytes.Buffer
	opener := func(context.Context, string) error {
		cancel()
		return errors.New("no display")
	}
	_, err := issuer.client(newVirtualClock()).LoginBrowser(ctx, BrowserLogin{Out: &out, OpenBrowser: opener, Ports: []int{0}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want the caller's cancellation", err)
	}
	if !strings.Contains(out.String(), "Could not open a browser (no display)") {
		t.Fatalf("prompt = %q, want the open failure reported", out.String())
	}
}

func TestCallbackServerCancelEndsTheLogin(t *testing.T) {
	server, err := ListenCallback(t.Context(), "state-1", 0)
	if err != nil {
		t.Fatalf("ListenCallback: %v", err)
	}
	defer func() {
		if err := server.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()
	for _, path := range []string{cancelPath, cancelPath, callbackPath + "?state=state-1"} {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(server.Port())+path, nil)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		if err := resp.Body.Close(); err != nil {
			t.Fatalf("close body: %v", err)
		}
	}
	if _, err := server.Wait(t.Context()); !errors.Is(err, ErrLoginCancelled) {
		t.Fatalf("Wait = %v, want the first outcome, ErrLoginCancelled", err)
	}
}

func TestCallbackServerFallsBackToTheNextPort(t *testing.T) {
	var lc net.ListenConfig
	busy, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupy a port: %v", err)
	}
	defer func() {
		if err := busy.Close(); err != nil {
			t.Errorf("close busy listener: %v", err)
		}
	}()
	busyAddr, ok := busy.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("listener address %v is not TCP", busy.Addr())
	}
	busyPort := busyAddr.Port
	server, err := ListenCallback(t.Context(), "s", busyPort, 0)
	if err != nil {
		t.Fatalf("ListenCallback: %v", err)
	}
	if server.Port() == busyPort || server.Port() == 0 {
		t.Fatalf("port = %d, want a free fallback port other than %d", server.Port(), busyPort)
	}
	if err := server.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := ListenCallback(t.Context(), "s", busyPort); err == nil || !strings.Contains(err.Error(), "bind ChatGPT login callback") {
		t.Fatalf("all ports busy error = %v", err)
	}
}

func TestDeviceLoginPollsAtTheIssuerIntervalThenExchanges(t *testing.T) {
	clock := newVirtualClock()
	issuer := newFakeIssuer(t)
	issuer.userCodeReply = issuerReply{status: http.StatusOK, body: map[string]any{"device_auth_id": "dev_123", "usercode": "ABCD-1234", "interval": "7"}}
	issuer.deviceReplies = []issuerReply{
		{status: http.StatusForbidden, body: map[string]string{}},
		{status: http.StatusNotFound, body: map[string]string{}},
		{status: http.StatusOK, body: map[string]string{"authorization_code": "dev-code", "code_challenge": "ch", "code_verifier": "server-verifier"}},
	}
	issuer.queueToken(issuerReply{status: http.StatusOK, body: tokenBody(t, "access-d", "refresh-d", 600)})
	var waits []time.Duration
	var out bytes.Buffer

	cred, err := issuer.client(clock).LoginDevice(t.Context(), DeviceLogin{Out: &out, Sleep: clock.sleeper(&waits)})
	if err != nil {
		t.Fatalf("LoginDevice: %v", err)
	}
	if len(waits) != 2 || waits[0] != 7*time.Second || waits[1] != 7*time.Second {
		t.Fatalf("waits = %v, want two 7s polls", waits)
	}
	form := issuer.forms()[0]
	if form.Get("code") != "dev-code" || form.Get("code_verifier") != "server-verifier" || form.Get("redirect_uri") != issuer.server.URL+deviceCallbackPath {
		t.Fatalf("device exchange form = %v", form)
	}
	if cred.AccessToken != "access-d" || !cred.ExpiresAt.Equal(testEpoch().Add(14*time.Second+10*time.Minute)) {
		t.Fatalf("credential = %+v", cred)
	}
	if !strings.Contains(out.String(), issuer.server.URL+deviceVerifyPath) || !strings.Contains(out.String(), "ABCD-1234") {
		t.Fatalf("prompt = %q, want the verification URL and code", out.String())
	}
}

func TestDeviceLoginExpiresAfterFifteenMinutesWithoutApproval(t *testing.T) {
	clock := newVirtualClock()
	issuer := newFakeIssuer(t)
	issuer.userCodeReply = issuerReply{status: http.StatusOK, body: map[string]any{"device_auth_id": "dev_123", "user_code": "ABCD-1234", "interval": 60}}
	var waits []time.Duration

	_, err := issuer.client(clock).LoginDevice(t.Context(), DeviceLogin{Sleep: clock.sleeper(&waits)})
	if err == nil || !strings.Contains(err.Error(), "device code expired") {
		t.Fatalf("error = %v, want expiry", err)
	}
	if got := clock.Now().Sub(testEpoch()); got != DeviceCodeLifetime {
		t.Fatalf("virtual time spent = %s, want exactly %s", got, DeviceCodeLifetime)
	}
	if issuer.devicePolls != len(waits)+1 {
		t.Fatalf("polls = %d, waits = %d", issuer.devicePolls, len(waits))
	}
}

func TestDeviceLoginFailures(t *testing.T) {
	tests := []struct {
		name     string
		userCode issuerReply
		poll     []issuerReply
		want     error
		wantText string
	}{
		{name: "not offered", userCode: issuerReply{status: http.StatusNotFound, body: map[string]string{}}, want: ErrDeviceCodeUnavailable},
		{name: "user code rejected", userCode: issuerReply{status: http.StatusTooManyRequests, body: map[string]string{"error": "slow_down"}}, wantText: "HTTP 429"},
		{name: "malformed user code", userCode: issuerReply{status: http.StatusOK, body: map[string]string{"device_auth_id": "dev_123"}}, wantText: "missing device_auth_id or user_code"},
		{
			name:     "poll fails",
			userCode: issuerReply{status: http.StatusOK, body: map[string]string{"device_auth_id": "dev_123", "user_code": "ABCD-1234"}},
			poll:     []issuerReply{{status: http.StatusInternalServerError, body: map[string]string{"message": "boom"}}},
			wantText: "device authorization failed (HTTP 500): boom",
		},
		{
			name:     "approval without code",
			userCode: issuerReply{status: http.StatusOK, body: map[string]string{"device_auth_id": "dev_123", "user_code": "ABCD-1234"}},
			poll:     []issuerReply{{status: http.StatusOK, body: map[string]string{}}},
			wantText: "missing the exchange code",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clock := newVirtualClock()
			issuer := newFakeIssuer(t)
			issuer.userCodeReply = tt.userCode
			issuer.deviceReplies = tt.poll
			var waits []time.Duration
			_, err := issuer.client(clock).LoginDevice(t.Context(), DeviceLogin{Sleep: clock.sleeper(&waits)})
			if tt.want != nil && !errors.Is(err, tt.want) {
				t.Fatalf("error = %v, want %v", err, tt.want)
			}
			if tt.wantText != "" && (err == nil || !strings.Contains(err.Error(), tt.wantText)) {
				t.Fatalf("error = %v, want %q", err, tt.wantText)
			}
		})
	}
}

func TestParseIntervalDefaultsAndFloors(t *testing.T) {
	for raw, want := range map[string]time.Duration{`"5"`: 5 * time.Second, `3`: 3 * time.Second, `"0.2"`: time.Second, `""`: defaultDevicePolling, `null`: defaultDevicePolling} {
		if got := parseInterval([]byte(raw)); got != want {
			t.Errorf("parseInterval(%s) = %s, want %s", raw, got, want)
		}
	}
}

func TestWaitContextReturnsWhenTheContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := WaitContext(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("WaitContext = %v, want context.Canceled", err)
	}
	if err := WaitContext(t.Context(), 0); err != nil {
		t.Fatalf("WaitContext(0) = %v", err)
	}
}
