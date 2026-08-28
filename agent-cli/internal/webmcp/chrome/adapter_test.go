package chrome

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResolveBrowserWebSocketResolvesRootWebSocketEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/json/version" {
			t.Fatalf("request path = %q, want /json/version", request.URL.Path)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"webSocketDebuggerUrl":"ws://browser.example/devtools/browser/pinned"}`))
	}))
	defer server.Close()

	runtime := NewRuntime(WithHTTPClient(server.Client()))
	rootWebSocket := strings.Replace(server.URL, "http://", "ws://", 1) + "/"
	resolved, err := runtime.resolveBrowserWebSocket(context.Background(), rootWebSocket)
	if err != nil {
		t.Fatalf("resolve root websocket endpoint: %v", err)
	}
	if resolved != "ws://browser.example/devtools/browser/pinned" {
		t.Fatalf("resolved websocket = %q, want pinned browser websocket", resolved)
	}
}

func TestResolveBrowserWebSocketPreservesFullBrowserWebSocketEndpoint(t *testing.T) {
	runtime := NewRuntime()
	endpoint := "ws://browser.example/devtools/browser/pinned"
	resolved, err := runtime.resolveBrowserWebSocket(context.Background(), endpoint)
	if err != nil {
		t.Fatalf("resolve full browser websocket endpoint: %v", err)
	}
	if resolved != endpoint {
		t.Fatalf("resolved websocket = %q, want %q", resolved, endpoint)
	}
}
