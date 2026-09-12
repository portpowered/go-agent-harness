package agentruntime_test

import devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"

import (
	"context"
	"errors"
	devicert "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/runtime"
	"io"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agentruntime "github.com/portpowered/go-agent-harness/agent-cli/internal/services/internal/agentruntime"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/inference"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
)

// TestRunSessionRTCDeviceBindingStartsRuntimePumps proves the production
// session boundary starts both device pumps from the provider-owned RTC media
// endpoints. The virtual registry provides exact device IDs and independent
// input/output loopbacks, while the fake session models a provider that owns
// the media endpoint lifecycle.
func TestRunSessionRTCDeviceBindingStartsRuntimePumps(t *testing.T) {
	registry := newRTCDeviceRoundtripRegistry(t)
	feed, err := devicegw.NewDeviceSink(registry, rtcRoundtripMicFeedID)
	if err != nil {
		t.Fatalf("open virtual microphone feeder: %v", err)
	}
	observe, err := devicegw.NewDeviceSource(registry, rtcRoundtripSpeakerID)
	if err != nil {
		_ = feed.Close()
		t.Fatalf("open virtual speaker observer: %v", err)
	}
	peer := newLoopbackRTCTrackPeer(rtcRoundtripFrameCount)
	sessionInferencer := newRuntimeRTCSessionInferencer(peer)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	t.Cleanup(func() {
		if session := sessionInferencer.sessionValue(); session != nil {
			_ = session.Close()
		}
		_ = peer.Close()
		_ = feed.Close()
		_ = observe.Close()
	})

	runErrCh := make(chan error, 1)
	go func() {
		runErrCh <- agentruntime.RunSession(ctx, io.Discard, agentruntime.SessionRunOptions{ModelCatalog: testModelCatalog(),
			ReplayPath:        "synthetic.json",
			SessionInferencer: sessionInferencer,
			RTCDeviceBinding: agentruntime.RTCDeviceBindingRequest{
				Registry:      registry,
				InputDevice:   rtcRoundtripInputID,
				OutputDevice:  rtcRoundtripOutputID,
				InputPresent:  true,
				OutputPresent: true,
			},
		})
	}()

	var session *runtimeRTCSession
	select {
	case session = <-sessionInferencer.connected:
	case <-ctx.Done():
		t.Fatalf("provider session did not connect: %v", ctx.Err())
	}

	wantFrames := make([][]int16, rtcRoundtripFrameCount)
	for frameIndex := range wantFrames {
		wantFrames[frameIndex] = rtcRoundtripPCMFrame(frameIndex)
		if err := feed.WriteFrame(ctx, wantFrames[frameIndex]); err != nil {
			t.Fatalf("feed virtual microphone frame %d: %v", frameIndex, err)
		}
	}

	readCtx, readCancel := context.WithTimeout(ctx, rtcRoundtripTimeout)
	defer readCancel()
	for frameIndex, want := range wantFrames {
		got := make([]int16, audio.FrameSize)
		if err := observe.ReadFrame(readCtx, got); err != nil {
			t.Fatalf("observe virtual speaker frame %d: %v", frameIndex, err)
		}
		if pcmAbsoluteEnergy(got) == 0 {
			t.Fatalf("observed virtual speaker frame %d has no emitted audio energy", frameIndex)
		}
		for sampleIndex := range want {
			if got[sampleIndex] != want[sampleIndex] {
				t.Fatalf("speaker frame %d sample %d = %d, want %d", frameIndex, sampleIndex, got[sampleIndex], want[sampleIndex])
			}
		}
	}

	if got := peer.Stats(); got.Writes != rtcRoundtripFrameCount || got.Reads != rtcRoundtripFrameCount {
		t.Fatalf("runtime RTC peer stats = %+v, want %d writes and reads", got, rtcRoundtripFrameCount)
	}
	session.finish()

	select {
	case err := <-runErrCh:
		if err != nil {
			t.Fatalf("RunSession with runtime RTC pumps: %v", err)
		}
	case <-ctx.Done():
		t.Fatalf("RunSession did not finish after provider close: %v", ctx.Err())
	}

	if err := feed.Close(); err != nil {
		t.Fatalf("close virtual microphone feeder: %v", err)
	}
	if err := observe.Close(); err != nil {
		t.Fatalf("close virtual speaker observer: %v", err)
	}
	if got := registry.Observations(); got.OpenCount != 4 || got.ReleaseCount != 4 {
		t.Fatalf("runtime registry observations = %+v, want four opens and releases", got)
	}
}

func TestRunSessionRTCDeviceBindingPropagatesPumpError(t *testing.T) {
	registry := newRTCDeviceRoundtripRegistry(t)
	feed, err := devicegw.NewDeviceSink(registry, rtcRoundtripMicFeedID)
	if err != nil {
		t.Fatalf("open virtual microphone feeder: %v", err)
	}
	wantErr := errors.New("outbound RTC track failed")
	sessionInferencer := newRuntimeRTCSessionInferencerWithMedia(agentruntime.RTCMediaEndpoints{
		Outbound: failingRTCOutboundMedia{err: wantErr},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	t.Cleanup(func() {
		if session := sessionInferencer.sessionValue(); session != nil {
			_ = session.Close()
		}
		_ = feed.Close()
	})

	runErrCh := make(chan error, 1)
	go func() {
		runErrCh <- agentruntime.RunSession(ctx, io.Discard, agentruntime.SessionRunOptions{ModelCatalog: testModelCatalog(),
			ReplayPath:        "synthetic.json",
			SessionInferencer: sessionInferencer,
			RTCDeviceBinding: agentruntime.RTCDeviceBindingRequest{
				Registry:     registry,
				InputDevice:  rtcRoundtripInputID,
				InputPresent: true,
			},
		})
	}()

	select {
	case <-sessionInferencer.connected:
	case <-ctx.Done():
		t.Fatalf("provider session did not connect: %v", ctx.Err())
	}
	if err := feed.WriteFrame(ctx, rtcRoundtripPCMFrame(0)); err != nil {
		t.Fatalf("feed virtual microphone frame: %v", err)
	}

	select {
	case err := <-runErrCh:
		if !errors.Is(err, wantErr) {
			t.Fatalf("RunSession error = %v, want outbound pump error", err)
		}
		var sourceErr *devicert.RTCDeviceSourceError
		if !errors.As(err, &sourceErr) {
			t.Fatalf("RunSession error = %v, want devicert.RTCDeviceSourceError", err)
		}
	case <-ctx.Done():
		t.Fatalf("RunSession did not surface the outbound pump error: %v", ctx.Err())
	}

	if err := feed.Close(); err != nil {
		t.Fatalf("close virtual microphone feeder: %v", err)
	}
	if got := registry.Observations(); got.OpenCount != 2 || got.ReleaseCount != 2 {
		t.Fatalf("pump-error registry observations = %+v, want two opens and releases", got)
	}
}

type runtimeRTCSessionInferencer struct {
	media     agentruntime.RTCMediaEndpoints
	connected chan *runtimeRTCSession

	mu      sync.Mutex
	session *runtimeRTCSession
}

func newRuntimeRTCSessionInferencer(peer *loopbackRTCTrackPeer) *runtimeRTCSessionInferencer {
	return newRuntimeRTCSessionInferencerWithMedia(agentruntime.RTCMediaEndpoints{
		Inbound:  peer,
		Outbound: peer,
	})
}

func newRuntimeRTCSessionInferencerWithMedia(media agentruntime.RTCMediaEndpoints) *runtimeRTCSessionInferencer {
	return &runtimeRTCSessionInferencer{
		media:     media,
		connected: make(chan *runtimeRTCSession, 1),
	}
}

func (i *runtimeRTCSessionInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	session := &runtimeRTCSession{
		recv:  messages.NewTypedBuffer[messages.StreamMessage](8),
		done:  make(chan struct{}),
		media: i.media,
	}
	i.mu.Lock()
	i.session = session
	i.mu.Unlock()
	if !session.recv.Write(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeSessionOpen,
		Value: messages.NewSessionOpenValue("runtime-rtc-session", "test"),
	}) {
		return nil, ctx.Err()
	}
	select {
	case i.connected <- session:
		return session, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (i *runtimeRTCSessionInferencer) sessionValue() *runtimeRTCSession {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.session
}

type runtimeRTCSession struct {
	recv  *messages.TypedBuffer[messages.StreamMessage]
	done  chan struct{}
	media agentruntime.RTCMediaEndpoints

	mu         sync.Mutex
	sent       []messages.StreamMessage
	doneOnce   sync.Once
	closeCalls atomic.Int32
}

type failingRTCOutboundMedia struct {
	err error
}

func (m failingRTCOutboundMedia) WriteFrame(context.Context, audio.PCMFrame) error { return m.err }
func (m failingRTCOutboundMedia) Close() error                                     { return nil }

func (s *runtimeRTCSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	select {
	case <-ctx.Done():
		return false
	default:
	}
	s.mu.Lock()
	s.sent = append(s.sent, msg)
	s.mu.Unlock()
	return true
}

func (s *runtimeRTCSession) Receive() *messages.TypedBuffer[messages.StreamMessage] { return s.recv }

func (s *runtimeRTCSession) Done() <-chan struct{} { return s.done }

func (s *runtimeRTCSession) Close() error {
	s.closeCalls.Add(1)
	s.doneOnce.Do(func() { close(s.done) })
	return nil
}

func (s *runtimeRTCSession) finish() { _ = s.Close() }

func (s *runtimeRTCSession) RTCMedia() agentruntime.RTCMediaEndpoints { return s.media }

func (s *runtimeRTCSession) sentMessages() []messages.StreamMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]messages.StreamMessage(nil), s.sent...)
}

var (
	_ messages.SessionInferencer   = (*runtimeRTCSessionInferencer)(nil)
	_ messages.Session             = (*runtimeRTCSession)(nil)
	_ agentruntime.RTCMediaSession = (*runtimeRTCSession)(nil)
)

const (
	terminalDrainProviderRate    = 24000
	terminalDrainProviderSamples = 9600
	terminalDrainDeviceSamples   = 6400
)

func TestRTCDeviceBoundSessionTerminalDrainPreservesAcceptedProviderAudio(t *testing.T) {
	scenario := newTerminalDrainExternalScenario(t)
	samples := terminalDrainExternalSamples()
	scenario.push(t, samples)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runErr := make(chan error, 1)
	go func() {
		runErr <- agentruntime.RunSession(ctx, io.Discard, agentruntime.SessionRunOptions{
			ModelCatalog: testModelCatalog(), ReplayPath: "synthetic.json", WaitForClose: true,
			SessionInferencer: &terminalDrainExternalInferencer{session: scenario.provider},
			RTCDeviceBinding:  scenario.request,
		})
	}()
	phase := scenario.releaseRun(t, runErr)
	select {
	case err := <-runErr:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("run session (%s): %v", phase, err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("run session (%s) did not complete", phase)
	}
	scenario.assertOutput(t, phase, samples)
}

type terminalDrainExternalScenario struct {
	registry        *devicegw.SimulatedDuplexRegistry
	request         agentruntime.RTCDeviceBindingRequest
	providerMedia   *audio.SessionMedia
	barrierInbound  *terminalDrainExternalInbound
	provider        *terminalDrainExternalSession
	gate            *terminalDrainExternalGate
	admittedMu      sync.Mutex
	admittedSamples int
	admittedPCM     []int16
}

func newTerminalDrainExternalScenario(t *testing.T) *terminalDrainExternalScenario {
	t.Helper()
	scenario := &terminalDrainExternalScenario{}
	registry, err := devicegw.NewSimulatedDuplexRegistry(devicegw.DuplexScenario{
		Seed:    53,
		Render:  devicegw.ClockSpec{NominalRate: audio.SampleRate, Quanta: []int{audio.FrameSize}},
		Capture: devicegw.ClockSpec{NominalRate: audio.SampleRate, Quanta: []int{audio.FrameSize}},
	})
	if err != nil {
		t.Fatalf("new callback-clocked registry: %v", err)
	}
	scenario.registry = registry
	scenario.gate = &terminalDrainExternalGate{
		started: make(chan struct{}), release: make(chan struct{}),
		record:  func(samples []int16) { scenario.record(samples) },
		advance: func() error { return registry.Advance(1) },
	}
	holdTone := audio.DefaultHoldToneConfig()
	holdTone.GapThreshold = time.Hour
	scenario.request = agentruntime.RTCDeviceBindingRequest{
		Registry: registry, OutputPresent: true, OutputDevice: "",
		OutputSampleRate: terminalDrainProviderRate, HoldToneConfig: &holdTone,
		PlaybackSamplesObserver: scenario.gate.observe,
	}
	scenario.providerMedia = audio.NewSessionMediaAtRate(nil, terminalDrainProviderRate)
	scenario.barrierInbound = &terminalDrainExternalInbound{
		InboundMedia: scenario.providerMedia.Endpoints().Inbound,
		drainStarted: make(chan struct{}),
	}
	scenario.provider = &terminalDrainExternalSession{
		receive: messages.NewTypedBuffer[messages.StreamMessage](4), done: make(chan struct{}),
		media: audio.MediaEndpoints{
			Inbound: scenario.barrierInbound, Outbound: scenario.providerMedia.Endpoints().Outbound,
		},
		closeStarted: make(chan struct{}), releaseClose: make(chan struct{}),
	}
	if !scenario.provider.receive.Write(context.Background(), messages.StreamMessage{Type: messages.StreamTypeSessionClose}) {
		t.Fatal("provider terminal did not enter receive buffer")
	}
	t.Cleanup(func() { scenario.cleanup(t) })
	return scenario
}

func terminalDrainExternalSamples() []int16 {
	samples := make([]int16, terminalDrainProviderSamples)
	for index := range samples {
		samples[index] = int16(index%257 - 128)
	}
	return samples
}

func (s *terminalDrainExternalScenario) record(samples []int16) {
	s.admittedMu.Lock()
	s.admittedSamples += len(samples)
	s.admittedPCM = append(s.admittedPCM, samples...)
	s.admittedMu.Unlock()
}

func (s *terminalDrainExternalScenario) push(t *testing.T, samples []int16) {
	t.Helper()
	if err := s.providerMedia.PushInbound(samples); err != nil {
		t.Fatalf("push provider response: %v", err)
	}
	if err := s.providerMedia.FlushInbound(); err != nil {
		t.Fatalf("flush provider response: %v", err)
	}
}

func (s *terminalDrainExternalScenario) releaseRun(t *testing.T, runErr <-chan error) string {
	t.Helper()
	select {
	case <-s.provider.closeStarted:
		s.provider.releaseOnce.Do(func() { close(s.provider.releaseClose) })
		return "provider-close-before-drain"
	case <-s.barrierInbound.drainStarted:
		close(s.gate.release)
		select {
		case <-s.provider.closeStarted:
		case <-time.After(time.Second):
			t.Fatal("provider close did not follow sink drain")
		}
		s.provider.releaseOnce.Do(func() { close(s.provider.releaseClose) })
		return "drain-before-provider-close"
	case err := <-runErr:
		t.Fatalf("run session completed before either lifecycle barrier: %v", err)
	}
	return ""
}

func (s *terminalDrainExternalScenario) assertOutput(t *testing.T, phase string, samples []int16) {
	t.Helper()
	s.admittedMu.Lock()
	got := s.admittedSamples
	gotPCM := append([]int16(nil), s.admittedPCM...)
	s.admittedMu.Unlock()
	if phase != "drain-before-provider-close" {
		t.Fatalf("terminal drain phase = %s, want drain-before-provider-close", phase)
	}
	if got != terminalDrainDeviceSamples {
		t.Fatalf("terminal drain phase %s admitted %d device samples, want exact %d", phase, got, terminalDrainDeviceSamples)
	}
	reference, err := wavio.NewPCM16Resampler(terminalDrainProviderRate, audio.SampleRate)
	if err != nil {
		t.Fatalf("create exact resampler: %v", err)
	}
	wantPCM, err := reference.Process(samples, true)
	if err != nil {
		t.Fatalf("resample exact provider block: %v", err)
	}
	if !reflect.DeepEqual(gotPCM, wantPCM) {
		t.Fatalf("terminal drain phase %s changed admitted PCM: got %d samples, want exact resampled block", phase, len(gotPCM))
	}
	providerSamples := s.barrierInbound.samplesRead()
	renderedPCM := s.registry.RenderedSamples()
	stats := s.registry.PlaybackStats()
	if providerSamples != len(samples) {
		t.Fatalf("terminal drain phase %s provider read %d samples, want exact %d", phase, providerSamples, len(samples))
	}
	assertTerminalDrainRendered(t, phase, wantPCM, renderedPCM, stats)
	consumedSamples := stats.RenderedSamples - stats.UnderflowSamples
	if consumedSamples != uint64(got) || consumedSamples != uint64(len(wantPCM)) || stats.QueuedSamples != 0 {
		t.Fatalf("terminal drain phase %s failed provider/admission/consumption/queue reconciliation: provider=%d admitted=%d consumed=%d rendered=%d queued=%d stats=%+v", phase, providerSamples, got, consumedSamples, len(renderedPCM), stats.QueuedSamples, stats)
	}
	if stats.DroppedSamples != 0 || stats.OverflowEvents != 0 || stats.DiscardedSamples != 0 || stats.DiscardEvents != 0 {
		t.Fatalf("terminal drain phase %s reported playback loss: %+v", phase, stats)
	}
	select {
	case <-s.provider.done:
	default:
		t.Fatalf("terminal drain phase %s returned before provider shutdown", phase)
	}
	t.Logf("C64_RENDER_EVIDENCE provider_samples=%d admitted_samples=%d consumed_samples=%d rendered_samples=%d queued_samples=%d underflow_samples=%d callback_count=%d shutdown=complete", providerSamples, got, consumedSamples, len(renderedPCM), stats.QueuedSamples, stats.UnderflowSamples, stats.CallbackCount)
}

func assertTerminalDrainRendered(t *testing.T, phase string, wantPCM, renderedPCM []int16, stats audio.PlaybackQueueStats) {
	t.Helper()
	if len(renderedPCM) < len(wantPCM) || !reflect.DeepEqual(renderedPCM[:len(wantPCM)], wantPCM) {
		t.Fatalf("terminal drain phase %s changed rendered PCM prefix: got %d samples, want exact resampled block of %d; stats=%+v", phase, len(renderedPCM), len(wantPCM), stats)
	}
	if stats.RenderedSamples < stats.UnderflowSamples || stats.RenderedSamples != uint64(len(renderedPCM)) || stats.UnderflowEvents != 1 || stats.UnderflowSamples != uint64(len(renderedPCM)-len(wantPCM)) {
		t.Fatalf("terminal drain phase %s reported unexpected render accounting: rendered=%d pcm=%d underflow_events=%d underflow_samples=%d", phase, stats.RenderedSamples, len(renderedPCM), stats.UnderflowEvents, stats.UnderflowSamples)
	}
	for index, sample := range renderedPCM[len(wantPCM):] {
		if sample != 0 {
			t.Fatalf("terminal drain phase %s fabricated nonzero underflow sample at offset %d: %d", phase, index, sample)
		}
	}
}

func (s *terminalDrainExternalScenario) cleanup(t *testing.T) {
	t.Helper()
	s.provider.releaseOnce.Do(func() { close(s.provider.releaseClose) })
	if err := s.providerMedia.Close(); err != nil {
		t.Errorf("cleanup provider media: %v", err)
	}
}

type terminalDrainExternalGate struct {
	started   chan struct{}
	release   chan struct{}
	record    func([]int16)
	advance   func() error
	startOnce sync.Once
}

func (g *terminalDrainExternalGate) observe(ctx context.Context, _ int, samples []int16) error {
	g.record(samples)
	g.startOnce.Do(func() { close(g.started) })
	select {
	case <-g.release:
		return g.advance()
	case <-ctx.Done():
		return ctx.Err()
	}
}

type terminalDrainExternalInbound struct {
	audio.InboundMedia
	drainStarted chan struct{}
	closeOnce    sync.Once
	readMu       sync.Mutex
	readSamples  int
}

func (m *terminalDrainExternalInbound) ReadFrame(ctx context.Context) (audio.PCMFrame, error) {
	frame, err := m.InboundMedia.ReadFrame(ctx)
	if err == nil {
		m.readMu.Lock()
		m.readSamples += len(frame.Samples)
		m.readMu.Unlock()
	}
	return frame, err
}

func (m *terminalDrainExternalInbound) samplesRead() int {
	m.readMu.Lock()
	defer m.readMu.Unlock()
	return m.readSamples
}

func (m *terminalDrainExternalInbound) Close() error {
	m.closeOnce.Do(func() { close(m.drainStarted) })
	return m.InboundMedia.Close()
}

type terminalDrainExternalInferencer struct {
	session messages.Session
}

func (i *terminalDrainExternalInferencer) Request() inference.SessionRequest {
	return inference.SessionRequest{Config: models.SessionConfig{
		InputAudioSampleRate: models.SampleRate(terminalDrainProviderRate), OutputAudioSampleRate: models.SampleRate(terminalDrainProviderRate),
	}}
}

func (i *terminalDrainExternalInferencer) ConnectSession(context.Context) (messages.Session, error) {
	return i.session, nil
}

type terminalDrainExternalSession struct {
	receive *messages.TypedBuffer[messages.StreamMessage]
	done    chan struct{}
	media   audio.MediaEndpoints

	closeStarted chan struct{}
	releaseClose chan struct{}
	closeOnce    sync.Once
	releaseOnce  sync.Once
	doneOnce     sync.Once
}

func (s *terminalDrainExternalSession) Send(ctx context.Context, _ messages.StreamMessage) bool {
	select {
	case <-ctx.Done():
		return false
	default:
		return true
	}
}

func (s *terminalDrainExternalSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.receive
}

func (s *terminalDrainExternalSession) Done() <-chan struct{} { return s.done }

func (s *terminalDrainExternalSession) RTCMedia() agentruntime.RTCMediaEndpoints { return s.media }

func (s *terminalDrainExternalSession) Close() error {
	var mediaErr error
	s.closeOnce.Do(func() {
		close(s.closeStarted)
		<-s.releaseClose
		mediaErr = errors.Join(s.media.Inbound.Close(), s.media.Outbound.Close())
		s.doneOnce.Do(func() { close(s.done) })
	})
	return mediaErr
}

var _ messages.SessionInferencer = (*terminalDrainExternalInferencer)(nil)
var _ messages.Session = (*terminalDrainExternalSession)(nil)
var _ agentruntime.RTCMediaSession = (*terminalDrainExternalSession)(nil)
