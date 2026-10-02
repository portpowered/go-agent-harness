package openailive

import (
	"context"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/logging"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/internal/realtime"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

var (
	_ messages.Session                     = (*liveSession)(nil)
	_ messages.SessionSendOutcomeSender    = (*liveSession)(nil)
	_ messages.SessionResponseCapability   = (*liveSession)(nil)
	_ messages.SessionTurnDetection        = (*liveSession)(nil)
	_ messages.SessionFullDuplex           = (*liveSession)(nil)
	_ messages.SessionDropCounters         = (*liveSession)(nil)
	_ messages.SessionOutboundFlusher      = (*liveSession)(nil)
	_ messages.SessionInitialConfigMarker  = (*liveSession)(nil)
	_ messages.SessionTerminalError        = (*liveSession)(nil)
	_ messages.SessionLocalPlayback        = (*liveSession)(nil)
	_ messages.SessionInputFormat          = (*liveSession)(nil)
	_ sharedaudio.MediaSession             = (*liveSession)(nil)
	_ sharedaudio.ConfigurableMediaSession = (*liveSession)(nil)
	_ realtime.Handler                     = (*liveSession)(nil)
	_ realtime.ReadEndHandler              = (*liveSession)(nil)
)

// sessionModeAudio is the SESSION.OPEN mode of a voice session.
const sessionModeAudio = "audio_inference"

// reasonFinalizationUnconfirmed is the SESSION.CLOSE reason of a socket that
// closed before session.closed, so final usage was never confirmed.
const reasonFinalizationUnconfirmed = "finalization_unconfirmed"

// sessionSettings are the per-connection options.
type sessionSettings struct {
	format       AudioFormat
	clock        clock.TimerSource
	segmentGap   time.Duration
	closeTimeout time.Duration
}

// liveSession is one GPT-Live primary-WebSocket session. The shared realtime
// skeleton owns the queues, loops and RTC media; this type owns the GPT-Live
// event mapping, the synthesized speech segments and the close handshake.
type liveSession struct {
	// Surface promotes only the caller-facing session methods; base is the
	// skeleton itself, whose mutators stay private to this provider.
	realtime.Surface
	base *realtime.Session

	format       AudioFormat
	mediaType    string
	closeTimeout time.Duration
	sessionID    string
	// closeHandshake runs the close handshake under the session's lifetime
	// context, which the connect context's cancellation does not reach.
	closeHandshake func() error

	// closed is closed when session.closed arrives; closedOnce guards it.
	closed     chan struct{}
	closedOnce sync.Once
	// closeOnce runs the close handshake once; closeErr is its result.
	closeOnce sync.Once
	closeErr  error

	// mu serializes the segmenter with the delivery of the messages it
	// produces, so the read loop and the idle timer emit in one order.
	mu       sync.Mutex
	segments *segmenter
	watching bool

	// sendMu orders audio appends and holds a trailing odd PCM byte.
	sendMu  sync.Mutex
	oddByte []byte
}

func newLiveSession(conn transport.Conn, logger logging.Logger, settings sessionSettings) *liveSession {
	s := &liveSession{
		format:       settings.format,
		mediaType:    settings.format.Type,
		closeTimeout: settings.closeTimeout,
		closed:       make(chan struct{}),
		segments:     newSegmenter(settings.segmentGap, settings.format),
	}
	s.base = realtime.NewSession(conn, logger, realtime.Config{
		LogPrefix:         "openai live",
		MediaName:         "OpenAI Live",
		LosslessInbound:   true,
		WriteBackpressure: true,
		OutputSampleRate:  settings.format.Rate,
		InputSampleRate:   settings.format.Rate,
		WriteMediaFrame:   s.writeRTCMediaFrame,
		InterruptPlayback: func(context.Context) { s.interruptRTCPlayback() },
		Clock:             settings.clock,
	})
	s.Surface = s.base.Surface()
	return s
}

// open queues SESSION.OPEN and SESSION.CREATED for the started session and
// starts the read and write loops.
//
// The loops run on a context that ctx's cancellation does not reach: when
// ctx ends, the session still runs the session.close handshake (which needs
// the write and read loops) before the socket closes.
func (s *liveSession) open(ctx context.Context, started SessionResource) {
	s.sessionID = started.ID
	loopCtx := context.WithoutCancel(ctx)
	s.closeHandshake = func() error { return s.closeWith(loopCtx) }
	s.base.PrepareRTCMedia()
	s.base.Deliver(loopCtx, messages.StreamMessage{Type: messages.StreamTypeSessionOpen, Value: messages.NewSessionOpenValue(started.ID, sessionModeAudio)})
	s.base.Deliver(loopCtx, messages.StreamMessage{Type: messages.StreamTypeSessionCreated, Value: messages.NewSessionCreatedValue(started.ID, started.Model)})
	s.base.Start(loopCtx, s)
	go func() {
		select {
		case <-ctx.Done():
			if err := s.closeWith(loopCtx); err != nil {
				s.base.Logger().Warn("openai live: close after context end", logging.Field{Key: "error", Value: err})
			}
		case <-s.base.Done():
		}
	}()
}

// Close runs the graceful close once: session.close, then up to the close
// timeout for session.closed, then the socket closes. Concurrent callers wait
// for the same handshake.
func (s *liveSession) Close() error { return s.closeHandshake() }

// closeWith runs the close handshake once under ctx, which the session's
// lifetime context derives without cancellation.
func (s *liveSession) closeWith(ctx context.Context) error {
	s.closeOnce.Do(func() {
		s.closeErr = s.base.CloseGracefully(ctx, models.SessionEvent{Type: TypeSessionClose}, s.closed, s.closeTimeout)
	})
	return s.closeErr
}

// ProviderTurnDetection reports that GPT-Live owns turn-taking, so the loop
// never runs its local barge-in against it.
func (*liveSession) ProviderTurnDetection() bool { return true }

// FullDuplex reports that GPT-Live listens while it speaks and handles
// interruption itself: overlapping user speech, backchannels included, is
// never a local barge-in, so the loop never cancels a segment on it.
func (*liveSession) FullDuplex() bool { return true }

// SupportsResponseRequests reports false: GPT-Live has no response request.
func (*liveSession) SupportsResponseRequests() bool { return false }

// ExpectedReadClose treats the socket closing after session.closed as
// orderly.
func (s *liveSession) ExpectedReadClose(error) bool { return s.sessionClosed() }

// ExpectedWriteClose leaves every write failure to the read loop: a socket
// that cannot be written has ended, and the read loop reports how (orderly
// after session.closed, otherwise finalization_unconfirmed).
func (s *liveSession) ExpectedWriteClose(ctx context.Context, err error) bool {
	if !s.base.Stopping(ctx) && !s.sessionClosed() {
		s.base.Logger().Warn("openai live: websocket write failed", logging.Field{Key: "error", Value: err})
	}
	return true
}

// EventWritten has no GPT-Live bookkeeping.
func (*liveSession) EventWritten(models.SessionEvent) {}

// ReadEnded reports a socket that ended before session.closed: the open
// segment and utterance close, then SESSION.CLOSE reports terminal_failure
// with reason finalization_unconfirmed, because final usage never arrived.
func (s *liveSession) ReadEnded(ctx context.Context, err error) bool {
	s.base.SetTerminalError(err)
	s.base.Logger().Warn("openai live: socket closed before session.closed", logging.Field{Key: "error", Value: err})
	s.mu.Lock()
	defer s.mu.Unlock()
	out, segmentOpen := s.segments.finish()
	s.deliverLocked(ctx, out)
	s.base.WriteTerminal(messages.StreamMessage{
		Type: messages.StreamTypeSessionClose,
		Value: messages.NewSessionCloseValueWithTerminal(s.sessionID, reasonFinalizationUnconfirmed, providers.ErrorClassTransport,
			messages.TerminalReasonTerminalFailure, messages.TerminalProvenanceProvider, outputState(segmentOpen)),
	})
	return true
}

func (s *liveSession) sessionClosed() bool {
	select {
	case <-s.closed:
		return true
	default:
		return false
	}
}

func (s *liveSession) markClosed() { s.closedOnce.Do(func() { close(s.closed) }) }

// deliverLocked emits msgs in order, keeping RTC media in step. Callers hold mu.
func (s *liveSession) deliverLocked(ctx context.Context, msgs []messages.StreamMessage) {
	for _, msg := range msgs {
		s.publishRTCMedia(msg)
		if !s.base.Deliver(ctx, msg) {
			return
		}
	}
}

// watchLocked starts the idle watcher if something can fall due and no
// watcher runs. Callers hold mu.
func (s *liveSession) watchLocked(ctx context.Context) {
	if s.watching {
		return
	}
	if _, pending := s.segments.nextDeadline(); !pending {
		return
	}
	s.watching = true
	go s.watch(ctx)
}

// watch closes segments and utterances that go quiet, on the session clock.
// It exits when nothing is pending or the session ends.
func (s *liveSession) watch(ctx context.Context) {
	source := s.base.Clock()
	for {
		s.mu.Lock()
		deadline, pending := s.segments.nextDeadline()
		if !pending {
			s.watching = false
			s.mu.Unlock()
			return
		}
		s.mu.Unlock()
		timer := source.NewTimer(deadline.Sub(source.Now()))
		select {
		case <-timer.C():
		case <-s.base.Done():
			timer.Stop()
			return
		case <-ctx.Done():
			timer.Stop()
			return
		}
		s.mu.Lock()
		s.deliverLocked(ctx, s.segments.due(source.Now()))
		s.mu.Unlock()
	}
}

func outputState(segmentOpen bool) messages.TerminalOutputState {
	if segmentOpen {
		return messages.TerminalOutputPartial
	}
	return messages.TerminalOutputNotApplicable
}
