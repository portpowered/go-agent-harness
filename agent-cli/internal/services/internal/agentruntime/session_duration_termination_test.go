package agentruntime

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	duration "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
	durationwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
	sessiontracewire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace/wire"
	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

const durationTerminalDrainAcceptedOutput = "accepted duration terminal delta"

const (
	rateLimitRetryCode         = "rate_limit_exceeded"
	defaultRateLimitRetryDelay = 2 * time.Second
	maxRateLimitRetryDelay     = 15 * time.Second
)

func rateLimitRetryDecision(terminal *messages.MessageEndValue) (time.Duration, bool) {
	decision := durationwire.NewService().EvaluateRetry(duration.RetryPolicy{Enabled: true}, terminal)
	return decision.Delay, decision.Eligible
}

func providerTerminalErrorCode(terminal *messages.MessageEndValue) string {
	if terminal == nil {
		return ""
	}
	if code := strings.TrimSpace(terminal.ProviderErrorCode); code != "" {
		return code
	}
	for _, part := range strings.Split(terminal.StatusDetails, ",") {
		key, value, ok := strings.Cut(part, "=")
		if ok && strings.EqualFold(strings.TrimSpace(key), "code") {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

type durationTerminalDrainFixture struct {
	ctx    context.Context
	cancel context.CancelFunc

	inferencer *durationTerminalDrainInferencer
	loopReady  chan *agentloop.AgentLoop
	loop       *agentloop.AgentLoop
	clock      SessionDurationClock
	done       chan struct{}

	options      sessionLoopOptions
	output       bytes.Buffer
	writer       io.Writer
	acceptedText string
	acceptedMu   sync.RWMutex
	loopMu       sync.RWMutex
	cleanup      []func()
}

func newDurationTerminalDrainFixture(t *testing.T) *durationTerminalDrainFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	fixture := &durationTerminalDrainFixture{
		ctx:    ctx,
		cancel: cancel,
		inferencer: &durationTerminalDrainInferencer{
			session:   newDurationTerminalDrainSession(),
			connected: make(chan struct{}),
			events: []messages.StreamMessage{{
				Type:  messages.StreamTypeSessionOpen,
				Value: messages.NewSessionOpenValue("duration-terminal-drain", "test"),
			}},
		},
		loopReady:    make(chan *agentloop.AgentLoop, 1),
		done:         make(chan struct{}),
		clock:        &durationTestClock{},
		acceptedText: durationTerminalDrainAcceptedOutput,
	}
	fixture.writer = &fixture.output
	fixture.options = sessionLoopOptions{loopReady: fixture.loopReady}
	return fixture
}

func (f *durationTerminalDrainFixture) run(t *testing.T, setup func(*durationTerminalDrainFixture) func()) error {
	t.Helper()
	trigger := setup(f)
	result := make(chan error, 1)
	go func() {
		result <- runAgentLoopSessionWithDurationAdmissionClockStream(
			f.ctx,
			f.writer,
			f.inferencer,
			f.options,
			time.Hour,
			f.clock,
			nil,
		)
	}()

	select {
	case loop := <-f.loopReady:
		f.loopMu.Lock()
		f.loop = loop
		f.loopMu.Unlock()
	case <-f.ctx.Done():
		t.Fatalf("duration session loop was not constructed: %v", f.ctx.Err())
	}
	select {
	case <-f.inferencer.connected:
	case <-f.ctx.Done():
		t.Fatalf("duration session did not connect: %v", f.ctx.Err())
	}
	if trigger != nil {
		trigger()
	}

	select {
	case err := <-result:
		f.cancel()
		for index := len(f.cleanup) - 1; index >= 0; index-- {
			f.cleanup[index]()
		}
		return err
	case <-time.After(2 * time.Second):
		f.cancel()
		t.Fatalf("duration terminal path did not return")
		return nil
	}
}

func (f *durationTerminalDrainFixture) acceptedOutput() {
	f.loopMu.RLock()
	loop := f.loop
	f.loopMu.RUnlock()
	f.acceptedMu.RLock()
	acceptedText := f.acceptedText
	f.acceptedMu.RUnlock()
	if loop == nil {
		panic("duration terminal regression loop unavailable")
	}
	for _, msg := range []messages.StreamMessage{
		{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()},
		{Type: messages.StreamTypeTextStart, Role: messages.RoleAssistant, Value: messages.NewTextStartValue()},
		{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue(acceptedText)},
	} {
		if !loop.Deltas().Write(context.Background(), msg) {
			panic("duration terminal regression delta buffer rejected test output")
		}
	}
}

func (f *durationTerminalDrainFixture) sendProviderMessage(msg messages.StreamMessage) {
	if !f.inferencer.session.receive.Write(context.Background(), msg) {
		panic("duration terminal regression provider buffer rejected test message")
	}
}

type durationTerminalDrainCase struct {
	name          string
	setup         func(*durationTerminalDrainFixture) func()
	wantErr       error
	wantErrorText string
	wantOutput    string
}

func TestRunAgentLoopSessionWithDurationTerminalOutcomesAlwaysDrainAcceptedDelta(t *testing.T) {
	tests := durationTerminalDrainControlCases(
		errors.New("duration page refresh failed"), context.Canceled,
	)
	tests = append(tests, durationTerminalDrainTransportCases(errors.New("duration transport done failed"))...)
	tests = append(tests, durationTerminalDrainLoopCases(
		errors.New("duration RTC media pump failed"), errors.New("duration agent loop failed"),
	)...)
	tests = append(tests, durationTerminalDrainProviderCases(
		errors.New("duration provider terminal failure"), errors.New("duration terminal output writer failed"),
	)...)
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newDurationTerminalDrainFixture(t)
			err := fixture.run(t, testCase.setup)
			if testCase.wantErr != nil && !errors.Is(err, testCase.wantErr) {
				t.Fatalf("duration terminal error = %v, want errors.Is(..., %v)", err, testCase.wantErr)
			}
			if testCase.wantErr == nil && testCase.wantErrorText == "" && err != nil {
				t.Fatalf("duration terminal error = %v, want clean completion", err)
			}
			if testCase.wantErrorText != "" && !strings.Contains(errorString(err), testCase.wantErrorText) {
				t.Fatalf("duration terminal error = %v, want text %q", err, testCase.wantErrorText)
			}
			fixture.acceptedMu.RLock()
			acceptedText := fixture.acceptedText
			fixture.acceptedMu.RUnlock()
			if !strings.Contains(fixture.output.String(), acceptedText) {
				t.Fatalf("duration terminal path lost accepted output %q; rendered output=%q", acceptedText, fixture.output.String())
			}
			if testCase.wantOutput != "" && !strings.Contains(fixture.output.String(), testCase.wantOutput) {
				t.Fatalf("duration terminal output = %q, missing %q", fixture.output.String(), testCase.wantOutput)
			}
		})
	}
}

func durationTerminalDrainControlCases(publicationErr, schedulerErr error) []durationTerminalDrainCase {
	return []durationTerminalDrainCase{
		{
			name: "deadline already ready",
			setup: func(f *durationTerminalDrainFixture) func() {
				clock := newGatedDurationTerminalDrainClock(false)
				f.clock = clock
				return func() { f.acceptedOutput(); clock.fire(); clock.releaseTimer() }
			},
			wantOutput: string(SessionMaxDurationReason),
		},
		{
			name: "duration clock construction failure",
			setup: func(f *durationTerminalDrainFixture) func() {
				clock := newGatedDurationTerminalDrainClock(true)
				f.clock = clock
				return func() { f.acceptedOutput(); clock.releaseTimer() }
			},
			wantErrorText: "session duration clock returned a nil timer",
		},
		{
			name: "dynamic publication failure",
			setup: func(f *durationTerminalDrainFixture) func() {
				watch := make(chan webmcp.BrokerEvent, 1)
				f.options.BrowserWatch = func(context.Context) <-chan webmcp.BrokerEvent { return watch }
				f.options.ToolDefinitionBase = []messages.ToolDefinition{{Name: "stable"}}
				f.options.RefreshToolDefinitions = func(context.Context) ([]messages.ToolDefinition, error) { return nil, publicationErr }
				f.inferencer.events = append(f.inferencer.events, messages.StreamMessage{Type: messages.StreamTypeSessionCreated, Value: messages.NewSessionCreatedValue("duration-terminal-drain", "test")})
				return func() {
					f.acceptedOutput()
					watch <- webmcp.BrokerEvent{Type: webmcp.BrokerEventCatalogChanged, Sequence: 1}
				}
			},
			wantErr: publicationErr,
		},
		{
			name: "scheduled dispatch failure",
			setup: func(f *durationTerminalDrainFixture) func() {
				observer := sessiontracewire.NewObserver(sessiontrace.NewObserverOptions{Provider: "test", Model: "test"})
				observer.ScheduleAudioInputs([]sessiontrace.ScheduledAudioInput{{AfterCompletedTurns: 0, PCM: []byte{1}}})
				observer.SetStreamObserver(func(msg messages.StreamMessage) {
					if msg.Type == messages.StreamTypeSessionOpen {
						f.acceptedOutput()
						f.cancel()
					}
				})
				f.options.observer = observer
				return nil
			},
			wantErr: schedulerErr,
		},
		{
			name: "timer expiry",
			setup: func(f *durationTerminalDrainFixture) func() {
				return func() { f.acceptedOutput(); f.clock.(*durationTestClock).fire() }
			},
			wantOutput: string(SessionMaxDurationReason),
		},
		{
			name: "session updated timeout",
			setup: func(f *durationTerminalDrainFixture) func() {
				observer := sessiontracewire.NewObserver(sessiontrace.NewObserverOptions{Provider: "test", Model: "test"})
				observer.SetRequireSessionUpdated(true)
				observer.ScheduleAudioInputs([]sessiontrace.ScheduledAudioInput{{AfterCompletedTurns: 0, PCM: []byte{1}}})
				f.options.observer = observer
				f.options.RequireSessionUpdated = true
				f.options.SessionUpdatedTimeout = 15 * time.Millisecond
				return func() { f.acceptedOutput() }
			},
			wantErr: ErrSessionScheduledAudioConfigTimeout,
		},
	}
}

func durationTerminalDrainTransportCases(doneErr error) []durationTerminalDrainCase {
	return []durationTerminalDrainCase{
		{
			name: "caller cancellation",
			setup: func(f *durationTerminalDrainFixture) func() {
				return func() { f.acceptedOutput(); f.cancel() }
			},
			wantErr: context.Canceled,
		},
		{
			name: "transport done without error",
			setup: func(f *durationTerminalDrainFixture) func() {
				f.options.Done = f.done
				f.options.DoneErr = func() error { f.acceptedOutput(); return nil }
				return func() { close(f.done) }
			},
		},
		{
			name: "transport done with error",
			setup: func(f *durationTerminalDrainFixture) func() {
				f.options.Done = f.done
				f.options.DoneErr = func() error { f.acceptedOutput(); return doneErr }
				return func() { close(f.done) }
			},
			wantErr: doneErr,
		},
	}
}

func durationTerminalDrainLoopCases(pumpErr, loopErr error) []durationTerminalDrainCase {
	return []durationTerminalDrainCase{
		{
			name: "observed provider completion",
			setup: func(f *durationTerminalDrainFixture) func() {
				return func() { f.acceptedOutput(); f.inferencer.session.end() }
			},
			wantOutput: "terminal_reason=provider_close",
		},
		{
			name: "RTC pump failure",
			setup: func(f *durationTerminalDrainFixture) func() {
				deviceErrors := make(chan error, 1)
				f.options.rtcDeviceBinding = &testRTCBinding{errors: deviceErrors}
				return func() { f.acceptedOutput(); deviceErrors <- pumpErr }
			},
			wantErr: pumpErr,
		},
		{
			name: "closed delta stream after loop completion",
			setup: func(f *durationTerminalDrainFixture) func() {
				return func() {
					f.acceptedOutput()
					f.sendProviderMessage(messages.StreamMessage{Type: messages.StreamTypeLoopEnd, Value: messages.NewLoopEndValue()})
				}
			},
		},
		{
			name: "loop failure",
			setup: func(f *durationTerminalDrainFixture) func() {
				return func() {
					f.acceptedOutput()
					f.sendProviderMessage(messages.StreamMessage{Type: messages.StreamTypeError, Value: messages.NewErrorValueWithError(loopErr)})
				}
			},
			wantErr: loopErr,
		},
	}
}

func durationTerminalDrainProviderCases(providerErr, writeErr error) []durationTerminalDrainCase {
	return []durationTerminalDrainCase{
		{
			name: "terminal provider message end",
			setup: func(f *durationTerminalDrainFixture) func() {
				return func() {
					f.acceptedOutput()
					f.sendProviderMessage(messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})})
				}
			},
		},
		{
			name: "terminal provider text end",
			setup: func(f *durationTerminalDrainFixture) func() {
				return func() {
					f.acceptedOutput()
					f.sendProviderMessage(messages.StreamMessage{Type: messages.StreamTypeTextEnd, Role: messages.RoleAssistant, Value: messages.NewTextEndValue()})
				}
			},
		},
		{
			name: "terminal provider session close",
			setup: func(f *durationTerminalDrainFixture) func() {
				return func() {
					f.acceptedOutput()
					f.sendProviderMessage(messages.StreamMessage{Type: messages.StreamTypeSessionClose, Value: messages.NewSessionCloseValue("duration-terminal-drain", "provider closed")})
				}
			},
		},
		{
			name: "terminal provider error",
			setup: func(f *durationTerminalDrainFixture) func() {
				return func() {
					f.acceptedOutput()
					f.sendProviderMessage(messages.StreamMessage{Type: messages.StreamTypeError, Value: messages.NewErrorValueWithError(providerErr)})
				}
			},
			wantErr: providerErr,
		},
		{
			name: "message processing output failure",
			setup: func(f *durationTerminalDrainFixture) func() {
				// Fail after the accepted chunk: the renderer writes its label first.
				f.writer = &durationTerminalDrainFailingWriter{target: &f.output, failAfter: 3, err: writeErr}
				return func() {
					f.acceptedOutput()
					f.sendProviderMessage(messages.StreamMessage{Type: messages.StreamTypeSessionClose, Value: messages.NewSessionCloseValue("duration-terminal-drain", "provider closed")})
				}
			},
			wantErr: writeErr,
		},
	}
}

type gatedDurationTerminalDrainClock struct {
	inner       *durationTestClock
	created     chan struct{}
	release     chan struct{}
	createdOnce sync.Once
	releaseOnce sync.Once
	returnNil   bool
}

func newGatedDurationTerminalDrainClock(returnNil bool) *gatedDurationTerminalDrainClock {
	return &gatedDurationTerminalDrainClock{
		inner:     &durationTestClock{},
		created:   make(chan struct{}),
		release:   make(chan struct{}),
		returnNil: returnNil,
	}
}

func (c *gatedDurationTerminalDrainClock) NewTimer(duration time.Duration) SessionDurationTimer {
	c.createdOnce.Do(func() { close(c.created) })
	<-c.release
	if c.returnNil {
		return nil
	}
	return c.inner.NewTimer(duration)
}

func (c *gatedDurationTerminalDrainClock) fire() {
	c.inner.fire()
}

func (c *gatedDurationTerminalDrainClock) releaseTimer() {
	c.releaseOnce.Do(func() { close(c.release) })
}

type durationTerminalDrainInferencer struct {
	session     *durationTerminalDrainSession
	events      []messages.StreamMessage
	connected   chan struct{}
	connectErr  error
	connectOnce sync.Once
}

func (i *durationTerminalDrainInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	if i.connectErr != nil {
		return nil, i.connectErr
	}
	for _, event := range i.events {
		if !i.session.receive.Write(ctx, event) {
			return nil, ctx.Err()
		}
	}
	i.connectOnce.Do(func() { close(i.connected) })
	return i.session, nil
}

type durationTerminalDrainSession struct {
	receive   *messages.TypedBuffer[messages.StreamMessage]
	done      chan struct{}
	closeOnce sync.Once
	doneOnce  sync.Once
	closeErr  error
	media     audio.MediaEndpoints
}

func newDurationTerminalDrainSession() *durationTerminalDrainSession {
	return &durationTerminalDrainSession{
		receive: messages.NewTypedBuffer[messages.StreamMessage](64),
		done:    make(chan struct{}),
	}
}

func (s *durationTerminalDrainSession) Send(ctx context.Context, _ messages.StreamMessage) bool {
	select {
	case <-ctx.Done():
		return false
	default:
		return true
	}
}

func (s *durationTerminalDrainSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.receive
}

func (s *durationTerminalDrainSession) Done() <-chan struct{} { return s.done }

func (s *durationTerminalDrainSession) Close() error {
	s.closeOnce.Do(func() { s.end() })
	return s.closeErr
}

func (s *durationTerminalDrainSession) end() {
	s.doneOnce.Do(func() { close(s.done) })
}

func (s *durationTerminalDrainSession) RTCMedia() audio.MediaEndpoints { return s.media }

type durationTerminalDrainFailingWriter struct {
	target    io.Writer
	failAfter int
	err       error
	mu        sync.Mutex
	writes    int
}

func (w *durationTerminalDrainFailingWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	w.writes++
	shouldFail := w.writes >= w.failAfter
	w.mu.Unlock()
	if shouldFail {
		return 0, w.err
	}
	return w.target.Write(data)
}

var _ messages.SessionInferencer = (*durationTerminalDrainInferencer)(nil)
var _ messages.Session = (*durationTerminalDrainSession)(nil)
var _ SessionDurationClock = (*gatedDurationTerminalDrainClock)(nil)

func waitForDurationTextOrError(t *testing.T, writer *durationTestWriter, runErr <-chan error, want string) {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case got := <-writer.writes:
			if strings.Contains(got, want) {
				return
			}
		case err := <-runErr:
			t.Fatalf("duration session ended before %q: %v; output=%q", want, err, writer.String())
		case <-timer.C:
			t.Fatalf("timed out waiting for %q; output=%q", want, writer.String())
		}
	}
}

func assertZeroSampleTerminalTranscript(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reopen zero-sample transcript artifact: %v", err)
	}
	if !bytes.HasSuffix(data, []byte("\n")) {
		t.Fatal("zero-sample transcript is missing its trailing JSONL newline")
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
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
	if len(eventTypes) != 1 || eventTypes[0] != messages.StreamTypeSessionClose {
		t.Fatalf("zero-sample transcript event order = %v, want only session_close", eventTypes)
	}
	if !bytes.Contains(terminalPayload, []byte("max_duration")) {
		t.Fatalf("zero-sample terminal record = %s, want max_duration", terminalPayload)
	}
}
