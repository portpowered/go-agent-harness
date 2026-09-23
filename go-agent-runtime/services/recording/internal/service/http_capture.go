package service

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

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
)

const privateCaptureFileMode = 0o600

type httpResponseBodyError string

func (e httpResponseBodyError) Error() string { return string(e) }

const errHTTPResponseBodyClosedEarly = httpResponseBodyError("HTTP response body closed before EOF")

type httpRecorder struct {
	transport  http.RoundTripper
	captures   []recording.HTTPCapturePair
	pending    map[int]struct{}
	captureErr error
	mu         sync.Mutex
}

var _ recording.HTTPRecorder = (*httpRecorder)(nil)

func newHTTPRecorder(transport http.RoundTripper) recording.HTTPRecorder {
	if transport == nil {
		transport = http.DefaultTransport
	}
	return &httpRecorder{
		transport: transport,
		captures:  make([]recording.HTTPCapturePair, 0),
		pending:   make(map[int]struct{}),
	}
}

func (t *httpRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	capturedReq, err := captureHTTPRequest(req)
	if err != nil {
		t.mu.Lock()
		t.captureErr = errors.Join(t.captureErr, err)
		t.mu.Unlock()
		return nil, err
	}

	resp, err := t.transport.RoundTrip(req)
	if err != nil {
		t.mu.Lock()
		t.captureErr = errors.Join(t.captureErr, err)
		t.captures = append(t.captures, recording.HTTPCapturePair{Request: capturedReq})
		t.mu.Unlock()
		return nil, err
	}

	capturedResp := recording.HTTPCapturedResponse{
		StatusCode: resp.StatusCode,
		Status:     resp.Status,
		Headers:    captureHTTPHeaders(resp.Header),
	}
	t.mu.Lock()
	index := len(t.captures)
	t.captures = append(t.captures, recording.HTTPCapturePair{Request: capturedReq, Response: capturedResp})
	if resp.Body != nil {
		t.pending[index] = struct{}{}
	}
	t.mu.Unlock()
	if resp.Body != nil {
		resp.Body = &httpResponseBody{inner: resp.Body, recorder: t, index: index, expectedLength: resp.ContentLength}
	}
	return resp, nil
}

func (t *httpRecorder) FlushToFile(path string) error {
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
	if err := os.WriteFile(path, data, privateCaptureFileMode); err != nil {
		return fmt.Errorf("write capture file: %w", err)
	}
	return nil
}

func (t *httpRecorder) Captures() []recording.HTTPCapturePair {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.copyCapturesLocked()
}

func (t *httpRecorder) copyCapturesLocked() []recording.HTTPCapturePair {
	out := make([]recording.HTTPCapturePair, len(t.captures))
	for index, pair := range t.captures {
		pair.Request.Headers = cloneHTTPHeaders(pair.Request.Headers)
		pair.Request.Body = append(recording.HTTPBody(nil), pair.Request.Body...)
		pair.Response.Headers = cloneHTTPHeaders(pair.Response.Headers)
		pair.Response.Body = append(recording.HTTPBody(nil), pair.Response.Body...)
		out[index] = pair
	}
	return out
}

func captureHTTPRequest(req *http.Request) (recording.HTTPCapturedRequest, error) {
	var body []byte
	if req.Body != nil {
		var err error
		body, err = io.ReadAll(req.Body)
		err = errors.Join(err, req.Body.Close())
		if err != nil {
			return recording.HTTPCapturedRequest{}, fmt.Errorf("capture HTTP request body: %w", err)
		}
		req.Body = io.NopCloser(bytes.NewReader(body))
	}
	return recording.HTTPCapturedRequest{
		Method:  req.Method,
		URL:     req.URL.String(),
		Headers: captureHTTPHeaders(req.Header),
		Body:    recording.HTTPBody(body),
	}, nil
}

func captureHTTPHeaders(headers http.Header) recording.HTTPHeaders {
	result := make(recording.HTTPHeaders, len(headers))
	for name, values := range headers {
		result[name] = append([]string(nil), values...)
	}
	for name := range result {
		switch strings.ToLower(name) {
		case "authorization", "proxy-authorization", "x-api-key", "api-key", "cookie", "set-cookie":
			delete(result, name)
		}
	}
	return result
}

func cloneHTTPHeaders(headers recording.HTTPHeaders) recording.HTTPHeaders {
	result := make(recording.HTTPHeaders, len(headers))
	for name, values := range headers {
		result[name] = append([]string(nil), values...)
	}
	return result
}

type httpResponseBody struct {
	inner          io.ReadCloser
	recorder       *httpRecorder
	index          int
	readEOF        bool
	readBytes      int64
	expectedLength int64
	closeOnce      sync.Once
	closeErr       error
}

func (b *httpResponseBody) Read(buffer []byte) (int, error) {
	count, err := b.inner.Read(buffer)
	b.recorder.mu.Lock()
	b.recorder.captures[b.index].Response.Body = append(b.recorder.captures[b.index].Response.Body, buffer[:count]...)
	b.readBytes += int64(count)
	captureErr := err
	if errors.Is(err, io.EOF) {
		b.readEOF = true
		if b.expectedLength > 0 && b.readBytes < b.expectedLength {
			captureErr = io.ErrUnexpectedEOF
		}
	}
	if captureErr != nil {
		b.recorder.finishBodyLocked(b.index, captureErr)
	}
	b.recorder.mu.Unlock()
	return count, err
}

func (b *httpResponseBody) Close() error {
	b.closeOnce.Do(func() {
		b.closeErr = b.inner.Close()
		b.recorder.mu.Lock()
		captureErr := b.closeErr
		if captureErr == nil && !b.readEOF && b.expectedLength > 0 && b.readBytes < b.expectedLength {
			captureErr = errHTTPResponseBodyClosedEarly
		}
		b.recorder.finishBodyLocked(b.index, captureErr)
		b.recorder.mu.Unlock()
	})
	return b.closeErr
}

func (t *httpRecorder) finishBodyLocked(index int, err error) {
	delete(t.pending, index)
	if err != nil && !errors.Is(err, io.EOF) && t.captureErr == nil {
		t.captureErr = err
	}
}

func (*Service) OpenHTTPRecorder(value any) (recording.HTTPRecorder, error) {
	transport, ok := value.(http.RoundTripper)
	if !ok {
		return nil, errors.New("HTTP recording requires an http.RoundTripper")
	}
	return newHTTPRecorder(transport), nil
}

var _ recording.HTTPRecordingService = (*Service)(nil)
