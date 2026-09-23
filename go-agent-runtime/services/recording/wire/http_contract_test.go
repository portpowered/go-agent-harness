package wire

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestHTTPRecordingPublicContractCapturesConsumedTrafficWithoutSecrets(t *testing.T) {
	var forwardedBody string
	service := NewService(clock.Real{})
	httpService, ok := service.(recording.HTTPRecordingService)
	if !ok {
		t.Fatal("recording service does not expose its HTTP capture contract")
	}
	recorder, err := httpService.OpenHTTPRecorder(roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		forwardedBody = string(body)
		return &http.Response{
			Status: "201 Created", StatusCode: http.StatusCreated,
			Header: http.Header{"Set-Cookie": {"provider-secret"}, "X-Result": {"stored"}},
			Body:   io.NopCloser(strings.NewReader("provider response")),
		}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	transport, ok := recorder.(http.RoundTripper)
	if !ok {
		t.Fatalf("HTTP recorder %T cannot be installed as a transport", recorder)
	}
	request, err := http.NewRequest(http.MethodPost, "https://provider.example.test/capture", strings.NewReader("request payload"))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer request-secret")
	request.Header.Set("Cookie", "session=request-secret")
	request.Header.Set("X-Request", "retained")
	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	if forwardedBody != "request payload" {
		t.Fatalf("provider received request body %q", forwardedBody)
	}
	destination := filepath.Join(t.TempDir(), "http-capture.json")
	if err := recorder.FlushToFile(destination); err == nil || !strings.Contains(err.Error(), "active response body") {
		t.Fatalf("flush with an unread response body = %v, want active-body rejection", err)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if string(body) != "provider response" {
		t.Fatalf("provider response body = %q", body)
	}
	if err := recorder.FlushToFile(destination); err != nil {
		t.Fatal(err)
	}

	assertCapturedHTTPExchange(t, recorder, destination, recorder.Captures())
}

func assertCapturedHTTPExchange(t *testing.T, recorder recording.HTTPRecorder, destination string, captures []recording.HTTPCapturePair) {
	t.Helper()
	if len(captures) != 1 {
		t.Fatalf("capture count = %d, want 1", len(captures))
	}
	got := captures[0]
	if string(got.Request.Body) != "request payload" || string(got.Response.Body) != "provider response" {
		t.Fatalf("captured bodies = %q / %q", got.Request.Body, got.Response.Body)
	}
	if _, ok := got.Request.Headers["Authorization"]; ok {
		t.Fatal("captured request retained Authorization")
	}
	if _, ok := got.Request.Headers["Cookie"]; ok {
		t.Fatal("captured request retained Cookie")
	}
	if _, ok := got.Response.Headers["Set-Cookie"]; ok {
		t.Fatal("captured response retained Set-Cookie")
	}
	if got.Request.Headers["X-Request"][0] != "retained" || got.Response.Headers["X-Result"][0] != "stored" {
		t.Fatalf("safe headers were not retained: request=%v response=%v", got.Request.Headers, got.Response.Headers)
	}
	got.Request.Headers["X-Request"][0] = "mutated"
	got.Response.Body[0] = 'X'
	if recorder.Captures()[0].Request.Headers["X-Request"][0] != "retained" || string(recorder.Captures()[0].Response.Body) != "provider response" {
		t.Fatal("capture snapshot mutation changed recording-owned state")
	}

	data, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	var persisted []recording.HTTPCapturePair
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	if len(persisted) != 1 || string(persisted[0].Response.Body) != "provider response" {
		t.Fatalf("persisted HTTP capture = %#v", persisted)
	}
}

func TestHTTPRecordingDoesNotPublishAfterTransportFailure(t *testing.T) {
	service := NewService(clock.Real{})
	httpService := service.(recording.HTTPRecordingService)
	transportErr := errors.New("provider transport failed")
	recorder, err := httpService.OpenHTTPRecorder(roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, transportErr
	}))
	if err != nil {
		t.Fatal(err)
	}
	transport := recorder.(http.RoundTripper)
	request, err := http.NewRequest(http.MethodGet, "https://provider.example.test/failure", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transport.RoundTrip(request); !errors.Is(err, transportErr) {
		t.Fatalf("transport result = %v, want original provider error", err)
	}
	destination := filepath.Join(t.TempDir(), "failed-capture.json")
	if err := recorder.FlushToFile(destination); !errors.Is(err, transportErr) {
		t.Fatalf("failed capture flush = %v, want original provider error", err)
	}
	if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed HTTP exchange published a capture: %v", err)
	}
}

func TestHTTPRecordingDoesNotPublishResponseClosedBeforeEOF(t *testing.T) {
	service := NewService(clock.Real{})
	httpService := service.(recording.HTTPRecordingService)
	const responseBody = "response body not consumed"
	recorder, err := httpService.OpenHTTPRecorder(roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			Status: "200 OK", StatusCode: http.StatusOK,
			Header: make(http.Header), ContentLength: int64(len(responseBody)),
			Body: io.NopCloser(strings.NewReader(responseBody)),
		}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	transport := recorder.(http.RoundTripper)
	request, err := http.NewRequest(http.MethodGet, "https://provider.example.test/partial", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatalf("close response body: %v", err)
	}
	destination := filepath.Join(t.TempDir(), "truncated-capture.json")
	if err := recorder.FlushToFile(destination); err == nil || !strings.Contains(err.Error(), "closed before EOF") {
		t.Fatalf("flush after early body close = %v, want incomplete-capture error", err)
	}
	if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("truncated HTTP response was published: %v", err)
	}
}

func TestHTTPRecordingDoesNotPublishResponseShorterThanContentLength(t *testing.T) {
	service := NewService(clock.Real{})
	httpService := service.(recording.HTTPRecordingService)
	const responseBody = "short provider response"
	recorder, err := httpService.OpenHTTPRecorder(roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			Status: "200 OK", StatusCode: http.StatusOK,
			Header: make(http.Header), ContentLength: int64(len(responseBody) + 1),
			Body: io.NopCloser(strings.NewReader(responseBody)),
		}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	transport := recorder.(http.RoundTripper)
	request, err := http.NewRequest(http.MethodGet, "https://provider.example.test/truncated", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(response.Body); err != nil {
		t.Fatalf("read synthetic short response: %v", err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "short-capture.json")
	if err := recorder.FlushToFile(destination); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("flush after short response = %v, want unexpected EOF", err)
	}
	if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("short HTTP response was published: %v", err)
	}
}

func TestHTTPRecordingRejectsNonTransportAdapter(t *testing.T) {
	service := NewService(clock.Real{}).(recording.HTTPRecordingService)
	if _, err := service.OpenHTTPRecorder(struct{}{}); err == nil {
		t.Fatal("non-transport HTTP adapter was accepted")
	}
}
