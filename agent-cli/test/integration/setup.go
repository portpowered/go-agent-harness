package integration

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

// testWriter captures stdout and stderr for assertion in tests.
type testWriter struct {
	stdout bytes.Buffer
	stderr bytes.Buffer
}

func (w *testWriter) Stdout() io.Writer {
	return &w.stdout
}

func (w *testWriter) Stderr() io.Writer {
	return &w.stderr
}

func (w *testWriter) StdoutString() string {
	return w.stdout.String()
}

func (w *testWriter) StderrString() string {
	return w.stderr.String()
}

func (w *testWriter) Reset() {
	w.stdout.Reset()
	w.stderr.Reset()
}

// NewTestWriter returns a writer that captures stdout and stderr.
func NewTestWriter() *testWriter {
	return &testWriter{
		stdout: bytes.Buffer{},
		stderr: bytes.Buffer{},
	}
}

// Realtime wire vocabulary shared by the hermetic provider fixtures.
const (
	rtEventSessionUpdate              = "session.update"
	rtEventSessionCreated             = "session.created"
	rtEventSessionClosed              = "session.closed"
	rtEventInputAudioAppend           = "input_audio_buffer.append"
	rtEventInputAudioCommit           = "input_audio_buffer.commit"
	rtEventConversationItemCreate     = "conversation.item.create"
	rtEventResponseCreate             = "response.create"
	rtEventResponseCreated            = "response.created"
	rtEventResponseCancel             = "response.cancel"
	rtEventResponseDone               = "response.done"
	rtEventOutputItemAdded            = "response.output_item.added"
	rtEventOutputAudioDelta           = "response.output_audio.delta"
	rtEventOutputAudioTranscriptDelta = "response.output_audio_transcript.delta"
	rtEventOutputTextDelta            = "response.output_text.delta"
	rtEventOutputTextDone             = "response.output_text.done"
	rtEventFunctionCallArgumentsDone  = "response.function_call_arguments.done"
	rtItemMessage                     = "message"
	rtItemFunctionCall                = "function_call"
	rtItemFunctionCallOutput          = "function_call_output"
	rtContentInputImage               = "input_image"
	rtRoleUser                        = "user"
	rtStatusCompleted                 = "completed"
	rtStatusCancelled                 = "cancelled"
	rtStatusFailed                    = "failed"
	rtAuthorizationHeader             = "Bearer hermetic-key"
	rtToolReadImage                   = "read_image"
	rtPlainResponseID                 = "response-plain-1"
)

// Transcript phrases used by the record/replay diagnosis fixture.
const (
	replayHeardPrefix     = "heard "
	replayHeardClearly    = "heard clearly"
	replayAnsweringPrefix = "answering "
	replayAnsweringNow    = "answering now"
	// replayFixtureFileMode keeps generated replay fixtures private to the test user.
	replayFixtureFileMode = 0o600
)

// testReporter is the subset of testing.TB the shared helpers need; keeping
// it an interface keeps this non-test file free of the testing import.
type testReporter interface {
	Helper()
	Errorf(format string, args ...any)
	Fatalf(format string, args ...any)
	TempDir() string
}

// closeForTest closes a resource whose close must succeed and reports a
// failure through the test instead of silently dropping it.
func closeForTest(t testReporter, closer io.Closer) {
	t.Helper()
	if err := closer.Close(); err != nil {
		t.Errorf("close %T: %v", closer, err)
	}
}

// discardCloseError closes a resource on a teardown path whose outcome is
// already decided (peer already hung up, connection abandoned). The close
// error cannot change the observation the test asserts on.
func discardCloseError(closer io.Closer) {
	if err := closer.Close(); err != nil {
		return
	}
}

// writeSSEEvents streams server-sent events from a fake provider handler,
// flushing after each frame. A write failure means the client went away, so
// the handler stops streaming; the client-side assertions report the outcome.
func writeSSEEvents(w http.ResponseWriter, events []string) {
	flusher, canFlush := w.(http.Flusher)
	for _, event := range events {
		if _, err := w.Write([]byte(event + "\n\n")); err != nil {
			return
		}
		if canFlush {
			flusher.Flush()
		}
	}
}

// mustAs asserts a decoded fixture value has type T and fails the test with
// the actual dynamic type when it does not.
func mustAs[T any](t testReporter, value any) T {
	t.Helper()
	typed, ok := value.(T)
	if !ok {
		var want T
		t.Fatalf("value %v has type %T, want %T", value, value, want)
	}
	return typed
}

// mustMarshalFixture encodes a static fixture value for helpers that have no
// test handle. Fixture values are plain maps, slices, and strings, so an
// encoding failure is a programming error in the fixture and panics loudly.
func mustMarshalFixture(value any) []byte {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Sprintf("marshal fixture %T: %v", value, err))
	}
	return encoded
}

// killForCleanup stops a child process on a teardown or failure path. The
// process may already have exited, in which case the kill error is expected
// and the test's own assertions carry the outcome.
func killForCleanup(process *os.Process) {
	if err := process.Kill(); err != nil {
		return
	}
}

// strictlyIncreasing reports whether values are in strictly ascending order,
// the shape of every wire-ordering assertion in these fixtures.
func strictlyIncreasing[T cmp.Ordered](values ...T) bool {
	for index := 1; index < len(values); index++ {
		if values[index-1] >= values[index] {
			return false
		}
	}
	return true
}

// mustJSON marshals a fixture value and fails the test when it cannot be
// encoded, so a malformed fixture never produces an empty wire frame.
func mustJSON(t testReporter, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal %T: %v", value, err)
	}
	return encoded
}

// writeReplayCaptureFixture writes a synthesized capture into a temporary
// file and requires the strict replay dialer to accept it.
func writeReplayCaptureFixture(t testReporter, capture gwtesting.SessionCapture, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	data, err := json.MarshalIndent(capture, "", "  ")
	if err != nil {
		t.Fatalf("marshal replay fixture %s: %v", name, err)
	}
	if err := os.WriteFile(path, data, replayFixtureFileMode); err != nil {
		t.Fatalf("write replay fixture %s: %v", name, err)
	}
	if _, err := gwtesting.NewReplayWebSocketDialer(path); err != nil {
		t.Fatalf("replay fixture %s rejected by the session replayer dialer: %v", name, err)
	}
	return path
}

func containsTimeline(timeline []string, want string) bool {
	return indexOfTimeline(timeline, want, 0) >= 0
}

func countTimeline(timeline []string, want string) int {
	count := 0
	for _, event := range timeline {
		if event == want {
			count++
		}
	}
	return count
}

func indexOfTimeline(timeline []string, want string, occurrence int) int {
	seen := 0
	for index, event := range timeline {
		if event != want {
			continue
		}
		if seen == occurrence {
			return index
		}
		seen++
	}
	return -1
}

func audioLengths(audio [][]byte) []int {
	lengths := make([]int, len(audio))
	for index, data := range audio {
		lengths[index] = len(data)
	}
	return lengths
}

func scheduledAppendRange(timeline []string, start, end int) (first, count int) {
	first = -1
	for index := start; index < end; index++ {
		if timeline[index] == "out:input_audio_buffer.append" {
			if first < 0 {
				first = index
			}
			count++
		}
	}
	return first, count
}

func readScheduledWAVSamples(t *testing.T, path string) []int16 {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read scheduled WAV %q: %v", path, err)
	}
	_, samples, err := wavio.Read(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode scheduled WAV %q: %v", path, err)
	}
	return samples
}

// scheduledSessionUpdate is the session.update subset the scheduled-boundary
// tests inspect.
type scheduledSessionUpdate struct {
	Instructions string `json:"instructions"`
	Tools        []struct {
		Name string `json:"name"`
	} `json:"tools"`
}

func scheduledUpdatesAdvertiseToolsWithoutInstructions(updates []json.RawMessage) bool {
	for _, raw := range updates {
		var update scheduledSessionUpdate
		if json.Unmarshal(raw, &update) == nil && update.Instructions == "" && len(update.Tools) > 0 {
			return true
		}
	}
	return false
}
