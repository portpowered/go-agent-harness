package realtime

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
)

const testWait = 2 * time.Second

type readResult struct {
	data []byte
	err  error
}

// fakeConn delivers scripted reads and records writes.
type fakeConn struct {
	reads    chan readResult
	writes   chan []byte
	writeErr error

	closeOnce sync.Once
	closed    chan struct{}
}

func newFakeConn() *fakeConn {
	return &fakeConn{reads: make(chan readResult, 8), writes: make(chan []byte, 8), closed: make(chan struct{})}
}

func (c *fakeConn) ReadMessage() (int, []byte, error) {
	select {
	case r := <-c.reads:
		return wireTextMessage, r.data, r.err
	case <-c.closed:
		return 0, nil, net.ErrClosed
	}
}

func (c *fakeConn) WriteMessage(_ int, data []byte) error {
	if c.writeErr != nil {
		return c.writeErr
	}
	c.writes <- data
	return nil
}

func (c *fakeConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return nil
}

// fakeHandler maps every event to one TEXT.DELTA and records written events.
type fakeHandler struct {
	expectedRead, expectedWrite bool
	written                     chan models.SessionEventType
}

func (fakeHandler) HandleEvent(_ context.Context, event models.SessionEvent) []messages.StreamMessage {
	return []messages.StreamMessage{{Type: messages.StreamTypeTextDelta, ResponseID: string(event.Type)}}
}

func (h fakeHandler) ExpectedReadClose(error) bool { return h.expectedRead }

func (h fakeHandler) ExpectedWriteClose(context.Context, error) bool { return h.expectedWrite }

func (h fakeHandler) EventWritten(event models.SessionEvent) {
	if h.written != nil {
		h.written <- event.Type
	}
}

func waitDone(t *testing.T, s *Session) {
	t.Helper()
	select {
	case <-s.Done():
	case <-time.After(testWait):
		t.Fatal("session did not close")
	}
}

func readMessage(t *testing.T, s *Session) messages.StreamMessage {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), testWait)
	defer cancel()
	msg, err := s.Receive().ReadContext(ctx)
	if err != nil {
		t.Fatalf("read normalized message: %v", err)
	}
	return msg
}

func TestReadLoopDeliversEventsAndClosesOnExpectedClose(t *testing.T) {
	for _, lossless := range []bool{false, true} {
		conn := newFakeConn()
		s := NewSession(conn, nil, Config{LogPrefix: "test", LosslessInbound: lossless})
		conn.reads <- readResult{data: []byte(`{"type":"response.created"}`)}
		conn.reads <- readResult{err: io.EOF}
		go s.ReadLoop(t.Context(), fakeHandler{expectedRead: true})
		if msg := readMessage(t, s); msg.ResponseID != "response.created" {
			t.Fatalf("lossless=%t: message = %+v", lossless, msg)
		}
		waitDone(t, s)
		if err := s.TerminalError(); err != nil {
			t.Fatalf("lossless=%t: orderly close recorded %v", lossless, err)
		}
	}
}

func TestReadLoopSurfacesTransportAndParseFailures(t *testing.T) {
	transportErr := errors.New("connection reset")
	for name, read := range map[string]readResult{
		"transport": {err: transportErr},
		"parse":     {data: []byte(`{"no_type":true}`)},
		"json":      {data: []byte(`{`)},
	} {
		conn := newFakeConn()
		s := NewSession(conn, nil, Config{LogPrefix: "test"})
		conn.reads <- read
		go s.ReadLoop(t.Context(), fakeHandler{})
		waitDone(t, s)
		if s.TerminalError() == nil {
			t.Fatalf("%s: no terminal error", name)
		}
		if msg := readMessage(t, s); msg.Type != messages.StreamTypeError {
			t.Fatalf("%s: terminal message = %+v, want ERROR", name, msg)
		}
	}
}

func TestReadLoopDropsWhenLossyBufferIsFull(t *testing.T) {
	conn := newFakeConn()
	s := NewSession(conn, nil, Config{LogPrefix: "test"})
	for s.recvBuf.Len() < s.recvBuf.Cap() {
		s.recvBuf.Write(t.Context(), messages.StreamMessage{Type: messages.StreamTypeTextDelta})
	}
	if !s.deliver(t.Context(), messages.StreamMessage{Type: messages.StreamTypeTextDelta}) {
		t.Fatal("lossy delivery stopped the read loop on a full buffer")
	}
	if s.OutputDrops() != 1 {
		t.Fatalf("output drops = %d, want 1", s.OutputDrops())
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if s.deliver(ctx, messages.StreamMessage{Type: messages.StreamTypeTextDelta}) {
		t.Fatal("delivery continued after its context ended")
	}
	waitDone(t, s)
	if s.deliver(t.Context(), messages.StreamMessage{Type: messages.StreamTypeTextDelta}) {
		t.Fatal("delivery continued after close")
	}
}

func TestLosslessDeliveryStopsOnCancellation(t *testing.T) {
	s := NewSession(newFakeConn(), nil, Config{LogPrefix: "test", LosslessInbound: true})
	for s.recvBuf.Len() < s.recvBuf.Cap() {
		s.recvBuf.Write(t.Context(), messages.StreamMessage{Type: messages.StreamTypeTextDelta})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if s.deliver(ctx, messages.StreamMessage{Type: messages.StreamTypeTextDelta}) {
		t.Fatal("lossless delivery continued after its context ended")
	}
	waitDone(t, s)
}

func TestWriteLoopWritesFlatEventsAndFlushes(t *testing.T) {
	conn := newFakeConn()
	s := NewSession(conn, nil, Config{LogPrefix: "test"})
	written := make(chan models.SessionEventType, 1)
	go s.WriteLoop(t.Context(), fakeHandler{written: written})
	defer closeSession(t, s)
	if outcome := s.EnqueueEvents(t.Context(), []models.SessionEvent{models.NewAudioBufferAppendEvent("AAAA")}); !outcome.OK() {
		t.Fatalf("enqueue: %+v", outcome)
	}
	if err := s.FlushOutbound(t.Context()); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if got := <-written; got != models.SessionEventInputAudioBufferAppend {
		t.Fatalf("written = %s", got)
	}
	if got := string(<-conn.writes); got != `{"audio":"AAAA","type":"input_audio_buffer.append"}` {
		t.Fatalf("wire frame = %s", got)
	}
}

func TestWriteLoopFailureIsTerminalUnlessExpected(t *testing.T) {
	writeErr := errors.New("broken pipe")
	for _, expected := range []bool{false, true} {
		conn := newFakeConn()
		conn.writeErr = writeErr
		s := NewSession(conn, nil, Config{LogPrefix: "test"})
		done := make(chan struct{})
		go func() { s.WriteLoop(t.Context(), fakeHandler{expectedWrite: expected}); close(done) }()
		if outcome := s.EnqueueEvent(t.Context(), models.NewAudioBufferCommitEvent()); !outcome.OK() {
			t.Fatalf("enqueue: %+v", outcome)
		}
		<-done
		if got := s.TerminalError(); expected != (got == nil) {
			t.Fatalf("expected=%t: terminal error = %v", expected, got)
		}
		closeSession(t, s)
	}
}

func TestWriteLoopClosesOnContextEnd(t *testing.T) {
	s := NewSession(newFakeConn(), nil, Config{LogPrefix: "test"})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	s.WriteLoop(ctx, fakeHandler{})
	waitDone(t, s)
}

func TestStartRunsBothLoops(t *testing.T) {
	conn := newFakeConn()
	s := NewSession(conn, nil, Config{LogPrefix: "test"})
	s.Start(t.Context(), fakeHandler{expectedRead: true})
	if err := s.WriteEvent(models.SessionEvent{Type: models.SessionEventSessionUpdate}); err != nil {
		t.Fatalf("write: %v", err)
	}
	conn.reads <- readResult{data: []byte(`{"type":"session.created"}`)}
	if msg := readMessage(t, s); msg.ResponseID != "session.created" {
		t.Fatalf("message = %+v", msg)
	}
	closeSession(t, s)
	waitDone(t, s)
}

func TestEnqueueOutcomes(t *testing.T) {
	s := NewSession(newFakeConn(), nil, Config{LogPrefix: "test"})
	for s.sendQueue.Len() < s.sendQueue.Cap() {
		s.sendQueue.Write(t.Context(), models.NewAudioBufferCommitEvent())
	}
	events := []models.SessionEvent{models.NewAudioBufferCommitEvent()}
	if got := s.EnqueueEvents(t.Context(), events); got.Status != messages.SessionSendBufferFull {
		t.Fatalf("full queue = %+v", got)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Millisecond)
	defer cancel()
	if got := sendOutcome(s.EnqueueEventWait(ctx, events[0])); got.Status != messages.SessionSendTimedOut {
		t.Fatalf("backpressure timeout = %+v", got)
	}
	closeSession(t, s)
	if got := s.EnqueueEvents(t.Context(), events); got.Status != messages.SessionSendClosed {
		t.Fatalf("closed = %+v", got)
	}
	if got := sendOutcome(s.EnqueueEventWait(t.Context(), events[0])); got.Status != messages.SessionSendClosed {
		t.Fatalf("backpressure after close = %+v", got)
	}
	if err := s.FlushOutbound(t.Context()); err != nil {
		t.Fatalf("flush with nothing pending: %v", err)
	}
}

func TestSendOutcomeMapping(t *testing.T) {
	for status, want := range map[messages.BufferWriteStatus]messages.SessionSendStatus{
		messages.BufferWriteSucceeded:  messages.SessionSendSucceeded,
		messages.BufferWriteBufferFull: messages.SessionSendBufferFull,
		messages.BufferWriteStopped:    messages.SessionSendClosed,
		messages.BufferWriteCancelled:  messages.SessionSendCancelled,
		messages.BufferWriteTimedOut:   messages.SessionSendTimedOut,
		"unknown":                      messages.SessionSendTerminalFailure,
	} {
		if got := sendOutcome(messages.BufferWriteOutcome{Status: status}); got.Status != want {
			t.Errorf("%s -> %s, want %s", status, got.Status, want)
		}
	}
}

func TestContextOutcome(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if got := ContextOutcome(ctx); got.Status != messages.SessionSendCancelled || !errors.Is(got.Err, context.Canceled) {
		t.Fatalf("cancelled = %+v", got)
	}
	ctx, cancel = context.WithDeadline(t.Context(), time.Unix(0, 0))
	defer cancel()
	if got := ContextOutcome(ctx); got.Status != messages.SessionSendTimedOut {
		t.Fatalf("deadline = %+v", got)
	}
}

func TestWriteEventRejectsNonObjectData(t *testing.T) {
	s := NewSession(newFakeConn(), nil, Config{LogPrefix: "test"})
	if err := s.WriteEvent(models.SessionEvent{Type: "x", Data: []byte(`[1]`)}); err == nil {
		t.Fatal("non-object event data was written")
	}
}

// JSON null decodes into a nil map; it must still write a type-only event.
func TestWriteEventTreatsNullDataAsEmpty(t *testing.T) {
	conn := newFakeConn()
	s := NewSession(conn, nil, Config{LogPrefix: "test"})
	if err := s.WriteEvent(models.SessionEvent{Type: "x", Data: []byte(`null`)}); err != nil {
		t.Fatalf("null event data: %v", err)
	}
	if got := string(<-conn.writes); got != `{"type":"x"}` {
		t.Fatalf("wire frame = %s, want type only", got)
	}
}

func TestParseEvent(t *testing.T) {
	event, err := ParseEvent([]byte(`{"type":"response.done","response":{}}`))
	if err != nil || event.Type != models.SessionEventResponseDone {
		t.Fatalf("event = %+v, err = %v", event, err)
	}
	for _, raw := range []string{`{`, `{"type":""}`} {
		if _, err := ParseEvent([]byte(raw)); err == nil {
			t.Errorf("ParseEvent(%s) accepted a malformed frame", raw)
		}
	}
}

func TestTerminalErrorKeepsFirstAndCloseRunsHookOnce(t *testing.T) {
	var nilSession *Session
	if nilSession.TerminalError() != nil || nilSession.FlushOutbound(t.Context()) != nil {
		t.Fatal("nil session reported state")
	}
	closes := 0
	s := NewSession(newFakeConn(), nil, Config{LogPrefix: "test", OnClose: func() { closes++ }})
	first := errors.New("first")
	s.SetTerminalError(nil)
	s.SetTerminalError(first)
	s.SetTerminalError(errors.New("second"))
	if !errors.Is(s.TerminalError(), first) {
		t.Fatalf("terminal error = %v", s.TerminalError())
	}
	if s.Closed() || s.Stopping(t.Context()) {
		t.Fatal("open session reports stopping")
	}
	closeSession(t, s)
	closeSession(t, s)
	if closes != 1 || !s.Closed() || s.Logger() == nil {
		t.Fatalf("OnClose ran %d times, closed=%t", closes, s.Closed())
	}
	if s.InputDrops() != 0 || s.OutputDrops() != 0 {
		t.Fatal("unexpected drops")
	}
}

func TestRTCMediaLifecycle(t *testing.T) {
	interrupts := 0
	s := NewSession(newFakeConn(), nil, Config{
		LogPrefix: "test", MediaName: "Test", OutputSampleRate: 24000,
		WriteMediaFrame:   func(context.Context, sharedaudio.PCMFrame) error { return nil },
		InterruptPlayback: func(context.Context) { interrupts++ },
	})
	if !s.InitialSessionConfigSent() || s.InputAudioSampleRate() != DefaultInputSampleRate {
		t.Fatal("unexpected session defaults")
	}
	s.PrepareRTCMedia()
	prepared := s.CurrentRTCMedia()
	// A continuous claim replaces the unclaimed one-shot media.
	s.RTCMediaWithOptions(sharedaudio.MediaSessionOptions{InboundContinuous: true})
	if s.CurrentRTCMedia() == prepared {
		t.Fatal("continuous claim kept the prepared media")
	}
	s.RTCMedia()
	s.Receive() // claimed media survives Receive
	if s.CurrentRTCMedia() == nil {
		t.Fatal("claimed media was released")
	}
	if s.InterruptLocalPlayback(t.Context()) || interrupts != 0 {
		t.Fatal("silent playback was interrupted")
	}
	media := s.CurrentRTCMedia()
	if err := PushInboundPCM(media, make([]byte, 24000*2)); err != nil {
		t.Fatalf("push inbound: %v", err)
	}
	if !s.LocalPlayback().Active || !s.InterruptLocalPlayback(t.Context()) || interrupts != 1 {
		t.Fatalf("audible playback: active=%t interrupts=%d", s.LocalPlayback().Active, interrupts)
	}
	if err := PushInboundPCM(media, []byte{1}); err == nil {
		t.Fatal("odd-length PCM accepted")
	}
	if err := PushInboundPCM(media, nil); err != nil {
		t.Fatalf("empty PCM: %v", err)
	}
	if FailInboundOnError(media, nil) != nil || FailInboundOnError(media, sharedaudio.ErrSessionMediaClosed) == nil {
		t.Fatal("FailInboundOnError changed its error")
	}
	closeSession(t, s)

	stream := NewSession(newFakeConn(), nil, Config{LogPrefix: "test", InputSampleRate: 16000})
	stream.PrepareRTCMedia()
	stream.Receive() // a stream-only caller releases the speculative media
	if stream.CurrentRTCMedia() != nil || stream.InputAudioSampleRate() != 16000 {
		t.Fatal("unclaimed media survived Receive")
	}
}

func closeSession(t *testing.T, s *Session) {
	t.Helper()
	if err := s.Close(); err != nil {
		t.Errorf("close: %v", err)
	}
}
