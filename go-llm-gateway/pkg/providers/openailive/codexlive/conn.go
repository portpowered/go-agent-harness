package codexlive

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/logging"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/codexrtc"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/internal/livesession"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/quicksilver"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

// wireTextMessage is the frame type ReadMessage reports.
const wireTextMessage = 1

// Transport queue bounds.
const (
	// inboundFrames bounds frames waiting for the read loop. A full queue
	// makes the peer and sideband readers wait: inbound is lossless.
	inboundFrames = 64
	// pendingControl bounds control events held while the sideband
	// reconnects; the oldest is dropped beyond it.
	pendingControl = 64
)

// errConnClosed reports use of the transport after Close.
var errConnClosed = fmt.Errorf("codexlive: transport closed: %w", net.ErrClosed)

// mediaPeer is the WebRTC side the transport uses (codexrtc.Peer).
type mediaPeer interface {
	WriteFrame(ctx context.Context, samples []int16) error
	ReadFrame(ctx context.Context) ([]int16, error)
	Failed() <-chan struct{}
	Close() error
}

// control is one sideband connection (codexrtc.Sideband).
type control interface {
	Send(event quicksilver.Event) error
	Receive() (quicksilver.Event, error)
	Close() error
}

// dialControl opens a new sideband connection for the call.
type dialControl func(ctx context.Context) (control, error)

// conn presents the call's peer and sideband to the shared session state
// machine as one transport.Conn:
//
//   - ReadMessage delivers sideband events, peer audio (as typeOutputAudio
//     frames at the session rate) and the transport's own end (typeEnded), in
//     arrival order;
//   - WriteMessage sends input_audio.append as paced Opus media and every
//     other event on the sideband, holding control events while the
//     sideband reconnects.
type conn struct {
	peer   mediaPeer
	dial   dialControl
	clock  clock.TimerSource
	logger logging.Logger
	policy reconnectPolicy

	framer *inputFramer
	output *outputConverter
	pacer  *pacer

	inbound chan []byte
	// done is closed by Close; cancel ends the workers' context.
	done    <-chan struct{}
	cancel  context.CancelFunc
	workers sync.WaitGroup

	closeOnce sync.Once
	closeErr  error

	// inputMu orders input audio through the framer.
	inputMu sync.Mutex

	// mu guards the sideband state.
	mu             sync.Mutex
	side           control
	connectedAt    time.Time
	pending        []quicksilver.Event
	closeRequested bool
	ended          bool
}

var _ transport.Conn = (*conn)(nil)

// connConfig is what a conn needs beyond its peer and first sideband.
type connConfig struct {
	rate   int
	clock  clock.TimerSource
	logger logging.Logger
	policy reconnectPolicy
}

// newConn starts the transport's workers. They run until Close, on a
// context that ctx's values reach but its cancellation does not: the session
// decides when the transport ends.
func newConn(ctx context.Context, peer mediaPeer, side control, dial dialControl, cfg connConfig) (*conn, error) {
	framer, err := newInputFramer(cfg.rate)
	if err != nil {
		return nil, err
	}
	output, err := newOutputConverter(cfg.rate)
	if err != nil {
		return nil, err
	}
	workCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	c := &conn{
		peer: peer, dial: dial, clock: cfg.clock, logger: cfg.logger, policy: cfg.policy,
		framer: framer, output: output,
		inbound: make(chan []byte, inboundFrames),
		done:    workCtx.Done(), cancel: cancel,
		side: side, connectedAt: cfg.clock.Now(),
	}
	c.pacer = newPacer(cfg.clock, peer.WriteFrame, c.mediaFailed)
	c.start(func() { c.pacer.run(workCtx) })
	c.start(func() { c.readPeer(workCtx) })
	c.start(c.watchPeer)
	c.start(func() { c.readSideband(workCtx, side) })
	return c, nil
}

// closed reports whether Close has run.
func (c *conn) closed() bool {
	select {
	case <-c.done:
		return true
	default:
		return false
	}
}

func (c *conn) start(run func()) {
	c.workers.Add(1)
	go func() {
		defer c.workers.Done()
		run()
	}()
}

// ReadMessage returns the next inbound frame.
func (c *conn) ReadMessage() (int, []byte, error) {
	select {
	case frame := <-c.inbound:
		return wireTextMessage, frame, nil
	case <-c.done:
		return 0, nil, errConnClosed
	}
}

// WriteMessage routes one client event: input audio to the peer, everything
// else to the sideband. A sideband that is reconnecting holds the event; only
// a closed transport is an error.
func (c *conn) WriteMessage(_ int, data []byte) error {
	if c.closed() {
		return errConnClosed
	}
	var head struct {
		Type  string `json:"type"`
		Audio string `json:"audio"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return fmt.Errorf("codexlive: undecodable client event: %w", err)
	}
	switch head.Type {
	case quicksilver.TypeInputAudioAppend:
		return c.writeAudio(head.Audio)
	case typeInputEnd:
		c.inputMu.Lock()
		frame := c.framer.flush()
		c.inputMu.Unlock()
		if frame != nil {
			c.pacer.push(frame)
		}
		return nil
	}
	event, err := quicksilver.DecodeClientEvent(data)
	if err != nil {
		return err
	}
	c.sendControl(event)
	return nil
}

// writeAudio frames one base64 chunk of session-rate PCM for the pacer.
func (c *conn) writeAudio(encoded string) error {
	pcm, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return fmt.Errorf("codexlive: undecodable input audio: %w", err)
	}
	samples, err := codec.DecodePCM16(pcm)
	if err != nil {
		return fmt.Errorf("codexlive: input audio: %w", err)
	}
	c.inputMu.Lock()
	frames, err := c.framer.frames(samples)
	c.inputMu.Unlock()
	if err != nil {
		return err
	}
	c.pacer.push(frames...)
	return nil
}

// sendControl sends event on the current sideband, or holds it until the
// sideband is back. A failed send is held too: the reader sees the same
// failure and reconnects.
func (c *conn) sendControl(event quicksilver.Event) {
	c.mu.Lock()
	if _, closing := event.(quicksilver.SessionClose); closing {
		c.closeRequested = true
	}
	side := c.side
	if side == nil {
		c.holdLocked(event)
		c.mu.Unlock()
		return
	}
	c.mu.Unlock()
	if err := side.Send(event); err != nil {
		c.logger.Warn("openai live codex: sideband send failed; holding the event for the reconnect", logging.Field{Key: "type", Value: event.EventType()}, logging.Field{Key: "error", Value: err})
		c.mu.Lock()
		c.holdLocked(event)
		c.mu.Unlock()
	}
}

func (c *conn) holdLocked(event quicksilver.Event) {
	c.pending = append(c.pending, event)
	if over := len(c.pending) - pendingControl; over > 0 {
		c.logger.Warn("openai live codex: dropped control events held for the reconnect", logging.Field{Key: "count", Value: over})
		c.pending = c.pending[over:]
	}
}

// push hands one frame to the read loop, unless the transport closed.
func (c *conn) push(frame []byte) bool {
	select {
	case c.inbound <- frame:
		return true
	case <-c.done:
		return false
	}
}

// end reports the transport's end once: a close reason, or a credential
// failure. Later ends are ignored.
func (c *conn) end(frame endedFrame) {
	c.mu.Lock()
	if c.ended {
		c.mu.Unlock()
		return
	}
	c.ended = true
	c.mu.Unlock()
	frame.Type = typeEnded
	encoded, err := json.Marshal(frame)
	if err != nil {
		encoded = []byte(`{"type":"` + typeEnded + `","reason":"` + livesession.CloseReasonConnectionLost + `"}`)
	}
	c.push(encoded)
}

// readPeer delivers the peer's decoded audio at the session rate.
func (c *conn) readPeer(ctx context.Context) {
	for {
		frame, err := c.peer.ReadFrame(ctx)
		if err != nil {
			return
		}
		samples, silent, err := c.output.convert(frame)
		if err != nil {
			c.logger.Warn("openai live codex: output audio dropped", logging.Field{Key: "error", Value: err})
			continue
		}
		encoded, err := json.Marshal(outputAudioFrame{Type: typeOutputAudio, Audio: codec.EncodePCM16(samples), Silent: silent})
		if err != nil || !c.push(encoded) {
			return
		}
	}
}

// watchPeer ends the session when the media connection fails.
func (c *conn) watchPeer() {
	select {
	case <-c.peer.Failed():
		c.mediaFailed(codexrtc.ErrPeerFailed)
	case <-c.done:
	}
}

func (c *conn) mediaFailed(err error) {
	if c.closed() {
		return
	}
	c.logger.Warn("openai live codex: media connection lost", logging.Field{Key: "error", Value: err})
	c.end(endedFrame{Reason: livesession.CloseReasonConnectionLost, Message: err.Error()})
}

// readSideband delivers one sideband's events until it ends, then decides
// how the session goes on: a normal close ends the session, any other loss
// reconnects.
func (c *conn) readSideband(ctx context.Context, side control) {
	for {
		event, err := side.Receive()
		if err == nil {
			c.deliverEvent(event)
			continue
		}
		if errors.Is(err, quicksilver.ErrMalformedEvent) {
			c.logger.Warn("openai live codex: malformed sideband event", logging.Field{Key: "error", Value: err})
			continue
		}
		c.sidebandLost(ctx, side, err)
		return
	}
}

func (c *conn) deliverEvent(event quicksilver.Event) {
	frame, err := quicksilver.EncodeEvent(event)
	if err != nil {
		c.logger.Warn("openai live codex: sideband event not re-encodable", logging.Field{Key: "error", Value: err})
		return
	}
	c.push(frame)
}

func (c *conn) sidebandLost(ctx context.Context, side control, err error) {
	if c.closed() {
		return
	}
	c.mu.Lock()
	c.side = nil
	closeRequested := c.closeRequested
	connectedFor := c.clock.Now().Sub(c.connectedAt)
	c.mu.Unlock()
	if closeErr := side.Close(); closeErr != nil {
		c.logger.Debug("openai live codex: close lost sideband", logging.Field{Key: "error", Value: closeErr})
	}
	if errors.Is(err, codexrtc.ErrSidebandClosed) {
		// A normal close is the end of the call, as Codex and OpenClaw treat
		// it: the answer to session.close, or the backend hanging up.
		reason := livesession.CloseReasonRemoteHangup
		if closeRequested {
			reason = livesession.CloseReasonCloseRequested
		}
		c.end(endedFrame{Reason: reason})
		return
	}
	c.logger.Warn("openai live codex: sideband lost; reconnecting", logging.Field{Key: "error", Value: err})
	c.reconnect(ctx, connectedFor)
}

// reconnect waits out the backoff for this loss, redials, and on success
// resumes reading and flushes held control events.
func (c *conn) reconnect(ctx context.Context, connectedFor time.Duration) {
	delay := c.policy.delayAfterLoss(connectedFor)
	if !c.sleep(delay) {
		return
	}
	side, err := c.redial(ctx)
	if err != nil {
		if !c.closed() {
			c.end(endFor(err))
		}
		return
	}
	c.mu.Lock()
	if c.closed() {
		c.mu.Unlock()
		if closeErr := side.Close(); closeErr != nil {
			c.logger.Debug("openai live codex: close redialed sideband", logging.Field{Key: "error", Value: closeErr})
		}
		return
	}
	c.side, c.connectedAt = side, c.clock.Now()
	held := c.pending
	c.pending = nil
	c.mu.Unlock()
	c.logger.Info("openai live codex: sideband reconnected")
	c.start(func() { c.readSideband(ctx, side) })
	for _, event := range held {
		c.sendControl(event)
	}
}

// redial tries the sideband up to the policy's attempts, with backoff
// between them. A call that ended or a rejected credential stops at once.
func (c *conn) redial(ctx context.Context) (control, error) {
	var lastErr error
	for attempt := range c.policy.attempts {
		if attempt > 0 && !c.sleep(c.policy.retryDelay(attempt)) {
			return nil, errConnClosed
		}
		side, err := c.dial(ctx)
		if err == nil {
			return side, nil
		}
		lastErr = err
		if permanentDialError(err) {
			return nil, err
		}
		c.logger.Warn("openai live codex: sideband redial failed", logging.Field{Key: "attempt", Value: attempt + 1}, logging.Field{Key: "error", Value: err})
	}
	return nil, lastErr
}

func (c *conn) sleep(delay time.Duration) bool {
	timer := c.clock.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C():
		return true
	case <-c.done:
		return false
	}
}

// permanentDialError reports a dial failure that retrying cannot fix.
func permanentDialError(err error) bool {
	return errors.Is(err, codexrtc.ErrCallEnded) || credentialError(err)
}

// credentialError reports a rejected or unusable ChatGPT credential.
func credentialError(err error) bool {
	return errors.Is(err, codexrtc.ErrUnauthorized) || errors.Is(err, codexrtc.ErrNoCredential)
}

// endFor maps a failed redial to the session's end.
func endFor(err error) endedFrame {
	switch {
	case credentialError(err):
		return endedFrame{Reason: reasonAuthFailed, Auth: true, Message: err.Error()}
	case errors.Is(err, codexrtc.ErrCallEnded):
		return endedFrame{Reason: reasonCallEnded, Message: err.Error()}
	default:
		return endedFrame{Reason: livesession.CloseReasonConnectionLost, Message: err.Error()}
	}
}

// Close stops the transport: the workers end, then the sideband and the peer
// close. It is idempotent and safe from any goroutine, the read loop
// included.
func (c *conn) Close() error {
	c.closeOnce.Do(func() {
		c.cancel()
		c.mu.Lock()
		side := c.side
		c.side = nil
		c.mu.Unlock()
		var sideErr error
		if side != nil {
			sideErr = side.Close()
		}
		peerErr := c.peer.Close()
		c.workers.Wait()
		c.closeErr = errors.Join(sideErr, peerErr)
		if dropped := c.pacer.droppedFrames(); dropped > 0 {
			c.logger.Warn("openai live codex: input frames dropped behind the pacer", logging.Field{Key: "count", Value: dropped})
		}
	})
	return c.closeErr
}
