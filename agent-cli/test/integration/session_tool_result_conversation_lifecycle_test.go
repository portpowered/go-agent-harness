package integration

// Story 004 controls for the depth-5 tool-call conversation. These controls
// keep the production session composition and its real executor/result
// boundary intact while proving that unresolved work, missing continuation,
// and unusable response audio cannot look like a successful conversation.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/wavio"
)

func shortConversationFixtureInputs(t *testing.T) (wavPath string, reply []int16) {
	t.Helper()
	samples := make([]int16, audio.FrameSize)
	for index := range samples {
		samples[index] = 7000
	}
	var data bytes.Buffer
	if err := wavio.Write(&data, audio.SampleRate, samples); err != nil {
		t.Fatalf("write short conversation input WAV: %v", err)
	}
	// The input is ephemeral and intentionally lives in the test temp tree;
	// the replay fixture receives its bytes through the normal file-backed
	// audio-in path just like the committed corpus.
	wavPath = filepath.Join(t.TempDir(), "short-tool-result-input.wav")
	if err := os.WriteFile(wavPath, data.Bytes(), 0o600); err != nil {
		t.Fatalf("write short conversation input WAV: %v", err)
	}
	return wavPath, samples
}

// gatedConversationExecutor holds the real tool result before returning it to
// the production ToolRunner. The close control uses the hold to ensure a
// provider terminal event cannot win while the call is unresolved; the
// success control releases the same executor and observes the complete
// accepted-result path.
type gatedConversationExecutor struct {
	mu        sync.Mutex
	result    string
	started   chan struct{}
	release   chan struct{}
	startOnce sync.Once
	calls     []messages.ToolCall
	returned  []string
}

func newGatedConversationExecutor(result string) *gatedConversationExecutor {
	return &gatedConversationExecutor{
		result:  result,
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (e *gatedConversationExecutor) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	e.mu.Lock()
	e.calls = append(e.calls, call)
	e.mu.Unlock()
	e.startOnce.Do(func() { close(e.started) })

	select {
	case <-e.release:
	case <-ctx.Done():
		return messages.ToolCallResponse{}, ctx.Err()
	}

	e.mu.Lock()
	e.returned = append(e.returned, e.result)
	e.mu.Unlock()
	return messages.ToolCallResponse{
		ToolCallID: call.ID,
		Name:       call.Name,
		Content:    e.result,
	}, nil
}

func (e *gatedConversationExecutor) releaseResult() {
	select {
	case <-e.release:
	default:
		close(e.release)
	}
}

func (e *gatedConversationExecutor) snapshot() (calls []messages.ToolCall, returned []string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]messages.ToolCall(nil), e.calls...), append([]string(nil), e.returned...)
}

type conversationRunResult struct {
	stdout     string
	outputPath string
	err        error
}

func waitConversationLifecycleSignal(t *testing.T, signal <-chan struct{}, name string, timeout time.Duration) {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-signal:
	case <-timer.C:
		t.Fatalf("timed out waiting for %s within %s", name, timeout)
	}
}

func assertGatedConversationCall(t *testing.T, executor *gatedConversationExecutor, wantReturned bool) {
	t.Helper()
	calls, returned := executor.snapshot()
	if err := validateExactlyOneToolCall(calls); err != nil {
		t.Fatal(err)
	}
	if wantReturned {
		if len(returned) != 1 || returned[0] != toolResultPositive {
			t.Fatalf("gated executor returned %q, want one exact result %s", returned, toolResultPositive)
		}
		return
	}
	if len(returned) != 0 {
		t.Fatalf("gated executor returned %q before provider close, want no result accepted", returned)
	}
}

func insertConversationProviderCloseBeforeResult(t *testing.T, capture *gwtesting.SessionCapture) {
	t.Helper()
	resultIndex := -1
	for index := range capture.Records {
		if functionCallOutputRecord(t, &capture.Records[index]) {
			resultIndex = index
			break
		}
	}
	if resultIndex < 0 {
		t.Fatal("close-boundary control found no function_call_output gate")
	}
	providerClose := gwtesting.CapturedSessionEvent{
		Direction:   gwtesting.DirectionServerToClient,
		Type:        "session.closed",
		PayloadType: gwtesting.SessionPayloadTypeWebSocketMessage,
		Payload:     json.RawMessage(`{"type":"session.closed","session_id":"sess_tool_result_conversation","reason":"provider_close_while_result_pending"}`),
	}
	records := make([]gwtesting.CapturedSessionEvent, 0, len(capture.Records)+1)
	records = append(records, capture.Records[:resultIndex]...)
	records = append(records, providerClose)
	records = append(records, capture.Records[resultIndex:]...)
	capture.Records = records
}

// removeConversationContinuationAfterResult leaves the provider-facing
// function_call_output as the final expected client frame. With --wait-for-
// close, the real session loop must remain alive until its explicit bound and
// report an incomplete conversation rather than treating the accepted result
// as a final assistant answer.
func removeConversationContinuationAfterResult(t *testing.T, capture *gwtesting.SessionCapture) {
	t.Helper()
	resultSeen := false
	filtered := make([]gwtesting.CapturedSessionEvent, 0, len(capture.Records))
	for _, record := range capture.Records {
		if functionCallOutputRecord(t, &record) {
			resultSeen = true
			filtered = append(filtered, record)
			continue
		}
		if resultSeen && record.Direction == gwtesting.DirectionServerToClient {
			continue
		}
		filtered = append(filtered, record)
	}
	if !resultSeen {
		t.Fatal("missing-continuation control found no function_call_output gate")
	}
	capture.Records = filtered
}

// removeConversationSessionClose removes the terminal provider close so the
// CLI must terminate at the final assistant MESSAGE.END. Without this
// mutation, the replay planner opts into --wait-for-close behavior based on
// the captured close and cannot exercise the default audio stop predicate.
func removeConversationSessionClose(t *testing.T, capture *gwtesting.SessionCapture) {
	t.Helper()
	filtered := make([]gwtesting.CapturedSessionEvent, 0, len(capture.Records))
	removed := false
	for _, record := range capture.Records {
		if record.Direction == gwtesting.DirectionServerToClient && record.Type == "session.closed" {
			removed = true
			continue
		}
		filtered = append(filtered, record)
	}
	if !removed {
		t.Fatal("default audio stop control found no provider session.closed record to remove")
	}
	capture.Records = filtered
}

func removeConversationAudioDelta(t *testing.T, capture *gwtesting.SessionCapture) {
	t.Helper()
	filtered := make([]gwtesting.CapturedSessionEvent, 0, len(capture.Records))
	removed := false
	for _, record := range capture.Records {
		if record.Direction == gwtesting.DirectionServerToClient && record.Type == "response.output_audio.delta" {
			removed = true
			continue
		}
		filtered = append(filtered, record)
	}
	if !removed {
		t.Fatal("missing-audio control found no response.output_audio.delta record")
	}
	capture.Records = filtered
}

func replaceConversationAudioDelta(t *testing.T, capture *gwtesting.SessionCapture, replacement func([]byte) []byte) {
	t.Helper()
	seen := false
	for index := range capture.Records {
		record := &capture.Records[index]
		if record.Direction != gwtesting.DirectionServerToClient || record.Type != "response.output_audio.delta" {
			continue
		}
		payload := conversationPayloadMap(t, record)
		encoded, ok := payload["delta"].(string)
		if !ok {
			t.Fatalf("response.output_audio.delta has non-string delta: %#v", payload["delta"])
		}
		raw, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			t.Fatalf("decode response.output_audio.delta: %v", err)
		}
		payload["delta"] = base64.StdEncoding.EncodeToString(replacement(raw))
		marshalConversationPayload(t, record, payload)
		seen = true
	}
	if !seen {
		t.Fatal("audio control found no response.output_audio.delta record")
	}
}

// conversationAudioFailureKind is deliberately local to the proof. The
// shipped CLI owns audio transport errors; this validator owns the output
// artifact contract and gives each negative control a discriminating cause.
type conversationAudioFailureKind string

const (
	conversationAudioMissing conversationAudioFailureKind = "audio_absence"
	conversationAudioCorrupt conversationAudioFailureKind = "audio_corruption"
	conversationAudioSignal  conversationAudioFailureKind = "audio_signal"
)

type conversationAudioValidationError struct {
	kind   conversationAudioFailureKind
	path   string
	detail string
}

func (e *conversationAudioValidationError) Error() string {
	return fmt.Sprintf("conversation response audio rejected (%s) for %q: %s", e.kind, e.path, e.detail)
}

func validateConversationAudioArtifact(path string, wantSamples int) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &conversationAudioValidationError{kind: conversationAudioMissing, path: path, detail: "response WAV was not created"}
		}
		return &conversationAudioValidationError{kind: conversationAudioCorrupt, path: path, detail: err.Error()}
	}
	if len(data) == 0 {
		return &conversationAudioValidationError{kind: conversationAudioMissing, path: path, detail: "response WAV is empty"}
	}
	rate, samples, err := wavio.Read(bytes.NewReader(data))
	if err != nil {
		return &conversationAudioValidationError{kind: conversationAudioCorrupt, path: path, detail: err.Error()}
	}
	if rate != audio.SampleRate {
		return &conversationAudioValidationError{kind: conversationAudioCorrupt, path: path, detail: fmt.Sprintf("sample rate %d; want %d", rate, audio.SampleRate)}
	}
	if len(samples) < wantSamples/2 || len(samples) > wantSamples*2 {
		return &conversationAudioValidationError{kind: conversationAudioCorrupt, path: path, detail: fmt.Sprintf("sample count %d outside [%d,%d]", len(samples), wantSamples/2, wantSamples*2)}
	}
	var energy float64
	for _, sample := range samples {
		energy += float64(sample) * float64(sample)
	}
	if math.Sqrt(energy/float64(len(samples))) <= 500 {
		return &conversationAudioValidationError{kind: conversationAudioSignal, path: path, detail: "response WAV has no audible signal"}
	}
	return nil
}

func assertConversationAcceptedExchange(t *testing.T, stdout, wirePath string, executor *conversationResultExecutor) {
	t.Helper()
	assertConversationOneCall(t, executor)
	outputs := functionCallOutputsInExchange(t, wirePath)
	if len(outputs) != 1 || outputs[0].CallID != toolConversationCallID || outputs[0].Output != toolResultPositive {
		t.Fatalf("accepted exchange = %v, want one exact result for %q", outputs, toolConversationCallID)
	}
	if err := transcriptReflectionError(stdout); err != nil {
		t.Fatalf("accepted exchange transcript missing result-grounded facts: %v\nstdout=%q", err, stdout)
	}
}

func TestSessionToolResultConversationCloseBoundaryRequiresAcceptedResult(t *testing.T) {
	t.Run("provider close while unresolved", func(t *testing.T) {
		wavPath, reply := shortConversationFixtureInputs(t)
		_, wirePath := buildConversationControlFixtureFromInputs(t, wavPath, reply, func(capture *gwtesting.SessionCapture) {
			insertConversationProviderCloseBeforeResult(t, capture)
		})
		executor := newGatedConversationExecutor(toolResultPositive)
		defer executor.releaseResult()

		runResult := make(chan conversationRunResult, 1)
		started := time.Now()
		go func() {
			stdout, outputPath, runErr := runToolResultConversation(t, wavPath, wirePath, executor)
			runResult <- conversationRunResult{stdout: stdout, outputPath: outputPath, err: runErr}
		}()
		waitConversationLifecycleSignal(t, executor.started, "tool executor before provider close", 3*time.Second)

		var result conversationRunResult
		select {
		case result = <-runResult:
		case <-time.After(3 * time.Second):
			t.Fatal("provider close did not terminate the unresolved conversation")
		}
		if result.err == nil {
			t.Fatalf("provider close returned clean success while %q was unresolved; stdout=%q", toolConversationCallID, result.stdout)
		}
		if !errors.Is(result.err, services.ErrSessionUnresolvedToolResults) {
			t.Fatalf("provider-close error = %v, want ErrSessionUnresolvedToolResults", result.err)
		}
		if !strings.Contains(result.err.Error(), toolConversationCallID) {
			t.Fatalf("provider-close error = %v, want unresolved call ID %q", result.err, toolConversationCallID)
		}
		if elapsed := time.Since(started); elapsed > 5*time.Second {
			t.Fatalf("provider-close control took %s; unresolved work must fail at the close boundary", elapsed)
		}
		assertGatedConversationCall(t, executor, false)
		if strings.Contains(result.stdout, "24 degrees") || strings.Contains(result.stdout, "clear skies") {
			t.Fatalf("provider-close control leaked follow-up speech after unresolved close:\n%s", result.stdout)
		}
	})

	t.Run("accepted result then completion", func(t *testing.T) {
		wavPath, reply := shortConversationFixtureInputs(t)
		executor := newGatedConversationExecutor(toolResultPositive)
		defer executor.releaseResult()
		wirePath := buildToolResultConversationFixture(t, wavPath, reply, toolResultPositive, true)

		runResult := make(chan conversationRunResult, 1)
		go func() {
			stdout, outputPath, runErr := runToolResultConversation(t, wavPath, wirePath, executor)
			runResult <- conversationRunResult{stdout: stdout, outputPath: outputPath, err: runErr}
		}()
		waitConversationLifecycleSignal(t, executor.started, "tool executor before accepted result", 3*time.Second)
		if calls, returned := executor.snapshot(); len(calls) != 1 || len(returned) != 0 {
			t.Fatalf("conversation advanced before result release: calls=%d returned=%d", len(calls), len(returned))
		}
		executor.releaseResult()

		var result conversationRunResult
		select {
		case result = <-runResult:
		case <-time.After(5 * time.Second):
			t.Fatal("conversation did not finish after accepted result")
		}
		if result.err != nil {
			t.Fatalf("accepted-result conversation failed: %v\nstdout=%s", result.err, result.stdout)
		}
		assertGatedConversationCall(t, executor, true)
		outputs := functionCallOutputsInExchange(t, wirePath)
		if len(outputs) != 1 || outputs[0].CallID != toolConversationCallID || outputs[0].Output != toolResultPositive {
			t.Fatalf("accepted-result exchange = %v, want one exact result for %q", outputs, toolConversationCallID)
		}
		if err := transcriptReflectionError(result.stdout); err != nil {
			t.Fatalf("accepted-result transcript is not grounded: %v", err)
		}
		if err := validateConversationAudioArtifact(result.outputPath, len(reply)); err != nil {
			t.Fatalf("accepted-result audio rejected: %v", err)
		}
	})
}

func TestSessionToolResultConversationContinuesWithoutProviderCloseShortcut(t *testing.T) {
	wavPath, reply := shortConversationFixtureInputs(t)
	_, wirePath := buildConversationControlFixtureFromInputs(t, wavPath, reply, func(capture *gwtesting.SessionCapture) {
		removeConversationSessionClose(t, capture)
	})

	capture, err := gwtesting.LoadSessionCapture(wirePath)
	if err != nil {
		t.Fatalf("load no-close replay fixture: %v", err)
	}
	for _, record := range capture.Records {
		if record.Direction == gwtesting.DirectionServerToClient && record.Type == "session.closed" {
			t.Fatal("default audio stop control still contains a provider session.closed record")
		}
	}

	executor := &conversationResultExecutor{result: toolResultPositive}
	stdout, outputPath, runErr := runToolResultConversationWithOptions(t, wavPath, wirePath, executor, 8*time.Second, 10*time.Second, false)
	if runErr != nil {
		t.Fatalf("default audio stop path failed before consuming the follow-up response: %v\nstdout=%s", runErr, stdout)
	}
	assertConversationAcceptedExchange(t, stdout, wirePath, executor)
	outputs := functionCallOutputsInExchange(t, wirePath)
	if len(outputs) != 1 {
		t.Fatalf("default audio stop exchange contains %d function_call_output events, want exactly one", len(outputs))
	}
	assertToolResultFollowUpOrdering(t, wirePath, outputs[0].Sequence)
	if err := validateConversationAudioArtifact(outputPath, len(reply)); err != nil {
		t.Fatalf("default audio stop follow-up audio rejected: %v", err)
	}
}

func TestSessionToolResultConversationMissingContinuationIsBounded(t *testing.T) {
	wavPath, reply := shortConversationFixtureInputs(t)
	_, wirePath := buildConversationControlFixtureFromInputs(t, wavPath, reply, func(capture *gwtesting.SessionCapture) {
		removeConversationContinuationAfterResult(t, capture)
	})
	executor := &conversationResultExecutor{result: toolResultPositive}
	started := time.Now()
	stdout, _, runErr := runToolResultConversationWithWaitForCloseAndBounds(t, wavPath, wirePath, executor, 350*time.Millisecond, 2*time.Second)
	elapsed := time.Since(started)
	if runErr == nil {
		t.Fatalf("missing-continuation control completed cleanly after accepting a result; stdout=%q", stdout)
	}
	if !errors.Is(runErr, services.ErrSessionAudioResponseIncomplete) {
		t.Fatalf("missing-continuation error = %v, want ErrSessionAudioResponseIncomplete", runErr)
	}
	if elapsed > 1500*time.Millisecond {
		t.Fatalf("missing-continuation control took %s, want bounded 350ms liveness failure", elapsed)
	}
	assertConversationOneCall(t, executor)
	outputs := functionCallOutputsInExchange(t, wirePath)
	if len(outputs) != 1 || outputs[0].CallID != toolConversationCallID || outputs[0].Output != toolResultPositive {
		t.Fatalf("missing-continuation result exchange = %v, want one accepted exact result", outputs)
	}
	if strings.Contains(stdout, "24 degrees") || strings.Contains(stdout, "clear skies") {
		t.Fatalf("missing-continuation control leaked follow-up transcript:\n%s", stdout)
	}
}

func TestSessionToolResultConversationAudioAbsenceAndSignalControls(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(*testing.T, *gwtesting.SessionCapture)
		wantKind conversationAudioFailureKind
	}{
		{name: "missing response delta", mutate: removeConversationAudioDelta, wantKind: conversationAudioMissing},
		{name: "silent response signal", mutate: func(t *testing.T, capture *gwtesting.SessionCapture) {
			replaceConversationAudioDelta(t, capture, func(raw []byte) []byte { return make([]byte, len(raw)) })
		}, wantKind: conversationAudioSignal},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			wavPath, reply := shortConversationFixtureInputs(t)
			_, wirePath := buildConversationControlFixtureFromInputs(t, wavPath, reply, func(capture *gwtesting.SessionCapture) {
				testCase.mutate(t, capture)
			})
			executor := &conversationResultExecutor{result: toolResultPositive}
			stdout, outputPath, runErr := runToolResultConversation(t, wavPath, wirePath, executor)
			if runErr != nil {
				t.Fatalf("%s control failed before output-artifact validation: %v\nstdout=%s", testCase.name, runErr, stdout)
			}
			assertConversationAcceptedExchange(t, stdout, wirePath, executor)
			validationErr := validateConversationAudioArtifact(outputPath, len(reply))
			if validationErr == nil {
				t.Fatalf("%s control passed the response-audio contract", testCase.name)
			}
			var typedErr *conversationAudioValidationError
			if !errors.As(validationErr, &typedErr) || typedErr.kind != testCase.wantKind {
				t.Fatalf("%s control error = %v, want audio category %q", testCase.name, validationErr, testCase.wantKind)
			}
			if strings.Contains(validationErr.Error(), "tool result") || strings.Contains(validationErr.Error(), "replay") {
				t.Fatalf("%s audio diagnostic was contaminated by a tool/result failure: %v", testCase.name, validationErr)
			}
			t.Logf("%s control rejected as expected: %v", testCase.name, validationErr)
		})
	}
}

func TestSessionToolResultConversationCorruptAudioDeltaIsRejected(t *testing.T) {
	wavPath, reply := shortConversationFixtureInputs(t)
	_, wirePath := buildConversationControlFixtureFromInputs(t, wavPath, reply, func(capture *gwtesting.SessionCapture) {
		replaceConversationAudioDelta(t, capture, func([]byte) []byte { return []byte{0} })
	})
	executor := &conversationResultExecutor{result: toolResultPositive}
	stdout, _, runErr := runToolResultConversation(t, wavPath, wirePath, executor)
	if runErr == nil {
		t.Fatalf("corrupt-audio control completed cleanly; odd PCM16 delta was accepted\nstdout=%s", stdout)
	}
	if !strings.Contains(runErr.Error(), "PCM16 audio delta has odd byte length") {
		t.Fatalf("corrupt-audio error = %v, want the audio PCM boundary diagnostic", runErr)
	}
	if errors.Is(runErr, services.ErrSessionUnresolvedToolResults) || strings.Contains(runErr.Error(), "replay mismatch") {
		t.Fatalf("corrupt-audio control was reported as a tool/result transport failure: %v", runErr)
	}
	assertConversationOneCall(t, executor)
	outputs := functionCallOutputsInExchange(t, wirePath)
	if len(outputs) != 1 || outputs[0].CallID != toolConversationCallID || outputs[0].Output != toolResultPositive {
		t.Fatalf("corrupt-audio result exchange = %v, want one accepted exact result", outputs)
	}
	if err := transcriptReflectionError(stdout); err != nil {
		t.Fatalf("corrupt-audio control lost the otherwise valid grounded transcript: %v", err)
	}
}
