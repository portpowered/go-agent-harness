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

var _ sharedaudio.MediaSession = (*realtimeSession)(nil)

// InitialSessionConfigSent reports that ConnectSession already sent the
// provider-owned session.update before the read loop started.
func (*realtimeSession) InitialSessionConfigSent() bool { return true }

// RTCMedia exposes provider-owned PCM media endpoints for an OpenAI Realtime
// session. Inbound audio remains available through Receive while also being
// framed for the local RTC device sink.
func (s *realtimeSession) RTCMedia() sharedaudio.MediaEndpoints {
	return s.rtcMedia(sharedaudio.MediaSessionOptions{})
}

func (s *realtimeSession) RTCMediaWithOptions(options sharedaudio.MediaSessionOptions) sharedaudio.MediaEndpoints {
	return s.rtcMedia(options)
}

func (s *realtimeSession) rtcMedia(options sharedaudio.MediaSessionOptions) sharedaudio.MediaEndpoints {
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
			s.setTerminalError(fmt.Errorf("close replaced OpenAI RTC media: %w", err))
		}
	}
	return endpoints
}

func (s *realtimeSession) prepareRTCMedia() {
	s.mediaMu.Lock()
	if s.media == nil {
		s.media = sharedaudio.NewSessionMediaAtRate(s.writeRTCMediaFrame, s.mediaSampleRate)
		s.mediaContinuous = false
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
	s.mediaContinuous = false
	s.mediaMu.Unlock()
	if err := media.Close(); err != nil {
		s.logger.Warn("openai realtime: release unclaimed RTC media", logging.Field{Key: "error", Value: err})
	}
}

func (s *realtimeSession) currentRTCMedia() *sharedaudio.SessionMedia {
	s.mediaMu.Lock()
	defer s.mediaMu.Unlock()
	return s.media
}

func (s *realtimeSession) writeRTCMediaFrame(ctx context.Context, frame sharedaudio.PCMFrame) error {
	encoded, err := codec.EncodePCM16Base64WithLimit(frame.Samples, codec.MaxPCM16Bytes)
	if err != nil {
		return fmt.Errorf("encode OpenAI Realtime RTC audio: %w", err)
	}
	select {
	case <-s.done:
		return fmt.Errorf("OpenAI Realtime RTC media write: session closed")
	default:
	}
	// Hardware capture is a continuous, clocked source. Backpressure it when
	// the WebSocket writer is briefly behind instead of treating a transient
	// full control queue as terminal audio loss.
	outcome := s.enqueueWireEventWait(ctx, models.NewAudioBufferAppendEvent(encoded))
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
	outcome := s.enqueueWireEventWait(ctx, truncate)
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
	return s.enqueueWireEvents(ctx, events)
}

// ProviderTurnDetection reports whether OpenAI detects user speech itself. It
// does unless the session owns its audio turn boundaries (turn_detection null).
func (s *realtimeSession) ProviderTurnDetection() bool { return !s.clientTurnBoundaries }

// LocalPlayback reports provider audio still queued for or audible on the
// local device, which outlives response.done.
func (s *realtimeSession) LocalPlayback() messages.LocalPlaybackState {
	activity := s.currentRTCMedia().PlaybackActivity()
	return messages.LocalPlaybackState{Active: activity.Active, Level: activity.Level}
}

// InterruptLocalPlayback stops local playback and truncates the heard item.
// It is valid after response.done, when there is no response left to cancel.
func (s *realtimeSession) InterruptLocalPlayback(ctx context.Context) bool {
	if !s.LocalPlayback().Active {
		return false
	}
	s.interruptPlaybackForCancel(ctx)
	return true
}

// interruptPlaybackForCancel applies a RESPONSE.CANCEL to local
// playback. Audio arrives faster than real time, so the cancelled response may
// still have seconds queued; that backlog is discarded and the item truncated
// at what was heard. A following server-VAD speech_started finds nothing
// audible and sends no second truncation.
func (s *realtimeSession) interruptPlaybackForCancel(ctx context.Context) {
	media := s.currentRTCMedia()
	if media == nil {
		return
	}
	if err := s.interruptPlayback(ctx, media); err != nil && !errors.Is(err, sharedaudio.ErrSessionMediaClosed) {
		s.logger.Warn("openai: playback interruption after response cancel failed", logging.Field{Key: "error", Value: err})
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

func (s *realtimeSession) enqueueWireEvents(ctx context.Context, events []models.SessionEvent) messages.SessionSendOutcome {
	for _, event := range events {
		// A terminated session reports closed regardless of remaining
		// outbound buffer capacity.
		select {
		case <-s.done:
			return messages.SessionSendOutcome{Status: messages.SessionSendClosed}
		default:
		}
		outcome := s.enqueueWireEvent(ctx, event)
		switch outcome.Status {
		case messages.BufferWriteSucceeded:
		case messages.BufferWriteBufferFull:
			return messages.SessionSendOutcome{Status: messages.SessionSendBufferFull}
		case messages.BufferWriteStopped:
			return messages.SessionSendOutcome{Status: messages.SessionSendClosed, Err: outcome.Err}
		case messages.BufferWriteCancelled:
			return messages.SessionSendOutcome{Status: messages.SessionSendCancelled, Err: outcome.Err}
		case messages.BufferWriteTimedOut:
			return messages.SessionSendOutcome{Status: messages.SessionSendTimedOut, Err: outcome.Err}
		default:
			return messages.SessionSendOutcome{Status: messages.SessionSendTerminalFailure}
		}
	}
	return messages.SessionSendOutcome{Status: messages.SessionSendSucceeded}
}

func (s *realtimeSession) enqueueWireEvent(ctx context.Context, event models.SessionEvent) messages.BufferWriteOutcome {
	return s.enqueueWireEventWithMode(ctx, event, s.writeBackpressure)

}

func (s *realtimeSession) enqueueWireEventWait(ctx context.Context, event models.SessionEvent) messages.BufferWriteOutcome {
	return s.enqueueWireEventWithMode(ctx, event, true)
}

func (s *realtimeSession) enqueueWireEventWithMode(ctx context.Context, event models.SessionEvent, backpressure bool) messages.BufferWriteOutcome {
	s.outbound.Begin()
	var outcome messages.BufferWriteOutcome
	if backpressure {
		outcome = s.sendQueue.WriteWaitContextOrDone(ctx, s.done, event)
	} else {
		outcome = s.sendQueue.WriteContext(ctx, event)
	}
	if !outcome.OK() {
		s.outbound.Complete()
	}
	return outcome
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

// closeWithLog closes the session from a background loop that has no caller
// to return the close result to; a failure is reported through the logger.
func (s *realtimeSession) closeWithLog() {
	if err := s.Close(); err != nil {
		s.logger.Warn("openai realtime: session close error", logging.Field{Key: "error", Value: err})
	}
}
