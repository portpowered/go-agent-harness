package openai

import sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"

import (
	"context"
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
