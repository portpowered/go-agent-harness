package main

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/target"
	cdpWebMCP "github.com/chromedp/cdproto/webmcp"
	"github.com/chromedp/chromedp"
)

const (
	crossProcessInitialToolName = "webmcp_watch_probe_tool"
	crossProcessDynamicToolName = "webmcp_watch_dynamic_tool"
)

//go:embed watch-cross-process-fixture.html
var crossProcessFixtureHTML embed.FS

type crossProcessPageState struct {
	Ready       bool     `json:"ready"`
	Value       string   `json:"value"`
	VisibleText string   `json:"visibleText"`
	Invocations []string `json:"invocations"`
}

type crossProcessFixture struct {
	server *httptest.Server

	mu    sync.Mutex
	state crossProcessPageState
}

func newCrossProcessFixture() (*crossProcessFixture, error) {
	html, err := crossProcessFixtureHTML.ReadFile("watch-cross-process-fixture.html")
	if err != nil {
		return nil, fmt.Errorf("read cross-process fixture: %w", err)
	}
	fixture := &crossProcessFixture{}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/" {
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = writer.Write(html)
	})
	mux.HandleFunc("/__watch/state", fixture.handleState)
	fixture.server = httptest.NewServer(mux)
	return fixture, nil
}

func (f *crossProcessFixture) URL() string {
	if f == nil || f.server == nil {
		return ""
	}
	return f.server.URL + "/"
}

func (f *crossProcessFixture) StateURL() string {
	if f == nil || f.server == nil {
		return ""
	}
	return f.server.URL + "/__watch/state"
}

func (f *crossProcessFixture) handleState(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/__watch/state" {
		http.NotFound(writer, request)
		return
	}
	switch request.Method {
	case http.MethodGet:
		f.mu.Lock()
		state := cloneCrossProcessPageState(f.state)
		f.mu.Unlock()
		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(state)
	case http.MethodPost:
		var state crossProcessPageState
		decoder := json.NewDecoder(io.LimitReader(request.Body, 64<<10))
		if err := decoder.Decode(&state); err != nil {
			http.Error(writer, "invalid state", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.state = cloneCrossProcessPageState(state)
		f.mu.Unlock()
		writer.WriteHeader(http.StatusNoContent)
	default:
		writer.Header().Set("Allow", "GET, POST")
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (f *crossProcessFixture) Close() {
	if f != nil && f.server != nil {
		f.server.Close()
	}
}

func cloneCrossProcessPageState(state crossProcessPageState) crossProcessPageState {
	if state.Invocations != nil {
		state.Invocations = append([]string{}, state.Invocations...)
	}
	return state
}

func crossProcessPageStateExpression() string {
	return `(() => {
  const state = window.__webmcpWatchCrossProcess;
  const visible = document.querySelector("#state");
  return {
    ready: Boolean(state && state.ready),
    value: state && state.value !== undefined ? String(state.value) : "",
    visibleText: visible ? String(visible.textContent || "") : "",
    invocations: state && Array.isArray(state.invocations)
      ? state.invocations.map((value) => String(value))
      : []
  };
})()`
}

func crossProcessAddToolExpression() string {
	return `(() => {
  const root = document.querySelector("#dynamic-tools");
  if (!root) throw new Error("dynamic tool root is unavailable");
  if (document.querySelector("#dynamic-form")) return "already-added";
  const form = document.createElement("form");
  form.id = "dynamic-form";
  form.setAttribute("toolname", "webmcp_watch_dynamic_tool");
  form.setAttribute("tooltitle", "Dynamic WebMCP watch tool");
  form.setAttribute("tooldescription", "A dynamically added DOM-declared tool.");
  form.setAttribute("toolautosubmit", "");
  const label = document.createElement("label");
  label.textContent = "Value";
  const input = document.createElement("input");
  input.name = "value";
  input.type = "text";
  input.setAttribute("toolparamdescription", "A dynamic probe value.");
  label.appendChild(input);
  form.appendChild(label);
  root.appendChild(form);
  return "added";
})()`
}

func crossProcessRemoveToolExpression() string {
	return `(() => {
  const form = document.querySelector("#dynamic-form");
  if (!form) return "already-removed";
  form.remove();
  return "removed";
})()`
}

func readCrossProcessPageState(ctx context.Context) (crossProcessPageState, error) {
	var state crossProcessPageState
	if err := chromedp.Run(ctx, chromedp.Evaluate(crossProcessPageStateExpression(), &state)); err != nil {
		return crossProcessPageState{}, fmt.Errorf("evaluate cross-process page state: %w", err)
	}
	return state, nil
}

type crossProcessCDPEvent struct {
	Type         string   `json:"type"`
	ToolNames    []string `json:"toolNames,omitempty"`
	InvocationID string   `json:"invocationID,omitempty"`
	Status       string   `json:"status,omitempty"`
}

type crossProcessCDPEventLog struct {
	events  chan crossProcessCDPEvent
	mu      sync.Mutex
	history []crossProcessCDPEvent
}

func newCrossProcessCDPEventLog() *crossProcessCDPEventLog {
	return &crossProcessCDPEventLog{events: make(chan crossProcessCDPEvent, 128)}
}

func (l *crossProcessCDPEventLog) observe(event any) {
	if l == nil {
		return
	}
	var observed crossProcessCDPEvent
	switch value := event.(type) {
	case *cdpWebMCP.EventToolsAdded:
		observed = crossProcessCDPEvent{Type: "toolsAdded", ToolNames: cdpToolNames(value.Tools)}
	case cdpWebMCP.EventToolsAdded:
		observed = crossProcessCDPEvent{Type: "toolsAdded", ToolNames: cdpToolNames(value.Tools)}
	case *cdpWebMCP.EventToolsRemoved:
		observed = crossProcessCDPEvent{Type: "toolsRemoved", ToolNames: cdpRemovedToolNames(value.Tools)}
	case cdpWebMCP.EventToolsRemoved:
		observed = crossProcessCDPEvent{Type: "toolsRemoved", ToolNames: cdpRemovedToolNames(value.Tools)}
	case *cdpWebMCP.EventToolInvoked:
		if value == nil {
			return
		}
		observed = crossProcessCDPEvent{Type: "toolInvoked", InvocationID: value.InvocationID}
	case cdpWebMCP.EventToolInvoked:
		observed = crossProcessCDPEvent{Type: "toolInvoked", InvocationID: value.InvocationID}
	case *cdpWebMCP.EventToolResponded:
		if value == nil {
			return
		}
		observed = crossProcessCDPEvent{Type: "toolResponded", InvocationID: value.InvocationID, Status: value.Status.String()}
	case cdpWebMCP.EventToolResponded:
		observed = crossProcessCDPEvent{Type: "toolResponded", InvocationID: value.InvocationID, Status: value.Status.String()}
	default:
		return
	}
	l.mu.Lock()
	l.history = append(l.history, observed)
	l.mu.Unlock()
	select {
	case l.events <- observed:
	default:
	}
}

func (l *crossProcessCDPEventLog) snapshot() []crossProcessCDPEvent {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]crossProcessCDPEvent(nil), l.history...)
}

func cdpToolNames(tools []*cdpWebMCP.Tool) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		if tool != nil {
			names = append(names, tool.Name)
		}
	}
	return names
}

func cdpRemovedToolNames(tools []*cdpWebMCP.RemovedTool) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		if tool != nil {
			names = append(names, tool.Name)
		}
	}
	return names
}

func waitForCrossProcessCDPEvent(ctx context.Context, events <-chan crossProcessCDPEvent, label string, match func(crossProcessCDPEvent) bool) (crossProcessCDPEvent, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		select {
		case event := <-events:
			if match == nil || match(event) {
				return event, nil
			}
		case <-ctx.Done():
			return crossProcessCDPEvent{}, fmt.Errorf("wait for %s: %w", label, ctx.Err())
		}
	}
}

type crossProcessTargetInfo struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	URL      string `json:"url"`
	Attached bool   `json:"attached"`
}

func browserHTTPEndpoint(browserWebSocket string) (string, error) {
	parsed, err := url.Parse(browserWebSocket)
	if err != nil || parsed.Host == "" || parsed.Scheme != "ws" {
		return "", fmt.Errorf("browser websocket endpoint is invalid: %q", browserWebSocket)
	}
	return "http://" + parsed.Host, nil
}

func readCrossProcessTarget(ctx context.Context, httpEndpoint, targetID string) (crossProcessTargetInfo, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, httpEndpoint+"/json/list", nil)
	if err != nil {
		return crossProcessTargetInfo{}, fmt.Errorf("create target-list request: %w", err)
	}
	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return crossProcessTargetInfo{}, fmt.Errorf("read target list: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return crossProcessTargetInfo{}, fmt.Errorf("target list status = %s", response.Status)
	}
	var targets []crossProcessTargetInfo
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&targets); err != nil {
		return crossProcessTargetInfo{}, fmt.Errorf("decode target list: %w", err)
	}
	for _, targetInfo := range targets {
		if targetInfo.ID == targetID {
			return targetInfo, nil
		}
	}
	return crossProcessTargetInfo{}, fmt.Errorf("target %s is not present", targetID)
}

func waitForCrossProcessTarget(ctx context.Context, httpEndpoint, targetID string, attached *bool) (crossProcessTargetInfo, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		info, err := readCrossProcessTarget(ctx, httpEndpoint, targetID)
		if err == nil && (attached == nil || info.Attached == *attached) {
			return info, nil
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			if err != nil {
				return crossProcessTargetInfo{}, fmt.Errorf("wait for target %s: %w: %v", targetID, ctx.Err(), err)
			}
			return crossProcessTargetInfo{}, fmt.Errorf("wait for target %s: %w", targetID, ctx.Err())
		}
	}
}

func sleepCrossProcess(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func targetIDFromContext(ctx context.Context) (string, error) {
	client := chromedp.FromContext(ctx)
	if client == nil || client.Target == nil || client.Target.TargetID == target.ID("") {
		return "", fmt.Errorf("CDP target context has no target ID")
	}
	return string(client.Target.TargetID), nil
}

func isLoopbackWatchFixtureURL(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme == "http" && parsed.Hostname() == "127.0.0.1" && parsed.Path == "/" && parsed.RawQuery == "" && parsed.Fragment == ""
}

func stateHasInvocation(state crossProcessPageState, value string) bool {
	for _, invocation := range state.Invocations {
		if invocation == value {
			return true
		}
	}
	return false
}

func stateMatchesInvocation(state crossProcessPageState, value string) bool {
	return state.Ready && state.Value == "invoked:"+value && state.VisibleText == "invoked:"+value && stateHasInvocation(state, value)
}

func waitForHTTPOracle(ctx context.Context, stateURL string, predicate func(crossProcessPageState) bool) (crossProcessPageState, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var last crossProcessPageState
	var lastErr error
	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, stateURL, nil)
		if err == nil {
			response, requestErr := (&http.Client{Timeout: 2 * time.Second}).Do(request)
			if requestErr == nil {
				if response.StatusCode == http.StatusOK {
					decodeErr := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&last)
					response.Body.Close()
					if decodeErr == nil && (predicate == nil || predicate(last)) {
						return cloneCrossProcessPageState(last), nil
					}
					lastErr = decodeErr
				} else {
					lastErr = fmt.Errorf("oracle status = %s", response.Status)
					response.Body.Close()
				}
			} else {
				lastErr = requestErr
			}
		} else {
			lastErr = err
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			if lastErr != nil {
				return crossProcessPageState{}, fmt.Errorf("wait for HTTP oracle: %w: %v (last state=%+v)", ctx.Err(), lastErr, last)
			}
			return crossProcessPageState{}, fmt.Errorf("wait for HTTP oracle: %w (last state=%+v)", ctx.Err(), last)
		}
	}
}

func trimWatchProcessOutput(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 2048 {
		return value[:2048] + "..."
	}
	return value
}
