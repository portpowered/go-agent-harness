package codexrtc_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/codexrtc"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/codexrtc/fakecodex"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/quicksilver"
)

// The request shape follows OpenClaw extensions/openai/realtime-quicksilver-wire.test.ts:114-159
// (URL, headers, JSON {sdp, session}) and Codex
// codex-api/src/endpoint/realtime_call.rs:129-165 and its test at 647-695
// (backend JSON body, query).
func TestCallCreationPostsTheChatGPTBackendRequestAndReturnsTheCall(t *testing.T) {
	h := newHarness(t, fixedCredential(), fakecodex.WithAnswer(answerSDP))
	call := h.create(t, deadline(t), "v=offer\r\n")

	want := codexrtc.Call{ID: fakecodex.DefaultCallID, AnswerSDP: answerSDP, SidebandURL: h.sidebandBase() + "/" + fakecodex.DefaultCallID, IDs: testIDs()}
	if call != want {
		t.Fatalf("call = %#v, want %#v", call, want)
	}
	if errs := h.backend.Errors(); len(errs) > 0 {
		t.Fatalf("backend saw protocol errors: %v", errs)
	}
	record := h.backend.Calls()[0]
	wantHeaders := map[string]string{
		"Authorization":      "Bearer " + fakecodex.DefaultToken,
		"Chatgpt-Account-Id": fakecodex.DefaultAccountID,
		"Openai-Alpha":       "quicksilver=v2",
		"Originator":         codexrtc.DefaultOriginator,
		"Version":            "test-1",
		"Session-Id":         "session-test",
		"Thread-Id":          "thread-test",
		"X-Session-Id":       "realtime-test",
		"Content-Type":       "application/json",
	}
	for name, value := range wantHeaders {
		if got := record.Header.Get(name); got != value {
			t.Errorf("header %s = %q, want %q", name, got, value)
		}
	}
	if record.OfferSDP != "v=offer\r\n" || !reflect.DeepEqual(record.Session, codexSession(t)) {
		t.Fatalf("recorded offer %q session %#v", record.OfferSDP, record.Session)
	}
}

func TestCallCreationGeneratesDistinctSessionIDsWhenNoneAreGiven(t *testing.T) {
	h := newHarness(t, fixedCredential(), fakecodex.WithAnswer(answerSDP))
	call, err := h.client.Create(deadline(t), codexrtc.CallRequest{OfferSDP: "v=offer\r\n", Session: codexSession(t)})
	if err != nil {
		t.Fatalf("create: %v (backend errors %v)", err, h.backend.Errors())
	}
	ids := call.IDs
	if ids.SessionID == "" || ids.SessionID == ids.ThreadID || ids.ThreadID == ids.RealtimeSessionID {
		t.Fatalf("ids = %#v", ids)
	}
	if got := h.backend.Calls()[0].Header.Get("X-Session-Id"); got != ids.RealtimeSessionID {
		t.Fatalf("x-session-id %q, want %q", got, ids.RealtimeSessionID)
	}
}

func TestCallIDComesFromLocationOrTheSessionIDHeader(t *testing.T) {
	const uuidID = "019eb97d-8e9a-7ff3-94b0-ea019babd5d7"
	cases := []struct {
		name, location, sessionID, want string
	}{
		// Codex realtime_call.rs:459-493 (forwarded backend Location).
		{name: "forwarded backend path", location: "/v1/realtime/calls/calls/rtc_backend_test", want: "rtc_backend_test"},
		// OpenClaw realtime-quicksilver-wire.test.ts:10-15 (query ignored).
		{name: "live path with query", location: "/v1/live/rtc_test?source=test", want: "rtc_test"},
		// Codex realtime_call.rs:784-796 and OpenClaw realtime-quicksilver-wire.test.ts:368-387.
		{name: "uuid", location: "/v1/realtime/calls/" + uuidID, want: uuidID},
		// OpenClaw realtime-quicksilver-wire.test.ts:337-366 (header fallbacks).
		{name: "no location", sessionID: "rtc_header_fallback", want: "rtc_header_fallback"},
		{name: "malformed location", location: "http://[invalid", sessionID: "rtc_malformed_location_fallback", want: "rtc_malformed_location_fallback"},
		{name: "location without id", location: "/v1/live/not-a-call", sessionID: "rtc_invalid_path_fallback", want: "rtc_invalid_path_fallback"},
		{name: "oversized location", location: "/v1/live/rtc_big?" + strings.Repeat("x", 600), sessionID: "rtc_size_fallback", want: "rtc_size_fallback"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, fixedCredential(), fakecodex.WithAnswer(answerSDP), fakecodex.WithLocation(c.location), fakecodex.WithSessionIDHeader(c.sessionID))
			call := h.create(t, deadline(t), "v=offer\r\n")
			if call.ID != c.want || call.SidebandURL != h.sidebandBase()+"/"+c.want {
				t.Fatalf("call = %#v, want id %q", call, c.want)
			}
		})
	}
}

func TestCallCreationRejectsResponsesWithoutACallIDOrAnswer(t *testing.T) {
	cases := map[string][]fakecodex.Option{
		// Codex realtime_call.rs:750-782; dot-only ids as in Codex
		// realtime_websocket/methods.rs:2142-2160.
		"location without id":     {fakecodex.WithAnswer(answerSDP), fakecodex.WithLocation("/v1/realtime/calls")},
		"no location or header":   {fakecodex.WithAnswer(answerSDP), fakecodex.WithLocation("")},
		"dot segment":             {fakecodex.WithAnswer(answerSDP), fakecodex.WithLocation("/v1/live/..")},
		"bad session id header":   {fakecodex.WithAnswer(answerSDP), fakecodex.WithLocation(""), fakecodex.WithSessionIDHeader("../admin")},
		"empty answer":            {fakecodex.WithAnswer("  \r\n")},
		"oversized answer":        {fakecodex.WithAnswer(answerSDP + strings.Repeat("x", codexrtc.MaxAnswerBytes))},
		"rtc prefix without tail": {fakecodex.WithAnswer(answerSDP), fakecodex.WithLocation("/v1/live/rtc_")},
	}
	for name, options := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, fixedCredential(), options...)
			_, err := h.client.Create(deadline(t), codexrtc.CallRequest{OfferSDP: "v=offer\r\n", Session: codexSession(t), IDs: testIDs()})
			if !errors.Is(err, codexrtc.ErrBadCallResponse) {
				t.Fatalf("err = %v, want ErrBadCallResponse", err)
			}
		})
	}
}

// OpenClaw realtime-sideband-call.test.ts:125-157 and
// realtime-quicksilver-wire.test.ts:246-268: the provider's error body is
// never surfaced, because it can echo credentials.
func TestRejectedCallReportsOnlyTheStatus(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusUnauthorized} {
		h := newHarness(t, fixedCredential(), fakecodex.WithCallStatus(status))
		_, err := h.client.Create(deadline(t), codexrtc.CallRequest{OfferSDP: "v=offer\r\n", Session: codexSession(t), IDs: testIDs()})
		var statusErr *codexrtc.StatusError
		if !errors.As(err, &statusErr) || statusErr.StatusCode != status || !errors.Is(err, codexrtc.ErrCallRejected) {
			t.Fatalf("err = %v", err)
		}
		if strings.Contains(err.Error(), fakecodex.DefaultToken) {
			t.Fatalf("error %q leaks the provider body", err)
		}
		if errors.Is(err, codexrtc.ErrUnauthorized) != (status == http.StatusUnauthorized) || errors.Is(err, codexrtc.ErrCallEnded) {
			t.Fatalf("status %d classified as %v", status, err)
		}
	}
}

func TestCallCreationNeedsACompleteCredentialAndRequestBeforeSending(t *testing.T) {
	failing := codexrtc.CredentialFunc(func(context.Context) (codexrtc.Credential, error) {
		return codexrtc.Credential{}, errors.New("sign in again")
	})
	partial := codexrtc.CredentialFunc(func(context.Context) (codexrtc.Credential, error) {
		return codexrtc.Credential{AccessToken: "token"}, nil
	})
	for name, credential := range map[string]codexrtc.CredentialSource{"failing": failing, "no account": partial} {
		h := newHarness(t, credential, fakecodex.WithAnswer(answerSDP))
		_, err := h.client.Create(deadline(t), codexrtc.CallRequest{OfferSDP: "v=offer\r\n", Session: codexSession(t)})
		if !errors.Is(err, codexrtc.ErrNoCredential) || len(h.backend.Calls()) != 0 {
			t.Fatalf("%s: err = %v, calls %d", name, err, len(h.backend.Calls()))
		}
	}
	h := newHarness(t, fixedCredential(), fakecodex.WithAnswer(answerSDP))
	for name, request := range map[string]codexrtc.CallRequest{
		"no offer": {Session: codexSession(t)},
		"no model": {OfferSDP: "v=offer\r\n"},
	} {
		if _, err := h.client.Create(deadline(t), request); !errors.Is(err, quicksilver.ErrInvalidSessionConfig) {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
	if len(h.backend.Calls()) != 0 || len(h.backend.Errors()) != 0 {
		t.Fatal("an invalid request reached the backend")
	}
}

func TestCallCreationReportsTransportFailures(t *testing.T) {
	h := newHarness(t, fixedCredential(), fakecodex.WithAnswer(answerSDP))
	h.server.Close()
	_, err := h.client.Create(deadline(t), codexrtc.CallRequest{OfferSDP: "v=offer\r\n", Session: codexSession(t)})
	var statusErr *codexrtc.StatusError
	if err == nil || errors.As(err, &statusErr) {
		t.Fatalf("err = %v, want a transport error", err)
	}
}

func TestNewCallClientValidatesItsConfig(t *testing.T) {
	if _, err := codexrtc.NewCallClient(codexrtc.CallConfig{}); !errors.Is(err, codexrtc.ErrNoCredential) {
		t.Fatalf("nil credential: err = %v", err)
	}
	for name, config := range map[string]codexrtc.CallConfig{
		"backend scheme":        {BackendURL: "ftp://chatgpt.com/backend-api/codex"},
		"backend query":         {BackendURL: "https://chatgpt.com/backend-api/codex?x=1"},
		"backend unparsable":    {BackendURL: "https://[::1"},
		"sideband scheme":       {SidebandBaseURL: "https://api.openai.com/v1/live"},
		"sideband without host": {SidebandBaseURL: "wss:///v1/live"},
	} {
		config.Credential = fixedCredential()
		if _, err := codexrtc.NewCallClient(config); err == nil {
			t.Errorf("%s: config accepted", name)
		}
	}
	if _, err := codexrtc.NewCallClient(codexrtc.CallConfig{Credential: fixedCredential()}); err != nil {
		t.Fatalf("defaults rejected: %v", err)
	}
}

func TestRequestIDsAreDistinctUUIDsAndReaderFailuresSurface(t *testing.T) {
	ids, err := codexrtc.NewRequestIDs(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids.SessionID) != 36 || ids.SessionID == ids.ThreadID || ids.ThreadID == ids.RealtimeSessionID {
		t.Fatalf("ids = %#v", ids)
	}
	if _, err := codexrtc.NewRequestIDs(strings.NewReader("short")); !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		t.Fatalf("short reader: err = %v", err)
	}
}

func TestCallBodyIsTheSessionWithoutAnID(t *testing.T) {
	h := newHarness(t, fixedCredential(), fakecodex.WithAnswer(answerSDP))
	h.create(t, deadline(t), "v=offer\r\n")
	encoded, err := json.Marshal(h.backend.Calls()[0].Session)
	if err != nil {
		t.Fatal(err)
	}
	// Codex realtime_call.rs:141-145 strips session.id before sending.
	if strings.Contains(string(encoded), `"id"`) || !strings.Contains(string(encoded), `"model":"gpt-live-1-codex"`) {
		t.Fatalf("session = %s", encoded)
	}
}
