package service

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

type contractSession struct {
	receive       *messages.TypedBuffer[messages.StreamMessage]
	done          chan struct{}
	closeOnce     sync.Once
	closeErr      error
	responseSent  bool
	messageSent   bool
	withoutSent   bool
	responseKnown bool
	terminalErr   error
}

func newContractSession() *contractSession {
	return &contractSession{receive: messages.NewTypedBuffer[messages.StreamMessage](8), done: make(chan struct{}), responseKnown: true}
}

func (s *contractSession) Send(context.Context, messages.StreamMessage) bool { return true }
func (s *contractSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.receive
}
func (s *contractSession) Done() <-chan struct{} { return s.done }
func (s *contractSession) Close() error {
	s.closeOnce.Do(func() { close(s.done) })
	return s.closeErr
}
func (s *contractSession) RequestResponse(context.Context) messages.SessionSendOutcome {
	s.responseSent = true
	return messages.SessionSendOutcome{Status: messages.SessionSendSucceeded}
}
func (s *contractSession) SupportsResponseRequests() bool { return s.responseKnown }
func (s *contractSession) SendMessage(context.Context, messages.Message) bool {
	s.messageSent = true
	return true
}
func (s *contractSession) SendMessageWithoutResponse(context.Context, messages.Message) bool {
	s.withoutSent = true
	return true
}
func (s *contractSession) SupportsCompleteMessages() bool                { return true }
func (s *contractSession) SupportsCompleteMessagesWithoutResponse() bool { return true }
func (s *contractSession) RTCMedia() (audio.MediaEndpoints, bool) {
	return audio.MediaEndpoints{}, true
}
func (s *contractSession) TerminalError() error { return s.terminalErr }

type contractInferencer struct {
	session *contractSession
	err     error
}

func (i contractInferencer) ConnectSession(context.Context) (messages.Session, error) {
	return i.session, i.err
}

func newConnectedAdmission(t *testing.T) (*AdmissionInferencer, *AdmissionSession, *contractSession) {
	t.Helper()
	inner := newContractSession()
	inferencer := NewAdmissionInferencer(contractInferencer{session: inner}, nil, nil)
	connected, err := inferencer.ConnectSession(context.Background())
	if err != nil {
		t.Fatalf("ConnectSession: %v", err)
	}
	wrapped, ok := connected.(*AdmissionSession)
	if !ok {
		t.Fatalf("wrapped session = %T, want *AdmissionSession", connected)
	}
	return inferencer, wrapped, inner
}

func closeAdmissionForTest(t *testing.T, inferencer *AdmissionInferencer, wrapped *AdmissionSession) {
	t.Helper()
	if err := wrapped.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case <-wrapped.Done():
	case <-time.After(time.Second):
		t.Fatal("wrapped session did not close its done channel")
	}
	inferencer.WaitForClose()
	if inferencer.CloseError() != nil || inferencer.RuntimeError() != nil {
		t.Fatalf("unexpected admission errors: close=%v runtime=%v", inferencer.CloseError(), inferencer.RuntimeError())
	}
}

func TestAdmissionContractForwardsCapabilities(t *testing.T) {
	inferencer, wrapped, inner := newConnectedAdmission(t)
	defer closeAdmissionForTest(t, inferencer, wrapped)
	if !wrapped.SupportsResponseRequests() {
		t.Fatal("response request capability was not forwarded")
	}
	if wrapped.RequestResponse(context.Background()).Status != messages.SessionSendSucceeded || !inner.responseSent {
		t.Fatal("response request capability was not forwarded")
	}
	if !wrapped.SupportsCompleteMessages() || !wrapped.SupportsCompleteMessagesWithoutResponse() {
		t.Fatal("complete-message capability was not forwarded")
	}
	if !wrapped.SendMessage(context.Background(), messages.NewTextMessage(messages.RoleUser, "tool")) || !inner.messageSent {
		t.Fatal("complete message was not forwarded")
	}
	if !wrapped.SendMessageWithoutResponse(context.Background(), messages.NewTextMessage(messages.RoleUser, "tool")) || !inner.withoutSent {
		t.Fatal("deferred complete message was not forwarded")
	}
	if !wrapped.Send(context.Background(), messages.StreamMessage{Type: messages.StreamTypeTextDelta}) {
		t.Fatal("stream message was not forwarded")
	}
	if _, ok := wrapped.RTCMedia(); !ok {
		t.Fatal("RTC media capability was not forwarded")
	}
	terminalErr := errors.New("terminal")
	inner.terminalErr = terminalErr
	if !errors.Is(wrapped.TerminalError(), terminalErr) {
		t.Fatal("terminal error capability was not forwarded")
	}
}

func TestAdmissionContractRetainsProviderTerminal(t *testing.T) {
	inferencer, wrapped, inner := newConnectedAdmission(t)
	defer closeAdmissionForTest(t, inferencer, wrapped)
	terminal := messages.StreamMessage{Type: messages.StreamTypeSessionClose, Value: messages.NewSessionCloseValueWithTerminal(
		"session", "provider", "provider_close", messages.TerminalReasonProviderClose,
		messages.TerminalProvenanceProvider, messages.TerminalOutputPartial,
	)}
	if !inner.receive.Write(context.Background(), terminal) {
		t.Fatal("write provider terminal")
	}
	select {
	case got := <-wrapped.Receive().Chan():
		if !wrapped.IsProviderTerminalMessage(got) {
			t.Fatal("forwarded terminal was not recognized as provider-authored")
		}
	case <-time.After(time.Second):
		t.Fatal("provider terminal was not forwarded")
	}
	if got, ok := inferencer.ProviderTerminalMessage(); !ok || got.Type != messages.StreamTypeSessionClose {
		t.Fatalf("provider terminal fact = (%+v, %v)", got, ok)
	}
	if !inferencer.IsProviderTerminalMessage(terminal) {
		t.Fatal("inferencer did not retain provider terminal identity")
	}
}

func TestAdmissionInferencerRecordsConnectionFailureAndClosesEmptyBoundary(t *testing.T) {
	failure := errors.New("connect failed")
	inferencer := NewAdmissionInferencer(contractInferencer{err: failure}, nil, nil)
	if _, err := inferencer.ConnectSession(context.Background()); !errors.Is(err, failure) {
		t.Fatalf("ConnectSession error = %v, want failure", err)
	}
	if !errors.Is(inferencer.RuntimeError(), failure) || inferencer.CloseError() != nil {
		t.Fatalf("recorded errors: runtime=%v close=%v", inferencer.RuntimeError(), inferencer.CloseError())
	}
	inferencer.CloseAdmission()
	inferencer.WaitForClose()
}

func TestControllerSnapshotAndToolObligationAccessors(t *testing.T) {
	controller, err := New().Begin(sessionduration.Options{Clock: testNoopScheduler{}})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if got := controller.OutputState(); got != messages.TerminalOutputNone || controller.TerminalWritten() {
		t.Fatalf("initial snapshot = state:%q written:%v", got, controller.TerminalWritten())
	}
	controller.SetToolObligation(true)
	controller.BeginLocalToolExecution()
	controller.EndLocalToolExecution()
	if _, err := controller.Finalize(context.Background(), sessionduration.FinalizeRequest{}); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
}

type testNoopScheduler struct{}

func (testNoopScheduler) NewTimer(time.Duration) sessionduration.Timer { return testNoopTimer{} }

type nilTimerScheduler struct{}

func (nilTimerScheduler) NewTimer(time.Duration) sessionduration.Timer { return nil }

type testNoopTimer struct{}

func (testNoopTimer) C() <-chan time.Time      { return make(chan time.Time) }
func (testNoopTimer) Stop() bool               { return true }
func (testNoopTimer) Reset(time.Duration) bool { return true }

func TestBeginRejectsUnavailableSchedulersAndNegativePolicyValues(t *testing.T) {
	if controller, err := New().Begin(sessionduration.Options{MaxDuration: time.Second}); controller != nil || !errors.Is(err, sessionduration.ErrSchedulerUnavailable) {
		t.Fatalf("Begin without scheduler = controller:%v err:%v", controller, err)
	}
	if controller, err := New().Begin(sessionduration.Options{Clock: nilTimerScheduler{}, MaxDuration: time.Second}); controller != nil || !errors.Is(err, sessionduration.ErrSchedulerUnavailable) {
		t.Fatalf("Begin with nil timer = controller:%v err:%v", controller, err)
	}
	if controller, err := New().Begin(sessionduration.Options{Liveness: sessionduration.LivenessOptions{Timeout: -time.Second}}); controller != nil || err == nil {
		t.Fatalf("Begin with negative liveness timeout = controller:%v err:%v", controller, err)
	}
	if controller, err := New().Begin(sessionduration.Options{Retry: sessionduration.RetryPolicy{MaxRetries: -1}}); controller != nil || err == nil {
		t.Fatalf("Begin with negative retry budget = controller:%v err:%v", controller, err)
	}
}

func TestControllerReportsUnavailableLivenessScheduler(t *testing.T) {
	controller, err := New().Begin(sessionduration.Options{
		Clock:         testNoopScheduler{},
		LivenessClock: nilTimerScheduler{},
		Liveness:      sessionduration.LivenessOptions{Enabled: true, Timeout: time.Second},
	})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	controller.Observe(messages.StreamMessage{Type: messages.StreamTypeMessageStart})
	select {
	case err := <-controller.Errors():
		if !errors.Is(err, sessionduration.ErrSchedulerUnavailable) {
			t.Fatalf("liveness scheduler error = %v, want unavailable identity", err)
		}
	case <-time.After(time.Second):
		t.Fatal("liveness scheduler failure was not reported")
	}
	if _, err := controller.Finalize(context.Background(), sessionduration.FinalizeRequest{}); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
}

type artifactAudioSink struct {
	samples  []int16
	writeErr error
	flushErr error
	closeErr error
}

func (s *artifactAudioSink) WriteSamples(samples []int16) error {
	if s.writeErr != nil {
		return s.writeErr
	}
	s.samples = append(s.samples, samples...)
	return nil
}
func (s *artifactAudioSink) Flush() error { return s.flushErr }
func (s *artifactAudioSink) Close() error { return s.closeErr }

type artifactTranscriptSink struct {
	records  []transcript.Record
	flushErr error
	closeErr error
	writeErr error
}

func (s *artifactTranscriptSink) Write(record transcript.Record) error {
	if s.writeErr != nil {
		return s.writeErr
	}
	s.records = append(s.records, record)
	return nil
}
func (s *artifactTranscriptSink) Flush() error { return s.flushErr }
func (s *artifactTranscriptSink) Close() error { return s.closeErr }

func TestArtifactsPreserveAcceptedAudioTranscriptAndLifecycleErrors(t *testing.T) {
	audioErr := errors.New("audio flush")
	transcriptErr := errors.New("transcript close")
	audio := &artifactAudioSink{flushErr: audioErr}
	transcriptSink := &artifactTranscriptSink{closeErr: transcriptErr}
	artifacts := NewSessionDurationArtifactSetWithSinks(audio, transcriptSink)
	pcm := make([]byte, 4)
	binary.LittleEndian.PutUint16(pcm[0:2], 12)
	negative := int16(-13)
	binary.LittleEndian.PutUint16(pcm[2:4], uint16(negative))
	if err := artifacts.Accept(messages.StreamMessage{Type: messages.StreamTypeAudioDelta, Value: messages.NewAudioDeltaValue(pcm)}); err != nil {
		t.Fatalf("Accept audio: %v", err)
	}
	if err := artifacts.Accept(messages.StreamMessage{Type: messages.StreamTypeLoopEnd}); err != nil {
		t.Fatalf("Accept loop end: %v", err)
	}
	if err := artifacts.Accept(messages.StreamMessage{Type: messages.StreamTypeTextDelta}); err != nil {
		t.Fatalf("Accept text: %v", err)
	}
	if len(audio.samples) != 2 || audio.samples[0] != 12 || audio.samples[1] != -13 || len(transcriptSink.records) != 2 {
		t.Fatalf("accepted artifacts = samples:%v records:%d", audio.samples, len(transcriptSink.records))
	}
	if err := artifacts.Flush(); !errors.Is(err, audioErr) {
		t.Fatalf("Flush error = %v, want audio identity", err)
	}
	if err := artifacts.Close(); !errors.Is(err, transcriptErr) {
		t.Fatalf("Close error = %v, want transcript identity", err)
	}
	if err := artifacts.Close(); !errors.Is(err, transcriptErr) {
		t.Fatalf("second Close error = %v, want cached identity", err)
	}
	if err := artifacts.Accept(messages.StreamMessage{Type: messages.StreamTypeTextDelta}); err == nil {
		t.Fatal("closed artifacts accepted a message")
	}
	if err := FinalizeArtifacts(artifacts); !errors.Is(err, transcriptErr) {
		t.Fatalf("FinalizeArtifacts error = %v, want cached close identity", err)
	}
}

func TestSessionDurationArtifactsPreserveWriteFailuresAndSequence(t *testing.T) {
	malformed := NewSessionDurationArtifactSetWithSinks(&artifactAudioSink{}, &artifactTranscriptSink{})
	if err := malformed.Accept(messages.StreamMessage{Type: messages.StreamTypeAudioDelta, Value: messages.NewAudioDeltaValue([]byte{1})}); err == nil {
		t.Fatal("malformed PCM was accepted")
	}

	audioErr := errors.New("audio write failed")
	transcriptSink := &artifactTranscriptSink{}
	audioFailure := NewSessionDurationArtifactSetWithSinks(&artifactAudioSink{writeErr: audioErr}, transcriptSink)
	pcm := make([]byte, 2)
	binary.LittleEndian.PutUint16(pcm, 21)
	if err := audioFailure.Accept(messages.StreamMessage{Type: messages.StreamTypeAudioDelta, Value: messages.NewAudioDeltaValue(pcm)}); !errors.Is(err, audioErr) {
		t.Fatalf("audio artifact error = %v, want write identity", err)
	}
	if len(transcriptSink.records) != 0 {
		t.Fatal("transcript recorded audio whose WAV write failed")
	}

	transcriptErr := errors.New("transcript write failed")
	transcriptSink = &artifactTranscriptSink{writeErr: transcriptErr}
	transcriptFailure := NewSessionDurationArtifactSetWithSinks(nil, transcriptSink)
	if err := transcriptFailure.Accept(messages.StreamMessage{Type: messages.StreamTypeTextDelta}); !errors.Is(err, transcriptErr) {
		t.Fatalf("transcript artifact error = %v, want write identity", err)
	}
	transcriptSink.writeErr = nil
	if err := transcriptFailure.Accept(messages.StreamMessage{Type: messages.StreamTypeTextDelta}); err != nil {
		t.Fatalf("transcript retry after write failure: %v", err)
	}
	if len(transcriptSink.records) != 1 || transcriptSink.records[0].Tick != 1 {
		t.Fatalf("transcript records after retry = %+v, want sequence to advance only on successful write", transcriptSink.records)
	}
}

func TestArtifactContextPreparationAndTerminalRecording(t *testing.T) {
	var nilContext context.Context
	if ArtifactsFromContext(nilContext) != nil {
		t.Fatal("nil context unexpectedly returned artifacts")
	}
	if _, ok := ArtifactPathsFromContext(nilContext); ok {
		t.Fatal("nil context unexpectedly returned artifact paths")
	}
	if _, err := PrepareArtifacts(WithSessionDurationArtifactPaths(context.Background(), sessionduration.SessionDurationArtifactPaths{AudioPath: "only-audio"})); err == nil {
		t.Fatal("partial artifact paths were accepted")
	}
	if got, err := PrepareArtifacts(context.Background()); err != nil || got == nil {
		t.Fatalf("empty artifact preparation = %v, %v", got, err)
	}

	directory := t.TempDir()
	paths := sessionduration.SessionDurationArtifactPaths{AudioPath: filepath.Join(directory, "audio.wav"), TranscriptPath: filepath.Join(directory, "transcript.jsonl")}
	ctx := WithSessionDurationArtifactPaths(nilContext, paths)
	prepared, err := PrepareArtifacts(ctx)
	if err != nil {
		t.Fatalf("PrepareArtifacts: %v", err)
	}
	artifacts := ArtifactsFromContext(prepared)
	if artifacts == nil {
		t.Fatal("prepared artifacts missing from context")
	}
	pcm := make([]byte, 2)
	binary.LittleEndian.PutUint16(pcm, 21)
	if err := artifacts.Accept(messages.StreamMessage{Type: messages.StreamTypeAudioDelta, Value: messages.NewAudioDeltaValue(pcm)}); err != nil {
		t.Fatalf("prepared audio Accept: %v", err)
	}
	if err := artifacts.Accept(messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("hello")}); err != nil {
		t.Fatalf("prepared Accept: %v", err)
	}
	if err := FinalizeArtifacts(artifacts); err != nil {
		t.Fatalf("FinalizeArtifacts: %v", err)
	}
	for _, path := range []string{paths.AudioPath, paths.TranscriptPath} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("artifact %s stat: %v", path, err)
		}
		if info.Size() == 0 {
			t.Fatalf("artifact %s is empty", path)
		}
	}

	recorder := &terminalRecorderProbe{}
	base := NewSessionDurationArtifactSetWithSinks(nil, &artifactTranscriptSink{})
	recorded := WithTerminalRecorder(WithSessionDurationArtifacts(context.Background(), base), recorder)
	lifecycle := ArtifactsFromContext(recorded)
	terminal := messages.StreamMessage{Type: messages.StreamTypeSessionClose, Value: messages.NewSessionCloseValueWithTerminal(
		"session", "done", "provider_close", messages.TerminalReasonProviderClose,
		messages.TerminalProvenanceProvider, messages.TerminalOutputComplete,
	)}
	if err := lifecycle.Accept(terminal); err != nil {
		t.Fatalf("terminal Accept: %v", err)
	}
	if len(recorder.summaries) != 1 || recorder.summaries[0].Classification != "provider_close" {
		t.Fatalf("terminal summaries = %+v", recorder.summaries)
	}
	if err := lifecycle.Flush(); err != nil {
		t.Fatalf("terminal lifecycle Flush: %v", err)
	}
	if err := lifecycle.Close(); err != nil {
		t.Fatalf("terminal lifecycle Close: %v", err)
	}

	pathRecorder := &terminalRecorderProbe{}
	recordedPaths := sessionduration.SessionDurationArtifactPaths{
		AudioPath:      filepath.Join(directory, "recorded-audio.wav"),
		TranscriptPath: filepath.Join(directory, "recorded-transcript.jsonl"),
	}
	recordedPathContext := WithTerminalRecorder(WithSessionDurationArtifactPaths(context.Background(), recordedPaths), pathRecorder)
	preparedRecording, err := PrepareArtifacts(recordedPathContext)
	if err != nil {
		t.Fatalf("PrepareArtifacts with terminal recorder: %v", err)
	}
	recordedLifecycle := ArtifactsFromContext(preparedRecording)
	if err := recordedLifecycle.Accept(terminal); err != nil {
		t.Fatalf("prepared terminal lifecycle Accept: %v", err)
	}
	if err := FinalizeArtifacts(recordedLifecycle); err != nil {
		t.Fatalf("FinalizeArtifacts with terminal recorder: %v", err)
	}
	if len(pathRecorder.summaries) != 1 || pathRecorder.summaries[0].Classification != "provider_close" {
		t.Fatalf("prepared path terminal summaries = %+v", pathRecorder.summaries)
	}
}

type admissionPolicySessionProbe struct {
	*contractSession
	closed bool
}

func (s *admissionPolicySessionProbe) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return nil
}
func (s *admissionPolicySessionProbe) Done() <-chan struct{}        { return nil }
func (s *admissionPolicySessionProbe) SessionAdmissionClosed() bool { return s.closed }
func (*admissionPolicySessionProbe) SessionAdmissionAllows(msg messages.StreamMessage) bool {
	return msg.Type == messages.StreamTypeResponseCancel
}
func (*admissionPolicySessionProbe) SessionAdmissionAllowsCompleteMessage(msg messages.Message) bool {
	return msg.Role == messages.RoleTool
}

type admissionClosedSessionProbe struct {
	*contractSession
	closed bool
}

func (s *admissionClosedSessionProbe) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return nil
}
func (s *admissionClosedSessionProbe) Done() <-chan struct{}        { return nil }
func (s *admissionClosedSessionProbe) SessionAdmissionClosed() bool { return s.closed }

func TestAdmissionSessionPreservesNestedAdmissionPolicy(t *testing.T) {
	service := New()
	ctx := context.Background()
	t.Run("explicit policy", func(t *testing.T) {
		inner := &admissionPolicySessionProbe{contractSession: newContractSession(), closed: true}
		session := service.NewAdmissionSession(ctx, inner, nil, nil)
		if !session.SessionAdmissionClosed() {
			t.Fatal("nested closed admission state was not preserved")
		}
		if !session.SessionAdmissionAllows(messages.StreamMessage{Type: messages.StreamTypeResponseCancel}) || session.SessionAdmissionAllows(messages.StreamMessage{Type: messages.StreamTypeResponseCreate}) {
			t.Fatal("nested stream admission policy was not preserved")
		}
		if !session.SessionAdmissionAllowsCompleteMessage(messages.Message{Role: messages.RoleTool}) || session.SessionAdmissionAllowsCompleteMessage(messages.Message{Role: messages.RoleUser}) {
			t.Fatal("nested complete-message admission policy was not preserved")
		}
	})
	t.Run("closed fallback", func(t *testing.T) {
		session := service.NewAdmissionSession(ctx, &admissionClosedSessionProbe{contractSession: newContractSession(), closed: true}, nil, nil)
		if !session.SessionAdmissionClosed() {
			t.Fatal("nested closed state was not preserved")
		}
		if !session.SessionAdmissionAllows(messages.StreamMessage{Type: messages.StreamTypeResponseCancel}) || !session.SessionAdmissionAllows(messages.StreamMessage{Type: messages.StreamTypeSessionClose}) || session.SessionAdmissionAllows(messages.StreamMessage{Type: messages.StreamTypeTextDelta}) {
			t.Fatal("closed session stream fallback admitted an ordinary output")
		}
		if session.SessionAdmissionAllowsCompleteMessage(messages.Message{Role: messages.RoleUser}) {
			t.Fatal("closed session fallback admitted a complete message")
		}
	})
	t.Run("open fallback", func(t *testing.T) {
		session := service.NewAdmissionSession(ctx, &admissionClosedSessionProbe{contractSession: newContractSession()}, nil, nil)
		if session.SessionAdmissionClosed() || !session.SessionAdmissionAllows(messages.StreamMessage{Type: messages.StreamTypeTextDelta}) || !session.SessionAdmissionAllowsCompleteMessage(messages.Message{Role: messages.RoleUser}) {
			t.Fatal("open session fallback did not preserve ordinary admission")
		}
	})
}

func TestSessionDurationArtifactSetWritesAcceptedAudioAndTerminalTranscript(t *testing.T) {
	directory := t.TempDir()
	audioPath := filepath.Join(directory, "accepted.wav")
	transcriptPath := filepath.Join(directory, "accepted.jsonl")
	artifacts, err := NewSessionDurationArtifactSet(audioPath, transcriptPath)
	if err != nil {
		t.Fatalf("NewSessionDurationArtifactSet: %v", err)
	}
	pcm := make([]byte, 4)
	binary.LittleEndian.PutUint16(pcm[:2], 42)
	negative := int16(-13)
	binary.LittleEndian.PutUint16(pcm[2:], uint16(negative))
	for _, message := range []messages.StreamMessage{
		{Type: messages.StreamTypeAudioDelta, Role: messages.RoleAssistant, Value: messages.NewAudioDeltaValue(pcm)},
		{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("accepted response")},
		{Type: messages.StreamTypeSessionClose, Role: messages.RoleSystem, Value: messages.NewSessionCloseValueWithTerminal("session-1", "expired", string(sessionduration.MaxDurationReason), sessionduration.MaxDurationReason, messages.TerminalProvenanceLoop, messages.TerminalOutputPartial)},
		{Type: messages.StreamTypeLoopEnd, Role: messages.RoleSystem},
	} {
		if err := artifacts.Accept(message); err != nil {
			t.Fatalf("Accept(%s): %v", message.Type, err)
		}
	}
	if err := FinalizeArtifacts(artifacts); err != nil {
		t.Fatalf("FinalizeArtifacts: %v", err)
	}

	wavBytes, err := os.ReadFile(audioPath)
	if err != nil {
		t.Fatalf("read WAV artifact: %v", err)
	}
	rate, samples, err := wavio.Read(bytes.NewReader(wavBytes))
	if err != nil || rate != audio.SampleRate || len(samples) != 2 || samples[0] != 42 || samples[1] != -13 {
		t.Fatalf("recorded WAV = rate %d samples %v err %v, want exact accepted PCM at %d Hz", rate, samples, err, audio.SampleRate)
	}
	transcriptBytes, err := os.ReadFile(transcriptPath)
	if err != nil {
		t.Fatalf("read transcript artifact: %v", err)
	}
	var eventTypes []messages.StreamMessageType
	for _, line := range bytes.Split(transcriptBytes, []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var record transcript.Record
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatalf("decode transcript record: %v", err)
		}
		var event struct {
			Type messages.StreamMessageType `json:"type"`
		}
		if err := json.Unmarshal(record.Payload, &event); err != nil {
			t.Fatalf("decode transcript event: %v", err)
		}
		eventTypes = append(eventTypes, event.Type)
		if event.Type == messages.StreamTypeTextDelta && !bytes.Contains(record.Payload, []byte("accepted response")) {
			t.Fatalf("text transcript event = %q", record.Payload)
		}
		if event.Type == messages.StreamTypeSessionClose && !bytes.Contains(record.Payload, []byte("max_duration")) {
			t.Fatalf("terminal transcript event = %q", record.Payload)
		}
	}
	wantTypes := []messages.StreamMessageType{messages.StreamTypeAudioDelta, messages.StreamTypeTextDelta, messages.StreamTypeSessionClose}
	if len(eventTypes) != len(wantTypes) {
		t.Fatalf("transcript event types = %v, want %v", eventTypes, wantTypes)
	}
	for i, want := range wantTypes {
		if eventTypes[i] != want {
			t.Fatalf("transcript event types = %v, want %v", eventTypes, wantTypes)
		}
	}
}

type terminalRecorderProbe struct {
	summaries []transcript.RecordingTerminalSummary
}

func (r *terminalRecorderProbe) RecordTerminalSummary(summary transcript.RecordingTerminalSummary) error {
	r.summaries = append(r.summaries, summary)
	return nil
}

type runLoopProbe struct {
	deltas *messages.TypedBuffer[messages.StreamMessage]
	ran    chan struct{}
	close  sync.Once
}

func newRunLoopProbe() *runLoopProbe {
	return &runLoopProbe{deltas: messages.NewTypedBuffer[messages.StreamMessage](4), ran: make(chan struct{})}
}

func (l *runLoopProbe) Run(context.Context) error {
	l.close.Do(func() { close(l.ran) })
	return nil
}
func (l *runLoopProbe) Deltas() *messages.TypedBuffer[messages.StreamMessage] { return l.deltas }
func (l *runLoopProbe) Send(context.Context, []messages.Message) error        { return nil }

type gatedRunLoopProbe struct {
	deltas *messages.TypedBuffer[messages.StreamMessage]
	sent   bool
}

func (l *gatedRunLoopProbe) Run(ctx context.Context) error {
	if !l.deltas.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: &messages.MessageEndValue{}}) {
		return errors.New("could not publish loop delta")
	}
	<-ctx.Done()
	return ctx.Err()
}
func (l *gatedRunLoopProbe) Deltas() *messages.TypedBuffer[messages.StreamMessage] { return l.deltas }
func (l *gatedRunLoopProbe) Send(context.Context, []messages.Message) error {
	l.sent = true
	return nil
}

type idleRunLoopProbe struct {
	deltas *messages.TypedBuffer[messages.StreamMessage]
}

func (l *idleRunLoopProbe) Run(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}
func (l *idleRunLoopProbe) Deltas() *messages.TypedBuffer[messages.StreamMessage] { return l.deltas }
func (l *idleRunLoopProbe) Send(context.Context, []messages.Message) error        { return nil }

type contextWaitingRunLoopProbe struct {
	deltas  *messages.TypedBuffer[messages.StreamMessage]
	started chan struct{}
	once    sync.Once
}

func (l *contextWaitingRunLoopProbe) Run(ctx context.Context) error {
	l.once.Do(func() { close(l.started) })
	<-ctx.Done()
	return ctx.Err()
}
func (l *contextWaitingRunLoopProbe) Deltas() *messages.TypedBuffer[messages.StreamMessage] {
	return l.deltas
}
func (l *contextWaitingRunLoopProbe) Send(context.Context, []messages.Message) error { return nil }
