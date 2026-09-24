package stream

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

type renderWriteError string

func (e renderWriteError) Error() string { return string(e) }

const errRenderWrite renderWriteError = "render write failed"

// countingWriter fails the write numbered failAt and every later write.
type countingWriter struct {
	out    bytes.Buffer
	writes int
	failAt int
	short  bool
}

func (w *countingWriter) Write(data []byte) (int, error) {
	w.writes++
	if w.failAt > 0 && w.writes >= w.failAt {
		if w.short {
			return 0, nil
		}
		return 0, errRenderWrite
	}
	return w.out.Write(data)
}

func renderScript() []messages.StreamMessage {
	return []messages.StreamMessage{
		{Type: messages.StreamTypeTranscriptStart, Role: messages.RoleUser, Value: messages.NewTranscriptStartValue()},
		{Type: messages.StreamTypeTranscriptDelta, Role: messages.RoleUser, Value: messages.NewTranscriptDeltaValue("heard")},
		{Type: messages.StreamTypeTranscriptDelta, Role: messages.RoleAssistant, Value: messages.NewTranscriptDeltaValue("reply")},
		{Type: messages.StreamTypeTranscriptEnd, Role: messages.RoleUser, Value: messages.NewTranscriptEndValue("heard")},
		{Type: messages.StreamTypeTranscriptEnd, Role: messages.RoleTool, Value: messages.NewTranscriptEndValue("tool said")},
		{Type: messages.StreamTypeTranscriptEnd, Role: messages.RoleAssistant, Value: messages.NewTranscriptEndValue("reply")},
		{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("text")},
		{Type: messages.StreamTypeTextDelta, Role: messages.RoleUser, Value: messages.NewTextDeltaValue("typed")},
		{Type: messages.StreamTypeToolCallEnd, Value: &messages.ToolCallEndValue{Name: "lookup", Arguments: `{ "q": 1 }`}},
		{Type: messages.StreamTypeTranscriptEnd, Role: messages.RoleAssistant, Value: messages.NewTranscriptEndValue("final only")},
		{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("tail")},
		{Type: messages.StreamTypeSessionClose, Value: messages.NewSessionCloseValue("session", "done")},
	}
}

func TestRendererSurfacesEveryWriteFailure(t *testing.T) {
	complete := &countingWriter{}
	renderAll(t, New(complete, nil), nil)
	for failAt := 1; failAt <= complete.writes; failAt++ {
		for _, short := range []bool{false, true} {
			writer := &countingWriter{failAt: failAt, short: short}
			var failed error
			renderAll(t, New(writer, nil), &failed)
			if failed == nil {
				t.Fatalf("write %d (short=%v) failure was not surfaced", failAt, short)
			}
		}
	}
}

func renderAll(t *testing.T, renderer *Renderer, failed *error) {
	t.Helper()
	for _, msg := range renderScript() {
		if err := renderer.WriteMessage(msg); err != nil {
			if failed == nil {
				t.Fatalf("render %s: %v", msg.Type, err)
			}
			*failed = err
			return
		}
	}
	if _, err := renderer.Write([]byte("raw\n")); err != nil {
		if failed == nil {
			t.Fatalf("raw write: %v", err)
		}
		*failed = err
	}
}

func TestRendererIgnoresEmptyAndInvisibleValues(t *testing.T) {
	var out bytes.Buffer
	renderer := New(&out, nil)
	for _, msg := range []messages.StreamMessage{
		{Type: messages.StreamTypeTranscriptDelta, Value: (*messages.TranscriptDeltaValue)(nil)},
		{Type: messages.StreamTypeTranscriptDelta, Value: messages.NewTranscriptDeltaValue("  ")},
		{Type: messages.StreamTypeTextDelta, Value: (*messages.TextDeltaValue)(nil)},
		{Type: messages.StreamTypeToolCallEnd, Value: (*messages.ToolCallEndValue)(nil)},
		{Type: messages.StreamTypeToolCallEnd, Value: &messages.ToolCallEndValue{Arguments: "not json  args"}},
		{Type: messages.StreamTypeAudioDelta, Value: messages.NewAudioDeltaValue([]byte{1})},
		{Type: messages.StreamTypeError, Value: messages.NewNonTerminalErrorValue("late", "response_cancel_not_active")},
	} {
		if err := renderer.WriteMessage(msg); err != nil {
			t.Fatalf("render %s: %v", msg.Type, err)
		}
	}
	if got, want := out.String(), "Tool call: unknown not json args\n"; got != want {
		t.Fatalf("rendered %q, want %q", got, want)
	}
}

func TestUnscopedWriterFormatsSingleMessages(t *testing.T) {
	var out bytes.Buffer
	for _, msg := range []messages.StreamMessage{
		{Type: messages.StreamTypeTranscriptDelta, Value: (*messages.TranscriptDeltaValue)(nil)},
		{Type: messages.StreamTypeTranscriptEnd, Value: (*messages.TranscriptEndValue)(nil)},
		{Type: messages.StreamTypeTranscriptEnd, Role: messages.RoleUser, Value: messages.NewTranscriptEndValue("hello")},
		{Type: messages.StreamTypeSessionClose, Value: (*messages.SessionCloseValue)(nil)},
		{Type: messages.StreamTypeAudioDelta, Value: messages.NewAudioDeltaValue([]byte{1})},
	} {
		if err := Write(&out, msg); err != nil {
			t.Fatalf("write %s: %v", msg.Type, err)
		}
	}
	if got, want := out.String(), "User: hello\n"; got != want {
		t.Fatalf("unscoped output %q, want %q", got, want)
	}
	closeValue := messages.NewSessionCloseValueWithTerminal("session", "closed", "provider_close", messages.TerminalReasonProviderClose, messages.TerminalProvenanceProvider, messages.TerminalOutputComplete)
	if err := Write(&countingWriter{failAt: 1}, messages.StreamMessage{Type: messages.StreamTypeSessionClose, Value: closeValue}); !errors.Is(err, errRenderWrite) {
		t.Fatalf("close write failure = %v", err)
	}
	cause := errors.New("socket reset")
	err := Write(io.Discard, messages.StreamMessage{Type: messages.StreamTypeError, Value: messages.NewErrorValueWithError(cause)})
	if !errors.Is(err, cause) || !strings.HasPrefix(err.Error(), "session error") {
		t.Fatalf("error message = %v, want wrapped session error", err)
	}
	if ErrorFields(nil) != "" {
		t.Fatal("nil error value produced fields")
	}
	if got := ErrorFields(&messages.ErrorValue{ErrorType: "invalid_request_error", Code: "bad"}); !strings.Contains(got, "error_type=invalid_request_error code=bad") {
		t.Fatalf("provider fields = %q", got)
	}
	if ErrInvalidStragglerDrain.Error() == "" {
		t.Fatal("straggler drain sentinel has no message")
	}
	decorated := &streamTerminalError{cause: cause, text: "decorated"}
	if decorated.Error() != "decorated" || !errors.Is(decorated, cause) {
		t.Fatal("decorated terminal error lost its text or cause")
	}
}

type observedMessage struct {
	msg     messages.StreamMessage
	newline bool
}

type recordingObserver struct{ seen []observedMessage }

func (o *recordingObserver) ObserveStreamMessage(msg messages.StreamMessage, newline bool) {
	o.seen = append(o.seen, observedMessage{msg: msg, newline: newline})
}

func TestRendererReportsTerminalEvidenceToObserver(t *testing.T) {
	var out bytes.Buffer
	observer := &recordingObserver{}
	renderer := New(&out, observer)
	for _, msg := range []messages.StreamMessage{
		{Type: messages.StreamTypeTranscriptStart, Role: messages.RoleTool, Value: messages.NewTranscriptStartValue()},
		{Type: messages.StreamTypeTranscriptDelta, Value: messages.NewTranscriptDeltaValue("pending role")},
		{Type: messages.StreamTypeTranscriptDelta, Value: messages.NewTranscriptDeltaValue(" continues")},
		{Type: messages.StreamTypeError, Value: messages.NewNonTerminalErrorValue("late", "response_cancel_not_active")},
		{Type: messages.StreamTypeSessionClose, Value: messages.NewSessionCloseValue("session", "done")},
	} {
		if err := renderer.WriteMessage(msg); err != nil {
			t.Fatalf("render %s: %v", msg.Type, err)
		}
	}
	if got, want := out.String(), "Tool result: pending role continues\n"; got != want {
		t.Fatalf("observed render = %q, want %q", got, want)
	}
	last := observer.seen[len(observer.seen)-1]
	if last.msg.Type != messages.StreamTypeSessionClose || last.newline {
		t.Fatalf("terminal observation = %+v, want close without leading newline", last)
	}
}
