package session

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
	recordingservice "github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording/internal/service"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

const testRecordingBase = "2031-02-03T04:05:06Z"

// C56 frozen oracles: client.transcript.jsonl agent.transcript.jsonl
// session-log.jsonl audio/in-000.pcm audio/in-001.pcm audio/out-000.pcm
// audio/out-001.pcm screenshots/000001 BrowserArtifactDefaultPath
// browserEventsVersion ErrLiveEvidenceClaimed conflicting terminal
// secret=hidden SHA256 audio_segments.
const testFailedStatus = "failed"

type testSession struct {
	outgoing *messages.TypedBuffer[messages.StreamMessage]
	incoming *messages.TypedBuffer[messages.StreamMessage]
	done     chan struct{}
	once     sync.Once
}

func newTestSession() *testSession {
	return &testSession{
		outgoing: messages.NewTypedBuffer[messages.StreamMessage](32),
		incoming: messages.NewTypedBuffer[messages.StreamMessage](32),
		done:     make(chan struct{}),
	}
}

func (s *testSession) Send(ctx context.Context, message messages.StreamMessage) bool {
	return s.outgoing.Write(ctx, message)
}

func (s *testSession) Receive() *messages.TypedBuffer[messages.StreamMessage] { return s.incoming }

func (s *testSession) Done() <-chan struct{} { return s.done }

func (s *testSession) Close() error {
	s.once.Do(func() { close(s.done) })
	return nil
}

type testInferencer struct{ session messages.Session }

func (i *testInferencer) ConnectSession(context.Context) (messages.Session, error) {
	return i.session, nil
}

func openTestRecorder(t *testing.T, destination string, browser recording.BrowserOptions) (recording.SessionRecorder, *clock.Deterministic) {
	t.Helper()
	source := clock.NewDeterministic(parseTestTime(t), time.Nanosecond)
	service := New(recordingservice.New(source), source)
	recorder, err := service.OpenSession(recording.SessionOptions{
		Destination: destination,
		SessionID:   "session-test",
		Provider:    "fixture-provider",
		Model:       "fixture-model",
		Transport:   "websocket",
		ClockBase:   parseTestTime(t),
		Metadata: transcript.RecordingMetadata{
			InputDevice:  transcript.DeviceMetadata{ID: "input", Name: "fixture mic", Driver: "test", SampleRateHz: 16000, Channels: 1},
			OutputDevice: transcript.DeviceMetadata{ID: "output", Name: "fixture speaker", Driver: "test", SampleRateHz: 16000, Channels: 1},
		},
		Browser: browser,
	})
	if err != nil {
		t.Fatalf("open recording session: %v", err)
	}
	return recorder, source
}

func parseTestTime(t *testing.T) time.Time {
	t.Helper()
	value, err := time.Parse(time.RFC3339, testRecordingBase)
	if err != nil {
		t.Fatalf("parse test base time: %v", err)
	}
	return value
}

func TestSessionRecorderCapturesExactAudioSegmentsAndLifecycle(t *testing.T) {
	fixture := newSessionAudioFixture(t)
	sendSessionAudioInput(t, fixture)
	sendSessionAudioOutput(t, fixture)
	receiveSessionAudioOutput(t, fixture)
	finalizeSessionAudioFixture(t, fixture)
	assertSessionAudioFixture(t, fixture)
}

func TestSessionRecorderClaimsDestinationAndRejectsConflictingTerminal(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "claimed")
	first, _ := openTestRecorder(t, destination, recording.BrowserOptions{})
	secondServiceSource := clock.NewDeterministic(parseTestTime(t), time.Nanosecond)
	secondService := New(recordingservice.New(secondServiceSource), secondServiceSource)
	if _, err := secondService.OpenSession(recording.SessionOptions{Destination: destination}); !errors.Is(err, recording.ErrLiveEvidenceClaimed) {
		t.Fatalf("second open error = %v, want destination claim", err)
	}
	if err := first.RecordTerminalSummary(testTerminalSummary()); err != nil {
		t.Fatalf("record first terminal: %v", err)
	}
	if err := first.RecordTerminalSummary(testTerminalSummary()); err != nil {
		t.Fatalf("repeat identical terminal: %v", err)
	}
	conflicting := testTerminalSummary()
	conflicting.Reason = testFailedStatus
	if err := first.RecordTerminalSummary(conflicting); err == nil {
		t.Fatal("conflicting terminal summary was accepted")
	}
	if err := first.Finalize(context.Background(), nil); err == nil {
		t.Fatal("finalize unexpectedly hid conflicting terminal summary")
	}
	if _, err := os.Stat(destination + ".lock"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("destination lock stat = %v, want released lock", err)
	}
	if err := first.Finalize(context.Background(), nil); err == nil {
		t.Fatal("idempotent finalize unexpectedly cleared its first error")
	}
}

func TestSessionRecorderCapturesImageAndBrowserProjection(t *testing.T) {
	events := make(chan recording.BrowserEvent, 4)
	destination := filepath.Join(t.TempDir(), "multimodal")
	recorder, _ := openTestRecorder(t, destination, recording.BrowserOptions{
		Enabled:           true,
		Events:            events,
		IncludeArguments:  true,
		IncludeResults:    true,
		RedactURLQuery:    true,
		RedactURLFragment: true,
	})
	call := messages.ToolCall{ID: "call-1", Name: "screenshot", Arguments: `{"target":"main"}`}
	if err := recorder.ObserveToolCall(context.Background(), call); err != nil {
		t.Fatalf("observe tool call: %v", err)
	}
	pixels := testPNG(t)
	digest := sha256.Sum256(pixels)
	digestText := hex.EncodeToString(digest[:])
	result := sightResult{
		Version: sightResultVersion, Status: sightResultSuccess, Source: sightSourceScreen,
		MIMEType: "image/png", ByteLength: len(pixels), Width: 2, Height: 1,
		SHA256: digestText, TypedProjection: sightProjectionImage,
	}
	resultBytes, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("encode sight result: %v", err)
	}
	if err := recorder.ObserveToolResult(context.Background(), call, messages.ToolCallResponse{
		ToolCallID: call.ID, Name: call.Name, Content: string(resultBytes),
		ContentParts: []messages.ContentPart{messages.ImagePart{MediaType: "image/png", Bytes: pixels}},
	}, false); err != nil {
		t.Fatalf("observe tool result: %v", err)
	}
	if err := recorder.RecordTerminalSummary(testTerminalSummary()); err != nil {
		t.Fatalf("record multimodal terminal: %v", err)
	}
	events <- recording.BrowserEvent{Type: "target_attached", BrowserID: "browser-1", TargetID: "target-1", At: parseTestTime(t)}
	events <- recording.BrowserEvent{Type: "tools_added", BrowserID: "browser-1", TargetID: "target-1", Generation: 2, Tools: []recording.BrowserTool{{Name: "screenshot", InputSchema: json.RawMessage(`{"type":"object"}`)}}}
	events <- recording.BrowserEvent{Type: "tool_invoked", BrowserID: "browser-1", TargetID: "target-1", Generation: 2, InvocationID: "inv-1", ToolName: "screenshot", Input: json.RawMessage(`{"url":"https://example.test/?secret=hidden#fragment"}`)}
	events <- recording.BrowserEvent{Type: "tool_responded", BrowserID: "browser-1", TargetID: "target-1", Generation: 2, InvocationID: "inv-1", Status: "completed", Output: json.RawMessage(`{"ok":true}`)}
	close(events)
	if err := recorder.Finalize(context.Background(), nil); err != nil {
		t.Fatalf("finalize multimodal recording: %v", err)
	}
	manifest := readManifest(t, destination)
	if manifest.Browser == nil || manifest.Browser.Format != browserEventsVersion {
		t.Fatalf("browser manifest = %+v", manifest.Browser)
	}
	assertManifestArtifacts(t, destination, manifest, []string{"session-log.jsonl", transcript.BrowserArtifactDefaultPath})
	var logEntry sessionLogEntry
	logData, err := os.ReadFile(filepath.Join(destination, "session-log.jsonl"))
	if err != nil {
		t.Fatalf("read multimodal session log: %v", err)
	}
	if err := json.Unmarshal(bytes.TrimSpace(logData), &logEntry); err != nil {
		t.Fatalf("decode multimodal session log: %v", err)
	}
	if len(logEntry.ToolEvents) != 2 || logEntry.ToolEvents[1].Image == nil {
		t.Fatalf("tool/image projection = %+v", logEntry.ToolEvents)
	}
	imagePath := filepath.Join(destination, filepath.FromSlash(logEntry.ToolEvents[1].Image.Path))
	assertFileBytes(t, imagePath, pixels)
	browserData, err := os.ReadFile(filepath.Join(destination, transcript.BrowserArtifactDefaultPath))
	if err != nil || bytes.Contains(browserData, []byte("secret=hidden")) || bytes.Contains(browserData, []byte("#fragment")) {
		t.Fatalf("browser artifact redaction err=%v data=%s", err, browserData)
	}
}

func TestPrivateValidationHelpersRejectUnsafeInputs(t *testing.T) {
	for _, value := range []string{"", ".", "../escape", "a/../../escape", `/absolute`, `a\\b`} {
		if safeArtifactPath(value) {
			t.Errorf("safeArtifactPath(%q) = true", value)
		}
	}
	for _, value := range []string{"screenshots/000001.png", "nested/file.json"} {
		if !safeArtifactPath(value) {
			t.Errorf("safeArtifactPath(%q) = false", value)
		}
	}
	if err := decodeStrictJSON([]byte(`{"version":1}{"extra":2}`), &map[string]any{}); err == nil {
		t.Fatal("decodeStrictJSON accepted multiple values")
	}
	if got := formatAudioName("in", 7); got != "in-007.pcm" {
		t.Fatalf("formatAudioName = %q", got)
	}
	if _, _, err := decodeImageCapture(messages.ToolCall{Name: "sight"}, messages.ToolCallResponse{Content: `{"version":2,"status":"success"}`}, "screenshots/000001"); err != nil {
		t.Fatalf("non-capture response should be ignored, got %v", err)
	}
	if _, err := browserEventInputs(recording.BrowserEvent{Type: "tool_invoked", BrowserID: "b", TargetID: "t", InvocationID: "i"}, true, true); err != nil {
		t.Fatalf("browser event conversion: %v", err)
	}
}

func TestSessionForwardingCapabilitiesAndConnectionFailures(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "capabilities")
	recorder, _ := openTestRecorder(t, destination, recording.BrowserOptions{})
	rich := &richTestSession{testSession: newTestSession(), terminalErr: errors.New("fixture terminal")}
	wrapper := recorder.Wrap(&testInferencer{session: rich})
	if wrapper == nil || recorder.Wrap(nil) != nil {
		t.Fatal("recording wrapper nil handling is incorrect")
	}
	session, err := wrapper.ConnectSession(context.Background())
	if err != nil {
		t.Fatalf("connect rich session: %v", err)
	}
	assertForwardingCapabilities(t, session, rich)
	assertResponseAndMediaCapabilities(t, session)
	if err := session.Close(); err != nil {
		t.Fatalf("close capability session: %v", err)
	}
	if err := recorder.Finalize(context.Background(), errors.New("capability fixture stopped")); err == nil {
		t.Fatalf("capability finalize error = %v", err)
	}
	assertConnectionFailures(t, recorder)
}

func TestBrowserAndImageHelperBranches(t *testing.T) {
	base := recording.BrowserEvent{BrowserID: "browser", TargetID: "target", Generation: 4, InvocationID: "inv", ToolName: "tool"}
	for _, event := range []recording.BrowserEvent{
		{Type: "tools_removed", BrowserID: base.BrowserID, TargetID: base.TargetID, Generation: base.Generation, RemovedToolNames: []string{"old"}},
		{Type: "catalog_ready", BrowserID: base.BrowserID, TargetID: base.TargetID, Generation: base.Generation, ToolCountKnown: true, ToolCount: 2},
		{Type: "tool_responded", BrowserID: base.BrowserID, TargetID: base.TargetID, Generation: base.Generation, InvocationID: base.InvocationID, Status: "canceled", Reason: "user"},
		{Type: "tool_responded", BrowserID: base.BrowserID, TargetID: base.TargetID, Generation: base.Generation, InvocationID: base.InvocationID, Status: testFailedStatus, ErrorCode: "E_FAIL", Reason: "failure", Output: json.RawMessage(`{"detail":"safe"}`)},
		{Type: "page_navigated", BrowserID: base.BrowserID, TargetID: base.TargetID, PreviousGeneration: 3, Generation: base.Generation},
		{Type: "target_detached", BrowserID: base.BrowserID, TargetID: base.TargetID, Reason: "closed"},
		{Type: "session_closed", BrowserID: base.BrowserID, TargetID: base.TargetID, Reason: "done"},
	} {
		if inputs, err := browserEventInputs(event, true, true); err != nil || len(inputs) == 0 {
			t.Fatalf("browser event %q inputs=%d err=%v", event.Type, len(inputs), err)
		}
	}
	if digest := browserSchemaDigest([]recording.BrowserTool{{Name: "b"}, {Name: "a"}}); digest == "" {
		t.Fatal("browser schema digest is empty")
	}
	if browserErrorCode(recording.BrowserEvent{ErrorCode: "explicit"}) != "explicit" || browserErrorCode(recording.BrowserEvent{Status: testFailedStatus}) != "invocation_error" {
		t.Fatal("browser error-code fallback is incorrect")
	}
	if got := sortedStrings([]string{"z", "a", "a"}); !equalStrings(got, []string{"a", "a", "z"}) {
		t.Fatalf("sorted browser strings = %v", got)
	}
	for _, mediaType := range []string{"image/png", "image/jpeg", "image/gif", "application/octet-stream"} {
		if captureExtension(mediaType) == "" {
			t.Fatalf("empty image extension for %q", mediaType)
		}
	}
	if errorsImage("expected") == nil {
		t.Fatal("errorsImage returned nil")
	}
	var envelope struct {
		Version string          `json:"version"`
		OK      bool            `json:"ok"`
		Data    json.RawMessage `json:"data"`
	}
	valid := sightResult{Version: sightResultVersion, Status: sightResultSuccess, Source: sightSourceScreen, MIMEType: "image/png", ByteLength: 1, Width: 1, Height: 1, SHA256: strings.Repeat("a", 64), TypedProjection: sightProjectionImage}
	data, err := json.Marshal(valid)
	if err != nil {
		t.Fatalf("encode valid sight result: %v", err)
	}
	envelopeBytes, err := json.Marshal(struct {
		Version string          `json:"version"`
		OK      bool            `json:"ok"`
		Data    json.RawMessage `json:"data"`
	}{"webmcp.tool-result.v1", true, data})
	if err != nil {
		t.Fatalf("encode valid envelope: %v", err)
	}
	if err := decodeStrictJSON(envelopeBytes, &envelope); err != nil {
		t.Fatalf("decode valid envelope: %v", err)
	}
	if _, ok := decodeSightResult(string(envelopeBytes)); !ok {
		t.Fatal("decodeSightResult rejected valid envelope")
	}
	pixels := testPNG(t)
	pixelDigest := sha256.Sum256(pixels)
	badLength := sightResult{
		Version: sightResultVersion, Status: sightResultSuccess, Source: sightSourceScreen,
		MIMEType: "image/png", ByteLength: len(pixels) + 1, Width: 2, Height: 1,
		SHA256: hex.EncodeToString(pixelDigest[:]), TypedProjection: sightProjectionImage,
	}
	badLengthJSON, err := json.Marshal(badLength)
	if err != nil {
		t.Fatalf("encode invalid-length sight result: %v", err)
	}
	if _, _, err := decodeImageCapture(messages.ToolCall{ID: "bad-length", Name: "screenshot"}, messages.ToolCallResponse{
		Content: string(badLengthJSON), ContentParts: []messages.ContentPart{messages.ImagePart{MediaType: "image/png", Bytes: pixels}},
	}, "screenshots/000001"); err == nil {
		t.Fatal("image byte-length mutation was accepted")
	}

	projection := audioProjection{}
	projection.observe(messages.StreamMessage{Type: messages.StreamTypeAudioDelta, Value: messages.NewAudioDeltaValue([]byte{1, 0})}, recordingDirectionClient)
	if len(projection.snapshot()) != 1 {
		t.Fatal("audio projection did not snapshot an open turn")
	}
	projection.observe(messages.StreamMessage{Type: messages.StreamTypeMessageEnd}, recordingDirectionClient)
	projection.observe(messages.StreamMessage{Type: messages.StreamTypeToolCallEnd}, recordingDirectionAgent)
	projection.observe(messages.StreamMessage{Type: messages.StreamTypeMessageEnd}, recordingDirectionAgent)
	if len(projection.snapshot()) != 1 {
		t.Fatal("audio projection changed turn count at tool boundary")
	}
}

func TestArtifactAndSessionUtilityBranches(t *testing.T) {
	destination := t.TempDir()
	manifest := &transcript.RecordingManifest{}
	if err := appendArtifact(destination, manifest, transcript.RecordingArtifact{Path: "nested/data.json", Data: []byte(`{"ok":true}`)}, nil); err != nil {
		t.Fatalf("append data artifact: %v", err)
	}
	sourcePath := filepath.Join(destination, "source.bin")
	if err := os.WriteFile(sourcePath, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := appendArtifact(destination, manifest, transcript.RecordingArtifact{Path: "source.bin.copy", SourcePath: sourcePath}, nil); err != nil {
		t.Fatalf("append source artifact: %v", err)
	}
	if err := appendArtifact(destination, manifest, transcript.RecordingArtifact{Path: "nested/data.json", Data: []byte("duplicate")}, nil); err == nil {
		t.Fatal("duplicate artifact was accepted")
	}
	if err := appendArtifact(destination, manifest, transcript.RecordingArtifact{Path: "bad", Data: []byte("secret")}, []string{"secret"}); err == nil {
		t.Fatal("credential-bearing artifact was accepted")
	}
	if err := appendArtifact(destination, manifest, transcript.RecordingArtifact{Path: "bad", SourcePath: sourcePath, Data: []byte("both")}, nil); err == nil {
		t.Fatal("source/data artifact was accepted")
	}
	if err := writeArtifact(destination, "../escape", []byte("x"), nil); err == nil {
		t.Fatal("unsafe artifact path was accepted")
	}
	if err := writeArtifact(destination, "secret.txt", []byte("secret"), []string{"secret"}); err == nil {
		t.Fatal("credential-bearing write was accepted")
	}
	replaced := filepath.Join(destination, "replaced.txt")
	if err := atomicReplace(replaced, []byte("new"), 0o644); err != nil {
		t.Fatalf("atomic replace: %v", err)
	}
	assertFileBytes(t, replaced, []byte("new"))
	if cloneStringMap(nil) != nil || len(cloneStringMap(map[string]string{"a": "b"})) != 1 {
		t.Fatal("cloneStringMap did not preserve nil/map shapes")
	}
	if responseText(messages.ToolCallResponse{Content: "text"}) != "text" || responseText(messages.ToolCallResponse{ContentParts: []messages.ContentPart{messages.TextPart{Text: "part"}}}) != "1 content parts" || responseText(messages.ToolCallResponse{}) != "" {
		t.Fatal("responseText projection is incorrect")
	}
	inner := newTestSession()
	wrapped := newRecordedSession(context.Background(), inner, &recorder{})
	if wrapped.Done() != inner.Done() {
		t.Fatal("recorded session did not expose inner Done channel")
	}
	if err := wrapped.Close(); err != nil {
		t.Fatalf("close empty recorded session: %v", err)
	}
}
