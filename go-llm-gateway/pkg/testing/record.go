package testing

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
)

// RecordRoundTripper is an http.RoundTripper that records request/response pairs
// in memory. Call FlushToFile to write all captures to a single JSON file.
type RecordRoundTripper struct {
	transport  http.RoundTripper
	captures   []CapturePair
	mu         sync.Mutex
	pending    map[int]struct{}
	captureErr error
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
		pending:   make(map[int]struct{}),
	}
}

// RoundTrip records response headers immediately and tees body reads without
// waiting for the complete response. The caller retains body ownership.
func (t *RecordRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	capturedReq, captureErr := t.captureRequest(req)
	if captureErr != nil {
		t.mu.Lock()
		t.captureErr = errors.Join(t.captureErr, captureErr)
		t.mu.Unlock()
		return nil, captureErr
	}

	resp, err := t.transport.RoundTrip(req)
	if err != nil {
		t.mu.Lock()
		t.captureErr = errors.Join(t.captureErr, err)
		t.captures = append(t.captures, CapturePair{
			Request:  capturedReq,
			Response: CapturedResponse{},
		})
		t.mu.Unlock()
		return nil, err
	}

	capturedResp := CapturedResponse{StatusCode: resp.StatusCode, Status: resp.Status, Headers: captureHeaders(resp.Header)}

	t.mu.Lock()
	index := len(t.captures)
	t.captures = append(t.captures, CapturePair{Request: capturedReq, Response: capturedResp})
	if resp.Body != nil {
		t.pending[index] = struct{}{}
	}
	t.mu.Unlock()
	if resp.Body != nil {
		resp.Body = &recordedHTTPBody{inner: resp.Body, recorder: t, index: index}
	}
	return resp, nil
}

// FlushToFile writes all captured request/response pairs to a JSON file.
// The file format is a JSON array of CapturePair objects.
func (t *RecordRoundTripper) FlushToFile(path string) error {
	t.mu.Lock()
	if len(t.pending) != 0 {
		t.mu.Unlock()
		return errors.New("HTTP capture has an active response body")
	}
	if t.captureErr != nil {
		err := t.captureErr
		t.mu.Unlock()
		return fmt.Errorf("HTTP capture is incomplete: %w", err)
	}
	captures := t.copyCapturesLocked()
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
	return t.copyCapturesLocked()
}

func (t *RecordRoundTripper) captureRequest(req *http.Request) (CapturedRequest, error) {
	var body []byte
	if req.Body != nil {
		var err error
		body, err = io.ReadAll(req.Body)
		err = errors.Join(err, req.Body.Close())
		if err != nil {
			return CapturedRequest{}, fmt.Errorf("capture HTTP request body: %w", err)
		}
		req.Body = io.NopCloser(bytes.NewReader(body))
	}
	return CapturedRequest{
		Method:  req.Method,
		URL:     req.URL.String(),
		Headers: captureHeaders(req.Header),
		Body:    rawBody(body),
	}, nil
}

// recordedHTTPBody observes bytes as the provider consumes them, preserving
// streaming latency. It performs no file writes or background reads.
type recordedHTTPBody struct {
	inner     io.ReadCloser
	recorder  *RecordRoundTripper
	index     int
	closeOnce sync.Once
	closeErr  error
}

func (b *recordedHTTPBody) Read(buffer []byte) (int, error) {
	count, err := b.inner.Read(buffer)
	b.recorder.mu.Lock()
	b.recorder.captures[b.index].Response.Body = append(b.recorder.captures[b.index].Response.Body, buffer[:count]...)
	if err != nil {
		b.recorder.finishBodyLocked(b.index, err)
	}
	b.recorder.mu.Unlock()
	return count, err
}

func (b *recordedHTTPBody) Close() error {
	b.closeOnce.Do(func() {
		b.closeErr = b.inner.Close()
		b.recorder.mu.Lock()
		b.recorder.finishBodyLocked(b.index, b.closeErr)
		b.recorder.mu.Unlock()
	})
	return b.closeErr
}

func (t *RecordRoundTripper) finishBodyLocked(index int, err error) {
	delete(t.pending, index)
	if err != nil && !errors.Is(err, io.EOF) && t.captureErr == nil {
		t.captureErr = err
	}
}

func (t *RecordRoundTripper) copyCapturesLocked() []CapturePair {
	out := make([]CapturePair, len(t.captures))
	for index, pair := range t.captures {
		pair.Request.Headers = pair.Request.Headers.Clone()
		pair.Request.Body = append(rawBody(nil), pair.Request.Body...)
		pair.Response.Headers = pair.Response.Headers.Clone()
		pair.Response.Body = append(rawBody(nil), pair.Response.Body...)
		out[index] = pair
	}
	return out
}

// captureHeaders excludes credential-bearing headers from portable artifacts.
// The original request and response headers remain owned by their caller.
func captureHeaders(headers http.Header) http.Header {
	result := headers.Clone()
	for name := range result {
		switch strings.ToLower(name) {
		case "authorization", "proxy-authorization", "x-api-key", "api-key", "cookie", "set-cookie":
			delete(result, name)
		}
	}
	return result
}
