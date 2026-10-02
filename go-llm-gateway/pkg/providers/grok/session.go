package grok

import (
	"context"
	"errors"
	"io"
	"net"
	"os"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/logging"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/internal/realtime"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

var (
	_ messages.Session                   = (*grokSession)(nil)
	_ messages.SessionSendOutcomeSender  = (*grokSession)(nil)
	_ messages.SessionResponseRequester  = (*grokSession)(nil)
	_ messages.SessionResponseCapability = (*grokSession)(nil)
	_ messages.SessionDropCounters       = (*grokSession)(nil)
	_ messages.SessionOutboundFlusher    = (*grokSession)(nil)
	_ sharedaudio.MediaSession           = (*grokSession)(nil)
	_ realtime.Handler                   = (*grokSession)(nil)
)

// grokSession wraps a WebSocket connection as a bidirectional StreamMessage
// session. The shared realtime skeleton owns the queues, loops and RTC media;
// this type translates between StreamMessages and the Grok wire protocol
// (OpenAI Realtime API conventions).
type grokSession struct {
	// Surface promotes only the caller-facing session methods; base is the
	// skeleton itself, whose mutators stay private to this provider.
	realtime.Surface
	base *realtime.Session
}

// grokSessionSettings are the per-connection session options.
type grokSessionSettings struct {
	outputSampleRate, inputSampleRate int
}

func newGrokSession(conn transport.Conn, logger logging.Logger) *grokSession {
	return newConfiguredGrokSession(conn, logger, grokSessionSettings{})
}

func newConfiguredGrokSession(conn transport.Conn, logger logging.Logger, settings grokSessionSettings) *grokSession {
	s := &grokSession{}
	s.base = realtime.NewSession(conn, logger, realtime.Config{
		LogPrefix:        "grok",
		MediaName:        "Grok",
		OutputSampleRate: settings.outputSampleRate,
		InputSampleRate:  settings.inputSampleRate,
		WriteMediaFrame:  s.writeRTCMediaFrame,
		// Grok audio deltas carry no conversation item identity, so no
		// provider-side truncation is possible.
		InterruptPlayback: func(context.Context) { s.interruptRTCPlayback() },
	})
	s.Surface = s.base.Surface()
	return s
}

// start launches the read and write goroutines.
func (s *grokSession) start(ctx context.Context) { s.base.Start(ctx, s) }

func (s *grokSession) writeLoop(ctx context.Context) { s.base.WriteLoop(ctx, s) }

// Send writes a StreamMessage to the session's outbound queue.
// It translates the StreamMessage to a Grok wire event. Returns false
// if the context is cancelled, the session is done, or the message type
// has no outbound representation.
func (s *grokSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	return s.SendWithOutcome(ctx, msg).OK()
}

// SendWithOutcome writes a StreamMessage to the outbound queue and reports the
// precise public lifecycle outcome.
func (s *grokSession) SendWithOutcome(ctx context.Context, msg messages.StreamMessage) messages.SessionSendOutcome {
	event, ok := translateOutbound(msg)
	if !ok {
		return messages.SessionSendOutcome{Status: messages.SessionSendTerminalFailure}
	}
	events := []models.SessionEvent{event}
	if msg.Type == messages.StreamTypeMessageEnd {
		// Finite client-side audio turns need an explicit response request after
		// committing their input buffer. This mirrors the OpenAI realtime
		// adapter and keeps Grok device probes from waiting for server-side VAD.
		events = append(events, models.NewResponseCreateEvent())
	}
	outcome := s.sendEvents(ctx, events)
	if outcome.OK() && messages.CancelStopsPlayback(msg) {
		// Audio arrives faster than real time; stop the cancelled response's
		// backlog that is still queued for local playback.
		s.interruptRTCPlayback()
	}
	return outcome
}

// RequestResponse starts a response without adding another user turn. This is
// needed when a tool result follows an audio-only input, whose history has no
// text event that can request the continuation.
func (s *grokSession) RequestResponse(ctx context.Context) messages.SessionSendOutcome {
	return s.SendWithOutcome(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeResponseCreate,
		Value: messages.NewResponseCreateValue(),
	})
}

// SupportsResponseRequests reports that RequestResponse is implemented.
func (*grokSession) SupportsResponseRequests() bool { return true }

// ProviderTurnDetection reports that Grok always runs server VAD.
func (*grokSession) ProviderTurnDetection() bool { return true }

func (s *grokSession) sendEvents(ctx context.Context, events []models.SessionEvent) messages.SessionSendOutcome {
	if ctx.Err() != nil {
		return realtime.ContextOutcome(ctx)
	}
	return s.base.EnqueueEvents(ctx, events)
}

// HandleEvent forwards provider audio to the RTC media path and translates the
// event for the normalized stream.
func (s *grokSession) HandleEvent(_ context.Context, event models.SessionEvent) []messages.StreamMessage {
	s.publishRTCMediaWithLog(event)
	return translateInbound(event)
}

// ExpectedReadClose treats a plain EOF or closed connection as an orderly
// close, unless a probe injected it or replay diverged.
func (*grokSession) ExpectedReadClose(err error) bool {
	expected := errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) || errors.Is(err, os.ErrClosed)
	return expected && !transport.IsInjectedFault(err) && !errors.Is(err, providers.ErrReplayMismatch)
}

// ExpectedWriteClose treats any write failure during shutdown as orderly;
// every other Grok write failure is terminal.
func (s *grokSession) ExpectedWriteClose(ctx context.Context, _ error) bool {
	return s.base.Stopping(ctx)
}

// EventWritten has no Grok-specific bookkeeping.
func (*grokSession) EventWritten(models.SessionEvent) {}
