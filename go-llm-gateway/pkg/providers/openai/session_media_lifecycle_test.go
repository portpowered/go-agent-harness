package openai

import sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"

import (
	"context"
	"errors"
	"strings"
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
