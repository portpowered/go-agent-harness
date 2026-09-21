package capture

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
)

type httpReplay struct {
	captures []recording.HTTPCapturePair
}

var _ http.RoundTripper = (*httpReplay)(nil)

func NewHTTPReplay(path string) (http.RoundTripper, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read HTTP capture file: %w", err)
	}
	var captures []recording.HTTPCapturePair
	if err := json.Unmarshal(data, &captures); err != nil {
		return nil, fmt.Errorf("parse HTTP capture file: %w", err)
	}
	return &httpReplay{captures: captures}, nil
}

func (r *httpReplay) RoundTrip(req *http.Request) (*http.Response, error) {
	if len(r.captures) == 0 {
		return nil, fmt.Errorf("no captures available for replay")
	}
	reqBody, err := replayRequestBody(req)
	if err != nil {
		return nil, err
	}
	for index := range r.captures {
		capture := &r.captures[index]
		if capture.Request.Method != req.Method || capture.Request.URL != req.URL.String() {
			continue
		}
		if !httpBodiesMatch(capture.Request.Body, reqBody) {
			continue
		}
		return replayResponse(req, capture.Response), nil
	}
	return nil, fmt.Errorf("no matching captures for your request, terminating")
}

func replayRequestBody(req *http.Request) ([]byte, error) {
	if req.Body == nil {
		return nil, nil
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, fmt.Errorf("read replay request body: %w", err)
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	return body, nil
}

func httpBodiesMatch(captured recording.HTTPBody, actual []byte) bool {
	if expected, received := chatCompletionShape([]byte(captured)), chatCompletionShape(actual); expected != nil && received != nil {
		return expected.equal(received)
	}
	return bytes.Equal([]byte(captured), actual)
}

func replayResponse(req *http.Request, captured recording.HTTPCapturedResponse) *http.Response {
	response := &http.Response{
		Status:        captured.Status,
		StatusCode:    captured.StatusCode,
		Header:        captured.Headers.Clone(),
		Body:          io.NopCloser(bytes.NewReader(captured.Body)),
		Request:       req,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		ContentLength: int64(len(captured.Body)),
	}
	return response
}

type chatShape struct {
	Messages []chatMessage
}

type chatMessage struct {
	Role    string
	Content []string
}

func chatCompletionShape(body []byte) *chatShape {
	var raw struct {
		Messages []struct {
			Role    json.RawMessage `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &raw); err != nil || len(raw.Messages) == 0 {
		return nil
	}
	shape := &chatShape{Messages: make([]chatMessage, len(raw.Messages))}
	for index, message := range raw.Messages {
		_ = json.Unmarshal(message.Role, &shape.Messages[index].Role)
		shape.Messages[index].Content = chatContentTypes(message.Content)
	}
	return shape
}

func chatContentTypes(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	if raw[0] == '"' {
		var value string
		if json.Unmarshal(raw, &value) == nil {
			return []string{"text"}
		}
		return nil
	}
	if raw[0] != '[' {
		return nil
	}
	var parts []struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return nil
	}
	types := make([]string, len(parts))
	for index, part := range parts {
		types[index] = part.Type
		if types[index] == "" {
			types[index] = "unknown"
		}
	}
	return types
}

func (a *chatShape) equal(b *chatShape) bool {
	if a == nil || b == nil || len(a.Messages) != len(b.Messages) {
		return a == b
	}
	for index := range a.Messages {
		left, right := a.Messages[index], b.Messages[index]
		if left.Role != right.Role || len(left.Content) != len(right.Content) {
			return false
		}
		for contentIndex := range left.Content {
			if left.Content[contentIndex] != right.Content[contentIndex] {
				return false
			}
		}
	}
	return true
}
