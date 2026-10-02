package livesession

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
	_ messages.Session                     = (*Session)(nil)
	_ messages.SessionSendOutcomeSender    = (*Session)(nil)
	_ messages.SessionResponseCapability   = (*Session)(nil)
	_ messages.SessionTurnDetection        = (*Session)(nil)
	_ messages.SessionFullDuplex           = (*Session)(nil)
	_ messages.SessionDropCounters         = (*Session)(nil)
	_ messages.SessionOutboundFlusher      = (*Session)(nil)
	_ messages.SessionInitialConfigMarker  = (*Session)(nil)
	_ messages.SessionTerminalError        = (*Session)(nil)
	_ messages.SessionLocalPlayback        = (*Session)(nil)
	_ messages.SessionInputFormat          = (*Session)(nil)
	_ sharedaudio.MediaSession             = (*Session)(nil)
	_ sharedaudio.ConfigurableMediaSession = (*Session)(nil)
	_ realtime.Handler                     = (*Session)(nil)
	_ realtime.ReadEndHandler              = (*Session)(nil)
)

// sessionModeAudio is the SESSION.OPEN mode of a voice session.
const sessionModeAudio = "audio_inference"

// reasonFinalizationUnconfirmed is the SESSION.CLOSE reason of a socket that
// closed before session.closed, so final usage was never confirmed.
const reasonFinalizationUnconfirmed = "finalization_unconfirmed"

// Settings are the per-connection options.
type Settings struct {
	// Name starts every log message, for example "openai live".
	Name string
	// MediaName names the provider in RTC media errors.
	MediaName string
	// Format is the session audio format, for input and output.
	Format Format
	// Clock drives the segment, utterance and close timers.
	Clock clock.TimerSource
	// SegmentGap is the quiet gap G that ends a segment or an utterance.
	SegmentGap time.Duration
	// DelegationSettle is the settle window D a client delegation waits
	// for the user transcript that covers its offset.
	DelegationSettle time.Duration
	// CloseTimeout bounds the wait for the close acknowledgement.
	CloseTimeout time.Duration
}

// Session is one GPT-Live session over one transport.Conn. The shared
// realtime skeleton owns the queues, loops and RTC media; this type owns the
// stream mapping, the synthesized speech segments and the close handshake;
// the Dialect owns the wire vocabulary.
type Session struct {
	// Surface promotes only the caller-facing session methods; base is the
	// skeleton itself, whose mutators stay private to this provider.
	realtime.Surface
	base *realtime.Session

	name         string
	dialect      Dialect
	format       Format
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

	// mu guards the segmenter and the outbox (outbox.go), so the read loop,
	// the idle watcher and RESPONSE.CANCEL emit in one order. It is never
	// held while waiting for the reader of Receive.
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
	// appends numbers them.
	appendMu sync.Mutex
	appends  atomic.Int64
}

// New returns a session over conn that speaks dialect. Open starts it.
func New(conn transport.Conn, logger logging.Logger, settings Settings, dialect Dialect) *Session {
	s := &Session{
		name:         settings.Name,
		dialect:      dialect,
		format:       settings.Format,
		mediaType:    settings.Format.Type,
		closeTimeout: settings.CloseTimeout,
		closed:       make(chan struct{}),
		pumpWake:     make(chan struct{}, 1),
		rearm:        make(chan struct{}, 1),
		drained:      make(chan struct{}, 1),
		segments:     newSegmenter(settings.SegmentGap, settings.Format),
		delegations:  newDelegationTracker(settings.DelegationSettle, settings.SegmentGap),
	}
	s.base = realtime.NewSession(conn, logger, realtime.Config{
		LogPrefix:         settings.Name,
		MediaName:         settings.MediaName,
		LosslessInbound:   true,
		WriteBackpressure: true,
		OutputSampleRate:  settings.Format.Rate,
		InputSampleRate:   settings.Format.Rate,
		WriteMediaFrame:   s.writeRTCMediaFrame,
		InterruptPlayback: func(context.Context) { s.interruptRTCPlayback() },
		Clock:             settings.Clock,
	})
	s.Surface = s.base.Surface()
	return s
}

// Open queues SESSION.OPEN and SESSION.CREATED for the started session id
// and model, and starts the read and write loops.
//
// The loops run on a context that ctx's cancellation does not reach: when
// ctx ends, the session still runs the session.close handshake (which needs
// the write and read loops) before the socket closes.
func (s *Session) Open(ctx context.Context, id, model string) {
	s.sessionID = id
	loopCtx := context.WithoutCancel(ctx)
	closing, startClosing := context.WithCancel(loopCtx)
	s.closing, s.startClosing = closing.Done(), startClosing
	s.closeHandshake = func() error { return s.closeWith(loopCtx) }
	s.base.PrepareRTCMedia()
	s.mu.Lock()
	s.emitLocked(
		messages.StreamMessage{Type: messages.StreamTypeSessionOpen, Value: messages.NewSessionOpenValue(id, sessionModeAudio)},
		messages.StreamMessage{Type: messages.StreamTypeSessionCreated, Value: messages.NewSessionCreatedValue(id, model)},
	)
	s.mu.Unlock()
	go s.pump(closing)
	s.base.Start(loopCtx, s)
	go func() {
		select {
		case <-ctx.Done():
			if err := s.closeWith(loopCtx); err != nil {
				s.logger().Warn(s.name+": close after context end", logging.Field{Key: "error", Value: err})
			}
		case <-s.base.Done():
		}
	}()
}

// Close runs the graceful close once: session.close, then up to the close
// timeout for session.closed, then the socket closes. Concurrent callers wait
// for the same handshake.
func (s *Session) Close() error { return s.closeHandshake() }

// closeWith runs the close handshake once under ctx, which the session's
// lifetime context derives without cancellation.
func (s *Session) closeWith(ctx context.Context) error {
	s.closeOnce.Do(func() {
		// From here the reader may have stopped: inbound delivery must not
		// wait for it, or the read loop never reaches session.closed.
		s.startClosing()
		s.closeErr = s.base.CloseGracefully(ctx, s.dialect.CloseEvent(), s.closed, s.closeTimeout)
	})
	return s.closeErr
}

// ProviderTurnDetection reports that GPT-Live owns turn-taking, so the loop
// never runs its local barge-in against it.
func (*Session) ProviderTurnDetection() bool { return true }

// FullDuplex reports that GPT-Live listens while it speaks and handles
// interruption itself: overlapping user speech, backchannels included, is
// never a local barge-in, so the loop never cancels a segment on it.
func (*Session) FullDuplex() bool { return true }

// SupportsResponseRequests reports false: GPT-Live has no response request.
func (*Session) SupportsResponseRequests() bool { return false }

// ExpectedReadClose treats the socket closing after session.closed as
// orderly.
func (s *Session) ExpectedReadClose(error) bool { return s.sessionClosed() }

// ExpectedWriteClose leaves every write failure to the read loop: a socket
// that cannot be written has ended, and the read loop reports how (orderly
// after session.closed, otherwise finalization_unconfirmed).
func (s *Session) ExpectedWriteClose(ctx context.Context, err error) bool {
	if !s.base.Stopping(ctx) && !s.sessionClosed() {
		s.logger().Warn(s.name+": websocket write failed", logging.Field{Key: "error", Value: err})
	}
	return true
}

// EventWritten has no GPT-Live bookkeeping.
func (*Session) EventWritten(models.SessionEvent) {}

// ReadEnded reports a socket that ended before session.closed: the open
// segment and utterance close, then SESSION.CLOSE reports terminal_failure
// with reason finalization_unconfirmed, because final usage never arrived.
func (s *Session) ReadEnded(_ context.Context, err error) bool {
	s.base.SetTerminalError(err)
	s.logger().Warn(s.name+": socket closed before session.closed", logging.Field{Key: "error", Value: err})
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

func (s *Session) logger() logging.Logger { return s.base.Logger() }

func (s *Session) sessionClosed() bool {
	select {
	case <-s.closed:
		return true
	default:
		return false
	}
}

func (s *Session) markClosed() { s.closedOnce.Do(func() { close(s.closed) }) }

// watchLocked starts the idle watcher if something can fall due and no
// watcher runs. Callers hold mu.
func (s *Session) watchLocked() {
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
func (s *Session) nextDeadlineLocked() (time.Time, bool) {
	next, pending := s.segments.nextDeadline()
	if settle, held := s.delegations.nextDeadline(); held && (!pending || settle.Before(next)) {
		next, pending = settle, true
	}
	return next, pending
}

// finishLocked closes the open segment and utterance and reports every held
// delegation at the end of the session. segmentOpen reports whether a
// segment was still open. Callers hold mu.
func (s *Session) finishLocked() (out []messages.StreamMessage, segmentOpen bool) {
	out, segmentOpen = s.segments.finish()
	return append(out, s.delegations.finish()...), segmentOpen
}

// watch closes segments and utterances that go quiet, and reports held
// delegations whose settle window passes, on the session clock. It exits
// when nothing is pending or the session ends.
func (s *Session) watch() {
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
