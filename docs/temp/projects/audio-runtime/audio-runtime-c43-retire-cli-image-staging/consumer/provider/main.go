package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	imageMode   = "image"
	timeoutMode = "timeout"
	pathMarker  = "Session-staged image path(s) (use one of these exact absolute paths):\n- "
)

type config struct {
	mode          string
	resultPath    string
	logPath       string
	fixtureSHA256 string
	fixtureBytes  int
	fixtureBase64 string
}

type sessionState struct {
	config config

	logMu sync.Mutex
	log   *os.File

	initialPath     string
	refreshedPath   string
	initialTools    []string
	refreshedTools  []string
	responseCreates int
	functionCall    bool
	toolResult      bool
	dataURL         bool
	continuation    bool
	outputSHA256    string
	failure         string
	clientClosed    bool

	resultMu sync.Mutex
	result   map[string]any
}

func main() {
	cfg := config{}
	flag.StringVar(&cfg.mode, "mode", imageMode, "provider mode: image or timeout")
	flag.StringVar(&cfg.resultPath, "result", "", "JSON result path")
	flag.StringVar(&cfg.logPath, "log", "", "JSONL event log path")
	flag.StringVar(&cfg.fixtureSHA256, "fixture-sha256", "", "expected image SHA-256")
	flag.IntVar(&cfg.fixtureBytes, "fixture-bytes", 0, "expected image byte length")
	flag.StringVar(&cfg.fixtureBase64, "fixture-base64", "", "expected image base64")
	flag.Parse()

	if cfg.mode != imageMode && cfg.mode != timeoutMode {
		fatal("unsupported mode %q", cfg.mode)
	}
	if strings.TrimSpace(cfg.resultPath) == "" || strings.TrimSpace(cfg.logPath) == "" {
		fatal("--result and --log are required")
	}
	logFile, err := os.OpenFile(cfg.logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		fatal("open event log: %v", err)
	}
	defer logFile.Close()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fatal("listen: %v", err)
	}
	defer listener.Close()

	state := &sessionState{config: cfg, log: logFile, result: map[string]any{
		"mode": cfg.mode,
	}}
	var connectionOnce sync.Once
	served := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(writer http.ResponseWriter, request *http.Request) {
		accepted := false
		connectionOnce.Do(func() {
			accepted = true
			state.handle(writer, request)
			close(served)
		})
		if !accepted {
			writer.WriteHeader(http.StatusConflict)
		}
	})
	server := &http.Server{Handler: mux}
	go func() {
		if serveErr := server.Serve(listener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			state.fail("HTTP server: %v", serveErr)
		}
	}()

	ready, _ := json.Marshal(map[string]any{
		"ready": true,
		"url":   "ws://" + listener.Addr().String() + "/realtime",
	})
	fmt.Println(string(ready))

	select {
	case <-served:
	case <-time.After(55 * time.Second):
		state.fail("provider waited 55s without a client")
	}
	shutdownContext, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	_ = server.Shutdown(shutdownContext)
	cancel()

	state.finalize()
	if err := writeJSONFile(cfg.resultPath, state.resultSnapshot()); err != nil {
		fatal("write result: %v", err)
	}
	if status, _ := state.resultSnapshot()["status"].(string); status != "PASS" {
		fmt.Fprintln(os.Stderr, state.resultSnapshot()["error"])
		os.Exit(1)
	}
}

func (s *sessionState) handle(writer http.ResponseWriter, request *http.Request) {
	if request.Header.Get("Authorization") != "Bearer c43-hermetic-key" {
		s.fail("unexpected provider authorization header")
		http.Error(writer, "unauthorized", http.StatusUnauthorized)
		return
	}
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	connection, err := upgrader.Upgrade(writer, request, nil)
	if err != nil {
		s.fail("upgrade WebSocket: %v", err)
		return
	}
	defer connection.Close()

	if err := s.run(connection); err != nil {
		s.fail("session: %v", err)
	}
}

func (s *sessionState) run(connection *websocket.Conn) error {
	for {
		if err := connection.SetReadDeadline(time.Now().Add(15 * time.Second)); err != nil {
			return fmt.Errorf("set read deadline: %w", err)
		}
		_, payload, err := connection.ReadMessage()
		if err != nil {
			if s.config.mode == timeoutMode && (websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) || errors.Is(err, io.EOF) || errors.Is(err, os.ErrClosed)) {
				s.clientClosed = true
				return nil
			}
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				s.clientClosed = true
				if s.config.mode == imageMode && s.continuation {
					return nil
				}
			}
			return fmt.Errorf("read client event: %w", err)
		}
		s.logEvent("client_to_server", payload)

		var event map[string]any
		if err := json.Unmarshal(payload, &event); err != nil {
			return fmt.Errorf("decode client event: %w", err)
		}
		eventType, _ := event["type"].(string)
		switch eventType {
		case "session.update":
			if err := s.observeSessionUpdate(event); err != nil {
				return err
			}
			if !s.hasSessionCreated() {
				if err := s.send(connection, map[string]any{
					"type":    "session.created",
					"session": map[string]any{"id": "c43-process-session", "model": "gpt-realtime"},
				}); err != nil {
					return err
				}
				s.setSessionCreated()
			}
			if err := s.maybeContinue(connection); err != nil {
				return err
			}
		case "response.create":
			s.responseCreates++
			if s.config.mode == imageMode && !s.functionCall {
				if err := s.sendFunctionCall(connection); err != nil {
					return err
				}
			}
			if err := s.maybeContinue(connection); err != nil {
				return err
			}
		case "conversation.item.create":
			if err := s.observeConversationItem(event); err != nil {
				return err
			}
			if err := s.maybeContinue(connection); err != nil {
				return err
			}
		}
	}
}

func (s *sessionState) observeSessionUpdate(event map[string]any) error {
	session, _ := event["session"].(map[string]any)
	tools, _ := session["tools"].([]any)
	names := make([]string, 0, len(tools))
	path := ""
	for _, rawTool := range tools {
		tool, _ := rawTool.(map[string]any)
		name, _ := tool["name"].(string)
		if name != "" {
			names = append(names, name)
		}
		if name != "read_image" {
			continue
		}
		parameters, _ := tool["parameters"].(map[string]any)
		properties, _ := parameters["properties"].(map[string]any)
		pathParameter, _ := properties["path"].(map[string]any)
		description, _ := pathParameter["description"].(string)
		if markerIndex := strings.Index(description, pathMarker); markerIndex >= 0 {
			remaining := description[markerIndex+len(pathMarker):]
			if lineEnd := strings.IndexByte(remaining, '\n'); lineEnd >= 0 {
				remaining = remaining[:lineEnd]
			}
			path = strings.TrimSpace(remaining)
		}
	}
	if path == "" {
		return errors.New("session.update did not advertise a staged read_image path")
	}
	if s.initialPath == "" {
		s.initialPath = path
		s.initialTools = append([]string(nil), names...)
		return nil
	}
	if !sameStrings(s.initialTools, names) {
		s.refreshedPath = path
		s.refreshedTools = append([]string(nil), names...)
	}
	return nil
}

func (s *sessionState) observeConversationItem(event map[string]any) error {
	item, _ := event["item"].(map[string]any)
	itemType, _ := item["type"].(string)
	switch itemType {
	case "function_call_output":
		output, _ := item["output"].(string)
		if err := s.validateImageEnvelope(output); err != nil {
			return err
		}
		s.toolResult = true
	case "message":
		content, _ := item["content"].([]any)
		for _, rawPart := range content {
			part, _ := rawPart.(map[string]any)
			partType, _ := part["type"].(string)
			if partType != "input_image" {
				continue
			}
			imageURL, _ := part["image_url"].(string)
			want := "data:image/png;base64," + s.config.fixtureBase64
			if imageURL != want {
				return fmt.Errorf("typed read_image projection did not preserve exact fixture bytes")
			}
			s.dataURL = true
		}
	}
	return nil
}

func (s *sessionState) validateImageEnvelope(output string) error {
	var envelope map[string]any
	if err := json.Unmarshal([]byte(output), &envelope); err != nil {
		return fmt.Errorf("read_image output is not JSON: %w", err)
	}
	if status, _ := envelope["status"].(string); status != "success" {
		return fmt.Errorf("read_image output status = %q, want success", status)
	}
	if byteLength, ok := envelope["byte_length"].(float64); !ok || int(byteLength) != s.config.fixtureBytes {
		return fmt.Errorf("read_image byte_length did not match fixture")
	}
	if digest, _ := envelope["sha256"].(string); digest != s.config.fixtureSHA256 {
		return fmt.Errorf("read_image SHA-256 = %q, want %q", digest, s.config.fixtureSHA256)
	}
	if projection, _ := envelope["typed_projection"].(string); projection != "input_image" {
		return fmt.Errorf("read_image typed_projection = %q, want input_image", projection)
	}
	s.outputSHA256 = s.config.fixtureSHA256
	return nil
}

func (s *sessionState) sendFunctionCall(connection *websocket.Conn) error {
	if s.initialPath == "" {
		return errors.New("cannot issue read_image call before staged path advertisement")
	}
	if err := s.send(connection, map[string]any{
		"type":     "response.created",
		"response": map[string]any{"id": "c43-image-response"},
	}); err != nil {
		return err
	}
	if err := s.send(connection, map[string]any{
		"type": "response.output_item.added",
		"item": map[string]any{"type": "function_call", "id": "c43-image-item", "call_id": "c43-read-image", "name": "read_image"},
	}); err != nil {
		return err
	}
	arguments, _ := json.Marshal(map[string]string{"path": s.initialPath})
	if err := s.send(connection, map[string]any{
		"type":      "response.function_call_arguments.done",
		"call_id":   "c43-read-image",
		"name":      "read_image",
		"arguments": string(arguments),
	}); err != nil {
		return err
	}
	if err := s.send(connection, map[string]any{
		"type":     "response.done",
		"response": map[string]any{"id": "c43-image-response", "status": "completed"},
	}); err != nil {
		return err
	}
	s.functionCall = true
	return nil
}

func (s *sessionState) maybeContinue(connection *websocket.Conn) error {
	if s.config.mode != imageMode || s.continuation || !s.toolResult || !s.dataURL || s.responseCreates < 2 || s.refreshedPath == "" {
		return nil
	}
	if err := s.send(connection, map[string]any{
		"type":     "response.created",
		"response": map[string]any{"id": "c43-image-continuation"},
	}); err != nil {
		return err
	}
	if err := s.send(connection, map[string]any{
		"type":  "response.output_text.delta",
		"delta": "c43 shipped image workflow verified",
	}); err != nil {
		return err
	}
	if err := s.send(connection, map[string]any{"type": "response.output_text.done"}); err != nil {
		return err
	}
	if err := s.send(connection, map[string]any{
		"type":     "response.done",
		"response": map[string]any{"id": "c43-image-continuation", "status": "completed"},
	}); err != nil {
		return err
	}
	if err := s.send(connection, map[string]any{
		"type":       "session.closed",
		"session_id": "c43-process-session",
		"reason":     "fixture_complete",
	}); err != nil {
		return err
	}
	s.continuation = true
	s.result["status"] = "PASS"
	s.result["initial_path"] = s.initialPath
	s.result["refreshed_path"] = s.refreshedPath
	s.result["initial_tools"] = s.initialTools
	s.result["refreshed_tools"] = s.refreshedTools
	s.result["output_sha256"] = s.outputSHA256
	s.result["typed_projection_exact"] = s.dataURL
	closePayload := websocket.FormatCloseMessage(websocket.CloseNormalClosure, "fixture_complete")
	return connection.WriteControl(websocket.CloseMessage, closePayload, time.Now().Add(time.Second))
}

func (s *sessionState) send(connection *websocket.Conn, value map[string]any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode server event: %w", err)
	}
	if err := connection.WriteMessage(websocket.TextMessage, payload); err != nil {
		return fmt.Errorf("write server event: %w", err)
	}
	s.logEvent("server_to_client", payload)
	return nil
}

func (s *sessionState) logEvent(direction string, payload []byte) {
	var event map[string]any
	_ = json.Unmarshal(payload, &event)
	entry := map[string]any{
		"direction": direction,
		"type":      event["type"],
		"payload":   json.RawMessage(append([]byte(nil), payload...)),
	}
	encoded, _ := json.Marshal(entry)
	s.logMu.Lock()
	defer s.logMu.Unlock()
	_, _ = s.log.Write(append(encoded, '\n'))
}

func (s *sessionState) fail(format string, args ...any) {
	if s.failure == "" {
		s.failure = fmt.Sprintf(format, args...)
	}
}

func (s *sessionState) finalize() {
	s.resultMu.Lock()
	defer s.resultMu.Unlock()
	if s.result["status"] == "PASS" {
		return
	}
	if s.config.mode == timeoutMode && s.clientClosed {
		s.result["status"] = "PASS"
		s.result["expected_timeout_client_close"] = true
		return
	}
	s.result["status"] = "FAIL"
	if s.failure != "" {
		s.result["error"] = s.failure
	} else {
		s.result["error"] = "provider session ended before the required workflow completed"
	}
}

func (s *sessionState) resultSnapshot() map[string]any {
	s.resultMu.Lock()
	defer s.resultMu.Unlock()
	copy := make(map[string]any, len(s.result))
	for key, value := range s.result {
		copy[key] = value
	}
	return copy
}

func (s *sessionState) hasSessionCreated() bool {
	_, ok := s.result["session_created"]
	return ok
}

func (s *sessionState) setSessionCreated() {
	s.result["session_created"] = true
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func writeJSONFile(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(2)
}
