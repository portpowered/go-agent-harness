package testing

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

// RecordingWebSocketDialer records bidirectional WebSocket traffic.
type RecordingWebSocketDialer struct {
	inner    transport.Dialer
	startAt  time.Time
	clock    clock.Source
	capture  SessionCapture
	sink     SessionCaptureSink
	sinkErr  error
	events   []CapturedSessionEvent
	sequence int
	mu       sync.Mutex
}

var _ transport.Dialer = (*RecordingWebSocketDialer)(nil)

// NewRecordingWebSocketDialer wraps a live WebSocket dialer and records
// raw JSON messages passing through the returned WebSocket connection.
func NewRecordingWebSocketDialer(inner transport.Dialer, providerName, model string, sources ...clock.Source) *RecordingWebSocketDialer {
	source := captureClock(sources)
	startAt := source.Now()
	d := &RecordingWebSocketDialer{inner: inner, clock: source, startAt: startAt, capture: newCaptureEnvelope(startAt)}
	d.capture.Provider = SessionProviderMetadata{Name: providerName, Model: model}
	return d
}

// Dial connects through the wrapped dialer and returns a recording connection.
func (d *RecordingWebSocketDialer) Dial(url string, headers map[string]string) (transport.Conn, error) {
	if d.inner == nil {
		return nil, fmt.Errorf("recording websocket dialer requires an inner dialer")
	}
	conn, err := d.inner.Dial(url, headers)
	if err != nil {
		return nil, err
	}
	return &recordingWebSocketConn{inner: conn, recorder: d}, nil
}

// Capture returns a copy of the current capture envelope.
func (d *RecordingWebSocketDialer) Capture() SessionCapture {
	d.mu.Lock()
	defer d.mu.Unlock()

	events := make([]CapturedSessionEvent, 0, len(d.events))
	for _, event := range d.events {
		// An outbound reservation is made before the wrapped connection is
		// called so a synchronous provider acknowledgement cannot be recorded
		// ahead of the client event that caused it. Failed writes are marked and
		// omitted here, keeping captures representative of accepted traffic.
		if event.PayloadType == "" {
			continue
		}
		events = append(events, cloneCapturedEvent(event))
	}

	capture := d.capture
	capture.Records = events
	// Keep the in-memory capture and the flushed representation equally
	// verifiable. FlushToFile still recomputes this value immediately before
	// publication through json.MarshalIndent.
	if sealed, err := SealSessionCapture(capture); err == nil {
		capture = sealed
	}
	return capture
}

// ReplayWebSocketDialer replays raw WebSocket messages from a capture.
type ReplayWebSocketDialer struct {
	capture        SessionCapture
	preserveTiming bool
	clock          clock.TimerSource
	mu             sync.Mutex
	conn           *replayWebSocketConn
	done           chan struct{}
}

var _ transport.Dialer = (*ReplayWebSocketDialer)(nil)

// ReplayOutboundPacer is the optional cursor gate used by a self-driving
// replay. It releases an outbound frame only after every earlier inbound
// capture record has been read. The replay connection still performs the
// strict payload and direction validation when the frame is written.
type ReplayOutboundPacer interface {
	WaitForNextOutbound() error
}

var _ ReplayOutboundPacer = (*ReplayWebSocketDialer)(nil)

// ReplayWebSocketDialerOption configures replay behavior without changing the
// strict payload and direction validation contract.
type ReplayWebSocketDialerOption func(*ReplayWebSocketDialer)

// WithRecordedSessionTiming preserves the relative timestamp_ms cadence from
// the capture. The first record is released immediately; every later record is
// gated by its offset from that first timestamp. The default remains the fast,
// order-only replay used by deterministic unit tests.
func WithRecordedSessionTiming() ReplayWebSocketDialerOption {
	return func(d *ReplayWebSocketDialer) { d.preserveTiming = true }
}

// NewReplayWebSocketDialer loads a raw WebSocket session capture from path.
// Current captures are fully verified; retained version-1 captures are
// structurally validated and replayed with a reduced-integrity guarantee.
func NewReplayWebSocketDialer(path string, opts ...ReplayWebSocketDialerOption) (*ReplayWebSocketDialer, error) {
	loaded, err := LoadSessionCaptureForReplay(path)
	if err != nil {
		return nil, err
	}
	return NewReplayWebSocketDialerFromCapture(loaded.Capture, opts...)
}

// NewReplayWebSocketDialerFromCapture builds a replay dialer from an already
// decoded capture. Version-2 captures must be integrity-verified; version-1
// captures are accepted only as an explicit replay compatibility seam and are
// structurally validated without claiming integrity.
func NewReplayWebSocketDialerFromCapture(capture SessionCapture, opts ...ReplayWebSocketDialerOption) (*ReplayWebSocketDialer, error) {
	if err := validateSessionCaptureReplayEnvelope("<in-memory>", capture); err != nil {
		return nil, err
	}
	for _, evt := range capture.Records {
		if evt.PayloadType != SessionPayloadTypeWebSocketMessage {
			return nil, fmt.Errorf("session capture contains %q payload; expected %q", evt.PayloadType, SessionPayloadTypeWebSocketMessage)
		}
	}
	dialer := &ReplayWebSocketDialer{capture: capture, done: make(chan struct{}), clock: clock.Real{}}
	for _, option := range opts {
		if option != nil {
			option(dialer)
		}
	}
	return dialer, nil
}

// Model returns the model metadata from the replay capture, if present.
func (d *ReplayWebSocketDialer) Model() string {
	return d.capture.Provider.Model
}

// WaitForNextOutbound waits for the active replay cursor to reach its next
// client-to-server record. It is intended for capture-derived self-driving
// callers; direct writes remain strict and continue to reject early frames.
func (d *ReplayWebSocketDialer) WaitForNextOutbound() error {
	d.mu.Lock()
	conn := d.conn
	d.mu.Unlock()
	if conn == nil {
		return fmt.Errorf("replay websocket dialer has no active connection")
	}
	return conn.waitForNextOutbound()
}

// Dial returns an in-memory replay connection and never opens a live network connection.
func (d *ReplayWebSocketDialer) Dial(_ string, _ map[string]string) (transport.Conn, error) {
	events := make([]CapturedSessionEvent, len(d.capture.Records))
	copy(events, d.capture.Records)
	conn := newReplayWebSocketConn(events, d.done, d.capture.EndsWithDisconnect, d.preserveTiming, d.clock)
	d.mu.Lock()
	d.conn = conn
	d.mu.Unlock()
	return conn, nil
}

// Done is closed when the active replay connection closes or detects divergence.
func (d *ReplayWebSocketDialer) Done() <-chan struct{} {
	return d.done
}

// Err returns the replay divergence or incompletion error for the active connection.
func (d *ReplayWebSocketDialer) Err() error {
	d.mu.Lock()
	conn := d.conn
	d.mu.Unlock()
	if conn == nil {
		return nil
	}
	return conn.Err()
}

type replayWebSocketConn struct {
	events             []CapturedSessionEvent
	index              int
	closed             bool
	endsWithDisconnect bool
	mu                 sync.Mutex
	cond               *sync.Cond
	err                error
	done               chan struct{}
	once               sync.Once
	preserveTiming     bool
	timingStartedAt    time.Time
	firstTimestampMs   int64
	clock              clock.TimerSource
}

var _ transport.Conn = (*replayWebSocketConn)(nil)

func newReplayWebSocketConn(events []CapturedSessionEvent, done chan struct{}, endsWithDisconnect, preserveTiming bool, source clock.TimerSource) *replayWebSocketConn {
	firstTimestampMs := int64(0)
	if len(events) > 0 {
		firstTimestampMs = events[0].TimestampMs
	}
	conn := &replayWebSocketConn{
		events:             events,
		done:               done,
		endsWithDisconnect: endsWithDisconnect,
		preserveTiming:     preserveTiming,
		timingStartedAt:    source.Now(),
		clock:              source,
		firstTimestampMs:   firstTimestampMs,
	}
	conn.cond = sync.NewCond(&conn.mu)
	return conn
}

func (c *replayWebSocketConn) ReadMessage() (int, []byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for {
		if c.err != nil {
			return 0, nil, c.err
		}
		if c.closed {
			return 0, nil, io.EOF
		}
		if c.index >= len(c.events) {
			if c.endsWithDisconnect {
				c.closeDoneLocked()
				return 0, nil, io.EOF
			}
			c.cond.Wait()
			continue
		}
		evt := c.events[c.index]
		if evt.Direction == DirectionServerToClient {
			index := c.index
			c.mu.Unlock()
			err := c.waitForRecordedTimestamp(evt.TimestampMs)
			c.mu.Lock()
			if err != nil {
				return 0, nil, err
			}
			if c.index != index {
				continue
			}
			c.index++
			c.cond.Broadcast()
			return 1, eventPayload(evt), nil
		}
		c.cond.Wait()
	}
}

// waitForNextOutbound is deliberately separate from WriteMessage. A
// self-driving caller may wait for the capture cursor, while an ordinary
// caller that writes early must still receive the established replay mismatch.
func (c *replayWebSocketConn) waitForNextOutbound() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	for {
		if c.err != nil {
			return c.err
		}
		if c.closed {
			return io.ErrClosedPipe
		}
		if c.index >= len(c.events) {
			return newReplayMismatchError(
				"replay completed",
				"self-driving outbound",
				fmt.Errorf("unexpected outbound event after replay completed"),
			)
		}
		if c.events[c.index].Direction == DirectionClientToServer {
			timestampMs := c.events[c.index].TimestampMs
			index := c.index
			c.mu.Unlock()
			err := c.waitForRecordedTimestamp(timestampMs)
			c.mu.Lock()
			if err != nil {
				return err
			}
			if c.index != index {
				continue
			}
			return nil
		}
		c.cond.Wait()
	}
}

func (c *replayWebSocketConn) WriteMessage(_ int, payload []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return io.ErrClosedPipe
	}
	if c.index >= len(c.events) {
		return c.setErrLocked(newReplayMismatchError("replay completed", websocketPayloadType(payload), fmt.Errorf("unexpected outbound event after replay completed")))
	}
	evt := c.events[c.index]
	if evt.Direction != DirectionClientToServer {
		return c.setErrLocked(newReplayMismatchError(
			fmt.Sprintf("%s event %s at sequence %d", evt.Direction, evt.Type, evt.Sequence),
			websocketPayloadType(payload),
			fmt.Errorf("got outbound before expected capture event"),
		))
	}
	if err := compareReplayPayloads(eventPayload(evt), payload); err != nil {
		return c.setErrLocked(newReplayMismatchError(
			replayEventDescription(evt.Sequence, evt.Type),
			replayEventDescription(evt.Sequence, websocketPayloadType(payload)),
			err,
		))
	}
	index := c.index
	c.mu.Unlock()
	waitErr := c.waitForRecordedTimestamp(evt.TimestampMs)
	c.mu.Lock()
	if waitErr != nil {
		return waitErr
	}
	if c.index != index {
		return c.setErrLocked(newReplayMismatchError(
			"unchanged replay cursor while awaiting recorded timing",
			websocketPayloadType(payload),
			fmt.Errorf("replay cursor advanced concurrently"),
		))
	}
	c.index++
	c.cond.Broadcast()
	return nil
}

func (c *replayWebSocketConn) waitForRecordedTimestamp(timestampMs int64) error {
	if c == nil || !c.preserveTiming {
		return nil
	}
	offsetMs := timestampMs - c.firstTimestampMs
	if offsetMs <= 0 {
		return nil
	}
	due := c.timingStartedAt.Add(time.Duration(offsetMs) * time.Millisecond)
	delay := due.Sub(c.clock.Now())
	if delay <= 0 {
		return nil
	}
	timer := c.clock.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C():
		return nil
	case <-c.done:
		return io.EOF
	}
}

func (c *replayWebSocketConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.err == nil && c.index < len(c.events) {
		evt := c.events[c.index]
		c.err = newReplayIncompleteError(
			fmt.Sprintf("%s event %s at sequence %d", evt.Direction, evt.Type, evt.Sequence),
			"connection close",
			fmt.Errorf("session replay incomplete"),
		)
	}
	c.closed = true
	c.closeDoneLocked()
	c.cond.Broadcast()
	return nil
}

func (c *replayWebSocketConn) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

func (c *replayWebSocketConn) setErrLocked(err error) error {
	c.err = err
	c.closeDoneLocked()
	c.cond.Broadcast()
	return err
}

func (c *replayWebSocketConn) closeDoneLocked() {
	c.once.Do(func() {
		close(c.done)
	})
}

// LoadSessionCapture reads and fully verifies a protected version-2 session
// capture. Legacy version-1 envelopes and event arrays are intentionally
// rejected; use LoadSessionCaptureForReplay for the shipped compatibility
// replay path or LoadSessionCaptureUnverified for explicit fixture migration.
func LoadSessionCapture(path string) (SessionCapture, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return SessionCapture{}, fmt.Errorf("read session capture file: %w", err)
	}
	return validateSessionCapturePath(path, data)
}

// LoadSessionCaptureForReplay validates a capture before replay setup. Current
// version-2 captures require a valid SHA-256 envelope. Retained version-1
// captures are accepted after structural validation because replaying owned
// historical evidence is still useful, but the result explicitly reports that
// its integrity could not be verified.
func LoadSessionCaptureForReplay(path string) (SessionCaptureReplayLoad, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return SessionCaptureReplayLoad{}, fmt.Errorf("read session capture file: %w", err)
	}
	return decodeSessionCaptureForReplay(path, data)
}

func decodeSessionCaptureForReplay(path string, data []byte) (SessionCaptureReplayLoad, error) {
	if isLegacySessionCaptureData(data) {
		capture, err := decodeUnverifiedSessionCapture(data)
		if err != nil {
			return SessionCaptureReplayLoad{}, fmt.Errorf("parse legacy session capture: %w", err)
		}
		if err := validateLegacySessionCaptureStructure(path, capture); err != nil {
			return SessionCaptureReplayLoad{}, err
		}
		return SessionCaptureReplayLoad{Capture: capture}, nil
	}

	capture, err := validateSessionCapturePath(path, data)
	if err != nil {
		return SessionCaptureReplayLoad{}, err
	}
	return SessionCaptureReplayLoad{Capture: capture, IntegrityVerified: true}, nil
}

func isLegacySessionCaptureData(data []byte) bool {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && trimmed[0] == '[' && json.Valid(trimmed) {
		return true
	}
	var header struct {
		Version *int `json:"version"`
	}
	if err := json.Unmarshal(trimmed, &header); err != nil {
		return false
	}
	return header.Version != nil && *header.Version == SessionCaptureLegacyVersion
}

// LoadSessionCaptureUnverified loads an old or otherwise unprotected capture
// for controlled migration tooling. It must not be used as a replay input.
func LoadSessionCaptureUnverified(path string) (SessionCapture, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return SessionCapture{}, fmt.Errorf("read session capture file: %w", err)
	}
	return decodeUnverifiedSessionCapture(data)
}

func decodeUnverifiedSessionCapture(data []byte) (SessionCapture, error) {
	var capture SessionCapture
	if err := json.Unmarshal(data, &capture); err == nil && capture.Version != 0 {
		return capture, nil
	}

	var events []CapturedSessionEvent
	if err := json.Unmarshal(data, &events); err != nil {
		return SessionCapture{}, fmt.Errorf("parse session capture: %w", err)
	}
	return SessionCapture{
		Version: SessionCaptureLegacyVersion,
		Records: events,
	}, nil
}

func validateSessionCaptureEnvelope(path string, capture SessionCapture) error {
	if capture.Version == SessionCaptureLegacyVersion {
		return newSessionCaptureValidationError(path, SessionCaptureErrorClassIntegrityUnavailable, "/version", 0, "", fmt.Sprintf("protected schema version %d", SessionCaptureVersion), fmt.Sprintf("unprotected schema version %d", capture.Version), ErrSessionCaptureIntegrityUnavailable)
	}
	if capture.Version != SessionCaptureVersion {
		return newSessionCaptureValidationError(path, SessionCaptureErrorClassUnsupportedVersion, "/version", 0, "", fmt.Sprintf("%d", SessionCaptureVersion), fmt.Sprintf("%d", capture.Version), ErrSessionCaptureUnsupportedVersion)
	}
	if isZeroSessionCaptureIntegrity(capture.Integrity) {
		return newSessionCaptureValidationError(path, SessionCaptureErrorClassIntegrityMetadata, "/integrity", 0, "", "algorithm, coverage, and digest", "missing", ErrSessionCaptureIntegrity)
	}
	metadata, err := json.Marshal(capture.Integrity)
	if err != nil {
		return newSessionCaptureValidationError(path, SessionCaptureErrorClassIntegrityMetadata, "/integrity", 0, "", "valid integrity object", "unserializable", errors.Join(ErrSessionCaptureIntegrity, err))
	}
	if err := validateSessionCaptureIntegrityMetadata(path, metadata, capture.Integrity); err != nil {
		return err
	}
	if err := validateSessionCaptureStructure(path, capture); err != nil {
		return err
	}
	actual, err := ComputeSessionCaptureDigest(capture)
	if err != nil {
		return newSessionCaptureValidationError(path, SessionCaptureErrorClassStructure, "$", 0, SessionCaptureIntegrityAlgorithm, "serializable protected envelope", "serialization failed", errors.Join(ErrSessionCaptureStructure, err))
	}
	if actual != capture.Integrity.Digest {
		return newSessionCaptureValidationError(path, SessionCaptureErrorClassIntegrityChecksum, "/integrity/digest", 0, capture.Integrity.Algorithm, "stored "+capture.Integrity.Digest, "computed "+actual, ErrSessionCaptureIntegrity)
	}
	return nil
}

func validateSessionCaptureReplayEnvelope(path string, capture SessionCapture) error {
	if capture.Version == SessionCaptureLegacyVersion {
		return validateLegacySessionCaptureStructure(path, capture)
	}
	return validateSessionCaptureEnvelope(path, capture)
}

func eventPayload(evt CapturedSessionEvent) []byte {
	if len(evt.Payload) > 0 {
		return evt.Payload
	}
	return evt.Data
}

func websocketPayloadType(payload []byte) string {
	var envelope struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil || envelope.Type == "" {
		return "websocket.message"
	}
	return envelope.Type
}
