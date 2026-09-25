package events

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/transport/cli/internal/roomhost"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
)

const testSecret = "sk-events-secret-42"

func newTestStream(t *testing.T, participants ...string) rooms.RoomEventStream {
	t.Helper()
	manifest := rooms.Manifest{}
	for index, id := range participants {
		participant := rooms.Participant{ID: id}
		if index == 0 {
			participant.APIKeyEnv = "EVENTS_TEST_KEY"
		}
		manifest.Participants = append(manifest.Participants, participant)
	}
	t.Setenv("EVENTS_TEST_KEY", testSecret)
	plan := rooms.RoomRunPlan{Manifest: manifest, ReplayPlan: &rooms.RoomReplayPlan{}}
	stream, err := roomhost.EventStream(plan, roomhost.SecretRedactor(plan, t.TempDir()))
	if err != nil {
		t.Fatalf("open room stream: %v", err)
	}
	t.Cleanup(func() {
		if err := stream.Close(); err != nil {
			t.Errorf("stream.Close(): %v", err)
		}
	})
	return stream
}

type sseReader struct{ scanner *bufio.Scanner }

func (r *sseReader) next(t *testing.T) map[string]string {
	t.Helper()
	for r.scanner.Scan() {
		line := r.scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &payload); err != nil {
			t.Fatalf("decode SSE payload %q: %v", line, err)
		}
		result := map[string]string{"raw": line}
		for key, value := range payload {
			if text, ok := value.(string); ok {
				result[key] = text
			}
		}
		return result
	}
	t.Fatalf("SSE stream ended before the next event: %v", r.scanner.Err())
	return nil
}

func openSSE(t *testing.T, server *httptest.Server, path string) (http.Header, *sseReader) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+path, nil)
	if err != nil {
		t.Fatalf("create SSE request: %v", err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	t.Cleanup(func() { closeResource(t, response.Body) })
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET %s status = %d, want %d", path, response.StatusCode, http.StatusOK)
	}
	return response.Header, &sseReader{scanner: bufio.NewScanner(response.Body)}
}

// closeResource closes a test-owned body; a body the server already ended
// may report net.ErrClosed, which is not a failure.
func closeResource(t *testing.T, resource io.Closer) {
	t.Helper()
	if err := resource.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		t.Errorf("close test resource: %v", err)
	}
}

func TestHandlerServesRedactedFramesWithSSEContractHeaders(t *testing.T) {
	stream := newTestStream(t, "alice", "bob")
	server := httptest.NewServer(NewHandler(stream))
	defer server.Close()
	header, all := openSSE(t, server, Path)
	_, bob := openSSE(t, server, Path+"?participant=bob")
	if got := header.Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") || header.Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("SSE headers = %v", header)
	}

	if err := stream.Publish(context.Background(), "alice", session.LiveEvent{Kind: "browser.failed", Reason: "auth " + testSecret + " rejected"}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	stream.PublishRoomEvent(rooms.RoomStreamEventParticipantTerminated, "bob", "ended")

	first := all.next(t)
	if first["type"] != rooms.RoomStreamTypeDiagnostic || first["participant_id"] != "alice" || strings.Contains(first["raw"], testSecret) || !strings.Contains(first["raw"], "auth [REDACTED] rejected") {
		t.Fatalf("participant event = %v, want the credential redacted", first)
	}
	for name, reader := range map[string]*sseReader{"unfiltered": all, "bob-filtered": bob} {
		if event := reader.next(t); event["event"] != rooms.RoomStreamEventParticipantTerminated || event["participant_id"] != "bob" || event["reason"] != "ended" {
			t.Fatalf("%s lifecycle event = %v", name, event)
		}
	}
}

func TestHandlerRejectsInvalidRequestsBeforeStreaming(t *testing.T) {
	stream := newTestStream(t, "a", "b")
	server := httptest.NewServer(NewHandler(stream))
	defer server.Close()
	cases := []struct {
		method, path string
		status       int
		body         string
	}{
		{http.MethodGet, "/events?participant=missing", http.StatusBadRequest, `unknown room stream participant: "missing"`},
		{http.MethodPost, "/events", http.StatusMethodNotAllowed, "requires GET /events"},
		{http.MethodGet, "/other", http.StatusNotFound, ""},
	}
	for _, tc := range cases {
		if status, body := fetch(t, tc.method, server.URL+tc.path); status != tc.status || !strings.Contains(body, tc.body) {
			t.Fatalf("%s %s = %d %q, want %d %q", tc.method, tc.path, status, body, tc.status, tc.body)
		}
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if status, _ := fetch(t, http.MethodGet, server.URL+"/events"); status != http.StatusGone {
		t.Fatalf("closed stream status = %d, want %d", status, http.StatusGone)
	}
}

// fetch performs one complete request and returns its status and body.
func fetch(t *testing.T, method, url string) (int, string) {
	t.Helper()
	request, err := http.NewRequestWithContext(context.Background(), method, url, nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer func() {
		if err := response.Body.Close(); err != nil {
			t.Errorf("close %s %s: %v", method, url, err)
		}
	}()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read %s %s: %v", method, url, err)
	}
	return response.StatusCode, string(body)
}

type blockingResponseWriter struct {
	header       http.Header
	ready        chan struct{}
	writeStarted chan struct{}
	unblock      chan struct{}
	readyOnce    sync.Once
	writeOnce    sync.Once
}

func (w *blockingResponseWriter) Header() http.Header { return w.header }
func (w *blockingResponseWriter) WriteHeader(int)     {}
func (w *blockingResponseWriter) Write(payload []byte) (int, error) {
	w.writeOnce.Do(func() { w.writeStarted <- struct{}{} })
	<-w.unblock
	return len(payload), nil
}
func (w *blockingResponseWriter) Flush() { w.readyOnce.Do(func() { close(w.ready) }) }

func awaitSignal(t *testing.T, signal <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatal(what)
	}
}

func TestHandlerSlowClientNeverBlocksRoomPublishers(t *testing.T) {
	stream := newTestStream(t, "a")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request := httptest.NewRequest(http.MethodGet, "http://room.test/events", nil).WithContext(ctx)
	writer := &blockingResponseWriter{header: make(http.Header), ready: make(chan struct{}), writeStarted: make(chan struct{}, 1), unblock: make(chan struct{})}
	done := make(chan struct{})
	go func() { NewHandler(stream).ServeHTTP(writer, request); close(done) }()
	awaitSignal(t, writer.ready, "slow client handler did not register")
	stream.PublishRoomEvent("first", "a", "")
	awaitSignal(t, writer.writeStarted, "slow client did not begin its blocked write")
	published := make(chan struct{})
	go func() {
		for index := 0; index < 256; index++ {
			stream.PublishRoomEvent("overflow", "a", "")
		}
		close(published)
	}()
	awaitSignal(t, published, "publishing to a slow client blocked the room")
	close(writer.unblock)
	awaitSignal(t, done, "dropped slow client handler did not settle")
}

func TestServerServesEventsAndShutsDown(t *testing.T) {
	stream := newTestStream(t, "alice")
	server, err := Start("127.0.0.1:0", stream)
	if err != nil {
		t.Fatalf("start room event server: %v", err)
	}
	response, err := http.Get(server.URL() + "?participant=alice")
	if err != nil {
		closeResource(t, shutdownCloser{server})
		t.Fatalf("connect event stream: %v", err)
	}
	defer closeResource(t, response.Body)
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "text/event-stream" {
		closeResource(t, shutdownCloser{server})
		t.Fatalf("event response = %d %q", response.StatusCode, response.Header.Get("Content-Type"))
	}
	stream.PublishRoomEvent(rooms.RoomStreamEventParticipantJoined, "alice", "")
	if err := server.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown event server: %v", err)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read event stream: %v", err)
	}
	if !strings.HasPrefix(string(body), "data: {") || !strings.Contains(string(body), `"event":"participant_joined"`) || !strings.HasSuffix(string(body), "}\n\n") {
		t.Fatalf("event stream body = %q", body)
	}
}

type shutdownCloser struct{ server *Server }

func (c shutdownCloser) Close() error { return c.server.Shutdown(context.Background()) }

func TestStartRejectsMissingAddressOrStream(t *testing.T) {
	if _, err := Start(" ", newTestStream(t, "a")); err == nil {
		t.Fatal("empty address accepted")
	}
	if _, err := Start("127.0.0.1:0", nil); err == nil {
		t.Fatal("nil stream accepted")
	}
}
