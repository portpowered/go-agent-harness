package strict

import (
	"context"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

func TestAttachReplayMediaPreservesProviderInterruptionBoundary(t *testing.T) {
	media := sharedaudio.NewSessionMediaAtRate(nil, 24000)
	defer func() {
		if err := media.Close(); err != nil {
			t.Errorf("close media: %v", err)
		}
	}()

	attachReplayMedia(context.Background(), &replayMediaTestSession{media: media})
	response := sharedaudio.PlaybackResponse{ResponseID: "resp-interrupted", ItemID: "item-interrupted"}
	media.StartInboundResponse(response)
	if err := media.PushInbound(make([]int16, 720)); err != nil {
		t.Fatal(err)
	}

	interruption, ok := media.InterruptInbound()
	if !ok || interruption.PlaybackResponse != response || interruption.AudioEndMS != 0 {
		t.Fatalf("replay interruption = %+v/%t, want response at zero cursor", interruption, ok)
	}
}

type replayMediaTestSession struct {
	media   *sharedaudio.SessionMedia
	receive *messages.TypedBuffer[messages.StreamMessage]
	done    chan struct{}
}

func (s *replayMediaTestSession) Send(context.Context, messages.StreamMessage) bool { return true }

func (s *replayMediaTestSession) RTCMedia() sharedaudio.MediaEndpoints {
	if s.media == nil {
		return sharedaudio.MediaEndpoints{}
	}
	return s.media.Endpoints()
}

func (s *replayMediaTestSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	if s.receive == nil {
		s.receive = messages.NewTypedBuffer[messages.StreamMessage](1)
	}
	return s.receive
}

func (s *replayMediaTestSession) Done() <-chan struct{} {
	if s.done == nil {
		s.done = make(chan struct{})
	}
	return s.done
}

func (s *replayMediaTestSession) Close() error {
	if s.media != nil {
		return s.media.Close()
	}
	return nil
}
