package causal_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	live "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/internal/live"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

const boundarySamples = 6400

type boundaryEvent struct {
	Sequence     int    `json:"sequence"`
	Boundary     string `json:"boundary"`
	SampleStart  int    `json:"sample_start"`
	SampleEnd    int    `json:"sample_end"`
	TimingDomain string `json:"timing_domain"`
	ElapsedNanos int64  `json:"elapsed_nanos"`
}

type boundaryTrace struct {
	started time.Time
	mu      sync.Mutex
	events  []boundaryEvent
}

func (t *boundaryTrace) record(boundary string, start, end int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.events = append(t.events, boundaryEvent{
		Sequence: len(t.events) + 1, Boundary: boundary, SampleStart: start, SampleEnd: end,
		TimingDomain: "monotonic-process", ElapsedNanos: time.Since(t.started).Nanoseconds(),
	})
}

func (t *boundaryTrace) snapshot() []boundaryEvent {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]boundaryEvent(nil), t.events...)
}

type providerSession struct {
	media       *sharedaudio.SessionMedia
	receive     *messages.TypedBuffer[messages.StreamMessage]
	done        chan struct{}
	closeOnce   sync.Once
	mu          sync.Mutex
	trace       *boundaryTrace
	receiveSeen chan struct{}
	receiveOnce sync.Once
}

func newProviderSession(trace *boundaryTrace) *providerSession {
	return &providerSession{
		media:   sharedaudio.NewSessionMediaAtRate(nil, 24000),
		receive: messages.NewTypedBuffer[messages.StreamMessage](32),
		done:    make(chan struct{}), trace: trace, receiveSeen: make(chan struct{}),
	}
}

func (p *providerSession) Send(context.Context, messages.StreamMessage) bool { return true }
func (p *providerSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	p.receiveOnce.Do(func() { close(p.receiveSeen) })
	p.trace.record("provider_receipt", 0, boundarySamples)
	return p.receive
}
func (p *providerSession) Done() <-chan struct{} { return p.done }
func (p *providerSession) Close() error {
	p.closeOnce.Do(func() { close(p.done) })
	return nil
}
func (p *providerSession) RTCMedia() sharedaudio.MediaEndpoints {
	p.mu.Lock()
	first := len(p.trace.snapshot()) == 0
	p.mu.Unlock()
	if first {
		p.trace.record("rtc_forwarding", 0, boundarySamples)
	}
	return p.media.Endpoints()
}

type providerInferencer struct{ provider *providerSession }

func (i providerInferencer) ConnectSession(context.Context) (messages.Session, error) {
	return i.provider, nil
}

func terminalMessage() messages.StreamMessage {
	return messages.StreamMessage{
		Type:       messages.StreamTypeSessionClose,
		Role:       messages.RoleAssistant,
		ResponseID: "terminal-drain-response",
		Value:      messages.NewSessionCloseValue("c127", "fixture_complete"),
	}
}

func samples() []int16 {
	pcm := make([]int16, boundarySamples)
	for index := range pcm {
		pcm[index] = int16(index%257 - 128)
	}
	return pcm
}

func waitForProviderReceive(t *testing.T, provider *providerSession) {
	t.Helper()
	select {
	case <-provider.receiveSeen:
	case <-time.After(time.Second):
		t.Fatal("provider Receive was not called")
	}
}

func assertTrace(t *testing.T, trace *boundaryTrace) string {
	t.Helper()
	want := []string{"rtc_forwarding", "provider_receipt", "sink_admission", "device_render", "response_terminal", "graceful_drain"}
	events := trace.snapshot()
	if len(events) != len(want) {
		t.Fatalf("ordered boundary events = %#v, want %v", events, want)
	}
	for index, event := range events {
		if event.Sequence != index+1 || event.Boundary != want[index] || event.TimingDomain != "monotonic-process" {
			t.Fatalf("ordered boundary event %d = %#v, want sequence/boundary %d/%s", index, event, index+1, want[index])
		}
		if event.SampleStart < 0 || event.SampleEnd < event.SampleStart || event.SampleEnd > boundarySamples {
			t.Fatalf("ordered boundary event %d has invalid sample range: %#v", index, event)
		}
	}
	encoded, err := json.Marshal(events)
	if err != nil {
		t.Fatalf("marshal ordered boundary events: %v", err)
	}
	return string(encoded)
}

func openHandle(t *testing.T, provider *providerSession) session.LiveHandle {
	t.Helper()
	service := live.New(live.Dependencies{InferencerFactory: func(context.Context, session.LiveRequest) (messages.SessionInferencer, error) {
		return providerInferencer{provider: provider}, nil
	}})
	handle, err := service.OpenLive(t.Context(), session.LiveRequest{SessionID: "c127", OutputAudioContinuous: true})
	if err != nil {
		t.Fatalf("OpenLive: %v", err)
	}
	t.Cleanup(func() {
		if err := handle.Close(); err != nil {
			t.Errorf("close live handle: %v", err)
		}
		if err := provider.media.Close(); err != nil {
			t.Errorf("close provider media: %v", err)
		}
	})
	if err := handle.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForProviderReceive(t, provider)
	return handle
}

func admitAndRender(t *testing.T, handle session.LiveHandle, provider *providerSession, trace *boundaryTrace, pcm []int16) {
	t.Helper()
	if err := provider.media.PushInbound(pcm); err != nil {
		t.Fatalf("admit provider PCM: %v", err)
	}
	if err := provider.media.FlushInbound(); err != nil {
		t.Fatalf("flush provider PCM: %v", err)
	}
	trace.record("sink_admission", 0, len(pcm))
	readContext, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	read := 0
	for read < len(pcm) {
		frame, err := handle.Media().Inbound.ReadFrame(readContext)
		if err != nil {
			t.Fatalf("render input frame after admission (%d samples): %v", read, err)
		}
		read += len(frame.Samples)
	}
	if read != len(pcm) {
		t.Fatalf("rendered sample count = %d, want %d", read, len(pcm))
	}
	trace.record("device_render", 0, read)
}

func closeAfterTerminal(t *testing.T, handle session.LiveHandle, provider *providerSession, trace *boundaryTrace, sampleCount int) {
	t.Helper()
	if !provider.receive.Write(t.Context(), terminalMessage()) {
		t.Fatal("provider response terminal was not admitted")
	}
	if err := provider.Close(); err != nil {
		t.Fatalf("close provider: %v", err)
	}
	terminalDeadline := time.NewTimer(time.Second)
	defer terminalDeadline.Stop()
	for {
		select {
		case event := <-handle.Events():
			if event.Kind == string(session.LiveEventTerminal) {
				trace.record("response_terminal", sampleCount, sampleCount)
				goto terminalObserved
			}
		case <-terminalDeadline.C:
			t.Fatal("live terminal event was not published")
		}
	}
terminalObserved:
	if err := handle.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	trace.record("graceful_drain", sampleCount, sampleCount)
}

func TestTerminalDrainOrderedBoundaryTrace(t *testing.T) {
	trace := &boundaryTrace{started: time.Now()}
	provider := newProviderSession(trace)
	handle := openHandle(t, provider)
	pcm := samples()
	admitAndRender(t, handle, provider, trace, pcm)
	closeAfterTerminal(t, handle, provider, trace, len(pcm))
	t.Logf("C127_ORDERED_BOUNDARY_TRACE events=%s", assertTrace(t, trace))
}
