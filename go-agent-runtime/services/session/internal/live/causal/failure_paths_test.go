package causal_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	live "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/internal/live"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"go.uber.org/goleak"
)

// TestMain fails the package when a live session leaves a goroutine running.
func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

type fakeDevice struct {
	closeErr error
	closed   atomic.Int32
}

func (*fakeDevice) Media() devices.MediaPorts { return devices.MediaPorts{} }
func (d *fakeDevice) Close() error {
	d.closed.Add(1)
	return d.closeErr
}

type fakeDevices struct {
	handle devices.Handle
	err    error
}

func (s fakeDevices) Open(context.Context, devices.Request) (devices.Handle, error) {
	return s.handle, s.err
}

type finalizingRecorder struct {
	finalizeErr error
	runErr      error
	finalized   atomic.Int32
}

func (*finalizingRecorder) RecordMessage(context.Context, session.LiveRecord) error    { return nil }
func (*finalizingRecorder) RecordAudio(context.Context, session.LiveAudioRecord) error { return nil }
func (*finalizingRecorder) RecordEvent(context.Context, session.LiveEvent) error       { return nil }
func (r *finalizingRecorder) Finalize(_ context.Context, runErr error) error {
	r.runErr = runErr
	r.finalized.Add(1)
	return r.finalizeErr
}

// scriptedSession is a provider whose Send outcome is chosen per message.
type scriptedSession struct {
	receive *messages.TypedBuffer[messages.StreamMessage]
	done    chan struct{}
	once    sync.Once
	send    func(context.Context, messages.StreamMessage) bool
}

func newScriptedSession(send func(context.Context, messages.StreamMessage) bool) *scriptedSession {
	return &scriptedSession{receive: messages.NewTypedBuffer[messages.StreamMessage](32), done: make(chan struct{}), send: send}
}

func (s *scriptedSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	if s.send != nil {
		return s.send(ctx, msg)
	}
	return ctx.Err() == nil
}
func (s *scriptedSession) Receive() *messages.TypedBuffer[messages.StreamMessage] { return s.receive }
func (s *scriptedSession) Done() <-chan struct{}                                  { return s.done }
func (s *scriptedSession) Close() error {
	s.once.Do(func() { close(s.done) })
	return nil
}

type sessionInferencer struct{ session messages.Session }

func (i sessionInferencer) ConnectSession(context.Context) (messages.Session, error) {
	return i.session, nil
}

func newService(provider messages.Session, factoryErr error, clock *platformclock.Deterministic) *live.Service {
	dependencies := live.Dependencies{InferencerFactory: func(context.Context, session.LiveRequest) (messages.SessionInferencer, error) {
		if factoryErr != nil {
			return nil, factoryErr
		}
		return sessionInferencer{session: provider}, nil
	}}
	if clock != nil {
		dependencies.Clock, dependencies.Scheduler = clock.Now, clock
	}
	return live.New(dependencies)
}

func startLive(t *testing.T, ctx context.Context, service *live.Service, request session.LiveRequest) session.LiveHandle {
	t.Helper()
	handle, err := service.OpenLive(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if err := handle.Start(ctx); err != nil {
		t.Fatalf("Start = %v", err)
	}
	return handle
}

func drainEvents(handle session.LiveHandle) []session.LiveEvent {
	var events []session.LiveEvent
	for event := range handle.Events() {
		events = append(events, event)
	}
	return events
}

// A provider that fails after device admission closes the opened device and
// handle, and the recorder sees the joined cause exactly once.
func TestRunLiveStartFailureClosesDeviceAndFinalizesRecorder(t *testing.T) {
	factoryErr, deviceErr := errors.New("provider unavailable"), errors.New("device close failed")
	device, recorder := &fakeDevice{closeErr: deviceErr}, &finalizingRecorder{}
	err := newService(nil, factoryErr, nil).RunLive(t.Context(), session.LiveRunOptions{
		Request:       session.LiveRequest{SessionID: "start-failure"},
		Devices:       fakeDevices{handle: device},
		DeviceRequest: devices.Request{PlaybackEnabled: true},
		Recorder:      recorder,
	})
	if !errors.Is(err, factoryErr) || !errors.Is(err, deviceErr) {
		t.Fatalf("RunLive = %v, want provider and device close causes", err)
	}
	if device.closed.Load() != 1 || recorder.finalized.Load() != 1 || !errors.Is(recorder.runErr, factoryErr) {
		t.Fatalf("device closed %d, recorder finalized %d with %v", device.closed.Load(), recorder.finalized.Load(), recorder.runErr)
	}
}

func runAdmissionFailure(t *testing.T, ctx context.Context, options session.LiveRunOptions, want string) {
	t.Helper()
	finalizeErr := errors.New("finalize failed")
	recorder := &finalizingRecorder{finalizeErr: finalizeErr}
	options.Request, options.Recorder = session.LiveRequest{SessionID: "admission"}, recorder
	err := newService(newScriptedSession(nil), nil, nil).RunLive(ctx, options)
	if err == nil || !strings.Contains(err.Error(), want) || !errors.Is(err, finalizeErr) {
		t.Fatalf("RunLive = %v, want %q joined with the finalization failure", err, want)
	}
	if recorder.finalized.Load() != 1 || recorder.runErr == nil {
		t.Fatalf("recorder finalized %d times with %v", recorder.finalized.Load(), recorder.runErr)
	}
}

func TestRunLiveAdmissionFailuresFinalizeRecorder(t *testing.T) {
	runAdmissionFailure(t, t.Context(), session.LiveRunOptions{CaptureTurns: []devices.FileInput{{}}}, "finite capture inputs require a device service")
	runAdmissionFailure(t, t.Context(), session.LiveRunOptions{
		Devices: fakeDevices{err: errors.New("no such device")}, DeviceRequest: devices.Request{CaptureEnabled: true},
	}, "open live devices: no such device")
	runAdmissionFailure(t, t.Context(), session.LiveRunOptions{
		Devices: fakeDevices{}, DeviceRequest: devices.Request{PlaybackEnabled: true},
	}, "nil handle")
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	runAdmissionFailure(t, canceled, session.LiveRunOptions{}, context.Canceled.Error())
}

// An operator cancellation is a clean terminal classified as user_cancelled.
func TestUserCancellationFinishesCleanlyWithCancellationTerminal(t *testing.T) {
	ctx, cancel := context.WithCancelCause(t.Context())
	handle := startLive(t, ctx, newService(newScriptedSession(nil), nil, nil), session.LiveRequest{SessionID: "user-cancel"})
	cancel(session.ErrLiveUserCancellation)
	if err := handle.Wait(); err != nil {
		t.Fatalf("Wait after user cancellation = %v, want nil", err)
	}
	events := drainEvents(handle)
	terminal := events[len(events)-1]
	if terminal.Kind != string(session.LiveEventTerminal) || terminal.Terminal == nil || terminal.Terminal.TerminalReason != messages.TerminalReasonCancellation {
		t.Fatalf("terminal = %+v, want user cancellation", terminal)
	}
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
}

// Controls report unknown kinds, provider rejection and sender cancellation.
func TestSendLiveControlErrorBranches(t *testing.T) {
	blocked := make(chan struct{})
	provider := newScriptedSession(func(ctx context.Context, msg messages.StreamMessage) bool {
		if msg.Type == messages.StreamTypeResponseCreate {
			close(blocked)
			<-ctx.Done()
		}
		return ctx.Err() == nil && msg.Type != messages.StreamTypeTextDelta && msg.Type != messages.StreamTypeResponseCreate
	})
	handle := startLive(t, t.Context(), newService(provider, nil, nil), session.LiveRequest{SessionID: "control-errors"})
	if err := handle.Send(t.Context(), session.LiveControl{Kind: "bogus"}); err == nil {
		t.Fatal("unknown control kind accepted")
	}
	if err := handle.Send(t.Context(), session.LiveControl{Kind: session.LiveControlText, Text: "hi"}); err == nil || !strings.Contains(err.Error(), "rejected control") {
		t.Fatalf("rejected text control = %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() { result <- handle.Send(ctx, session.LiveControl{Kind: session.LiveControlResponseCreate}) }()
	<-blocked
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled control = %v, want context.Canceled", err)
	}
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
}

// A response that produced output disarms the provider watchdog: advancing far
// past its timeout does not fail the session.
func TestProviderLivenessDisarmsAfterCompletedResponse(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		clock := platformclock.NewDeterministic(time.Unix(900, 0), time.Millisecond)
		provider := newScriptedSession(nil)
		handle := startLive(t, t.Context(), newService(provider, nil, clock), session.LiveRequest{
			SessionID: "liveness-disarm", ProviderLiveness: session.LiveLivenessPolicy{Enabled: true, Timeout: 9 * time.Millisecond},
		})
		if err := handle.Send(t.Context(), session.LiveControl{Kind: session.LiveControlResponseCreate}); err != nil {
			t.Fatal(err)
		}
		for _, msg := range []messages.StreamMessage{
			{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, ResponseID: "r1", Value: messages.NewMessageStartValue()},
			{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, ResponseID: "r1", Value: &messages.TextDeltaValue{Content: "hello"}},
			{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, ResponseID: "r1", Value: messages.NewMessageEndValueWithTerminal(
				messages.TokenUsage{}, messages.TerminalReasonProviderAuthoredCompletion, messages.TerminalProvenanceProvider, messages.TerminalOutputComplete,
			)},
		} {
			provider.receive.Write(t.Context(), msg)
		}
		synctest.Wait()
		clock.AdvanceBy(time.Second)
		synctest.Wait()
		cause := errors.New("host stop")
		handle.Cancel(cause)
		if err := handle.Wait(); !errors.Is(err, cause) || errors.Is(err, session.ErrLiveSilentProviderTimeout) {
			t.Fatalf("Wait = %v, want host cause without liveness failure", err)
		}
		for _, event := range drainEvents(handle) {
			if event.Kind == string(session.LiveEventLiveness) {
				t.Fatalf("liveness fault after a completed response: %+v", event.Liveness)
			}
		}
	})
}
