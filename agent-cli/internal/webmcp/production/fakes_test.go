package production

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/discovery"
)

const (
	opOpen        = "open"
	opHandleClose = "handle_close"
	opActivate    = "activate"
	opAttach      = "attach"
	opOpenTab     = "open_tab"
	opSessionEnd  = "session_close"
	fixtureOrigin = "https://fixture.test"
)

// fakeRuntime is a raw browser runtime that records every raw operation so
// tests can assert which transport calls the composition made.
type fakeRuntime struct {
	mu           sync.Mutex
	targets      []webmcp.Target
	openedTarget webmcp.Target
	openedURL    string
	tool         webmcp.ToolDescriptor
	operations   []string
}

var (
	_ webmcp.BrowserRuntime   = (*fakeRuntime)(nil)
	_ webmcp.BrowserHandle    = (*fakeHandle)(nil)
	_ webmcp.BrowserTabOpener = (*fakeHandle)(nil)
	_ webmcp.TargetSession    = (*fakeSession)(nil)
)

func (r *fakeRuntime) Open(ctx context.Context, candidate webmcp.BrowserCandidate) (webmcp.BrowserHandle, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.record(opOpen)
	return &fakeHandle{runtime: r, candidate: candidate}, nil
}

func (r *fakeRuntime) record(operation string) {
	r.mu.Lock()
	r.operations = append(r.operations, operation)
	r.mu.Unlock()
}

func (r *fakeRuntime) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.operations...)
}

func (r *fakeRuntime) count(want string) int {
	count := 0
	for _, operation := range r.snapshot() {
		if operation == want {
			count++
		}
	}
	return count
}

type fakeHandle struct {
	runtime   *fakeRuntime
	candidate webmcp.BrowserCandidate
	mu        sync.Mutex
	closed    bool
}

func (h *fakeHandle) Candidate() webmcp.BrowserCandidate { return h.candidate }

func (h *fakeHandle) ListTargets(ctx context.Context) ([]webmcp.Target, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	h.runtime.mu.Lock()
	targets := append([]webmcp.Target(nil), h.runtime.targets...)
	h.runtime.mu.Unlock()
	return targets, nil
}

func (h *fakeHandle) Activate(ctx context.Context, _ webmcp.TargetID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	h.runtime.record(opActivate)
	return nil
}

func (h *fakeHandle) OpenTab(ctx context.Context, rawURL string) (webmcp.Target, error) {
	if err := ctx.Err(); err != nil {
		return webmcp.Target{}, err
	}
	h.runtime.mu.Lock()
	h.runtime.openedURL = rawURL
	opened := h.runtime.openedTarget
	h.runtime.mu.Unlock()
	h.runtime.record(opOpenTab)
	return opened, nil
}

func (h *fakeHandle) Attach(ctx context.Context, _ webmcp.TargetID, ownership webmcp.TargetOwnership) (webmcp.TargetSession, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	h.runtime.record(opAttach)
	h.runtime.mu.Lock()
	target := h.runtime.targets[0]
	tool := h.runtime.tool
	h.runtime.mu.Unlock()
	page := webmcp.PageContext{
		Key:        webmcp.PageKey{BrowserID: h.candidate.ID, TargetID: target.ID},
		Title:      target.Title,
		URL:        target.URL,
		Origin:     target.Origin,
		Generation: target.Generation,
		Connected:  true,
	}
	session := &fakeSession{runtime: h.runtime, page: page, ownership: ownership, tool: tool}
	session.init()
	return session, nil
}

func (h *fakeHandle) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.closed {
		h.closed = true
		h.runtime.record(opHandleClose)
	}
	return nil
}

type fakeSession struct {
	runtime   *fakeRuntime
	page      webmcp.PageContext
	ownership webmcp.TargetOwnership
	tool      webmcp.ToolDescriptor
	enableErr error
	events    chan webmcp.BrowserEvent
	done      chan struct{}
	once      sync.Once
	mu        sync.Mutex
	closed    bool
}

func (s *fakeSession) init() {
	s.once.Do(func() {
		s.events = make(chan webmcp.BrowserEvent, 4)
		s.done = make(chan struct{})
	})
}

func (s *fakeSession) Context() webmcp.PageContext {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.page
}

func (s *fakeSession) Ownership() webmcp.TargetOwnership { return s.ownership }

func (s *fakeSession) EnableWebMCP(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.enableErr != nil {
		return s.enableErr
	}
	tool := s.tool
	tool.BrowserID = s.page.Key.BrowserID
	tool.TargetID = s.page.Key.TargetID
	tool.Generation = s.page.Generation
	s.events <- webmcp.BrowserEvent{Type: webmcp.EventToolsAdded, Tools: []webmcp.ToolDescriptor{tool}, Generation: s.page.Generation}
	return nil
}

func (s *fakeSession) Events() <-chan webmcp.BrowserEvent { return s.events }

func (s *fakeSession) InvokeWebMCP(context.Context, webmcp.FrameID, string, json.RawMessage) (webmcp.InvocationID, error) {
	return "inv-production-test", nil
}

func (s *fakeSession) CancelWebMCP(context.Context, webmcp.InvocationID) error { return nil }

func (s *fakeSession) Done() <-chan struct{} { return s.done }

func (s *fakeSession) Err() error { return nil }

func (s *fakeSession) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	close(s.events)
	close(s.done)
	s.runtime.record(opSessionEnd)
	return nil
}

// fixtureEndpoint is a loopback /json/version server plus the normalized IDs
// discovery derives for its one raw page target.
type fixtureEndpoint struct {
	server    *httptest.Server
	browserID string
	targetID  string
	runtime   *fakeRuntime
}

func newFixtureEndpoint(t *testing.T) fixtureEndpoint {
	t.Helper()
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/json/version" {
			http.NotFound(writer, request)
			return
		}
		browserWebSocket := "ws" + strings.TrimPrefix(server.URL, "http") + "/devtools/browser/browser-token"
		writer.Header().Set("Content-Type", "application/json")
		if _, err := fmt.Fprintf(writer, `{"Browser":"Chrome/Test","Protocol-Version":"1.3","webSocketDebuggerUrl":%q}`, browserWebSocket); err != nil {
			t.Errorf("write version fixture: %v", err)
		}
	}))
	t.Cleanup(server.Close)
	parsed, err := url.Parse("ws" + strings.TrimPrefix(server.URL, "http") + "/devtools/browser/browser-token")
	if err != nil {
		t.Fatalf("parse browser websocket: %v", err)
	}
	browserID := discovery.HashIDMapper{}.BrowserID(discovery.BrowserIdentity{
		Scheme: parsed.Scheme,
		Host:   parsed.Hostname(),
		Port:   parsed.Port(),
		Path:   parsed.EscapedPath(),
	})
	rawTargetID := "raw-tab"
	target := webmcp.Target{
		ID:               webmcp.TargetID(rawTargetID),
		Type:             "page",
		Title:            "Fixture",
		URL:              fixtureOrigin + "/page?secret=query#fragment",
		Origin:           fixtureOrigin,
		WebSocketURL:     "ws" + strings.TrimPrefix(server.URL, "http") + "/devtools/page/raw-tab",
		ContinuityMarker: "document-a",
	}
	tool := webmcp.ToolDescriptor{
		Name:        "read_state",
		Description: "Read fixture state",
		FrameID:     "frame-1",
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`),
		Origin:      fixtureOrigin,
	}
	return fixtureEndpoint{
		server:    server,
		browserID: browserID,
		targetID:  discovery.HashTargetIDMapper{}.TargetID(discovery.TargetIdentity{BrowserID: browserID, RawID: rawTargetID}),
		runtime:   &fakeRuntime{targets: []webmcp.Target{target}, tool: tool},
	}
}
