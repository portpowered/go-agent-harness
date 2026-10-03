package openaichatgpt_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/capabilities"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openai/chatgptauth"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openaichatgpt"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openaichatgpt/fakechatgpt"
)

const (
	testToken     = "access-token-secret"
	testAccountID = "acct-123"
	testSessionID = "session-fixed"
)

// testNow is the fixed clock of every credential manager in these tests.
func testNow() time.Time { return time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC) }

// signedInManager returns a credential manager over a store holding a
// credential valid at testNow, refreshed through issuer when it is not.
func signedInManager(t *testing.T, cred chatgptauth.Credential, issuer string) *chatgptauth.Manager {
	t.Helper()
	store := chatgptauth.NewFileStore(filepath.Join(t.TempDir(), "auth", "chatgpt.json"))
	if err := store.Save(cred); err != nil {
		t.Fatalf("save credential: %v", err)
	}
	return chatgptauth.NewManager(store, chatgptauth.NewClient(chatgptauth.Config{Issuer: issuer, Now: testNow}))
}

// testIssuer is an in-process OAuth token endpoint: every refresh answers
// with status and body, and is counted.
type testIssuer struct {
	server *httptest.Server
	mu     sync.Mutex
	calls  int
}

func newTestIssuer(t *testing.T, status int, body string) *testIssuer {
	t.Helper()
	issuer := &testIssuer{}
	issuer.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		issuer.mu.Lock()
		issuer.calls++
		issuer.mu.Unlock()
		if r.URL.Path != "/oauth/token" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if _, err := w.Write([]byte(body)); err != nil {
			t.Errorf("write token response: %v", err)
		}
	}))
	t.Cleanup(issuer.server.Close)
	return issuer
}

// refreshTo is an issuer whose refresh issues access token.
func refreshTo(t *testing.T, token string) *testIssuer {
	t.Helper()
	return newTestIssuer(t, http.StatusOK, `{"access_token":"`+token+`","refresh_token":"refresh-2","expires_in":3600}`)
}

// unusedIssuer fails the test if any refresh reaches it.
func unusedIssuer(t *testing.T) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("unexpected token refresh")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	return server.URL
}

func (i *testIssuer) refreshes() int {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.calls
}

func freshCredential() chatgptauth.Credential {
	return chatgptauth.Credential{
		AccessToken: testToken, RefreshToken: "refresh-secret", AccountID: testAccountID,
		ExpiresAt: testNow().Add(time.Hour), LastRefresh: testNow(),
	}
}

type harness struct {
	fake     *fakechatgpt.Server
	server   *httptest.Server
	provider *openaichatgpt.Provider
}

func newHarness(t *testing.T, opts ...openaichatgpt.Option) *harness {
	t.Helper()
	fake := fakechatgpt.New(testToken, testAccountID)
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	manager := signedInManager(t, freshCredential(), unusedIssuer(t))
	base := []openaichatgpt.Option{
		openaichatgpt.WithBaseURL(server.URL + "/backend-api/codex"),
		openaichatgpt.WithHTTPClient(server.Client()),
		openaichatgpt.WithSessionID(testSessionID),
		openaichatgpt.WithModel("gpt-test"),
	}
	return &harness{fake: fake, server: server, provider: openaichatgpt.New(manager, append(base, opts...)...)}
}

func userPrompt(text string) providers.InferenceRequest {
	return providers.InferenceRequest{Messages: []models.Message{messages.NewTextMessage(models.RoleUser, text)}}
}

func streamTypes(t *testing.T, stream <-chan messages.StreamMessage) ([]messages.StreamMessageType, string) {
	t.Helper()
	var types []messages.StreamMessageType
	var text strings.Builder
	for msg := range stream {
		types = append(types, msg.Type)
		if delta, ok := msg.Value.(*messages.TextDeltaValue); ok {
			text.WriteString(delta.Content)
		}
	}
	return types, text.String()
}

func TestInferStreamSendsCodexRequestAndStreamsText(t *testing.T) {
	h := newHarness(t)
	h.fake.Enqueue(fakechatgpt.TextReply("Hello there"))

	stream, err := h.provider.InferStream(t.Context(), userPrompt("hi"))
	if err != nil {
		t.Fatalf("InferStream: %v", err)
	}
	types, text := streamTypes(t, stream)
	want := []messages.StreamMessageType{
		messages.StreamTypeMessageStart, messages.StreamTypeTextStart, messages.StreamTypeTextDelta, messages.StreamTypeTextDelta,
		messages.StreamTypeTextEnd, messages.StreamTypeMessageEnd, messages.StreamTypeUsageInfo,
	}
	if !equalTypes(types, want) || text != "Hello there" {
		t.Fatalf("stream = %v %q, want %v %q", types, text, want, "Hello there")
	}

	requests := h.fake.Requests()
	if len(requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(requests))
	}
	req := requests[0]
	if req.Method != http.MethodPost || req.Path != "/backend-api/codex/responses" {
		t.Fatalf("request = %s %s, want POST /backend-api/codex/responses", req.Method, req.Path)
	}
	wantHeaders := map[string]string{
		"Authorization":       "Bearer " + testToken,
		"Chatgpt-Account-Id":  testAccountID,
		"Originator":          "yui",
		"Openai-Beta":         "responses=experimental",
		"Accept":              "text/event-stream",
		"Content-Type":        "application/json",
		"Session-Id":          testSessionID,
		"X-Client-Request-Id": testSessionID,
	}
	for name, value := range wantHeaders {
		if got := req.Header.Get(name); got != value {
			t.Errorf("header %s = %q, want %q", name, got, value)
		}
	}
	var body map[string]any
	if err := json.Unmarshal(req.Body, &body); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	if body["store"] != false || body["stream"] != true || body["model"] != "gpt-test" {
		t.Fatalf("body store/stream/model = %v/%v/%v, want false/true/gpt-test", body["store"], body["stream"], body["model"])
	}
}

func TestInferRunsToolCallRoundTrip(t *testing.T) {
	h := newHarness(t)
	h.fake.Enqueue(fakechatgpt.ToolCallReply("call_1", "get_weather", `{"city":"Paris"}`), fakechatgpt.TextReply("It is sunny."))
	tools := []models.ToolDefinition{{Name: "get_weather", Description: "Look up the weather"}}

	first, err := h.provider.Infer(t.Context(), providers.InferenceRequest{Messages: userPrompt("weather?").Messages, Tools: tools})
	if err != nil {
		t.Fatalf("first Infer: %v", err)
	}
	if len(first.Message.ToolCalls) != 1 {
		t.Fatalf("tool calls = %+v, want one", first.Message.ToolCalls)
	}
	call := first.Message.ToolCalls[0]
	if call.ID != "call_1" || call.Name != "get_weather" || call.Arguments != `{"city":"Paris"}` {
		t.Fatalf("tool call = %+v", call)
	}
	if first.Usage.TotalTokens != 28 {
		t.Fatalf("usage = %+v, want total 28", first.Usage)
	}

	conversation := []models.Message{
		messages.NewTextMessage(models.RoleUser, "weather?"), first.Message,
		{Role: models.RoleTool, ToolCallID: call.ID, ContentParts: []models.ContentPart{models.TextPart{Text: "sunny"}}},
	}
	second, err := h.provider.Infer(t.Context(), providers.InferenceRequest{Messages: conversation, Tools: tools})
	if err != nil {
		t.Fatalf("second Infer: %v", err)
	}
	if got := second.Message.TextContent(); got != "It is sunny." {
		t.Fatalf("second text = %q", got)
	}

	var body struct {
		Input []map[string]any `json:"input"`
		Tools []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal(h.fake.Requests()[1].Body, &body); err != nil {
		t.Fatalf("decode second request: %v", err)
	}
	if len(body.Input) != 3 || body.Input[1]["type"] != "function_call" || body.Input[1]["call_id"] != "call_1" ||
		body.Input[2]["type"] != "function_call_output" || body.Input[2]["output"] != "sunny" {
		t.Fatalf("second request input = %v, want user, function_call, function_call_output", body.Input)
	}
	if len(body.Tools) != 1 || body.Tools[0]["strict"] != false {
		t.Fatalf("tools = %v, want one strict:false tool", body.Tools)
	}
}

func TestDefaultModelComesFromAccountModelList(t *testing.T) {
	h := newHarness(t, openaichatgpt.WithModel(""))
	h.fake.SetModels(`{"models":[{"slug":"gpt-b","visibility":"list","priority":5},{"slug":"gpt-a","visibility":"list","priority":1},{"slug":"gpt-hidden","visibility":"hide","priority":0}]}`)
	h.fake.Enqueue(fakechatgpt.TextReply("one"), fakechatgpt.TextReply("two"))

	for range 2 {
		if _, err := h.provider.Infer(t.Context(), userPrompt("hi")); err != nil {
			t.Fatalf("Infer: %v", err)
		}
	}
	requests := h.fake.Requests()
	if len(requests) != 3 || requests[0].Path != "/backend-api/codex/models" || requests[0].Query != "client_version="+openaichatgpt.DefaultClientVersion {
		t.Fatalf("requests = %+v, want one model list then two responses", requests)
	}
	for _, req := range requests[1:] {
		var body struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal(req.Body, &body); err != nil || body.Model != "gpt-a" {
			t.Fatalf("model = %q (%v), want gpt-a", body.Model, err)
		}
	}

	listed, err := h.provider.Models(t.Context())
	if err != nil || len(listed) != 3 || listed[0].Slug != "gpt-hidden" {
		t.Fatalf("Models = %+v, %v; want three sorted by priority", listed, err)
	}
}

func TestEmptyModelListFailsWithTypedError(t *testing.T) {
	h := newHarness(t, openaichatgpt.WithModel(""))
	_, err := h.provider.Infer(t.Context(), userPrompt("hi"))
	if !errors.Is(err, openaichatgpt.ErrNoModels) {
		t.Fatalf("error = %v, want ErrNoModels", err)
	}
}

func TestBackendErrorsAreTypedAndCarryNoSecrets(t *testing.T) {
	const bodySecret = "Provided authentication token is expired"
	tests := []struct {
		name    string
		reply   fakechatgpt.Reply
		want    []error
		notWant []error
	}{
		{name: "403 is a block, not a sign-in failure", reply: fakechatgpt.Reply{Status: http.StatusForbidden, Body: `<html>Cloudflare: ` + bodySecret + ` blocked</html>`},
			want: []error{openaichatgpt.ErrBlocked, providers.ErrProviderRejected}, notWant: []error{openaichatgpt.ErrSignInAgain, providers.ErrAuthentication}},
		{name: "403 misalignment is a policy violation", reply: fakechatgpt.Reply{Status: http.StatusForbidden, Body: `{"error":{"code":"misalignment_policy_violation","message":"` + bodySecret + `"}}`},
			want: []error{openaichatgpt.ErrPolicyViolation, providers.ErrInvalidRequest}, notWant: []error{providers.ErrAuthentication}},
		{name: "usage limit", reply: fakechatgpt.Reply{Status: http.StatusTooManyRequests, Body: `{"error":{"type":"usage_limit_reached","message":"` + bodySecret + `"}}`}, want: []error{openaichatgpt.ErrUsageLimitReached, providers.ErrRateLimited}},
		{name: "usage not included", reply: fakechatgpt.Reply{Status: http.StatusTooManyRequests, Body: `{"error":{"type":"usage_not_included"}}`}, want: []error{openaichatgpt.ErrUsageNotIncluded}},
		{name: "server overloaded", reply: fakechatgpt.Reply{Status: http.StatusServiceUnavailable, Body: `{"error":{"code":"server_is_overloaded","message":"` + bodySecret + `"}}`}, want: []error{openaichatgpt.ErrServerOverloaded, providers.ErrRateLimited}},
		{name: "slow down", reply: fakechatgpt.Reply{Status: http.StatusServiceUnavailable, Body: `{"error":{"code":"slow_down"}}`}, want: []error{openaichatgpt.ErrServerOverloaded}},
		{name: "context length", reply: fakechatgpt.Reply{Status: http.StatusBadRequest, Body: `{"error":{"code":"context_length_exceeded","message":"` + bodySecret + `"}}`}, want: []error{providers.ErrInvalidRequest}},
		{name: "server error", reply: fakechatgpt.Reply{Status: http.StatusBadGateway, Body: `<html>` + bodySecret + `</html>`}, want: []error{providers.ErrTransport}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			h.fake.Enqueue(tt.reply)
			_, err := h.provider.InferStream(t.Context(), userPrompt("hi"))
			if err == nil {
				t.Fatal("InferStream succeeded, want error")
			}
			for _, want := range tt.want {
				if !errors.Is(err, want) {
					t.Errorf("error %v does not match %v", err, want)
				}
			}
			for _, notWant := range tt.notWant {
				if errors.Is(err, notWant) {
					t.Errorf("error %v must not match %v", err, notWant)
				}
			}
			assertNoSecrets(t, err, bodySecret, testToken, "<html>")
		})
	}
}

func assertNoSecrets(t *testing.T, err error, secrets ...string) {
	t.Helper()
	for _, secret := range secrets {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("error %q leaks %q", err, secret)
		}
	}
}

// staleProvider is a provider whose stored access token the fake backend
// rejects, refreshing through issuer.
func staleProvider(t *testing.T, issuer string, opts ...openaichatgpt.Option) (*openaichatgpt.Provider, *fakechatgpt.Server, *chatgptauth.FileStore) {
	t.Helper()
	fake := fakechatgpt.New(testToken, testAccountID)
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	stale := freshCredential()
	stale.AccessToken = "stale-token"
	store := chatgptauth.NewFileStore(filepath.Join(t.TempDir(), "auth", "chatgpt.json"))
	if err := store.Save(stale); err != nil {
		t.Fatalf("save credential: %v", err)
	}
	manager := chatgptauth.NewManager(store, chatgptauth.NewClient(chatgptauth.Config{Issuer: issuer, Now: testNow}))
	base := []openaichatgpt.Option{openaichatgpt.WithBaseURL(server.URL), openaichatgpt.WithHTTPClient(server.Client()), openaichatgpt.WithModel("gpt-test")}
	return openaichatgpt.New(manager, append(base, opts...)...), fake, store
}

func TestUnauthorizedIsRefreshedAndRetriedOnce(t *testing.T) {
	issuer := refreshTo(t, testToken)
	provider, fake, store := staleProvider(t, issuer.server.URL)
	fake.Enqueue(fakechatgpt.TextReply("after refresh"))

	got, err := provider.Infer(t.Context(), userPrompt("hi"))
	if err != nil || got.Message.TextContent() != "after refresh" {
		t.Fatalf("Infer = %q, %v; want the reply after one refresh", got.Message.TextContent(), err)
	}
	if fake.Unauthorized() != 1 || issuer.refreshes() != 1 || len(fake.Requests()) != 2 {
		t.Fatalf("unauthorized=%d refreshes=%d requests=%d; want 1, 1, 2", fake.Unauthorized(), issuer.refreshes(), len(fake.Requests()))
	}
	if stored, err := store.Load(); err != nil || stored.AccessToken != testToken {
		t.Fatalf("stored token = %q, %v; want the refreshed token persisted", stored.AccessToken, err)
	}
}

func TestUnauthorizedAfterRefreshAsksToSignInAgain(t *testing.T) {
	issuer := refreshTo(t, "still-rejected")
	provider, fake, _ := staleProvider(t, issuer.server.URL)

	_, err := provider.InferStream(t.Context(), userPrompt("hi"))
	if !errors.Is(err, openaichatgpt.ErrSignInAgain) || !errors.Is(err, providers.ErrAuthentication) {
		t.Fatalf("error = %v, want ErrSignInAgain and ErrAuthentication", err)
	}
	if fake.Unauthorized() != 2 || issuer.refreshes() != 1 {
		t.Fatalf("unauthorized=%d refreshes=%d; want exactly one retry after one refresh", fake.Unauthorized(), issuer.refreshes())
	}
	assertNoSecrets(t, err, "stale-token", "still-rejected")
}

func TestUnauthorizedWithADeadRefreshTokenAsksToSignInAgain(t *testing.T) {
	const issuerText = "refresh token was already used: secret-detail"
	issuer := newTestIssuer(t, http.StatusBadRequest, `{"error":"refresh_token_reused","error_description":"`+issuerText+`"}`)
	provider, fake, _ := staleProvider(t, issuer.server.URL)

	_, err := provider.InferStream(t.Context(), userPrompt("hi"))
	if !errors.Is(err, chatgptauth.ErrReauthRequired) || !errors.Is(err, providers.ErrAuthentication) {
		t.Fatalf("error = %v, want ErrReauthRequired and ErrAuthentication", err)
	}
	if !strings.Contains(err.Error(), "run `yui auth chatgpt`") || fake.Unauthorized() != 1 {
		t.Fatalf("error = %q after %d unauthorized; want a sign-in message and no retry", err, fake.Unauthorized())
	}
	assertNoSecrets(t, err, issuerText, "stale-token", "refresh-secret")
}

func TestTransientRefreshFailureKeepsOnlyStatusAndCode(t *testing.T) {
	const issuerText = "upstream exploded with secret-detail"
	issuer := newTestIssuer(t, http.StatusBadGateway, `{"error":"temporarily_unavailable","error_description":"`+issuerText+`"}`)
	provider, _, _ := staleProvider(t, issuer.server.URL)

	_, err := provider.InferStream(t.Context(), userPrompt("hi"))
	if !errors.Is(err, providers.ErrTransport) || !strings.Contains(err.Error(), "HTTP 502") {
		t.Fatalf("error = %v, want a transport error naming HTTP 502", err)
	}
	assertNoSecrets(t, err, issuerText)
}

func TestStreamFailuresBecomeStreamErrors(t *testing.T) {
	tests := []struct {
		name   string
		events []string
		want   error
	}{
		{name: "error event", events: []string{`{"type":"error","code":"rate_limit_exceeded","message":"slow down"}`}, want: providers.ErrRateLimited},
		{name: "response.failed usage limit", events: []string{`{"type":"response.failed","response":{"error":{"code":"usage_limit_reached","message":"limit"}}}`}, want: openaichatgpt.ErrUsageLimitReached},
		{name: "incomplete response", events: []string{`{"type":"response.output_text.delta","delta":"par"}`, `{"type":"response.incomplete","response":{"incomplete_details":{"reason":"max_output_tokens"}}}`}, want: providers.ErrProviderRejected},
		{name: "stream ends early", events: []string{`{"type":"response.output_text.delta","delta":"par"}`}, want: openaichatgpt.ErrStreamEnded},
		{name: "unfinished function call", events: []string{
			`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","call_id":"c","name":"f"}}`,
			`{"type":"response.completed","response":{}}`,
		}, want: openaichatgpt.ErrUnresolvedToolCall},
		{name: "malformed event", events: []string{`{not json`}, want: providers.ErrProviderRejected},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			h.fake.Enqueue(fakechatgpt.Reply{Events: tt.events})
			_, err := h.provider.Infer(t.Context(), userPrompt("hi"))
			if !errors.Is(err, tt.want) {
				t.Fatalf("error = %v, want %v", err, tt.want)
			}
			if strings.Contains(err.Error(), "slow down") {
				t.Fatalf("error %q carries backend message text", err)
			}
		})
	}
}

func TestMissingLoginFailsBeforeAnyRequest(t *testing.T) {
	fake := fakechatgpt.New(testToken, testAccountID)
	server := httptest.NewServer(fake)
	defer server.Close()
	store := chatgptauth.NewFileStore(filepath.Join(t.TempDir(), "auth", "chatgpt.json"))
	manager := chatgptauth.NewManager(store, chatgptauth.NewClient(chatgptauth.Config{Now: testNow}))
	provider := openaichatgpt.New(manager, openaichatgpt.WithBaseURL(server.URL), openaichatgpt.WithHTTPClient(server.Client()))

	_, err := provider.Infer(t.Context(), userPrompt("hi"))
	if !errors.Is(err, chatgptauth.ErrNotLoggedIn) || !errors.Is(err, providers.ErrAuthentication) {
		t.Fatalf("error = %v, want ErrNotLoggedIn and ErrAuthentication", err)
	}
	if !strings.Contains(err.Error(), "run `yui auth chatgpt`") {
		t.Fatalf("error %q does not say to run yui auth chatgpt", err)
	}
	if got := len(fake.Requests()); got != 0 {
		t.Fatalf("backend requests = %d, want 0", got)
	}
}

func TestExpiredLoginIsRefreshedBeforeTheRequest(t *testing.T) {
	issuer := refreshTo(t, testToken)
	fake := fakechatgpt.New(testToken, testAccountID)
	server := httptest.NewServer(fake)
	defer server.Close()
	expired := freshCredential()
	expired.AccessToken = "expired-token"
	expired.ExpiresAt = testNow().Add(-time.Minute)
	provider := openaichatgpt.New(signedInManager(t, expired, issuer.server.URL),
		openaichatgpt.WithBaseURL(server.URL), openaichatgpt.WithHTTPClient(server.Client()), openaichatgpt.WithModel("gpt-test"))
	fake.Enqueue(fakechatgpt.TextReply("ok"))

	if _, err := provider.Infer(t.Context(), userPrompt("hi")); err != nil {
		t.Fatalf("Infer after refresh: %v", err)
	}
	if fake.Unauthorized() != 0 {
		t.Fatalf("backend saw %d unauthorized requests, want 0", fake.Unauthorized())
	}
}

func TestCapabilitiesReportTextOnly(t *testing.T) {
	caps := providers.ReportedCapabilities(openaichatgpt.New(nil))
	if caps.Provider != openaichatgpt.ProviderName || caps.Stateless.Tools.State != capabilities.CapabilityStateSupported || caps.Session.Sessions.State == capabilities.CapabilityStateSupported {
		t.Fatalf("capabilities = %+v", caps)
	}
}

func equalTypes(got, want []messages.StreamMessageType) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestModelConfigSetsReasoningEffort(t *testing.T) {
	h := newHarness(t)
	h.fake.Enqueue(fakechatgpt.TextReply("ok"))
	req := userPrompt("think")
	req.Config = json.RawMessage(`{"reasoning_effort":"high"}`)
	if _, err := h.provider.Infer(t.Context(), req); err != nil {
		t.Fatalf("Infer: %v", err)
	}
	var body struct {
		Reasoning map[string]string `json:"reasoning"`
	}
	if err := json.Unmarshal(h.fake.Requests()[0].Body, &body); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if body.Reasoning["effort"] != "high" || body.Reasoning["summary"] != "auto" {
		t.Fatalf("reasoning = %v, want effort high, summary auto", body.Reasoning)
	}

	req.Config = json.RawMessage(`["not an object"]`)
	if _, err := h.provider.Infer(t.Context(), req); !errors.Is(err, providers.ErrInvalidRequest) {
		t.Fatalf("invalid config error = %v, want ErrInvalidRequest", err)
	}
}

func TestTransportFailuresAndCancellationAreClassified(t *testing.T) {
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	provider := openaichatgpt.New(signedInManager(t, freshCredential(), unusedIssuer(t)),
		openaichatgpt.WithBaseURL(closed.URL), openaichatgpt.WithModel("gpt-test"))
	if _, err := provider.Infer(t.Context(), userPrompt("hi")); !errors.Is(err, providers.ErrTransport) {
		t.Fatalf("closed server error = %v, want ErrTransport", err)
	}
	if _, err := provider.Models(t.Context()); !errors.Is(err, providers.ErrTransport) {
		t.Fatalf("closed server models error = %v, want ErrTransport", err)
	}

	h := newHarness(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := h.provider.Infer(ctx, userPrompt("hi")); !errors.Is(err, providers.ErrCancellation) && !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled error = %v, want cancellation", err)
	}
}

func TestModelListErrorsAreTyped(t *testing.T) {
	h := newHarness(t, openaichatgpt.WithModel(""))
	h.fake.SetModels(`not json`)
	if _, err := h.provider.Models(t.Context()); err == nil || strings.Contains(err.Error(), "not json") {
		t.Fatalf("models error = %v, want a decode error without the body", err)
	}

	issuer := refreshTo(t, testToken)
	stale, fake, _ := staleProvider(t, issuer.server.URL, openaichatgpt.WithModel(""))
	fake.SetModels(`{"models":[{"slug":"gpt-a","visibility":"list","priority":1}]}`)
	fake.Enqueue(fakechatgpt.TextReply("ok"))
	if _, err := stale.Infer(t.Context(), userPrompt("hi")); err != nil {
		t.Fatalf("Infer after a refreshed model list: %v", err)
	}
	if fake.Unauthorized() != 1 || issuer.refreshes() != 1 {
		t.Fatalf("unauthorized=%d refreshes=%d; want the model list refreshed once and the request reusing the token", fake.Unauthorized(), issuer.refreshes())
	}
}

func TestReasoningCarriesAcrossAToolStep(t *testing.T) {
	h := newHarness(t)
	h.fake.Enqueue(fakechatgpt.Reply{Events: []string{
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_9","summary":[{"type":"summary_text","text":"plan"}],"encrypted_content":"enc-9"}}`,
		`{"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","call_id":"call_9","name":"lookup","arguments":"{}"}}`,
		`{"type":"response.completed","response":{}}`,
	}}, fakechatgpt.TextReply("done"))

	first, err := h.provider.Infer(t.Context(), userPrompt("go"))
	if err != nil || len(first.Message.ToolCalls) != 1 {
		t.Fatalf("first Infer = %+v, %v", first.Message, err)
	}
	conversation := []models.Message{
		messages.NewTextMessage(models.RoleUser, "go"), first.Message,
		{Role: models.RoleTool, ToolCallID: "call_9", ContentParts: []models.ContentPart{models.TextPart{Text: "result"}}},
	}
	if _, err := h.provider.Infer(t.Context(), providers.InferenceRequest{Messages: conversation}); err != nil {
		t.Fatalf("second Infer: %v", err)
	}
	var body struct {
		Input []map[string]any `json:"input"`
	}
	if err := json.Unmarshal(h.fake.Requests()[1].Body, &body); err != nil {
		t.Fatalf("decode second request: %v", err)
	}
	if len(body.Input) != 4 || body.Input[1]["type"] != "reasoning" || body.Input[1]["encrypted_content"] != "enc-9" || body.Input[2]["call_id"] != "call_9" {
		t.Fatalf("second request input = %v, want user, reasoning(enc-9), function_call, function_call_output", body.Input)
	}
}

func TestReasoningBeforeAFinalAnswerCarriesIntoTheNextUserTurn(t *testing.T) {
	h := newHarness(t)
	h.fake.Enqueue(fakechatgpt.Reply{Events: []string{
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_7","summary":[],"encrypted_content":"enc-7"}}`,
		`{"type":"response.output_text.delta","item_id":"msg_7","delta":"Hello."}`,
		`{"type":"response.output_item.done","output_index":1,"item":{"type":"message","id":"msg_7","role":"assistant","content":[{"type":"output_text","text":"Hello."}]}}`,
		`{"type":"response.completed","response":{}}`,
	}}, fakechatgpt.TextReply("bye"))

	first, err := h.provider.Infer(t.Context(), userPrompt("hi"))
	if err != nil {
		t.Fatalf("first Infer: %v", err)
	}
	conversation := []models.Message{messages.NewTextMessage(models.RoleUser, "hi"), first.Message, messages.NewTextMessage(models.RoleUser, "thanks")}
	if _, err := h.provider.Infer(t.Context(), providers.InferenceRequest{Messages: conversation}); err != nil {
		t.Fatalf("second Infer: %v", err)
	}
	var body struct {
		Input []map[string]any `json:"input"`
	}
	if err := json.Unmarshal(h.fake.Requests()[1].Body, &body); err != nil {
		t.Fatalf("decode second request: %v", err)
	}
	if len(body.Input) != 4 || body.Input[1]["type"] != "reasoning" || body.Input[1]["encrypted_content"] != "enc-7" || body.Input[2]["role"] != "assistant" {
		t.Fatalf("second request input = %v, want user, reasoning(enc-7), assistant message, user", body.Input)
	}
	if _, hasID := body.Input[1]["id"]; hasID {
		t.Fatalf("replayed reasoning %v carries an id", body.Input[1])
	}
}
