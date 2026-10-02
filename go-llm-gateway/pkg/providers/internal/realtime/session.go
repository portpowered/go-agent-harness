// Package realtime is the WebSocket session skeleton shared by the providers
// that speak the OpenAI Realtime wire conventions (OpenAI and Grok): the
// bounded send/receive queues, the read and write loops, terminal-error and
// close bookkeeping, outbound drain, wire encoding and the RTC media endpoint.
// Provider-specific event mapping and response admission stay in each
// provider; they plug in through Handler and Config.
package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/logging"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

// queueCapacity bounds both the outbound wire queue and the normalized
// inbound buffer.
const queueCapacity = 64

// wireTextMessage is the WebSocket text frame type.
const wireTextMessage = 1

// Config is the provider-specific part of a session skeleton.
type Config struct {
	// LogPrefix starts every log message, e.g. "openai realtime" or "grok".
	LogPrefix string
	// MediaName names the provider in RTC media errors, e.g. "OpenAI".
	MediaName string
	// WriteBackpressure makes ordinary outbound events wait for queue space
	// instead of failing with buffer-full.
	WriteBackpressure bool
	// LosslessInbound applies backpressure to the read loop when the
	// normalized receive buffer is full; otherwise the message is dropped
	// and counted.
	LosslessInbound bool
	// OutputSampleRate and InputSampleRate are the provider audio rates; zero
	// selects the media default and DefaultInputSampleRate respectively.
	OutputSampleRate, InputSampleRate int
	// WriteMediaFrame sends one captured RTC PCM frame to the provider.
	WriteMediaFrame func(context.Context, sharedaudio.PCMFrame) error
	// InterruptPlayback discards local playback for InterruptLocalPlayback.
	InterruptPlayback func(context.Context)
	// OnClose runs once inside Close, after Done is closed and before the
	// media and connection are released.
	OnClose func()
	// Clock drives the provider's timers (CloseGracefully and any
	// provider-owned idle timers). Nil selects the host clock, which is
	// virtual inside a testing/synctest bubble.
	Clock clock.TimerSource
}

// Session is the provider-neutral realtime WebSocket session. Providers embed
// it and supply event handling through Handler.
type Session struct {
	conn   transport.Conn
	logger logging.Logger
	cfg    Config

	// sendQueue buffers client-to-provider wire events; recvBuf buffers
	// normalized provider-to-client messages. Overflow drops are counted and
	// logged through the default observers.
	sendQueue *messages.TypedBuffer[models.SessionEvent]
	recvBuf   *messages.TypedBuffer[messages.StreamMessage]
	outbound  providers.OutboundWireDrain

	done        chan struct{}
	closeOnce   sync.Once
	errMu       sync.Mutex
	terminalErr error

	media mediaSlot
}

// NewSession returns a session over conn. A nil logger discards records.
func NewSession(conn transport.Conn, logger logging.Logger, cfg Config) *Session {
	if logger == nil {
		logger = logging.DummyLogger()
	}
	if cfg.Clock == nil {
		cfg.Clock = clock.Real{}
	}
	s := &Session{
		conn:      conn,
		logger:    logger,
		cfg:       cfg,
		sendQueue: messages.NewTypedBuffer[models.SessionEvent](queueCapacity),
		recvBuf:   messages.NewTypedBuffer[messages.StreamMessage](queueCapacity),
		done:      make(chan struct{}),
	}
	providers.AttachSessionDropLoggers(logger, s.sendQueue, s.recvBuf)
	return s
}

// Logger returns the session logger.
func (s *Session) Logger() logging.Logger { return s.logger }

// Clock returns the clock that drives the session's timers.
func (s *Session) Clock() clock.TimerSource { return s.cfg.Clock }

// SendQueue returns the outbound wire queue.
func (s *Session) SendQueue() *messages.TypedBuffer[models.SessionEvent] { return s.sendQueue }

// Receive returns the normalized inbound buffer. The first call releases RTC
// media prepared speculatively for a caller that never claimed it.
func (s *Session) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	s.releaseUnclaimedRTCMedia()
	return s.recvBuf
}

// InputDrops reports cumulative drops on the client-to-provider send queue.
func (s *Session) InputDrops() int64 { return s.sendQueue.Drops() }

// OutputDrops reports cumulative drops on the provider-to-client receive buffer.
func (s *Session) OutputDrops() int64 { return s.recvBuf.Drops() }

// Done returns a channel closed when the session terminates.
func (s *Session) Done() <-chan struct{} { return s.done }

// Closed reports whether the session has terminated.
func (s *Session) Closed() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

// Stopping reports whether the session or its lifetime context has ended.
func (s *Session) Stopping(ctx context.Context) bool {
	return s.Closed() || ctx.Err() != nil
}

// TerminalError returns the unexpected provider-side transport or protocol
// error that terminated the session, if one was observed. A clean caller-side
// Close and context cancellation do not set this value.
func (s *Session) TerminalError() error {
	if s == nil {
		return nil
	}
	s.errMu.Lock()
	defer s.errMu.Unlock()
	return s.terminalErr
}

// SetTerminalError records the first terminal error.
func (s *Session) SetTerminalError(err error) {
	if err == nil {
		return
	}
	s.errMu.Lock()
	if s.terminalErr == nil {
		s.terminalErr = err
	}
	s.errMu.Unlock()
}

// Close terminates the session, closing the connection and signalling Done.
func (s *Session) Close() error {
	var closeErr error
	s.closeOnce.Do(func() {
		close(s.done)
		if s.cfg.OnClose != nil {
			s.cfg.OnClose()
		}
		closeErr = errors.Join(s.CurrentRTCMedia().Close(), s.conn.Close())
	})
	return closeErr
}

// CloseGracefully runs a protocol close handshake, then closes the session.
// It admits closeEvent to the outbound queue and waits until settled is
// closed (the provider acknowledged the close), the session ends on its own,
// ctx ends, or timeout passes on the session clock, whichever comes first.
func (s *Session) CloseGracefully(ctx context.Context, closeEvent models.SessionEvent, settled <-chan struct{}, timeout time.Duration) error {
	if s.Closed() {
		return s.Close()
	}
	ctx, cancel, err := clock.WithTimeout(ctx, s.cfg.Clock, timeout)
	if err != nil {
		return errors.Join(err, s.Close())
	}
	defer cancel()
	if outcome := s.EnqueueEventWait(ctx, closeEvent); outcome.OK() {
		select {
		case <-settled:
		case <-s.done:
		case <-ctx.Done():
			s.logger.Warn(s.cfg.LogPrefix+": close handshake timed out", logging.Field{Key: "timeout", Value: timeout})
		}
	}
	return s.Close()
}

// CloseWithLog closes the session from a background loop that has no caller
// to return the close result to; a failure is reported through the logger.
func (s *Session) CloseWithLog() {
	if err := s.Close(); err != nil {
		s.logger.Warn(s.cfg.LogPrefix+": session close error", logging.Field{Key: "error", Value: err})
	}
}

// FlushOutbound waits until events already admitted to the provider queue have
// completed their websocket writes. Queue admission alone is insufficient for
// a finite audio boundary: the runtime may close the session immediately after
// the commit control is acknowledged.
func (s *Session) FlushOutbound(ctx context.Context) error {
	if s == nil {
		return nil
	}
	return s.outbound.Flush(ctx, s.done, s.TerminalError)
}

// ContextOutcome maps a finished context to its send outcome.
func ContextOutcome(ctx context.Context) messages.SessionSendOutcome {
	err := ctx.Err()
	if errors.Is(err, context.DeadlineExceeded) {
		return messages.SessionSendOutcome{Status: messages.SessionSendTimedOut, Err: err}
	}
	return messages.SessionSendOutcome{Status: messages.SessionSendCancelled, Err: err}
}

// EnqueueEvents admits events to the outbound wire queue in order, using the
// configured write mode, and stops at the first event that is not admitted.
func (s *Session) EnqueueEvents(ctx context.Context, events []models.SessionEvent) messages.SessionSendOutcome {
	for _, event := range events {
		// A terminated session reports closed regardless of remaining
		// outbound buffer capacity.
		if s.Closed() {
			return messages.SessionSendOutcome{Status: messages.SessionSendClosed}
		}
		if outcome := sendOutcome(s.EnqueueEvent(ctx, event)); !outcome.OK() {
			return outcome
		}
	}
	return messages.SessionSendOutcome{Status: messages.SessionSendSucceeded}
}

func sendOutcome(outcome messages.BufferWriteOutcome) messages.SessionSendOutcome {
	switch outcome.Status {
	case messages.BufferWriteSucceeded:
		return messages.SessionSendOutcome{Status: messages.SessionSendSucceeded}
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

// EnqueueEvent admits one event using the configured write mode.
func (s *Session) EnqueueEvent(ctx context.Context, event models.SessionEvent) messages.BufferWriteOutcome {
	return s.enqueue(ctx, event, s.cfg.WriteBackpressure)
}

// EnqueueEventWait admits one event, waiting for queue space. Clocked media
// sources use it so a briefly slow writer is not mistaken for audio loss.
func (s *Session) EnqueueEventWait(ctx context.Context, event models.SessionEvent) messages.BufferWriteOutcome {
	return s.enqueue(ctx, event, true)
}

func (s *Session) enqueue(ctx context.Context, event models.SessionEvent, backpressure bool) messages.BufferWriteOutcome {
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

// WriteEvent serializes event as one flat JSON object, its Data fields plus
// "type", and writes it to the connection. Empty or JSON null Data writes a
// type-only event.
//
// Data that is not a JSON object is an error, and the write loop treats it as
// a terminal write failure. This is deliberate and differs from Grok's
// pre-#616 writer, which dropped undecodable Data and sent a type-only event:
// stripping the payload silently sends a different event (an audio append
// without audio, a tool result without output) and hides the encoder bug that
// produced it. Every outbound event is built by json.Marshal of an object, so
// in practice this fires only on such a bug.
func (s *Session) WriteEvent(event models.SessionEvent) error {
	payload := map[string]json.RawMessage{}
	if len(event.Data) > 0 {
		if err := json.Unmarshal(event.Data, &payload); err != nil {
			return fmt.Errorf("unmarshal event payload: %w", err)
		}
		if payload == nil { // JSON null
			payload = map[string]json.RawMessage{}
		}
	}
	typeBytes, err := json.Marshal(event.Type)
	if err != nil {
		return err
	}
	payload["type"] = typeBytes
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return s.conn.WriteMessage(wireTextMessage, data)
}

// ParseEvent decodes one provider frame into a SessionEvent whose Data is the
// full frame.
func ParseEvent(raw []byte) (models.SessionEvent, error) {
	var envelope struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return models.SessionEvent{}, fmt.Errorf("unmarshal event type: %w", err)
	}
	if envelope.Type == "" {
		return models.SessionEvent{}, errors.New("event missing type field")
	}
	return models.SessionEvent{Type: models.SessionEventType(envelope.Type), Data: raw}, nil
}

// TryDeliver writes one normalized message to the inbound buffer if it fits;
// a full buffer drops and counts it. Providers that deliver messages outside
// HandleEvent use it.
func (s *Session) TryDeliver(msg messages.StreamMessage) bool {
	return s.recvBuf.TryWrite(msg).OK()
}

// DeliverWait writes one normalized message to the inbound buffer, waiting
// for space until ctx ends or the session terminates. Unlike the read loop's
// delivery, an ended ctx does not close the session: the caller decides what
// a stopped write means.
func (s *Session) DeliverWait(ctx context.Context, msg messages.StreamMessage) messages.BufferWriteOutcome {
	return s.recvBuf.WriteWaitContextOrDone(ctx, s.done, msg)
}

// WriteTerminal delivers a terminal record to the normalized inbound buffer,
// evicting the oldest ordinary record when it is full.
func (s *Session) WriteTerminal(msg messages.StreamMessage) bool {
	return s.recvBuf.WriteTerminal(msg)
}
