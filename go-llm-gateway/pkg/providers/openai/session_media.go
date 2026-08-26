package openai

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport/rtc"
)

var _ rtc.MediaSession = (*realtimeSession)(nil)

// RTCMedia exposes provider-owned PCM media endpoints for an OpenAI Realtime
// session. Inbound audio remains available through Receive while also being
// framed for the local RTC device sink.
func (s *realtimeSession) RTCMedia() rtc.MediaEndpoints {
	s.mediaMu.Lock()
	defer s.mediaMu.Unlock()
	if s.media == nil {
		s.media = rtc.NewSessionMedia(s.writeRTCMediaFrame)
	}
	return s.media.Endpoints()
}

func (s *realtimeSession) currentRTCMedia() *rtc.SessionMedia {
	s.mediaMu.Lock()
	defer s.mediaMu.Unlock()
	return s.media
}

func (s *realtimeSession) writeRTCMediaFrame(ctx context.Context, frame rtc.PCMFrame) error {
	encoded := base64.StdEncoding.EncodeToString(encodePCM16(frame.Samples))
	outcome := s.sendEvents(ctx, []models.SessionEvent{models.NewAudioBufferAppendEvent(encoded)})
	if outcome.OK() {
		return nil
	}
	if outcome.Err != nil {
		return outcome.Err
	}
	return fmt.Errorf("OpenAI Realtime RTC media write: %s", outcome.Status)
}

func (s *realtimeSession) publishRTCMedia(event models.SessionEvent) error {
	media := s.currentRTCMedia()
	if media == nil {
		return nil
	}

	var err error
	switch event.Type {
	case models.SessionEventResponseOutputAudioDelta:
		format := realtimeAudioMediaType(event.Data)
		if format != "" && format != "audio/pcm" {
			err = fmt.Errorf("OpenAI Realtime RTC audio format %q is not PCM16", format)
			break
		}
		data, decodeErr := decodeOpenAIRealtimeAudioDelta(event.Data)
		if decodeErr != nil {
			err = decodeErr
		} else if len(data) > 0 {
			err = media.PushInbound(decodePCM16(data))
		}
	case models.SessionEventResponseOutputAudioDone:
		err = media.FlushInbound()
	}
	if err != nil && !errors.Is(err, rtc.ErrSessionMediaClosed) {
		media.FailInbound(err)
	}
	return err
}

func decodeOpenAIRealtimeAudioDelta(data []byte) ([]byte, error) {
	encoded := firstStringField(data, "delta")
	if encoded == "" {
		return nil, nil
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode OpenAI Realtime RTC audio delta: %w", err)
	}
	if len(decoded)%2 != 0 {
		return nil, fmt.Errorf("decode OpenAI Realtime RTC audio delta: PCM16 payload has %d odd bytes", len(decoded))
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
