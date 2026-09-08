package replay

import (
	"context"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

// replayMediaInferencer claims headless media so captured server-VAD keeps its
// validated conversation.item.truncate boundary during directory replay.
type replayMediaInferencer struct{ inner messages.SessionInferencer }

func (i replayMediaInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	sess, err := i.inner.ConnectSession(ctx)
	if err != nil {
		return nil, err
	}
	attachReplayMedia(ctx, sess)
	return sess, nil
}

func attachReplayMedia(ctx context.Context, sess messages.Session) {
	owner, ok := sess.(sharedaudio.MediaSession)
	if !ok || owner.RTCMedia().Inbound == nil {
		return
	}
	inbound := owner.RTCMedia().Inbound
	if controlled, ok := inbound.(sharedaudio.PlaybackControlledInbound); ok {
		controlled.SetPlaybackController(replayPlaybackController{})
	}
	go drainReplayInbound(ctx, inbound)
}

func drainReplayInbound(ctx context.Context, inbound sharedaudio.InboundMedia) {
	for {
		if _, err := inbound.ReadFrame(ctx); err != nil {
			return
		}
	}
}

type replayPlaybackController struct{}

func (replayPlaybackController) StartPlayback(sharedaudio.PlaybackResponse) {}
func (replayPlaybackController) InterruptPlayback(sharedaudio.PlaybackResponse) (int, bool) {
	return 0, true
}
