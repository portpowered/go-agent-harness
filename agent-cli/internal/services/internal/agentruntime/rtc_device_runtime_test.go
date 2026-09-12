package agentruntime_test

import devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
import (
	"context"
	"errors"
	"fmt"
	agentruntime "github.com/portpowered/go-agent-harness/agent-cli/internal/services/internal/agentruntime"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
	devicert "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/runtime"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/inference"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"io"
	"os"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

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
	mu        sync.Mutex
	session   *runtimeRTCSession
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
	recv       *messages.TypedBuffer[messages.StreamMessage]
	done       chan struct{}
	media      agentruntime.RTCMediaEndpoints
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
func (s *runtimeRTCSession) Done() <-chan struct{}                                  { return s.done }
func (s *runtimeRTCSession) Close() error {
	s.closeCalls.Add(1)
	s.doneOnce.Do(func() { close(s.done) })
	return nil
}
func (s *runtimeRTCSession) finish()                                  { _ = s.Close() }
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
	terminalDrainProviderRate     = 24000
	terminalDrainProviderSamples  = 9600
	terminalDrainDeviceSamples    = 6400
	terminalDrainFinalResponseID  = "terminal-drain-final-response"
	terminalDrainFirstToolCallID  = "terminal-drain-first-call"
	terminalDrainSecondToolCallID = "terminal-drain-second-call"
	terminalDrainFirstToolName    = "terminal_drain_first"
	terminalDrainSecondToolName   = "terminal_drain_second"
)

func terminalDrainProviderEvents() []messages.StreamMessage {
	return []messages.StreamMessage{{Type: messages.StreamTypeSessionOpen, Value: messages.NewSessionOpenValue("terminal-drain-session", "test")}, {Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()}, {Type: messages.StreamTypeToolCallStart, Value: messages.NewToolCallStartValue(terminalDrainFirstToolCallID, terminalDrainFirstToolName)}, {Type: messages.StreamTypeToolCallDelta, ToolCallId: terminalDrainFirstToolCallID, Value: messages.NewToolCallDeltaValue(`{"value":"first"}`)}, {Type: messages.StreamTypeToolCallEnd, ToolCallId: terminalDrainFirstToolCallID, Value: messages.NewToolCallEndValue(terminalDrainFirstToolCallID, terminalDrainFirstToolName, `{"value":"first"}`)}, {Type: messages.StreamTypeToolCallStart, Value: messages.NewToolCallStartValue(terminalDrainSecondToolCallID, terminalDrainSecondToolName)}, {Type: messages.StreamTypeToolCallDelta, ToolCallId: terminalDrainSecondToolCallID, Value: messages.NewToolCallDeltaValue(`{"value":"second"}`)}, {Type: messages.StreamTypeToolCallEnd, ToolCallId: terminalDrainSecondToolCallID, Value: messages.NewToolCallEndValue(terminalDrainSecondToolCallID, terminalDrainSecondToolName, `{"value":"second"}`)}, {Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})}, {Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, ResponseID: terminalDrainFinalResponseID, Value: messages.NewMessageStartValue()}, {Type: messages.StreamTypeAudioStart, Role: messages.RoleAssistant, ResponseID: terminalDrainFinalResponseID, Value: messages.NewAudioStartValue()}, {Type: messages.StreamTypeAudioEnd, Role: messages.RoleAssistant, ResponseID: terminalDrainFinalResponseID, Value: messages.NewAudioEndValue()}, {Type: messages.StreamTypeTextStart, Role: messages.RoleAssistant, ResponseID: terminalDrainFinalResponseID, Value: messages.NewTextStartValue()}, {Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, ResponseID: terminalDrainFinalResponseID, Value: messages.NewTextDeltaValue(terminalDrainFinalResponseID)}, {Type: messages.StreamTypeTextEnd, Role: messages.RoleAssistant, ResponseID: terminalDrainFinalResponseID, Value: messages.NewTextEndValue()}, {Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, ResponseID: terminalDrainFinalResponseID, Value: messages.NewMessageEndValue(messages.TokenUsage{})}, {Type: messages.StreamTypeSessionClose, Value: messages.NewSessionCloseValue("terminal-drain-session", "test complete")}}
}
func terminalDrainEventForMessage(msg messages.StreamMessage) (string, bool) {
	if msg.Role == messages.RoleTool {
		return "", false
	}
	toolName, content := "", ""
	switch value := msg.Value.(type) {
	case *messages.ToolCallStartValue:
		toolName = value.Name
	case *messages.ToolCallEndValue:
		toolName = value.Name
	case *messages.TextDeltaValue:
		content = value.Content
	}
	if _, ok := map[messages.StreamMessageType]struct{}{messages.StreamTypeSessionOpen: {}, messages.StreamTypeMessageStart: {}, messages.StreamTypeToolCallStart: {}, messages.StreamTypeToolCallDelta: {}, messages.StreamTypeToolCallEnd: {}, messages.StreamTypeMessageEnd: {}, messages.StreamTypeAudioStart: {}, messages.StreamTypeAudioEnd: {}, messages.StreamTypeTextStart: {}, messages.StreamTypeTextDelta: {}, messages.StreamTypeTextEnd: {}, messages.StreamTypeSessionClose: {}}[msg.Type]; !ok {
		return "", false
	}
	return fmt.Sprintf("%v|%v|%s|%s|%s", msg.Type, msg.Role, msg.ResponseID, msg.ToolCallId, toolName+content), true
}

type terminalDrainExternalScenario struct {
	registry        *devicegw.SimulatedDuplexRegistry
	request         agentruntime.RTCDeviceBindingRequest
	providerMedia   *audio.SessionMedia
	barrierInbound  *terminalDrainExternalInbound
	provider        *terminalDrainExternalSession
	toolExecutor    *terminalDrainToolExecutor
	admittedSamples int
	admittedPCM     []int16
	providerSamples int
	streamEvents    []string
}

func newTerminalDrainExternalScenario(t *testing.T) *terminalDrainExternalScenario {
	s := &terminalDrainExternalScenario{}
	registry, err := devicegw.NewSimulatedDuplexRegistry(devicegw.DuplexScenario{
		Seed: 53, Render: devicegw.ClockSpec{NominalRate: audio.SampleRate, Quanta: []int{audio.FrameSize}},
		Capture: devicegw.ClockSpec{NominalRate: audio.SampleRate, Quanta: []int{audio.FrameSize}},
	})
	if err != nil {
		t.Fatalf("new callback-clocked registry: %v", err)
	}
	s.registry = registry
	holdTone := audio.DefaultHoldToneConfig()
	holdTone.GapThreshold = time.Hour
	s.request = agentruntime.RTCDeviceBindingRequest{
		Registry: registry, OutputPresent: true, OutputDevice: "", OutputSampleRate: terminalDrainProviderRate,
		HoldToneConfig: &holdTone, PlaybackSamplesObserver: func(_ context.Context, _ int, samples []int16) error {
			s.record(samples)
			return registry.Advance(1)
		},
	}
	s.providerMedia = audio.NewSessionMediaAtRate(nil, terminalDrainProviderRate)
	s.barrierInbound = &terminalDrainExternalInbound{InboundMedia: s.providerMedia.Endpoints().Inbound,
		drainStarted: make(chan struct{}), readReady: make(chan struct{}), readClosed: make(chan struct{})}
	s.provider = &terminalDrainExternalSession{runtimeRTCSession: &runtimeRTCSession{
		recv: messages.NewTypedBuffer[messages.StreamMessage](32), done: make(chan struct{}),
		media: audio.MediaEndpoints{Inbound: s.barrierInbound, Outbound: s.providerMedia.Endpoints().Outbound},
	}, continuationRequested: make(chan struct{}), closeStarted: make(chan struct{}), releaseClose: make(chan struct{})}
	s.toolExecutor = &terminalDrainToolExecutor{}
	events := terminalDrainProviderEvents()
	for _, event := range events[:9] {
		if !s.provider.recv.Write(context.Background(), event) {
			t.Fatalf("provider event %s did not enter receive buffer", event.Type)
		}
	}
	go s.provider.writeContinuation(events[9:])
	t.Cleanup(func() { s.cleanup(t) })
	return s
}
func (s *terminalDrainExternalScenario) observeStream(msg messages.StreamMessage) {
	event, ok := terminalDrainEventForMessage(msg)
	if !ok {
		return
	}
	s.streamEvents = append(s.streamEvents, event)
}
func TestRTCDeviceBoundSessionTerminalDrainPreservesAcceptedProviderAudio(t *testing.T) {
	cancelOnTerminal := os.Getenv("C64_CANCEL_ON_TERMINAL") == "1"
	s := newTerminalDrainExternalScenario(t)
	if !cancelOnTerminal {
		s.barrierInbound.releaseRead()
	}
	samples := make([]int16, terminalDrainProviderSamples)
	for index := range samples {
		samples[index] = int16(index%257 - 128)
	}
	s.push(t, samples)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runErr := make(chan error, 1)
	go func() {
		runErr <- agentruntime.RunSession(ctx, io.Discard, agentruntime.SessionRunOptions{
			ModelCatalog: testModelCatalog(), Provider: "grok", Model: "test-model", APIKey: "test-key", WaitForClose: true, BareLive: true,
			SessionInferencer: &terminalDrainExternalInferencer{session: s.provider}, RTCDeviceBinding: s.request,
			ToolExecutor: s.toolExecutor, ToolDefinitions: []messages.ToolDefinition{{Name: terminalDrainFirstToolName}, {Name: terminalDrainSecondToolName}},
			StreamObserver: func(msg messages.StreamMessage) {
				s.observeStream(msg)
				if cancelOnTerminal && msg.Type == messages.StreamTypeMessageEnd && msg.ResponseID == "" {
					cancel()
				}
			},
		})
	}()
	phase := s.releaseRun(t, runErr)
	select {
	case err := <-runErr:
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("run session (%s): %v", phase, err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("run session (%s) did not complete", phase)
	}
	if cancelOnTerminal {
		s.assertAcceptedSourceFailure(t, phase, samples)
		return
	}
	s.assertContinuationSequence(t)
	s.assertOutput(t, phase, samples)
}
func (s *terminalDrainExternalScenario) record(samples []int16) {
	s.admittedSamples += len(samples)
	s.admittedPCM = append(s.admittedPCM, samples...)
}
func (s *terminalDrainExternalScenario) push(t *testing.T, samples []int16) {
	if err := s.providerMedia.PushInbound(samples); err != nil {
		t.Fatalf("push provider response: %v", err)
	}
	if err := s.providerMedia.FlushInbound(); err != nil {
		t.Fatalf("flush provider response: %v", err)
	}
	s.providerSamples += len(samples)
}
func (s *terminalDrainExternalScenario) releaseRun(t *testing.T, runErr <-chan error) string {
	select {
	case <-s.provider.closeStarted:
		s.provider.releaseOnce.Do(func() { close(s.provider.releaseClose) })
		return "provider-close-before-drain"
	case <-s.barrierInbound.drainStarted:
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
func (s *terminalDrainExternalScenario) assertContinuationSequence(t *testing.T) {
	want := make([]string, 0, len(terminalDrainProviderEvents()))
	for _, msg := range terminalDrainProviderEvents() {
		event, _ := terminalDrainEventForMessage(msg)
		want = append(want, event)
	}
	got := append([]string(nil), s.streamEvents...)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("terminal drain provider sequence = %#v, want %#v", got, want)
	}
	calls := s.toolExecutor.callsSnapshot()
	if len(calls) != 2 || !((calls[0].ID == terminalDrainFirstToolCallID && calls[1].ID == terminalDrainSecondToolCallID) || (calls[0].ID == terminalDrainSecondToolCallID && calls[1].ID == terminalDrainFirstToolCallID)) {
		t.Fatalf("terminal drain tool calls = %#v, want both two-tool continuation calls", calls)
	}
	t.Logf("C64_SEQUENCE_EVIDENCE provider_events=%d tool_calls=%d continuation=two-tool final_response=%s order=tool-turn-before-final-audio", len(want), len(calls), terminalDrainFinalResponseID)
}
func (s *terminalDrainExternalScenario) assertAcceptedSourceFailure(t *testing.T, phase string, samples []int16) {
	providerSamples, admitted := s.providerSamples, s.admittedSamples
	readSamples := s.barrierInbound.samplesRead()
	renderedPCM := s.registry.RenderedSamples()
	stats := s.registry.PlaybackStats()
	consumed := stats.RenderedSamples - stats.UnderflowSamples
	t.Logf("C64_ACCEPTED_SOURCE_FAILURE provider_samples=%d admitted_samples=%d consumed_samples=%d rendered_samples=%d queued_samples=%d underflow_samples=%d callback_count=%d shutdown=provider-close", providerSamples, admitted, consumed, len(renderedPCM), stats.QueuedSamples, stats.UnderflowSamples, stats.CallbackCount)
	if phase != "provider-close-before-drain" || providerSamples != len(samples) || readSamples != 0 || admitted != 0 || consumed != 0 || len(renderedPCM) != 0 || stats.QueuedSamples != 0 {
		t.Fatalf("accepted source control did not reproduce provider-close-before-drain: phase=%s provider=%d read=%d admitted=%d consumed=%d rendered=%d queued=%d stats=%+v", phase, providerSamples, readSamples, admitted, consumed, len(renderedPCM), stats.QueuedSamples, stats)
	}
	t.Fatalf("accepted source incorrectly passed terminal drain control: phase=%s provider=%d admitted=%d consumed=%d rendered=%d queued=%d", phase, providerSamples, admitted, consumed, len(renderedPCM), stats.QueuedSamples)
}
func (s *terminalDrainExternalScenario) assertOutput(t *testing.T, phase string, samples []int16) {
	got, gotPCM, pushed := s.admittedSamples, append([]int16(nil), s.admittedPCM...), s.providerSamples
	if phase != "drain-before-provider-close" {
		stats := s.registry.PlaybackStats()
		t.Logf("C64_ACCEPTED_SOURCE_FAILURE provider_samples=%d admitted_samples=%d consumed_samples=%d rendered_samples=%d queued_samples=%d underflow_samples=%d callback_count=%d shutdown=%s", s.barrierInbound.samplesRead(), got, stats.RenderedSamples-stats.UnderflowSamples, len(s.registry.RenderedSamples()), stats.QueuedSamples, stats.UnderflowSamples, stats.CallbackCount, phase)
		t.Fatalf("terminal drain phase = %s, want drain-before-provider-close", phase)
	}
	if got != terminalDrainDeviceSamples {
		t.Fatalf("terminal drain admitted %d device samples, want exact %d", got, terminalDrainDeviceSamples)
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
		t.Fatalf("terminal drain changed admitted PCM: got %d samples, want exact resampled block", len(gotPCM))
	}
	renderedPCM := s.registry.RenderedSamples()
	stats := s.registry.PlaybackStats()
	if pushed != len(samples) || s.barrierInbound.samplesRead() != len(samples) {
		t.Fatalf("terminal drain provider receipt/read %d/%d samples, want exact %d", pushed, s.barrierInbound.samplesRead(), len(samples))
	}
	assertTerminalDrainRendered(t, phase, wantPCM, renderedPCM, stats)
	consumed := stats.RenderedSamples - stats.UnderflowSamples
	if consumed != uint64(got) || consumed != uint64(len(wantPCM)) || stats.QueuedSamples != 0 {
		t.Fatalf("terminal drain phase %s failed provider/admission/consumption/queue reconciliation: provider=%d admitted=%d consumed=%d rendered=%d queued=%d stats=%+v", phase, s.barrierInbound.samplesRead(), got, consumed, len(renderedPCM), stats.QueuedSamples, stats)
	}
	if stats.DroppedSamples != 0 || stats.OverflowEvents != 0 || stats.DiscardedSamples != 0 || stats.DiscardEvents != 0 {
		t.Fatalf("terminal drain phase %s reported playback loss: %+v", phase, stats)
	}
	select {
	case <-s.provider.done:
	default:
		t.Fatalf("terminal drain phase %s returned before provider shutdown", phase)
	}
	t.Logf("C64_RENDER_EVIDENCE provider_samples=%d admitted_samples=%d consumed_samples=%d rendered_samples=%d queued_samples=%d underflow_samples=%d callback_count=%d shutdown=complete", s.barrierInbound.samplesRead(), got, consumed, len(renderedPCM), stats.QueuedSamples, stats.UnderflowSamples, stats.CallbackCount)
}
func assertTerminalDrainRendered(t *testing.T, phase string, wantPCM, renderedPCM []int16, stats audio.PlaybackQueueStats) {
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
	s.provider.releaseOnce.Do(func() { close(s.provider.releaseClose) })
	if err := s.providerMedia.Close(); err != nil {
		t.Errorf("cleanup provider media: %v", err)
	}
}

type terminalDrainExternalInbound struct {
	audio.InboundMedia
	drainStarted, readReady, readClosed chan struct{}
	closeOnce                           sync.Once
	readSamples                         int
}

func (m *terminalDrainExternalInbound) ReadFrame(ctx context.Context) (audio.PCMFrame, error) {
	select {
	case <-m.readReady:
	case <-m.readClosed:
		return audio.PCMFrame{}, audio.ErrSessionMediaClosed
	case <-ctx.Done():
		return audio.PCMFrame{}, ctx.Err()
	}
	frame, err := m.InboundMedia.ReadFrame(ctx)
	if err == nil {
		m.readSamples += len(frame.Samples)
	}
	return frame, err
}
func (m *terminalDrainExternalInbound) releaseRead()     { close(m.readReady) }
func (m *terminalDrainExternalInbound) samplesRead() int { return m.readSamples }
func (m *terminalDrainExternalInbound) Close() error {
	m.closeOnce.Do(func() { close(m.drainStarted); close(m.readClosed) })
	return m.InboundMedia.Close()
}

type terminalDrainExternalInferencer struct{ session messages.Session }

func (i *terminalDrainExternalInferencer) Request() inference.SessionRequest {
	return inference.SessionRequest{Config: models.SessionConfig{InputAudioSampleRate: models.SampleRate(terminalDrainProviderRate), OutputAudioSampleRate: models.SampleRate(terminalDrainProviderRate)}}
}
func (i *terminalDrainExternalInferencer) ConnectSession(context.Context) (messages.Session, error) {
	return i.session, nil
}

type terminalDrainExternalSession struct {
	*runtimeRTCSession
	continuationRequested, closeStarted, releaseClose chan struct{}
	continuationOnce, closeOnce, releaseOnce          sync.Once
}

func (s *terminalDrainExternalSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	if msg.Type == messages.StreamTypeResponseCreate {
		s.continuationOnce.Do(func() { close(s.continuationRequested) })
	}
	return s.runtimeRTCSession.Send(ctx, msg)
}
func (s *terminalDrainExternalSession) writeContinuation(events []messages.StreamMessage) {
	<-s.continuationRequested
	for _, event := range events {
		if !s.recv.Write(context.Background(), event) {
			return
		}
	}
}
func (s *terminalDrainExternalSession) Close() error {
	var mediaErr error
	s.closeOnce.Do(func() {
		close(s.closeStarted)
		<-s.releaseClose
		mediaErr = errors.Join(s.media.Inbound.Close(), s.media.Outbound.Close())
		_ = s.runtimeRTCSession.Close()
	})
	return mediaErr
}

type terminalDrainToolExecutor struct {
	mu    sync.Mutex
	calls []messages.ToolCall
}

func (e *terminalDrainToolExecutor) Execute(_ context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	e.mu.Lock()
	e.calls = append(e.calls, call)
	e.mu.Unlock()
	return messages.ToolCallResponse{ToolCallID: call.ID, Name: call.Name, Content: "terminal-drain-tool-result"}, nil
}
func (e *terminalDrainToolExecutor) callsSnapshot() []messages.ToolCall {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]messages.ToolCall(nil), e.calls...)
}
