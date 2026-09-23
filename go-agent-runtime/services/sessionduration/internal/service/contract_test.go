package service

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
)

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

type artifactAudioSink struct {
	samples  []int16
	flushErr error
	closeErr error
}

func (s *artifactAudioSink) WriteSamples(samples []int16) error {
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
}

func TestRunPublishesAdmittedMessageAndPerformsPlannedBoundedStop(t *testing.T) {
	loop := &gatedRunLoopProbe{deltas: messages.NewTypedBuffer[messages.StreamMessage](1)}
	var published []messages.StreamMessage
	err := New().Run(sessionduration.RunRequest{
		Context:    context.Background(),
		Inferencer: contractInferencer{session: newContractSession()},
		LoopFactory: func(context.Context, sessionduration.AdmissionInferencer, sessionduration.Controller) (sessionduration.Loop, error) {
			return loop, nil
		},
		Publication: sessionduration.Publication{Write: func(msg messages.StreamMessage) error {
			published = append(published, msg)
			return nil
		}},
		Handle: func(context.Context, sessionduration.Loop, sessionduration.Controller, messages.StreamMessage) (sessionduration.MessageResult, error) {
			return sessionduration.MessageResult{Stop: true, Planned: true}, nil
		},
		Drain: func(context.Context, sessionduration.Loop, sessionduration.Controller) error { return nil },
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(published) != 1 || published[0].Type != messages.StreamTypeTextDelta {
		t.Fatalf("published messages = %+v, want one admitted delta", published)
	}
	if !loop.sent {
		t.Fatal("planned stop did not send the loop close control message")
	}
}

func TestRunOwnsLoopExecutionAndBoundedCleanup(t *testing.T) {
	loop := newRunLoopProbe()
	var drained, closed bool
	err := New().Run(sessionduration.RunRequest{
		Context:    context.Background(),
		Inferencer: contractInferencer{session: newContractSession()},
		Clock:      testNoopScheduler{},
		LoopFactory: func(context.Context, sessionduration.AdmissionInferencer, sessionduration.Controller) (sessionduration.Loop, error) {
			return loop, nil
		},
		Drain: func(context.Context, sessionduration.Loop, sessionduration.Controller) error {
			drained = true
			return nil
		},
		Close: func() error {
			closed = true
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	select {
	case <-loop.ran:
	case <-time.After(time.Second):
		t.Fatal("Run did not start the loop")
	}
	if !drained || !closed {
		t.Fatalf("cleanup callbacks drained=%v closed=%v", drained, closed)
	}
}

func TestRunCancelsLoopBeforeWaitingWhenDrainCallbackIsMissing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	loop := &contextWaitingRunLoopProbe{
		deltas:  messages.NewTypedBuffer[messages.StreamMessage](1),
		started: make(chan struct{}),
	}
	done := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- New().Run(sessionduration.RunRequest{
			Context:    ctx,
			Inferencer: contractInferencer{session: newContractSession()},
			LoopFactory: func(context.Context, sessionduration.AdmissionInferencer, sessionduration.Controller) (sessionduration.Loop, error) {
				return loop, nil
			},
			Done: done,
		})
	}()
	select {
	case <-loop.started:
	case <-time.After(time.Second):
		t.Fatal("session loop did not start")
	}
	close(done)
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(time.Second):
		cancel()
		select {
		case <-result:
		case <-time.After(time.Second):
		}
		t.Fatal("Run waited for the loop before canceling it")
	}
}

func TestControllerDrainsExpiredOutputAndReportsEmptyProviderResponse(t *testing.T) {
	controller, err := New().Begin(sessionduration.Options{Clock: testNoopScheduler{}})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := controller.Expire(); !errors.Is(err, sessionduration.ErrMaxDurationExceeded) {
		t.Fatalf("Expire: %v", err)
	}
	if admission := controller.Observe(messages.StreamMessage{Type: messages.StreamTypeError}); admission.Accepted {
		t.Fatal("terminal error was admitted after expiry")
	}
	if admission := controller.ObserveDrain(messages.StreamMessage{Type: messages.StreamTypeError}); admission.Accepted {
		t.Fatal("terminal error was admitted during post-expiry drain")
	}
	if admission := controller.ObserveDrain(messages.StreamMessage{Type: messages.StreamTypeTextDelta}); !admission.Accepted {
		t.Fatal("expired output was not admitted during bounded drain")
	}
	if admission := controller.ObserveDrain(messages.StreamMessage{Type: messages.StreamTypeSessionClose}); !admission.Accepted || !admission.TerminalSeen {
		t.Fatalf("drained provider terminal admission = %+v", admission)
	}
	if admission := controller.ObserveDrain(messages.StreamMessage{Type: messages.StreamTypeSessionClose}); admission.Accepted {
		t.Fatal("duplicate provider terminal was admitted during drain")
	}
	if _, err := controller.Finalize(context.Background(), sessionduration.FinalizeRequest{}); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if admission := controller.Observe(messages.StreamMessage{Type: messages.StreamTypeTextDelta}); admission.Accepted {
		t.Fatal("closed controller admitted output")
	}
	if admission := controller.ObserveDrain(messages.StreamMessage{Type: messages.StreamTypeTextDelta}); admission.Accepted {
		t.Fatal("closed controller admitted output after finalization")
	}

	liveness, err := New().Begin(sessionduration.Options{
		Clock:    testNoopScheduler{},
		Liveness: sessionduration.LivenessOptions{Enabled: true, Timeout: time.Second},
	})
	if err != nil {
		t.Fatalf("Begin liveness: %v", err)
	}
	liveness.Observe(messages.StreamMessage{Type: messages.StreamTypeMessageStart})
	empty := messages.StreamMessage{
		Type: messages.StreamTypeMessageEnd,
		Value: &messages.MessageEndValue{
			TerminalReason: messages.TerminalReasonPartialOutput,
			OutputState:    messages.TerminalOutputNone,
		},
	}
	admission := liveness.Observe(empty)
	if !errors.Is(admission.LivenessErr, sessionduration.ErrProviderEmptyResponse) {
		t.Fatalf("empty response admission = %+v, want typed liveness failure", admission)
	}
	if !errors.Is(liveness.LivenessFailure(), sessionduration.ErrProviderEmptyResponse) {
		t.Fatalf("LivenessFailure() = %v, want empty-response identity", liveness.LivenessFailure())
	}
	if _, err := liveness.Finalize(context.Background(), sessionduration.FinalizeRequest{}); err != nil {
		t.Fatalf("Finalize liveness: %v", err)
	}
}

func TestRunRejectsInvalidRequestsBeforeStartingResources(t *testing.T) {
	service := New()
	validInferencer := contractInferencer{session: newContractSession()}
	cases := []struct {
		name    string
		request sessionduration.RunRequest
		want    string
	}{
		{name: "negative duration", request: sessionduration.RunRequest{Inferencer: validInferencer, MaxDuration: -time.Second, LoopFactory: func(context.Context, sessionduration.AdmissionInferencer, sessionduration.Controller) (sessionduration.Loop, error) {
			t.Fatal("loop factory called")
			return nil, nil
		}}, want: "--max-duration"},
		{name: "missing inferencer", request: sessionduration.RunRequest{LoopFactory: func(context.Context, sessionduration.AdmissionInferencer, sessionduration.Controller) (sessionduration.Loop, error) {
			return newRunLoopProbe(), nil
		}}, want: "inferencer is required"},
		{name: "missing loop factory", request: sessionduration.RunRequest{Inferencer: validInferencer}, want: "loop factory is required"},
		{name: "loop factory failure", request: sessionduration.RunRequest{Inferencer: validInferencer, LoopFactory: func(context.Context, sessionduration.AdmissionInferencer, sessionduration.Controller) (sessionduration.Loop, error) {
			return nil, errors.New("factory failed")
		}}, want: "factory failed"},
		{name: "invalid loop", request: sessionduration.RunRequest{Inferencer: validInferencer, LoopFactory: func(context.Context, sessionduration.AdmissionInferencer, sessionduration.Controller) (sessionduration.Loop, error) {
			return newRunLoopProbeWithNilDeltas(), nil
		}}, want: "invalid loop"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			err := service.Run(test.request)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Run() = %v, want %q", err, test.want)
			}
		})
	}
}

type retryRunLoopProbe struct {
	deltas *messages.TypedBuffer[messages.StreamMessage]
	sent   chan messages.StreamMessage
}

func (l *retryRunLoopProbe) Run(ctx context.Context) error {
	terminal := messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: &messages.MessageEndValue{Status: "failed", ProviderErrorCode: "rate_limit_exceeded", ProviderErrorMessage: "retry after 1s"}}
	if !l.deltas.Write(ctx, terminal) {
		return ctx.Err()
	}
	<-ctx.Done()
	return ctx.Err()
}
func (l *retryRunLoopProbe) Deltas() *messages.TypedBuffer[messages.StreamMessage] { return l.deltas }
func (l *retryRunLoopProbe) Send(context.Context, []messages.Message) error        { return nil }
func (l *retryRunLoopProbe) SendSessionEvent(ctx context.Context, msg messages.StreamMessage) error {
	select {
	case l.sent <- msg:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type retryRunLoopWithoutSessionEvents struct {
	deltas *messages.TypedBuffer[messages.StreamMessage]
}

func (l *retryRunLoopWithoutSessionEvents) Run(ctx context.Context) error {
	terminal := messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: &messages.MessageEndValue{Status: "failed", ProviderErrorCode: "rate_limit_exceeded"}}
	if !l.deltas.Write(ctx, terminal) {
		return ctx.Err()
	}
	<-ctx.Done()
	return ctx.Err()
}
func (l *retryRunLoopWithoutSessionEvents) Deltas() *messages.TypedBuffer[messages.StreamMessage] {
	return l.deltas
}
func (l *retryRunLoopWithoutSessionEvents) Send(context.Context, []messages.Message) error {
	return nil
}

type triggerScheduler struct {
	created chan *triggerTimer
}

type triggerTimer struct {
	events chan time.Time
}

func (s *triggerScheduler) NewTimer(time.Duration) sessionduration.Timer {
	timer := &triggerTimer{events: make(chan time.Time, 1)}
	s.created <- timer
	return timer
}
func (t *triggerTimer) C() <-chan time.Time      { return t.events }
func (t *triggerTimer) Stop() bool               { return false }
func (t *triggerTimer) Reset(time.Duration) bool { return true }
