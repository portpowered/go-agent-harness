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
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

const testCompletedReason = "completed"

func testTerminalSummary() transcript.RecordingTerminalSummary {
	return transcript.RecordingTerminalSummary{
		Reason: testCompletedReason, Classification: "success",
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

type sessionAudioFixture struct {
	destination string
	recorder    recording.SessionRecorder
	source      *clock.Deterministic
	inner       *testSession
	session     messages.Session
	input       [][]byte
	output      [][]byte
}

func newSessionAudioFixture(t *testing.T) *sessionAudioFixture {
	t.Helper()
	destination := filepath.Join(t.TempDir(), "nested", "recording")
	recorder, source := openTestRecorder(t, destination, recording.BrowserOptions{})
	inner := newTestSession()
	session, err := recorder.Wrap(&testInferencer{session: inner}).ConnectSession(context.Background())
	if err != nil {
		t.Fatalf("connect wrapped session: %v", err)
	}
	return &sessionAudioFixture{
		destination: destination, recorder: recorder, source: source, inner: inner,
		session: session, input: [][]byte{{0x01, 0x00}, {0x02, 0x00}}, output: [][]byte{{0x03, 0x00}, {0x04, 0x00}},
	}
}

func sendSessionAudioInput(t *testing.T, fixture *sessionAudioFixture) {
	t.Helper()
	ctx := context.Background()
	for _, data := range fixture.input {
		if !fixture.session.Send(ctx, messages.StreamMessage{Type: messages.StreamTypeAudioDelta, Role: messages.RoleUser, Value: messages.NewAudioDeltaValue(data)}) {
			t.Fatal("wrapped session rejected input audio")
		}
	}
	if !fixture.session.Send(ctx, messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleUser}) {
		t.Fatal("wrapped session rejected input commit")
	}
}

func sendSessionAudioOutput(t *testing.T, fixture *sessionAudioFixture) {
	t.Helper()
	ctx := context.Background()
	for _, data := range fixture.output {
		if !fixture.inner.incoming.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeAudioDelta, Role: messages.RoleAssistant, Value: messages.NewAudioDeltaValue(data)}) {
			t.Fatal("fixture session rejected output audio")
		}
	}
	if !fixture.inner.incoming.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("reply")}) ||
		!fixture.inner.incoming.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant}) {
		t.Fatal("fixture session rejected output completion")
	}
}

func receiveSessionAudioOutput(t *testing.T, fixture *sessionAudioFixture) {
	t.Helper()
	ctx := context.Background()
	for range fixture.output {
		if _, ok := fixture.session.Receive().ReadBlockingContext(ctx); !ok {
			t.Fatal("wrapped session did not forward provider audio")
		}
	}
	for range []string{"text", "completion"} {
		if _, ok := fixture.session.Receive().ReadBlockingContext(ctx); !ok {
			t.Fatal("wrapped session did not forward provider output")
		}
	}
}

func finalizeSessionAudioFixture(t *testing.T, fixture *sessionAudioFixture) {
	t.Helper()
	if fixture.source.Tick() != 7 {
		t.Fatalf("observation ticks = %d, want 7", fixture.source.Tick())
	}
	if err := fixture.recorder.RecordTerminalSummary(testTerminalSummary()); err != nil {
		t.Fatalf("record terminal summary: %v", err)
	}
	if err := fixture.session.Close(); err != nil {
		t.Fatalf("close wrapped session: %v", err)
	}
	if err := fixture.recorder.Finalize(context.Background(), nil); err != nil {
		t.Fatalf("finalize recording: %v", err)
	}
}

func assertSessionAudioFixture(t *testing.T, fixture *sessionAudioFixture) {
	t.Helper()
	assertSessionAudioFiles(t, fixture)
	manifest := readManifest(t, fixture.destination)
	expected := []string{"client.transcript.jsonl", "agent.transcript.jsonl", "session-log.jsonl", "audio/in-000.pcm", "audio/in-001.pcm", "audio/out-000.pcm", "audio/out-001.pcm"}
	assertManifestArtifacts(t, fixture.destination, manifest, expected)
	assertManifestArtifactOrder(t, manifest, expected)
	if manifest.Terminal == nil || manifest.Terminal.Reason != testCompletedReason {
		t.Fatalf("terminal summary = %+v", manifest.Terminal)
	}
	assertSessionAudioLog(t, fixture.destination)
}

func assertSessionAudioFiles(t *testing.T, fixture *sessionAudioFixture) {
	t.Helper()
	for index, want := range fixture.input {
		assertFileBytes(t, filepath.Join(fixture.destination, "audio", formatAudioName("in", index)), want)
	}
	for index, want := range fixture.output {
		assertFileBytes(t, filepath.Join(fixture.destination, "audio", formatAudioName("out", index)), want)
	}
}

func assertSessionAudioLog(t *testing.T, destination string) {
	t.Helper()
	var entry struct {
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
	if err := json.Unmarshal(bytes.TrimSpace(logData), &entry); err != nil {
		t.Fatalf("decode session log: %v", err)
	}
	if entry.Input.AudioBytes != 4 || !entry.Input.Committed || !equalStrings(entry.Input.AudioSegments, []string{"audio/in-000.pcm", "audio/in-001.pcm"}) {
		t.Fatalf("input audio projection = %+v", entry.Input)
	}
	if entry.Response.Text != "reply" || entry.Response.AudioBytes != 4 || !entry.Response.Complete || !equalStrings(entry.Response.AudioSegments, []string{"audio/out-000.pcm", "audio/out-001.pcm"}) {
		t.Fatalf("response audio projection = %+v", entry.Response)
	}
	records := readTranscript(t, filepath.Join(destination, "client.transcript.jsonl"))
	if len(records) != 8 || records[0].Tick != 1 || records[7].Tick != 8 {
		t.Fatalf("client transcript records = %d or ticks %+v", len(records), records)
	}
}

func assertForwardingCapabilities(t *testing.T, session messages.Session, rich *richTestSession) {
	t.Helper()
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
}

func assertResponseAndMediaCapabilities(t *testing.T, session messages.Session) {
	t.Helper()
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
}

func assertConnectionFailures(t *testing.T, recorder recording.SessionRecorder) {
	t.Helper()
	failing := recorder.Wrap(&errorInferencer{err: errors.New("connect failed")})
	if _, err := failing.ConnectSession(context.Background()); err == nil || !strings.Contains(err.Error(), "connect failed") {
		t.Fatalf("connect error = %v", err)
	}
	nilSession := recorder.Wrap(&nilInferencer{})
	if _, err := nilSession.ConnectSession(context.Background()); err == nil || !strings.Contains(err.Error(), "nil session") {
		t.Fatalf("nil session error = %v", err)
	}
}
