package grok

import sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"

import (
	"context"
	"errors"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/logging"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
)

var _ sharedaudio.MediaSession = (*grokSession)(nil)

// InitialSessionConfigSent reports that ConnectSession already sent the
// provider-owned session.update before the read loop started.
func (*grokSession) InitialSessionConfigSent() bool { return true }

// RTCMedia exposes the provider-owned PCM media endpoints for the live Grok
// realtime session. The endpoint writer feeds the same input-audio event path
// used by StreamMessage audio, while inbound provider deltas are fanned out to
// the media reader without removing them from the normal session stream.
func (s *grokSession) RTCMedia() sharedaudio.MediaEndpoints {
	return s.rtcMedia(sharedaudio.MediaSessionOptions{})
}

func (s *grokSession) RTCMediaWithOptions(options sharedaudio.MediaSessionOptions) sharedaudio.MediaEndpoints {
	return s.rtcMedia(options)
}

func (s *grokSession) rtcMedia(options sharedaudio.MediaSessionOptions) sharedaudio.MediaEndpoints {
	s.mediaMu.Lock()
	var previous *sharedaudio.SessionMedia
	if s.media != nil && !s.mediaClaimed && s.mediaContinuous != options.InboundContinuous {
		previous = s.media
		s.media = nil
	}
	s.mediaClaimed = true
	if s.media == nil {
		s.media = sharedaudio.NewSessionMediaAtRateWithOptions(s.writeRTCMediaFrame, s.mediaSampleRate, options)
		s.mediaContinuous = options.InboundContinuous
	}
	endpoints := s.media.Endpoints()
	s.mediaMu.Unlock()
	if previous != nil {
		if err := previous.Close(); err != nil {
			s.setTerminalError(fmt.Errorf("close replaced Grok RTC media: %w", err))
		}
	}
	return endpoints
}

func (s *grokSession) prepareRTCMedia() {
	s.mediaMu.Lock()
	if s.media == nil {
		s.media = sharedaudio.NewSessionMediaAtRate(s.writeRTCMediaFrame, s.mediaSampleRate)
		s.mediaContinuous = false
	}
	s.mediaMu.Unlock()
}

func (s *grokSession) releaseUnclaimedRTCMedia() {
	s.mediaMu.Lock()
	if s.mediaClaimed || s.media == nil {
		s.mediaMu.Unlock()
		return
	}
	media := s.media
	s.media = nil
	s.mediaContinuous = false
	s.mediaMu.Unlock()
	if err := media.Close(); err != nil {
		s.logger.Warn("grok: release unclaimed RTC media", logging.Field{Key: "error", Value: err})
	}
}

func (s *grokSession) currentRTCMedia() *sharedaudio.SessionMedia {
	s.mediaMu.Lock()
	defer s.mediaMu.Unlock()
	return s.media
}

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

func (s *grokSession) publishRTCMedia(event models.SessionEvent) error {
	media := s.currentRTCMedia()
	if media == nil {
		return nil
	}

	var err error
	switch event.Type {
	case models.SessionEventInputAudioBufferSpeechStarted:
		media.InterruptInbound()
	case models.SessionEventResponseOutputAudioDelta, grokSessionEventResponseAudioDelta:
		// The response identity lets an interruption discard this response's
		// late deltas.
		media.StartInboundResponse(sharedaudio.PlaybackResponse{ResponseID: responseEventID(event.Data)})
		data, decodeErr := decodeGrokAudioDelta(event.Data)
		if decodeErr != nil {
			err = decodeErr
		} else if len(data) > 0 {
			var samples []int16
			samples, err = codec.DecodePCM16(data)
			if err == nil {
				err = media.PushInbound(samples)
			}
		}
	case models.SessionEventResponseOutputAudioDone, grokSessionEventResponseAudioDone:
		err = media.FlushInbound()
	}
	if err != nil && !errors.Is(err, sharedaudio.ErrSessionMediaClosed) {
		media.FailInbound(err)
	}
	return err
}

// ProviderTurnDetection reports that Grok always runs server VAD.
func (*grokSession) ProviderTurnDetection() bool { return true }

// LocalPlayback reports provider audio still queued for or audible on the
// local device.
func (s *grokSession) LocalPlayback() messages.LocalPlaybackState {
	activity := s.currentRTCMedia().PlaybackActivity()
	return messages.LocalPlaybackState{Active: activity.Active, Level: activity.Level}
}

// InterruptLocalPlayback discards audio not yet heard.
func (s *grokSession) InterruptLocalPlayback(context.Context) bool {
	if !s.LocalPlayback().Active {
		return false
	}
	s.interruptRTCPlayback()
	return true
}

// interruptRTCPlayback discards response audio queued for local playback.
// Grok audio deltas carry no conversation item identity, so no provider-side
// truncation is possible.
func (s *grokSession) interruptRTCPlayback() {
	if media := s.currentRTCMedia(); media != nil {
		media.InterruptInbound()
	}
}

// publishRTCMediaWithLog forwards provider audio to the RTC media path. The
// media path records its own failure; the read loop keeps translating events.
func (s *grokSession) publishRTCMediaWithLog(event models.SessionEvent) {
	if err := s.publishRTCMedia(event); err != nil && !errors.Is(err, sharedaudio.ErrSessionMediaClosed) {
		s.logger.Warn("grok: RTC media event failed", logging.Field{Key: "error", Value: err})
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
