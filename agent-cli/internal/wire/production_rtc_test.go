package wire

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport/rtc"
)

// TestGeneratedRootCLI_WebRTCTraversesProductionRTCComposition proves the
// shipped graph, not only the service seam. The source fixture is a local
// go2rtc-compatible peer, while the provider is the only deterministic
// session edge. The production runtime must negotiate its own Pion data plane,
// attach the source through the production media opener, deliver non-zero RTP,
// and complete a real data-channel echo before the provider can emit output.
func TestGeneratedRootCLI_WebRTCTraversesProductionRTCComposition(t *testing.T) {
	source := startProductionCLIMediaFixture(t)
	const signalingEndpoint = "loopback://production-root-cli/session"

	production := newProductionRTCComposition()
	base := production.components()
	evidence := &productionRTCCompositionEvidence{}
	runtimeObserver := &productionRootCLIRuntimeObserver{}
	var dataPlaneMu sync.Mutex
	var dataPlane *productionRTCDataPlane

	components := services.SessionRTCComponents{
		ResolveSignaling: func(ctx context.Context, endpoint string) (rtc.Signaling, error) {
			evidence.record("resolve signaling", endpoint, "")
			return base.ResolveSignaling(ctx, endpoint)
		},
		NewDataPlane: func(ctx context.Context, signaling rtc.Signaling) (services.SessionRTCDataPlane, error) {
			evidence.record("create RTC data plane", "", "")
			plane, err := base.NewDataPlane(ctx, signaling)
			if err == nil {
				concrete, ok := plane.(*productionRTCDataPlane)
				if !ok {
					return nil, fmt.Errorf("production data-plane type = %T, want *productionRTCDataPlane", plane)
				}
				dataPlaneMu.Lock()
				dataPlane = concrete
				dataPlaneMu.Unlock()
			}
			return plane, err
		},
		OpenMediaSource: func(ctx context.Context, raw string) (rtc.InboundMedia, error) {
			evidence.record("open media source", "", raw)
			return base.OpenMediaSource(ctx, raw)
		},
	}

	provider := &productionRootCLIInferencer{
		dataPlane: func() *productionRTCDataPlane {
			dataPlaneMu.Lock()
			defer dataPlaneMu.Unlock()
			return dataPlane
		},
		record: func(event string) { evidence.record(event, "", "") },
	}
	transportDialer := &recordingDialer{}
	app, err := ComposeAgentCLI(
		&recordingToolExecutor{},
		transportDialer,
		&recordingDeviceRegistry{},
		&recordingAudioSource{},
		&recordingAudioSink{},
		&recordingClock{now: time.Unix(123, 0)},
		WithSessionInferencer(provider),
		WithSessionRTCComponents(components),
		WithSessionRuntimeObserver(runtimeObserver),
	)
	if err != nil {
		t.Fatalf("compose generated root CLI: %v", err)
	}

	var stdout, stderr bytes.Buffer
	root := app.Generate()
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{
		"--config-dir", t.TempDir(),
		"session",
		"--record", "capture.json",
		"--provider", "grok",
		"--model", "grok-production-root-cli",
		"--api-key", "test-provider-key",
		"--transport", "webrtc",
		"--signaling", signalingEndpoint,
		"--media-source", source.rawURL,
		"complete the production RTC turn",
	})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := root.ExecuteContext(ctx); err != nil {
		t.Fatalf("generated root WebRTC session: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}

	source.waitForFrames(t)
	sourceSnapshot := source.snapshot()
	if sourceSnapshot.err != "" {
		t.Fatalf("media fixture failed: %s", sourceSnapshot.err)
	}
	if sourceSnapshot.path != "/api/ws" || sourceSnapshot.source != "production-cli-fixture" {
		t.Fatalf("media source request = path %q source %q, want /api/ws and production-cli-fixture", sourceSnapshot.path, sourceSnapshot.source)
	}
	if sourceSnapshot.frames == 0 {
		t.Fatal("media fixture sent zero non-zero PCM source frames")
	}

	evidenceSnapshot := evidence.snapshot()
	if evidenceSnapshot.signaling != signalingEndpoint {
		t.Fatalf("resolved signaling endpoint = %q, want exact %q", evidenceSnapshot.signaling, signalingEndpoint)
	}
	if evidenceSnapshot.mediaSource != source.rawURL {
		t.Fatalf("opened media source = %q, want exact %q", evidenceSnapshot.mediaSource, source.rawURL)
	}
	wantPrefix := []string{"resolve signaling", "create RTC data plane", "open media source", "provider connected", "provider data echo"}
	if !hasOrderedEvents(evidenceSnapshot.events, wantPrefix) {
		t.Fatalf("RTC/provider lifecycle events = %v, want ordered subsequence %v", evidenceSnapshot.events, wantPrefix)
	}

	dataPlaneMu.Lock()
	concreteDataPlane := dataPlane
	dataPlaneMu.Unlock()
	if concreteDataPlane == nil {
		t.Fatal("generated graph did not construct the concrete production RTC data plane")
	}
	concreteDataPlane.attachMu.Lock()
	attached := concreteDataPlane.attached
	concreteDataPlane.attachMu.Unlock()
	if !attached {
		t.Fatal("production RTC data plane did not attach the opened media source")
	}
	if concreteDataPlane.mediaFrames.Load() == 0 || concreteDataPlane.mediaPackets.Load() == 0 {
		t.Fatalf("production RTC media activity = outbound frames %d, received packets %d; want both non-zero", concreteDataPlane.mediaFrames.Load(), concreteDataPlane.mediaPackets.Load())
	}
	if concreteDataPlane.data == nil {
		t.Fatal("production RTC data plane did not create its provider data channel")
	}
	select {
	case <-concreteDataPlane.clientDataOpen:
	default:
		t.Fatal("production RTC client data channel did not open")
	}
	select {
	case <-concreteDataPlane.serverDataOpen:
	default:
		t.Fatal("production RTC server data channel did not open")
	}
	if transportDials := transportDialer.dials.Load(); transportDials != 0 {
		t.Fatalf("WebRTC session used the composed WebSocket transport dialer %d times", transportDials)
	}

	if got := stdout.String(); !strings.Contains(got, productionRootCLITranscript) {
		t.Fatalf("root CLI output does not contain the completed RTC transcript %q:\n%s", productionRootCLITranscript, got)
	}
	if strings.Contains(strings.ToLower(stdout.String()), "usage:") || strings.Contains(stdout.String(), "Run or manage agent sessions") {
		t.Fatalf("successful RTC session emitted help instead of a turn:\n%s", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("successful RTC session stderr = %q", stderr.String())
	}
	observations := runtimeObserver.snapshot()
	var turnCompleted, terminal *services.SessionRuntimeObservation
	for index := range observations {
		observation := observations[index]
		switch observation.Kind {
		case services.SessionRuntimeObservationTurnCompleted:
			turnCompleted = &observation
		case services.SessionRuntimeObservationTerminal:
			terminal = &observation
		}
	}
	if turnCompleted == nil || turnCompleted.TurnsCompleted != 1 {
		t.Fatalf("runtime observations = %#v, want one completed observable turn", observations)
	}
	if terminal == nil || !terminal.Clean || terminal.TurnsCompleted != 1 || terminal.FinalAccounting == nil {
		t.Fatalf("runtime terminal observation = %#v, want clean terminal accounting after one turn", terminal)
	}
}

const productionRootCLITranscript = "production RTC data turn complete"

type productionRTCCompositionEvidence struct {
	mu          sync.Mutex
	events      []string
	signaling   string
	mediaSource string
}

type productionRootCLIRuntimeObserver struct {
	mu           sync.Mutex
	observations []services.SessionRuntimeObservation
}

func (o *productionRootCLIRuntimeObserver) ObserveSessionRuntime(observation services.SessionRuntimeObservation) {
	if o == nil {
		return
	}
	o.mu.Lock()
	o.observations = append(o.observations, observation)
	o.mu.Unlock()
}

func (o *productionRootCLIRuntimeObserver) snapshot() []services.SessionRuntimeObservation {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]services.SessionRuntimeObservation(nil), o.observations...)
}

func (e *productionRTCCompositionEvidence) record(event, signaling, mediaSource string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, event)
	if signaling != "" {
		e.signaling = signaling
	}
	if mediaSource != "" {
		e.mediaSource = mediaSource
	}
}

func (e *productionRTCCompositionEvidence) snapshot() productionRTCCompositionEvidence {
	e.mu.Lock()
	defer e.mu.Unlock()
	return productionRTCCompositionEvidence{
		events:      append([]string(nil), e.events...),
		signaling:   e.signaling,
		mediaSource: e.mediaSource,
	}
}

func hasOrderedEvents(events, want []string) bool {
	position := 0
	for _, event := range events {
		if position < len(want) && event == want[position] {
			position++
		}
	}
	return position == len(want)
}

type productionRootCLIInferencer struct {
	dataPlane func() *productionRTCDataPlane
	record    func(string)
}

func (i *productionRootCLIInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	if i == nil || i.dataPlane == nil {
		return nil, errors.New("production RTC provider has no data-plane observation")
	}
	if i.record != nil {
		i.record("provider connected")
	}
	var plane *productionRTCDataPlane
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(2 * time.Millisecond)
	defer ticker.Stop()
	for {
		plane = i.dataPlane()
		if plane != nil && plane.mediaFrames.Load() > 0 && plane.mediaPackets.Load() > 0 {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			return nil, errors.New("production RTC media did not reach the peer data plane")
		case <-ticker.C:
		}
	}

	conn, err := plane.Dial("provider-over-rtc", map[string]string{"x-runtime": "production"})
	if err != nil {
		return nil, fmt.Errorf("dial provider over production RTC data plane: %w", err)
	}
	if err := conn.WriteMessage(1, []byte("production-rtc-provider-probe")); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("write provider RTC probe: %w", err)
	}
	messageType, payload, err := conn.ReadMessage()
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("read provider RTC echo: %w", err)
	}
	if messageType != 1 || string(payload) != "production-rtc-provider-probe" {
		_ = conn.Close()
		return nil, fmt.Errorf("provider RTC echo = (%d, %q), want text probe", messageType, payload)
	}
	if i.record != nil {
		i.record("provider data echo")
	}
	return newProductionRootCLISession(conn), nil
}

type productionRootCLISession struct {
	receive   *messages.TypedBuffer[messages.StreamMessage]
	done      chan struct{}
	conn      transport.Conn
	sendOnce  sync.Once
	closeOnce sync.Once
}

func newProductionRootCLISession(conn transport.Conn) *productionRootCLISession {
	session := &productionRootCLISession{
		receive: messages.NewTypedBuffer[messages.StreamMessage](16),
		done:    make(chan struct{}),
		conn:    conn,
	}
	session.receive.Write(context.Background(), messages.StreamMessage{
		Type:  messages.StreamTypeSessionOpen,
		Value: messages.NewSessionOpenValue("production-root-cli-session", "webrtc"),
	})
	return session
}

func (s *productionRootCLISession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	select {
	case <-s.done:
		return false
	default:
	}
	if msg.Type == messages.StreamTypeSessionClose {
		s.receive.Write(context.Background(), messages.StreamMessage{
			Type:  messages.StreamTypeSessionClose,
			Value: messages.NewSessionCloseValue("production-root-cli-session", "completed"),
		})
		_ = s.Close()
		return true
	}
	s.sendOnce.Do(func() {
		s.receive.Write(context.Background(), messages.StreamMessage{
			Type:  messages.StreamTypeTranscriptStart,
			Role:  messages.RoleAssistant,
			Value: messages.NewTranscriptStartValue(),
		})
		s.receive.Write(context.Background(), messages.StreamMessage{
			Type:  messages.StreamTypeTranscriptDelta,
			Role:  messages.RoleAssistant,
			Value: messages.NewTranscriptDeltaValue(productionRootCLITranscript),
		})
		s.receive.Write(context.Background(), messages.StreamMessage{
			Type:  messages.StreamTypeTranscriptEnd,
			Role:  messages.RoleAssistant,
			Value: messages.NewTranscriptEndValue(productionRootCLITranscript),
		})
		s.receive.Write(context.Background(), messages.StreamMessage{
			Type:  messages.StreamTypeMessageEnd,
			Role:  messages.RoleAssistant,
			Value: messages.NewMessageEndValue(messages.TokenUsage{}),
		})
	})
	return true
}

func (s *productionRootCLISession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.receive
}

func (s *productionRootCLISession) Done() <-chan struct{} { return s.done }

func (s *productionRootCLISession) Close() error {
	s.closeOnce.Do(func() {
		close(s.done)
		if s.conn != nil {
			_ = s.conn.Close()
		}
	})
	return nil
}

type productionCLIMediaFixture struct {
	server      *httptest.Server
	rawURL      string
	fixtureCtx  context.Context
	cancel      context.CancelFunc
	handlerDone chan struct{}
	handlerOnce sync.Once
	framesSent  chan struct{}
	framesOnce  sync.Once

	mu          sync.Mutex
	path        string
	source      string
	frames      int
	err         error
	cleanupOnce sync.Once
}

type productionCLIMediaFixtureSnapshot struct {
	path, source string
	frames       int
	err          string
}

func startProductionCLIMediaFixture(t *testing.T) *productionCLIMediaFixture {
	t.Helper()
	fixtureCtx, cancel := context.WithCancel(context.Background())
	fixture := &productionCLIMediaFixture{
		fixtureCtx:  fixtureCtx,
		cancel:      cancel,
		handlerDone: make(chan struct{}),
		framesSent:  make(chan struct{}),
	}
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	fixture.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer fixture.handlerOnce.Do(func() { close(fixture.handlerDone) })
		fixture.mu.Lock()
		fixture.path = r.URL.Path
		fixture.source = r.URL.Query().Get("src")
		fixture.mu.Unlock()
		handlerCtx, cancelHandler := context.WithCancel(r.Context())
		defer cancelHandler()
		go func() {
			select {
			case <-fixture.fixtureCtx.Done():
				cancelHandler()
			case <-handlerCtx.Done():
			}
		}()
		if r.URL.Path != "/api/ws" {
			w.WriteHeader(http.StatusNotFound)
			fixture.recordError(fmt.Errorf("media fixture path %q, want /api/ws", r.URL.Path))
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			fixture.recordError(fmt.Errorf("upgrade media fixture: %w", err))
			return
		}
		defer conn.Close()
		_, data, err := conn.ReadMessage()
		if err != nil {
			fixture.recordError(fmt.Errorf("read media offer: %w", err))
			return
		}
		var offer struct {
			Type  string `json:"type"`
			Value string `json:"value"`
		}
		if err := json.Unmarshal(data, &offer); err != nil {
			fixture.recordError(fmt.Errorf("decode media offer: %w", err))
			return
		}
		if offer.Type != "webrtc/offer" || strings.TrimSpace(offer.Value) == "" {
			fixture.recordError(fmt.Errorf("media offer type = %q", offer.Type))
			return
		}

		mediaEngine := &webrtc.MediaEngine{}
		if err := mediaEngine.RegisterCodec(webrtc.RTPCodecParameters{
			RTPCodecCapability: webrtc.RTPCodecCapability{
				MimeType:  webrtc.MimeTypePCMU,
				ClockRate: 8000,
				Channels:  1,
			},
			PayloadType: 0,
		}, webrtc.RTPCodecTypeAudio); err != nil {
			fixture.recordError(fmt.Errorf("register fixture PCMU: %w", err))
			return
		}
		peer, err := webrtc.NewAPI(webrtc.WithMediaEngine(mediaEngine)).NewPeerConnection(webrtc.Configuration{})
		if err != nil {
			fixture.recordError(fmt.Errorf("create media fixture peer: %w", err))
			return
		}
		defer peer.Close()
		connected := make(chan struct{})
		var connectedOnce sync.Once
		peer.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
			switch state {
			case webrtc.PeerConnectionStateConnected:
				connectedOnce.Do(func() { close(connected) })
			case webrtc.PeerConnectionStateFailed:
				fixture.recordError(fmt.Errorf("media fixture peer reached %s", state))
				cancelHandler()
			}
		})
		audio, err := webrtc.NewTrackLocalStaticRTP(
			webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypePCMU, ClockRate: 8000, Channels: 1},
			"audio", "production-root-cli-fixture")
		if err != nil {
			fixture.recordError(fmt.Errorf("create media fixture track: %w", err))
			return
		}
		if _, err := peer.AddTrack(audio); err != nil {
			fixture.recordError(fmt.Errorf("attach media fixture track: %w", err))
			return
		}
		if err := peer.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offer.Value}); err != nil {
			fixture.recordError(fmt.Errorf("set media fixture offer: %w", err))
			return
		}
		answer, err := peer.CreateAnswer(nil)
		if err != nil {
			fixture.recordError(fmt.Errorf("create media fixture answer: %w", err))
			return
		}
		if err := peer.SetLocalDescription(answer); err != nil {
			fixture.recordError(fmt.Errorf("set media fixture answer: %w", err))
			return
		}
		gatherCtx, cancelGather := context.WithTimeout(handlerCtx, 2*time.Second)
		select {
		case <-webrtc.GatheringCompletePromise(peer):
		case <-gatherCtx.Done():
			cancelGather()
			fixture.recordError(fmt.Errorf("gather media fixture answer: %w", gatherCtx.Err()))
			return
		}
		cancelGather()
		local := peer.LocalDescription()
		if local == nil {
			fixture.recordError(errors.New("media fixture peer has no local answer"))
			return
		}
		if err := conn.WriteJSON(struct {
			Type  string `json:"type"`
			Value string `json:"value"`
		}{Type: "webrtc/answer", Value: local.SDP}); err != nil {
			fixture.recordError(fmt.Errorf("write media fixture answer: %w", err))
			return
		}
		select {
		case <-connected:
		case <-handlerCtx.Done():
			return
		}
		for index := 0; index < 6; index++ {
			payload := bytes.Repeat([]byte{0x00}, 160)
			if err := audio.WriteRTP(&rtp.Packet{
				Header: rtp.Header{
					Version:        2,
					PayloadType:    0,
					SequenceNumber: uint16(index + 1),
					Timestamp:      uint32(index * 160),
				},
				Payload: payload,
			}); err != nil {
				fixture.recordError(fmt.Errorf("write media fixture frame %d: %w", index+1, err))
				return
			}
			fixture.mu.Lock()
			fixture.frames++
			fixture.mu.Unlock()
		}
		fixture.framesOnce.Do(func() { close(fixture.framesSent) })
		select {
		case <-fixture.fixtureCtx.Done():
		case <-handlerCtx.Done():
		}
	}))
	parsed, err := url.Parse(fixture.server.URL)
	if err != nil {
		t.Fatalf("parse media fixture URL: %v", err)
	}
	fixture.rawURL = "go2rtc://" + parsed.Host + "/api/ws?src=production-cli-fixture"
	t.Cleanup(fixture.close)
	return fixture
}

func (f *productionCLIMediaFixture) recordError(err error) {
	if err == nil {
		return
	}
	f.mu.Lock()
	if f.err == nil {
		f.err = err
	}
	f.mu.Unlock()
}

func (f *productionCLIMediaFixture) waitForFrames(t *testing.T) {
	t.Helper()
	select {
	case <-f.framesSent:
	case <-time.After(7 * time.Second):
		snapshot := f.snapshot()
		t.Fatalf("timed out waiting for source media frames: %+v", snapshot)
	}
}

func (f *productionCLIMediaFixture) snapshot() productionCLIMediaFixtureSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return productionCLIMediaFixtureSnapshot{
		path:   f.path,
		source: f.source,
		frames: f.frames,
		err:    errorString(f.err),
	}
}

func (f *productionCLIMediaFixture) close() {
	f.cleanupOnce.Do(func() {
		f.cancel()
		f.server.CloseClientConnections()
		f.server.Close()
		select {
		case <-f.handlerDone:
		case <-time.After(time.Second):
		}
	})
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

var _ messages.SessionInferencer = (*productionRootCLIInferencer)(nil)
var _ messages.Session = (*productionRootCLISession)(nil)
