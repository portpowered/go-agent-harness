package services

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/wavio"
)

func TestRunSessionWithMaxDuration_RejectsNegativeBeforePlanning(t *testing.T) {
	inferencer := &durationTestInferencer{}
	artifactDir := t.TempDir()
	wavPath := filepath.Join(artifactDir, "negative.wav")
	transcriptPath := filepath.Join(artifactDir, "negative.jsonl")
	err := RunSessionWithMaxDuration(WithSessionDurationArtifactPaths(context.Background(), SessionDurationArtifactPaths{
		AudioPath:      wavPath,
		TranscriptPath: transcriptPath,
	}), io.Discard, SessionRunOptions{
		SessionInferencer: inferencer,
	}, -time.Millisecond)
	if err == nil {
		t.Fatal("negative max duration returned nil")
	}
	var durationErr *SessionMaxDurationError
	if !errors.As(err, &durationErr) {
		t.Fatalf("error type = %T, want *SessionMaxDurationError: %v", err, err)
	}
	if !errors.Is(err, ErrInvalidSessionMaxDuration) {
		t.Fatalf("error does not preserve ErrInvalidSessionMaxDuration: %v", err)
	}
	if inferencer.connected {
		t.Fatal("negative duration started the injected session")
	}
	if _, statErr := os.Stat(wavPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("negative duration opened WAV artifact: %v", statErr)
	}
	if _, statErr := os.Stat(transcriptPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("negative duration opened transcript artifact: %v", statErr)
	}
}

func TestRunSessionWithMaxDuration_ZeroDoesNotCreateTimer(t *testing.T) {
	clock := &durationTestClock{}
	var out bytes.Buffer
	err := RunSessionWithMaxDurationClock(context.Background(), &out, SessionRunOptions{
		ReplayPath:        "synthetic.session.json",
		SessionInferencer: &durationTestInferencer{events: durationNaturalEvents()},
	}, 0, clock)
	if err != nil {
		t.Fatalf("zero max duration: %v", err)
	}
	if clock.calls != 0 {
		t.Fatalf("zero max duration created %d timers, want 0", clock.calls)
	}
	if !strings.Contains(out.String(), "accepted output") || strings.Contains(out.String(), string(SessionMaxDurationReason)) {
		t.Fatalf("zero duration did not preserve natural output/reason: %q", out.String())
	}
}

func TestSessionDurationAdmission_PreservesCompleteMessageCapabilities(t *testing.T) {
	inner := &durationCompleteMessageSession{
		complete:        true,
		withoutResponse: true,
	}
	wrapped := &sessionDurationAdmissionSession{inner: inner}
	message := messages.NewTextMessage(messages.RoleUser, "image result")

	if !wrapped.SendMessage(context.Background(), message) {
		t.Fatal("duration admission rejected a complete message")
	}
	if !wrapped.SendMessageWithoutResponse(context.Background(), message) {
		t.Fatal("duration admission rejected a deferred complete message")
	}
	if !wrapped.SupportsCompleteMessages() {
		t.Fatal("duration admission hid complete-message capability")
	}
	if !wrapped.SupportsCompleteMessagesWithoutResponse() {
		t.Fatal("duration admission hid deferred complete-message capability")
	}
	if len(inner.messages) != 1 || len(inner.deferredMessages) != 1 {
		t.Fatalf("forwarded complete messages = %d/%d, want one of each", len(inner.messages), len(inner.deferredMessages))
	}
}

func TestSessionDurationAdmission_ForwardsNonTerminalDiagnosticWithoutShutdown(t *testing.T) {
	msg := messages.StreamMessage{
		Type:  messages.StreamTypeError,
		Value: messages.NewNonTerminalErrorValue("response is not active", "response_cancel_not_active"),
	}
	if isDurationShutdownMessage(msg) {
		t.Fatal("nonterminal provider diagnostic is a shutdown message")
	}
	if !isDurationForwardMessage(msg) {
		t.Fatal("nonterminal provider diagnostic was not retained for forwarding")
	}
}

func TestSessionCommandHelpAndOmittedDurationBehavior(t *testing.T) {
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller did not return the test file")
	}
	moduleDir := filepath.Clean(filepath.Join(filepath.Dir(testFile), "..", ".."))
	// A bare session is now a live-device admission path. Keep this help smoke
	// test on an explicit non-session-mode invocation so it never needs a host
	// credential or audio device.
	for _, args := range [][]string{{"session", "--help"}, {"session", "--prompt="}} {
		cmd := exec.Command("go", append([]string{"run", "./cmd/agent"}, args...)...)
		cmd.Dir = moduleDir
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("agent %v: %v\n%s", args, err, output)
		}
		if !strings.Contains(string(output), "--max-duration") {
			t.Fatalf("agent %v help omitted --max-duration:\n%s", args, output)
		}
	}
}

func TestRunSessionWithMaxDuration_S2Table(t *testing.T) {
	cases := []struct {
		name          string
		maxDuration   time.Duration
		wantTimerCall int
		wantReason    string
	}{
		{name: "omitted", maxDuration: 0, wantTimerCall: 0, wantReason: "provider_close"},
		{name: "zero", maxDuration: 0, wantTimerCall: 0, wantReason: "provider_close"},
		{name: "negative", maxDuration: -time.Millisecond, wantTimerCall: 0},
		{name: "shorter_than_one_frame", maxDuration: time.Nanosecond, wantTimerCall: 1, wantReason: string(SessionMaxDurationReason)},
		{name: "deadline_during_output", maxDuration: time.Minute, wantTimerCall: 1, wantReason: string(SessionMaxDurationReason)},
		{name: "longer_than_session", maxDuration: time.Hour, wantTimerCall: 1, wantReason: "provider_close"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			clock := &durationTestClock{}
			if testCase.maxDuration < 0 {
				inferencer := &durationTestInferencer{}
				err := RunSessionWithMaxDurationClock(context.Background(), io.Discard, SessionRunOptions{SessionInferencer: inferencer}, testCase.maxDuration, clock)
				var durationErr *SessionMaxDurationError
				if !errors.As(err, &durationErr) || inferencer.connected || clock.calls != testCase.wantTimerCall {
					t.Fatalf("negative case error=%v connected=%v timer_calls=%d", err, inferencer.connected, clock.calls)
				}
				return
			}

			if testCase.maxDuration == 0 {
				var out bytes.Buffer
				err := RunSessionWithMaxDurationClock(context.Background(), &out, SessionRunOptions{
					ReplayPath:        "synthetic.session.json",
					SessionInferencer: &durationTestInferencer{events: durationNaturalEvents()},
				}, testCase.maxDuration, clock)
				if err != nil {
					t.Fatalf("unbounded case: %v", err)
				}
				if !strings.Contains(out.String(), "terminal_reason=provider_close") {
					t.Fatalf("unbounded case lost natural terminal reason: %q", out.String())
				}
			} else {
				writer := newDurationTestWriter()
				events := durationOutputEvents()
				closeAfterEvents := false
				if testCase.name == "longer_than_session" {
					events = durationNaturalEvents()
					closeAfterEvents = true
				}
				inferencer := &durationTestInferencer{
					events:           events,
					connectedCh:      make(chan struct{}),
					closeAfterEvents: closeAfterEvents,
				}
				runErrCh := make(chan error, 1)
				go func() {
					runErrCh <- runAgentLoopSessionWithDurationClock(context.Background(), writer, inferencer, sessionLoopOptions{}, testCase.maxDuration, clock)
				}()
				select {
				case <-inferencer.connectedCh:
				case <-time.After(2 * time.Second):
					t.Fatal("session did not connect")
				}
				if testCase.name == "deadline_during_output" {
					writer.waitFor(t, "accepted output")
				}
				if testCase.name != "longer_than_session" {
					clock.fire()
				}
				select {
				case err := <-runErrCh:
					if err != nil {
						t.Fatalf("bounded case: %v", err)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("bounded case did not finish")
				}
				if !strings.Contains(writer.String(), "terminal_reason="+testCase.wantReason) {
					t.Fatalf("bounded case terminal output = %q", writer.String())
				}
			}
			if clock.calls != testCase.wantTimerCall || testCase.maxDuration > 0 && (clock.timer == nil || !clock.timer.stopped) {
				t.Fatalf("timer lifecycle calls=%d timer=%v", clock.calls, clock.timer)
			}
		})
	}
}

func TestRunSessionWithMaxDuration_GracefullyClosesAtDeadline(t *testing.T) {
	clock := &durationTestClock{}
	writer := newDurationTestWriter()
	runErrCh := make(chan error, 1)
	go func() {
		runErrCh <- runAgentLoopSessionWithDurationClock(
			context.Background(),
			writer,
			&durationTestInferencer{events: durationOutputEvents()},
			sessionLoopOptions{},
			time.Minute,
			clock,
		)
	}()

	writer.waitFor(t, "accepted output")
	clock.fire()
	select {
	case err := <-runErrCh:
		if err != nil {
			t.Fatalf("duration cutoff returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("duration cutoff did not finish")
	}

	got := writer.String()
	for _, want := range []string{
		"accepted output",
		"[session closed: max_duration]",
		"terminal_reason=max_duration",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("duration output missing %q: %q", want, got)
		}
	}
	if clock.calls != 1 || !clock.timer.stopped {
		t.Fatalf("duration timer lifecycle = calls:%d stopped:%v, want one stopped timer", clock.calls, clock.timer.stopped)
	}
}

func TestRunSessionWithMaxDuration_NaturalCompletionKeepsNaturalReason(t *testing.T) {
	clock := &durationTestClock{}
	var out bytes.Buffer
	err := runAgentLoopSessionWithDurationClock(
		context.Background(),
		&out,
		&durationTestInferencer{events: durationNaturalEvents()},
		sessionLoopOptions{},
		time.Hour,
		clock,
	)
	if err != nil {
		t.Fatalf("natural completion: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "terminal_reason=provider_close") {
		t.Fatalf("natural completion lost provider reason: %q", got)
	}
	if strings.Contains(got, string(SessionMaxDurationReason)) {
		t.Fatalf("natural completion was mislabeled as max duration: %q", got)
	}
	if clock.calls != 1 || !clock.timer.stopped {
		t.Fatalf("natural timer lifecycle = calls:%d stopped:%v, want one stopped timer", clock.calls, clock.timer.stopped)
	}
}

func TestRunSessionWithMaxDuration_PreservesProviderTerminalDuringShutdown(t *testing.T) {
	clock := &durationTestClock{}
	writer := newDurationTestWriter()
	providerTerminal := messages.StreamMessage{
		Type: messages.StreamTypeSessionClose,
		Value: messages.NewSessionCloseValueWithTerminal(
			"duration-session",
			"provider_shutdown",
			"transport",
			messages.TerminalReasonProviderClose,
			messages.TerminalProvenanceProvider,
			messages.TerminalOutputPartial,
		),
	}
	inferencer := &durationTestInferencer{
		events:               durationOutputEvents(),
		providerCloseOnClose: &providerTerminal,
	}
	runErrCh := make(chan error, 1)
	go func() {
		runErrCh <- runAgentLoopSessionWithDurationClock(
			context.Background(),
			writer,
			inferencer,
			sessionLoopOptions{},
			time.Minute,
			clock,
		)
	}()

	writer.waitFor(t, "accepted output")
	clock.fire()
	select {
	case err := <-runErrCh:
		if err != nil {
			t.Fatalf("duration cutoff returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("duration cutoff did not finish")
	}

	got := writer.String()
	if !strings.Contains(got, "provider_shutdown") {
		t.Fatalf("duration output lost provider terminal: %q", got)
	}
	if !strings.Contains(got, "terminal_provenance=provider") {
		t.Fatalf("duration output lost provider provenance: %q", got)
	}
	if !strings.Contains(got, "output_state=partial") {
		t.Fatalf("duration output lost partial output state: %q", got)
	}
	if strings.Contains(got, "terminal_reason=max_duration") {
		t.Fatalf("duration output rewrote provider terminal as max duration: %q", got)
	}
}

func durationOutputEvents() []messages.StreamMessage {
	return []messages.StreamMessage{
		{Type: messages.StreamTypeSessionOpen, Value: messages.NewSessionOpenValue("duration-session", "test")},
		{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("accepted output")},
	}
}

func durationArtifactEvents() []messages.StreamMessage {
	return []messages.StreamMessage{
		{Type: messages.StreamTypeSessionOpen, Value: messages.NewSessionOpenValue("duration-session", "test")},
		{Type: messages.StreamTypeAudioStart, Value: messages.NewAudioStartValue()},
		{Type: messages.StreamTypeAudioDelta, Value: messages.NewAudioDeltaValue([]byte{1, 0, 2, 0})},
		{Type: messages.StreamTypeTranscriptStart, Value: messages.NewTranscriptStartValue()},
		{Type: messages.StreamTypeTranscriptDelta, Value: messages.NewTranscriptDeltaValue("hello")},
		{Type: messages.StreamTypeTranscriptEnd, Value: messages.NewTranscriptEndValue("hello")},
		{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("artifact-ready")},
	}
}

func durationNaturalEvents() []messages.StreamMessage {
	return append(durationOutputEvents(), messages.StreamMessage{
		Type: messages.StreamTypeSessionClose,
		Value: messages.NewSessionCloseValueWithTerminal(
			"duration-session",
			"provider_closed",
			string(messages.TerminalReasonProviderClose),
			messages.TerminalReasonProviderClose,
			messages.TerminalProvenanceProvider,
			messages.TerminalOutputNotApplicable,
		),
	})
}

// durationTestClock is shared between the test goroutine, which fires the
// deadline, and the session goroutine, which creates the timer after the loop
// starts. A fire can therefore land before NewTimer under load; the clock
// records it as pending so NewTimer delivers it instead of dropping it. The
// mutex makes the timer hand-off race-free.
type durationTestClock struct {
	mu      sync.Mutex
	timer   *durationTestTimer
	pending bool
	calls   int
}

func (c *durationTestClock) NewTimer(time.Duration) SessionDurationTimer {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	c.timer = &durationTestTimer{ch: make(chan time.Time, 1)}
	if c.pending {
		c.pending = false
		c.timer.ch <- time.Time{}
	}
	return c.timer
}

func (c *durationTestClock) fire() {
	c.mu.Lock()
	c.pending = true
	timer := c.timer
	c.mu.Unlock()
	if timer != nil {
		c.mu.Lock()
		c.pending = false
		c.mu.Unlock()
		timer.ch <- time.Time{}
	}
}

type durationTestTimer struct {
	ch      chan time.Time
	stopped bool
}

func (t *durationTestTimer) C() <-chan time.Time { return t.ch }

func (t *durationTestTimer) Stop() bool {
	t.stopped = true
	return true
}

type durationTestInferencer struct {
	events               []messages.StreamMessage
	connected            bool
	connectedCh          chan struct{}
	closeAfterEvents     bool
	providerCloseOnClose *messages.StreamMessage
	session              *durationTestSession
	connectErr           error
	sessionCloseErr      error
}

func (i *durationTestInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	if i.connectErr != nil {
		return nil, i.connectErr
	}
	i.connected = true
	session := newDurationTestSession()
	session.closeErr = i.sessionCloseErr
	session.providerCloseOnClose = i.providerCloseOnClose
	i.session = session
	if i.connectedCh != nil {
		close(i.connectedCh)
	}
	for _, event := range i.events {
		if !session.receive.Write(ctx, event) {
			return nil, ctx.Err()
		}
	}
	if i.closeAfterEvents {
		session.end()
	}
	return session, nil
}

type durationTestSession struct {
	receive              *messages.TypedBuffer[messages.StreamMessage]
	done                 chan struct{}
	closeCh              chan struct{}
	once                 sync.Once
	closeCallOnce        sync.Once
	closeErr             error
	closeCount           int
	providerCloseOnClose *messages.StreamMessage
}

func newDurationTestSession() *durationTestSession {
	return &durationTestSession{
		receive: messages.NewTypedBuffer[messages.StreamMessage](16),
		done:    make(chan struct{}),
		closeCh: make(chan struct{}),
	}
}

func (s *durationTestSession) Send(context.Context, messages.StreamMessage) bool { return true }

func (s *durationTestSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.receive
}

func (s *durationTestSession) Done() <-chan struct{} { return s.done }

func (s *durationTestSession) Close() error {
	s.closeCount++
	s.closeCallOnce.Do(func() { close(s.closeCh) })
	if s.providerCloseOnClose != nil {
		s.receive.Write(context.Background(), *s.providerCloseOnClose)
	}
	s.end()
	return s.closeErr
}

func (s *durationTestSession) end() {
	s.once.Do(func() { close(s.done) })
}

type durationTestWriter struct {
	mu     sync.Mutex
	output bytes.Buffer
	writes chan string
}

func newDurationTestWriter() *durationTestWriter {
	return &durationTestWriter{writes: make(chan string, 16)}
}

func (w *durationTestWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	n, err := w.output.Write(p)
	w.mu.Unlock()
	select {
	case w.writes <- string(p):
	default:
	}
	return n, err
}

func (w *durationTestWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.output.String()
}

func (w *durationTestWriter) waitFor(t *testing.T, want string) {
	t.Helper()
	for {
		select {
		case got := <-w.writes:
			if strings.Contains(got, want) {
				return
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for %q; output=%q", want, w.String())
		}
	}
}

func TestRunAgentLoopSessionWithDuration_ProviderDoneDrainsAcceptedOutput(t *testing.T) {
	clock := &durationTestClock{}
	var out bytes.Buffer
	err := runAgentLoopSessionWithDurationClock(
		context.Background(),
		&out,
		&durationTestInferencer{
			events:           durationOutputEvents(),
			closeAfterEvents: true,
		},
		sessionLoopOptions{},
		time.Hour,
		clock,
	)
	if err != nil {
		t.Fatalf("provider Done drain: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "accepted output") {
		t.Fatalf("provider Done dropped accepted output: %q", got)
	}
	if !strings.Contains(got, "terminal_reason=provider_close") {
		t.Fatalf("provider Done lost its terminal reason: %q", got)
	}
}

func TestRunSessionWithMaxDuration_FinalizesRealArtifactsAndRejectsLateFrame(t *testing.T) {
	artifactDir := t.TempDir()
	wavPath := filepath.Join(artifactDir, "cutoff.wav")
	transcriptPath := filepath.Join(artifactDir, "cutoff.jsonl")

	clock := &durationTestClock{}
	inferencer := &durationTestInferencer{
		events: durationArtifactEvents(),
	}
	writer := newDurationTestWriter()
	runErrCh := make(chan error, 1)
	ctx := WithSessionDurationArtifactPaths(context.Background(), SessionDurationArtifactPaths{
		AudioPath:      wavPath,
		TranscriptPath: transcriptPath,
	})
	go func() {
		runErrCh <- RunSessionWithMaxDurationClock(ctx, writer, SessionRunOptions{
			ReplayPath:        filepath.Join(artifactDir, "fixture.session.json"),
			SessionInferencer: inferencer,
		}, time.Nanosecond, clock)
	}()

	// The ready marker is emitted only after the production artifact lifecycle
	// has accepted every preceding audio/transcript message.
	writer.waitFor(t, "artifact-ready")
	clock.fire()
	if inferencer.session == nil {
		t.Fatal("duration session was not connected")
	}
	select {
	case <-inferencer.session.closeCh:
	case <-time.After(2 * time.Second):
		t.Fatal("duration cutoff did not close provider session")
	}

	// The provider close call occurs after the admission boundary is closed, so
	// this frame is deterministically late even though the fake provider buffer
	// itself can still accept it.
	_ = inferencer.session.receive.Write(context.Background(), messages.StreamMessage{
		Type:  messages.StreamTypeAudioDelta,
		Value: messages.NewAudioDeltaValue([]byte{3, 0}),
	})
	if err := <-runErrCh; err != nil {
		t.Fatalf("duration cutoff: %v", err)
	}
	if got := writer.String(); !strings.Contains(got, "terminal_reason=max_duration") {
		t.Fatalf("duration output missing max_duration: %q", got)
	}

	wavFile, err := os.Open(wavPath)
	if err != nil {
		t.Fatalf("reopen finalized WAV artifact: %v", err)
	}
	rate, samples, readErr := wavio.Read(wavFile)
	closeErr := wavFile.Close()
	if readErr != nil {
		t.Fatalf("read finalized WAV artifact: %v", readErr)
	}
	if closeErr != nil {
		t.Fatalf("close reopened WAV artifact: %v", closeErr)
	}
	if rate != wavio.Rate16kHz {
		t.Fatalf("WAV sample rate = %d, want %d", rate, wavio.Rate16kHz)
	}
	if len(samples) != 2 || len(samples) >= audio.FrameSize {
		t.Fatalf("WAV sample count = %d, want 2 and shorter than one frame (%d)", len(samples), audio.FrameSize)
	}
	if samples[0] != 1 || samples[1] != 2 {
		t.Fatalf("WAV samples = %v, want [1 2] and no late frame", samples)
	}

	transcriptData, err := os.ReadFile(transcriptPath)
	if err != nil {
		t.Fatalf("reopen finalized transcript artifact: %v", err)
	}
	if !bytes.HasSuffix(transcriptData, []byte("\n")) {
		t.Fatal("finalized transcript is missing its trailing JSONL newline")
	}
	scanner := bufio.NewScanner(bytes.NewReader(transcriptData))
	var eventTypes []messages.StreamMessageType
	var terminalPayload []byte
	for scanner.Scan() {
		record, err := transcript.Decode(scanner.Bytes())
		if err != nil {
			t.Fatalf("decode transcript JSONL record: %v", err)
		}
		var event struct {
			Type messages.StreamMessageType `json:"type"`
		}
		if err := json.Unmarshal(record.Payload, &event); err != nil {
			t.Fatalf("decode transcript event payload: %v", err)
		}
		eventTypes = append(eventTypes, event.Type)
		if event.Type == messages.StreamTypeSessionClose {
			terminalPayload = append([]byte(nil), record.Payload...)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan transcript JSONL: %v", err)
	}
	wantTypes := []messages.StreamMessageType{
		messages.StreamTypeSessionOpen,
		messages.StreamTypeAudioStart,
		messages.StreamTypeAudioDelta,
		messages.StreamTypeTranscriptStart,
		messages.StreamTypeTranscriptDelta,
		messages.StreamTypeTranscriptEnd,
		messages.StreamTypeTextDelta,
		messages.StreamTypeSessionClose,
	}
	if !reflect.DeepEqual(eventTypes, wantTypes) {
		t.Fatalf("transcript event order = %v, want %v", eventTypes, wantTypes)
	}
	if !bytes.Contains(terminalPayload, []byte("max_duration")) {
		t.Fatalf("transcript terminal record = %s, want max_duration", terminalPayload)
	}
	for _, want := range []string{"\"terminal_provenance\":\"loop\"", "\"output_state\":\"partial\""} {
		if !bytes.Contains(terminalPayload, []byte(want)) {
			t.Fatalf("transcript terminal record = %s, want %s", terminalPayload, want)
		}
	}
}

func TestRunSessionWithMaxDuration_FinalizesZeroSampleArtifactsBeforeFirstAudio(t *testing.T) {
	artifactDir := t.TempDir()
	wavPath := filepath.Join(artifactDir, "zero.wav")
	transcriptPath := filepath.Join(artifactDir, "zero.jsonl")
	clock := &durationTestClock{}
	inferencer := &durationTestInferencer{
		connectedCh: make(chan struct{}),
	}
	var out bytes.Buffer
	runErrCh := make(chan error, 1)
	ctx := WithSessionDurationArtifactPaths(context.Background(), SessionDurationArtifactPaths{
		AudioPath:      wavPath,
		TranscriptPath: transcriptPath,
	})
	go func() {
		runErrCh <- RunSessionWithMaxDurationClock(ctx, &out, SessionRunOptions{
			ReplayPath:        filepath.Join(artifactDir, "fixture.session.json"),
			SessionInferencer: inferencer,
		}, time.Nanosecond, clock)
	}()
	select {
	case <-inferencer.connectedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("zero-sample session did not connect")
	}
	clock.fire()
	if err := <-runErrCh; err != nil {
		t.Fatalf("zero-sample duration cutoff: %v", err)
	}
	if !strings.Contains(out.String(), "terminal_reason=max_duration") {
		t.Fatalf("zero-sample output missing max_duration: %q", out.String())
	}

	wavFile, err := os.Open(wavPath)
	if err != nil {
		t.Fatalf("reopen zero-sample WAV artifact: %v", err)
	}
	wavData, err := io.ReadAll(wavFile)
	closeErr := wavFile.Close()
	if err != nil {
		t.Fatalf("read zero-sample WAV artifact: %v", err)
	}
	if closeErr != nil {
		t.Fatalf("close reopened zero-sample WAV artifact: %v", closeErr)
	}
	if len(wavData) != 44 || string(wavData[0:4]) != "RIFF" || string(wavData[8:12]) != "WAVE" {
		t.Fatalf("zero-sample WAV header = %q, want canonical 44-byte RIFF/WAVE", wavData)
	}
	if binary.LittleEndian.Uint32(wavData[4:8]) != 36 || string(wavData[36:40]) != "data" || binary.LittleEndian.Uint32(wavData[40:44]) != 0 {
		t.Fatalf("zero-sample WAV header has unexpected sizes: %v", wavData)
	}

	transcriptData, err := os.ReadFile(transcriptPath)
	if err != nil {
		t.Fatalf("reopen zero-sample transcript artifact: %v", err)
	}
	if !bytes.HasSuffix(transcriptData, []byte("\n")) {
		t.Fatal("zero-sample transcript is missing its trailing JSONL newline")
	}
	scanner := bufio.NewScanner(bytes.NewReader(transcriptData))
	var eventTypes []messages.StreamMessageType
	var terminalPayload []byte
	for scanner.Scan() {
		record, err := transcript.Decode(scanner.Bytes())
		if err != nil {
			t.Fatalf("decode zero-sample transcript record: %v", err)
		}
		var event struct {
			Type messages.StreamMessageType `json:"type"`
		}
		if err := json.Unmarshal(record.Payload, &event); err != nil {
			t.Fatalf("decode zero-sample transcript payload: %v", err)
		}
		eventTypes = append(eventTypes, event.Type)
		if event.Type == messages.StreamTypeSessionClose {
			terminalPayload = append([]byte(nil), record.Payload...)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan zero-sample transcript: %v", err)
	}
	wantTypes := []messages.StreamMessageType{
		messages.StreamTypeSessionClose,
	}
	if !reflect.DeepEqual(eventTypes, wantTypes) {
		t.Fatalf("zero-sample transcript event order = %v, want %v", eventTypes, wantTypes)
	}
	if !bytes.Contains(terminalPayload, []byte("max_duration")) {
		t.Fatalf("zero-sample terminal record = %s, want max_duration", terminalPayload)
	}
}

func TestRunSessionWithMaxDuration_PreservesArtifactFlushAndCloseIdentity(t *testing.T) {
	flushErr := errors.New("artifact flush failed")
	closeErr := errors.New("artifact close failed")
	for _, testCase := range []struct {
		name     string
		flushErr error
		closeErr error
		wantErr  error
	}{
		{name: "flush", flushErr: flushErr, wantErr: flushErr},
		{name: "close", closeErr: closeErr, wantErr: closeErr},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			lifecycle := &durationArtifactLifecycleProbe{
				flushErr: testCase.flushErr,
				closeErr: testCase.closeErr,
			}
			err := RunSessionWithMaxDurationClock(
				WithSessionDurationArtifacts(context.Background(), lifecycle),
				io.Discard,
				SessionRunOptions{
					ReplayPath:        "artifact-failure.session.json",
					SessionInferencer: &durationTestInferencer{events: durationNaturalEvents(), closeAfterEvents: true},
				},
				time.Hour,
				&durationTestClock{},
			)
			if !errors.Is(err, testCase.wantErr) {
				t.Fatalf("artifact lifecycle error = %v, want %v", err, testCase.wantErr)
			}
			if lifecycle.accepted == 0 || !lifecycle.closed {
				t.Fatalf("artifact lifecycle accepted=%d closed=%v, want accepted output and close", lifecycle.accepted, lifecycle.closed)
			}
		})
	}
}

func TestRunAgentLoopSessionWithDuration_PreservesFailureIdentity(t *testing.T) {
	providerErr := errors.New("provider failed")
	providerRunErr := runAgentLoopSessionWithDurationClock(
		context.Background(),
		io.Discard,
		&durationTestInferencer{connectErr: providerErr},
		sessionLoopOptions{},
		time.Hour,
		&durationTestClock{},
	)
	if !errors.Is(providerRunErr, providerErr) || strings.Contains(providerRunErr.Error(), string(SessionMaxDurationReason)) {
		t.Fatalf("provider failure = %v, want provider identity without max_duration", providerRunErr)
	}

	drainErr := errors.New("sink write failed")
	drainRunErr := runAgentLoopSessionWithDurationClock(
		context.Background(),
		failingDurationWriter{err: drainErr},
		&durationTestInferencer{events: durationOutputEvents()},
		sessionLoopOptions{},
		time.Hour,
		&durationTestClock{},
	)
	if !errors.Is(drainRunErr, drainErr) || strings.Contains(drainRunErr.Error(), string(SessionMaxDurationReason)) {
		t.Fatalf("drain failure = %v, want drain identity without max_duration", drainRunErr)
	}

	closeErr := errors.New("session close failed")
	closeInferencer := &durationTestInferencer{
		events:          durationOutputEvents(),
		sessionCloseErr: closeErr,
	}
	closeClock := &durationTestClock{}
	closeWriter := newDurationTestWriter()
	closeRunErrCh := make(chan error, 1)
	go func() {
		closeRunErrCh <- runAgentLoopSessionWithDurationClock(
			context.Background(), closeWriter, closeInferencer, sessionLoopOptions{}, time.Hour, closeClock,
		)
	}()
	closeWriter.waitFor(t, "accepted output")
	closeClock.fire()
	closeRunErr := <-closeRunErrCh
	if !errors.Is(closeRunErr, closeErr) || strings.Contains(closeRunErr.Error(), string(SessionMaxDurationReason)) {
		t.Fatalf("close failure = %v, want close identity without max_duration", closeRunErr)
	}
}

func TestRunSessionDurationPlan_PreservesFlushAndFinalizeFailures(t *testing.T) {
	flushErr := errors.New("capture flush failed")
	flushPlan := sessionRuntimePlan{
		mode:       sessionRuntimeModeInjectedLive,
		inferencer: &durationTestInferencer{events: durationNaturalEvents(), closeAfterEvents: true},
		flushCapture: func() error {
			return flushErr
		},
	}
	if err := runSessionDurationPlan(context.Background(), io.Discard, flushPlan, time.Hour, &durationTestClock{}); !errors.Is(err, flushErr) {
		t.Fatalf("flush failure = %v, want %v", err, flushErr)
	}

	finalizeErr := errors.New("transcript finalize failed")
	finalizePlan := sessionRuntimePlan{
		mode:       sessionRuntimeModeInjectedLive,
		inferencer: &durationTestInferencer{events: durationNaturalEvents(), closeAfterEvents: true},
		finalize: func(context.Context, io.Writer) error {
			return finalizeErr
		},
	}
	if err := runSessionDurationPlan(context.Background(), io.Discard, finalizePlan, time.Hour, &durationTestClock{}); !errors.Is(err, finalizeErr) {
		t.Fatalf("finalize failure = %v, want %v", err, finalizeErr)
	}
}

func TestRunSessionWithMaxDuration_ReleasesTimerSessionAndProductionArtifacts(t *testing.T) {
	baselineGoroutines := runtime.NumGoroutine()
	artifactDir := t.TempDir()
	wavPath := filepath.Join(artifactDir, "session-resource.wav")
	transcriptPath := filepath.Join(artifactDir, "session-resource.jsonl")
	clock := &durationTestClock{}
	inferencer := &durationTestInferencer{
		events: durationArtifactEvents(),
	}
	writer := newDurationTestWriter()
	runErrCh := make(chan error, 1)
	ctx := WithSessionDurationArtifactPaths(context.Background(), SessionDurationArtifactPaths{
		AudioPath:      wavPath,
		TranscriptPath: transcriptPath,
	})
	go func() {
		runErrCh <- RunSessionWithMaxDurationClock(ctx, writer, SessionRunOptions{
			ReplayPath:        filepath.Join(artifactDir, "fixture.session.json"),
			SessionInferencer: inferencer,
		}, time.Nanosecond, clock)
	}()
	writer.waitFor(t, "artifact-ready")
	clock.fire()
	if err := <-runErrCh; err != nil {
		t.Fatalf("resource cutoff: %v", err)
	}
	if inferencer.session == nil || inferencer.session.closeCount != 1 {
		t.Fatalf("session close count = %v, want exactly one", inferencer.session)
	}
	if clock.timer == nil || !clock.timer.stopped {
		t.Fatal("duration timer was not stopped")
	}

	for _, path := range []string{wavPath, transcriptPath} {
		renamed := path + ".closed"
		if err := os.Rename(path, renamed); err != nil {
			t.Fatalf("resource file %s remained open after cutoff: %v", path, err)
		}
		if err := os.Remove(renamed); err != nil {
			t.Fatalf("remove closed resource probe: %v", err)
		}
	}
	waitForDurationGoroutines(t, baselineGoroutines+4)
}

func waitForDurationGoroutines(t *testing.T, maximum int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > maximum && time.Now().Before(deadline) {
		runtime.Gosched()
	}
	if got := runtime.NumGoroutine(); got > maximum {
		t.Fatalf("goroutines after cutoff = %d, want <= %d", got, maximum)
	}
}

type failingDurationWriter struct{ err error }

func (w failingDurationWriter) Write([]byte) (int, error) { return 0, w.err }

type durationArtifactLifecycleProbe struct {
	accepted int
	flushErr error
	closeErr error
	closed   bool
}

type durationCompleteMessageSession struct {
	messages         []messages.Message
	deferredMessages []messages.Message
	complete         bool
	withoutResponse  bool
}

func (s *durationCompleteMessageSession) Send(context.Context, messages.StreamMessage) bool {
	return true
}

func (s *durationCompleteMessageSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return messages.NewTypedBuffer[messages.StreamMessage](1)
}

func (s *durationCompleteMessageSession) Done() <-chan struct{} { return nil }

func (s *durationCompleteMessageSession) Close() error { return nil }

func (s *durationCompleteMessageSession) SendMessage(_ context.Context, message messages.Message) bool {
	s.messages = append(s.messages, message)
	return true
}

func (s *durationCompleteMessageSession) SendMessageWithoutResponse(_ context.Context, message messages.Message) bool {
	s.deferredMessages = append(s.deferredMessages, message)
	return true
}

func (s *durationCompleteMessageSession) SupportsCompleteMessages() bool {
	return s.complete
}

func (s *durationCompleteMessageSession) SupportsCompleteMessagesWithoutResponse() bool {
	return s.withoutResponse
}

func (p *durationArtifactLifecycleProbe) Accept(messages.StreamMessage) error {
	p.accepted++
	return nil
}

func (p *durationArtifactLifecycleProbe) Flush() error { return p.flushErr }

func (p *durationArtifactLifecycleProbe) Close() error {
	p.closed = true
	return p.closeErr
}

var _ messages.SessionInferencer = (*durationTestInferencer)(nil)
var _ messages.Session = (*durationTestSession)(nil)
var _ messages.Session = (*durationCompleteMessageSession)(nil)
var _ SessionImageMessageSender = (*durationCompleteMessageSession)(nil)
var _ SessionImageMessageSenderWithoutResponse = (*durationCompleteMessageSession)(nil)
var _ SessionDurationClock = (*durationTestClock)(nil)
var _ SessionDurationTimer = (*durationTestTimer)(nil)
var _ SessionDurationArtifactLifecycle = (*durationArtifactLifecycleProbe)(nil)
