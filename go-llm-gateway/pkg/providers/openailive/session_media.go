package openailive

import (
	"context"
	"errors"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/logging"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/internal/realtime"
)

// errMediaNeedsPCM reports an RTC media frame for a G.711 session. RTC media
// carries PCM16 samples, and GPT-Live never converts formats.
var errMediaNeedsPCM = errors.New("openailive: RTC media requires an audio/pcm session format")

// writeRTCMediaFrame feeds one captured RTC frame through the same input
// path as AUDIO.DELTA.
func (s *liveSession) writeRTCMediaFrame(ctx context.Context, frame sharedaudio.PCMFrame) error {
	if s.format.Type != AudioTypePCM {
		return errMediaNeedsPCM
	}
	outcome := s.appendAudio(ctx, codec.EncodePCM16(frame.Samples))
	if outcome.OK() {
		return nil
	}
	return errors.Join(fmt.Errorf("openai live RTC media write: %s", outcome.Status), outcome.Err)
}

// publishRTCMedia mirrors segment audio onto the RTC media path without
// removing it from the stream: each segment's audio is one playback
// response, flushed when the segment ends.
func (s *liveSession) publishRTCMedia(msg messages.StreamMessage) {
	media := s.base.CurrentRTCMedia()
	if media == nil || s.format.Type != AudioTypePCM {
		return
	}
	var err error
	switch msg.Type { //nolint:exhaustive // Only segment audio and segment ends touch RTC media.
	case messages.StreamTypeAudioDelta:
		if value, ok := msg.Value.(*messages.AudioDeltaValue); ok && value != nil {
			media.StartInboundResponse(sharedaudio.PlaybackResponse{ResponseID: msg.ResponseID})
			err = realtime.PushInboundPCM(media, value.Content)
		}
	case messages.StreamTypeMessageEnd:
		err = media.FlushInbound()
	}
	if err = realtime.FailInboundOnError(media, err); err != nil && !errors.Is(err, sharedaudio.ErrSessionMediaClosed) {
		s.base.Logger().Warn("openai live: RTC media event failed", logging.Field{Key: "error", Value: err})
	}
}

// interruptRTCPlayback discards segment audio queued for local playback.
func (s *liveSession) interruptRTCPlayback() {
	if media := s.base.CurrentRTCMedia(); media != nil {
		media.InterruptInbound()
	}
}
