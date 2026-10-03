package fakelive_test

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gorilla/websocket"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	live "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/fakelive"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport/transporttest"
)

func TestInProcessDialerConformsToTheTransportContract(t *testing.T) {
	dialErr, readErr := errors.New("dial sentinel"), errors.New("read sentinel")
	writeErr, closeErr := errors.New("write sentinel"), errors.New("close sentinel")
	inbound := []transporttest.Message{
		{Type: websocket.TextMessage, Payload: []byte(`{"type":"info","code":"c","message":"m"}`)},
		{Type: websocket.BinaryMessage, Payload: []byte{0, 1, 2}},
	}
	script := fakelive.WithScript(
		fakelive.SendRaw(inbound[0].Type, inbound[0].Payload),
		fakelive.SendRaw(inbound[1].Type, inbound[1].Payload),
	)
	faulty := func(option fakelive.Option) func() transport.Dialer {
		return func() transport.Dialer { return fakelive.New(option).Dialer() }
	}
	transporttest.RunConformance(t, transporttest.ConformanceHarness{
		Endpoint: live.DefaultEndpoint,
		Headers:  map[string]string{authHeader: bearer, "X-Trace": "conformance"},
		Inbound:  inbound,
		Outbound: []transporttest.Message{
			{Type: websocket.TextMessage, Payload: []byte(`{"type":"session.close"}`)},
			{Type: websocket.BinaryMessage, Payload: []byte{9, 0, 8}},
		},
		NewValid: func() (transport.Dialer, transporttest.Observer) {
			server := fakelive.New(fakelive.WithAPIKey(testKey), script)
			return server.Dialer(), server
		},
		DialFailure:  transporttest.FailureCase{New: faulty(fakelive.WithDialError(dialErr)), WantErr: dialErr},
		ReadFailure:  transporttest.FailureCase{New: faulty(fakelive.WithConnFaults(fakelive.ConnFaults{Read: readErr})), WantErr: readErr},
		WriteFailure: transporttest.FailureCase{New: faulty(fakelive.WithConnFaults(fakelive.ConnFaults{Write: writeErr})), WantErr: writeErr},
		CloseFailure: transporttest.FailureCase{New: faulty(fakelive.WithConnFaults(fakelive.ConnFaults{Close: closeErr})), WantErr: closeErr},
	})
}

func TestDialRequiresTheBearerTokenAndNoQuery(t *testing.T) {
	server := fakelive.New(fakelive.WithAPIKey(testKey))
	for name, tc := range map[string]struct {
		endpoint string
		headers  map[string]string
		want     error
	}{
		"no token":    {live.DefaultEndpoint, nil, fakelive.ErrUnauthorized},
		"wrong token": {live.DefaultEndpoint, map[string]string{authHeader: "Bearer other"}, fakelive.ErrUnauthorized},
		"model query": {live.DefaultEndpoint + "?model=gpt-live-1", map[string]string{authHeader: bearer}, fakelive.ErrQueryNotAllowed},
	} {
		t.Run(name, func(t *testing.T) {
			if conn, err := server.Dialer().Dial(tc.endpoint, tc.headers); !errors.Is(err, tc.want) || conn != nil {
				t.Fatalf("dial = %v, %v; want %v", conn, err, tc.want)
			}
		})
	}
	if _, err := server.Dialer().Dial("://bad", nil); err == nil {
		t.Fatal("unparseable endpoint accepted")
	}
	conn, err := server.Dialer().Dial(live.DefaultEndpoint, map[string]string{"authorization": bearer})
	if err != nil {
		t.Fatalf("lower-case header refused: %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if calls := server.DialCalls(); len(calls) != 5 {
		t.Fatalf("recorded %d dial attempts, want every attempt", len(calls))
	}
}

// TestDialChecksWhateverHeadersTheCredentialProviderSupplies uses a stub
// OAuth credential: a bearer access token plus an account header.
func TestDialChecksWhateverHeadersTheCredentialProviderSupplies(t *testing.T) {
	server := fakelive.New(fakelive.WithAuthHeaders(map[string]string{
		authHeader: "Bearer oauth-access-token", "chatgpt-account-id": "acct_1",
	}))
	for name, tc := range map[string]struct {
		headers map[string]string
		want    error
	}{
		"oauth credential":  {map[string]string{"authorization": "Bearer oauth-access-token", "ChatGPT-Account-Id": "acct_1"}, nil},
		"no account header": {map[string]string{authHeader: "Bearer oauth-access-token"}, fakelive.ErrUnauthorized},
		"other account":     {map[string]string{authHeader: "Bearer oauth-access-token", "ChatGPT-Account-Id": "acct_2"}, fakelive.ErrUnauthorized},
		"api key instead":   {map[string]string{authHeader: bearer, "ChatGPT-Account-Id": "acct_1"}, fakelive.ErrUnauthorized},
	} {
		t.Run(name, func(t *testing.T) {
			conn, err := server.Dialer().Dial(live.DefaultEndpoint, tc.headers)
			if !errors.Is(err, tc.want) {
				t.Fatalf("dial error = %v, want %v", err, tc.want)
			}
			if conn != nil {
				if err := conn.Close(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestScriptWaitsOnVirtualTimeThenPlaysOutput(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		server := fakelive.New(fakelive.WithScript(
			fakelive.AwaitStarted(),
			fakelive.Wait(2*time.Second),
			fakelive.Send(live.OutputAudioDelta{Delta: "AAA="}, live.OutputTranscriptDelta{Delta: "Hi.", StartMS: 0, EndMS: 300}),
		))
		c := dial(t, server)
		c.start(liveConfig())
		started := time.Now()
		expect[live.OutputAudioDelta](c)
		if waited := time.Since(started); waited != 2*time.Second {
			t.Fatalf("output came after %v, want exactly the scripted 2s", waited)
		}
		if transcript := expect[live.OutputTranscriptDelta](c); transcript.Delta != "Hi." {
			t.Fatalf("transcript = %#v", transcript)
		}
	})
}

func TestScriptWaitsOnAnInjectedClock(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		deterministic := clock.NewDeterministic(time.Unix(0, 0), time.Millisecond)
		c := dial(t, fakelive.New(fakelive.WithClock(deterministic), fakelive.WithScript(
			fakelive.AwaitStarted(),
			fakelive.Wait(time.Second),
			fakelive.Send(live.Info{Code: "after_wait", Message: "waited"}),
		)))
		c.start(liveConfig())
		synctest.Wait()
		deterministic.AdvanceBy(999 * time.Millisecond)
		c.send(live.InputAudioMute{EventID: "m1"})
		expect[live.InputAudioMuted](c) // the script is still waiting
		deterministic.AdvanceBy(time.Millisecond)
		if info := expect[live.Info](c); info.Code != "after_wait" {
			t.Fatalf("info = %#v", info)
		}
	})
}

func TestAwaitClientHoldsTheScriptUntilEnoughEvents(t *testing.T) {
	c := dial(t, fakelive.New(fakelive.WithScript(
		fakelive.AwaitClient(live.TypeInputAudioAppend, 2),
		fakelive.Send(live.InputTranscriptDelta{Delta: "Hello", StartMS: 0, EndMS: 10}),
	)))
	c.start(liveConfig())
	c.send(live.InputAudioAppend{Audio: "AAA="})
	c.send(live.InputAudioMute{EventID: "m1"})
	expect[live.InputAudioMuted](c)
	c.send(live.InputAudioAppend{Audio: "AAA="})
	if transcript := expect[live.InputTranscriptDelta](c); transcript.Delta != "Hello" {
		t.Fatalf("transcript = %#v", transcript)
	}
}

// TestAwaitClientBlocksUntilTheEventArrivesOrTheConnectionEnds forces the
// waiting path: the script is durably blocked before the awaited event is
// sent, and a second script is still waiting when the client hangs up.
func TestAwaitClientBlocksUntilTheEventArrivesOrTheConnectionEnds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		server := fakelive.New(fakelive.WithScript(
			fakelive.AwaitClient(live.TypeInputAudioMute, 1),
			fakelive.Send(live.Info{Code: "muted_seen", Message: "m"}),
		))
		c := dial(t, server)
		synctest.Wait() // the script now waits for a state change
		c.start(liveConfig())
		synctest.Wait() // a state change that does not satisfy it
		c.send(live.InputAudioMute{EventID: "m1"})
		expect[live.InputAudioMuted](c)
		if info := expect[live.Info](c); info.Code != "muted_seen" {
			t.Fatalf("info = %#v", info)
		}

		hungUp := fakelive.New(fakelive.WithScript(
			fakelive.AwaitClient(live.TypeInputAudioMute, 1),
			fakelive.Send(live.Info{Code: "never", Message: "m"}),
		))
		conn, err := hungUp.Dialer().Dial(live.DefaultEndpoint, nil)
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if err := conn.Close(); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if errs := hungUp.Errors(); len(errs) != 0 {
			t.Fatalf("errors = %v, want a quiet stop", errs)
		}
	})
}

// TestAwaitedStepsFollowTheAckOfTheEventTheyAwaited: a step after
// AwaitClient is written after the server's answer to the awaited event,
// never before it. Before the fix the step was woken when the event was
// counted, ahead of its ack, and overtook it in about 1 run in 370 under
// load; many connections at once make that window easy to hit.
func TestAwaitedStepsFollowTheAckOfTheEventTheyAwaited(t *testing.T) {
	server := fakelive.New(fakelive.WithScript(
		fakelive.AwaitClient(live.TypeInputAudioMute, 1),
		fakelive.Send(live.Info{Code: "muted_seen", Message: "m"}),
	))
	const connections = 64
	for i := range connections {
		t.Run(fmt.Sprintf("connection %d", i), func(t *testing.T) {
			t.Parallel()
			c := dial(t, server)
			c.start(liveConfig())
			c.send(live.InputAudioMute{EventID: "m1"})
			expect[live.InputAudioMuted](c)
			if info := expect[live.Info](c); info.Code != "muted_seen" {
				t.Fatalf("info = %#v", info)
			}
		})
	}
}

func TestScriptedCloseSendsTheReasonThenClosesTheSocket(t *testing.T) {
	c := dial(t, fakelive.New(fakelive.WithScript(fakelive.AwaitStarted(), fakelive.CloseSession(live.CloseReasonExpired))))
	c.start(liveConfig())
	if closed := expect[live.SessionClosed](c); closed.Reason != live.CloseReasonExpired || closed.Session.ID != fakelive.DefaultSessionID {
		t.Fatalf("session.closed = %#v, want expired", closed)
	}
	c.expectClosedSocket()
	if err := c.conn.WriteMessage(websocket.TextMessage, []byte(`{}`)); !errors.Is(err, fakelive.ErrClosed) {
		t.Fatalf("write after close = %v, want ErrClosed", err)
	}
}

func TestDroppedSocketsEndWithoutSessionClosed(t *testing.T) {
	scripted := dial(t, fakelive.New(fakelive.WithScript(fakelive.AwaitStarted(), fakelive.Drop())))
	scripted.start(liveConfig())
	scripted.expectClosedSocket()

	onClose := dial(t, fakelive.New(fakelive.WithDropOnClose()))
	onClose.start(liveConfig())
	onClose.send(live.SessionClose{EventID: "bye"})
	onClose.expectClosedSocket()
}

func TestScriptFailuresAreReportedAndEndedConnectionsAreNot(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		failing := fakelive.New(fakelive.WithScript(fakelive.Send(nil), fakelive.Send(live.Info{Code: "never"})))
		c := dial(t, failing)
		synctest.Wait()
		if errs := failing.Errors(); len(errs) != 1 || !errors.Is(errs[0], live.ErrMalformedEvent) {
			t.Fatalf("errors = %v, want the one failed step", errs)
		}
		c.start(liveConfig())

		waiting := fakelive.New(fakelive.WithScript(fakelive.Wait(time.Hour), fakelive.Send(live.Info{Code: "late"})))
		conn, err := waiting.Dialer().Dial(live.DefaultEndpoint, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := conn.Close(); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if errs := waiting.Errors(); len(errs) != 0 {
			t.Fatalf("errors after a client close = %v, want none", errs)
		}
		if waiting.CloseCount() != 1 {
			t.Fatalf("close count = %d, want 1", waiting.CloseCount())
		}
	})
}

func TestHTTPServerSpeaksTheProtocolOverWebSocket(t *testing.T) {
	server := fakelive.New(fakelive.WithAPIKey(testKey))
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()
	endpoint := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/v1/live/sessions"

	conn, response, err := websocket.DefaultDialer.Dial(endpoint, http.Header{authHeader: {bearer}})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	closeBody(t, response)
	c := &liveConn{t: t, conn: conn}
	defer func() {
		if err := conn.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	}()
	c.start(liveConfig())
	c.send(live.SessionClose{EventID: "bye"})
	if closed := expect[live.SessionClosed](c); closed.ClientEventID != "bye" {
		t.Fatalf("session.closed = %#v", closed)
	}
	if len(server.WrittenMessages()) != 2 || len(server.Errors()) != 0 {
		t.Fatalf("writes %d, errors %v; want two client frames and no fake errors", len(server.WrittenMessages()), server.Errors())
	}
}

func TestHTTPServerRefusesBadHandshakes(t *testing.T) {
	server := fakelive.New(fakelive.WithAPIKey(testKey))
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()
	endpoint := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/v1/live/sessions"
	for name, tc := range map[string]struct {
		url    string
		header http.Header
		status int
	}{
		"wrong token": {endpoint, http.Header{authHeader: {"Bearer other"}}, http.StatusUnauthorized},
		"query":       {endpoint + "?model=gpt-live-1", http.Header{authHeader: {bearer}}, http.StatusBadRequest},
	} {
		t.Run(name, func(t *testing.T) {
			conn, response, err := websocket.DefaultDialer.Dial(tc.url, tc.header)
			if err == nil {
				t.Fatalf("handshake accepted (close: %v)", conn.Close())
			}
			if response == nil || response.StatusCode != tc.status {
				t.Fatalf("response = %v, want status %d", response, tc.status)
			}
			closeBody(t, response)
		})
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, httpServer.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set(authHeader, bearer)
	plain, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	closeBody(t, plain)
	if errs := server.Errors(); len(errs) != 1 {
		t.Fatalf("errors = %v, want the failed upgrade of a plain request", errs)
	}
}

func closeBody(t *testing.T, response *http.Response) {
	t.Helper()
	if response == nil {
		return
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
}
