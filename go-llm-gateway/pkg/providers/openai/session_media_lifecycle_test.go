package openai

import sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
)

func TestRTCMediaBackpressureUnblocksWhenSessionCloses(t *testing.T) {
	session := &realtimeSession{
		sendQueue: messages.NewTypedBuffer[models.SessionEvent](1),
		done:      make(chan struct{}),
	}
	if outcome := session.sendQueue.WriteContext(context.Background(), models.NewAudioBufferAppendEvent("seed")); !outcome.OK() {
		t.Fatalf("seed send queue: %+v", outcome)
	}
	result := make(chan error, 1)
	go func() {
		result <- session.writeRTCMediaFrame(context.Background(), sharedaudio.PCMFrame{Samples: []int16{1, 2, 3}})
	}()
	select {
	case err := <-result:
		t.Fatalf("media write returned before session shutdown: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(session.done)
	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "session closed") {
			t.Fatalf("media write after shutdown = %v, want session closed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("backpressured media write remained stuck after session shutdown")
	}
}

func TestControlBackpressurePreservesCommitAfterQueuedAudio(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	session := &realtimeSession{sendQueue: messages.NewTypedBuffer[models.SessionEvent](1), done: make(chan struct{}), writeBackpressure: true}
	if outcome := session.sendQueue.WriteContext(ctx, models.NewAudioBufferAppendEvent("seed")); !outcome.OK() {
		t.Fatal(outcome)
	}
	result := make(chan messages.SessionSendOutcome, 1)
	go func() {
		result <- session.enqueueWireEvents(ctx, []models.SessionEvent{models.NewAudioBufferCommitEvent()})
	}()
	select {
	case outcome := <-result:
		t.Fatalf("commit bypassed full audio queue: %+v", outcome)
	case <-time.After(20 * time.Millisecond):
	}
	audio, ok := session.sendQueue.ReadBlockingContext(ctx)
	if !ok || audio.Type != models.SessionEventInputAudioBufferAppend {
		t.Fatalf("first event = %+v", audio)
	}
	if outcome := <-result; !outcome.OK() {
		t.Fatalf("commit rejected after capacity released: %+v", outcome)
	}
	commit, ok := session.sendQueue.ReadBlockingContext(ctx)
	if !ok || commit.Type != models.SessionEventInputAudioBufferCommit {
		t.Fatalf("second event = %+v", commit)
	}
	if drops := session.sendQueue.Drops(); drops != 0 {
		t.Fatalf("backpressure dropped %d events", drops)
	}
}

func TestControlBackpressureUnblocksOnCancellationAndClose(t *testing.T) {
	t.Run("cancel", func(t *testing.T) { verifyControlBackpressureStop(t, false) })
	t.Run("close", func(t *testing.T) { verifyControlBackpressureStop(t, true) })
}

func verifyControlBackpressureStop(t *testing.T, closeSession bool) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	session := &realtimeSession{sendQueue: messages.NewTypedBuffer[models.SessionEvent](1), done: make(chan struct{}), writeBackpressure: true}
	if outcome := session.sendQueue.WriteContext(ctx, models.NewAudioBufferAppendEvent("seed")); !outcome.OK() {
		t.Fatal(outcome)
	}
	result := make(chan messages.SessionSendOutcome, 1)
	go func() {
		result <- session.enqueueWireEvents(ctx, []models.SessionEvent{models.NewAudioBufferCommitEvent()})
	}()
	want := messages.SessionSendCancelled
	if closeSession {
		close(session.done)
		want = messages.SessionSendClosed
	} else {
		cancel()
	}
	select {
	case outcome := <-result:
		if outcome.Status != want {
			t.Fatalf("outcome = %+v, want %s", outcome, want)
		}
		if !closeSession && !errors.Is(outcome.Err, context.Canceled) {
			t.Fatalf("lost cancellation cause: %v", outcome.Err)
		}
	case <-time.After(time.Second):
		t.Fatal("control admission stuck after cancellation/close")
	}
}

func TestFlushOutboundWaitsForWebSocketWrite(t *testing.T) {
	conn := newBlockingOutboundConn()
	session := newRealtimeSession(conn, nopLogger())
	writerDone := make(chan struct{})
	go func() {
		session.writeLoop(context.Background())
		close(writerDone)
	}()
	t.Cleanup(func() {
		_ = session.Close()
		select {
		case <-writerDone:
		case <-time.After(time.Second):
			t.Error("write loop did not stop")
		}
	})

	if outcome := session.enqueueWireEvent(context.Background(), models.NewAudioBufferAppendEvent("pending")); !outcome.OK() {
		t.Fatalf("enqueue outbound event: %+v", outcome)
	}
	select {
	case <-conn.writeStarted:
	case <-time.After(time.Second):
		t.Fatal("write loop did not reach the transport")
	}

	flushCtx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	flushed := make(chan error, 1)
	go func() { flushed <- session.FlushOutbound(flushCtx) }()
	select {
	case err := <-flushed:
		t.Fatalf("FlushOutbound returned while transport write was blocked: %v", err)
	case <-flushCtx.Done():
		t.Fatal("FlushOutbound did not wait for transport settlement before its context expired")
	case <-time.After(20 * time.Millisecond):
	}

	close(conn.release)
	select {
	case err := <-flushed:
		if err != nil {
			t.Fatalf("FlushOutbound after transport settlement: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("FlushOutbound remained blocked after transport settlement")
	}
}

func TestRealtimeSession_FlushOutboundWaitsForDeferredAudioCommit(t *testing.T) {
	conn := newMockWebSocketConn()
	session := newRealtimeSession(conn, nopLogger())
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	session.start(ctx)
	defer func() { _ = session.Close() }()

	session.observeResponseCreated(models.SessionEvent{
		Type: models.SessionEventResponseCreated,
		Data: []byte(`{"response":{"id":"resp-active"}}`),
	})
	if outcome := session.SendWithOutcome(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeMessageEnd,
		Value: messages.NewMessageEndValue(messages.TokenUsage{}),
	}); !outcome.OK() {
		t.Fatalf("deferred audio end admission: %#v", outcome)
	}

	flushed := make(chan error, 1)
	go func() { flushed <- session.FlushOutbound(ctx) }()
	select {
	case err := <-flushed:
		t.Fatalf("FlushOutbound returned before the deferred commit was admitted: %v", err)
	case <-time.After(20 * time.Millisecond):
	}

	session.observeResponseDone(models.SessionEvent{
		Type: models.SessionEventResponseDone,
		Data: []byte(`{"response":{"id":"resp-active","status":"completed"}}`),
	})
	select {
	case err := <-flushed:
		if err != nil {
			t.Fatalf("FlushOutbound after deferred commit: %v", err)
		}
	case <-ctx.Done():
		t.Fatalf("FlushOutbound did not settle deferred commit: %v", ctx.Err())
	}

	frames := parseWireFrames(t, conn.getClientMessages())
	if len(frames) != 2 || frames[0].Type != "input_audio_buffer.commit" || frames[1].Type != "response.create" {
		t.Fatalf("deferred audio wire frames = %#v, want commit then response.create", frames)
	}
}

type blockingOutboundConn struct {
	writeStarted chan struct{}
	release      chan struct{}
	closed       chan struct{}
	closeOnce    sync.Once
}

func newBlockingOutboundConn() *blockingOutboundConn {
	return &blockingOutboundConn{
		writeStarted: make(chan struct{}),
		release:      make(chan struct{}),
		closed:       make(chan struct{}),
	}
}

func (c *blockingOutboundConn) ReadMessage() (int, []byte, error) {
	<-c.closed
	return 0, nil, errors.New("connection closed")
}

func (c *blockingOutboundConn) WriteMessage(int, []byte) error {
	c.closeOnce.Do(func() { close(c.writeStarted) })
	select {
	case <-c.release:
		return nil
	case <-c.closed:
		return errors.New("connection closed")
	}
}

func (c *blockingOutboundConn) Close() error {
	c.closeOnce.Do(func() { close(c.writeStarted) })
	select {
	case <-c.closed:
	default:
		close(c.closed)
	}
	return nil
}
