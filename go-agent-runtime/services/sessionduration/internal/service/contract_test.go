package service

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
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

func TestAdmissionContractForwardsCapabilitiesAndTerminalFacts(t *testing.T) {
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
	if !wrapped.SupportsResponseRequests() || wrapped.RequestResponse(context.Background()).Status != messages.SessionSendSucceeded || !inner.responseSent {
		t.Fatal("response request capability was not forwarded")
	}
	if !wrapped.SupportsCompleteMessages() || !wrapped.SupportsCompleteMessagesWithoutResponse() || !wrapped.SendMessage(context.Background(), messages.NewTextMessage(messages.RoleUser, "tool")) || !wrapped.SendMessageWithoutResponse(context.Background(), messages.NewTextMessage(messages.RoleUser, "tool")) || !inner.messageSent || !inner.withoutSent {
		t.Fatal("complete-message capability was not forwarded")
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
	if ArtifactsFromContext(nil) != nil {
		t.Fatal("nil context unexpectedly returned artifacts")
	}
	if _, ok := ArtifactPathsFromContext(nil); ok {
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
	ctx := WithSessionDurationArtifactPaths(nil, paths)
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
	if !l.deltas.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("hello")}) {
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
func (t *triggerTimer) Stop() bool               { return true }
func (t *triggerTimer) Reset(time.Duration) bool { return true }

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

func TestRunHandlesWakeAndDoneBoundaryFailures(t *testing.T) {
	wakeErr := errors.New("wake failed")
	doneErr := errors.New("done failed")
	tests := []struct {
		name      string
		wake      <-chan struct{}
		done      <-chan struct{}
		onWake    func(context.Context, sessionduration.Loop, sessionduration.Controller) error
		doneError func() error
		want      error
	}{
		{
			name: "wake",
			wake: func() <-chan struct{} {
				wake := make(chan struct{}, 1)
				wake <- struct{}{}
				return wake
			}(),
			onWake: func(context.Context, sessionduration.Loop, sessionduration.Controller) error { return wakeErr },
			want:   wakeErr,
		},
		{
			name: "done",
			done: func() <-chan struct{} {
				done := make(chan struct{})
				close(done)
				return done
			}(),
			doneError: func() error { return doneErr },
			want:      doneErr,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := sessionduration.RunRequest{
				Context:    context.Background(),
				Inferencer: contractInferencer{session: newContractSession()},
				LoopFactory: func(context.Context, sessionduration.AdmissionInferencer, sessionduration.Controller) (sessionduration.Loop, error) {
					return &idleRunLoopProbe{deltas: messages.NewTypedBuffer[messages.StreamMessage](1)}, nil
				},
				Wake:      test.wake,
				OnWake:    test.onWake,
				Done:      test.done,
				DoneError: test.doneError,
				Drain:     func(context.Context, sessionduration.Loop, sessionduration.Controller) error { return nil },
			}
			if err := New().Run(request); !errors.Is(err, test.want) {
				t.Fatalf("Run() = %v, want %v", err, test.want)
			}
		})
	}
}

func TestRunExpiresAtMaxDurationAndClosesLoop(t *testing.T) {
	scheduler := &triggerScheduler{created: make(chan *triggerTimer, 1)}
	result := make(chan error, 1)
	go func() {
		result <- New().Run(sessionduration.RunRequest{
			Context:     context.Background(),
			Inferencer:  contractInferencer{session: newContractSession()},
			Clock:       scheduler,
			MaxDuration: time.Second,
			LoopFactory: func(context.Context, sessionduration.AdmissionInferencer, sessionduration.Controller) (sessionduration.Loop, error) {
				return &idleRunLoopProbe{deltas: messages.NewTypedBuffer[messages.StreamMessage](1)}, nil
			},
			Drain: func(context.Context, sessionduration.Loop, sessionduration.Controller) error { return nil },
		})
	}()
	var timer *triggerTimer
	select {
	case timer = <-scheduler.created:
	case <-time.After(time.Second):
		t.Fatal("max-duration timer was not created")
	}
	timer.events <- time.Now()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("Run after max duration = %v, want bounded clean stop", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not finish after max duration")
	}
}

func TestWaitForLoopNormalizesCancellation(t *testing.T) {
	results := make(chan error, 2)
	results <- context.Canceled
	if err := waitForLoop(results); err != nil {
		t.Fatalf("waitForLoop(cancellation) = %v, want nil", err)
	}
	failure := errors.New("loop failed")
	results <- failure
	if err := waitForLoop(results); !errors.Is(err, failure) {
		t.Fatalf("waitForLoop(failure) = %v, want failure identity", err)
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

func TestControllerDrainsExpiredOutputAndReportsEmptyProviderResponse(t *testing.T) {
	controller, err := New().Begin(sessionduration.Options{Clock: testNoopScheduler{}})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := controller.Expire(); !errors.Is(err, sessionduration.ErrMaxDurationExceeded) {
		t.Fatalf("Expire: %v", err)
	}
	if admission := controller.Observe(messages.StreamMessage{Type: messages.StreamTypeError}); !admission.Accepted {
		t.Fatal("terminal error was rejected after expiry")
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

func TestServiceFacadeExposesDurationContract(t *testing.T) {
	service := New()
	if err := service.ValidateDuration(-time.Second); !errors.Is(err, sessionduration.ErrInvalidDuration) {
		t.Fatalf("ValidateDuration() = %v, want invalid-duration identity", err)
	}
	if err := service.ValidateDuration(0); err != nil {
		t.Fatalf("ValidateDuration(0) = %v", err)
	}

	providerTerminal := messages.StreamMessage{Type: messages.StreamTypeSessionClose, Value: messages.NewSessionCloseValueWithTerminal(
		"session", "provider", "provider_close", messages.TerminalReasonProviderClose,
		messages.TerminalProvenanceProvider, messages.TerminalOutputComplete,
	)}
	state := service.NewState(sessionduration.TerminalSource{
		Message: func() (messages.StreamMessage, bool) { return providerTerminal, true },
		Matches: func(msg messages.StreamMessage) bool { return msg.Type == messages.StreamTypeSessionClose },
	})
	state.Observe(messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant})
	if got := state.OutputState(); got != messages.TerminalOutputPartial {
		t.Fatalf("state output = %q, want partial", got)
	}
	if err := state.PublishProviderTerminal(sessionduration.Publication{}); err != nil {
		t.Fatalf("PublishProviderTerminal: %v", err)
	}
	if !state.Written() {
		t.Fatal("state did not retain terminal-written state")
	}
	if _, ok := state.Admit(true, providerTerminal); ok {
		t.Fatal("state admitted a duplicate provider terminal")
	}

	var published messages.StreamMessage
	if err := service.PublishMaxDuration(sessionduration.Publication{Write: func(msg messages.StreamMessage) error {
		published = msg
		return nil
	}}, messages.TerminalOutputPartial); err != nil {
		t.Fatalf("PublishMaxDuration: %v", err)
	}
	if published.Type != messages.StreamTypeSessionClose {
		t.Fatalf("max-duration publication type = %q", published.Type)
	}

	runtimeErr := errors.New("runtime")
	closeErr := errors.New("close")
	if got := service.LifecycleError(sessionduration.LifecycleFailures{Runtime: runtimeErr, Close: closeErr}); !errors.Is(got, runtimeErr) || !errors.Is(got, closeErr) {
		t.Fatalf("LifecycleError() = %v, missing causes", got)
	}
	if service.TransportError(nil) != nil || !errors.Is(service.TransportError(runtimeErr), runtimeErr) {
		t.Fatal("TransportError did not preserve nil and wrapped identities")
	}

	admission := service.NewEventAdmission()
	if admission == nil {
		t.Fatal("NewEventAdmission returned nil")
	}
	wrappedInferencer := service.NewAdmissionInferencer(contractInferencer{session: newContractSession()}, admission, nil)
	if wrappedInferencer == nil {
		t.Fatal("NewAdmissionInferencer returned nil")
	}
	wrapper := service.NewAdmissionSession(context.Background(), newContractSession(), admission, nil)
	if wrapper == nil || !wrapper.SupportsCompleteMessages() || !wrapper.SupportsCompleteMessagesWithoutResponse() {
		t.Fatal("NewAdmissionSession did not preserve complete-message capabilities")
	}
	if err := wrapper.Close(); err != nil {
		t.Fatalf("wrapped session Close: %v", err)
	}
	if service.NewAdmissionInferencer(contractInferencer{session: newContractSession()}, struct{}{}, nil) == nil {
		t.Fatal("NewAdmissionInferencer rejected an opaque admission boundary")
	}

	artifact := NewSessionDurationArtifactSetWithSinks(nil, &artifactTranscriptSink{})
	ctx := service.WithArtifacts(context.Background(), artifact)
	if service.ArtifactsFromContext(ctx) != artifact {
		t.Fatal("ArtifactsFromContext did not return the attached lifecycle")
	}
	ctx = service.WithTerminalRecorder(ctx, &terminalRecorderProbe{})
	if service.ArtifactsFromContext(ctx) == nil {
		t.Fatal("WithTerminalRecorder removed the artifact lifecycle")
	}
	if service.WithTerminalRecorder(ctx, nil) != ctx {
		t.Fatal("WithTerminalRecorder(nil) did not preserve the context")
	}
	ctx = service.WithArtifactPaths(ctx, sessionduration.SessionDurationArtifactPaths{AudioPath: "audio", TranscriptPath: "transcript"})
	if _, ok := ArtifactPathsFromContext(ctx); !ok {
		t.Fatal("WithArtifactPaths did not retain paths")
	}
	if _, err := service.PrepareArtifacts(context.Background()); err != nil {
		t.Fatalf("PrepareArtifacts with no paths: %v", err)
	}
	if err := service.FinalizeArtifacts(nil); err != nil {
		t.Fatalf("FinalizeArtifacts(nil): %v", err)
	}

	retryTerminal := &messages.MessageEndValue{Status: "failed", ProviderErrorCode: "rate_limit_exceeded", ProviderErrorMessage: "please try again in 3s"}
	if decision := service.EvaluateRetry(sessionduration.RetryPolicy{Enabled: true}, retryTerminal); !decision.Eligible || decision.Delay != 3*time.Second {
		t.Fatalf("EvaluateRetry() = %+v", decision)
	}
	statusRetry := &messages.MessageEndValue{Status: "failed", StatusDetails: "code=rate_limit_exceeded,message=please try again in 1.5s"}
	if decision := service.EvaluateRetry(sessionduration.RetryPolicy{Enabled: true}, statusRetry); !decision.Eligible || decision.Delay != 1500*time.Millisecond {
		t.Fatalf("EvaluateRetry(status details) = %+v", decision)
	}
	if decision := service.EvaluateRetry(sessionduration.RetryPolicy{Enabled: true}, nil); decision != (sessionduration.RetryDecision{}) {
		t.Fatalf("EvaluateRetry(nil) = %+v, want empty decision", decision)
	}
	if !service.IsDurationShutdownMessage(providerTerminal) || !service.IsDurationForwardMessage(messages.StreamMessage{Type: messages.StreamTypeError}) {
		t.Fatal("duration message classification did not preserve terminal/error forwarding")
	}
	if summary, present, err := service.RecordingTerminalSummaryFromMessage(providerTerminal); err != nil || !present || summary == nil {
		t.Fatalf("RecordingTerminalSummaryFromMessage() = %+v, %v, %v", summary, present, err)
	}
	if _, present, err := service.RecordingTerminalSummaryFromMessage(messages.StreamMessage{Type: messages.StreamTypeSessionClose, Value: &messages.SessionCloseValue{Classification: "incomplete"}}); present || err == nil {
		t.Fatalf("invalid terminal summary = present:%v err:%v, want validation error", present, err)
	}
}

func newRunLoopProbeWithNilDeltas() *runLoopProbe {
	return &runLoopProbe{ran: make(chan struct{})}
}
