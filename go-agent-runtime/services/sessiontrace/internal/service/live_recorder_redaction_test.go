package service

import (
	"context"
	"encoding/base64"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

// A session credential never reaches the trace: not split across two
// streamed TOOLCALL.DELTA messages, not JSON-escaped in TOOLCALL.END, and not
// base64 (standard or URL) or URL-query-escaped in a delegation tool's result
// or error. The credential has characters JSON and URLs escape, and bytes
// whose base64 differs between the two alphabets.
func TestTraceRedactsEveryFormOfACredential(t *testing.T) {
	const secret = "sk-trace<sec>?~~~-9876"
	root := t.TempDir()
	prepared, err := New().Prepare(sessiontrace.Request{
		RecordDirectory: filepath.Join(root, "requested"), Clock: clock.NewDeterministic(time.Unix(800, 0), time.Millisecond),
		Credentials: []string{secret},
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder := prepared.WrapLiveRecorder(nil, session.LiveRequest{})
	ctx := context.Background()
	arguments := `{"token":"` + secret + `"}`
	split := len(`{"token":"sk-tr`)
	for _, message := range []messages.StreamMessage{
		{Type: messages.StreamTypeToolCallStart, ToolCallId: "call-1", Value: messages.NewToolCallStartValue("call-1", "lookup")},
		{Type: messages.StreamTypeToolCallDelta, ToolCallId: "call-1", Value: messages.NewToolCallDeltaValue(arguments[:split])},
		{Type: messages.StreamTypeToolCallDelta, ToolCallId: "call-1", Value: messages.NewToolCallDeltaValue(arguments[split:])},
		{Type: messages.StreamTypeToolCallEnd, ToolCallId: "call-1", Value: messages.NewToolCallEndValue("call-1", "lookup", arguments)},
	} {
		if err := recorder.RecordMessage(ctx, session.LiveRecord{Direction: session.LiveRecordAgent, Timestamp: time.Unix(800, 0), Message: message}); err != nil {
			t.Fatal(err)
		}
	}
	encoded := strings.Join([]string{
		base64.StdEncoding.EncodeToString([]byte(secret)), base64.URLEncoding.EncodeToString([]byte(secret)),
		base64.RawURLEncoding.EncodeToString([]byte(secret)), url.QueryEscape(secret),
	}, " ")
	if err := recorder.RecordEvent(ctx, session.LiveEvent{
		Kind: string(session.LiveEventDelegationToolResult), ItemID: "del", ToolCallID: "call-2", Text: "lookup",
		DelegationTool: &session.LiveDelegationTool{Name: "lookup", Arguments: arguments, Result: encoded},
		Error:          errors.New("backend refused " + secret),
	}); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Finalize(ctx, nil); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(root, "bundle")
	if err := os.Mkdir(bundle, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := prepared.Finish(ctx, bundle, true); err != nil {
		t.Fatal(err)
	}
	leaks := append(transcript.CredentialForms([]string{secret}), "sk-tr", "-9876")
	toolEvents := 0
	for _, event := range readTraceEvents(t, filepath.Join(bundle, "audio-trace", "timeline.jsonl")) {
		text := string(event.Payload) + " " + event.Error
		if strings.Contains(text, "call-1") || strings.Contains(text, "call-2") {
			toolEvents++
		}
		for _, leak := range leaks {
			if strings.Contains(text, leak) {
				t.Errorf("trace %s event holds %q: %s", event.RuntimeKind, leak, text)
			}
		}
	}
	if toolEvents == 0 {
		t.Fatal("the trace holds no tool event to check")
	}
}
