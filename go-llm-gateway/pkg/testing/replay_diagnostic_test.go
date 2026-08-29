package testing

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/gateway"
)

func TestCompareReplayPayloadsReportsFirstDeterministicJSONDifference(t *testing.T) {
	tests := []struct {
		name         string
		expected     string
		actual       string
		wantLocation string
		wantExpected string
		wantActual   string
	}{
		{
			name:         "nested object key order is deterministic",
			expected:     `{"outer":{"target":"before","other":1}}`,
			actual:       `{"outer":{"other":1,"target":"after"}}`,
			wantLocation: "JSON pointer /outer/target",
			wantExpected: `"before"`,
			wantActual:   `"after"`,
		},
		{
			name:         "array index",
			expected:     `{"items":[{"value":"keep"},{"value":"before"}]}`,
			actual:       `{"items":[{"value":"keep"},{"value":"after"}]}`,
			wantLocation: "JSON pointer /items/1/value",
			wantExpected: `"before"`,
			wantActual:   `"after"`,
		},
		{
			name:         "missing object field",
			expected:     `{"object":{"kept":true}}`,
			actual:       `{"object":{"extra":1,"kept":true}}`,
			wantLocation: "JSON pointer /object/extra",
			wantExpected: "<missing>",
			wantActual:   "1",
		},
		{
			name:         "different scalar types",
			expected:     `{"value":1}`,
			actual:       `{"value":"one"}`,
			wantLocation: "JSON pointer /value",
			wantExpected: "1",
			wantActual:   `"one"`,
		},
		{
			name:         "root scalar value",
			expected:     `1`,
			actual:       `true`,
			wantLocation: `JSON pointer ""`,
			wantExpected: "1",
			wantActual:   "true",
		},
		{
			name:         "RFC 6901 token escaping",
			expected:     `{"a/b":{"tilde~key":"before"}}`,
			actual:       `{"a/b":{"tilde~key":"after"}}`,
			wantLocation: "JSON pointer /a~1b/tilde~0key",
			wantExpected: `"before"`,
			wantActual:   `"after"`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := compareReplayPayloads([]byte(test.expected), []byte(test.actual))
			if err == nil {
				t.Fatal("compareReplayPayloads returned nil for divergent payloads")
			}

			var divergence *gateway.ReplayPayloadDivergenceError
			if !errors.As(err, &divergence) {
				t.Fatalf("divergence error = %v, want ReplayPayloadDivergenceError", err)
			}
			if divergence.Location != test.wantLocation {
				t.Fatalf("location = %q, want %q", divergence.Location, test.wantLocation)
			}
			if divergence.ExpectedExcerpt != test.wantExpected {
				t.Fatalf("expected excerpt = %q, want %q", divergence.ExpectedExcerpt, test.wantExpected)
			}
			if divergence.ActualExcerpt != test.wantActual {
				t.Fatalf("actual excerpt = %q, want %q", divergence.ActualExcerpt, test.wantActual)
			}
		})
	}
}

func TestCompareReplayPayloadsFallsBackToFirstByteOffset(t *testing.T) {
	tests := []struct {
		name         string
		expected     []byte
		actual       []byte
		wantLocation string
	}{
		{
			name:         "malformed JSON",
			expected:     []byte(`{"value":`),
			actual:       []byte(`{"value":1}`),
			wantLocation: "byte offset 9",
		},
		{
			name:         "shared prefix reaches end of input",
			expected:     []byte("invalid"),
			actual:       []byte("invalid-more"),
			wantLocation: "byte offset 7",
		},
		{
			name:         "escaped control bytes",
			expected:     []byte("{\n"),
			actual:       []byte("{\r"),
			wantLocation: "byte offset 1",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := compareReplayPayloads(test.expected, test.actual)
			if err == nil {
				t.Fatal("compareReplayPayloads returned nil for divergent payloads")
			}

			var divergence *gateway.ReplayPayloadDivergenceError
			if !errors.As(err, &divergence) {
				t.Fatalf("divergence error = %v, want ReplayPayloadDivergenceError", err)
			}
			if divergence.Location != test.wantLocation {
				t.Fatalf("location = %q, want %q", divergence.Location, test.wantLocation)
			}
			if divergence.ExpectedExcerpt == divergence.ActualExcerpt {
				t.Fatalf("expected and actual excerpts should differ: %q", divergence.ExpectedExcerpt)
			}
			if test.name == "escaped control bytes" && strings.Contains(err.Error(), "\n") {
				t.Fatalf("error contains an unescaped newline: %q", err)
			}
		})
	}
}

func TestCompareReplayPayloadsBoundsLongExcerpts(t *testing.T) {
	prefix := strings.Repeat("a", 128)
	err := compareReplayPayloads(
		[]byte(`{"value":"`+prefix+`x"}`),
		[]byte(`{"value":"`+prefix+`y"}`),
	)
	if err == nil {
		t.Fatal("compareReplayPayloads returned nil for long divergent payloads")
	}

	var divergence *gateway.ReplayPayloadDivergenceError
	if !errors.As(err, &divergence) {
		t.Fatalf("divergence error = %v, want ReplayPayloadDivergenceError", err)
	}
	if len(divergence.ExpectedExcerpt) > replayExcerptLimit || len(divergence.ActualExcerpt) > replayExcerptLimit {
		t.Fatalf("excerpts exceed %d characters: expected %d, actual %d", replayExcerptLimit, len(divergence.ExpectedExcerpt), len(divergence.ActualExcerpt))
	}
	if !strings.Contains(divergence.ExpectedExcerpt, replayTruncationMarker) || !strings.Contains(divergence.ActualExcerpt, replayTruncationMarker) {
		t.Fatalf("long excerpts should contain %q: expected %q, actual %q", replayTruncationMarker, divergence.ExpectedExcerpt, divergence.ActualExcerpt)
	}
	if divergence.ExpectedExcerpt == divergence.ActualExcerpt {
		t.Fatalf("long excerpts should remain distinct: %q", divergence.ExpectedExcerpt)
	}
}

func TestCompareReplayPayloadsAcceptsSemanticallyEqualJSON(t *testing.T) {
	err := compareReplayPayloads(
		[]byte(`{"b":2,"a":[true,null]}`),
		[]byte(`{ "a": [true, null], "b": 2 }`),
	)
	if err != nil {
		t.Fatalf("compareReplayPayloads = %v, want nil for semantically equal JSON", err)
	}
}

func TestSessionReplayerMismatchIncludesDivergenceContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "typed.session.json")
	writeCapture(t, path, []CapturedSessionEvent{
		makeCapture(DirectionClientToServer, 0, messages.StreamTypeTextDelta, messages.NewTextDeltaValue("expected")),
	})
	replayer := mustNewSessionReplayer(t, path)

	if replayer.Send(context.Background(), messages.StreamMessage{
		Type:  messages.StreamTypeTextDelta,
		Value: messages.NewTextDeltaValue("actual"),
	}) {
		t.Fatal("Send returned true for a divergent payload")
	}

	err := replayer.Err()
	if err == nil {
		t.Fatal("replayer.Err() = nil, want replay mismatch")
	}
	var mismatch *gateway.ReplayMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("error = %v, want ReplayMismatchError", err)
	}
	wantEvent := fmt.Sprintf("event type %q at sequence 1", messages.StreamTypeTextDelta)
	if !strings.Contains(err.Error(), wantEvent) {
		t.Fatalf("error = %q, want %q", err, wantEvent)
	}
	if !strings.Contains(err.Error(), "JSON pointer /value/content") {
		t.Fatalf("error = %q, want JSON pointer", err)
	}
	if !strings.Contains(err.Error(), `expected "expected"`) || !strings.Contains(err.Error(), `actual "actual"`) {
		t.Fatalf("error = %q, want distinct expected/actual excerpts", err)
	}
	var divergence *gateway.ReplayPayloadDivergenceError
	if !errors.As(err, &divergence) {
		t.Fatalf("error = %v, want structured payload divergence", err)
	}
	if got := replayer.Outcome().Status; got != SessionReplayDiverged {
		t.Fatalf("outcome status = %q, want %q", got, SessionReplayDiverged)
	}
}

func TestReplayWebSocketDialerMismatchIncludesDivergenceContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "raw.session.json")
	writeWebSocketCapture(t, path, []CapturedSessionEvent{
		websocketCapture(DirectionClientToServer, 1, `{"type":"session.update","session":{"model":"expected"}}`),
	})
	dialer, err := NewReplayWebSocketDialer(path)
	if err != nil {
		t.Fatalf("NewReplayWebSocketDialer: %v", err)
	}
	conn, err := dialer.Dial("", nil)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}

	err = conn.WriteMessage(1, []byte(`{"type":"session.update","session":{"model":"actual"}}`))
	if err == nil {
		t.Fatal("WriteMessage returned nil for a divergent payload")
	}
	if !errors.Is(err, gateway.ErrReplayMismatch) {
		t.Fatalf("error = %v, want replay mismatch", err)
	}
	if !strings.Contains(err.Error(), `event type "session.update" at sequence 1`) {
		t.Fatalf("error = %q, want event type and sequence", err)
	}
	if !strings.Contains(err.Error(), "JSON pointer /session/model") {
		t.Fatalf("error = %q, want JSON pointer", err)
	}
	if !strings.Contains(err.Error(), `expected "expected"`) || !strings.Contains(err.Error(), `actual "actual"`) {
		t.Fatalf("error = %q, want distinct expected/actual excerpts", err)
	}
	var divergence *gateway.ReplayPayloadDivergenceError
	if !errors.As(err, &divergence) {
		t.Fatalf("error = %v, want structured payload divergence", err)
	}
}
