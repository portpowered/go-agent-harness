package openailive

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/logging"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/internal/realtime"
)

// ErrNoWireEvent reports a stream message GPT-Live has no channel for:
// response requests, tool results and typed text. Senders must not treat it
// as delivered.
var ErrNoWireEvent = errors.New("openailive: GPT-Live has no wire event for this message")

// Send writes msg to the session. See SendWithOutcome.
func (s *liveSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	return s.SendWithOutcome(ctx, msg).OK()
}

// SendWithOutcome maps one stream message onto the GPT-Live wire:
//
//   - AUDIO.DELTA becomes session.input_audio.append; a trailing odd PCM byte
//     is held back and joined to the next chunk;
//   - MESSAGE.END and SESSION.UPDATE succeed with no wire event: GPT-Live
//     decides when to speak, and its startup fields are immutable;
//   - RESPONSE.CANCEL has no wire event either: it ends the open speech
//     segment as cancelled, drops its later output and interrupts local
//     playback unless KeepPlayback is set;
//   - RESPONSE.CREATE, TOOLCALL.END, TEXT.DELTA and every other type fail
//     with a terminal-failure outcome, never a silent success.
func (s *liveSession) SendWithOutcome(ctx context.Context, msg messages.StreamMessage) messages.SessionSendOutcome {
	if ctx.Err() != nil {
		return realtime.ContextOutcome(ctx)
	}
	if s.base.Closed() {
		return messages.SessionSendOutcome{Status: messages.SessionSendClosed}
	}
	switch msg.Type { //nolint:exhaustive // GPT-Live accepts audio and a few local controls; every other type fails below.
	case messages.StreamTypeAudioDelta:
		value, ok := msg.Value.(*messages.AudioDeltaValue)
		if !ok || value == nil {
			return s.noWireEvent(msg)
		}
		return s.appendAudio(ctx, value.Content)
	case messages.StreamTypeMessageEnd, messages.StreamTypeSessionUpdate:
		s.base.Logger().Debug("openai live: no wire event", logging.Field{Key: "stream_type", Value: string(msg.Type)})
		return messages.SessionSendOutcome{Status: messages.SessionSendSucceeded}
	case messages.StreamTypeResponseCancel:
		s.cancelSegment(msg)
		return messages.SessionSendOutcome{Status: messages.SessionSendSucceeded}
	default:
		return s.noWireEvent(msg)
	}
}

func (s *liveSession) noWireEvent(msg messages.StreamMessage) messages.SessionSendOutcome {
	err := fmt.Errorf("%w: %s", ErrNoWireEvent, msg.Type)
	s.base.Logger().Error("openai live: refused stream message", logging.Field{Key: "error", Value: err})
	return messages.SessionSendOutcome{Status: messages.SessionSendTerminalFailure, Err: err}
}

// appendAudio queues one input audio chunk in the session format.
func (s *liveSession) appendAudio(ctx context.Context, audio []byte) messages.SessionSendOutcome {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	if s.format.Type == AudioTypePCM {
		if len(s.oddByte) > 0 {
			audio = append(append([]byte(nil), s.oddByte...), audio...)
			s.oddByte = nil
		}
		if len(audio)%2 != 0 {
			s.oddByte = []byte{audio[len(audio)-1]}
			audio = audio[:len(audio)-1]
		}
	}
	if len(audio) == 0 {
		return messages.SessionSendOutcome{Status: messages.SessionSendSucceeded}
	}
	// Standard base64 needs no JSON escaping, so the body is built directly.
	data := []byte(`{"audio":"` + base64.StdEncoding.EncodeToString(audio) + `"}`)
	return s.base.EnqueueEvents(ctx, []models.SessionEvent{{Type: TypeInputAudioAppend, Data: data}})
}

// cancelSegment ends the open segment locally. GPT-Live keeps no response a
// cancel could reach, so its later output for that speech is dropped here.
// Its closing messages join the session outbox without waiting (the caller
// may be the goroutine that drains Receive), in order before anything the
// read loop emits later, SESSION.CLOSE included.
func (s *liveSession) cancelSegment(msg messages.StreamMessage) {
	s.mu.Lock()
	s.emitLocked(s.segments.cancel(s.base.Clock().Now())...)
	s.watchLocked()
	s.mu.Unlock()
	if messages.CancelStopsPlayback(msg) {
		s.interruptRTCPlayback()
	}
}
