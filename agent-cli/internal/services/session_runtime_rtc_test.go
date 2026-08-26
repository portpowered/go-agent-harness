package services

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport/rtc"
)

func TestSessionRTCRuntime_ComposesSelectedDependenciesAndClosesInReverseOrder(t *testing.T) {
	const (
		signalingEndpoint = "loopback://signaling/sentinel"
		mediaSource       = "fixture://media/sentinel"
	)

	var (
		events          []string
		gotSignalingURL string
		gotMediaSource  string
		gotAttached     rtc.InboundMedia
	)
	appendEvent := func(event string) { events = append(events, event) }

	signaling := &testRTCSignaling{close: func() error {
		appendEvent("close signaling")
		return nil
	}}
	media := &testRTCInboundMedia{close: func() error {
		appendEvent("close media")
		return nil
	}}
	dataPlane := &testRTCDataPlane{
		dial: func(string, map[string]string) (transport.Conn, error) { return testRTCConn{}, nil },
		attach: func(_ context.Context, inbound rtc.InboundMedia) error {
			gotAttached = inbound
			appendEvent("attach media")
			return nil
		},
		close: func() error {
			appendEvent("close data plane")
			return nil
		},
	}

	factory := NewSessionRTCRuntimeFactory(SessionRTCComponents{
		ResolveSignaling: func(_ context.Context, endpoint string) (rtc.Signaling, error) {
			gotSignalingURL = endpoint
			appendEvent("resolve signaling")
			return signaling, nil
		},
		NewDataPlane: func(_ context.Context, got rtc.Signaling) (SessionRTCDataPlane, error) {
			if got != signaling {
				t.Fatal("data-plane factory received a different signaling endpoint")
			}
			appendEvent("create data plane")
			return dataPlane, nil
		},
		OpenMediaSource: func(_ context.Context, source string) (rtc.InboundMedia, error) {
			gotMediaSource = source
			appendEvent("open media source")
			return media, nil
		},
	})

	selection := SessionRuntimeSelection{
		Transport:         SessionTransportWebRTC,
		SignalingEndpoint: signalingEndpoint,
		MediaSource:       mediaSource,
	}
	runtime, err := factory(selection)
	if err != nil {
		t.Fatalf("construct RTC runtime: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("runtime construction performed setup: %v", events)
	}

	data, err := runtime.Start(context.Background())
	if err != nil {
		t.Fatalf("start RTC runtime: %v", err)
	}
	if data != dataPlane {
		t.Fatal("runtime returned a different data plane")
	}
	if gotSignalingURL != signalingEndpoint || gotMediaSource != mediaSource {
		t.Fatalf("runtime inputs = (%q, %q), want exact (%q, %q)", gotSignalingURL, gotMediaSource, signalingEndpoint, mediaSource)
	}
	if gotAttached != media {
		t.Fatal("runtime attached a different media source")
	}
	if want := []string{"resolve signaling", "create data plane", "open media source", "attach media"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("startup order = %v, want %v", events, want)
	}

	if err := runtime.Close(); err != nil {
		t.Fatalf("close RTC runtime: %v", err)
	}
	if err := runtime.Close(); err != nil {
		t.Fatalf("second close RTC runtime: %v", err)
	}
	want := []string{
		"resolve signaling", "create data plane", "open media source", "attach media",
		"close media", "close data plane", "close signaling",
	}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("lifecycle events = %v, want %v", events, want)
	}
}

func TestSessionRTCRuntime_CleansPartialStartupAndPreservesCause(t *testing.T) {
	setupErr := errors.New("fixture media source unavailable")
	var events []string
	appendEvent := func(event string) { events = append(events, event) }
	dataPlane := &testRTCDataPlane{close: func() error {
		appendEvent("close data plane")
		return nil
	}}
	signaling := &testRTCSignaling{close: func() error {
		appendEvent("close signaling")
		return nil
	}}

	runtimeFactory := NewSessionRTCRuntimeFactory(SessionRTCComponents{
		ResolveSignaling: func(context.Context, string) (rtc.Signaling, error) {
			appendEvent("resolve signaling")
			return signaling, nil
		},
		NewDataPlane: func(context.Context, rtc.Signaling) (SessionRTCDataPlane, error) {
			appendEvent("create data plane")
			return dataPlane, nil
		},
		OpenMediaSource: func(context.Context, string) (rtc.InboundMedia, error) {
			appendEvent("open media source")
			return nil, setupErr
		},
	})
	runtime, err := runtimeFactory(SessionRuntimeSelection{
		Transport:         SessionTransportWebRTC,
		SignalingEndpoint: "loopback://partial",
		MediaSource:       "fixture://partial",
	})
	if err != nil {
		t.Fatalf("construct RTC runtime: %v", err)
	}

	if _, err := runtime.Start(context.Background()); err == nil {
		t.Fatal("start RTC runtime unexpectedly succeeded")
	} else {
		if !errors.Is(err, setupErr) {
			t.Fatalf("startup error = %v, want source cause", err)
		}
		var phaseErr *SessionRTCRuntimeError
		if !errors.As(err, &phaseErr) {
			t.Fatalf("startup error type = %T, want *SessionRTCRuntimeError", err)
		}
		if phaseErr.Phase != "open media source" {
			t.Fatalf("startup phase = %q, want open media source", phaseErr.Phase)
		}
	}
	if err := runtime.Close(); err != nil {
		t.Fatalf("close failed-start runtime: %v", err)
	}
	if want := []string{"resolve signaling", "create data plane", "open media source", "close data plane", "close signaling"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("partial lifecycle events = %v, want %v", events, want)
	}
}

func TestPlanSessionRuntime_WebRTCDispatchesThroughRuntimeFactory(t *testing.T) {
	const (
		signaling = " loopback://plan/sentinel "
		media     = "fixture://plan/sentinel"
	)
	runtime := &testSessionRTCRuntime{}
	var got SessionRuntimeSelection
	plan, err := planSessionRuntimeWithFactory(SessionRunOptions{
		SessionInferencer: &selectionTestInferencer{},
		Transport:         "WebRTC",
		Signaling:         signaling,
		MediaSource:       media,
	}, sessionRuntimeFactory{
		newRTCRuntime: func(selection SessionRuntimeSelection) (SessionRTCRuntime, error) {
			got = selection
			return runtime, nil
		},
	})
	if err != nil {
		t.Fatalf("planSessionRuntimeWithFactory: %v", err)
	}
	if got != (SessionRuntimeSelection{
		Transport:         SessionTransportWebRTC,
		SignalingEndpoint: signaling,
		MediaSource:       media,
	}) {
		t.Fatalf("runtime selection = %#v, want exact values", got)
	}
	if plan.rtcRuntime != runtime {
		t.Fatal("WebRTC plan did not retain the owned runtime")
	}
	if plan.transport != SessionTransportWebRTC || plan.signalingEndpoint != signaling || plan.mediaSource != media {
		t.Fatalf("plan selection fields = (%q, %q, %q), want exact WebRTC values", plan.transport, plan.signalingEndpoint, plan.mediaSource)
	}
	if _, ok := plan.inferencer.(*sessionRTCRuntimeInferencer); !ok {
		t.Fatalf("plan inferencer = %T, want RTC lifecycle wrapper", plan.inferencer)
	}
	if runtime.closeCount != 0 {
		t.Fatal("planning closed the RTC runtime before execution")
	}
	if err := runtime.Close(); err != nil {
		t.Fatalf("cleanup test runtime: %v", err)
	}
}

func TestSessionRTCRuntimeInferencerStartsBeforeProviderAndUsesRTCDataPlane(t *testing.T) {
	var order []string
	dataPlane := &testRTCDataPlane{
		dial: func(endpoint string, _ map[string]string) (transport.Conn, error) {
			order = append(order, "dial "+endpoint)
			return testRTCConn{}, nil
		},
	}
	runtime := &testSessionRTCRuntime{
		dataPlane: dataPlane,
		start:     func() { order = append(order, "start runtime") },
		close:     func() { order = append(order, "close runtime") },
	}
	inner := &testSessionInferencer{connect: func() (messages.Session, error) {
		order = append(order, "connect provider")
		return newScriptedSession(), nil
	}}
	wrapped := &sessionRTCRuntimeInferencer{inner: inner, runtime: runtime}

	session, err := wrapped.ConnectSession(context.Background())
	if err != nil {
		t.Fatalf("ConnectSession: %v", err)
	}
	dialer := &sessionRTCLazyDialer{runtime: runtime}
	if _, err := dialer.Dial("provider-endpoint", nil); err != nil {
		t.Fatalf("RTC data dial: %v", err)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("close session: %v", err)
	}
	if want := []string{"start runtime", "connect provider", "dial provider-endpoint", "close runtime"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("provider/runtime order = %v, want %v", order, want)
	}
}

func TestPlanWebRTCRecordSuppliesRuntimeDataPlaneToProvider(t *testing.T) {
	configDir := t.TempDir()
	writeSessionConfigFile(t, configDir, `
model:
  provider: grok
  grok:
    model: grok-rtc-test
    api_key: test-key
`)

	var order []string
	dataPlane := &testRTCDataPlane{
		dial: func(endpoint string, _ map[string]string) (transport.Conn, error) {
			order = append(order, "dial "+endpoint)
			return testRTCConn{}, nil
		},
	}
	runtime := &testSessionRTCRuntime{
		dataPlane: dataPlane,
		start:     func() { order = append(order, "start runtime") },
		close:     func() { order = append(order, "close runtime") },
	}
	var providerDialer transport.Dialer
	inner := &testSessionInferencer{connect: func() (messages.Session, error) {
		order = append(order, "connect provider")
		if providerDialer == nil {
			return nil, errors.New("provider did not receive a data dialer")
		}
		if _, err := providerDialer.Dial("provider-endpoint", nil); err != nil {
			return nil, err
		}
		return newScriptedSession(), nil
	}}

	plan, err := planSessionRuntimeWithFactory(SessionRunOptions{
		RecordPath:  filepath.Join(t.TempDir(), "rtc.session.json"),
		ConfigDir:   configDir,
		Transport:   SessionTransportWebRTC,
		Signaling:   "loopback://record/sentinel",
		MediaSource: "fixture://record/sentinel",
	}, sessionRuntimeFactory{
		newRTCRuntime: func(SessionRuntimeSelection) (SessionRTCRuntime, error) {
			return runtime, nil
		},
		newRecordingDialer: func(inner transport.Dialer, _, _ string) sessionRecordingDialer {
			return &testForwardingRecordingDialer{inner: inner}
		},
		newGrokSessionInferencer: func(_ config.GrokConfig, dialer transport.Dialer) (messages.SessionInferencer, error) {
			providerDialer = dialer
			return inner, nil
		},
	})
	if err != nil {
		t.Fatalf("planSessionRuntimeWithFactory: %v", err)
	}
	session, err := plan.inferencer.ConnectSession(context.Background())
	if err != nil {
		t.Fatalf("connect planned WebRTC provider: %v", err)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("close planned WebRTC provider: %v", err)
	}
	if want := []string{"start runtime", "connect provider", "dial provider-endpoint", "close runtime"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("provider handoff order = %v, want %v", order, want)
	}
}

func TestPlanSessionRuntime_WebSocketDoesNotConstructRTCRuntime(t *testing.T) {
	configDir := t.TempDir()
	writeSessionConfigFile(t, configDir, `
model:
  provider: grok
  grok:
    model: grok-websocket-test
    api_key: test-key
`)
	plan, err := planSessionRuntimeWithFactory(SessionRunOptions{
		SessionInferencer: &selectionTestInferencer{},
		Transport:         SessionTransportWebSocket,
		Provider:          config.ProviderGrok,
		ConfigDir:         configDir,
	}, sessionRuntimeFactory{
		newRTCRuntime: func(SessionRuntimeSelection) (SessionRTCRuntime, error) {
			t.Fatal("WebSocket planning constructed an RTC runtime")
			return nil, nil
		},
	})
	if err != nil {
		t.Fatalf("planSessionRuntimeWithFactory: %v", err)
	}
	if plan.rtcRuntime != nil {
		t.Fatal("WebSocket plan retained an RTC runtime")
	}
}

func TestPlanSessionRuntime_ReplayDoesNotConstructLiveRTCRuntime(t *testing.T) {
	plan, err := planSessionRuntimeWithFactory(SessionRunOptions{
		ReplayPath:        "synthetic.session.json",
		SessionInferencer: &selectionTestInferencer{},
		Transport:         SessionTransportWebRTC,
		Signaling:         "loopback://replay/sentinel",
		MediaSource:       "fixture://replay/sentinel",
	}, sessionRuntimeFactory{
		newRTCRuntime: func(SessionRuntimeSelection) (SessionRTCRuntime, error) {
			t.Fatal("replay planning constructed a live RTC runtime")
			return nil, nil
		},
	})
	if err != nil {
		t.Fatalf("planSessionRuntimeWithFactory: %v", err)
	}
	if plan.rtcRuntime != nil {
		t.Fatal("replay plan retained a live RTC runtime")
	}
}

type testRTCSignaling struct {
	close func() error
}

func (s *testRTCSignaling) SendOffer(context.Context, rtc.SessionDescription) error { return nil }
func (s *testRTCSignaling) ReceiveOffer(context.Context) (rtc.SessionDescription, error) {
	return rtc.SessionDescription{}, nil
}
func (s *testRTCSignaling) SendAnswer(context.Context, rtc.SessionDescription) error { return nil }
func (s *testRTCSignaling) ReceiveAnswer(context.Context) (rtc.SessionDescription, error) {
	return rtc.SessionDescription{}, nil
}
func (s *testRTCSignaling) SendCandidate(context.Context, rtc.ICECandidate) error { return nil }
func (s *testRTCSignaling) ReceiveCandidate(context.Context) (rtc.ICECandidate, error) {
	return rtc.ICECandidate{}, nil
}
func (s *testRTCSignaling) CompleteCandidateGathering(context.Context) error { return nil }
func (s *testRTCSignaling) WaitCandidateGathering(context.Context) error     { return nil }
func (s *testRTCSignaling) Done() <-chan struct{}                            { return nil }
func (s *testRTCSignaling) Close() error {
	if s == nil || s.close == nil {
		return nil
	}
	return s.close()
}

type testRTCInboundMedia struct {
	close func() error
}

func (*testRTCInboundMedia) ReadFrame(context.Context) (rtc.PCMFrame, error) {
	return rtc.PCMFrame{}, io.EOF
}
func (m *testRTCInboundMedia) Close() error {
	if m == nil || m.close == nil {
		return nil
	}
	return m.close()
}

type testRTCDataPlane struct {
	dial   func(string, map[string]string) (transport.Conn, error)
	attach func(context.Context, rtc.InboundMedia) error
	close  func() error
}

func (d *testRTCDataPlane) Dial(endpoint string, headers map[string]string) (transport.Conn, error) {
	if d == nil || d.dial == nil {
		return nil, errors.New("test data plane has no dialer")
	}
	return d.dial(endpoint, headers)
}
func (d *testRTCDataPlane) AttachInboundMedia(ctx context.Context, media rtc.InboundMedia) error {
	if d == nil || d.attach == nil {
		return nil
	}
	return d.attach(ctx, media)
}
func (d *testRTCDataPlane) Close() error {
	if d == nil || d.close == nil {
		return nil
	}
	return d.close()
}

type testRTCConn struct{}

func (testRTCConn) ReadMessage() (int, []byte, error) { return 0, nil, io.EOF }
func (testRTCConn) WriteMessage(int, []byte) error    { return nil }
func (testRTCConn) Close() error                      { return nil }

type testForwardingRecordingDialer struct {
	inner transport.Dialer
}

func (d *testForwardingRecordingDialer) Dial(endpoint string, headers map[string]string) (transport.Conn, error) {
	return d.inner.Dial(endpoint, headers)
}

func (*testForwardingRecordingDialer) FlushToFile(string) error { return nil }

type testSessionRTCRuntime struct {
	dataPlane  SessionRTCDataPlane
	start      func()
	close      func()
	startErr   error
	closeErr   error
	startCount int
	closeCount int
	mu         sync.Mutex
}

func (r *testSessionRTCRuntime) Start(context.Context) (SessionRTCDataPlane, error) {
	r.mu.Lock()
	r.startCount++
	start := r.start
	firstStart := r.startCount == 1
	dataPlane := r.dataPlane
	startErr := r.startErr
	r.mu.Unlock()
	if firstStart && start != nil {
		start()
	}
	if startErr != nil {
		return nil, startErr
	}
	if dataPlane == nil {
		dataPlane = &testRTCDataPlane{dial: func(string, map[string]string) (transport.Conn, error) { return testRTCConn{}, nil }}
	}
	return dataPlane, nil
}

func (r *testSessionRTCRuntime) Close() error {
	r.mu.Lock()
	r.closeCount++
	closeFn := r.close
	closeErr := r.closeErr
	r.mu.Unlock()
	if closeFn != nil {
		closeFn()
	}
	return closeErr
}

type testSessionInferencer struct {
	connect func() (messages.Session, error)
}

func (i *testSessionInferencer) ConnectSession(context.Context) (messages.Session, error) {
	if i.connect == nil {
		return newScriptedSession(), nil
	}
	return i.connect()
}
