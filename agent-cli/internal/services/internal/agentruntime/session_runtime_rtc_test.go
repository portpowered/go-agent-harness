package agentruntime

import sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/observability"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport/rtc"
)

func TestSessionRTCRuntimeObservabilityCoversSuccessFailureAndClose(t *testing.T) {
	var samples []observability.MetricSample
	var logs []observability.LogRecord
	sampler := observability.MetricSamplerFunc(func(_ context.Context, sample observability.MetricSample) error {
		samples = append(samples, sample)
		return errors.New("ignored metric error")
	})
	logger := observability.LoggerFunc(func(_ context.Context, record observability.LogRecord) error {
		logs = append(logs, record)
		return errors.New("ignored log error")
	})
	components := SessionRTCComponents{
		ResolveSignaling: func(context.Context, string) (rtc.Signaling, error) { return &testRTCSignaling{}, nil },
		NewDataPlane:     func(context.Context, rtc.Signaling) (SessionRTCDataPlane, error) { return &testRTCDataPlane{}, nil },
		OpenMediaSource:  func(context.Context, string) (sharedaudio.InboundMedia, error) { return &testRTCInboundMedia{}, nil },
	}
	factory := NewSessionRTCRuntimeFactoryWithObservability(components, sampler, logger)
	runtime, err := factory(SessionRuntimeSelection{Transport: SessionTransportWebRTC})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}

	failing := components
	failing.ResolveSignaling = func(context.Context, string) (rtc.Signaling, error) { return nil, errors.New("offline") }
	runtime, err = NewSessionRTCRuntimeFactoryWithObservability(failing, sampler, logger)(SessionRuntimeSelection{Transport: SessionTransportWebRTC})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Start(context.Background()); err == nil {
		t.Fatal("failing runtime start returned nil error")
	}

	var events []string
	for _, sample := range samples {
		if sample.Name == "session.rtc.lifecycle" {
			events = append(events, sample.Fields["event"])
		}
	}
	if !reflect.DeepEqual(events, []string{"started", "closed", "start_failed"}) {
		t.Fatalf("lifecycle events = %v", events)
	}
	if len(logs) != 3 || logs[2].Level != "error" || logs[2].Fields["phase"] != "resolve signaling" {
		t.Fatalf("lifecycle logs = %+v", logs)
	}
}

func TestSessionRTCRuntime_ComposesSelectedDependenciesAndClosesInReverseOrder(t *testing.T) {
	const (
		signalingEndpoint = "loopback://signaling/sentinel"
		mediaSource       = "fixture://media/sentinel"
	)

	var (
		events          []string
		gotSignalingURL string
		gotMediaSource  string
		gotAttached     sharedaudio.InboundMedia
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
		attach: func(_ context.Context, inbound sharedaudio.InboundMedia) error {
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
		OpenMediaSource: func(_ context.Context, source string) (sharedaudio.InboundMedia, error) {
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
		OpenMediaSource: func(context.Context, string) (sharedaudio.InboundMedia, error) {
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

func TestSessionRTCRuntimeSessionForwardsProviderCapabilities(t *testing.T) {
	wantSendErr := errors.New("provider send rejected")
	media := RTCMediaEndpoints{
		Inbound:  &testRTCInboundMedia{},
		Outbound: &testRTCOutboundMedia{},
	}
	provider := &runtimeCapabilitySession{
		scriptedSession: newScriptedSession(),
		media:           media,
		sendOutcome:     messages.SessionSendOutcome{Status: messages.SessionSendTerminalFailure, Err: wantSendErr},
		inputDrops:      3,
		outputDrops:     5,
	}
	wrapper := &sessionRTCRuntimeSession{Session: provider}

	gotMedia, ok := rtcMediaFromSession(wrapper)
	if !ok {
		t.Fatal("runtime session did not preserve provider RTC media capability")
	}
	if gotMedia.Inbound != media.Inbound || gotMedia.Outbound != media.Outbound {
		t.Fatalf("forwarded RTC media = %#v, want %#v", gotMedia, media)
	}

	sender, ok := any(wrapper).(messages.SessionSendOutcomeSender)
	if !ok {
		t.Fatal("runtime session did not preserve SessionSendOutcomeSender")
	}
	outcome := sender.SendWithOutcome(context.Background(), messages.StreamMessage{Type: messages.StreamTypeTextDelta})
	if outcome.Status != messages.SessionSendTerminalFailure || !errors.Is(outcome.Err, wantSendErr) {
		t.Fatalf("forwarded send outcome = %#v, want terminal failure with provider cause", outcome)
	}

	counters, ok := any(wrapper).(messages.SessionDropCounters)
	if !ok {
		t.Fatal("runtime session did not preserve SessionDropCounters")
	}
	if counters.InputDrops() != provider.inputDrops || counters.OutputDrops() != provider.outputDrops {
		t.Fatalf("forwarded drop counters = (%d, %d), want (%d, %d)", counters.InputDrops(), counters.OutputDrops(), provider.inputDrops, provider.outputDrops)
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

func TestRunSession_WebRTCCompletesHermeticTurnThroughExportedService(t *testing.T) {
	const (
		signalingEndpoint = "loopback://hermetic/session-endpoint"
		mediaSource       = "fixture://hermetic/media-source"
	)

	fixture := newHermeticSessionRTCFixture()
	var observationsMu sync.Mutex
	var observations []messages.StreamMessage
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var gotSelection SessionRuntimeSelection
	runtimeFactory := func(selection SessionRuntimeSelection) (SessionRTCRuntime, error) {
		gotSelection = selection
		return NewSessionRTCRuntimeFactory(SessionRTCComponents{
			ResolveSignaling: fixture.resolveSignaling,
			NewDataPlane:     fixture.newDataPlane,
			OpenMediaSource:  fixture.openMediaSource,
		})(selection)
	}

	var out bytes.Buffer
	err := RunSession(ctx, &out, SessionRunOptions{
		RecordPath:        filepath.Join(t.TempDir(), "hermetic.session.json"),
		Transport:         SessionTransportWebRTC,
		Signaling:         signalingEndpoint,
		MediaSource:       mediaSource,
		SessionInferencer: &hermeticSessionInferencer{fixture: fixture},
		RTCRuntimeFactory: runtimeFactory,
		Prompt:            "complete one hermetic turn",
		StreamObserver: func(msg messages.StreamMessage) {
			observationsMu.Lock()
			observations = append(observations, msg)
			observationsMu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("RunSession WebRTC fixture: %v", err)
	}

	if gotSelection != (SessionRuntimeSelection{
		Transport:         SessionTransportWebRTC,
		SignalingEndpoint: signalingEndpoint,
		MediaSource:       mediaSource,
	}) {
		t.Fatalf("runtime factory selection = %#v, want exact fixture values", gotSelection)
	}
	if fixture.signalingEndpoint != signalingEndpoint || fixture.mediaSource != mediaSource {
		t.Fatalf("runtime boundary values = (%q, %q), want (%q, %q)", fixture.signalingEndpoint, fixture.mediaSource, signalingEndpoint, mediaSource)
	}
	if fixture.mediaFrames != len(fixture.sourceFrames) {
		t.Fatalf("consumed media frames = %d, want %d", fixture.mediaFrames, len(fixture.sourceFrames))
	}
	if !fixture.dataDialed {
		t.Fatal("hermetic provider did not use the RTC data plane")
	}
	if !strings.Contains(out.String(), "hermetic RTC turn") {
		t.Fatalf("session output does not contain the completed RTC turn:\n%s", out.String())
	}
	observationsMu.Lock()
	observed := append([]messages.StreamMessage(nil), observations...)
	observationsMu.Unlock()
	if !containsStreamType(observed, messages.StreamTypeMessageEnd) {
		t.Fatalf("stream observer did not observe a completed turn: %v", observed)
	}

	if fixture.mediaCloseCount != 1 || fixture.dataCloseCount != 1 || fixture.signalingCloseCount != 1 || fixture.answererCloseCount != 1 {
		t.Fatalf("RTC resource closes = media:%d data:%d signaling:%d answerer:%d, want one each", fixture.mediaCloseCount, fixture.dataCloseCount, fixture.signalingCloseCount, fixture.answererCloseCount)
	}
	if fixture.connCloseCount != 1 {
		t.Fatalf("RTC provider data connection closes = %d, want one", fixture.connCloseCount)
	}
}

func TestSessionRTCRuntime_PreservesTypedFailuresAndRedactsMediaCredentials(t *testing.T) {
	t.Run("signaling", func(t *testing.T) {
		wantErr := rtc.ErrSignalingUnreachable
		signaling := &testRTCSignaling{}
		runtimeFactory := NewSessionRTCRuntimeFactory(SessionRTCComponents{
			ResolveSignaling: func(context.Context, string) (rtc.Signaling, error) {
				return signaling, wantErr
			},
			NewDataPlane: func(context.Context, rtc.Signaling) (SessionRTCDataPlane, error) {
				t.Fatal("peer/data factory ran after signaling failed")
				return nil, nil
			},
			OpenMediaSource: func(context.Context, string) (sharedaudio.InboundMedia, error) {
				t.Fatal("media source opened after signaling failed")
				return nil, nil
			},
		})
		runtime, err := runtimeFactory(SessionRuntimeSelection{Transport: SessionTransportWebRTC, SignalingEndpoint: "loopback://failure", MediaSource: "fixture://failure"})
		if err != nil {
			t.Fatalf("construct runtime: %v", err)
		}
		_, err = runtime.Start(context.Background())
		if !errors.Is(err, wantErr) {
			t.Fatalf("signaling error = %v, want errors.Is(..., %v)", err, wantErr)
		}
		var phaseErr *SessionRTCRuntimeError
		if !errors.As(err, &phaseErr) || phaseErr.Phase != "resolve signaling" {
			t.Fatalf("signaling error = %#v, want resolve-signaling phase wrapper", err)
		}
	})

	t.Run("peer", func(t *testing.T) {
		wantErr := &rtc.TerminalError{Cause: errors.New("fixture peer failed"), Attempts: 1}
		signaling := &testRTCSignaling{}
		runtimeFactory := NewSessionRTCRuntimeFactory(SessionRTCComponents{
			ResolveSignaling: func(context.Context, string) (rtc.Signaling, error) { return signaling, nil },
			NewDataPlane: func(context.Context, rtc.Signaling) (SessionRTCDataPlane, error) {
				return nil, wantErr
			},
			OpenMediaSource: func(context.Context, string) (sharedaudio.InboundMedia, error) {
				t.Fatal("media source opened after peer setup failed")
				return nil, nil
			},
		})
		runtime, err := runtimeFactory(SessionRuntimeSelection{Transport: SessionTransportWebRTC, SignalingEndpoint: "loopback://failure", MediaSource: "fixture://failure"})
		if err != nil {
			t.Fatalf("construct runtime: %v", err)
		}
		_, err = runtime.Start(context.Background())
		if !errors.Is(err, wantErr) {
			t.Fatalf("peer error = %v, want errors.Is(..., %v)", err, wantErr)
		}
		var terminalErr *rtc.TerminalError
		if !errors.As(err, &terminalErr) {
			t.Fatalf("peer error type = %T, want *rtc.TerminalError", err)
		}
	})

	t.Run("media source", func(t *testing.T) {
		const secret = "fixture-password-must-not-leak"
		signaling := &testRTCSignaling{}
		dataPlane := &testRTCDataPlane{}
		runtimeFactory := NewSessionRTCRuntimeFactory(SessionRTCComponents{
			ResolveSignaling: func(context.Context, string) (rtc.Signaling, error) { return signaling, nil },
			NewDataPlane:     func(context.Context, rtc.Signaling) (SessionRTCDataPlane, error) { return dataPlane, nil },
			OpenMediaSource: func(ctx context.Context, raw string) (sharedaudio.InboundMedia, error) {
				return rtc.OpenMediaSource(ctx, raw)
			},
		})
		runtime, err := runtimeFactory(SessionRuntimeSelection{
			Transport:         SessionTransportWebRTC,
			SignalingEndpoint: "loopback://failure",
			MediaSource:       "rtsp://camera:" + secret + "@",
		})
		if err != nil {
			t.Fatalf("construct runtime: %v", err)
		}
		_, err = runtime.Start(context.Background())
		if err == nil {
			t.Fatal("media setup unexpectedly succeeded")
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("media setup error leaked source credentials: %v", err)
		}
		var sourceErr *rtc.MediaSourceError
		if !errors.As(err, &sourceErr) {
			t.Fatalf("media setup error type = %T, want *rtc.MediaSourceError", err)
		}
		if !errors.Is(err, rtc.ErrMalformedSource) {
			t.Fatalf("media setup error = %v, want malformed-source identity", err)
		}
	})
}

func containsStreamType(messages []messages.StreamMessage, want messages.StreamMessageType) bool {
	for _, msg := range messages {
		if msg.Type == want {
			return true
		}
	}
	return false
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

func (*testRTCInboundMedia) ReadFrame(context.Context) (sharedaudio.PCMFrame, error) {
	return sharedaudio.PCMFrame{}, io.EOF
}
func (m *testRTCInboundMedia) Close() error {
	if m == nil || m.close == nil {
		return nil
	}
	return m.close()
}

type testRTCOutboundMedia struct{}

func (*testRTCOutboundMedia) WriteFrame(context.Context, sharedaudio.PCMFrame) error { return nil }
func (*testRTCOutboundMedia) Close() error                                           { return nil }

type runtimeCapabilitySession struct {
	*scriptedSession
	media       RTCMediaEndpoints
	sendOutcome messages.SessionSendOutcome
	inputDrops  int64
	outputDrops int64
}

func (s *runtimeCapabilitySession) RTCMedia() sharedaudio.MediaEndpoints { return s.media }

func (s *runtimeCapabilitySession) SendWithOutcome(context.Context, messages.StreamMessage) messages.SessionSendOutcome {
	return s.sendOutcome
}

func (s *runtimeCapabilitySession) InputDrops() int64  { return s.inputDrops }
func (s *runtimeCapabilitySession) OutputDrops() int64 { return s.outputDrops }

type testRTCDataPlane struct {
	dial   func(string, map[string]string) (transport.Conn, error)
	attach func(context.Context, sharedaudio.InboundMedia) error
	close  func() error
}

func (d *testRTCDataPlane) Dial(endpoint string, headers map[string]string) (transport.Conn, error) {
	if d == nil || d.dial == nil {
		return nil, errors.New("test data plane has no dialer")
	}
	return d.dial(endpoint, headers)
}
func (d *testRTCDataPlane) AttachInboundMedia(ctx context.Context, media sharedaudio.InboundMedia) error {
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

type hermeticSessionRTCFixture struct {
	mu sync.Mutex

	answerer            *rtc.LoopbackEndpoint
	dataPlane           *hermeticSessionRTCDataPlane
	mediaSource         string
	signalingEndpoint   string
	sourceFrames        []sharedaudio.PCMFrame
	mediaFrames         int
	dataDialed          bool
	mediaCloseCount     int
	dataCloseCount      int
	signalingCloseCount int
	answererCloseCount  int
	connCloseCount      int
}

func newHermeticSessionRTCFixture() *hermeticSessionRTCFixture {
	return &hermeticSessionRTCFixture{
		sourceFrames: []sharedaudio.PCMFrame{
			{Samples: []int16{100, -100, 200, -200}},
			{Samples: []int16{300, -300, 400, -400}},
		},
	}
}

func (f *hermeticSessionRTCFixture) resolveSignaling(_ context.Context, endpoint string) (rtc.Signaling, error) {
	f.mu.Lock()
	f.signalingEndpoint = endpoint
	f.mu.Unlock()
	offerer, answerer, err := rtc.NewLoopbackSignalingPair(rtc.SignalingConfig{ICEGatheringTimeout: time.Second})
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.answerer = answerer
	f.mu.Unlock()
	return &hermeticSessionRTCSignaling{Signaling: offerer, fixture: f}, nil
}

func (f *hermeticSessionRTCFixture) newDataPlane(ctx context.Context, signaling rtc.Signaling) (SessionRTCDataPlane, error) {
	f.mu.Lock()
	answerer := f.answerer
	f.mu.Unlock()
	if answerer == nil {
		return nil, errors.New("loopback answerer was not resolved")
	}
	if err := completeHermeticSessionRTCExchange(ctx, signaling, answerer); err != nil {
		_ = answerer.Close()
		return nil, err
	}
	dataPlane := &hermeticSessionRTCDataPlane{fixture: f, answerer: answerer}
	f.mu.Lock()
	f.dataPlane = dataPlane
	f.mu.Unlock()
	return dataPlane, nil
}

func (f *hermeticSessionRTCFixture) openMediaSource(_ context.Context, source string) (sharedaudio.InboundMedia, error) {
	f.mu.Lock()
	f.mediaSource = source
	frames := append([]sharedaudio.PCMFrame(nil), f.sourceFrames...)
	f.mu.Unlock()
	return &hermeticSessionRTCMedia{fixture: f, frames: frames}, nil
}

type hermeticSessionRTCSignaling struct {
	rtc.Signaling
	fixture   *hermeticSessionRTCFixture
	closeOnce sync.Once
}

func (s *hermeticSessionRTCSignaling) Close() error {
	s.closeOnce.Do(func() {
		s.fixture.mu.Lock()
		s.fixture.signalingCloseCount++
		s.fixture.mu.Unlock()
	})
	return s.Signaling.Close()
}

type hermeticSessionRTCDataPlane struct {
	fixture   *hermeticSessionRTCFixture
	answerer  *rtc.LoopbackEndpoint
	closeOnce sync.Once
}

func (d *hermeticSessionRTCDataPlane) Dial(string, map[string]string) (transport.Conn, error) {
	d.fixture.mu.Lock()
	d.fixture.dataDialed = true
	d.fixture.mu.Unlock()
	return &hermeticSessionRTCConn{fixture: d.fixture}, nil
}

func (d *hermeticSessionRTCDataPlane) AttachInboundMedia(ctx context.Context, source sharedaudio.InboundMedia) error {
	for {
		frame, err := source.ReadFrame(ctx)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if len(frame.Samples) == 0 {
			return errors.New("hermetic RTC fixture received an empty media frame")
		}
		d.fixture.mu.Lock()
		d.fixture.mediaFrames++
		d.fixture.mu.Unlock()
	}
}

func (d *hermeticSessionRTCDataPlane) Close() error {
	d.closeOnce.Do(func() {
		d.fixture.mu.Lock()
		d.fixture.dataCloseCount++
		d.fixture.answererCloseCount++
		d.fixture.mu.Unlock()
		_ = d.answerer.Close()
	})
	return nil
}

type hermeticSessionRTCMedia struct {
	fixture   *hermeticSessionRTCFixture
	frames    []sharedaudio.PCMFrame
	mu        sync.Mutex
	index     int
	closeOnce sync.Once
}

func (m *hermeticSessionRTCMedia) ReadFrame(ctx context.Context) (sharedaudio.PCMFrame, error) {
	if ctx != nil {
		select {
		case <-ctx.Done():
			return sharedaudio.PCMFrame{}, ctx.Err()
		default:
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.index >= len(m.frames) {
		return sharedaudio.PCMFrame{}, io.EOF
	}
	frame := m.frames[m.index]
	frame.Samples = append([]int16(nil), frame.Samples...)
	m.index++
	return frame, nil
}

func (m *hermeticSessionRTCMedia) Close() error {
	m.closeOnce.Do(func() {
		m.fixture.mu.Lock()
		m.fixture.mediaCloseCount++
		m.fixture.mu.Unlock()
	})
	return nil
}

type hermeticSessionRTCConn struct {
	fixture   *hermeticSessionRTCFixture
	closeOnce sync.Once
}

func (*hermeticSessionRTCConn) ReadMessage() (int, []byte, error) { return 0, nil, io.EOF }
func (*hermeticSessionRTCConn) WriteMessage(int, []byte) error    { return nil }
func (c *hermeticSessionRTCConn) Close() error {
	c.closeOnce.Do(func() {
		c.fixture.mu.Lock()
		c.fixture.connCloseCount++
		c.fixture.mu.Unlock()
	})
	return nil
}

type hermeticSessionInferencer struct {
	fixture *hermeticSessionRTCFixture
}

func (i *hermeticSessionInferencer) ConnectSession(context.Context) (messages.Session, error) {
	i.fixture.mu.Lock()
	dataPlane := i.fixture.dataPlane
	wantFrames := len(i.fixture.sourceFrames)
	gotFrames := i.fixture.mediaFrames
	i.fixture.mu.Unlock()
	if dataPlane == nil || gotFrames != wantFrames {
		return nil, errors.New("hermetic provider connected before RTC media was attached")
	}
	conn, err := dataPlane.Dial("hermetic-provider-data", nil)
	if err != nil {
		return nil, err
	}
	session := newHermeticSessionRTCProviderSession()
	session.conn = conn
	return session, nil
}

type hermeticSessionRTCProviderSession struct {
	receive   *messages.TypedBuffer[messages.StreamMessage]
	done      chan struct{}
	conn      transport.Conn
	sendOnce  sync.Once
	closeOnce sync.Once
}

func newHermeticSessionRTCProviderSession() *hermeticSessionRTCProviderSession {
	s := &hermeticSessionRTCProviderSession{
		receive: messages.NewTypedBuffer[messages.StreamMessage](32),
		done:    make(chan struct{}),
	}
	s.receive.Write(context.Background(), messages.StreamMessage{
		Type:  messages.StreamTypeSessionOpen,
		Value: messages.NewSessionOpenValue("hermetic-rtc-session", "webrtc"),
	})
	return s
}

func (s *hermeticSessionRTCProviderSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	select {
	case <-s.done:
		return false
	default:
	}
	if msg.Type == messages.StreamTypeSessionClose {
		s.receive.Write(context.Background(), messages.StreamMessage{
			Type:  messages.StreamTypeSessionClose,
			Value: messages.NewSessionCloseValue("hermetic-rtc-session", "completed"),
		})
		_ = s.Close()
		return true
	}
	s.sendOnce.Do(func() {
		s.receive.Write(context.Background(), messages.StreamMessage{Type: messages.StreamTypeTranscriptStart, Role: messages.RoleAssistant, Value: messages.NewTranscriptStartValue()})
		s.receive.Write(context.Background(), messages.StreamMessage{Type: messages.StreamTypeTranscriptDelta, Role: messages.RoleAssistant, Value: messages.NewTranscriptDeltaValue("hermetic RTC turn")})
		s.receive.Write(context.Background(), messages.StreamMessage{Type: messages.StreamTypeTranscriptEnd, Role: messages.RoleAssistant, Value: messages.NewTranscriptEndValue("hermetic RTC turn")})
		s.receive.Write(context.Background(), messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})})
	})
	return true
}

func (s *hermeticSessionRTCProviderSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.receive
}
func (s *hermeticSessionRTCProviderSession) Done() <-chan struct{} { return s.done }
func (s *hermeticSessionRTCProviderSession) Close() error {
	s.closeOnce.Do(func() {
		close(s.done)
		if s.conn != nil {
			_ = s.conn.Close()
		}
	})
	return nil
}

func completeHermeticSessionRTCExchange(ctx context.Context, offerer rtc.Signaling, answerer *rtc.LoopbackEndpoint) error {
	offer := rtc.SessionDescription{Type: "offer", SDP: "v=0\r\no=- hermetic-offer 1 IN IP4 127.0.0.1\r\ns=hermetic\r\nt=0 0"}
	answer := rtc.SessionDescription{Type: "answer", SDP: "v=0\r\no=- hermetic-answer 1 IN IP4 127.0.0.1\r\ns=hermetic\r\nt=0 0"}
	if err := offerer.SendOffer(ctx, offer); err != nil {
		return err
	}
	if err := offerer.SendCandidate(ctx, rtc.ICECandidate{Candidate: "hermetic-offer-candidate"}); err != nil {
		return err
	}
	if err := offerer.CompleteCandidateGathering(ctx); err != nil {
		return err
	}
	if _, err := answerer.ReceiveOffer(ctx); err != nil {
		return err
	}
	if _, err := answerer.ReceiveCandidate(ctx); err != nil {
		return err
	}
	if _, err := answerer.ReceiveCandidate(ctx); !errors.Is(err, rtc.ErrGatheringComplete) {
		return err
	}
	if err := answerer.SendAnswer(ctx, answer); err != nil {
		return err
	}
	if err := answerer.SendCandidate(ctx, rtc.ICECandidate{Candidate: "hermetic-answer-candidate"}); err != nil {
		return err
	}
	if err := answerer.CompleteCandidateGathering(ctx); err != nil {
		return err
	}
	if _, err := offerer.ReceiveAnswer(ctx); err != nil {
		return err
	}
	if _, err := offerer.ReceiveCandidate(ctx); err != nil {
		return err
	}
	if _, err := offerer.ReceiveCandidate(ctx); !errors.Is(err, rtc.ErrGatheringComplete) {
		return err
	}
	if err := offerer.WaitCandidateGathering(ctx); err != nil {
		return err
	}
	return answerer.WaitCandidateGathering(ctx)
}
