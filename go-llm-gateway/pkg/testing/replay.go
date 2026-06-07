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

// ReplayRoundTripper is an http.RoundTripper that replays captured request/response
// pairs from a JSON file (as produced by RecordRoundTripper.FlushToFile).
type ReplayRoundTripper struct {
	captures   []CapturePair
	capturesMu sync.RWMutex
}

var _ http.RoundTripper = (*ReplayRoundTripper)(nil)

// NewReplayRoundTripper creates a ReplayRoundTripper that loads captures from
// the given JSON file path.
func NewReplayRoundTripper(path string) (*ReplayRoundTripper, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read capture file: %w", err)
	}
	var captures []CapturePair
	if err := json.Unmarshal(data, &captures); err != nil {
		return nil, fmt.Errorf("parse capture file: %w", err)
	}
	return &ReplayRoundTripper{captures: captures}, nil
}

// RoundTrip returns the next captured response, matching by URL and method
// when possible, otherwise sequential.
func (t *ReplayRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	t.capturesMu.RLock()
	captures := t.captures
	t.capturesMu.RUnlock()

	if len(captures) == 0 {
		return nil, fmt.Errorf("no captures available for replay")
	}

	capturedResp := t.findMatchingCapture(req, captures)
	if capturedResp == nil {
		return nil, fmt.Errorf("no matching captures for your request, terminating")
	}

	return t.buildResponse(req, capturedResp), nil
}

func (t *ReplayRoundTripper) findMatchingCapture(req *http.Request, captures []CapturePair) *CapturedResponse {
	reqURL := req.URL.String()
	reqMethod := req.Method
	var reqBody []byte
	if req.Body != nil {
		reqBody, _ = io.ReadAll(req.Body)
		req.Body = io.NopCloser(bytes.NewReader(reqBody))
	}
	reqShape := extractChatCompletionShape(reqBody)
	bodiesMatch := func(captured rawBody) bool {
		if reqShape != nil {
			capturedShape := extractChatCompletionShape([]byte(captured))
			if capturedShape != nil && chatCompletionShapesEqual(reqShape, capturedShape) {
				return true
			}
		}
		return bytes.Equal([]byte(captured), reqBody)
	}
	for i := range captures {
		if captures[i].Request.Method == reqMethod && captures[i].Request.URL == reqURL && bodiesMatch(captures[i].Request.Body) {
			return &captures[i].Response
		}
	}
	return nil
}

func (t *ReplayRoundTripper) buildResponse(req *http.Request, captured *CapturedResponse) *http.Response {
	resp := &http.Response{
		Status:     captured.Status,
		StatusCode: captured.StatusCode,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader([]byte(captured.Body))),
		Request:    req,
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
	}
	for k, v := range captured.Headers {
		for _, vv := range v {
			resp.Header.Add(k, vv)
		}
	}
	resp.ContentLength = int64(len([]byte(captured.Body)))
	return resp
}
