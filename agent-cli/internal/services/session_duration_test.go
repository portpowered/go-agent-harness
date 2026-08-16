package services

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
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
	err := RunSessionWithMaxDuration(context.Background(), io.Discard, SessionRunOptions{
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

func TestSessionCommandHelpAndOmittedDurationBehavior(t *testing.T) {
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller did not return the test file")
	}
	moduleDir := filepath.Clean(filepath.Join(filepath.Dir(testFile), "..", ".."))
	for _, args := range [][]string{{"session", "--help"}, {"session"}} {
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

func durationOutputEvents() []messages.StreamMessage {
	return []messages.StreamMessage{
		{Type: messages.StreamTypeSessionOpen, Value: messages.NewSessionOpenValue("duration-session", "test")},
		{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("accepted output")},
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

type durationTestClock struct {
	timer *durationTestTimer
	calls int
}

func (c *durationTestClock) NewTimer(time.Duration) SessionDurationTimer {
	c.calls++
	c.timer = &durationTestTimer{ch: make(chan time.Time, 1)}
	return c.timer
}

func (c *durationTestClock) fire() {
	c.timer.ch <- time.Time{}
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
	events           []messages.StreamMessage
	connected        bool
	connectedCh      chan struct{}
	closeAfterEvents bool
	session          *durationTestSession
	connectErr       error
	sessionCloseErr  error
	openFile         *os.File
}

func (i *durationTestInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	if i.connectErr != nil {
		return nil, i.connectErr
	}
	i.connected = true
	session := newDurationTestSession()
	session.closeErr = i.sessionCloseErr
	session.openFile = i.openFile
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
	receive    *messages.TypedBuffer[messages.StreamMessage]
	done       chan struct{}
	once       sync.Once
	closeErr   error
	closeCount int
	openFile   *os.File
}

func newDurationTestSession() *durationTestSession {
	return &durationTestSession{
		receive: messages.NewTypedBuffer[messages.StreamMessage](16),
		done:    make(chan struct{}),
	}
}

func (s *durationTestSession) Send(context.Context, messages.StreamMessage) bool { return true }

func (s *durationTestSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.receive
}

func (s *durationTestSession) Done() <-chan struct{} { return s.done }

func (s *durationTestSession) Close() error {
	s.closeCount++
	s.end()
	if s.openFile != nil {
		s.closeErr = errors.Join(s.closeErr, s.openFile.Close())
		s.openFile = nil
	}
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

func TestSessionDurationAdmission_FinalizesPlayableArtifactsAndRejectsLateFrame(t *testing.T) {
	admission := newSessionDurationAdmission()
	source := newDurationTestSession()
	wrapped := newSessionDurationAdmissionSession(context.Background(), source, admission, nil)
	accepted := []messages.StreamMessage{
		{Type: messages.StreamTypeAudioStart, Value: messages.NewAudioStartValue()},
		{Type: messages.StreamTypeAudioDelta, Value: messages.NewAudioDeltaValue([]byte{1, 0, 2, 0})},
		{Type: messages.StreamTypeTranscriptStart, Value: messages.NewTranscriptStartValue()},
		{Type: messages.StreamTypeTranscriptDelta, Value: messages.NewTranscriptDeltaValue("hello")},
		{Type: messages.StreamTypeTranscriptEnd, Value: messages.NewTranscriptEndValue("hello")},
		{Type: messages.StreamTypeAudioEnd, Value: messages.NewAudioEndValue()},
	}
	for _, event := range accepted {
		if !source.receive.Write(context.Background(), event) {
			t.Fatalf("write accepted event %s", event.Type)
		}
	}
	waitForDurationBufferLen(t, wrapped.Receive(), len(accepted))

	// This is the deterministic equivalent of the timer firing. The late audio
	// delta is still present on the provider buffer, but cannot cross the gate.
	wrapped.closeAdmission()
	if !source.receive.Write(context.Background(), messages.StreamMessage{
		Type:  messages.StreamTypeAudioDelta,
		Value: messages.NewAudioDeltaValue([]byte{3, 0}),
	}) {
		t.Fatal("write late provider frame")
	}
	source.end()
	select {
	case <-wrapped.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("admission session did not release after provider close")
	}

	artifact := &durationArtifact{}
	for {
		event, ok := wrapped.Receive().Read()
		if !ok {
			break
		}
		artifact.accept(event)
	}
	artifact.addTerminal()
	wavBytes, transcriptBytes, err := artifact.finalize()
	if err != nil {
		t.Fatalf("finalize artifacts: %v", err)
	}

	wavPath := filepath.Join(t.TempDir(), "cutoff.wav")
	if err := os.WriteFile(wavPath, wavBytes, 0600); err != nil {
		t.Fatalf("write WAV artifact: %v", err)
	}
	wavFile, err := os.Open(wavPath)
	if err != nil {
		t.Fatalf("reopen WAV artifact: %v", err)
	}
	rate, samples, err := wavio.Read(wavFile)
	closeErr := wavFile.Close()
	if err != nil {
		t.Fatalf("read finalized WAV artifact: %v", err)
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
		t.Fatalf("WAV samples = %v, want [1 2]", samples)
	}

	transcriptPath := filepath.Join(t.TempDir(), "cutoff.jsonl")
	if err := os.WriteFile(transcriptPath, transcriptBytes, 0600); err != nil {
		t.Fatalf("write transcript artifact: %v", err)
	}
	transcriptData, err := os.ReadFile(transcriptPath)
	if err != nil {
		t.Fatalf("reopen transcript artifact: %v", err)
	}
	if !bytes.HasSuffix(transcriptData, []byte("\n")) {
		t.Fatal("finalized transcript is missing its trailing JSONL newline")
	}
	file := bytes.NewReader(transcriptData)
	scanner := bufio.NewScanner(file)
	var payloads []string
	for scanner.Scan() {
		record, err := transcript.Decode(scanner.Bytes())
		if err != nil {
			t.Fatalf("decode transcript JSONL record: %v", err)
		}
		payloads = append(payloads, string(record.Payload))
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan transcript JSONL: %v", err)
	}
	wantPayloads := []string{"start", "delta:hello", "end:hello", "terminal:max_duration"}
	if len(payloads) != len(wantPayloads) {
		t.Fatalf("transcript records = %v, want %v", payloads, wantPayloads)
	}
	for index, want := range wantPayloads {
		if payloads[index] != want {
			t.Fatalf("transcript record %d = %q, want %q", index, payloads[index], want)
		}
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

func TestRunAgentLoopSessionWithDuration_ReleasesTimerSessionAndFileResources(t *testing.T) {
	baselineGoroutines := runtime.NumGoroutine()
	path := filepath.Join(t.TempDir(), "session-resource")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatalf("open resource probe: %v", err)
	}
	clock := &durationTestClock{}
	inferencer := &durationTestInferencer{
		events:   durationOutputEvents(),
		openFile: file,
	}
	writer := newDurationTestWriter()
	runErrCh := make(chan error, 1)
	go func() {
		runErrCh <- runAgentLoopSessionWithDurationClock(context.Background(), writer, inferencer, sessionLoopOptions{}, time.Hour, clock)
	}()
	writer.waitFor(t, "accepted output")
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

	renamed := path + ".closed"
	if err := os.Rename(path, renamed); err != nil {
		t.Fatalf("resource file remained open after cutoff: %v", err)
	}
	if err := os.Remove(renamed); err != nil {
		t.Fatalf("remove closed resource probe: %v", err)
	}
	waitForDurationGoroutines(t, baselineGoroutines+4)
}

type durationArtifact struct {
	samples            []int16
	transcriptPayloads []string
}

func (a *durationArtifact) accept(event messages.StreamMessage) {
	switch value := event.Value.(type) {
	case *messages.AudioDeltaValue:
		for index := 0; index+1 < len(value.Content); index += 2 {
			a.samples = append(a.samples, int16(binary.LittleEndian.Uint16(value.Content[index:])))
		}
	case *messages.TranscriptStartValue:
		a.transcriptPayloads = append(a.transcriptPayloads, "start")
	case *messages.TranscriptDeltaValue:
		a.transcriptPayloads = append(a.transcriptPayloads, "delta:"+value.Text)
	case *messages.TranscriptEndValue:
		a.transcriptPayloads = append(a.transcriptPayloads, "end:"+value.FullText)
	}
}

func (a *durationArtifact) addTerminal() {
	a.transcriptPayloads = append(a.transcriptPayloads, "terminal:max_duration")
}

func (a *durationArtifact) finalize() ([]byte, []byte, error) {
	var wav bytes.Buffer
	if err := wavio.Write(&wav, wavio.Rate16kHz, a.samples); err != nil {
		return nil, nil, err
	}
	var jsonl bytes.Buffer
	for index, payload := range a.transcriptPayloads {
		record := transcript.NewRecord(uint64(index+1), time.Unix(0, int64(index+1)), transcript.PeerAgent, transcript.DirectionIn, transcript.StreamWebSocket, []byte(payload))
		encoded, err := transcript.Encode(record)
		if err != nil {
			return nil, nil, err
		}
		jsonl.Write(encoded)
	}
	return wav.Bytes(), jsonl.Bytes(), nil
}

func waitForDurationBufferLen(t *testing.T, buffer *messages.TypedBuffer[messages.StreamMessage], want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for buffer.Len() < want && time.Now().Before(deadline) {
		runtime.Gosched()
	}
	if buffer.Len() < want {
		t.Fatalf("admission buffer length = %d, want at least %d", buffer.Len(), want)
	}
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

var _ messages.SessionInferencer = (*durationTestInferencer)(nil)
var _ messages.Session = (*durationTestSession)(nil)
var _ SessionDurationClock = (*durationTestClock)(nil)
var _ SessionDurationTimer = (*durationTestTimer)(nil)
