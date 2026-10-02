package openailive

import (
	"context"
	"sync"
	"sync/atomic"
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
	format           AudioFormat
	clock            clock.TimerSource
	segmentGap       time.Duration
	delegationSettle time.Duration
	closeTimeout     time.Duration
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

	// closing is done once the close handshake starts; startClosing ends it.
	closing      <-chan struct{}
	startClosing context.CancelFunc

	// mu guards the segmenter, the delegation tracker and the outbox
	// (outbox.go), so the read loop, the idle watcher and RESPONSE.CANCEL
	// emit in one order. It is never held while waiting for the reader of
	// Receive.
	mu          sync.Mutex
	segments    *segmenter
	delegations *delegationTracker
	watching    bool
	// armed is the deadline the running watcher sleeps until; rearm wakes
	// it when an earlier deadline appears.
	armed   time.Time
	rearm   chan struct{}
	outbox  []outboxEntry
	backlog int
	// pumpExited is set once the pump has drained after the session ended;
	// later entries are written directly.
	pumpExited bool
	// pumpWake wakes the pump; drained reports its progress.
	pumpWake chan struct{}
	drained  chan struct{}

	// sendMu orders audio appends and holds a trailing odd PCM byte.
	sendMu  sync.Mutex
	oddByte []byte
	// appendMu serializes context appends so their chunks never interleave;
	// appends numbers their event ids.
	appendMu sync.Mutex
	appends  atomic.Int64
}

func newLiveSession(conn transport.Conn, logger logging.Logger, settings sessionSettings) *liveSession {
	s := &liveSession{
		format:       settings.format,
		mediaType:    settings.format.Type,
		closeTimeout: settings.closeTimeout,
		closed:       make(chan struct{}),
		pumpWake:     make(chan struct{}, 1),
		rearm:        make(chan struct{}, 1),
		drained:      make(chan struct{}, 1),
		segments:     newSegmenter(settings.segmentGap, settings.format),
		delegations:  newDelegationTracker(settings.delegationSettle, settings.segmentGap),
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
	closing, startClosing := context.WithCancel(loopCtx)
	s.closing, s.startClosing = closing.Done(), startClosing
	s.closeHandshake = func() error { return s.closeWith(loopCtx) }
	s.base.PrepareRTCMedia()
	s.mu.Lock()
	s.emitLocked(
		messages.StreamMessage{Type: messages.StreamTypeSessionOpen, Value: messages.NewSessionOpenValue(started.ID, sessionModeAudio)},
		messages.StreamMessage{Type: messages.StreamTypeSessionCreated, Value: messages.NewSessionCreatedValue(started.ID, started.Model)},
	)
	s.mu.Unlock()
	go s.pump(closing)
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
		// From here the reader may have stopped: inbound delivery must not
		// wait for it, or the read loop never reaches session.closed.
		s.startClosing()
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
func (s *liveSession) ReadEnded(_ context.Context, err error) bool {
	s.base.SetTerminalError(err)
	s.base.Logger().Warn("openai live: socket closed before session.closed", logging.Field{Key: "error", Value: err})
	s.mu.Lock()
	out, segmentOpen := s.finishLocked()
	s.emitLocked(out...)
	s.emitTerminalLocked(messages.StreamMessage{
		Type: messages.StreamTypeSessionClose,
		Value: messages.NewSessionCloseValueWithTerminal(s.sessionID, reasonFinalizationUnconfirmed, providers.ErrorClassTransport,
			messages.TerminalReasonTerminalFailure, messages.TerminalProvenanceProvider, outputState(segmentOpen)),
	})
	s.mu.Unlock()
	s.awaitBacklog(0, true)
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

// watchLocked starts the idle watcher if something can fall due and no
// watcher runs, or wakes the running watcher when something now falls due
// before the deadline it sleeps until (a delegation settle window inside an
// open segment's gap, for example). Callers hold mu.
func (s *liveSession) watchLocked() {
	next, pending := s.nextDeadlineLocked()
	if !pending {
		return
	}
	if s.watching {
		if next.Before(s.armed) {
			select {
			case s.rearm <- struct{}{}:
			default:
			}
		}
		return
	}
	s.watching = true
	go s.watch()
}

// nextDeadlineLocked is the earliest time the segmenter or the delegation
// tracker has work due. Callers hold mu.
func (s *liveSession) nextDeadlineLocked() (time.Time, bool) {
	next, pending := s.segments.nextDeadline()
	if settle, held := s.delegations.nextDeadline(); held && (!pending || settle.Before(next)) {
		next, pending = settle, true
	}
	return next, pending
}

// finishLocked closes the open segment and utterance and reports every held
// delegation at the end of the session. segmentOpen reports whether a
// segment was still open. Callers hold mu.
func (s *liveSession) finishLocked() (out []messages.StreamMessage, segmentOpen bool) {
	out, segmentOpen = s.segments.finish()
	return append(out, s.delegations.finish()...), segmentOpen
}

// watch closes segments and utterances that go quiet, and reports held
// delegations whose settle window passes, on the session clock.
// It exits when nothing is pending or the session ends.
func (s *liveSession) watch() {
	source := s.base.Clock()
	for {
		s.mu.Lock()
		deadline, pending := s.nextDeadlineLocked()
		if !pending {
			s.watching = false
			s.mu.Unlock()
			return
		}
		s.armed = deadline
		s.mu.Unlock()
		timer := source.NewTimer(deadline.Sub(source.Now()))
		select {
		case <-timer.C():
		case <-s.rearm:
			// An earlier deadline appeared: sleep until that one instead.
			timer.Stop()
			continue
		case <-s.base.Done():
			timer.Stop()
			return
		}
		s.mu.Lock()
		now := source.Now()
		s.emitLocked(s.segments.due(now)...)
		s.emitLocked(s.delegations.due(now)...)
		s.mu.Unlock()
	}
}

func outputState(segmentOpen bool) messages.TerminalOutputState {
	if segmentOpen {
		return messages.TerminalOutputPartial
	}
	return messages.TerminalOutputNotApplicable
}
