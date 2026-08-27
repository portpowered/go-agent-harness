package grok

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport/rtc"
)

var _ rtc.MediaSession = (*grokSession)(nil)

// RTCMedia exposes the provider-owned PCM media endpoints for the live Grok
// realtime session. The endpoint writer feeds the same input-audio event path
// used by StreamMessage audio, while inbound provider deltas are fanned out to
// the media reader without removing them from the normal session stream.
func (s *grokSession) RTCMedia() rtc.MediaEndpoints {
	s.mediaMu.Lock()
	defer s.mediaMu.Unlock()
	if s.media == nil {
		s.media = rtc.NewSessionMedia(s.writeRTCMediaFrame)
	}
	return s.media.Endpoints()
}

func (s *grokSession) currentRTCMedia() *rtc.SessionMedia {
	s.mediaMu.Lock()
	defer s.mediaMu.Unlock()
	return s.media
}

func (s *grokSession) writeRTCMediaFrame(ctx context.Context, frame rtc.PCMFrame) error {
	outcome := s.SendWithOutcome(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeAudioDelta,
		Value: messages.NewAudioDeltaValue(encodePCM16(frame.Samples)),
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
	case models.SessionEventResponseOutputAudioDelta, grokSessionEventResponseAudioDelta:
		data, decodeErr := decodeGrokAudioDelta(event.Data)
		if decodeErr != nil {
			err = decodeErr
		} else if len(data) > 0 {
			err = media.PushInbound(decodePCM16(data))
		}
	case models.SessionEventResponseOutputAudioDone, grokSessionEventResponseAudioDone:
		err = media.FlushInbound()
	}
	if err != nil && !errors.Is(err, rtc.ErrSessionMediaClosed) {
		media.FailInbound(err)
	}
	return err
}

func decodeGrokAudioDelta(data []byte) ([]byte, error) {
	encoded := extractStringField(data, "delta")
	if encoded == "" {
		return nil, nil
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode Grok RTC audio delta: %w", err)
	}
	if len(decoded)%2 != 0 {
		return nil, fmt.Errorf("decode Grok RTC audio delta: PCM16 payload has %d odd bytes", len(decoded))
	}
	return decoded, nil
}

func decodePCM16(data []byte) []int16 {
	samples := make([]int16, len(data)/2)
	for index := range samples {
		samples[index] = int16(binary.LittleEndian.Uint16(data[index*2:])) //nolint:gosec // PCM16 bit pattern is intentional
	}
	return samples
}

func encodePCM16(samples []int16) []byte {
	data := make([]byte, len(samples)*2)
	for index, sample := range samples {
		binary.LittleEndian.PutUint16(data[index*2:], uint16(sample)) //nolint:gosec // PCM16 bit pattern is intentional
	}
	return data
}
