// Package fakechatgpt is a scripted, in-process fake of the ChatGPT Codex
// backend (https://chatgpt.com/backend-api/codex): POST /responses answered
// with a Responses SSE stream and GET /models answered with a model list. It
// is test support: only _test.go files may import it
// (docs/architecture/test-support-packages.md).
//
// A Server is an http.Handler for httptest.NewServer. It checks the bearer
// token and chatgpt-account-id header it was given, records every request
// (method, path, query, headers, body), and answers each POST /responses
// with the next scripted Reply. It never contacts the network.
package fakechatgpt

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

const (
	responsesPath = "/responses"
	modelsPath    = "/models"
)

// Request is one request the server received.
type Request struct {
	Method string
	Path   string
	Query  string
	Header http.Header
	Body   []byte
}

// Reply is the scripted answer to one POST /responses. A zero Status means
// 200 with Events as an SSE stream; any other Status answers with Body as a
// JSON error.
type Reply struct {
	Status int
	Body   string
	// Events are the data payloads of the SSE stream, in order. Each is sent
	// as "event: <type>\ndata: <payload>\n\n".
	Events []string
}

// Server is the fake backend.
type Server struct {
	mu        sync.Mutex
	token     string
	accountID string
	models    string
	replies   []Reply
	requests  []Request
	// unauthorized counts the requests rejected for their credential.
	unauthorized int
}

// New returns a server that accepts token as the bearer token and
// accountID as the chatgpt-account-id header.
func New(token, accountID string) *Server {
	return &Server{token: token, accountID: accountID, models: `{"models":[]}`}
}

// SetModels sets the GET /models response body.
func (s *Server) SetModels(body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.models = body
}

// Enqueue appends replies to the POST /responses script.
func (s *Server) Enqueue(replies ...Reply) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.replies = append(s.replies, replies...)
}

// Requests returns a copy of the requests received so far.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

// Unauthorized returns how many requests were rejected for their
// credential.
func (s *Server) Unauthorized() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.unauthorized
}

// ServeHTTP records the request and answers it.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.requests = append(s.requests, Request{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Header: r.Header.Clone(), Body: body})
	authorized := r.Header.Get("Authorization") == "Bearer "+s.token && r.Header.Get("Chatgpt-Account-Id") == s.accountID
	if !authorized {
		s.unauthorized++
	}
	s.mu.Unlock()
	if !authorized {
		writeJSON(w, http.StatusUnauthorized, `{"error":{"code":"token_expired","message":"Provided authentication token is expired. Please try signing in again."}}`)
		return
	}
	switch {
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, modelsPath):
		s.mu.Lock()
		models := s.models
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, models)
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, responsesPath):
		s.reply(w)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) reply(w http.ResponseWriter) {
	s.mu.Lock()
	if len(s.replies) == 0 {
		s.mu.Unlock()
		writeJSON(w, http.StatusInternalServerError, `{"error":{"code":"fake_script_exhausted"}}`)
		return
	}
	next := s.replies[0]
	s.replies = s.replies[1:]
	s.mu.Unlock()
	if next.Status != 0 && next.Status != http.StatusOK {
		writeJSON(w, next.Status, next.Body)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	for _, event := range next.Events {
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType(event), event); err != nil {
			return
		}
	}
}

func eventType(payload string) string {
	var head struct {
		Type string `json:"type"`
	}
	if json.Unmarshal([]byte(payload), &head) != nil {
		return "message"
	}
	return head.Type
}

func writeJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if _, err := io.WriteString(w, body); err != nil {
		return
	}
}

// TextReply is a stream that answers with text in two deltas and then the
// completed message item and usage. Its event shapes are the ones Codex
// parses (codex-rs/codex-api/src/sse/responses.rs).
func TextReply(text string) Reply {
	half := len(text) / 2
	return Reply{Events: []string{
		`{"type":"response.created","response":{"id":"resp_fake"}}`,
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_fake","role":"assistant","content":[]}}`,
		textDelta(text[:half]),
		textDelta(text[half:]),
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"msg_fake","role":"assistant","content":[{"type":"output_text","text":` + quote(text) + `}]}}`,
		`{"type":"response.completed","response":{"id":"resp_fake","usage":{"input_tokens":12,"output_tokens":5,"total_tokens":17,"output_tokens_details":{"reasoning_tokens":0}}}}`,
	}}
}

// ToolCallReply is a stream in which the model calls one function.
func ToolCallReply(callID, name, arguments string) Reply {
	return Reply{Events: []string{
		`{"type":"response.created","response":{"id":"resp_fake_tool"}}`,
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_fake","call_id":` + quote(callID) + `,"name":` + quote(name) + `,"arguments":""}}`,
		`{"type":"response.function_call_arguments.delta","output_index":0,"item_id":"fc_fake","delta":` + quote(arguments) + `}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"fc_fake","call_id":` + quote(callID) + `,"name":` + quote(name) + `,"arguments":` + quote(arguments) + `}}`,
		`{"type":"response.completed","response":{"id":"resp_fake_tool","usage":{"input_tokens":20,"output_tokens":8,"total_tokens":28}}}`,
	}}
}

func textDelta(delta string) string {
	return `{"type":"response.output_text.delta","output_index":0,"item_id":"msg_fake","content_index":0,"delta":` + quote(delta) + `}`
}

func quote(value string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return `""`
	}
	return string(encoded)
}
