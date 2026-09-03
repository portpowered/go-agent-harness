package openai

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
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
	s.mediaClaimed = true
	if s.media == nil {
		s.media = rtc.NewSessionMediaAtRate(s.writeRTCMediaFrame, s.mediaSampleRate)
	}
	return s.media.Endpoints()
}

func (s *realtimeSession) prepareRTCMedia() {
	s.mediaMu.Lock()
	if s.media == nil {
		s.media = rtc.NewSessionMediaAtRate(s.writeRTCMediaFrame, s.mediaSampleRate)
	}
	s.mediaMu.Unlock()
}

func (s *realtimeSession) releaseUnclaimedRTCMedia() {
	s.mediaMu.Lock()
	if s.mediaClaimed || s.media == nil {
		s.mediaMu.Unlock()
		return
	}
	media := s.media
	s.media = nil
	s.mediaMu.Unlock()
	_ = media.Close()
}

func (s *realtimeSession) currentRTCMedia() *rtc.SessionMedia {
	s.mediaMu.Lock()
	defer s.mediaMu.Unlock()
	return s.media
}

func (s *realtimeSession) writeRTCMediaFrame(ctx context.Context, frame rtc.PCMFrame) error {
	encoded := base64.StdEncoding.EncodeToString(encodePCM16(frame.Samples))
	select {
	case <-s.done:
		return fmt.Errorf("OpenAI Realtime RTC media write: session closed")
	default:
	}
	// Hardware capture is a continuous, clocked source. Backpressure it when
	// the WebSocket writer is briefly behind instead of treating a transient
	// full control queue as terminal audio loss.
	outcome := s.sendQueue.WriteWaitContextOrDone(ctx, s.done, models.NewAudioBufferAppendEvent(encoded))
	if outcome.OK() {
		return nil
	}
	if outcome.Status == messages.BufferWriteStopped {
		return fmt.Errorf("OpenAI Realtime RTC media write: session closed")
	}
	if outcome.Err != nil {
		return outcome.Err
	}
	return fmt.Errorf("OpenAI Realtime RTC media write: %s", outcome.Status)
}

func (s *realtimeSession) publishRTCMedia(ctx context.Context, event models.SessionEvent) error {
	media := s.currentRTCMedia()
	if media == nil {
		return nil
	}

	var err error
	switch event.Type {
	case models.SessionEventInputAudioBufferSpeechStarted:
		if interruption, ok := media.InterruptInbound(); ok {
			truncate := models.NewConversationItemTruncateEvent(interruption.ItemID, interruption.ContentIndex, interruption.AudioEndMS)
			outcome := s.sendQueue.WriteWaitContextOrDone(ctx, s.done, truncate)
			if !outcome.OK() {
				if outcome.Err != nil {
					err = outcome.Err
				} else {
					err = fmt.Errorf("queue OpenAI Realtime conversation truncation: %s", outcome.Status)
				}
			}
		}
	case models.SessionEventResponseOutputAudioDelta:
		format := realtimeAudioMediaType(event.Data)
		if format != "" && format != "audio/pcm" {
			err = fmt.Errorf("OpenAI Realtime RTC audio format %q is not PCM16", format)
			break
		}
		response := realtimePlaybackResponse(event.Data)
		if response.ItemID != "" {
			media.StartInboundResponse(response)
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

func realtimePlaybackResponse(data json.RawMessage) rtc.PlaybackResponse {
	var payload struct {
		ResponseID   string `json:"response_id"`
		ItemID       string `json:"item_id"`
		ContentIndex int    `json:"content_index"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return rtc.PlaybackResponse{}
	}
	return rtc.PlaybackResponse{
		ResponseID: payload.ResponseID, ItemID: payload.ItemID, ContentIndex: payload.ContentIndex,
	}
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
