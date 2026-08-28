package wire

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
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
	root.SilenceUsage = true
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
	if sourceSnapshot.frames == 0 || sourceSnapshot.nonZeroFrames == 0 {
		t.Fatalf("media fixture non-zero frames = %d of %d, want at least one", sourceSnapshot.nonZeroFrames, sourceSnapshot.frames)
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

// TestGeneratedRootCLI_WebRTCDeviceBindingPreservesProviderMedia proves that
// the generated/root path forwards the provider-owned RTC media capability
// through the production runtime wrapper. The concrete production signaling,
// Pion data plane, external media opener, and attachment still run; only the
// provider session and virtual registry are deterministic test edges.
func TestGeneratedRootCLI_WebRTCDeviceBindingPreservesProviderMedia(t *testing.T) {
	source := startProductionCLIMediaFixture(t)
	const signalingEndpoint = "loopback://production-root-cli/device-binding"
	registry, inputDevice, inputFeedDevice, outputDevice, outputObserverDevice := newProductionRootCLIDeviceRegistry(t)
	feed, err := audio.NewDeviceSink(registry, inputFeedDevice)
	if err != nil {
		t.Fatalf("open root CLI input feeder: %v", err)
	}
	observe, err := audio.NewDeviceSource(registry, outputObserverDevice)
	if err != nil {
		_ = feed.Close()
		t.Fatalf("open root CLI output observer: %v", err)
	}
	deviceMedia := newProductionRootCLIDeviceMedia(productionRootCLIDeviceFrame())
	inputFrame := productionRootCLIDeviceFrame()
	if err := feed.WriteFrame(context.Background(), inputFrame.Samples); err != nil {
		_ = feed.Close()
		_ = observe.Close()
		t.Fatalf("seed root CLI input device: %v", err)
	}
	t.Cleanup(func() {
		_ = deviceMedia.Close()
		_ = feed.Close()
		_ = observe.Close()
	})

	production := newProductionRTCComposition()
	base := production.components()
	var dataPlaneMu sync.Mutex
	var dataPlane *productionRTCDataPlane
	components := services.SessionRTCComponents{
		ResolveSignaling: base.ResolveSignaling,
		NewDataPlane: func(ctx context.Context, signaling rtc.Signaling) (services.SessionRTCDataPlane, error) {
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
		OpenMediaSource: base.OpenMediaSource,
	}
	provider := &productionRootCLIInferencer{
		dataPlane: func() *productionRTCDataPlane {
			dataPlaneMu.Lock()
			defer dataPlaneMu.Unlock()
			return dataPlane
		},
		sessionMedia: func() rtc.MediaEndpoints {
			return rtc.MediaEndpoints{Inbound: deviceMedia, Outbound: deviceMedia}
		},
	}
	transportDialer := &recordingDialer{}
	app, err := ComposeAgentCLI(
		&recordingToolExecutor{},
		transportDialer,
		registry,
		&recordingAudioSource{},
		&recordingAudioSink{},
		&recordingClock{now: time.Unix(123, 0)},
		WithSessionInferencer(provider),
		WithSessionRTCComponents(components),
	)
	if err != nil {
		t.Fatalf("compose generated root CLI with device binding: %v", err)
	}

	var stdout, stderr bytes.Buffer
	root := app.Generate()
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SilenceUsage = true
	root.SetArgs([]string{
		"--config-dir", t.TempDir(),
		"session",
		"--record", filepath.Join(t.TempDir(), "device-binding.session.json"),
		"--provider", "grok",
		"--model", "grok-production-root-cli-device-binding",
		"--api-key", "test-provider-key",
		"--transport", "webrtc",
		"--signaling", signalingEndpoint,
		"--media-source", source.rawURL,
		"--audio-in-device", string(inputDevice),
		"--audio-out-device", string(outputDevice),
		"complete the production RTC device-binding turn",
	})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	observedFrameCh := make(chan []int16, 1)
	observedErrCh := make(chan error, 1)
	go func() {
		frame := make([]int16, audio.FrameSize)
		if err := observe.ReadFrame(ctx, frame); err != nil {
			observedErrCh <- err
			return
		}
		observedFrameCh <- frame
	}()
	executeErrCh := make(chan error, 1)
	go func() { executeErrCh <- root.ExecuteContext(ctx) }()

	var observedFrame []int16
	select {
	case err := <-observedErrCh:
		t.Fatalf("root CLI output device read: %v", err)
	case observedFrame = <-observedFrameCh:
	}
	select {
	case err := <-executeErrCh:
		if err != nil {
			t.Fatalf("generated root WebRTC device-binding session: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
	case <-ctx.Done():
		t.Fatalf("generated root WebRTC device-binding session did not finish: %v", ctx.Err())
	}

	source.waitForFrames(t)
	if got := source.snapshot(); got.err != "" || got.frames == 0 || got.nonZeroFrames == 0 {
		t.Fatalf("production media fixture snapshot = %+v, want non-zero media without errors", got)
	}
	deviceSnapshot := deviceMedia.snapshot()
	if deviceSnapshot.outboundFrames == 0 || deviceSnapshot.outboundEnergy == 0 {
		t.Fatalf("provider-owned outbound device media = %+v, want non-zero input pump activity", deviceSnapshot)
	}
	if deviceSnapshot.inboundFrames == 0 {
		t.Fatalf("provider-owned inbound device media = %+v, want output pump activity", deviceSnapshot)
	}
	wantOutput := productionRootCLIDeviceFrame()
	if !reflect.DeepEqual(observedFrame, wantOutput.Samples) {
		for index := range wantOutput.Samples {
			if observedFrame[index] != wantOutput.Samples[index] {
				t.Fatalf("output device frame differs at sample %d: got %d want %d", index, observedFrame[index], wantOutput.Samples[index])
			}
		}
		t.Fatalf("output device frame differs from provider-owned RTC frame: got len=%d first=%d want len=%d first=%d", len(observedFrame), observedFrame[0], len(wantOutput.Samples), wantOutput.Samples[0])
	}
	if got := registry.Observations(); got.OpenCount != 4 || got.ReleaseCount != 2 {
		t.Fatalf("device registry before external cleanup = %+v, want four opens and two binding releases", got)
	}
	if !strings.Contains(stdout.String(), productionRootCLITranscript) {
		t.Fatalf("root CLI output does not contain the completed RTC transcript %q:\n%s", productionRootCLITranscript, stdout.String())
	}
	if strings.Contains(strings.ToLower(stdout.String()), "usage:") || stderr.Len() != 0 {
		t.Fatalf("device-bound RTC session emitted help or stderr: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}

	if err := feed.Close(); err != nil {
		t.Fatalf("close root CLI input feeder: %v", err)
	}
	if err := observe.Close(); err != nil {
		t.Fatalf("close root CLI output observer: %v", err)
	}
	if got := registry.Observations(); got.OpenCount != 4 || got.ReleaseCount != 4 {
		t.Fatalf("device registry after cleanup = %+v, want four opens and releases", got)
	}
	dataPlaneMu.Lock()
	concreteDataPlane := dataPlane
	dataPlaneMu.Unlock()
	if concreteDataPlane == nil {
		t.Fatal("device-bound root CLI did not construct the production RTC data plane")
	}
	if concreteDataPlane.mediaFrames.Load() == 0 || concreteDataPlane.mediaPackets.Load() == 0 {
		t.Fatalf("production RTC media activity = outbound frames %d, received packets %d; want both non-zero", concreteDataPlane.mediaFrames.Load(), concreteDataPlane.mediaPackets.Load())
	}
	if transportDials := transportDialer.dials.Load(); transportDials != 0 {
		t.Fatalf("WebRTC device-bound session used the composed WebSocket transport dialer %d times", transportDials)
	}
}

func TestGeneratedRootCLI_WebRTCPreservesTypedStartupFailures(t *testing.T) {
	const secret = "root-cli-media-password"

	closedServer := httptest.NewServer(http.NotFoundHandler())
	closedURL, err := url.Parse(closedServer.URL)
	if err != nil {
		t.Fatalf("parse closed media fixture URL: %v", err)
	}
	closedServer.Close()
	unreachableMedia := "go2rtc://" + closedURL.Host + "/api/ws?src=unreachable-root-cli"

	tests := []struct {
		name          string
		signaling     string
		media         string
		wantCause     error
		wantPhase     string
		wantMediaErr  bool
		forbiddenText string
	}{
		{
			name:      "unreachable signaling",
			signaling: "https://signal.invalid/root-cli",
			media:     "go2rtc://media.invalid/api/ws?src=must-not-open",
			wantCause: rtc.ErrSignalingUnreachable,
			wantPhase: "resolve signaling",
		},
		{
			name:          "malformed media source",
			signaling:     "loopback://root-cli/malformed-media",
			media:         "rtsp://camera:" + secret + "@",
			wantCause:     rtc.ErrMalformedSource,
			wantPhase:     "open media source",
			wantMediaErr:  true,
			forbiddenText: secret,
		},
		{
			name:         "unreachable media source",
			signaling:    "loopback://root-cli/unreachable-media",
			media:        unreachableMedia,
			wantCause:    rtc.ErrSourceUnreachable,
			wantPhase:    "open media source",
			wantMediaErr: true,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			provider := &recordingSessionInferencer{}
			result := executeGeneratedRootCLIWebRTCFailure(t, provider, nil, testCase.signaling, testCase.media, 5*time.Second)
			if result.err == nil {
				t.Fatal("invalid WebRTC startup unexpectedly completed successfully")
			}
			if !errors.Is(result.err, testCase.wantCause) {
				t.Fatalf("root CLI error = %v, want errors.Is(..., %v)", result.err, testCase.wantCause)
			}
			var phaseErr *services.SessionRTCRuntimeError
			if !errors.As(result.err, &phaseErr) {
				t.Fatalf("root CLI error type = %T, want *services.SessionRTCRuntimeError: %v", result.err, result.err)
			}
			if phaseErr.Phase != testCase.wantPhase {
				t.Fatalf("root CLI failure phase = %q, want %q", phaseErr.Phase, testCase.wantPhase)
			}
			if provider.connects != 0 {
				t.Fatalf("provider ConnectSession calls = %d, want zero before RTC startup failure", provider.connects)
			}
			if testCase.wantMediaErr {
				var sourceErr *rtc.MediaSourceError
				if !errors.As(result.err, &sourceErr) {
					t.Fatalf("root CLI media error type = %T, want *rtc.MediaSourceError: %v", result.err, result.err)
				}
			}
			if testCase.forbiddenText != "" && strings.Contains(result.err.Error(), testCase.forbiddenText) {
				t.Fatalf("root CLI error leaked source credentials: %v", result.err)
			}
			combinedOutput := strings.ToLower(result.stdout + result.stderr)
			if strings.Contains(combinedOutput, "usage:") || strings.Contains(combinedOutput, "run or manage agent sessions") {
				t.Fatalf("startup failure emitted help instead of a lifecycle error: stdout=%q stderr=%q", result.stdout, result.stderr)
			}
			if strings.Contains(result.stdout, productionRootCLITranscript) {
				t.Fatalf("startup failure emitted a synthetic transcript: %q", result.stdout)
			}
		})
	}
}

func TestGeneratedRootCLI_WebRTCZeroMediaCannotCompleteSyntheticTurn(t *testing.T) {
	production := newProductionRTCComposition()
	base := production.components()
	var dataPlaneMu sync.Mutex
	var dataPlane *productionRTCDataPlane
	components := services.SessionRTCComponents{
		ResolveSignaling: base.ResolveSignaling,
		NewDataPlane: func(ctx context.Context, signaling rtc.Signaling) (services.SessionRTCDataPlane, error) {
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
		OpenMediaSource: func(context.Context, string) (rtc.InboundMedia, error) {
			return &zeroProductionCLIMedia{}, nil
		},
	}
	provider := &productionRootCLIInferencer{
		dataPlane: func() *productionRTCDataPlane {
			dataPlaneMu.Lock()
			defer dataPlaneMu.Unlock()
			return dataPlane
		},
	}
	result := executeGeneratedRootCLIWebRTCFailure(t, provider, &components, "loopback://root-cli/zero-media", "fixture://root-cli/zero-media", 2*time.Second)
	if result.err == nil {
		t.Fatal("zero-frame WebRTC source unexpectedly completed successfully")
	}
	if !errors.Is(result.err, context.DeadlineExceeded) {
		t.Fatalf("zero-frame root CLI error = %v, want bounded context deadline", result.err)
	}
	dataPlaneMu.Lock()
	concrete := dataPlane
	dataPlaneMu.Unlock()
	if concrete == nil {
		t.Fatal("zero-frame control did not construct the production data plane")
	}
	if concrete.mediaFrames.Load() != 0 || concrete.mediaPackets.Load() != 0 {
		t.Fatalf("zero-frame RTC activity = outbound frames %d, received packets %d; want zero", concrete.mediaFrames.Load(), concrete.mediaPackets.Load())
	}
	if strings.Contains(result.stdout, productionRootCLITranscript) {
		t.Fatalf("zero-frame control emitted a synthetic transcript: %q", result.stdout)
	}
	if strings.Contains(strings.ToLower(result.stdout+result.stderr), "usage:") {
		t.Fatalf("zero-frame control emitted help: stdout=%q stderr=%q", result.stdout, result.stderr)
	}
}

type productionRootCLIRunResult struct {
	err            error
	stdout, stderr string
}

func executeGeneratedRootCLIWebRTCFailure(t *testing.T, provider messages.SessionInferencer, components *services.SessionRTCComponents, signaling, media string, timeout time.Duration) productionRootCLIRunResult {
	t.Helper()
	transportDialer := &recordingDialer{}
	options := []CompositionOption{WithSessionInferencer(provider)}
	if components != nil {
		options = append(options, WithSessionRTCComponents(*components))
	}
	app, err := ComposeAgentCLI(
		&recordingToolExecutor{},
		transportDialer,
		&recordingDeviceRegistry{},
		&recordingAudioSource{},
		&recordingAudioSink{},
		&recordingClock{now: time.Unix(123, 0)},
		options...,
	)
	if err != nil {
		t.Fatalf("compose generated root CLI: %v", err)
	}

	var stdout, stderr bytes.Buffer
	root := app.Generate()
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SilenceUsage = true
	sessionCommand, _, findErr := root.Find([]string{"session"})
	if findErr != nil {
		t.Fatalf("find generated session command: %v", findErr)
	}
	sessionCommand.SilenceUsage = true
	root.SetArgs([]string{
		"--config-dir", t.TempDir(),
		"session",
		"--record", filepath.Join(t.TempDir(), "failure-control.session.json"),
		"--provider", "grok",
		"--model", "grok-failure-control",
		"--api-key", "test-provider-key",
		"--transport", "webrtc",
		"--signaling", signaling,
		"--media-source", media,
		"must not emit a synthetic response",
	})
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return productionRootCLIRunResult{err: root.ExecuteContext(ctx), stdout: stdout.String(), stderr: stderr.String()}
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
	dataPlane    func() *productionRTCDataPlane
	record       func(string)
	sessionMedia func() rtc.MediaEndpoints
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
	var media rtc.MediaEndpoints
	if i.sessionMedia != nil {
		media = i.sessionMedia()
	}
	return newProductionRootCLISessionWithMedia(conn, media), nil
}

type productionRootCLISession struct {
	receive   *messages.TypedBuffer[messages.StreamMessage]
	done      chan struct{}
	conn      transport.Conn
	media     rtc.MediaEndpoints
	sendOnce  sync.Once
	closeOnce sync.Once
}

func newProductionRootCLISessionWithMedia(conn transport.Conn, media rtc.MediaEndpoints) *productionRootCLISession {
	session := &productionRootCLISession{
		receive: messages.NewTypedBuffer[messages.StreamMessage](16),
		done:    make(chan struct{}),
		conn:    conn,
		media:   media,
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

func (s *productionRootCLISession) RTCMedia() rtc.MediaEndpoints {
	if s == nil {
		return rtc.MediaEndpoints{}
	}
	return s.media
}

func (s *productionRootCLISession) Close() error {
	s.closeOnce.Do(func() {
		close(s.done)
		if s.conn != nil {
			_ = s.conn.Close()
		}
		if s.media.Inbound != nil {
			_ = s.media.Inbound.Close()
		}
		if s.media.Outbound != nil {
			_ = s.media.Outbound.Close()
		}
	})
	return nil
}

func newProductionRootCLIDeviceRegistry(t *testing.T) (*audio.VirtualRegistry, audio.DeviceID, audio.DeviceID, audio.DeviceID, audio.DeviceID) {
	t.Helper()
	registry, err := audio.NewVirtualRegistry(audio.VirtualBackendConfig{
		Devices: []audio.VirtualDeviceConfig{
			{ID: "root-cli-input", Name: "Root CLI Input", Direction: audio.DirectionInput, LoopbackID: "root-cli-input-feed"},
			{ID: "root-cli-input-feed", Name: "Root CLI Input Feed", Direction: audio.DirectionOutput, LoopbackID: "root-cli-input"},
			{ID: "root-cli-output", Name: "Root CLI Output", Direction: audio.DirectionOutput, LoopbackID: "root-cli-output-observer"},
			{ID: "root-cli-output-observer", Name: "Root CLI Output Observer", Direction: audio.DirectionInput, LoopbackID: "root-cli-output"},
		},
		Defaults: map[audio.Direction]string{
			audio.DirectionInput:  "root-cli-input",
			audio.DirectionOutput: "root-cli-output",
		},
	})
	if err != nil {
		t.Fatalf("new root CLI virtual device registry: %v", err)
	}
	return registry,
		audio.DeviceID("virtual:root-cli-input"),
		audio.DeviceID("virtual:root-cli-input-feed"),
		audio.DeviceID("virtual:root-cli-output"),
		audio.DeviceID("virtual:root-cli-output-observer")
}

type productionRootCLIDeviceMedia struct {
	inbound chan rtc.PCMFrame
	closed  chan struct{}

	closeOnce      sync.Once
	outboundFrames atomic.Uint64
	outboundEnergy atomic.Uint64
	inboundFrames  atomic.Uint64
}

type productionRootCLIDeviceMediaSnapshot struct {
	outboundFrames uint64
	outboundEnergy uint64
	inboundFrames  uint64
}

func newProductionRootCLIDeviceMedia(frame rtc.PCMFrame) *productionRootCLIDeviceMedia {
	media := &productionRootCLIDeviceMedia{
		inbound: make(chan rtc.PCMFrame, 1),
		closed:  make(chan struct{}),
	}
	media.inbound <- cloneProductionRootCLIFrame(frame)
	return media
}

func (m *productionRootCLIDeviceMedia) WriteFrame(ctx context.Context, frame rtc.PCMFrame) error {
	if m == nil {
		return rtc.ErrSessionMediaClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if len(frame.Samples) == 0 {
		return errors.New("root CLI device media received an empty frame")
	}
	select {
	case <-m.closed:
		return rtc.ErrSessionMediaClosed
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	m.outboundFrames.Add(1)
	m.outboundEnergy.Add(productionRootCLIAudioEnergy(frame.Samples))
	return nil
}

func (m *productionRootCLIDeviceMedia) ReadFrame(ctx context.Context) (rtc.PCMFrame, error) {
	if m == nil {
		return rtc.PCMFrame{}, rtc.ErrSessionMediaClosed
	}
	// Prefer a frame already queued even if the session is concurrently
	// closing; this makes the seeded device-output assertion deterministic.
	select {
	case frame := <-m.inbound:
		m.inboundFrames.Add(1)
		return cloneProductionRootCLIFrame(frame), nil
	default:
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case frame := <-m.inbound:
		m.inboundFrames.Add(1)
		return cloneProductionRootCLIFrame(frame), nil
	case <-m.closed:
		return rtc.PCMFrame{}, rtc.ErrSessionMediaClosed
	case <-ctx.Done():
		return rtc.PCMFrame{}, ctx.Err()
	}
}

func (m *productionRootCLIDeviceMedia) Close() error {
	if m == nil {
		return nil
	}
	m.closeOnce.Do(func() { close(m.closed) })
	return nil
}

func (m *productionRootCLIDeviceMedia) snapshot() productionRootCLIDeviceMediaSnapshot {
	if m == nil {
		return productionRootCLIDeviceMediaSnapshot{}
	}
	return productionRootCLIDeviceMediaSnapshot{
		outboundFrames: m.outboundFrames.Load(),
		outboundEnergy: m.outboundEnergy.Load(),
		inboundFrames:  m.inboundFrames.Load(),
	}
}

func cloneProductionRootCLIFrame(frame rtc.PCMFrame) rtc.PCMFrame {
	frame.Samples = append([]int16(nil), frame.Samples...)
	return frame
}

func productionRootCLIDeviceFrame() rtc.PCMFrame {
	samples := make([]int16, audio.FrameSize)
	for index := range samples {
		samples[index] = int16((index % 97) + 1)
	}
	return rtc.PCMFrame{Samples: samples}
}

func productionRootCLIAudioEnergy(samples []int16) uint64 {
	var energy uint64
	for _, sample := range samples {
		if sample < 0 {
			energy += uint64(-int64(sample))
			continue
		}
		energy += uint64(sample)
	}
	return energy
}

var _ rtc.InboundMedia = (*productionRootCLIDeviceMedia)(nil)
var _ rtc.OutboundMedia = (*productionRootCLIDeviceMedia)(nil)
var _ rtc.MediaSession = (*productionRootCLISession)(nil)

type productionCLIMediaFixture struct {
	server      *httptest.Server
	rawURL      string
	fixtureCtx  context.Context
	cancel      context.CancelFunc
	handlerDone chan struct{}
	handlerOnce sync.Once
	framesSent  chan struct{}
	framesOnce  sync.Once

	mu            sync.Mutex
	path          string
	source        string
	frames        int
	nonZeroFrames int
	err           error
	cleanupOnce   sync.Once
}

type productionCLIMediaFixtureSnapshot struct {
	path, source  string
	frames        int
	nonZeroFrames int
	err           string
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
			payload := bytes.Repeat([]byte{0x01}, 160)
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
			if hasNonZeroBytes(payload) {
				fixture.nonZeroFrames++
			}
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
		path:          f.path,
		source:        f.source,
		frames:        f.frames,
		nonZeroFrames: f.nonZeroFrames,
		err:           errorString(f.err),
	}
}

func hasNonZeroBytes(value []byte) bool {
	for _, item := range value {
		if item != 0 {
			return true
		}
	}
	return false
}

type zeroProductionCLIMedia struct{}

func (*zeroProductionCLIMedia) ReadFrame(context.Context) (rtc.PCMFrame, error) {
	return rtc.PCMFrame{}, io.EOF
}

func (*zeroProductionCLIMedia) Close() error { return nil }

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
