package fakecodex_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/codexrtc"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/codexrtc/fakecodex"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/quicksilver"
)

const (
	callPath = "/backend-api/codex/realtime/calls"
	goodBody = `{"sdp":"v=offer\r\n","session":{"model":"gpt-live-1-codex","instructions":"","audio":{"output":{"voice":"cove"}},"delegation":{"type":"client"}}}`
)

func identity() http.Header {
	header := http.Header{}
	header.Set(codexrtc.HeaderAuthorization, "Bearer "+fakecodex.DefaultToken)
	header.Set(codexrtc.HeaderAccountID, fakecodex.DefaultAccountID)
	header.Set(codexrtc.HeaderAlpha, codexrtc.AlphaQuicksilverV2)
	header.Set(codexrtc.HeaderOriginator, "test")
	header.Set(codexrtc.HeaderSessionID, "s")
	header.Set(codexrtc.HeaderThreadID, "t")
	header.Set(codexrtc.HeaderXSessionID, "x")
	header.Set("Content-Type", "application/json")
	return header
}

func post(t *testing.T, server *httptest.Server, method, query string, header http.Header, body string) int {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), method, server.URL+callPath+query, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header = header
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	closeBody(t, response)
	return response.StatusCode
}

func closeBody(t *testing.T, response *http.Response) {
	t.Helper()
	if err := response.Body.Close(); err != nil {
		t.Errorf("close body: %v", err)
	}
}

// closeQuietly closes test resources whose peer may already be gone; only
// the failure is logged.
func closeQuietly(t *testing.T, closeFn func() error) {
	t.Helper()
	if err := closeFn(); err != nil {
		t.Logf("close: %v", err)
	}
}

func serve(t *testing.T, options ...fakecodex.Option) (*fakecodex.Backend, *httptest.Server) {
	t.Helper()
	backend := fakecodex.New(options...)
	server := httptest.NewServer(backend)
	t.Cleanup(server.Close)
	return backend, server
}

func TestCallCreationBreakingTheProtocolIsRejectedAndRecorded(t *testing.T) {
	backend, server := serve(t, fakecodex.WithAnswer("v=answer\r\n"))
	without := func(name string) http.Header {
		header := identity()
		header.Del(name)
		return header
	}
	repeated := identity()
	repeated.Set(codexrtc.HeaderThreadID, "s")
	cases := []struct {
		name, method, query, body string
		header                    http.Header
	}{
		{"method", http.MethodPut, "?" + codexrtc.CallQuery, goodBody, identity()},
		{"query", http.MethodPost, "", goodBody, identity()},
		{"content type", http.MethodPost, "?" + codexrtc.CallQuery, goodBody, without("Content-Type")},
		{"bearer", http.MethodPost, "?" + codexrtc.CallQuery, goodBody, without(codexrtc.HeaderAuthorization)},
		{"originator", http.MethodPost, "?" + codexrtc.CallQuery, goodBody, without(codexrtc.HeaderOriginator)},
		{"repeated id", http.MethodPost, "?" + codexrtc.CallQuery, goodBody, repeated},
		{"unknown field", http.MethodPost, "?" + codexrtc.CallQuery, `{"sdp":"v=0","session":{},"extra":1}`, identity()},
		{"no model", http.MethodPost, "?" + codexrtc.CallQuery, `{"sdp":"v=0","session":{"delegation":{"type":"client"}}}`, identity()},
	}
	for _, c := range cases {
		if status := post(t, server, c.method, c.query, c.header, c.body); status != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", c.name, status)
		}
	}
	if got := len(backend.Errors()); got != len(cases) || len(backend.Calls()) != 0 {
		t.Fatalf("recorded %d errors and %d calls", got, len(backend.Calls()))
	}
	if status := post(t, server, http.MethodPost, "?"+codexrtc.CallQuery, identity(), goodBody); status != http.StatusCreated {
		t.Fatalf("valid call: status %d", status)
	}
	if calls := backend.Calls(); len(calls) != 1 || calls[0].Session.Model != quicksilver.ModelCodex || backend.Peer() != nil {
		t.Fatalf("calls = %#v", calls)
	}
}

func TestCallCreationWithoutANetworkOrAnswerFails(t *testing.T) {
	backend, server := serve(t)
	if status := post(t, server, http.MethodPost, "?"+codexrtc.CallQuery, identity(), goodBody); status != http.StatusInternalServerError {
		t.Fatalf("status %d", status)
	}
	if len(backend.Errors()) != 1 {
		t.Fatalf("errors = %v", backend.Errors())
	}
	if err := backend.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestUnknownPathsAndCallsAreNotFound(t *testing.T) {
	_, server := serve(t)
	for _, path := range []string{"/elsewhere", "/v1/live/" + fakecodex.DefaultCallID} {
		request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		closeBody(t, response)
		if response.StatusCode != http.StatusNotFound {
			t.Fatalf("%s: status %d", path, response.StatusCode)
		}
	}
}

func TestSidebandRecordsMalformedClientFramesAndWaitsAreBounded(t *testing.T) {
	backend, server := serve(t, fakecodex.WithAnswer("v=answer\r\n"))
	if status := post(t, server, http.MethodPost, "?"+codexrtc.CallQuery, identity(), goodBody); status != http.StatusCreated {
		t.Fatalf("status %d", status)
	}
	url := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/live/" + fakecodex.DefaultCallID
	header := identity()
	header.Del("Content-Type")
	conn, response, err := websocket.DefaultDialer.DialContext(t.Context(), url, header)
	if response != nil {
		closeBody(t, response)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeQuietly(t, conn.Close) })
	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"no":"type"}`)); err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"session.close"}`)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	events, err := backend.WaitClientEvents(ctx, 1)
	if err != nil || len(events) != 1 || len(backend.ClientEvents()) != 1 {
		t.Fatalf("events %#v, err %v", events, err)
	}
	if len(backend.Errors()) != 1 {
		t.Fatalf("errors = %v", backend.Errors())
	}
	expired, cancelExpired := context.WithCancel(t.Context())
	cancelExpired()
	if _, err := backend.WaitClientEvents(expired, 5); !errors.Is(err, context.Canceled) {
		t.Fatalf("bounded wait: %v", err)
	}
}

func TestVirtualNetworkClosesOnce(t *testing.T) {
	network, err := fakecodex.NewVirtualNetwork()
	if err != nil {
		t.Fatal(err)
	}
	if network.Client == nil || network.Server == nil {
		t.Fatal("missing host settings")
	}
	if err := network.Close(); err != nil {
		t.Fatal(err)
	}
	if err := network.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
}

func dialSideband(t *testing.T, server *httptest.Server, header http.Header) (*websocket.Conn, int) {
	t.Helper()
	url := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/live/" + fakecodex.DefaultCallID
	header = header.Clone()
	header.Del("Content-Type")
	conn, response, err := websocket.DefaultDialer.DialContext(t.Context(), url, header)
	status := 0
	if response != nil {
		status = response.StatusCode
		closeBody(t, response)
	}
	if err == nil {
		t.Cleanup(func() { closeQuietly(t, conn.Close) })
	}
	return conn, status
}

func TestPeerAnswerScriptAndResponseShapingFollowTheOptions(t *testing.T) {
	network, err := fakecodex.NewVirtualNetwork()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeQuietly(t, network.Close) })
	started := quicksilver.SessionStarted{Session: &quicksilver.SessionResource{ID: "rtc_custom"}}
	backend, server := serve(t, fakecodex.WithPeerNetwork(network), fakecodex.WithCredential("tok", "acct"),
		fakecodex.WithLocation("/v1/live/rtc_custom"), fakecodex.WithSessionIDHeader("rtc_custom"),
		fakecodex.WithSidebandEvents(started), fakecodex.WithSidebandDrop())
	t.Cleanup(func() { closeQuietly(t, backend.Close) })
	offerer, err := codexrtc.NewPeer(codexrtc.PeerConfig{SettingEngine: network.Client})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeQuietly(t, offerer.Close) })
	offer, err := offerer.CreateOffer(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	header := identity()
	header.Set(codexrtc.HeaderAuthorization, "Bearer tok")
	header.Set(codexrtc.HeaderAccountID, "acct")
	body := strings.Replace(goodBody, `v=offer\r\n`, strings.ReplaceAll(strings.ReplaceAll(offer, "\r", `\r`), "\n", `\n`), 1)
	if status := post(t, server, http.MethodPost, "?"+codexrtc.CallQuery, header, body); status != http.StatusCreated {
		t.Fatalf("status %d, errors %v", status, backend.Errors())
	}
	if backend.Peer() == nil {
		t.Fatal("no answer peer")
	}
	// The sideband needs the call's exact identity.
	wrong := header.Clone()
	wrong.Set(codexrtc.HeaderThreadID, "other")
	if _, status := dialSideband(t, server, wrong); status != http.StatusUnauthorized {
		t.Fatalf("mismatched identity: status %d", status)
	}
	conn, _ := dialSideband(t, server, header)
	if conn == nil {
		t.Fatal("sideband refused")
	}
	_, frame, err := conn.ReadMessage()
	if err != nil || !strings.Contains(string(frame), `"rtc_custom"`) {
		t.Fatalf("scripted frame %s, err %v", frame, err)
	}
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("dropped sideband kept reading")
	}
}

func TestStatusOptionsRejectCallsAndSidebands(t *testing.T) {
	_, server := serve(t, fakecodex.WithCallStatus(http.StatusTooManyRequests))
	if status := post(t, server, http.MethodPost, "?"+codexrtc.CallQuery, identity(), goodBody); status != http.StatusTooManyRequests {
		t.Fatalf("call status %d", status)
	}
	_, sidebandServer := serve(t, fakecodex.WithSidebandStatus(http.StatusGone))
	if _, status := dialSideband(t, sidebandServer, identity()); status != http.StatusGone {
		t.Fatalf("sideband status %d", status)
	}
}

func TestStalledSidebandHoldsUntilTheBackendCloses(t *testing.T) {
	backend, server := serve(t, fakecodex.WithAnswer("v=answer\r\n"), fakecodex.WithSidebandStall())
	if status := post(t, server, http.MethodPost, "?"+codexrtc.CallQuery, identity(), goodBody); status != http.StatusCreated {
		t.Fatalf("status %d", status)
	}
	conn, _ := dialSideband(t, server, identity())
	if conn == nil {
		t.Fatal("sideband refused")
	}
	if err := backend.Close(); err != nil {
		t.Fatal(err)
	}
	// Released, the handler closes the connection, so the read ends.
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("stalled sideband stayed open after Close")
	}
	if err := backend.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
}
