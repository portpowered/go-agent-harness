package session

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
	recordingservice "github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording/internal/service"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

const testRecordingBase = "2031-02-03T04:05:06Z"

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
	destination := filepath.Join(t.TempDir(), "nested", "recording")
	recorder, source := openTestRecorder(t, destination, recording.BrowserOptions{})
	inner := newTestSession()
	session, err := recorder.Wrap(&testInferencer{session: inner}).ConnectSession(context.Background())
	if err != nil {
		t.Fatalf("connect wrapped session: %v", err)
	}
	ctx := context.Background()
	input := [][]byte{{0x01, 0x00}, {0x02, 0x00}}
	output := [][]byte{{0x03, 0x00}, {0x04, 0x00}}
	for _, data := range input {
		if !session.Send(ctx, messages.StreamMessage{Type: messages.StreamTypeAudioDelta, Role: messages.RoleUser, Value: messages.NewAudioDeltaValue(data)}) {
			t.Fatal("wrapped session rejected input audio")
		}
	}
	if !session.Send(ctx, messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleUser}) {
		t.Fatal("wrapped session rejected input commit")
	}
	for _, data := range output {
		if !inner.incoming.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeAudioDelta, Role: messages.RoleAssistant, Value: messages.NewAudioDeltaValue(data)}) {
			t.Fatal("fixture session rejected output audio")
		}
	}
	if !inner.incoming.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("reply")}) ||
		!inner.incoming.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant}) {
		t.Fatal("fixture session rejected output completion")
	}
	for range output {
		if _, ok := session.Receive().ReadBlockingContext(ctx); !ok {
			t.Fatal("wrapped session did not forward provider audio")
		}
	}
	if _, ok := session.Receive().ReadBlockingContext(ctx); !ok {
		t.Fatal("wrapped session did not forward provider text")
	}
	if _, ok := session.Receive().ReadBlockingContext(ctx); !ok {
		t.Fatal("wrapped session did not forward provider completion")
	}
	if source.Tick() != 7 {
		t.Fatalf("observation ticks = %d, want 7", source.Tick())
	}
	if err := recorder.RecordTerminalSummary(testTerminalSummary()); err != nil {
		t.Fatalf("record terminal summary: %v", err)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("close wrapped session: %v", err)
	}
	if err := recorder.Finalize(ctx, nil); err != nil {
		t.Fatalf("finalize recording: %v", err)
	}

	for index, want := range input {
		assertFileBytes(t, filepath.Join(destination, "audio", formatAudioName("in", index)), want)
	}
	for index, want := range output {
		assertFileBytes(t, filepath.Join(destination, "audio", formatAudioName("out", index)), want)
	}
	manifest := readManifest(t, destination)
	expectedArtifacts := []string{
		"client.transcript.jsonl", "agent.transcript.jsonl", "session-log.jsonl",
		"audio/in-000.pcm", "audio/in-001.pcm", "audio/out-000.pcm", "audio/out-001.pcm",
	}
	assertManifestArtifacts(t, destination, manifest, expectedArtifacts)
	assertManifestArtifactOrder(t, manifest, expectedArtifacts)
	if manifest.Terminal == nil || manifest.Terminal.Reason != "completed" {
		t.Fatalf("terminal summary = %+v", manifest.Terminal)
	}
	var logEntry struct {
		Input struct {
			AudioBytes    uint64   `json:"audio_bytes"`
			Committed     bool     `json:"committed"`
			AudioSegments []string `json:"audio_segments"`
		} `json:"input"`
		Response struct {
			Text          string   `json:"text"`
			AudioBytes    uint64   `json:"audio_bytes"`
			Complete      bool     `json:"complete"`
			AudioSegments []string `json:"audio_segments"`
		} `json:"response"`
	}
	logData, err := os.ReadFile(filepath.Join(destination, "session-log.jsonl"))
	if err != nil {
		t.Fatalf("read session log: %v", err)
	}
	if err := json.Unmarshal(bytes.TrimSpace(logData), &logEntry); err != nil {
		t.Fatalf("decode session log: %v", err)
	}
	if logEntry.Input.AudioBytes != 4 || !logEntry.Input.Committed || !equalStrings(logEntry.Input.AudioSegments, []string{"audio/in-000.pcm", "audio/in-001.pcm"}) {
		t.Fatalf("input audio projection = %+v", logEntry.Input)
	}
	if logEntry.Response.Text != "reply" || logEntry.Response.AudioBytes != 4 || !logEntry.Response.Complete || !equalStrings(logEntry.Response.AudioSegments, []string{"audio/out-000.pcm", "audio/out-001.pcm"}) {
		t.Fatalf("response projection = %+v", logEntry.Response)
	}
	records := readTranscript(t, filepath.Join(destination, "client.transcript.jsonl"))
	if len(records) != 8 || records[0].Tick != 1 || records[7].Tick != 8 {
		t.Fatalf("client transcript records = %d or ticks %+v", len(records), records)
	}
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
	conflicting.Reason = "failed"
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
	resultBytes, _ := json.Marshal(result)
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
	complete, ok := session.(interface {
		SendMessage(context.Context, messages.Message) bool
		SendMessageWithoutResponse(context.Context, messages.Message) bool
		SupportsCompleteMessages() bool
		SupportsCompleteMessagesWithoutResponse() bool
	})
	if !ok || !complete.SendMessage(context.Background(), messages.Message{}) || !rich.messageSent {
		t.Fatal("complete message was not delegated")
	}
	if !complete.SendMessageWithoutResponse(context.Background(), messages.Message{}) || !rich.messageWithoutResponseSent {
		t.Fatal("complete message without response was not delegated")
	}
	if !complete.SupportsCompleteMessages() || complete.SupportsCompleteMessagesWithoutResponse() {
		t.Fatal("complete-message capabilities were not delegated")
	}
	responseRequester, ok := session.(interface {
		messages.SessionResponseRequester
		messages.SessionResponseCapability
	})
	if !ok || !responseRequester.SupportsResponseRequests() {
		t.Fatal("response capability was not delegated")
	}
	if outcome := responseRequester.RequestResponse(context.Background()); !outcome.OK() {
		t.Fatalf("response request outcome = %+v", outcome)
	}
	media, mediaOK := session.(interface {
		RTCMedia() sharedaudio.MediaEndpoints
	})
	terminal, terminalOK := session.(interface{ TerminalError() error })
	if !mediaOK || media.RTCMedia().Inbound != nil || !terminalOK || terminal.TerminalError() == nil {
		t.Fatal("optional media/terminal capabilities were not delegated")
	}
	_ = session.Close()
	_ = recorder.Finalize(context.Background(), errors.New("capability fixture stopped"))

	failing := recorder.Wrap(&errorInferencer{err: errors.New("connect failed")})
	if _, err := failing.ConnectSession(context.Background()); err == nil || !strings.Contains(err.Error(), "connect failed") {
		t.Fatalf("connect error = %v", err)
	}
	nilSession := recorder.Wrap(&nilInferencer{})
	if _, err := nilSession.ConnectSession(context.Background()); err == nil || !strings.Contains(err.Error(), "nil session") {
		t.Fatalf("nil session error = %v", err)
	}
}

func TestBrowserAndImageHelperBranches(t *testing.T) {
	base := recording.BrowserEvent{BrowserID: "browser", TargetID: "target", Generation: 4, InvocationID: "inv", ToolName: "tool"}
	for _, event := range []recording.BrowserEvent{
		{Type: "tools_removed", BrowserID: base.BrowserID, TargetID: base.TargetID, Generation: base.Generation, RemovedToolNames: []string{"old"}},
		{Type: "catalog_ready", BrowserID: base.BrowserID, TargetID: base.TargetID, Generation: base.Generation, ToolCountKnown: true, ToolCount: 2},
		{Type: "tool_responded", BrowserID: base.BrowserID, TargetID: base.TargetID, Generation: base.Generation, InvocationID: base.InvocationID, Status: "canceled", Reason: "user"},
		{Type: "tool_responded", BrowserID: base.BrowserID, TargetID: base.TargetID, Generation: base.Generation, InvocationID: base.InvocationID, Status: "failed", ErrorCode: "E_FAIL", Reason: "failure", Output: json.RawMessage(`{"detail":"safe"}`)},
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
	if browserErrorCode(recording.BrowserEvent{ErrorCode: "explicit"}) != "explicit" || browserErrorCode(recording.BrowserEvent{Status: "failed"}) != "invocation_error" {
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
	data, _ := json.Marshal(valid)
	envelopeBytes, _ := json.Marshal(struct {
		Version string          `json:"version"`
		OK      bool            `json:"ok"`
		Data    json.RawMessage `json:"data"`
	}{"webmcp.tool-result.v1", true, data})
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
	badLengthJSON, _ := json.Marshal(badLength)
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

func testTerminalSummary() transcript.RecordingTerminalSummary {
	return transcript.RecordingTerminalSummary{
		Reason: "completed", Classification: "success",
		TerminalReason:     messages.TerminalReasonProviderAuthoredCompletion,
		TerminalProvenance: messages.TerminalProvenanceProvider,
		OutputState:        messages.TerminalOutputComplete,
	}
}

type richTestSession struct {
	*testSession
	messageSent                bool
	messageWithoutResponseSent bool
	terminalErr                error
}

func (s *richTestSession) SendMessage(context.Context, messages.Message) bool {
	s.messageSent = true
	return true
}

func (s *richTestSession) SendMessageWithoutResponse(context.Context, messages.Message) bool {
	s.messageWithoutResponseSent = true
	return true
}

func (*richTestSession) SupportsCompleteMessages() bool { return true }

func (*richTestSession) SupportsCompleteMessagesWithoutResponse() bool { return false }

func (*richTestSession) RequestResponse(context.Context) messages.SessionSendOutcome {
	return messages.SessionSendOutcome{Status: messages.SessionSendSucceeded}
}

func (*richTestSession) SupportsResponseRequests() bool { return true }

func (*richTestSession) RTCMedia() sharedaudio.MediaEndpoints { return sharedaudio.MediaEndpoints{} }

func (s *richTestSession) TerminalError() error { return s.terminalErr }

type errorInferencer struct{ err error }

func (i *errorInferencer) ConnectSession(context.Context) (messages.Session, error) {
	return nil, i.err
}

type nilInferencer struct{}

func (*nilInferencer) ConnectSession(context.Context) (messages.Session, error) { return nil, nil }

func testPNG(t *testing.T) []byte {
	t.Helper()
	imageData := image.NewRGBA(image.Rect(0, 0, 2, 1))
	imageData.Set(0, 0, color.RGBA{R: 0x11, G: 0x22, B: 0x33, A: 0xff})
	imageData.Set(1, 0, color.RGBA{R: 0xaa, G: 0xbb, B: 0xcc, A: 0xff})
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, imageData); err != nil {
		t.Fatalf("encode PNG: %v", err)
	}
	return buffer.Bytes()
}

func assertFileBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s = %x, want %x", path, got, want)
	}
}

func readManifest(t *testing.T, destination string) transcript.RecordingManifest {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(destination, "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var manifest transcript.RecordingManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	return manifest
}

func assertManifestArtifacts(t *testing.T, destination string, manifest transcript.RecordingManifest, want []string) {
	t.Helper()
	seen := make(map[string]bool, len(manifest.Artifacts))
	for _, artifact := range manifest.Artifacts {
		if seen[artifact.Path] {
			t.Fatalf("duplicate manifest artifact %q", artifact.Path)
		}
		seen[artifact.Path] = true
		data, err := os.ReadFile(filepath.Join(destination, filepath.FromSlash(artifact.Path)))
		if err != nil {
			t.Fatalf("read artifact %s: %v", artifact.Path, err)
		}
		digest := sha256.Sum256(data)
		if artifact.SHA256 != hex.EncodeToString(digest[:]) {
			t.Fatalf("artifact %s digest = %s, want %s", artifact.Path, artifact.SHA256, hex.EncodeToString(digest[:]))
		}
	}
	for _, path := range want {
		if !seen[path] {
			t.Fatalf("manifest missing artifact %q; got %v", path, strings.Join(sortedKeys(seen), ","))
		}
	}
}

func assertManifestArtifactOrder(t *testing.T, manifest transcript.RecordingManifest, want []string) {
	t.Helper()
	got := make([]string, 0, len(manifest.Artifacts))
	for _, artifact := range manifest.Artifacts {
		got = append(got, artifact.Path)
	}
	if !equalStrings(got, want) {
		t.Fatalf("manifest artifact order = %v, want %v", got, want)
	}
}

func readTranscript(t *testing.T, path string) []transcript.Record {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	var records []transcript.Record
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		record, decodeErr := transcript.Decode(line)
		if decodeErr != nil {
			t.Fatalf("decode transcript: %v", decodeErr)
		}
		records = append(records, record)
	}
	return records
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func sortedKeys(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
