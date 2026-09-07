package integration

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

const sessionReplaySafetyTimeout = 10 * time.Second

// TestRecordReplaySession exercises the session replay path by loading a
// .session.json fixture containing realistic bidirectional events (text deltas)
// and verifying that the replayer produces the same server-to-client event
// sequence.
//
// The fixture file contains a synthetic user text event, model text deltas, and
// session close. Client-to-server events are filtered out for this read-only
// fixture rendering check.
func TestRecordReplaySession(t *testing.T) {
	fixturePath := locateSharedSessionFixture(t, "session_text_reply.session.json")
	assertSanitizedSessionFixture(t, fixturePath)

	replayer, err := gwtesting.NewSessionReplayer(fixturePath, gwtesting.WithReplayOutboundValidation(false))
	if err != nil {
		t.Fatalf("NewSessionReplayer: %v", err)
	}

	// Collect all server-to-client events from the replayer.
	var received []messages.StreamMessage
	ctx, cancel := context.WithTimeout(context.Background(), sessionReplaySafetyTimeout)
	defer cancel()
	for {
		select {
		case <-replayer.Done():
			goto drain
		case msg := <-replayer.Receive().Chan():
			received = append(received, msg)
		case <-ctx.Done():
			t.Fatalf("timed out waiting for replayer to finish: %v", ctx.Err())
		}
	}
drain:
	for {
		msg, ok := replayer.Receive().Read()
		if !ok {
			break
		}
		received = append(received, msg)
	}

	expectedTypes := []messages.StreamMessageType{
		messages.StreamTypeSessionCreated,
		messages.StreamTypeMessageStart,
		messages.StreamTypeTextStart,
		messages.StreamTypeTextDelta,
		messages.StreamTypeTextDelta,
		messages.StreamTypeTextEnd,
		messages.StreamTypeMessageEnd,
		messages.StreamTypeSessionClose,
	}

	if len(received) != len(expectedTypes) {
		t.Fatalf("expected %d events, got %d", len(expectedTypes), len(received))
	}

	for i, want := range expectedTypes {
		if received[i].Type != want {
			t.Errorf("event[%d] type = %q, want %q", i, received[i].Type, want)
		}
	}

	// Verify text delta content was deserialized correctly.
	delta1, ok := received[3].Value.(*messages.TextDeltaValue)
	if !ok {
		t.Fatalf("event[3] value type = %T, want *TextDeltaValue", received[3].Value)
	}
	if delta1.Content != "Hello! How can I " {
		t.Errorf("event[3] content = %q, want %q", delta1.Content, "Hello! How can I ")
	}

	delta2, ok := received[4].Value.(*messages.TextDeltaValue)
	if !ok {
		t.Fatalf("event[4] value type = %T, want *TextDeltaValue", received[4].Value)
	}
	if delta2.Content != "help you today?" {
		t.Errorf("event[4] content = %q, want %q", delta2.Content, "help you today?")
	}

	closeValue, ok := received[7].Value.(*messages.SessionCloseValue)
	if !ok {
		t.Fatalf("event[7] value type = %T, want *SessionCloseValue", received[7].Value)
	}
	if closeValue.Reason != "fixture_complete" {
		t.Errorf("session close reason = %q, want fixture_complete", closeValue.Reason)
	}
}

func TestSessionReplayFixture_InboundBeforeOutbound_UnblocksLaterInbound(t *testing.T) {
	fixturePath := locateSharedSessionFixture(t, "session_inbound_then_outbound.session.json")
	assertSanitizedSessionFixture(t, fixturePath)

	replayer, err := gwtesting.NewSessionReplayer(fixturePath)
	if err != nil {
		t.Fatalf("NewSessionReplayer: %v", err)
	}
	defer func() { _ = replayer.Close() }()

	first := readFixtureReplayMessage(t, replayer)
	if first.Type != messages.StreamTypeSessionCreated {
		t.Fatalf("first event type = %q, want %q", first.Type, messages.StreamTypeSessionCreated)
	}

	assertNoFixtureReplayMessage(t, replayer)

	if !replayer.Send(context.Background(), messages.StreamMessage{
		Type:  messages.StreamTypeTextDelta,
		Value: messages.NewTextDeltaValue("continue after provider greeting"),
	}) {
		t.Fatalf("expected outbound user event to match fixture: %v", replayer.Err())
	}

	next := readFixtureReplayMessage(t, replayer)
	delta, ok := next.Value.(*messages.TextDeltaValue)
	if !ok {
		t.Fatalf("next event value type = %T, want *TextDeltaValue", next.Value)
	}
	if delta.Content != "provider response after client event" {
		t.Fatalf("next delta = %q, want provider response after client event", delta.Content)
	}
}

func TestSessionReplayFixture_OutboundBeforeInbound_StartsReplayAfterClientEvent(t *testing.T) {
	fixturePath := locateSharedSessionFixture(t, "session_outbound_then_inbound.session.json")
	assertSanitizedSessionFixture(t, fixturePath)

	replayer, err := gwtesting.NewSessionReplayer(fixturePath)
	if err != nil {
		t.Fatalf("NewSessionReplayer: %v", err)
	}
	defer func() { _ = replayer.Close() }()

	assertNoFixtureReplayMessage(t, replayer)

	if !replayer.Send(context.Background(), messages.StreamMessage{
		Type:  messages.StreamTypeTextDelta,
		Value: messages.NewTextDeltaValue("start with client input"),
	}) {
		t.Fatalf("expected initial outbound user event to match fixture: %v", replayer.Err())
	}

	next := readFixtureReplayMessage(t, replayer)
	delta, ok := next.Value.(*messages.TextDeltaValue)
	if !ok {
		t.Fatalf("next event value type = %T, want *TextDeltaValue", next.Value)
	}
	if delta.Content != "provider response after first client input" {
		t.Fatalf("next delta = %q, want provider response after first client input", delta.Content)
	}
}

func assertSanitizedSessionFixture(t *testing.T, path string) {
	t.Helper()

	capture, err := gwtesting.LoadSessionCapture(path)
	if err != nil {
		t.Fatalf("load session fixture: %v", err)
	}
	if capture.Session.FixtureProvenance == "" {
		t.Fatalf("session fixture %s missing fixture_provenance metadata", path)
	}
	for _, record := range capture.Records {
		if record.Direction != gwtesting.DirectionClientToServer {
			continue
		}
		if record.PayloadType == gwtesting.SessionPayloadTypeWebSocketMessage {
			t.Fatalf("stream fixture %s should not contain raw websocket client traffic at sequence %d", path, record.Sequence)
		}
	}
}

func readFixtureReplayMessage(t *testing.T, replayer *gwtesting.SessionReplayer) messages.StreamMessage {
	t.Helper()

	select {
	case msg := <-replayer.Receive().Chan():
		return msg
	case <-replayer.Done():
		// Done can become selectable alongside a buffered final message because
		// the replayer publishes the message before closing its lifecycle signal.
		// Drain that message before treating completion as an unexpected end.
		if msg, ok := replayer.Receive().Read(); ok {
			return msg
		}
		t.Fatalf("replayer finished before next fixture event: %v", replayer.Err())
	case <-time.After(sessionReplaySafetyTimeout):
		t.Fatalf("timed out waiting for fixture replay message after %s", sessionReplaySafetyTimeout)
	}
	return messages.StreamMessage{}
}

func assertNoFixtureReplayMessage(t *testing.T, replayer *gwtesting.SessionReplayer) {
	t.Helper()

	select {
	case msg := <-replayer.Receive().Chan():
		t.Fatalf("received %s before expected outbound fixture event was sent", msg.Type)
	case <-replayer.Done():
		t.Fatalf("replayer finished while waiting for expected outbound fixture event: %v", replayer.Err())
	case <-time.After(50 * time.Millisecond):
	}
}

// TestRecordReplayStateless exercises the HTTP replay round-tripper by loading
// the streaming_2_2.json fixture (a recorded "what is 2 + 2?" response from
// OpenRouter) and replaying a matching request against it.
//
// This validates the core replay mechanism: fixture loading, request matching,
// and response reconstruction. The fixture file is checked into testdata/ for
// CI reproducibility.
func TestRecordReplayStateless(t *testing.T) {
	fixturePath := locateCLIFixture(t, "streaming_2_2.json")

	replayRT, err := gwtesting.NewReplayRoundTripper(fixturePath)
	if err != nil {
		t.Fatalf("load replay transport: %v", err)
	}

	// Build a request matching the fixture's captured request shape:
	// single user message with text content, same URL/method as the fixture.
	reqBody := `{"messages":[{"content":[{"text":"what is 2 + 2?","type":"text"}],"role":"user"}],"model":"z-ai/glm-4.7","tools":[{"function":{"name":"edit_file","description":"Edit a file","parameters":{"properties":{"path":{"type":"string"}},"required":["path"],"type":"object"}},"type":"function"}],"stream":true}`
	req, err := http.NewRequest("POST", "https://openrouter.ai/api/v1/chat/completions", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := replayRT.RoundTrip(req)
	if err != nil {
		t.Fatalf("replay round trip: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	if len(body) == 0 {
		t.Error("expected non-empty response body from replay")
	}

	// The fixture response is a streaming SSE response containing "4".
	if !strings.Contains(string(body), "4") {
		bodyPreview := string(body)
		if len(bodyPreview) > 500 {
			bodyPreview = bodyPreview[:500]
		}
		t.Errorf("expected response body to contain '4'; got:\n%s", bodyPreview)
	}
}

func locateSharedSessionFixture(t *testing.T, name string) string {
	t.Helper()

	path := gwtesting.SharedSessionFixturePath(name)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("shared session fixture %q not found at %q: %v", name, path, err)
	}
	return path
}

func locateCLIFixture(t *testing.T, name string) string {
	t.Helper()

	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve CLI fixture helper path: runtime.Caller failed")
	}

	path := filepath.Join(filepath.Dir(currentFile), "testdata", name)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("CLI fixture %q not found at %q: %v", name, path, err)
	}
	return path
}

func TestLocateSharedSessionFixtureUsesGatewayOwnedRoot(t *testing.T) {
	path := locateSharedSessionFixture(t, "session_text_reply.session.json")

	normalizedPath := filepath.ToSlash(path)
	if !strings.Contains(normalizedPath, gwtesting.SharedSessionFixtureRoot+"/") {
		t.Fatalf("shared fixture path %q should resolve under %q", normalizedPath, gwtesting.SharedSessionFixtureRoot)
	}
}

func TestLocateCLIFixtureUsesAgentCLIPrivateTestdata(t *testing.T) {
	path := locateCLIFixture(t, "openai_realtime_text.session.json")

	normalizedPath := filepath.ToSlash(path)
	if !strings.Contains(normalizedPath, "/agent-cli/test/integration/testdata/") {
		t.Fatalf("CLI fixture path %q should stay under agent-cli private testdata", normalizedPath)
	}
}
