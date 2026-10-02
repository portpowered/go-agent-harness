package grok

import (
	"context"
	"errors"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/logging"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/internal/realtime"
)

// writeRTCMediaFrame feeds one captured RTC frame through the same input-audio
// event path used by StreamMessage audio.
func (s *grokSession) writeRTCMediaFrame(ctx context.Context, frame sharedaudio.PCMFrame) error {
	encoded, err := codec.EncodePCM16WithLimit(frame.Samples, codec.MaxPCM16Bytes)
	if err != nil {
		return fmt.Errorf("encode Grok RTC audio: %w", err)
	}
	outcome := s.SendWithOutcome(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeAudioDelta,
		Value: messages.NewAudioDeltaValue(encoded),
	})
	if outcome.OK() {
		return nil
	}
	if outcome.Err != nil {
		return outcome.Err
	}
	return fmt.Errorf("grok RTC media write: %s", outcome.Status)
}

// publishRTCMedia fans inbound provider audio out to the media reader without
// removing it from the normal session stream.
func (s *grokSession) publishRTCMedia(event models.SessionEvent) error {
	media := s.base.CurrentRTCMedia()
	if media == nil {
		return nil
	}
	var err error
	switch event.Type { //nolint:exhaustive // Only audio and response-boundary events touch RTC media.
	case models.SessionEventInputAudioBufferSpeechStarted:
		media.InterruptInbound()
	case models.SessionEventResponseCreated:
		// Name the response before its first audio delta, so an interruption
		// in between still discards that response's late audio.
		media.StartInboundResponse(sharedaudio.PlaybackResponse{ResponseID: responseEventID(event.Data)})
	case models.SessionEventResponseOutputAudioDelta, grokSessionEventResponseAudioDelta:
		// The response identity lets an interruption discard this response's
		// late deltas.
		media.StartInboundResponse(sharedaudio.PlaybackResponse{ResponseID: responseEventID(event.Data)})
		var data []byte
		if data, err = decodeGrokAudioDelta(event.Data); err == nil {
			err = realtime.PushInboundPCM(media, data)
		}
	case models.SessionEventResponseOutputAudioDone, grokSessionEventResponseAudioDone:
		err = media.FlushInbound()
	}
	return realtime.FailInboundOnError(media, err)
}

// interruptRTCPlayback discards response audio queued for local playback.
func (s *grokSession) interruptRTCPlayback() {
	if media := s.base.CurrentRTCMedia(); media != nil {
		media.InterruptInbound()
	}
}

// publishRTCMediaWithLog forwards provider audio to the RTC media path. The
// media path records its own failure; the read loop keeps translating events.
func (s *grokSession) publishRTCMediaWithLog(event models.SessionEvent) {
	if err := s.publishRTCMedia(event); err != nil && !errors.Is(err, sharedaudio.ErrSessionMediaClosed) {
		s.base.Logger().Warn("grok: RTC media event failed", logging.Field{Key: "error", Value: err})
	}
}

func decodeGrokAudioDelta(data []byte) ([]byte, error) {
	encoded := extractStringField(data, "delta")
	if encoded == "" {
		return nil, nil
	}
	decoded, err := codec.DecodeBase64(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode Grok RTC audio delta: %w", err)
	}
	if err := codec.ValidatePCM16(decoded, codec.MaxPCM16Bytes); err != nil {
		return nil, fmt.Errorf("decode Grok RTC audio delta: %w", err)
	}
	return decoded, nil
}
