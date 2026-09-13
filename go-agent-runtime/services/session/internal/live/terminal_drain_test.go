package live

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

type terminalDrainBoundaryEvent struct {
	Sequence     int    `json:"sequence"`
	Boundary     string `json:"boundary"`
	SampleStart  int    `json:"sample_start"`
	SampleEnd    int    `json:"sample_end"`
	TimingDomain string `json:"timing_domain"`
	ElapsedNanos int64  `json:"elapsed_nanos"`
}

type terminalDrainBoundaryTrace struct {
	started time.Time
	mu      sync.Mutex
	next    int
	events  []terminalDrainBoundaryEvent
}

func newTerminalDrainBoundaryTrace() *terminalDrainBoundaryTrace {
	return &terminalDrainBoundaryTrace{started: time.Now()}
}

func (t *terminalDrainBoundaryTrace) record(boundary string, sampleStart, sampleEnd int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.next++
	t.events = append(t.events, terminalDrainBoundaryEvent{
		Sequence:     t.next,
		Boundary:     boundary,
		SampleStart:  sampleStart,
		SampleEnd:    sampleEnd,
		TimingDomain: "monotonic-process",
		ElapsedNanos: time.Since(t.started).Nanoseconds(),
	})
}

func (t *terminalDrainBoundaryTrace) snapshot() []terminalDrainBoundaryEvent {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]terminalDrainBoundaryEvent(nil), t.events...)
}

type orderedTerminalDrainSession struct {
	*testSession
	trace   *terminalDrainBoundaryTrace
	media   *sharedaudio.SessionMedia
	mu      sync.Mutex
	claimed bool
}

func newOrderedTerminalDrainSession(trace *terminalDrainBoundaryTrace) *orderedTerminalDrainSession {
	return &orderedTerminalDrainSession{
		testSession: newTestSession(),
		trace:       trace,
		media:       sharedaudio.NewSessionMediaAtRate(nil, 24000),
	}
}

func (s *orderedTerminalDrainSession) RTCMedia() sharedaudio.MediaEndpoints {
	s.mu.Lock()
	s.claimed = true
	s.mu.Unlock()
	s.trace.record("rtc_forwarding", 0, terminalDrainBoundarySamples)
	return s.media.Endpoints()
}

func (s *orderedTerminalDrainSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	s.mu.Lock()
	claimed := s.claimed
	s.mu.Unlock()
	s.trace.record("provider_receipt", 0, terminalDrainBoundarySamples)
	if !claimed {
		s.trace.record("provider_media_release", 0, terminalDrainBoundarySamples)
	}
	return s.testSession.Receive()
}

const terminalDrainBoundarySamples = 6400

func openOrderedTerminalDrain(t *testing.T, provider *orderedTerminalDrainSession) (messages.Session, *terminalDrainSession) {
	t.Helper()
	connected, err := (terminalDrainInferencer{inner: &testInferencer{session: provider}}).ConnectSession(context.Background())
	if err != nil {
		t.Fatalf("connect terminal-drain session: %v", err)
	}
	terminal, ok := connected.(*terminalDrainSession)
	if !ok {
		t.Fatalf("connected session = %T, want *terminalDrainSession", connected)
	}
	t.Cleanup(func() {
		if err := connected.Close(); err != nil {
			t.Errorf("close terminal-drain session: %v", err)
		}
		if err := provider.media.Close(); err != nil {
			t.Errorf("close provider media: %v", err)
		}
	})
	return connected, terminal
}

func requireProviderMediaClaimed(t *testing.T, provider *orderedTerminalDrainSession) {
	t.Helper()
	provider.mu.Lock()
	claimed := provider.claimed
	provider.mu.Unlock()
	if !claimed {
		t.Fatalf("provider media admitted during Receive: context deadline exceeded")
	}
}

func terminalDrainSamples() []int16 {
	samples := make([]int16, terminalDrainBoundarySamples)
	for index := range samples {
		samples[index] = int16(index%257 - 128)
	}
	return samples
}

func admitAndRenderTerminalDrain(t *testing.T, provider *orderedTerminalDrainSession, trace *terminalDrainBoundaryTrace, samples []int16) {
	t.Helper()
	if err := provider.media.PushInbound(samples); err != nil {
		t.Fatalf("admit provider PCM: %v", err)
	}
	if err := provider.media.FlushInbound(); err != nil {
		t.Fatalf("flush provider PCM: %v", err)
	}
	trace.record("sink_admission", 0, len(samples))
	readContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	read := 0
	for read < len(samples) {
		frame, err := provider.media.Endpoints().Inbound.ReadFrame(readContext)
		if err != nil {
			t.Fatalf("render input frame after admission (%d samples): %v", read, err)
		}
		read += len(frame.Samples)
	}
	if read != len(samples) {
		t.Fatalf("rendered sample count = %d, want %d", read, len(samples))
	}
	trace.record("device_render", 0, read)
}

func forwardTerminalDrainMessage(t *testing.T, provider *orderedTerminalDrainSession, terminal *terminalDrainSession, trace *terminalDrainBoundaryTrace, sampleCount int) {
	t.Helper()
	terminalMessage := messages.StreamMessage{
		Type:       messages.StreamTypeMessageEnd,
		Role:       messages.RoleAssistant,
		ResponseID: "terminal-drain-response",
		Value:      messages.NewMessageEndValue(messages.TokenUsage{}),
	}
	if !provider.receive.Write(context.Background(), terminalMessage) {
		t.Fatal("provider response terminal was not admitted")
	}
	readContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	forwarded, err := terminal.receive.ReadContext(readContext)
	if err != nil {
		t.Fatalf("read forwarded response terminal: %v", err)
	}
	if forwarded.Type != messages.StreamTypeMessageEnd || forwarded.ResponseID != terminalMessage.ResponseID {
		t.Fatalf("forwarded response terminal = %+v, want %+v", forwarded, terminalMessage)
	}
	trace.record("response_terminal", sampleCount, sampleCount)
}

func assertTerminalDrainEvent(t *testing.T, index int, event terminalDrainBoundaryEvent, want string) {
	t.Helper()
	if event.Sequence != index+1 || event.Boundary != want || event.TimingDomain != "monotonic-process" {
		t.Fatalf("ordered boundary event %d = %#v, want sequence/boundary %d/%s", index, event, index+1, want)
	}
	if event.SampleStart < 0 || event.SampleEnd < event.SampleStart || event.SampleEnd > terminalDrainBoundarySamples {
		t.Fatalf("ordered boundary event %d has invalid sample range: %#v", index, event)
	}
}

func assertTerminalDrainTrace(t *testing.T, trace *terminalDrainBoundaryTrace) string {
	t.Helper()
	events := trace.snapshot()
	wantBoundaries := []string{"rtc_forwarding", "provider_receipt", "sink_admission", "device_render", "response_terminal", "graceful_drain"}
	if len(events) != len(wantBoundaries) {
		t.Fatalf("ordered boundary events = %#v, want %v", events, wantBoundaries)
	}
	for index, want := range wantBoundaries {
		assertTerminalDrainEvent(t, index, events[index], want)
	}
	encoded, err := json.Marshal(events)
	if err != nil {
		t.Fatalf("marshal ordered boundary events: %v", err)
	}
	return string(encoded)
}

func TestTerminalDrainOrderedBoundaryTrace(t *testing.T) {
	trace := newTerminalDrainBoundaryTrace()
	provider := newOrderedTerminalDrainSession(trace)
	connected, terminal := openOrderedTerminalDrain(t, provider)
	requireProviderMediaClaimed(t, provider)
	samples := terminalDrainSamples()
	admitAndRenderTerminalDrain(t, provider, trace, samples)
	forwardTerminalDrainMessage(t, provider, terminal, trace, len(samples))
	if err := connected.Close(); err != nil {
		t.Fatalf("close after terminal: %v", err)
	}
	trace.record("graceful_drain", len(samples), len(samples))
	t.Logf("C127_ORDERED_BOUNDARY_TRACE events=%s", assertTerminalDrainTrace(t, trace))
}
