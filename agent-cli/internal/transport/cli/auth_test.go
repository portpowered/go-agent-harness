package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openai/chatgptauth"
	"github.com/spf13/cobra"
)

func authTestEpoch() time.Time { return time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC) }

// authTestIssuer is an httptest OAuth issuer for the auth commands. It
// accepts any code, issues one identity, and records revocations.
type authTestIssuer struct {
	server       *httptest.Server
	mu           sync.Mutex
	exchanges    []url.Values
	revoked      []string
	revokeStatus int
	devicePolls  int
}

func newAuthTestIssuer(t *testing.T) *authTestIssuer {
	t.Helper()
	issuer := &authTestIssuer{revokeStatus: http.StatusOK}
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/token", issuer.token(t))
	mux.HandleFunc("/oauth/revoke", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode revoke: %v", err)
		}
		issuer.mu.Lock()
		issuer.revoked = append(issuer.revoked, body["token"])
		status := issuer.revokeStatus
		issuer.mu.Unlock()
		w.WriteHeader(status)
	})
	mux.HandleFunc("/api/accounts/deviceauth/usercode", func(w http.ResponseWriter, _ *http.Request) {
		writeAuthJSON(t, w, http.StatusOK, map[string]string{"device_auth_id": "dev_1", "user_code": "WXYZ-0001", "interval": "5"})
	})
	mux.HandleFunc("/api/accounts/deviceauth/token", func(w http.ResponseWriter, _ *http.Request) {
		issuer.mu.Lock()
		issuer.devicePolls++
		approved := issuer.devicePolls > 1
		issuer.mu.Unlock()
		if !approved {
			writeAuthJSON(t, w, http.StatusForbidden, map[string]string{})
			return
		}
		writeAuthJSON(t, w, http.StatusOK, map[string]string{"authorization_code": "dev-code", "code_verifier": "dev-verifier"})
	})
	issuer.server = httptest.NewServer(mux)
	t.Cleanup(issuer.server.Close)
	return issuer
}

func (i *authTestIssuer) token(t *testing.T) http.HandlerFunc {
	t.Helper()
	claims, err := json.Marshal(map[string]any{
		"email":                       "dev@example.com",
		"https://api.openai.com/auth": map[string]string{"chatgpt_account_id": "acct_9", "chatgpt_plan_type": "pro"},
	})
	if err != nil {
		t.Fatalf("encode claims: %v", err)
	}
	idToken := "e30." + base64.RawURLEncoding.EncodeToString(claims) + ".sig"
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse token form: %v", err)
		}
		i.mu.Lock()
		i.exchanges = append(i.exchanges, r.PostForm)
		i.mu.Unlock()
		writeAuthJSON(t, w, http.StatusOK, map[string]any{
			"id_token": idToken, "access_token": "secret-access", "refresh_token": "secret-refresh", "expires_in": 7200,
		})
	}
}

func writeAuthJSON(t *testing.T, w http.ResponseWriter, status int, body any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		t.Errorf("write response: %v", err)
	}
}

// authHarness runs auth commands against a temp config dir, the fake
// issuer and a virtual clock.
type authHarness struct {
	t       *testing.T
	issuer  *authTestIssuer
	flags   *flags.GlobalFlags
	now     time.Time
	waits   []time.Duration
	browser func(ctx context.Context, url string) error
}

func newAuthHarness(t *testing.T) *authHarness {
	t.Helper()
	h := &authHarness{t: t, issuer: newAuthTestIssuer(t), flags: flags.NewGlobalFlags(), now: authTestEpoch()}
	h.flags.ConfigDirPath = t.TempDir()
	h.browser = h.approveInBrowser
	return h
}

func (h *authHarness) effects() authEffects {
	return authEffects{
		config: chatgptauth.Config{Issuer: h.issuer.server.URL, HTTPClient: h.issuer.server.Client(), Now: func() time.Time { return h.now }},
		openBrowser: func(ctx context.Context, url string) error {
			return h.browser(ctx, url)
		},
		sleep: func(ctx context.Context, d time.Duration) error {
			h.waits = append(h.waits, d)
			h.now = h.now.Add(d)
			return ctx.Err()
		},
		ports: []int{0},
	}
}

// approveInBrowser follows the authorize URL's redirect back to the CLI's
// loopback listener, as the issuer would after the user signs in.
func (h *authHarness) approveInBrowser(ctx context.Context, rawURL string) error {
	authorize, err := url.Parse(rawURL)
	if err != nil {
		return err
	}
	redirect, err := url.Parse(authorize.Query().Get("redirect_uri"))
	if err != nil {
		return err
	}
	redirect.Host = net.JoinHostPort("127.0.0.1", redirect.Port())
	redirect.RawQuery = url.Values{"code": {"browser-code"}, "state": {authorize.Query().Get("state")}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, redirect.String(), nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

type authRun struct {
	stdout string
	stderr string
	err    error
}

func (h *authHarness) run(command interface{ Generate() *cobra.Command }, args ...string) authRun {
	h.t.Helper()
	cmd := command.Generate()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs(args)
	cmd.SilenceUsage = true
	err := cmd.ExecuteContext(h.t.Context())
	return authRun{stdout: stdout.String(), stderr: stderr.String(), err: err}
}

func (h *authHarness) login(args ...string) authRun {
	h.t.Helper()
	return h.run(&AuthChatGPTCommand{flags: h.flags, effects: h.effects()}, args...)
}

func (h *authHarness) status(args ...string) authRun {
	h.t.Helper()
	return h.run(&AuthStatusCommand{flags: h.flags, effects: h.effects()}, args...)
}

func (h *authHarness) logout() authRun {
	h.t.Helper()
	return h.run(&AuthLogoutCommand{flags: h.flags, effects: h.effects()})
}

func (h *authHarness) storePath() string {
	return filepath.Join(h.flags.ConfigDirPath, "auth", "chatgpt.json")
}

func TestAuthChatGPTBrowserLoginThenStatusThenLogout(t *testing.T) {
	h := newAuthHarness(t)

	login := h.login()
	if login.err != nil {
		t.Fatalf("login: %v\n%s", login.err, login.stdout)
	}
	if !strings.Contains(login.stdout, "Signed in to ChatGPT as dev@example.com (pro plan).") || !strings.Contains(login.stdout, h.storePath()) {
		t.Fatalf("login output = %q", login.stdout)
	}
	if strings.Contains(login.stdout, "secret-") {
		t.Fatalf("login output leaked a token: %q", login.stdout)
	}
	if got := h.issuer.exchanges[0].Get("code"); got != "browser-code" {
		t.Fatalf("exchanged code = %q", got)
	}

	h.now = h.now.Add(30 * time.Minute)
	status := h.status()
	wantLines := []string{"ChatGPT: signed in", "Account:      dev@example.com (pro plan)", "Account ID:   acct_9", "Access token: expires 2026-10-02T14:00:00Z (in 1h30m0s)", "Store:        " + h.storePath()}
	for _, want := range wantLines {
		if !strings.Contains(status.stdout, want) {
			t.Fatalf("status missing %q:\n%s", want, status.stdout)
		}
	}
	if strings.Contains(status.stdout, "secret-") {
		t.Fatalf("status leaked a token: %q", status.stdout)
	}

	logout := h.logout()
	if logout.err != nil || !strings.Contains(logout.stdout, "Signed out of ChatGPT") {
		t.Fatalf("logout = %+v", logout)
	}
	if len(h.issuer.revoked) != 1 || h.issuer.revoked[0] != "secret-refresh" {
		t.Fatalf("revoked = %v, want the refresh token", h.issuer.revoked)
	}
	if _, err := os.Stat(h.storePath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("store still exists after logout: %v", err)
	}
	if after := h.status(); !strings.Contains(after.stdout, "ChatGPT: not signed in. Run `yui auth chatgpt`.") {
		t.Fatalf("status after logout = %q", after.stdout)
	}
	if again := h.logout(); again.err != nil || again.stdout != "Not signed in to ChatGPT.\n" {
		t.Fatalf("second logout = %+v", again)
	}
}

func TestAuthStatusJSONAndExpiredToken(t *testing.T) {
	h := newAuthHarness(t)
	if run := h.status("--json"); run.err != nil || run.stdout != `{"provider":"chatgpt","signed_in":false,"expired":false,"store":"`+jsonEscape(h.storePath())+`"}`+"\n" {
		t.Fatalf("signed-out JSON = %+v", run)
	}
	if run := h.login(); run.err != nil {
		t.Fatalf("login: %v", run.err)
	}
	h.now = h.now.Add(3 * time.Hour)
	var status authStatus
	run := h.status("--json")
	if err := json.Unmarshal([]byte(run.stdout), &status); err != nil {
		t.Fatalf("decode %q: %v", run.stdout, err)
	}
	if !status.SignedIn || !status.Expired || status.Email != "dev@example.com" || status.ExpiresAt == nil || !status.ExpiresAt.Equal(authTestEpoch().Add(2*time.Hour)) {
		t.Fatalf("status = %+v", status)
	}
	if text := h.status(); !strings.Contains(text.stdout, "Access token: expired 2026-10-02T14:00:00Z; it is refreshed on next use") {
		t.Fatalf("expired status = %q", text.stdout)
	}
}

func jsonEscape(value string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return value
	}
	return strings.Trim(string(encoded), `"`)
}

func TestAuthChatGPTDeviceLoginPollsOnVirtualTime(t *testing.T) {
	h := newAuthHarness(t)
	h.browser = func(context.Context, string) error {
		t.Fatal("device login must not open a browser")
		return nil
	}
	run := h.login("--device")
	if run.err != nil {
		t.Fatalf("device login: %v\n%s", run.err, run.stdout)
	}
	if !strings.Contains(run.stdout, h.issuer.server.URL+"/codex/device") || !strings.Contains(run.stdout, "WXYZ-0001") {
		t.Fatalf("device prompt = %q", run.stdout)
	}
	if len(h.waits) != 1 || h.waits[0] != 5*time.Second {
		t.Fatalf("waits = %v, want one 5s poll interval", h.waits)
	}
	if form := h.issuer.exchanges[0]; form.Get("code_verifier") != "dev-verifier" {
		t.Fatalf("device exchange = %v", form)
	}
}

// urlWatcher is the terminal for --no-browser: when the sign-in URL is
// printed, the "user" opens it by hand.
type urlWatcher struct {
	bytes.Buffer
	once   sync.Once
	opened chan error
	open   func(string) error
}

func (w *urlWatcher) Write(p []byte) (int, error) {
	n, err := w.Buffer.Write(p)
	if match := regexp.MustCompile(`https?://\S+/oauth/authorize\?\S+`).Find(p); match != nil {
		w.once.Do(func() { go func() { w.opened <- w.open(string(match)) }() })
	}
	return n, err
}

func TestAuthChatGPTNoBrowserPrintsTheURLForTheUser(t *testing.T) {
	h := newAuthHarness(t)
	h.browser = func(context.Context, string) error {
		t.Fatal("--no-browser must not launch a browser")
		return nil
	}
	out := &urlWatcher{opened: make(chan error, 1), open: func(raw string) error { return h.approveInBrowser(t.Context(), raw) }}
	cmd := (&AuthChatGPTCommand{flags: h.flags, effects: h.effects()}).Generate()
	cmd.SetOut(out)
	cmd.SetArgs([]string{"--no-browser"})
	if err := cmd.ExecuteContext(t.Context()); err != nil {
		t.Fatalf("login: %v\n%s", err, out.String())
	}
	if err := <-out.opened; err != nil {
		t.Fatalf("manual open: %v", err)
	}
	if !strings.Contains(out.String(), "Signed in to ChatGPT as dev@example.com") {
		t.Fatalf("output = %q", out.String())
	}
	if conflict := h.login("--device", "--no-browser"); conflict.err == nil || !strings.Contains(conflict.err.Error(), "none of the others can be") {
		t.Fatalf("--device with --no-browser = %v, want a flag conflict", conflict.err)
	}
}

func TestAuthLogoutDeletesLocallyWhenRevokeFails(t *testing.T) {
	h := newAuthHarness(t)
	if run := h.login(); run.err != nil {
		t.Fatalf("login: %v", run.err)
	}
	h.issuer.revokeStatus = http.StatusServiceUnavailable
	run := h.logout()
	if run.err != nil || !strings.Contains(run.stderr, "warning: ChatGPT token revoke failed (HTTP 503)") {
		t.Fatalf("logout = %+v, want a revoke warning", run)
	}
	if _, err := os.Stat(h.storePath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("store kept after a failed revoke: %v", err)
	}
}

func TestAuthCommandsNeedAConfigDirectory(t *testing.T) {
	h := newAuthHarness(t)
	h.flags = nil
	for name, run := range map[string]authRun{"login": h.login(), "status": h.status(), "logout": h.logout()} {
		if run.err == nil || !strings.Contains(run.err.Error(), "no config directory") {
			t.Errorf("%s without a config dir = %v", name, run.err)
		}
	}
}

func TestAuthRoutesAreRegisteredOnTheRoot(t *testing.T) {
	help := executeCLI("auth", "--help")
	for _, want := range []string{"chatgpt", "status", "logout", "Sign in with a ChatGPT account instead of an OpenAI API key"} {
		if !strings.Contains(help.stdout, want) {
			t.Fatalf("auth help missing %q:\n%s", want, help.stdout)
		}
	}
	status := executeCLI("--config-dir", t.TempDir(), "auth", "status")
	if status.exitCode != 0 || !strings.Contains(status.stdout, "ChatGPT: not signed in") {
		t.Fatalf("auth status = %+v", status)
	}
}

func TestBrowserLauncherPerOS(t *testing.T) {
	tests := map[string]string{"darwin": "open", "windows": "rundll32", "linux": "xdg-open", "freebsd": "xdg-open"}
	for goos, wantName := range tests {
		name, args := browserCommand(goos, "https://auth.example/x?a=1&b=2")
		if name != wantName || args[len(args)-1] != "https://auth.example/x?a=1&b=2" {
			t.Errorf("%s: %s %v", goos, name, args)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := openSystemBrowser(ctx, "https://auth.example/"); err == nil {
		t.Fatal("openSystemBrowser with a cancelled context launched a browser")
	}
}
