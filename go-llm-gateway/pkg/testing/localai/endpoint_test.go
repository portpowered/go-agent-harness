package localai

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestResolveEndpoint(t *testing.T) {
	t.Setenv(realtimeEndpointEnv, "")
	if got := resolveEndpoint(); got != DefaultEndpoint {
		t.Fatalf("default endpoint = %q, want %q", got, DefaultEndpoint)
	}

	const override = "ws://example.invalid/custom/realtime?model=stub&voice=amy"
	t.Setenv(realtimeEndpointEnv, override)
	if got := resolveEndpoint(); got != override {
		t.Fatalf("override endpoint = %q, want exact %q", got, override)
	}
}

func TestEndpointHonorsWholeURLOverrideAndProbesSessionCreated(t *testing.T) {
	queries := make(chan string, 1)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		queries <- request.URL.RawQuery
		conn, err := upgrader.Upgrade(writer, request, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.WriteJSON(map[string]string{"type": "session.created"})
	}))
	defer server.Close()

	endpoint := strings.Replace(server.URL, "http://", "ws://", 1) + "/v1/realtime?model=stub&voice=amy"
	t.Setenv(realtimeEndpointEnv, endpoint)

	got, ok := Endpoint(t)
	if !ok {
		t.Fatalf("Endpoint(%q) returned ok=false", endpoint)
	}
	if got != endpoint {
		t.Fatalf("Endpoint returned %q, want exact override %q", got, endpoint)
	}
	select {
	case query := <-queries:
		if query != "model=stub&voice=amy" {
			t.Fatalf("server saw query %q, want exact query string", query)
		}
	case <-time.After(time.Second):
		t.Fatal("WebSocket stub did not receive a request")
	}
}

func TestEndpointCachesClosedEndpoint(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for ephemeral address: %v", err)
	}
	endpoint := "ws://" + listener.Addr().String() + "/v1/realtime?model=closed"
	if err := listener.Close(); err != nil {
		t.Fatalf("close ephemeral listener: %v", err)
	}
	t.Setenv(realtimeEndpointEnv, endpoint)

	started := time.Now()
	firstURL, firstOK := Endpoint(t)
	firstDuration := time.Since(started)
	if firstOK || firstURL != endpoint {
		t.Fatalf("first Endpoint result = (%q, %t), want (%q, false)", firstURL, firstOK, endpoint)
	}
	if firstDuration > probeTimeout+time.Second {
		t.Fatalf("first closed-port lookup took %s, want no more than %s", firstDuration, probeTimeout+time.Second)
	}

	started = time.Now()
	secondURL, secondOK := Endpoint(t)
	secondDuration := time.Since(started)
	if secondOK || secondURL != endpoint {
		t.Fatalf("cached Endpoint result = (%q, %t), want (%q, false)", secondURL, secondOK, endpoint)
	}
	if secondDuration >= time.Second {
		t.Fatalf("cached lookup took %s, want under one second", secondDuration)
	}
	t.Logf("closed-port lookup: first=%s, cached=%s", firstDuration, secondDuration)
}

func TestEndpointDoesNotProbeAFailedEndpointAgain(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for ephemeral address: %v", err)
	}
	defer listener.Close()

	endpoint := "ws://" + listener.Addr().String() + "/v1/realtime?model=protocol-failure"
	var accepted atomic.Int32
	acceptedOnce := make(chan struct{})
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		accepted.Add(1)
		close(acceptedOnce)
		_ = conn.Close()
	}()
	t.Setenv(realtimeEndpointEnv, endpoint)

	if _, ok := Endpoint(t); ok {
		t.Fatal("non-speaking endpoint unexpectedly reported ready")
	}
	select {
	case <-acceptedOnce:
	case <-time.After(time.Second):
		t.Fatal("probe did not reach the ephemeral listener")
	}
	if err := listener.Close(); err != nil {
		t.Fatalf("close failed endpoint listener: %v", err)
	}
	<-serverDone

	started := time.Now()
	if _, ok := Endpoint(t); ok {
		t.Fatal("cached failed endpoint unexpectedly reported ready")
	}
	if elapsed := time.Since(started); elapsed >= time.Second {
		t.Fatalf("cached failed endpoint took %s, want under one second", elapsed)
	}
	if got := accepted.Load(); got != 1 {
		t.Fatalf("failed endpoint was contacted %d times, want once", got)
	}
}
