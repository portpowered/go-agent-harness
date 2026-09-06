package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	llmproviders "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

type staticRoundTripper struct {
	statusCode int
	headers    http.Header
	body       string
	calls      int
}

func (t *staticRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	t.calls++
	return &http.Response{
		StatusCode: t.statusCode,
		Status:     http.StatusText(t.statusCode),
		Header:     t.headers.Clone(),
		Body:       io.NopCloser(strings.NewReader(t.body)),
		Request:    req,
	}, nil
}

func TestBuildProviderHTTPRuntime_LiveModeUsesExplicitDefaultTransport(t *testing.T) {
	runtime, err := buildProviderHTTPRuntime(&providers.Config{})
	if err != nil {
		t.Fatalf("buildProviderHTTPRuntime() error = %v", err)
	}
	if runtime.Client == nil {
		t.Fatal("expected HTTP client")
	}
	if runtime.Client.Transport != http.DefaultTransport {
		t.Fatalf("transport = %#v, want http.DefaultTransport", runtime.Client.Transport)
	}
	if runtime.Recorder != nil {
		t.Fatal("expected no recorder in live mode")
	}
}

func TestBuildProviderHTTPRuntime_LiveModeUsesInjectedBaseTransport(t *testing.T) {
	transport := &staticRoundTripper{
		statusCode: http.StatusAccepted,
		body:       `{"runtime":"injected"}`,
	}
	runtime, err := buildProviderHTTPRuntime(&providers.Config{}, transport)
	if err != nil {
		t.Fatalf("buildProviderHTTPRuntime() error = %v", err)
	}
	if runtime.Client == nil {
		t.Fatal("expected HTTP client")
	}
	if runtime.Client.Transport != transport {
		t.Fatalf("transport = %#v, want injected transport", runtime.Client.Transport)
	}

	resp, err := runtime.Client.Get("https://example.test/v1/chat/completions")
	if err != nil {
		t.Fatalf("runtime.Client.Get() error = %v", err)
	}
	defer closeHTTPResponseForTest(t, resp)
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("io.ReadAll() error = %v", err)
	}
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("resp.StatusCode = %d, want %d", resp.StatusCode, http.StatusAccepted)
	}
	if string(body) != `{"runtime":"injected"}` {
		t.Fatalf("response body = %q, want injected transport body", string(body))
	}
	if transport.calls != 1 {
		t.Fatalf("transport calls = %d, want 1", transport.calls)
	}
}

func TestBuildProviderHTTPRuntime_RecordModeReturnsRecorderBackedClient(t *testing.T) {
	runtime, err := buildProviderHTTPRuntime(&providers.Config{RecordPath: filepath.Join(t.TempDir(), "capture.json")})
	if err != nil {
		t.Fatalf("buildProviderHTTPRuntime() error = %v", err)
	}
	if runtime.Client == nil {
		t.Fatal("expected HTTP client")
	}
	if runtime.Recorder == nil {
		t.Fatal("expected recorder in record mode")
	}
	if runtime.Client.Transport != runtime.Recorder {
		t.Fatal("expected recorder transport to back the client")
	}
}

func TestBuildProviderHTTPRuntime_RecordModeWrapsInjectedBaseTransport(t *testing.T) {
	transport := &staticRoundTripper{
		statusCode: http.StatusCreated,
		body:       `{"recorded":"injected"}`,
	}
	runtime, err := buildProviderHTTPRuntime(
		&providers.Config{RecordPath: filepath.Join(t.TempDir(), "capture.json")},
		transport,
	)
	if err != nil {
		t.Fatalf("buildProviderHTTPRuntime() error = %v", err)
	}
	if runtime.Client == nil {
		t.Fatal("expected HTTP client")
	}
	if runtime.Recorder == nil {
		t.Fatal("expected recorder in record mode")
	}
	if runtime.Client.Transport != runtime.Recorder {
		t.Fatal("expected recorder transport to back the client")
	}

	req, err := http.NewRequest(http.MethodPost, "https://example.test/v1/chat/completions", strings.NewReader(`{"message":"hello"}`))
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	resp, err := runtime.Client.Do(req)
	if err != nil {
		t.Fatalf("runtime.Client.Do() error = %v", err)
	}
	defer closeHTTPResponseForTest(t, resp)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("resp.StatusCode = %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	if transport.calls != 1 {
		t.Fatalf("transport calls = %d, want 1", transport.calls)
	}

	// The capture observes consumption without pre-reading a streaming body.
	if _, err := io.ReadAll(resp.Body); err != nil {
		t.Fatal(err)
	}
	captures := runtime.Recorder.Captures()
	if len(captures) != 1 {
		t.Fatalf("len(captures) = %d, want 1", len(captures))
	}
	if captures[0].Response.StatusCode != http.StatusCreated {
		t.Fatalf("recorded status = %d, want %d", captures[0].Response.StatusCode, http.StatusCreated)
	}
	if string(captures[0].Response.Body) != `{"recorded":"injected"}` {
		t.Fatalf("recorded body = %q, want injected transport body", string(captures[0].Response.Body))
	}
}

func TestBuildProviderHTTPRuntime_RecordModeCapturesRoundTripAndFlushes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if _, err := io.WriteString(w, `{"ok":true}`); err != nil {
			t.Errorf("write fixture response: %v", err)
		}
	}))
	defer server.Close()

	runtime, err := buildProviderHTTPRuntime(&providers.Config{RecordPath: filepath.Join(t.TempDir(), "capture.json")})
	if err != nil {
		t.Fatalf("buildProviderHTTPRuntime() error = %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", strings.NewReader(`{"message":"hello"}`))
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := runtime.Client.Do(req)
	if err != nil {
		t.Fatalf("runtime.Client.Do() error = %v", err)
	}
	defer closeHTTPResponseForTest(t, resp)

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("io.ReadAll() error = %v", err)
	}
	if string(body) != `{"ok":true}` {
		t.Fatalf("response body = %q, want %q", string(body), `{"ok":true}`)
	}

	capturePath := filepath.Join(t.TempDir(), "captured.json")
	if err := runtime.Recorder.FlushToFile(capturePath); err != nil {
		t.Fatalf("FlushToFile() error = %v", err)
	}

	raw, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatalf("os.ReadFile() error = %v", err)
	}

	var captures []gwtesting.CapturePair
	if err := json.Unmarshal(raw, &captures); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if len(captures) != 1 {
		t.Fatalf("len(captures) = %d, want 1", len(captures))
	}
	if captures[0].Request.URL != server.URL+"/v1/chat/completions" {
		t.Fatalf("captures[0].Request.URL = %q, want %q", captures[0].Request.URL, server.URL+"/v1/chat/completions")
	}
	if string(captures[0].Request.Body) != `{"message":"hello"}` {
		t.Fatalf("captures[0].Request.Body = %q, want %q", string(captures[0].Request.Body), `{"message":"hello"}`)
	}
	if string(captures[0].Response.Body) != `{"ok":true}` {
		t.Fatalf("captures[0].Response.Body = %q, want %q", string(captures[0].Response.Body), `{"ok":true}`)
	}
}

func TestBuildProviderHTTPRuntime_ReplayModeUsesReplayTransport(t *testing.T) {
	fixturePath := filepath.Join("testdata", "streaming_2_2.json")
	runtime, err := buildProviderHTTPRuntime(&providers.Config{ReplayPath: fixturePath})
	if err != nil {
		t.Fatalf("buildProviderHTTPRuntime() error = %v", err)
	}
	if runtime.Client == nil {
		t.Fatal("expected HTTP client")
	}
	if runtime.Client.Transport == nil {
		t.Fatal("expected replay transport")
	}
	if runtime.Client.Transport == http.DefaultTransport {
		t.Fatal("expected replay transport, got http.DefaultTransport")
	}
	if runtime.Recorder != nil {
		t.Fatal("expected no recorder in replay mode")
	}
}

func TestBuildProviderHTTPRuntime_ReplayModeServesCapturedResponse(t *testing.T) {
	fixturePath := filepath.Join("testdata", "streaming_2_2.json")
	runtime, err := buildProviderHTTPRuntime(&providers.Config{ReplayPath: fixturePath})
	if err != nil {
		t.Fatalf("buildProviderHTTPRuntime() error = %v", err)
	}

	reqBody := `{"messages":[{"content":[{"text":"what is 2 + 2?","type":"text"}],"role":"user"}],"model":"z-ai/glm-4.7","tools":[{"function":{"name":"edit_file","description":"Edit a file","parameters":{"properties":{"path":{"type":"string"}},"required":["path"],"type":"object"}},"type":"function"}],"stream":true}`
	req, err := http.NewRequest(http.MethodPost, "https://openrouter.ai/api/v1/chat/completions", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := runtime.Client.Do(req)
	if err != nil {
		t.Fatalf("runtime.Client.Do() error = %v", err)
	}
	defer closeHTTPResponseForTest(t, resp)

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("io.ReadAll() error = %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("resp.StatusCode = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if !strings.Contains(string(body), "4") {
		t.Fatalf("response body %q should contain replayed answer", string(body))
	}
}

func TestBuildProviderHTTPRuntime_ReplayModePropagatesFixtureErrors(t *testing.T) {
	_, err := buildProviderHTTPRuntime(&providers.Config{ReplayPath: filepath.Join(t.TempDir(), "missing.json")})
	if err == nil {
		t.Fatal("expected replay fixture error")
	}
}

// This adapter retains the original runtime transport assertions while the
// production graph is now owned by the provider service.
type providerHTTPTestRuntime struct {
	Client   *http.Client
	Recorder *gwtesting.RecordRoundTripper
}

func buildProviderHTTPRuntime(cfg *providers.Config, transports ...http.RoundTripper) (providerHTTPTestRuntime, error) {
	client := &http.Client{}
	if len(transports) != 0 {
		client.Transport = transports[0]
	}
	invocation, recorder, err := New(client, nil, clock.Real{}, nil).httpRuntime(*cfg)
	if err != nil {
		return providerHTTPTestRuntime{}, err
	}
	var concrete *gwtesting.RecordRoundTripper
	if recorder != nil {
		var ok bool
		concrete, ok = recorder.(*gwtesting.RecordRoundTripper)
		if !ok {
			return providerHTTPTestRuntime{}, fmt.Errorf("unexpected recorder type %T", recorder)
		}
	}
	return providerHTTPTestRuntime{Client: invocation.httpClient, Recorder: concrete}, nil
}
func closeHTTPResponseForTest(t *testing.T, response *http.Response) {
	t.Helper()
	if err := response.Body.Close(); err != nil {
		t.Errorf("close response: %v", err)
	}
}

func TestHTTPRecordingPreservesProviderCapabilities(t *testing.T) {
	service := New(nil, nil, clock.Real{}, nil)
	cfg := providers.Config{Provider: "openai", Model: "model", APIKey: "configured-test-key"}
	plain, err := service.Build(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg.RecordPath = filepath.Join(t.TempDir(), "capture.json")
	recorded, err := service.Build(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	plainReporter, plainOK := plain.(llmproviders.CapabilityReporter)
	recordedReporter, recordedOK := recorded.(llmproviders.CapabilityReporter)
	if !plainOK || !recordedOK {
		t.Fatal("recording lost capability reporter")
	}
	if !reflect.DeepEqual(plainReporter.Capabilities(), recordedReporter.Capabilities()) {
		t.Fatal("recording changed provider capabilities")
	}
}

type observedHTTPBody struct {
	reader   io.Reader
	reads    int
	readErr  error
	closeErr error
}

func (b *observedHTTPBody) Read(dst []byte) (int, error) {
	b.reads++
	count, err := b.reader.Read(dst)
	if b.readErr != nil {
		err = b.readErr
	}
	return count, err
}
func (b *observedHTTPBody) Close() error { return b.closeErr }

type responseTransport struct{ body io.ReadCloser }

func (r responseTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: r.body}, nil
}

func TestHTTPRecordingPreservesStreamingAndSnapshotOwnership(t *testing.T) {
	body := &observedHTTPBody{reader: strings.NewReader("first then last")}
	capture := gwtesting.NewRecordRoundTripper(responseTransport{body: body})
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://fixture.invalid", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := capture.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	defer closeHTTPResponseForTest(t, response)
	if body.reads != 0 {
		t.Fatalf("recorder consumed %d reads before returning headers", body.reads)
	}
	path := filepath.Join(t.TempDir(), "capture.json")
	if err := capture.FlushToFile(path); err == nil {
		t.Fatal("active response was published as final evidence")
	}
	first := make([]byte, len("first"))
	if _, err := io.ReadFull(response.Body, first); err != nil {
		t.Fatal(err)
	}
	snapshot := capture.Captures()
	if string(snapshot[0].Response.Body) != "first" {
		t.Fatalf("partial capture = %q", snapshot[0].Response.Body)
	}
	snapshot[0].Response.Body[0] = 'X'
	rest, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(first)+string(rest) != "first then last" {
		t.Fatal("streamed bytes changed")
	}
	if string(capture.Captures()[0].Response.Body) != "first then last" {
		t.Fatal("snapshot mutated recorded bytes")
	}
	if err := capture.FlushToFile(path); err != nil {
		t.Fatal(err)
	}
}

func TestHTTPRecordingRejectsReadAndCloseFailures(t *testing.T) {
	for _, failure := range []string{"read", "close"} {
		t.Run(failure, func(t *testing.T) {
			cause := errors.New("body failed")
			body := &observedHTTPBody{reader: strings.NewReader("partial")}
			if failure == "read" {
				body.readErr = cause
			} else {
				body.closeErr = cause
			}
			capture := gwtesting.NewRecordRoundTripper(responseTransport{body: body})
			request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://fixture.invalid", nil)
			if err != nil {
				t.Fatal(err)
			}
			response, err := capture.RoundTrip(request)
			if err != nil {
				t.Fatal(err)
			}
			data, readErr := io.ReadAll(response.Body)
			closeErr := response.Body.Close()
			if string(data) != "partial" || !errors.Is(errors.Join(readErr, closeErr), cause) {
				t.Fatalf("body = %q, read/close = %v/%v", data, readErr, closeErr)
			}
			if err := capture.FlushToFile(filepath.Join(t.TempDir(), "capture.json")); !errors.Is(err, cause) {
				t.Fatalf("incomplete capture error = %v", err)
			}
		})
	}
}

func TestHTTPRecordingRejectsTruncatedRequestAndRetainsFailure(t *testing.T) {
	failure := errors.New("request body interrupted")
	body := &observedHTTPBody{reader: strings.NewReader("partial"), readErr: failure}
	transport := &staticRoundTripper{}
	recorder := gwtesting.NewRecordRoundTripper(transport)
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://fixture.invalid", body)
	if err != nil {
		t.Fatal(err)
	}
	response, err := recorder.RoundTrip(request)
	if response != nil {
		closeHTTPResponseForTest(t, response)
	}
	if !errors.Is(err, failure) {
		t.Fatalf("request failure = %v", err)
	}
	if transport.calls != 0 {
		t.Fatal("truncated request reached transport")
	}
	if err := recorder.FlushToFile(filepath.Join(t.TempDir(), "incomplete.json")); !errors.Is(err, failure) {
		t.Fatalf("finalization lost request failure: %v", err)
	}
}

func TestHTTPRecordingOmitsCredentialHeadersWithoutMutatingTransport(t *testing.T) {
	transport := &staticRoundTripper{statusCode: http.StatusOK, headers: http.Header{"Set-Cookie": {"session=fixture"}}}
	recorder := gwtesting.NewRecordRoundTripper(transport)
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://fixture.invalid", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer fixture")
	request.Header.Set("X-Api-Key", "fixture")
	request.Header.Set("X-Request-ID", "request-fixture")
	response, err := recorder.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	closeHTTPResponseForTest(t, response)
	pair := recorder.Captures()[0]
	if pair.Request.Headers.Get("Authorization") != "" || pair.Request.Headers.Get("X-Api-Key") != "" || pair.Response.Headers.Get("Set-Cookie") != "" {
		t.Fatal("capture retained credential headers")
	}
	if request.Header.Get("Authorization") != "Bearer fixture" || pair.Request.Headers.Get("X-Request-ID") != "request-fixture" {
		t.Fatal("capture mutated live authorization or removed correlation metadata")
	}
}
