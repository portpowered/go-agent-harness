package codexrtc_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/codexrtc"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/codexrtc/fakecodex"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/quicksilver"
)

func receive(t *testing.T, sideband *codexrtc.Sideband) quicksilver.Event {
	t.Helper()
	event, err := sideband.Receive()
	if err != nil {
		t.Fatalf("receive: %v", err)
	}
	return event
}

// The sideband repeats the call's identity headers (OpenClaw
// realtime-quicksilver-session.test.ts:364-389), sends nothing on connect
// (OpenClaw realtime-quicksilver-gateway-bridge.test.ts:424 and Codex
// realtime_websocket/methods.rs:986-1016), receives server events, and
// answers delegations with context appends.
func TestSidebandCarriesTheDialectWithTheCallIdentity(t *testing.T) {
	started := quicksilver.SessionStarted{Session: &quicksilver.SessionResource{ID: fakecodex.DefaultCallID}}
	delegation := quicksilver.DelegationCreated{Item: quicksilver.DelegationItem{
		ID: "delegation-1", Type: quicksilver.ItemTypeDelegation, Target: quicksilver.TargetClient,
		Content: []quicksilver.ContentPart{{Type: quicksilver.PartInputText, Text: "check the repository"}},
	}}
	h := newHarness(t, fixedCredential(), fakecodex.WithAnswer(answerSDP), fakecodex.WithSidebandEvents(started, delegation))
	ctx := deadline(t)
	call := h.create(t, ctx, "v=offer\r\n")
	sideband, err := h.client.DialSideband(ctx, call)
	if err != nil {
		t.Fatalf("dial: %v (backend errors %v)", err, h.backend.Errors())
	}
	if got := receive(t, sideband); !reflect.DeepEqual(got, started) {
		t.Fatalf("first event = %#v", got)
	}
	got, ok := receive(t, sideband).(quicksilver.DelegationCreated)
	if !ok || !got.IsClient() || got.Prompt() != "check the repository" {
		t.Fatalf("delegation = %#v", got)
	}
	for _, event := range quicksilver.ContextAppends("The repository is clean.", quicksilver.ChannelSpeakable, got.Item.ID) {
		if err := sideband.Send(event); err != nil {
			t.Fatal(err)
		}
	}
	if err := sideband.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	events, err := h.backend.WaitClientEvents(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	want := []quicksilver.Event{
		quicksilver.DelegationContextAppend{DelegationItemID: "delegation-1", Channel: quicksilver.ChannelSpeakable,
			Content: []quicksilver.ContentPart{{Type: quicksilver.PartInputText, Text: "The repository is clean."}}},
		quicksilver.SessionClose{},
	}
	if !reflect.DeepEqual(events, want) || len(h.backend.Errors()) != 0 {
		t.Fatalf("client events %#v, errors %v", events, h.backend.Errors())
	}
	if _, err := sideband.Receive(); !errors.Is(err, codexrtc.ErrSidebandClosed) {
		t.Fatalf("receive after close: %v", err)
	}
	if err := sideband.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if err := sideband.Send(quicksilver.SessionClose{}); !errors.Is(err, codexrtc.ErrSidebandClosed) {
		t.Fatalf("send after close: %v", err)
	}
}

func TestServerNormalCloseEndsTheSidebandCleanly(t *testing.T) {
	h := newHarness(t, fixedCredential(), fakecodex.WithAnswer(answerSDP))
	ctx := deadline(t)
	sideband, err := h.client.DialSideband(ctx, h.create(t, ctx, "v=offer\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := sideband.Close(); err != nil {
			t.Logf("close: %v", err)
		}
	})
	// The fake answers session.close with a normal close frame.
	if err := sideband.Send(quicksilver.SessionClose{}); err != nil {
		t.Fatal(err)
	}
	if _, err := sideband.Receive(); !errors.Is(err, codexrtc.ErrSidebandClosed) {
		t.Fatalf("receive: %v, want ErrSidebandClosed", err)
	}
}

// Codex realtime_websocket/methods.rs:1476-1497: losing the socket without a
// normal close is a transport error, not a clean end.
func TestDroppedSidebandIsATransportError(t *testing.T) {
	h := newHarness(t, fixedCredential(), fakecodex.WithAnswer(answerSDP), fakecodex.WithSidebandDrop())
	ctx := deadline(t)
	sideband, err := h.client.DialSideband(ctx, h.create(t, ctx, "v=offer\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := sideband.Close(); err != nil {
			t.Logf("close: %v", err)
		}
	})
	if _, err := sideband.Receive(); err == nil || errors.Is(err, codexrtc.ErrSidebandClosed) {
		t.Fatalf("receive: %v, want a transport error", err)
	}
}

// Codex realtime_websocket/methods.rs:1021-1027 and 1429-1440: 404 and 410
// mean the call is gone; other statuses are retryable.
func TestSidebandHandshakeStatusesAreClassified(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusGone, http.StatusUnauthorized, http.StatusInternalServerError} {
		h := newHarness(t, fixedCredential(), fakecodex.WithAnswer(answerSDP), fakecodex.WithSidebandStatus(status))
		ctx := deadline(t)
		_, err := h.client.DialSideband(ctx, h.create(t, ctx, "v=offer\r\n"))
		var statusErr *codexrtc.StatusError
		if !errors.As(err, &statusErr) || statusErr.StatusCode != status || errors.Is(err, codexrtc.ErrCallRejected) {
			t.Fatalf("status %d: err = %v", status, err)
		}
		ended := status == http.StatusNotFound || status == http.StatusGone
		if errors.Is(err, codexrtc.ErrCallEnded) != ended || errors.Is(err, codexrtc.ErrUnauthorized) != (status == http.StatusUnauthorized) {
			t.Fatalf("status %d classified as %v", status, err)
		}
	}
}

func TestSidebandRefusesUnsafeCallIDsAndMissingCredentials(t *testing.T) {
	h := newHarness(t, fixedCredential(), fakecodex.WithAnswer(answerSDP))
	ctx := deadline(t)
	for _, id := range []string{"", "..", "../admin", "rtc_a/b"} {
		if _, err := h.client.DialSideband(ctx, codexrtc.Call{ID: id, IDs: testIDs()}); !errors.Is(err, codexrtc.ErrBadCallResponse) {
			t.Fatalf("call id %q: err = %v", id, err)
		}
	}
	failing := newHarness(t, codexrtc.CredentialFunc(func(context.Context) (codexrtc.Credential, error) {
		return codexrtc.Credential{}, errors.New("signed out")
	}))
	if _, err := failing.client.DialSideband(ctx, codexrtc.Call{ID: fakecodex.DefaultCallID, IDs: testIDs()}); !errors.Is(err, codexrtc.ErrNoCredential) {
		t.Fatalf("err = %v", err)
	}
}

func TestSidebandDialFailureWithoutAResponseIsATransportError(t *testing.T) {
	h := newHarness(t, fixedCredential(), fakecodex.WithAnswer(answerSDP))
	ctx := deadline(t)
	call := h.create(t, ctx, "v=offer\r\n")
	h.server.Close()
	_, err := h.client.DialSideband(ctx, call)
	var statusErr *codexrtc.StatusError
	if err == nil || errors.As(err, &statusErr) {
		t.Fatalf("err = %v, want a transport error", err)
	}
}

func TestSidebandRejectsAMismatchedIdentity(t *testing.T) {
	h := newHarness(t, fixedCredential(), fakecodex.WithAnswer(answerSDP))
	ctx := deadline(t)
	call := h.create(t, ctx, "v=offer\r\n")
	call.IDs.ThreadID = "another-thread"
	_, err := h.client.DialSideband(ctx, call)
	if !errors.Is(err, codexrtc.ErrUnauthorized) || len(h.backend.Errors()) != 1 {
		t.Fatalf("err = %v, backend errors %v", err, h.backend.Errors())
	}
}

// bigAppend is a context append large enough that a peer which stops reading
// fills the socket buffers within a few sends.
func bigAppend() quicksilver.Event {
	return quicksilver.SessionContextAppend{Content: []quicksilver.ContentPart{{Type: quicksilver.PartInputText, Text: strings.Repeat("x", 1<<20)}}}
}

// stalledSideband dials a sideband whose peer never reads.
func stalledSideband(t *testing.T, writeTimeout time.Duration) *codexrtc.Sideband {
	t.Helper()
	h := newHarness(t, fixedCredential(), fakecodex.WithAnswer(answerSDP), fakecodex.WithSidebandStall())
	client, err := codexrtc.NewCallClient(codexrtc.CallConfig{
		Credential: fixedCredential(), BackendURL: h.server.URL + "/backend-api/codex",
		SidebandBaseURL: h.sidebandBase(), HTTPClient: h.server.Client(), SidebandWriteTimeout: writeTimeout,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := deadline(t)
	call, err := client.Create(ctx, codexrtc.CallRequest{OfferSDP: "v=offer\r\n", Session: codexSession(t), IDs: testIDs()})
	if err != nil {
		t.Fatal(err)
	}
	sideband, err := client.DialSideband(ctx, call)
	if err != nil {
		t.Fatal(err)
	}
	return sideband
}

// A peer that stops reading must not hold Close: it expires the stuck
// write, skips the courtesy frames, and drops the connection.
func TestCloseReturnsPromptlyWhileASendIsStuckOnAStalledPeer(t *testing.T) {
	sideband := stalledSideband(t, time.Hour)
	sendDone := make(chan error, 1)
	go func() {
		for {
			if err := sideband.Send(bigAppend()); err != nil {
				sendDone <- err
				return
			}
		}
	}()
	// Wait until the sender is blocked: no send error arrives for a while.
	select {
	case err := <-sendDone:
		t.Fatalf("send failed before the peer stalled: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	closed := make(chan error, 1)
	start := time.Now()
	go func() { closed <- sideband.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("close: %v", err)
		}
	case <-time.After(5 * codexrtc.SidebandCloseGrace):
		t.Fatal("Close blocked behind a stuck Send")
	}
	if elapsed := time.Since(start); elapsed > 3*codexrtc.SidebandCloseGrace {
		t.Fatalf("Close took %s", elapsed)
	}
	select {
	case err := <-sendDone:
		if err == nil {
			t.Fatal("stuck send reported success")
		}
	case <-time.After(5 * codexrtc.SidebandCloseGrace):
		t.Fatal("stuck Send never returned after Close")
	}
}

func TestSendGivesUpAfterTheWriteTimeoutOnAStalledPeer(t *testing.T) {
	sideband := stalledSideband(t, 100*time.Millisecond)
	t.Cleanup(func() {
		if err := sideband.Close(); err != nil {
			t.Logf("close: %v", err)
		}
	})
	var err error
	for range 64 {
		if err = sideband.Send(bigAppend()); err != nil {
			break
		}
	}
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("send on a stalled peer: %v, want a write deadline error", err)
	}
}
