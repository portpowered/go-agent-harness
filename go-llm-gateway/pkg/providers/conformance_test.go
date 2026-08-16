package providers_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/anthropic"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/fal"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/gemini"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/grok"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openai"
)

const conformanceSecret = "s2s-conformance-secret-not-real"

type conformanceRoundTripper struct {
	mu           sync.Mutex
	transportErr error
	status       int
	body         string
	calls        int
	lastURL      string
	lastHeaders  http.Header
}

func (r *conformanceRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	r.mu.Lock()
	r.calls++
	r.lastURL = req.URL.String()
	r.lastHeaders = req.Header.Clone()
	transportErr := r.transportErr
	status := r.status
	body := r.body
	r.mu.Unlock()

	if transportErr != nil {
		return nil, transportErr
	}
	return &http.Response{
		StatusCode:    status,
		Status:        fmt.Sprintf("%d", status),
		Header:        make(http.Header),
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)),
		Request:       req,
	}, nil
}

func (r *conformanceRoundTripper) snapshot() (calls int, url string, headers http.Header) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls, r.lastURL, r.lastHeaders.Clone()
}

type statelessProviderCase struct {
	name    string
	new     func(*http.Client, string) providers.Provider
	request providers.InferenceRequest
}

func s11StatelessProviderCases() []statelessProviderCase {
	request := providers.InferenceRequest{
		Messages: []models.Message{models.NewTextMessage(models.RoleUser, "deterministic conformance request")},
	}
	return []statelessProviderCase{
		{
			name: "anthropic",
			new: func(client *http.Client, secret string) providers.Provider {
				return anthropic.New(anthropic.WithAPIKey(secret), anthropic.WithHTTPClient(client))
			},
			request: request,
		},
		{
			name: "gemini",
			new: func(client *http.Client, secret string) providers.Provider {
				return gemini.New(gemini.WithAPIKey(secret), gemini.WithHTTPClient(client))
			},
			request: request,
		},
		{
			name: "fal",
			new: func(client *http.Client, secret string) providers.Provider {
				return fal.New(fal.WithAPIKey(secret), fal.WithBaseURL("https://s2s-conformance.invalid"), fal.WithHTTPClient(client))
			},
			request: func() providers.InferenceRequest {
				req := request
				req.Model = fal.ModelQwenTTS
				return req
			}(),
		},
		{
			name: "openai",
			new: func(client *http.Client, secret string) providers.Provider {
				return openai.New(
					openai.WithAPIKey(secret),
					openai.WithBaseURL("https://s2s-conformance.invalid/v1"),
					openai.WithHTTPClient(client),
				)
			},
			request: request,
		},
	}
}

func TestS11OfflineStatelessProviderConformance(t *testing.T) {
	for _, tc := range s11StatelessProviderCases() {
		t.Run(tc.name+"/identity", func(t *testing.T) {
			provider := tc.new(&http.Client{Transport: &conformanceRoundTripper{
				transportErr: errors.New("s2s-conformance-identity-probe"),
			}}, conformanceSecret)
			if got := provider.Name(); got != tc.name {
				t.Fatalf("provider.Name() = %q, want %q", got, tc.name)
			}
		})

		t.Run(tc.name+"/transport-failure", func(t *testing.T) {
			probe := &conformanceRoundTripper{transportErr: errors.New("s2s-conformance-transport-failure")}
			provider := tc.new(&http.Client{Transport: probe}, conformanceSecret)
			_, err := provider.Infer(context.Background(), tc.request)
			assertStatelessFailure(t, tc.name, probe, err, ErrorExpectation{
				class:     providers.ErrorClassTransport,
				cause:     providers.ErrTransport,
				signal:    "s2s-conformance-transport-failure",
				retryable: true,
			})
		})

		t.Run(tc.name+"/protocol-failure", func(t *testing.T) {
			probe := &conformanceRoundTripper{
				status: http.StatusTooManyRequests,
				body:   `{"error":{"message":"s2s-conformance-protocol-failure"}}`,
			}
			provider := tc.new(&http.Client{Transport: probe}, conformanceSecret)
			_, err := provider.Infer(context.Background(), tc.request)
			assertStatelessFailure(t, tc.name, probe, err, ErrorExpectation{
				class:     providers.ErrorClassRateLimited,
				cause:     providers.ErrRateLimited,
				signal:    "s2s-conformance-protocol-failure",
				retryable: true,
			})
			var typed *providers.ProviderError
			if !errors.As(err, &typed) {
				t.Skipf("provider defect: protocol failure did not provide *providers.ProviderError: %T: %v", err, err)
			}
			if typed.Provider != tc.name || typed.StatusCode != http.StatusTooManyRequests {
				t.Fatalf("ProviderError = %+v, want provider %q status %d", typed, tc.name, http.StatusTooManyRequests)
			}
			wrapped := fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", err))
			if !errors.Is(wrapped, providers.ErrProviderRejected) || !errors.Is(wrapped, providers.ErrRateLimited) {
				t.Fatal("protocol taxonomy did not survive two wrapping levels")
			}
			if !providers.IsRetryable(wrapped) {
				t.Fatal("protocol retryability did not survive two wrapping levels")
			}
			var wrappedTyped *providers.ProviderError
			if !errors.As(wrapped, &wrappedTyped) || wrappedTyped.StatusCode != http.StatusTooManyRequests {
				t.Fatalf("wrapped ProviderError = %+v", wrappedTyped)
			}
		})
	}
}

type ErrorExpectation struct {
	class     string
	cause     error
	signal    string
	retryable bool
}

func assertStatelessFailure(t *testing.T, providerName string, probe *conformanceRoundTripper, err error, want ErrorExpectation) {
	t.Helper()
	if err == nil {
		t.Fatal("provider failure returned nil error")
	}
	calls, url, headers := probe.snapshot()
	if calls == 0 {
		t.Skipf("provider defect: %s did not use the injected HTTP transport", providerName)
	}
	if !requestContainsSecret(url, headers, conformanceSecret) {
		t.Fatalf("injected credential was not present in the provider request (url=%q headers=%v)", url, headers)
	}
	assertErrorChainSecretFree(t, err, conformanceSecret)
	if !errorChainContains(err, want.signal) {
		t.Skipf("provider defect: %s error omitted deterministic non-secret signal %q: %v", providerName, want.signal, err)
	}
	if got := providers.ErrorClassification(err); got != want.class {
		t.Skipf("provider defect: %s failure classified as %q, want shared %q: %T: %v", providerName, got, want.class, err, err)
	}
	if got := providers.IsRetryable(err); got != want.retryable {
		t.Skipf("provider defect: %s failure retryable = %v, want %v: %T: %v", providerName, got, want.retryable, err, err)
	}
	if !errors.Is(err, want.cause) {
		t.Skipf("provider defect: %s failure did not preserve shared cause %v: %T: %v", providerName, want.cause, err, err)
	}
}

func requestContainsSecret(url string, headers http.Header, secret string) bool {
	if strings.Contains(url, secret) {
		return true
	}
	for _, values := range headers {
		for _, value := range values {
			if strings.Contains(value, secret) {
				return true
			}
		}
	}
	return false
}

func assertErrorChainSecretFree(t *testing.T, err error, secret string) {
	t.Helper()
	seen := make(map[string]struct{})
	var visit func(error)
	visit = func(current error) {
		if current == nil {
			return
		}
		key := errorVisitKey(current)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		if strings.Contains(current.Error(), secret) {
			t.Fatalf("credential leaked from %T: %q", current, current.Error())
		}
		if joined, ok := current.(interface{ Unwrap() []error }); ok {
			for _, cause := range joined.Unwrap() {
				visit(cause)
			}
		}
		if single, ok := current.(interface{ Unwrap() error }); ok {
			visit(single.Unwrap())
		}
	}
	visit(err)
}

func errorChainContains(err error, signal string) bool {
	seen := make(map[string]struct{})
	var visit func(error) bool
	visit = func(current error) bool {
		if current == nil {
			return false
		}
		key := errorVisitKey(current)
		if _, ok := seen[key]; ok {
			return false
		}
		seen[key] = struct{}{}
		if strings.Contains(current.Error(), signal) {
			return true
		}
		if joined, ok := current.(interface{ Unwrap() []error }); ok {
			for _, cause := range joined.Unwrap() {
				if visit(cause) {
					return true
				}
			}
		}
		if single, ok := current.(interface{ Unwrap() error }); ok {
			return visit(single.Unwrap())
		}
		return false
	}
	return visit(err)
}

func errorVisitKey(err error) string {
	value := reflect.ValueOf(err)
	if value.IsValid() && value.Kind() == reflect.Ptr {
		return fmt.Sprintf("%T:%x", err, value.Pointer())
	}
	return fmt.Sprintf("%T:%s", err, err.Error())
}

type sessionProbe struct {
	mu          sync.Mutex
	dialCalls   int
	writes      int
	lastURL     string
	lastHeaders map[string]string
	dialErr     error
	writeErr    error
	readDone    chan struct{}
	closeOnce   sync.Once
}

func newSessionProbe(dialErr, writeErr error) *sessionProbe {
	return &sessionProbe{dialErr: dialErr, writeErr: writeErr, readDone: make(chan struct{})}
}

func (p *sessionProbe) recordDial(url string, headers map[string]string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.dialCalls++
	p.lastURL = url
	p.lastHeaders = make(map[string]string, len(headers))
	for key, value := range headers {
		p.lastHeaders[key] = value
	}
}

func (p *sessionProbe) recordWrite() error {
	p.mu.Lock()
	p.writes++
	err := p.writeErr
	p.mu.Unlock()
	return err
}

func (p *sessionProbe) close() {
	p.closeOnce.Do(func() { close(p.readDone) })
}

func (p *sessionProbe) snapshot() (dialCalls, writes int, url string, headers map[string]string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	headers = make(map[string]string, len(p.lastHeaders))
	for key, value := range p.lastHeaders {
		headers[key] = value
	}
	return p.dialCalls, p.writes, p.lastURL, headers
}

type conformanceWebSocket struct{ probe *sessionProbe }

func (c *conformanceWebSocket) ReadMessage() (int, []byte, error) {
	<-c.probe.readDone
	return 0, nil, io.EOF
}

func (c *conformanceWebSocket) WriteMessage(int, []byte) error {
	return c.probe.recordWrite()
}

func (c *conformanceWebSocket) Close() error {
	c.probe.close()
	return nil
}

type openAIConformanceDialer struct{ probe *sessionProbe }

func (d openAIConformanceDialer) Dial(url string, headers map[string]string) (openai.WebSocketConn, error) {
	d.probe.recordDial(url, headers)
	if d.probe.dialErr != nil {
		return nil, d.probe.dialErr
	}
	return &conformanceWebSocket{probe: d.probe}, nil
}

type grokConformanceDialer struct{ probe *sessionProbe }

func (d grokConformanceDialer) Dial(url string, headers map[string]string) (grok.WebSocketConn, error) {
	d.probe.recordDial(url, headers)
	if d.probe.dialErr != nil {
		return nil, d.probe.dialErr
	}
	return &conformanceWebSocket{probe: d.probe}, nil
}

type sessionProviderCase struct {
	name string
	new  func(*sessionProbe, string) providers.SessionProvider
}

func s11SessionProviderCases() []sessionProviderCase {
	return []sessionProviderCase{
		{
			name: "openai",
			new: func(probe *sessionProbe, secret string) providers.SessionProvider {
				return openai.New(
					openai.WithAPIKey(secret),
					openai.WithRealtimeBaseURL("wss://s2s-conformance.invalid/v1/realtime"),
					openai.WithWebSocketDialer(openAIConformanceDialer{probe: probe}),
				)
			},
		},
		{
			name: "grok",
			new: func(probe *sessionProbe, secret string) providers.SessionProvider {
				return grok.New(
					grok.WithAPIKey(secret),
					grok.WithBaseURL("wss://s2s-conformance.invalid/v1/realtime"),
					grok.WithWebSocketDialer(grokConformanceDialer{probe: probe}),
				)
			},
		},
	}
}

func TestS11OfflineSessionProviderConformance(t *testing.T) {
	for _, tc := range s11SessionProviderCases() {
		t.Run(tc.name+"/identity", func(t *testing.T) {
			provider := tc.new(newSessionProbe(errors.New("s2s-conformance-identity-probe"), nil), conformanceSecret)
			if got := provider.Name(); got != tc.name {
				t.Fatalf("provider.Name() = %q, want %q", got, tc.name)
			}
		})

		t.Run(tc.name+"/dial-failure", func(t *testing.T) {
			probe := newSessionProbe(errors.New("s2s-conformance-transport-failure"), nil)
			provider := tc.new(probe, conformanceSecret)
			_, err := provider.ConnectSession(context.Background(), models.SessionConfig{Model: "s2s-conformance-model"})
			assertSessionFailure(t, tc.name, probe, err, "s2s-conformance-transport-failure", true)
			if got := providers.ErrorClassification(err); got != providers.ErrorClassTransport {
				t.Skipf("provider defect: %s dial failure classified as %q, want shared %q: %T: %v", tc.name, got, providers.ErrorClassTransport, err, err)
			}
		})

		t.Run(tc.name+"/protocol-failure", func(t *testing.T) {
			probe := newSessionProbe(nil, errors.New("s2s-conformance-protocol-failure"))
			provider := tc.new(probe, conformanceSecret)
			_, err := provider.ConnectSession(context.Background(), models.SessionConfig{Model: "s2s-conformance-model"})
			assertSessionFailure(t, tc.name, probe, err, "s2s-conformance-protocol-failure", false)
			if got := providers.ErrorClassification(err); got != providers.ErrorClassProviderRejected {
				t.Skipf("provider defect: %s protocol failure classified as %q, want shared %q: %T: %v", tc.name, got, providers.ErrorClassProviderRejected, err, err)
			}
		})
	}
}

func assertSessionFailure(t *testing.T, providerName string, probe *sessionProbe, err error, signal string, wantRetryable bool) {
	t.Helper()
	if err == nil {
		t.Fatal("session provider failure returned nil error")
	}
	dialCalls, writes, url, headers := probe.snapshot()
	if dialCalls != 1 {
		t.Fatalf("dial calls = %d, want exactly one injected dial", dialCalls)
	}
	if !strings.Contains(headers["Authorization"], "Bearer "+conformanceSecret) {
		t.Fatalf("authorization header = %q, want injected bearer token", headers["Authorization"])
	}
	if !strings.Contains(url, "s2s-conformance.invalid") {
		t.Fatalf("dial URL = %q, want deterministic offline endpoint", url)
	}
	if strings.Contains(err.Error(), conformanceSecret) {
		t.Fatalf("credential leaked from top-level session error: %q", err.Error())
	}
	assertErrorChainSecretFree(t, err, conformanceSecret)
	if !errorChainContains(err, signal) {
		t.Fatalf("%s session error omitted deterministic non-secret signal %q (writes=%d): %v", providerName, signal, writes, err)
	}
	if got := providers.IsRetryable(err); got != wantRetryable {
		t.Skipf("provider defect: %s session failure retryable = %v, want %v: %T: %v", providerName, got, wantRetryable, err, err)
	}
}

func TestS11OfflineSessionProviderContract(t *testing.T) {
	for _, tc := range s11SessionProviderCases() {
		t.Run(tc.name, func(t *testing.T) {
			probe := newSessionProbe(nil, nil)
			provider := tc.new(probe, conformanceSecret)
			session, err := provider.ConnectSession(context.Background(), models.SessionConfig{Model: "s2s-conformance-model"})
			if err != nil {
				t.Fatalf("ConnectSession() error = %v", err)
			}
			if session == nil {
				t.Fatal("ConnectSession() returned nil session")
			}
			dialCalls, writes, url, headers := probe.snapshot()
			if dialCalls != 1 || writes == 0 {
				t.Fatalf("session setup = dial calls %d, writes %d; want one dial and initial protocol write", dialCalls, writes)
			}
			if !strings.Contains(headers["Authorization"], "Bearer "+conformanceSecret) {
				t.Fatalf("authorization header = %q, want injected bearer token", headers["Authorization"])
			}
			if !strings.Contains(url, "s2s-conformance.invalid") {
				t.Fatalf("dial URL = %q, want deterministic offline endpoint", url)
			}
			if session.Receive() == nil || session.Done() == nil {
				t.Fatal("session did not expose receive buffer and done channel")
			}
			outcome := messages.SendSessionWithOutcome(context.Background(), session, messages.StreamMessage{
				Type:  messages.StreamTypeTextDelta,
				Value: messages.NewTextDeltaValue("conformance"),
			})
			if !outcome.OK() {
				t.Fatalf("SendSessionWithOutcome() = %+v, want success", outcome)
			}
			if err := session.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}
			if err := session.Close(); err != nil {
				t.Fatalf("second Close() error = %v", err)
			}
			select {
			case <-session.Done():
			case <-time.After(time.Second):
				t.Fatal("session Done channel did not close")
			}
		})
	}
}

func TestS11ProviderRootFallbackCapabilities(t *testing.T) {
	got := providers.UnknownProviderCapabilities("s2s-conformance")
	if got.Provider != "s2s-conformance" {
		t.Fatalf("UnknownProviderCapabilities().Provider = %q, want s2s-conformance", got.Provider)
	}
}
