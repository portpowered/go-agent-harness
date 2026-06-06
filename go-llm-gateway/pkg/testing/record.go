package testing

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
)

// RecordRoundTripper is an http.RoundTripper that records request/response pairs
// in memory. Call FlushToFile to write all captures to a single JSON file.
type RecordRoundTripper struct {
	transport http.RoundTripper
	captures  []CapturePair
	mu        sync.Mutex
}

var _ http.RoundTripper = (*RecordRoundTripper)(nil)

// NewRecordRoundTripper creates a RecordRoundTripper that stores captures in memory.
func NewRecordRoundTripper(transport http.RoundTripper) *RecordRoundTripper {
	if transport == nil {
		transport = http.DefaultTransport
	}
	return &RecordRoundTripper{
		transport: transport,
		captures:  make([]CapturePair, 0),
	}
}

// RoundTrip executes the request, records the pair, and returns the response.
func (t *RecordRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	capturedReq := t.captureRequest(req)

	resp, err := t.transport.RoundTrip(req)
	if err != nil {
		t.mu.Lock()
		t.captures = append(t.captures, CapturePair{
			Request:  capturedReq,
			Response: CapturedResponse{},
		})
		t.mu.Unlock()
		return nil, err
	}

	capturedResp := t.captureResponse(resp)

	t.mu.Lock()
	t.captures = append(t.captures, CapturePair{
		Request:  capturedReq,
		Response: capturedResp,
	})
	t.mu.Unlock()

	resp.Body = io.NopCloser(bytes.NewReader([]byte(capturedResp.Body)))
	return resp, nil
}

// FlushToFile writes all captured request/response pairs to a JSON file.
// The file format is a JSON array of CapturePair objects.
func (t *RecordRoundTripper) FlushToFile(path string) error {
	t.mu.Lock()
	captures := make([]CapturePair, len(t.captures))
	copy(captures, t.captures)
	t.mu.Unlock()

	data, err := json.MarshalIndent(captures, "", "  ")
	if err != nil {
		return fmt.Errorf("encode captures: %w", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write capture file: %w", err)
	}
	return nil
}

// Captures returns a copy of the recorded pairs (for inspection).
func (t *RecordRoundTripper) Captures() []CapturePair {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]CapturePair, len(t.captures))
	copy(out, t.captures)
	return out
}

func (t *RecordRoundTripper) captureRequest(req *http.Request) CapturedRequest {
	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
		req.Body = io.NopCloser(bytes.NewReader(body))
	}
	return CapturedRequest{
		Method:  req.Method,
		URL:     req.URL.String(),
		Headers: req.Header.Clone(),
		Body:    rawBody(body),
	}
}

func (t *RecordRoundTripper) captureResponse(resp *http.Response) CapturedResponse {
	var body []byte
	if resp.Body != nil {
		body, _ = io.ReadAll(resp.Body)
	}
	return CapturedResponse{
		StatusCode: resp.StatusCode,
		Status:     resp.Status,
		Headers:    resp.Header.Clone(),
		Body:       rawBody(body),
	}
}
