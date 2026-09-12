package agentruntime_test

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	agentruntime "github.com/portpowered/go-agent-harness/agent-cli/internal/services/internal/agentruntime"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/inference"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
)

const (
	terminalDrainFinalResponseID  = "terminal-drain-final-response"
	terminalDrainFirstToolCallID  = "terminal-drain-first-call"
	terminalDrainSecondToolCallID = "terminal-drain-second-call"
	terminalDrainFirstToolName    = "terminal_drain_first"
	terminalDrainSecondToolName   = "terminal_drain_second"
)

type terminalDrainStreamEvent struct {
	Type       messages.StreamMessageType
	Role       messages.Role
	ResponseID string
	ToolCallID string
	ToolName   string
	Content    string
}

func terminalDrainProviderEvents() []messages.StreamMessage {
	return []messages.StreamMessage{
		{Type: messages.StreamTypeSessionOpen, Value: messages.NewSessionOpenValue("terminal-drain-session", "test")},
		{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()},
		{Type: messages.StreamTypeToolCallStart, Value: messages.NewToolCallStartValue(terminalDrainFirstToolCallID, terminalDrainFirstToolName)},
		{Type: messages.StreamTypeToolCallDelta, ToolCallId: terminalDrainFirstToolCallID, Value: messages.NewToolCallDeltaValue(`{"value":"first"}`)},
		{Type: messages.StreamTypeToolCallEnd, ToolCallId: terminalDrainFirstToolCallID, Value: messages.NewToolCallEndValue(terminalDrainFirstToolCallID, terminalDrainFirstToolName, `{"value":"first"}`)},
		{Type: messages.StreamTypeToolCallStart, Value: messages.NewToolCallStartValue(terminalDrainSecondToolCallID, terminalDrainSecondToolName)},
		{Type: messages.StreamTypeToolCallDelta, ToolCallId: terminalDrainSecondToolCallID, Value: messages.NewToolCallDeltaValue(`{"value":"second"}`)},
		{Type: messages.StreamTypeToolCallEnd, ToolCallId: terminalDrainSecondToolCallID, Value: messages.NewToolCallEndValue(terminalDrainSecondToolCallID, terminalDrainSecondToolName, `{"value":"second"}`)},
		{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
		{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, ResponseID: terminalDrainFinalResponseID, Value: messages.NewMessageStartValue()},
		{Type: messages.StreamTypeAudioStart, Role: messages.RoleAssistant, ResponseID: terminalDrainFinalResponseID, Value: messages.NewAudioStartValue()},
		{Type: messages.StreamTypeAudioEnd, Role: messages.RoleAssistant, ResponseID: terminalDrainFinalResponseID, Value: messages.NewAudioEndValue()},
		{Type: messages.StreamTypeTextStart, Role: messages.RoleAssistant, ResponseID: terminalDrainFinalResponseID, Value: messages.NewTextStartValue()},
		{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, ResponseID: terminalDrainFinalResponseID, Value: messages.NewTextDeltaValue("terminal-drain-final-response")},
		{Type: messages.StreamTypeTextEnd, Role: messages.RoleAssistant, ResponseID: terminalDrainFinalResponseID, Value: messages.NewTextEndValue()},
		{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, ResponseID: terminalDrainFinalResponseID, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
		{Type: messages.StreamTypeSessionClose, Value: messages.NewSessionCloseValue("terminal-drain-session", "test complete")},
	}
}

func terminalDrainEventForMessage(msg messages.StreamMessage) (terminalDrainStreamEvent, bool) {
	if msg.Role == messages.RoleTool {
		return terminalDrainStreamEvent{}, false
	}
	event := terminalDrainStreamEvent{Type: msg.Type, Role: msg.Role, ResponseID: msg.ResponseID, ToolCallID: msg.ToolCallId}
	switch value := msg.Value.(type) {
	case *messages.ToolCallStartValue:
		event.ToolCallID, event.ToolName = value.ToolCallID, value.Name
	case *messages.ToolCallEndValue:
		event.ToolCallID, event.ToolName = value.ToolCallID, value.Name
	case *messages.TextDeltaValue:
		event.Content = value.Content
	}
	switch msg.Type {
	case messages.StreamTypeSessionOpen, messages.StreamTypeMessageStart, messages.StreamTypeToolCallStart,
		messages.StreamTypeToolCallDelta, messages.StreamTypeToolCallEnd, messages.StreamTypeMessageEnd,
		messages.StreamTypeAudioStart, messages.StreamTypeAudioEnd, messages.StreamTypeTextStart,
		messages.StreamTypeTextDelta, messages.StreamTypeTextEnd, messages.StreamTypeSessionClose:
		return event, true
	default:
		return terminalDrainStreamEvent{}, false
	}
}

func (s *terminalDrainExternalScenario) observeStream(msg messages.StreamMessage) {
	event, ok := terminalDrainEventForMessage(msg)
	if !ok {
		return
	}
	s.streamMu.Lock()
	s.streamEvents = append(s.streamEvents, event)
	s.streamMu.Unlock()
}

func (s *terminalDrainExternalScenario) streamSnapshot() []terminalDrainStreamEvent {
	s.streamMu.Lock()
	defer s.streamMu.Unlock()
	return append([]terminalDrainStreamEvent(nil), s.streamEvents...)
}

func (s *terminalDrainExternalScenario) assertContinuationSequence(t *testing.T) {
	t.Helper()
	got := s.streamSnapshot()
	wantMessages := terminalDrainProviderEvents()
	want := make([]terminalDrainStreamEvent, 0, len(wantMessages))
	for _, msg := range wantMessages {
		event, ok := terminalDrainEventForMessage(msg)
		if !ok {
			t.Fatalf("provider event %s was not representable in the sequence evidence", msg.Type)
		}
		want = append(want, event)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("terminal drain provider sequence = %#v, want %#v", got, want)
	}
	calls := s.toolExecutor.callsSnapshot()
	if len(calls) != 2 {
		t.Fatalf("terminal drain tool calls = %#v, want two-tool continuation", calls)
	}
	callIDs := map[string]bool{}
	for _, call := range calls {
		callIDs[call.ID] = true
	}
	if !callIDs[terminalDrainFirstToolCallID] || !callIDs[terminalDrainSecondToolCallID] {
		t.Fatalf("terminal drain tool calls = %#v, want both continuation calls", calls)
	}
	t.Logf("C64_SEQUENCE_EVIDENCE provider_events=%d tool_calls=%d continuation=two-tool final_response=%s order=tool-turn-before-final-audio", len(got), len(calls), terminalDrainFinalResponseID)
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
	s.admittedMu.Lock()
	s.providerSamples += len(samples)
	s.admittedMu.Unlock()
}

func (s *terminalDrainExternalScenario) releaseRun(t *testing.T, runErr <-chan error) string {
	t.Helper()
	select {
	case <-s.provider.closeStarted:
		s.provider.releaseOnce.Do(func() { close(s.provider.releaseClose) })
		return "provider-close-before-drain"
	case <-s.barrierInbound.drainStarted:
		s.gate.releaseGate()
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

func (s *terminalDrainExternalScenario) assertAcceptedSourceFailure(t *testing.T, phase string, samples []int16) {
	t.Helper()
	s.admittedMu.Lock()
	admitted := s.admittedSamples
	providerSamples := s.providerSamples
	s.admittedMu.Unlock()
	readSamples := s.barrierInbound.samplesRead()
	renderedPCM := s.registry.RenderedSamples()
	stats := s.registry.PlaybackStats()
	consumed := stats.RenderedSamples - stats.UnderflowSamples
	t.Logf("C64_ACCEPTED_SOURCE_FAILURE provider_samples=%d admitted_samples=%d consumed_samples=%d rendered_samples=%d queued_samples=%d underflow_samples=%d callback_count=%d shutdown=provider-close", providerSamples, admitted, consumed, len(renderedPCM), stats.QueuedSamples, stats.UnderflowSamples, stats.CallbackCount)
	if phase != "provider-close-before-drain" || providerSamples != len(samples) || readSamples != 0 || admitted != 0 || consumed != 0 || len(renderedPCM) != 0 || stats.QueuedSamples != 0 {
		t.Fatalf("accepted source control did not reproduce provider-close-before-drain: phase=%s provider=%d read=%d admitted=%d consumed=%d rendered=%d queued=%d stats=%+v", phase, providerSamples, readSamples, admitted, consumed, len(renderedPCM), stats.QueuedSamples, stats)
	}
	t.Fatalf("accepted source incorrectly passed terminal drain control: phase=%s provider=%d read=%d admitted=%d consumed=%d rendered=%d queued=%d", phase, providerSamples, readSamples, admitted, consumed, len(renderedPCM), stats.QueuedSamples)
}

func (s *terminalDrainExternalScenario) assertOutput(t *testing.T, phase string, samples []int16) {
	t.Helper()
	s.admittedMu.Lock()
	got := s.admittedSamples
	gotPCM := append([]int16(nil), s.admittedPCM...)
	pushedSamples := s.providerSamples
	s.admittedMu.Unlock()
	if phase != "drain-before-provider-close" {
		providerSamples := s.barrierInbound.samplesRead()
		stats := s.registry.PlaybackStats()
		t.Logf("C64_ACCEPTED_SOURCE_FAILURE provider_samples=%d admitted_samples=%d consumed_samples=%d rendered_samples=%d queued_samples=%d underflow_samples=%d callback_count=%d shutdown=%s", providerSamples, got, stats.RenderedSamples-stats.UnderflowSamples, len(s.registry.RenderedSamples()), stats.QueuedSamples, stats.UnderflowSamples, stats.CallbackCount, phase)
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
	if pushedSamples != len(samples) || providerSamples != len(samples) {
		t.Fatalf("terminal drain phase %s provider receipt/read %d/%d samples, want exact %d", phase, pushedSamples, providerSamples, len(samples))
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
	started     chan struct{}
	release     chan struct{}
	record      func([]int16)
	advance     func() error
	startOnce   sync.Once
	releaseOnce sync.Once
}

func (g *terminalDrainExternalGate) releaseGate() {
	g.releaseOnce.Do(func() {
		select {
		case <-g.release:
		default:
			close(g.release)
		}
	})
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
	readReady    chan struct{}
	readClosed   chan struct{}
	closeOnce    sync.Once
	readOnce     sync.Once
	closedOnce   sync.Once
	readMu       sync.Mutex
	readSamples  int
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
		m.readMu.Lock()
		m.readSamples += len(frame.Samples)
		m.readMu.Unlock()
	}
	return frame, err
}

func (m *terminalDrainExternalInbound) releaseRead() {
	m.readOnce.Do(func() { close(m.readReady) })
}

func (m *terminalDrainExternalInbound) samplesRead() int {
	m.readMu.Lock()
	defer m.readMu.Unlock()
	return m.readSamples
}

func (m *terminalDrainExternalInbound) Close() error {
	m.closeOnce.Do(func() {
		close(m.drainStarted)
		m.closedOnce.Do(func() { close(m.readClosed) })
	})
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
	receive               *messages.TypedBuffer[messages.StreamMessage]
	done                  chan struct{}
	media                 audio.MediaEndpoints
	continuationRequested chan struct{}

	closeStarted     chan struct{}
	releaseClose     chan struct{}
	continuationOnce sync.Once
	closeOnce        sync.Once
	releaseOnce      sync.Once
	doneOnce         sync.Once
}

func (s *terminalDrainExternalSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	select {
	case <-ctx.Done():
		return false
	default:
		if msg.Type == messages.StreamTypeResponseCreate {
			s.continuationOnce.Do(func() { close(s.continuationRequested) })
		}
		return true
	}
}

func (s *terminalDrainExternalSession) writeContinuation(events []messages.StreamMessage) {
	<-s.continuationRequested
	for _, event := range events {
		if !s.receive.Write(context.Background(), event) {
			return
		}
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
