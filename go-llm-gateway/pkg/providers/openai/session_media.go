package openai

import sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/logging"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
)

func (s *realtimeSession) writeRTCMediaFrame(ctx context.Context, frame sharedaudio.PCMFrame) error {
	encoded, err := codec.EncodePCM16Base64WithLimit(frame.Samples, codec.MaxPCM16Bytes)
	if err != nil {
		return fmt.Errorf("encode OpenAI Realtime RTC audio: %w", err)
	}
	if s.base.Closed() {
		return errors.New("OpenAI Realtime RTC media write: session closed")
	}
	// Hardware capture is a continuous, clocked source. Backpressure it when
	// the WebSocket writer is briefly behind instead of treating a transient
	// full control queue as terminal audio loss.
	outcome := s.base.EnqueueEventWait(ctx, models.NewAudioBufferAppendEvent(encoded))
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
	media := s.base.CurrentRTCMedia()
	if media == nil {
		return nil
	}

	var err error
	switch event.Type { //nolint:exhaustive // Only audio and response-boundary events touch RTC media.
	case models.SessionEventInputAudioBufferSpeechStarted:
		err = s.interruptPlayback(ctx, media)
	case models.SessionEventResponseCreated:
		// Name the response before its first audio delta, so an interruption
		// in between still discards that response's late audio.
		media.StartInboundResponse(sharedaudio.PlaybackResponse{ResponseID: firstStringField(event.Data, "response.id")})
	case models.SessionEventResponseOutputAudioDelta:
		format := realtimeAudioMediaType(event.Data)
		if format != "" && format != realtimePCMAudioFormat {
			err = fmt.Errorf("OpenAI Realtime RTC audio format %q is not PCM16", format)
			break
		}
		response := realtimePlaybackResponse(event.Data)
		if response.HasIdentity() {
			media.StartInboundResponse(response)
		}
		data, decodeErr := decodeOpenAIRealtimeAudioDelta(event.Data)
		if decodeErr != nil {
			err = decodeErr
		} else if len(data) > 0 {
			var samples []int16
			samples, err = codec.DecodePCM16(data)
			if err == nil {
				err = media.PushInbound(samples)
			}
		}
	case models.SessionEventResponseOutputAudioDone:
		err = media.FlushInbound()
	}
	if err != nil && !errors.Is(err, sharedaudio.ErrSessionMediaClosed) {
		media.FailInbound(err)
	}
	return err
}

// interruptPlayback discards queued local playback of the audible response
// and truncates its conversation item at the audio the device actually
// played, as the OpenAI Realtime protocol expects on an interruption.
func (s *realtimeSession) interruptPlayback(ctx context.Context, media *sharedaudio.SessionMedia) error {
	interruption, ok := media.InterruptInbound()
	if !ok {
		return nil
	}
	truncate := models.NewConversationItemTruncateEvent(interruption.ItemID, interruption.ContentIndex, interruption.AudioEndMS)
	outcome := s.base.EnqueueEventWait(ctx, truncate)
	if outcome.OK() {
		return nil
	}
	if outcome.Err != nil {
		return outcome.Err
	}
	return fmt.Errorf("queue OpenAI Realtime conversation truncation: %s", outcome.Status)
}

// sendResponseCancel sends RESPONSE.CANCEL outside the response intent queue:
// it must reach the provider even while a default response is active, and it
// invalidates queued work from the cancelled generation.
func (s *realtimeSession) sendResponseCancel(ctx context.Context, events []models.SessionEvent) messages.SessionSendOutcome {
	s.responseWireMu.Lock()
	defer s.responseWireMu.Unlock()
	s.invalidatePendingResponseIntents()
	return s.base.EnqueueEvents(ctx, events)
}

// ProviderTurnDetection reports whether OpenAI detects user speech itself. It
// does unless the session owns its audio turn boundaries (turn_detection null).
func (s *realtimeSession) ProviderTurnDetection() bool { return !s.clientTurnBoundaries }

// interruptPlaybackForCancel applies a RESPONSE.CANCEL to local
// playback. Audio arrives faster than real time, so the cancelled response may
// still have seconds queued; that backlog is discarded and the item truncated
// at what was heard. A following server-VAD speech_started finds nothing
// audible and sends no second truncation.
func (s *realtimeSession) interruptPlaybackForCancel(ctx context.Context) {
	media := s.base.CurrentRTCMedia()
	if media == nil {
		return
	}
	if err := s.interruptPlayback(ctx, media); err != nil && !errors.Is(err, sharedaudio.ErrSessionMediaClosed) {
		s.base.Logger().Warn("openai: playback interruption after response cancel failed", logging.Field{Key: "error", Value: err})
	}
}

func realtimePlaybackResponse(data json.RawMessage) sharedaudio.PlaybackResponse {
	var payload struct {
		ResponseID   string `json:"response_id"`
		ItemID       string `json:"item_id"`
		ContentIndex int    `json:"content_index"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return sharedaudio.PlaybackResponse{}
	}
	return sharedaudio.PlaybackResponse{
		ResponseID: payload.ResponseID, ItemID: payload.ItemID, ContentIndex: payload.ContentIndex,
	}
}

func decodeOpenAIRealtimeAudioDelta(data []byte) ([]byte, error) {
	encoded := firstStringField(data, "delta")
	if encoded == "" {
		return nil, nil
	}
	decoded, err := codec.DecodeBase64(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode OpenAI Realtime RTC audio delta: %w", err)
	}
	if err := codec.ValidatePCM16(decoded, codec.MaxPCM16Bytes); err != nil {
		if errors.Is(err, codec.ErrPCM16OddLength) {
			return nil, fmt.Errorf("decode OpenAI Realtime RTC audio delta: PCM16 audio delta has odd byte length %d: %w", len(decoded), err)
		}
		return nil, fmt.Errorf("decode OpenAI Realtime RTC audio delta: %w", err)
	}
	return decoded, nil
}

func realtimeAudioBytes(data json.RawMessage) []byte {
	encoded := firstStringField(data, "delta")
	if encoded == "" {
		return nil
	}
	decoded, err := codec.DecodeBase64(encoded)
	if err != nil {
		return nil
	}
	return decoded
}

func realtimeAudioMediaType(data json.RawMessage) string {
	format := firstStringField(data, "format", "format.type", "audio_format", "response.audio.output.format.type", "response.output_audio_format")
	switch format {
	case "pcm16":
		return realtimePCMAudioFormat
	case "g711_ulaw":
		return "audio/g711-ulaw"
	case "g711_alaw":
		return "audio/g711-alaw"
	default:
		return format
	}
}

// realtimePCMAudioFormat is the realtime wire name for raw PCM16 audio.
const realtimePCMAudioFormat = "audio/pcm"
