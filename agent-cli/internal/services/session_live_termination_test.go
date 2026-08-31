package services

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport/rtc"
)

const liveTerminalDrainAcceptedOutput = "accepted live terminal delta"

type liveTerminalDrainFixture struct {
	ctx    context.Context
	cancel context.CancelFunc

	session   *liveTerminalDrainSession
	observer  *sessionProgressObserver
	opened    chan struct{}
	openOnce  sync.Once
	loopReady chan *agentloop.AgentLoop
	loop      *agentloop.AgentLoop

	done         chan struct{}
	options      sessionLoopOptions
	output       bytes.Buffer
	writer       io.Writer
	acceptedText string
	cleanup      []func()
}

func newLiveTerminalDrainFixture(t *testing.T) *liveTerminalDrainFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	f := &liveTerminalDrainFixture{
		ctx:          ctx,
		cancel:       cancel,
		session:      newLiveTerminalDrainSession(),
		opened:       make(chan struct{}),
		loopReady:    make(chan *agentloop.AgentLoop, 1),
		done:         make(chan struct{}),
		acceptedText: liveTerminalDrainAcceptedOutput,
	}
	f.writer = &f.output
	f.options = sessionLoopOptions{
		loopReady: f.loopReady,
	}
	f.session.opened = func() {
		// ConnectSession has accepted SESSION.OPEN. The service may not have
		// consumed it yet, but every trigger below remains buffered until the
		// loop reaches the corresponding terminal branch.
		f.openOnce.Do(func() { close(f.opened) })
	}
	return f
}

func (f *liveTerminalDrainFixture) run(t *testing.T, setup func(*liveTerminalDrainFixture) func()) error {
	t.Helper()
	trigger := setup(f)
	result := make(chan error, 1)
	go func() {
		result <- runAgentLoopSessionStream(f.ctx, f.writer, &liveTerminalDrainInferencer{session: f.session}, f.options)
	}()

	select {
	case f.loop = <-f.loopReady:
	case <-f.ctx.Done():
		t.Fatalf("session loop was not constructed: %v", f.ctx.Err())
	}
	select {
	case <-f.opened:
	case <-f.ctx.Done():
		t.Fatalf("session loop did not consume SESSION.OPEN: %v", f.ctx.Err())
	}
	if trigger != nil {
		trigger()
	}

	select {
	case err := <-result:
		f.finish()
		return err
	case <-time.After(2 * time.Second):
		f.finish()
		t.Fatalf("session terminal path did not return")
		return nil
	}
}

func (f *liveTerminalDrainFixture) finish() {
	f.cancel()
	_ = f.session.Close()
	for index := len(f.cleanup) - 1; index >= 0; index-- {
		f.cleanup[index]()
	}
}

func (f *liveTerminalDrainFixture) acceptedOutput() {
	if f.loop == nil {
		return
	}
	for _, msg := range []messages.StreamMessage{
		{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()},
		{Type: messages.StreamTypeTextStart, Role: messages.RoleAssistant, Value: messages.NewTextStartValue()},
		{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue(f.acceptedText)},
	} {
		if !f.loop.Deltas().Write(context.Background(), msg) {
			panic("live terminal regression delta buffer rejected test output")
		}
	}
}

func (f *liveTerminalDrainFixture) acceptedResponseThen(msg messages.StreamMessage) {
	f.acceptedOutput()
	if !f.loop.Deltas().Write(context.Background(), msg) {
		panic("live terminal regression delta buffer rejected terminal message")
	}
}

func TestRunAgentLoopSessionTerminalOutcomesAlwaysDrainAcceptedDelta(t *testing.T) {
	publicationErr := errors.New("page tool refresh failed")
	audioErr := errors.New("audio source failed")
	pumpErr := errors.New("RTC media pump failed")
	doneErr := errors.New("transport done failed")
	loopErr := errors.New("agent loop failed")
	providerErr := errors.New("provider terminal failure")
	writeErr := errors.New("terminal output writer failed")

	tests := []struct {
		name          string
		setup         func(*liveTerminalDrainFixture) func()
		wantErr       error
		wantErrorText string
	}{
		{
			name: "dynamic publication failure",
			setup: func(f *liveTerminalDrainFixture) func() {
				watch := make(chan webmcp.BrokerEvent)
				f.options.BrowserWatch = func(context.Context) <-chan webmcp.BrokerEvent { return watch }
				f.options.ToolDefinitionBase = []messages.ToolDefinition{{Name: "stable"}}
				f.options.RefreshToolDefinitions = func(context.Context) ([]messages.ToolDefinition, error) {
					return nil, publicationErr
				}
				f.session.emitCreated = true
				return func() { f.acceptedOutput() }
			},
			wantErr: publicationErr,
		},
		{
			name: "audio input failure",
			setup: func(f *liveTerminalDrainFixture) func() {
				f.options.AudioIn = &sessionAudioSource{source: &liveTerminalDrainFailingAudioSource{err: audioErr}}
				return func() { f.acceptedOutput() }
			},
			wantErr: audioErr,
		},
		{
			name: "RTC pump failure",
			setup: func(f *liveTerminalDrainFixture) func() {
				registry, err := audio.NewVirtualRegistry(audio.DefaultVirtualBackendConfig())
				if err != nil {
					t.Fatalf("new virtual registry: %v", err)
				}
				source, err := NewRTCDeviceSource(registry, "virtual:input")
				if err != nil {
					t.Fatalf("open RTC source: %v", err)
				}
				feed, err := audio.NewDeviceSink(registry, "virtual:output")
				if err != nil {
					_ = source.Close()
					t.Fatalf("open RTC feed: %v", err)
				}
				f.cleanup = append(f.cleanup, func() { _ = feed.Close() })
				f.options.rtcDeviceBinding = &RTCDeviceBinding{Source: source}
				f.session.media.Outbound = &liveTerminalDrainFailingOutboundMedia{err: pumpErr}
				return func() {
					f.acceptedOutput()
					if err := feed.WriteFrame(context.Background(), make([]int16, audio.FrameSize)); err != nil {
						t.Errorf("write RTC trigger frame: %v", err)
					}
				}
			},
			wantErr: pumpErr,
		},
		{
			name: "transport done without error",
			setup: func(f *liveTerminalDrainFixture) func() {
				f.options.Done = f.done
				f.options.DoneErr = func() error {
					f.acceptedOutput()
					return nil
				}
				return func() { close(f.done) }
			},
		},
		{
			name: "transport done with error",
			setup: func(f *liveTerminalDrainFixture) func() {
				f.options.Done = f.done
				f.options.DoneErr = func() error {
					f.acceptedOutput()
					return doneErr
				}
				return func() { close(f.done) }
			},
			wantErr: doneErr,
		},
		{
			name: "max duration timeout",
			setup: func(f *liveTerminalDrainFixture) func() {
				f.options.MaxDuration = 100 * time.Millisecond
				return func() { f.acceptedOutput() }
			},
		},
		{
			name: "session updated timeout",
			setup: func(f *liveTerminalDrainFixture) func() {
				f.observer = newSessionProgressObserver(nil, nil, "test", "test")
				f.options.observer = f.observer
				f.options.RequireSessionUpdated = true
				f.options.SessionUpdatedTimeout = 25 * time.Millisecond
				f.observer.requireSessionUpdated = true
				f.observer.scheduleAudioInputs([]ScheduledAudioInput{{AfterCompletedTurns: 0, PCM: []byte{1}}})
				return func() { f.acceptedOutput() }
			},
			wantErr: ErrSessionScheduledAudioConfigTimeout,
		},
		{
			name: "caller cancellation while awaiting response",
			setup: func(f *liveTerminalDrainFixture) func() {
				return func() {
					f.acceptedOutput()
					f.cancel()
				}
			},
			wantErr:       context.Canceled,
			wantErrorText: "awaiting model response after end-of-turn",
		},
		{
			name: "observed provider completion",
			setup: func(f *liveTerminalDrainFixture) func() {
				return func() {
					f.acceptedOutput()
					_ = f.session.Close()
				}
			},
		},
		{
			name: "loop run failure",
			setup: func(f *liveTerminalDrainFixture) func() {
				return func() {
					f.acceptedOutput()
					f.session.recv.Write(context.Background(), messages.StreamMessage{
						Type:  messages.StreamTypeError,
						Value: messages.NewErrorValueWithError(loopErr),
					})
				}
			},
			wantErr: loopErr,
		},
		{
			name: "terminal provider error",
			setup: func(f *liveTerminalDrainFixture) func() {
				return func() {
					f.acceptedResponseThen(messages.StreamMessage{
						Type:  messages.StreamTypeError,
						Value: messages.NewErrorValueWithError(providerErr),
					})
				}
			},
			wantErr: providerErr,
		},
		{
			name: "terminal provider message end",
			setup: func(f *liveTerminalDrainFixture) func() {
				return func() {
					f.acceptedResponseThen(messages.StreamMessage{
						Type:  messages.StreamTypeMessageEnd,
						Role:  messages.RoleAssistant,
						Value: messages.NewMessageEndValue(messages.TokenUsage{}),
					})
				}
			},
		},
		{
			name: "terminal provider text end",
			setup: func(f *liveTerminalDrainFixture) func() {
				return func() {
					f.acceptedResponseThen(messages.StreamMessage{
						Type:  messages.StreamTypeTextEnd,
						Role:  messages.RoleAssistant,
						Value: messages.NewTextEndValue(),
					})
				}
			},
		},
		{
			name: "terminal provider session close",
			setup: func(f *liveTerminalDrainFixture) func() {
				return func() {
					f.acceptedResponseThen(messages.StreamMessage{
						Type:  messages.StreamTypeSessionClose,
						Value: messages.NewSessionCloseValue("live-test", "provider closed"),
					})
				}
			},
		},
		{
			name: "message processing output failure",
			setup: func(f *liveTerminalDrainFixture) func() {
				f.writer = &liveTerminalDrainFailingWriter{target: &f.output, failAfter: 2, err: writeErr}
				return func() {
					f.acceptedResponseThen(messages.StreamMessage{
						Type:  messages.StreamTypeSessionClose,
						Value: messages.NewSessionCloseValue("live-test", "provider closed"),
					})
				}
			},
			wantErr: writeErr,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newLiveTerminalDrainFixture(t)
			fixture.acceptedText = liveTerminalDrainAcceptedOutput + ": " + testCase.name
			err := fixture.run(t, testCase.setup)
			if testCase.wantErr != nil && !errors.Is(err, testCase.wantErr) {
				t.Fatalf("terminal error = %v, want errors.Is(..., %v)", err, testCase.wantErr)
			}
			if testCase.wantErr == nil && err != nil {
				t.Fatalf("terminal error = %v, want clean completion", err)
			}
			if testCase.wantErrorText != "" && !strings.Contains(errorString(err), testCase.wantErrorText) {
				t.Fatalf("terminal error = %v, want text %q", err, testCase.wantErrorText)
			}
			if !strings.Contains(fixture.output.String(), fixture.acceptedText) {
				t.Fatalf("terminal path lost accepted output %q; rendered output=%q", fixture.acceptedText, fixture.output.String())
			}
		})
	}
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

type liveTerminalDrainInferencer struct {
	session *liveTerminalDrainSession
}

func (i *liveTerminalDrainInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	if !i.session.recv.Write(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeSessionOpen,
		Value: messages.NewSessionOpenValue("live-terminal-drain", "test"),
	}) {
		return nil, ctx.Err()
	}
	if i.session.opened != nil {
		i.session.opened()
	}
	if i.session.emitCreated && !i.session.recv.Write(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeSessionCreated,
		Value: messages.NewSessionCreatedValue("live-terminal-drain", "test"),
	}) {
		return nil, ctx.Err()
	}
	return i.session, nil
}

type liveTerminalDrainSession struct {
	recv        *messages.TypedBuffer[messages.StreamMessage]
	done        chan struct{}
	closeOnce   sync.Once
	emitCreated bool
	opened      func()
	media       rtc.MediaEndpoints
}

func newLiveTerminalDrainSession() *liveTerminalDrainSession {
	return &liveTerminalDrainSession{
		recv: messages.NewTypedBuffer[messages.StreamMessage](64),
		done: make(chan struct{}),
	}
}

func (s *liveTerminalDrainSession) Send(ctx context.Context, _ messages.StreamMessage) bool {
	if ctx == nil {
		return true
	}
	select {
	case <-ctx.Done():
		return false
	default:
		return true
	}
}

func (s *liveTerminalDrainSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.recv
}

func (s *liveTerminalDrainSession) Done() <-chan struct{} { return s.done }

func (s *liveTerminalDrainSession) Close() error {
	s.closeOnce.Do(func() { close(s.done) })
	return nil
}

func (s *liveTerminalDrainSession) RTCMedia() rtc.MediaEndpoints { return s.media }

type liveTerminalDrainFailingAudioSource struct {
	err error
}

func (s *liveTerminalDrainFailingAudioSource) ReadFrame(context.Context, []int16) error { return s.err }
func (*liveTerminalDrainFailingAudioSource) Close() error                               { return nil }

type liveTerminalDrainFailingOutboundMedia struct {
	err error
}

func (m *liveTerminalDrainFailingOutboundMedia) WriteFrame(context.Context, rtc.PCMFrame) error {
	return m.err
}
func (*liveTerminalDrainFailingOutboundMedia) Close() error { return nil }

type liveTerminalDrainFailingWriter struct {
	target    io.Writer
	failAfter int
	err       error
	mu        sync.Mutex
	writes    int
}

func (w *liveTerminalDrainFailingWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	w.writes++
	shouldFail := w.writes >= w.failAfter
	w.mu.Unlock()
	if shouldFail {
		return 0, w.err
	}
	return w.target.Write(data)
}
