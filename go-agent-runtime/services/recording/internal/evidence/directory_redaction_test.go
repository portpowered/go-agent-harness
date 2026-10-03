package evidence

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
)

// A session credential in the voice loop's own tool call (its streamed and
// final arguments), in a tool result, or in a delegation tool call (once
// whole and once across the 4 KiB cut) never reaches the bundle: not in a
// file and not in a base64 transcript payload, raw or JSON-escaped. The
// credential has a character JSON escapes, so both of its forms are covered.
func TestDirectoryRecorderRedactsCredentialsFromToolPayloads(t *testing.T) {
	t.Parallel()
	const secret = "sk-tool<secret>-0123"
	r := newEvidenceRecorder(t)
	r.options.Credentials = []string{secret}
	arguments := `{"token":"` + secret + `"}`
	record := func(direction session.LiveRecordDirection, msg messages.StreamMessage) {
		t.Helper()
		if err := r.RecordMessage(t.Context(), session.LiveRecord{Direction: direction, Timestamp: evidenceTime(), Message: msg}); err != nil {
			t.Fatal(err)
		}
	}
	record(session.LiveRecordAgent, messages.StreamMessage{Type: messages.StreamTypeToolCallStart, ToolCallId: "call-1", Value: messages.NewToolCallStartValue("call-1", "lookup")})
	record(session.LiveRecordAgent, messages.StreamMessage{Type: messages.StreamTypeToolCallDelta, ToolCallId: "call-1", Value: messages.NewToolCallDeltaValue(arguments)})
	record(session.LiveRecordAgent, messages.StreamMessage{Type: messages.StreamTypeToolCallEnd, ToolCallId: "call-1", Value: messages.NewToolCallEndValue("call-1", "lookup", arguments)})
	record(session.LiveRecordAgent, messages.StreamMessage{Type: messages.StreamTypeMessageEnd})
	record(session.LiveRecordClient, messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleTool, ToolCallId: "call-1", Value: messages.NewTextDeltaValue("key is " + secret)})
	straddle := strings.Repeat("m", session.LiveDelegationToolPayloadLimit-len(session.LiveDelegationToolTruncated)-4) + secret + " tail"
	for _, kind := range []session.LiveEventKind{session.LiveEventDelegationToolCall, session.LiveEventDelegationToolResult} {
		if err := r.RecordEvent(t.Context(), session.LiveEvent{
			Kind: string(kind), Timestamp: evidenceTime(), ItemID: "del", ToolCallID: "call-2", Text: "lookup",
			DelegationTool: &session.LiveDelegationTool{Name: "lookup", Arguments: arguments, Result: straddle},
		}); err != nil {
			t.Fatal(err)
		}
	}
	recordEvidenceTerminal(t, r)
	if err := r.Finalize(t.Context(), nil); err != nil {
		t.Fatal(err)
	}

	leaks := []string{secret, `sk-tool\u003csecret\u003e`, "sk-tool"}
	if toolPayloads := assertBundleOmits(t, r.destination, leaks); toolPayloads == 0 {
		t.Fatal("the bundle holds no tool payload to check")
	}
}

// assertBundleOmits fails when a bundle file, or a decoded transcript
// payload, holds one of leaks. It returns how many transcript payloads
// belong to the test's tool calls.
func assertBundleOmits(t *testing.T, destination string, leaks []string) int {
	t.Helper()
	toolPayloads := 0
	err := filepath.WalkDir(destination, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		contents := [][]byte{data}
		if strings.HasSuffix(path, ".transcript.jsonl") {
			payloads, err := transcriptPayloads(data)
			if err != nil {
				return err
			}
			for _, payload := range payloads {
				if bytes.Contains(payload, []byte("call-1")) || bytes.Contains(payload, []byte("call-2")) {
					toolPayloads++
				}
			}
			contents = append(contents, payloads...)
		}
		for _, content := range contents {
			for _, leak := range leaks {
				if bytes.Contains(content, []byte(leak)) {
					t.Errorf("%s holds %q:\n%s", path, leak, content)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return toolPayloads
}

// transcriptPayloads decodes the payload of each record of a transcript.
func transcriptPayloads(data []byte) ([][]byte, error) {
	var payloads [][]byte
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte{'\n'}) {
		record, err := transcript.Decode(line)
		if err != nil {
			return nil, err
		}
		payloads = append(payloads, record.Payload)
	}
	return payloads, nil
}
