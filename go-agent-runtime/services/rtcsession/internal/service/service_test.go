package service

import (
	"context"
	"errors"
	"io"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rtcsession"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport/rtc"
)

func TestRuntimeSuccessRepeatedStartCloseReverseOrderAndIndependent(t *testing.T) {
	var (
		mu     sync.Mutex
		events []string
	)
	components := rtcsession.SessionRTCComponents{
		ResolveSignaling: func(_ context.Context, endpoint string) (rtc.Signaling, error) {
			return &testSignaling{id: endpoint, closeFn: func() error {
				mu.Lock()
				events = append(events, endpoint+" signaling close")
				mu.Unlock()
				return nil
			}}, nil
		},
		NewDataPlane: func(_ context.Context, signaling rtc.Signaling) (rtcsession.SessionRTCDataPlane, error) {
			id := signaling.(*testSignaling).id
			return &testDataPlane{id: id, attachFn: func(context.Context, sharedaudio.InboundMedia) error {
				mu.Lock()
				events = append(events, id+" attach")
				mu.Unlock()
				return nil
			}, closeFn: func() error {
				mu.Lock()
				events = append(events, id+" data close")
				mu.Unlock()
				return nil
			}}, nil
		},
		OpenMediaSource: func(_ context.Context, source string) (sharedaudio.InboundMedia, error) {
			return &testInbound{closeFn: func() error {
				mu.Lock()
				events = append(events, source+" media close")
				mu.Unlock()
				return nil
			}}, nil
		},
	}
	service := New(components, nil, nil)
	first, err := service.NewRuntime(rtcsession.SessionRuntimeSelection{Transport: "webrtc", SignalingEndpoint: "first", MediaSource: "first"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.NewRuntime(rtcsession.SessionRuntimeSelection{Transport: "webrtc", SignalingEndpoint: "second", MediaSource: "second"})
	if err != nil {
		t.Fatal(err)
	}

	firstPlane, err := first.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	repeatedPlane, err := first.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if firstPlane != repeatedPlane {
		t.Fatal("repeated Start returned a different data plane")
	}
	if _, err := second.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	got := append([]string(nil), events...)
	mu.Unlock()
	want := []string{
		"first attach",
		"second attach",
		"first media close",
		"first data close",
		"first signaling close",
		"second media close",
		"second data close",
		"second signaling close",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("lifecycle events = %v, want %v", got, want)
	}
	if _, err := first.Start(context.Background()); !errors.Is(err, rtcsession.ErrSessionRTCRuntimeClosed) {
		t.Fatalf("start after close = %v, want closed identity", err)
	}
}

func TestRuntimeNilDependenciesAndReturns(t *testing.T) {
	if _, err := New(rtcsession.SessionRTCComponents{}, nil, nil).NewRuntime(rtcsession.SessionRuntimeSelection{}); !errors.Is(err, rtcsession.ErrSessionRTCRuntimeUnavailable) {
		t.Fatalf("nil components error = %v, want unavailable identity", err)
	}

	base := rtcsession.SessionRTCComponents{
		ResolveSignaling: func(context.Context, string) (rtc.Signaling, error) {
			return &testSignaling{}, nil
		},
		NewDataPlane: func(context.Context, rtc.Signaling) (rtcsession.SessionRTCDataPlane, error) {
			return &testDataPlane{}, nil
		},
		OpenMediaSource: func(context.Context, string) (sharedaudio.InboundMedia, error) {
			return &testInbound{}, nil
		},
	}
	tests := []struct {
		name   string
		mutate func(*rtcsession.SessionRTCComponents)
		phase  string
		cause  error
	}{
		{name: "nil signaling", mutate: func(c *rtcsession.SessionRTCComponents) {
			c.ResolveSignaling = func(context.Context, string) (rtc.Signaling, error) { return nil, nil }
		}, phase: "resolve signaling", cause: rtcsession.ErrSessionRTCRuntimeUnavailable},
		{name: "nil data plane", mutate: func(c *rtcsession.SessionRTCComponents) {
			c.NewDataPlane = func(context.Context, rtc.Signaling) (rtcsession.SessionRTCDataPlane, error) { return nil, nil }
		}, phase: "create RTC peer/data path", cause: rtcsession.ErrSessionRTCDataPlaneUnavailable},
		{name: "nil media", mutate: func(c *rtcsession.SessionRTCComponents) {
			c.OpenMediaSource = func(context.Context, string) (sharedaudio.InboundMedia, error) { return nil, nil }
		}, phase: "open media source", cause: rtcsession.ErrSessionRTCRuntimeUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			components := base
			test.mutate(&components)
			runtime, err := New(components, nil, nil).NewRuntime(rtcsession.SessionRuntimeSelection{})
			if err != nil {
				t.Fatal(err)
			}
			_, err = runtime.Start(context.Background())
			if !errors.Is(err, test.cause) {
				t.Fatalf("start error = %v, want cause %v", err, test.cause)
			}
			var phaseErr *rtcsession.SessionRTCRuntimeError
			if !errors.As(err, &phaseErr) || phaseErr.Phase != test.phase {
				t.Fatalf("start error = %v, want phase %q", err, test.phase)
			}
		})
	}
}

func TestRuntimeTypedNilReturnsAreUnavailable(t *testing.T) {
	var signaling *testSignaling
	components := testComponents()
	components.ResolveSignaling = func(context.Context, string) (rtc.Signaling, error) {
		return signaling, nil
	}
	runtime, err := New(components, nil, nil).NewRuntime(rtcsession.SessionRuntimeSelection{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Start(context.Background()); !errors.Is(err, rtcsession.ErrSessionRTCRuntimeUnavailable) {
		t.Fatalf("typed nil signaling error = %v, want unavailable identity", err)
	}
}

func TestRuntimePartialStartResolveFailureRetainsCause(t *testing.T) {
	cause := errors.New("signaling offline")
	components := testComponents()
	components.ResolveSignaling = func(context.Context, string) (rtc.Signaling, error) { return nil, cause }
	assertPartialStartFailure(t, components, cause, nil, nil, "resolve signaling")
}

func TestRuntimePartialStartDataFailureCleansSignaling(t *testing.T) {
	cause := errors.New("peer allocation failed")
	var events []string
	components := testComponents()
	components.ResolveSignaling = func(context.Context, string) (rtc.Signaling, error) {
		return &testSignaling{closeFn: func() error { events = append(events, "signaling close"); return nil }}, nil
	}
	components.NewDataPlane = func(context.Context, rtc.Signaling) (rtcsession.SessionRTCDataPlane, error) {
		return nil, cause
	}
	assertPartialStartFailure(t, components, cause, &events, []string{"signaling close"}, "create RTC peer/data path")
}

func TestRuntimePartialStartMediaFailureCleansDataAndSignaling(t *testing.T) {
	cause := errors.New("media unavailable")
	var events []string
	components := testComponents()
	components.ResolveSignaling = func(context.Context, string) (rtc.Signaling, error) {
		return &testSignaling{closeFn: func() error { events = append(events, "signaling close"); return nil }}, nil
	}
	components.NewDataPlane = func(context.Context, rtc.Signaling) (rtcsession.SessionRTCDataPlane, error) {
		return &testDataPlane{closeFn: func() error { events = append(events, "data close"); return nil }}, nil
	}
	components.OpenMediaSource = func(context.Context, string) (sharedaudio.InboundMedia, error) { return nil, cause }
	assertPartialStartFailure(t, components, cause, &events, []string{"data close", "signaling close"}, "open media source")
}

func TestRuntimePartialStartAttachFailureCleansAllResources(t *testing.T) {
	cause := errors.New("media attach rejected")
	var events []string
	components := testComponents()
	components.ResolveSignaling = func(context.Context, string) (rtc.Signaling, error) {
		return &testSignaling{closeFn: func() error { events = append(events, "signaling close"); return nil }}, nil
	}
	components.NewDataPlane = func(context.Context, rtc.Signaling) (rtcsession.SessionRTCDataPlane, error) {
		return &testDataPlane{
			attachFn: func(context.Context, sharedaudio.InboundMedia) error { return cause },
			closeFn:  func() error { events = append(events, "data close"); return nil },
		}, nil
	}
	components.OpenMediaSource = func(context.Context, string) (sharedaudio.InboundMedia, error) {
		return &testInbound{closeFn: func() error { events = append(events, "media close"); return nil }}, nil
	}
	assertPartialStartFailure(t, components, cause, &events, []string{"media close", "data close", "signaling close"}, "attach media source")
}

func assertPartialStartFailure(t *testing.T, components rtcsession.SessionRTCComponents, cause error, gotEvents *[]string, wantEvents []string, wantPhase string) {
	t.Helper()
	runtime, err := New(components, nil, nil).NewRuntime(rtcsession.SessionRuntimeSelection{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.Start(context.Background())
	if !errors.Is(err, cause) {
		t.Fatalf("start error = %v, want cause %v", err, cause)
	}
	var phaseErr *rtcsession.SessionRTCRuntimeError
	if !errors.As(err, &phaseErr) || phaseErr.Phase != wantPhase {
		t.Fatalf("start error = %v, want phase %q", err, wantPhase)
	}
	actualEvents := []string(nil)
	if gotEvents != nil {
		actualEvents = *gotEvents
	}
	if !reflect.DeepEqual(actualEvents, wantEvents) {
		t.Fatalf("cleanup events = %v, want %v", actualEvents, wantEvents)
	}
}

func TestRuntimeCancellationAndConcurrentCloseAreBounded(t *testing.T) {
	entered := make(chan struct{})
	components := rtcsession.SessionRTCComponents{
		ResolveSignaling: func(ctx context.Context, _ string) (rtc.Signaling, error) {
			close(entered)
			<-ctx.Done()
			return nil, ctx.Err()
		},
		NewDataPlane: func(context.Context, rtc.Signaling) (rtcsession.SessionRTCDataPlane, error) {
			t.Fatal("data plane was created after cancellation")
			return nil, nil
		},
		OpenMediaSource: func(context.Context, string) (sharedaudio.InboundMedia, error) {
			t.Fatal("media was opened after cancellation")
			return nil, nil
		},
	}
	runtime, err := New(components, nil, nil).NewRuntime(rtcsession.SessionRuntimeSelection{})
	if err != nil {
		t.Fatal(err)
	}
	startDone := make(chan error, 1)
	go func() {
		_, startErr := runtime.Start(context.Background())
		startDone <- startErr
	}()
	<-entered
	closeDone := make(chan error, 1)
	go func() { closeDone <- runtime.Close() }()
	if err := <-startDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled start error = %v, want context cancellation", err)
	}
	if err := <-closeDone; err != nil {
		t.Fatalf("close after cancellation = %v", err)
	}

	valid := New(testComponents(), nil, nil)
	closedRuntime, err := valid.NewRuntime(rtcsession.SessionRuntimeSelection{})
	if err != nil {
		t.Fatal(err)
	}
	if err := closedRuntime.Close(); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := closedRuntime.Close(); err != nil {
				t.Errorf("concurrent close = %v", err)
			}
		}()
	}
	wg.Wait()
}

func TestRuntimeCloseDuringStartReturnsCleanupErrorsInReverseOrder(t *testing.T) {
	mediaErr := errors.New("media close failed")
	dataErr := errors.New("data close failed")
	signalingErr := errors.New("signaling close failed")
	attachStarted := make(chan struct{})
	var events []string
	components := testComponents()
	components.ResolveSignaling = func(context.Context, string) (rtc.Signaling, error) {
		return &testSignaling{closeFn: func() error {
			events = append(events, "signaling close")
			return signalingErr
		}}, nil
	}
	components.NewDataPlane = func(context.Context, rtc.Signaling) (rtcsession.SessionRTCDataPlane, error) {
		return &testDataPlane{
			attachFn: func(ctx context.Context, _ sharedaudio.InboundMedia) error {
				close(attachStarted)
				<-ctx.Done()
				return ctx.Err()
			},
			closeFn: func() error {
				events = append(events, "data close")
				return dataErr
			},
		}, nil
	}
	components.OpenMediaSource = func(context.Context, string) (sharedaudio.InboundMedia, error) {
		return &testInbound{closeFn: func() error {
			events = append(events, "media close")
			return mediaErr
		}}, nil
	}
	runtime, err := New(components, nil, nil).NewRuntime(rtcsession.SessionRuntimeSelection{})
	if err != nil {
		t.Fatal(err)
	}
	startDone := make(chan error, 1)
	go func() {
		_, startErr := runtime.Start(context.Background())
		startDone <- startErr
	}()
	<-attachStarted
	closeDone := make(chan error, 1)
	go func() { closeDone <- runtime.Close() }()
	if err := <-startDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled start error = %v, want context cancellation", err)
	}
	closeErr := <-closeDone
	for _, cause := range []error{mediaErr, dataErr, signalingErr} {
		if !errors.Is(closeErr, cause) {
			t.Fatalf("close error = %v, want cleanup cause %v", closeErr, cause)
		}
	}
	if want := []string{"media close", "data close", "signaling close"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("cleanup events = %v, want %v", events, want)
	}
}

func TestInferencerProviderFailureAndNilSession(t *testing.T) {
	providerErr := errors.New("provider connect failed")
	failingRuntime := &testRuntime{}
	service := New(testComponents(), nil, nil)
	failing := service.WrapInferencer(failingRuntime, testInferencerFunc(func(context.Context) (messages.Session, error) {
		return nil, providerErr
	}))
	_, err := failing.ConnectSession(context.Background())
	if !errors.Is(err, providerErr) || failingRuntime.closeCount.Load() != 1 {
		t.Fatalf("provider failure = %v, runtime closes = %d", err, failingRuntime.closeCount.Load())
	}

	nilSessionRuntime := &testRuntime{}
	nilSession := service.WrapInferencer(nilSessionRuntime, testInferencerFunc(func(context.Context) (messages.Session, error) {
		return nil, nil
	}))
	if _, err := nilSession.ConnectSession(context.Background()); !errors.Is(err, rtcsession.ErrSessionRTCRuntimeUnavailable) {
		t.Fatalf("nil provider session error = %v, want unavailable identity", err)
	}
	if nilSessionRuntime.closeCount.Load() != 1 {
		t.Fatalf("nil provider session runtime closes = %d, want one", nilSessionRuntime.closeCount.Load())
	}
}

func TestInferencerCapabilityForwarding(t *testing.T) {
	providerErr := errors.New("provider connect failed")
	service := New(testComponents(), nil, nil)

	provider := &testSession{
		media:       sharedaudio.MediaEndpoints{Inbound: &testInbound{}, Outbound: &testOutbound{}},
		sendOutcome: messages.SessionSendOutcome{Status: messages.SessionSendBufferFull, Err: providerErr},
		inputDrops:  7,
		outputDrops: 11,
		responseOK:  true,
		complete:    true,
		terminalErr: providerErr,
	}
	runtime := &testRuntime{}
	wrapped := service.WrapInferencer(runtime, &testConfigurableInferencer{connect: func(context.Context) (messages.Session, error) {
		return provider, nil
	}})
	assertAudioConfigurationCapability(t, wrapped)
	session, err := wrapped.ConnectSession(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	assertMediaCapability(t, session, provider)
	assertSendOutcome(t, session, providerErr)
	assertDropCounters(t, session)
	assertResponseCapabilities(t, session)
	assertCompleteMessageCapabilities(t, session)
	assertMessageCapabilities(t, session)
	assertTerminalCapability(t, session, providerErr)
	assertSessionClose(t, wrapped, session, provider, runtime, providerErr)
}

func TestLazyDialerPreservesDialAndNilConnectionErrors(t *testing.T) {
	dialErr := errors.New("dial rejected")
	service := New(testComponents(), nil, nil)
	runtime := &testRuntime{dataPlane: &testDataPlane{dialFn: func(string, map[string]string) (transport.Conn, error) {
		return nil, dialErr
	}}}
	dialer := service.NewLazyDialer(runtime)
	_, err := dialer.Dial("endpoint", nil)
	if !errors.Is(err, dialErr) {
		t.Fatalf("dial error = %v, want identity", err)
	}

	nilConnRuntime := &testRuntime{dataPlane: &testDataPlane{dialFn: func(string, map[string]string) (transport.Conn, error) {
		return nil, nil
	}}}
	_, err = service.NewLazyDialer(nilConnRuntime).Dial("endpoint", nil)
	if !errors.Is(err, rtc.ErrNilConnection) {
		t.Fatalf("nil connection error = %v, want RTC nil-connection identity", err)
	}
}

func testComponents() rtcsession.SessionRTCComponents {
	return rtcsession.SessionRTCComponents{
		ResolveSignaling: func(context.Context, string) (rtc.Signaling, error) { return &testSignaling{}, nil },
		NewDataPlane: func(context.Context, rtc.Signaling) (rtcsession.SessionRTCDataPlane, error) {
			return &testDataPlane{}, nil
		},
		OpenMediaSource: func(context.Context, string) (sharedaudio.InboundMedia, error) {
			return &testInbound{}, nil
		},
	}
}

type testSignaling struct {
	rtc.Signaling
	id      string
	closeFn func() error
}

func (s *testSignaling) Close() error {
	if s == nil || s.closeFn == nil {
		return nil
	}
	return s.closeFn()
}

type testInbound struct{ closeFn func() error }

func (*testInbound) ReadFrame(context.Context) (sharedaudio.PCMFrame, error) {
	return sharedaudio.PCMFrame{}, io.EOF
}
func (m *testInbound) Close() error {
	if m == nil || m.closeFn == nil {
		return nil
	}
	return m.closeFn()
}

type testOutbound struct{}

func (*testOutbound) WriteFrame(context.Context, sharedaudio.PCMFrame) error { return nil }
func (*testOutbound) Close() error                                           { return nil }

type testDataPlane struct {
	id       string
	attachFn func(context.Context, sharedaudio.InboundMedia) error
	dialFn   func(string, map[string]string) (transport.Conn, error)
	closeFn  func() error
}

func (d *testDataPlane) Dial(endpoint string, headers map[string]string) (transport.Conn, error) {
	if d != nil && d.dialFn != nil {
		return d.dialFn(endpoint, headers)
	}
	return testConn{}, nil
}

func (d *testDataPlane) AttachInboundMedia(ctx context.Context, media sharedaudio.InboundMedia) error {
	if d == nil || d.attachFn == nil {
		return nil
	}
	return d.attachFn(ctx, media)
}

func (d *testDataPlane) Close() error {
	if d == nil || d.closeFn == nil {
		return nil
	}
	return d.closeFn()
}

type testConn struct{}

func (testConn) ReadMessage() (int, []byte, error) { return 0, nil, io.EOF }
func (testConn) WriteMessage(int, []byte) error    { return nil }
func (testConn) Close() error                      { return nil }

type testRuntime struct {
	dataPlane  rtcsession.SessionRTCDataPlane
	startCount atomic.Int32
	closeCount atomic.Int32
}

func (r *testRuntime) Start(context.Context) (rtcsession.SessionRTCDataPlane, error) {
	r.startCount.Add(1)
	if r.dataPlane == nil {
		return &testDataPlane{}, nil
	}
	return r.dataPlane, nil
}

func (r *testRuntime) Close() error {
	r.closeCount.Add(1)
	return nil
}

type testInferencerFunc func(context.Context) (messages.Session, error)

func (f testInferencerFunc) ConnectSession(ctx context.Context) (messages.Session, error) {
	return f(ctx)
}

type testConfigurableInferencer struct {
	connect func(context.Context) (messages.Session, error)
}

func (i *testConfigurableInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	return i.connect(ctx)
}
func (*testConfigurableInferencer) SetSessionAudioOutput(models.AudioFormat, models.SampleRate) {}
func (*testConfigurableInferencer) SetSessionAudioInput(models.AudioFormat, models.SampleRate)  {}

type testSession struct {
	media       sharedaudio.MediaEndpoints
	closeCount  atomic.Int32
	closeOnce   sync.Once
	sendOutcome messages.SessionSendOutcome
	inputDrops  int64
	outputDrops int64
	responseOK  bool
	complete    bool
	terminalErr error
	done        chan struct{}
}

func (s *testSession) Send(context.Context, messages.StreamMessage) bool      { return true }
func (s *testSession) Receive() *messages.TypedBuffer[messages.StreamMessage] { return nil }
func (s *testSession) Done() <-chan struct{} {
	if s.done == nil {
		s.done = make(chan struct{})
	}
	return s.done
}
func (s *testSession) Close() error {
	s.closeOnce.Do(func() {
		s.closeCount.Add(1)
		if s.done != nil {
			close(s.done)
		}
	})
	return s.terminalErr
}
func (s *testSession) RTCMedia() sharedaudio.MediaEndpoints { return s.media }
func (s *testSession) SendWithOutcome(context.Context, messages.StreamMessage) messages.SessionSendOutcome {
	return s.sendOutcome
}
func (s *testSession) InputDrops() int64  { return s.inputDrops }
func (s *testSession) OutputDrops() int64 { return s.outputDrops }
func (s *testSession) RequestResponse(context.Context) messages.SessionSendOutcome {
	if s.responseOK {
		return messages.SessionSendOutcome{Status: messages.SessionSendSucceeded}
	}
	return messages.SessionSendOutcome{Status: messages.SessionSendTerminalFailure}
}
func (s *testSession) SupportsResponseRequests() bool                                    { return s.responseOK }
func (s *testSession) SendMessage(context.Context, messages.Message) bool                { return true }
func (s *testSession) SendMessageWithoutResponse(context.Context, messages.Message) bool { return true }
func (s *testSession) SupportsCompleteMessages() bool                                    { return s.complete }
func (s *testSession) SupportsCompleteMessagesWithoutResponse() bool                     { return s.complete }
func (s *testSession) TerminalError() error                                              { return s.terminalErr }
func (s *testSession) SetSessionAudioOutput(models.AudioFormat, models.SampleRate)       {}
