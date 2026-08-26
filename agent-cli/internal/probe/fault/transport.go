// Package fault provides deterministic decorators for the probe transport
// seam. Faults are injected below the provider session so the session sees
// the same Conn contract it uses in production.
package fault

import (
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

var (
	// ErrInvalidConfiguration identifies an invalid fault option or a missing
	// transport to wrap.
	ErrInvalidConfiguration = errors.New("invalid transport fault configuration")
	// ErrMidStreamClose identifies a deliberately injected read-side close.
	ErrMidStreamClose = errors.New("injected mid-stream close")
)

// FrameDirection identifies the side of a bidirectional transport on which a
// frame fault is applied. Inbound is provider-to-client (ReadMessage), and
// outbound is client-to-provider (WriteMessage).
type FrameDirection string

const (
	DirectionInbound  FrameDirection = "inbound"
	DirectionOutbound FrameDirection = "outbound"

	// DirectionRead and DirectionWrite are concise aliases for callers that
	// want to match the Conn method names.
	DirectionRead  = DirectionInbound
	DirectionWrite = DirectionOutbound
)

// LogicalClock is the clock contract required by deterministic frame timing
// faults.
// The repository's *clock.Deterministic satisfies this interface. A delay
// advances logical ticks until the configured duration has elapsed; it never
// waits on host time.
type LogicalClock interface {
	Now() time.Time
	Tick() uint64
	AdvanceTo(target uint64) uint64
}

// FrameEvent records one deterministic frame fault. Frame ordinals are
// one-based and count successful frames observed at the wrapped transport
// seam, including frames intentionally dropped.
type FrameEvent struct {
	Direction FrameDirection
	Frame     int
	Tick      uint64
}

// DelayEvent records the logical-clock movement associated with one delayed
// frame.
type DelayEvent struct {
	Direction  FrameDirection
	Frame      int
	Duration   time.Duration
	BeforeTick uint64
	AfterTick  uint64
}

// StallEvent records a bounded logical stall before an egress frame is
// delivered to the provider session. Egress is the provider-to-client
// direction, so its direction is always DirectionInbound.
type StallEvent struct {
	Direction  FrameDirection
	Frame      int
	Duration   time.Duration
	BeforeTick uint64
	AfterTick  uint64
}

// FaultStats is a race-safe snapshot of observable frame-fault evidence. The
// event slices are copied by Stats, so callers may retain and inspect a
// snapshot without synchronizing with the connection.
type FaultStats struct {
	ReadAttempts  int
	WriteAttempts int
	ReadFrames    int
	WrittenFrames int

	DroppedReadFrames  int
	DroppedWriteFrames int
	DelayedReadFrames  int
	DelayedWriteFrames int
	StalledReadFrames  int

	Drops  []FrameEvent
	Delays []DelayEvent
	Stalls []StallEvent
}

// MidStreamCloseError identifies the deterministic point at which a wrapped
// connection closed. It also unwraps to io.EOF because a provider transport
// observes this fault as an abrupt end of its read stream.
type MidStreamCloseError struct {
	// AfterFrames is the number of successful inbound frames allowed before
	// the injected close. Zero closes before the first frame is read.
	AfterFrames int
	// ObservedFrames is the number of successful inbound frames returned when
	// the fault was triggered. It normally equals AfterFrames.
	ObservedFrames int
}

func (e *MidStreamCloseError) Error() string {
	if e == nil {
		return "injected mid-stream close"
	}
	return fmt.Sprintf("injected mid-stream close after %d frame(s)", e.ObservedFrames)
}

func (e *MidStreamCloseError) Unwrap() error {
	return errors.Join(ErrMidStreamClose, io.EOF)
}

// TransportFault marks this error as an intentional probe fault at the shared
// transport seam.
func (*MidStreamCloseError) TransportFault() {}

// Option configures a transport fault decorator.
type Option func(*config) error

type frameSelector struct {
	all    bool
	frames map[int]struct{}
}

type frameDelay struct {
	frameSelector
	duration time.Duration
	enabled  bool
}

type consumerStall struct {
	frameSelector
	duration time.Duration
	enabled  bool
}

type config struct {
	midStreamCloseAfter *int
	clock               LogicalClock
	drops               map[FrameDirection]frameSelector
	delays              map[FrameDirection]frameDelay
	slowConsumer        consumerStall
}

// WithMidStreamCloseAfter configures a close before the first frame after
// afterFrames successful inbound frames have been returned. The count is
// deterministic and does not depend on wall-clock timing.
func WithMidStreamCloseAfter(afterFrames int) Option {
	return func(cfg *config) error {
		if afterFrames < 0 {
			return fmt.Errorf("%w: mid-stream close frame count %d is negative", ErrInvalidConfiguration, afterFrames)
		}
		count := afterFrames
		cfg.midStreamCloseAfter = &count
		return nil
	}
}

// WithMidStreamClose is a concise alias for WithMidStreamCloseAfter.
func WithMidStreamClose(afterFrames int) Option {
	return WithMidStreamCloseAfter(afterFrames)
}

// WithClock supplies the deterministic clock used by frame-delay and
// slow-consumer options. A timing option without a clock is rejected when the
// wrapper is built.
func WithClock(source LogicalClock) Option {
	return func(cfg *config) error {
		if source == nil {
			return fmt.Errorf("%w: logical clock is nil", ErrInvalidConfiguration)
		}
		cfg.clock = source
		return nil
	}
}

// WithDropFrames drops the selected one-based frames in the requested
// direction. With no frame numbers, every frame in that direction is
// dropped. Dropped frames return nil to the caller and are reported by Stats.
func WithDropFrames(direction FrameDirection, frameNumbers ...int) Option {
	selected := append([]int(nil), frameNumbers...)
	return func(cfg *config) error {
		selector, err := newFrameSelector(selected)
		if err != nil {
			return err
		}
		if err := validateDirection(direction); err != nil {
			return err
		}
		if cfg.drops == nil {
			cfg.drops = make(map[FrameDirection]frameSelector)
		}
		merged := cfg.drops[direction]
		mergeFrameSelector(&merged, selector)
		cfg.drops[direction] = merged
		return nil
	}
}

// WithDropReadFrames drops selected provider-to-client frames.
func WithDropReadFrames(frameNumbers ...int) Option {
	return WithDropFrames(DirectionInbound, frameNumbers...)
}

// WithDropWriteFrames drops selected client-to-provider frames.
func WithDropWriteFrames(frameNumbers ...int) Option {
	return WithDropFrames(DirectionOutbound, frameNumbers...)
}

// WithFrameDelay delays every read and write frame by the supplied logical
// duration. Use WithReadFrameDelay or WithWriteFrameDelay to target one side.
func WithFrameDelay(delay time.Duration) Option {
	return func(cfg *config) error {
		if err := configureFrameDelay(cfg, DirectionInbound, delay); err != nil {
			return err
		}
		return configureFrameDelay(cfg, DirectionOutbound, delay)
	}
}

// WithDelay is a concise alias for WithFrameDelay.
func WithDelay(delay time.Duration) Option {
	return WithFrameDelay(delay)
}

// WithDelayFrames delays selected one-based frames in the requested direction
// by a logical duration. With no frame numbers, every frame is delayed.
func WithDelayFrames(direction FrameDirection, delay time.Duration, frameNumbers ...int) Option {
	selected := append([]int(nil), frameNumbers...)
	return func(cfg *config) error {
		if err := validateDirection(direction); err != nil {
			return err
		}
		return configureFrameDelay(cfg, direction, delay, selected...)
	}
}

// WithReadFrameDelay delays selected provider-to-client frames.
func WithReadFrameDelay(delay time.Duration, frameNumbers ...int) Option {
	return WithDelayFrames(DirectionInbound, delay, frameNumbers...)
}

// WithWriteFrameDelay delays selected client-to-provider frames.
func WithWriteFrameDelay(delay time.Duration, frameNumbers ...int) Option {
	return WithDelayFrames(DirectionOutbound, delay, frameNumbers...)
}

// WithSlowConsumer stalls selected provider-to-client (egress) frames for a
// bounded duration on the configured logical clock. With no frame numbers,
// every egress frame is stalled. It never sleeps on host time.
func WithSlowConsumer(duration time.Duration, frameNumbers ...int) Option {
	selected := append([]int(nil), frameNumbers...)
	return func(cfg *config) error {
		return configureConsumerStall(&cfg.slowConsumer, duration, selected...)
	}
}

// WithEgressStall is a concise alias for WithSlowConsumer.
func WithEgressStall(duration time.Duration, frameNumbers ...int) Option {
	return WithSlowConsumer(duration, frameNumbers...)
}

// WithSlowConsumerEgress is an explicit alias for WithSlowConsumer.
func WithSlowConsumerEgress(duration time.Duration, frameNumbers ...int) Option {
	return WithSlowConsumer(duration, frameNumbers...)
}

func resolveOptions(options []Option) (config, error) {
	var cfg config
	for _, option := range options {
		if option == nil {
			return config{}, fmt.Errorf("%w: nil option", ErrInvalidConfiguration)
		}
		if err := option(&cfg); err != nil {
			return config{}, err
		}
	}
	for _, delay := range cfg.delays {
		if delay.enabled && cfg.clock == nil {
			return config{}, fmt.Errorf("%w: frame delay requires a logical clock", ErrInvalidConfiguration)
		}
	}
	if cfg.slowConsumer.enabled && cfg.clock == nil {
		return config{}, fmt.Errorf("%w: slow consumer stall requires a logical clock", ErrInvalidConfiguration)
	}
	return cfg, nil
}

func validateDirection(direction FrameDirection) error {
	if direction != DirectionInbound && direction != DirectionOutbound {
		return fmt.Errorf("%w: unknown frame direction %q", ErrInvalidConfiguration, direction)
	}
	return nil
}

func newFrameSelector(frameNumbers []int) (frameSelector, error) {
	selector := frameSelector{}
	if len(frameNumbers) == 0 {
		selector.all = true
		return selector, nil
	}
	selector.frames = make(map[int]struct{}, len(frameNumbers))
	for _, frame := range frameNumbers {
		if frame <= 0 {
			return frameSelector{}, fmt.Errorf("%w: frame ordinal %d is not positive", ErrInvalidConfiguration, frame)
		}
		selector.frames[frame] = struct{}{}
	}
	return selector, nil
}

func mergeFrameSelector(dst *frameSelector, src frameSelector) {
	if src.all {
		dst.all = true
		dst.frames = nil
		return
	}
	if dst.all {
		return
	}
	if dst.frames == nil {
		dst.frames = make(map[int]struct{}, len(src.frames))
	}
	for frame := range src.frames {
		dst.frames[frame] = struct{}{}
	}
}

func configureFrameDelay(cfg *config, direction FrameDirection, delay time.Duration, frameNumbers ...int) error {
	if delay <= 0 {
		return fmt.Errorf("%w: frame delay %s is not positive", ErrInvalidConfiguration, delay)
	}
	selector, err := newFrameSelector(frameNumbers)
	if err != nil {
		return err
	}
	if cfg.delays == nil {
		cfg.delays = make(map[FrameDirection]frameDelay)
	}
	configured := cfg.delays[direction]
	if configured.enabled && configured.duration != delay {
		return fmt.Errorf("%w: frame delay for %s configured as %s and %s", ErrInvalidConfiguration, direction, configured.duration, delay)
	}
	configured.enabled = true
	configured.duration = delay
	mergeFrameSelector(&configured.frameSelector, selector)
	cfg.delays[direction] = configured
	return nil
}

func configureConsumerStall(stall *consumerStall, duration time.Duration, frameNumbers ...int) error {
	if duration <= 0 {
		return fmt.Errorf("%w: slow consumer stall %s is not positive", ErrInvalidConfiguration, duration)
	}
	selector, err := newFrameSelector(frameNumbers)
	if err != nil {
		return err
	}
	if stall.enabled && stall.duration != duration {
		return fmt.Errorf("%w: slow consumer stall configured as %s and %s", ErrInvalidConfiguration, stall.duration, duration)
	}
	stall.enabled = true
	stall.duration = duration
	mergeFrameSelector(&stall.frameSelector, selector)
	return nil
}

func validateConn(inner transport.Conn) error {
	if inner == nil {
		return fmt.Errorf("%w: connection is nil", ErrInvalidConfiguration)
	}
	return nil
}

// Conn decorates a transport.Conn with deterministic fault behavior.
type Conn struct {
	inner transport.Conn
	cfg   config

	// A transport normally has one reader. Serializing wrapper reads makes the
	// frame threshold deterministic even when a test deliberately has multiple
	// readers, while Close remains able to run concurrently and unblock inner
	// transports that support it.
	readMu  sync.Mutex
	writeMu sync.Mutex
	mu      sync.Mutex

	stats    FaultStats
	faultErr error

	closeOnce sync.Once
	closeErr  error
}

var _ transport.Conn = (*Conn)(nil)

// WrapConn wraps inner with the requested deterministic fault options.
func WrapConn(inner transport.Conn, options ...Option) (*Conn, error) {
	if err := validateConn(inner); err != nil {
		return nil, err
	}
	cfg, err := resolveOptions(options)
	if err != nil {
		return nil, err
	}
	return &Conn{inner: inner, cfg: cfg}, nil
}

// NewConn is an explicit constructor alias for WrapConn.
func NewConn(inner transport.Conn, options ...Option) (*Conn, error) {
	return WrapConn(inner, options...)
}

// ReadMessage returns the next non-dropped inner frame, or triggers the
// configured mid-stream close once the deterministic frame threshold is
// reached. Dropped frames are consumed and counted rather than returned.
func (c *Conn) ReadMessage() (messageType int, payload []byte, err error) {
	if c == nil {
		return 0, nil, fmt.Errorf("%w: connection is nil", ErrInvalidConfiguration)
	}
	c.readMu.Lock()
	defer c.readMu.Unlock()

	for {
		if faultErr := c.err(); faultErr != nil {
			return 0, nil, faultErr
		}
		if c.cfg.midStreamCloseAfter != nil {
			c.mu.Lock()
			shouldClose := c.stats.ReadFrames >= *c.cfg.midStreamCloseAfter
			observed := c.stats.ReadFrames
			afterFrames := *c.cfg.midStreamCloseAfter
			c.mu.Unlock()
			if shouldClose {
				return 0, nil, c.triggerMidStreamClose(afterFrames, observed)
			}
		}

		messageType, payload, err = c.inner.ReadMessage()
		if err != nil {
			return messageType, payload, err
		}

		c.mu.Lock()
		c.stats.ReadAttempts++
		frame := c.stats.ReadAttempts
		c.mu.Unlock()

		if c.shouldDrop(DirectionInbound, frame) {
			c.recordDrop(DirectionInbound, frame)
			continue
		}
		if delay, ok := c.frameDelay(DirectionInbound, frame); ok {
			c.applyDelay(DirectionInbound, frame, delay)
		}
		if duration, ok := c.egressStall(frame); ok {
			c.applyStall(frame, duration)
		}

		c.mu.Lock()
		c.stats.ReadFrames++
		c.mu.Unlock()
		return messageType, payload, nil
	}
}

// WriteMessage forwards writes until an injected close has terminated the
// connection. Once faulted, returning the same typed fault makes the session
// outcome stable even for transports whose Close is only advisory.
func (c *Conn) WriteMessage(messageType int, payload []byte) error {
	if c == nil {
		return fmt.Errorf("%w: connection is nil", ErrInvalidConfiguration)
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	if faultErr := c.err(); faultErr != nil {
		return faultErr
	}
	c.mu.Lock()
	c.stats.WriteAttempts++
	frame := c.stats.WriteAttempts
	c.mu.Unlock()

	if c.shouldDrop(DirectionOutbound, frame) {
		c.recordDrop(DirectionOutbound, frame)
		return nil
	}
	if delay, ok := c.frameDelay(DirectionOutbound, frame); ok {
		c.applyDelay(DirectionOutbound, frame, delay)
	}

	err := c.inner.WriteMessage(messageType, payload)
	if err == nil {
		c.mu.Lock()
		c.stats.WrittenFrames++
		c.mu.Unlock()
	}
	return err
}

// Close closes the wrapped transport exactly once and preserves its close
// error. A close caused by a fault is also observable through Err.
func (c *Conn) Close() error {
	if c == nil {
		return nil
	}
	return c.closeInner()
}

// Err returns the typed injected fault, if this connection has faulted.
func (c *Conn) Err() error {
	if c == nil {
		return nil
	}
	return c.err()
}

// ReadFrames returns the number of successful inbound frames returned by the
// wrapper. It is useful for deterministic probe evidence and test assertions.
func (c *Conn) ReadFrames() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stats.ReadFrames
}

// Stats returns a consistent snapshot of drop, delay, and frame counters.
func (c *Conn) Stats() FaultStats {
	if c == nil {
		return FaultStats{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	snapshot := c.stats
	snapshot.Drops = append([]FrameEvent(nil), c.stats.Drops...)
	snapshot.Delays = append([]DelayEvent(nil), c.stats.Delays...)
	snapshot.Stalls = append([]StallEvent(nil), c.stats.Stalls...)
	return snapshot
}

// DroppedFrames returns the number of intentionally dropped frames in one
// direction from the current snapshot.
func (c *Conn) DroppedFrames(direction FrameDirection) int {
	stats := c.Stats()
	if direction == DirectionInbound {
		return stats.DroppedReadFrames
	}
	if direction == DirectionOutbound {
		return stats.DroppedWriteFrames
	}
	return 0
}

// DelayedFrames returns the number of logically delayed frames in one
// direction from the current snapshot.
func (c *Conn) DelayedFrames(direction FrameDirection) int {
	stats := c.Stats()
	if direction == DirectionInbound {
		return stats.DelayedReadFrames
	}
	if direction == DirectionOutbound {
		return stats.DelayedWriteFrames
	}
	return 0
}

// StalledFrames returns the number of bounded logical egress stalls in the
// requested direction from the current snapshot.
func (c *Conn) StalledFrames(direction FrameDirection) int {
	if direction != DirectionInbound {
		return 0
	}
	return c.Stats().StalledReadFrames
}

func (c *Conn) err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.faultErr
}

func (c *Conn) triggerMidStreamClose(afterFrames, observedFrames int) error {
	c.mu.Lock()
	if c.faultErr != nil {
		err := c.faultErr
		c.mu.Unlock()
		return err
	}
	faultErr := &MidStreamCloseError{
		AfterFrames:    afterFrames,
		ObservedFrames: observedFrames,
	}
	c.faultErr = faultErr
	c.mu.Unlock()

	if closeErr := c.closeInner(); closeErr != nil {
		combined := errors.Join(faultErr, closeErr)
		c.mu.Lock()
		c.faultErr = combined
		c.mu.Unlock()
		return combined
	}
	return faultErr
}

func (c *Conn) shouldDrop(direction FrameDirection, frame int) bool {
	selector, ok := c.cfg.drops[direction]
	if !ok {
		return false
	}
	if selector.all {
		return true
	}
	_, ok = selector.frames[frame]
	return ok
}

func (c *Conn) frameDelay(direction FrameDirection, frame int) (time.Duration, bool) {
	configured, ok := c.cfg.delays[direction]
	if !ok || !configured.enabled {
		return 0, false
	}
	if configured.all {
		return configured.duration, true
	}
	_, ok = configured.frames[frame]
	return configured.duration, ok
}

func (c *Conn) egressStall(frame int) (time.Duration, bool) {
	configured := c.cfg.slowConsumer
	if !configured.enabled {
		return 0, false
	}
	if configured.all {
		return configured.duration, true
	}
	_, ok := configured.frames[frame]
	return configured.duration, ok
}

func (c *Conn) recordDrop(direction FrameDirection, frame int) {
	tick := uint64(0)
	if c.cfg.clock != nil {
		tick = c.cfg.clock.Tick()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if direction == DirectionInbound {
		c.stats.DroppedReadFrames++
	} else {
		c.stats.DroppedWriteFrames++
	}
	c.stats.Drops = append(c.stats.Drops, FrameEvent{Direction: direction, Frame: frame, Tick: tick})
}

func (c *Conn) applyDelay(direction FrameDirection, frame int, duration time.Duration) {
	clock := c.cfg.clock
	before, after := advanceLogicalDuration(clock, duration)

	c.mu.Lock()
	defer c.mu.Unlock()
	if direction == DirectionInbound {
		c.stats.DelayedReadFrames++
	} else {
		c.stats.DelayedWriteFrames++
	}
	c.stats.Delays = append(c.stats.Delays, DelayEvent{
		Direction:  direction,
		Frame:      frame,
		Duration:   duration,
		BeforeTick: before,
		AfterTick:  after,
	})
}

func (c *Conn) applyStall(frame int, duration time.Duration) {
	before, after := advanceLogicalDuration(c.cfg.clock, duration)

	c.mu.Lock()
	defer c.mu.Unlock()
	c.stats.StalledReadFrames++
	c.stats.Stalls = append(c.stats.Stalls, StallEvent{
		Direction:  DirectionInbound,
		Frame:      frame,
		Duration:   duration,
		BeforeTick: before,
		AfterTick:  after,
	})
}

func advanceLogicalDuration(clock LogicalClock, duration time.Duration) (uint64, uint64) {
	before := clock.Tick()
	target := clock.Now().Add(duration)
	for clock.Now().Before(target) {
		current := clock.Tick()
		if current == ^uint64(0) {
			break
		}
		next := clock.AdvanceTo(current + 1)
		if next <= current {
			break
		}
	}
	return before, clock.Tick()
}

func (c *Conn) closeInner() error {
	c.closeOnce.Do(func() {
		err := c.inner.Close()
		c.mu.Lock()
		c.closeErr = err
		c.mu.Unlock()
	})
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closeErr
}

// Dialer decorates each connection returned by an underlying transport.Dialer
// with the configured deterministic faults.
type Dialer struct {
	inner transport.Dialer
	cfg   config
}

var _ transport.Dialer = (*Dialer)(nil)

// WrapDialer wraps inner and applies its options to every successful Dial.
func WrapDialer(inner transport.Dialer, options ...Option) (*Dialer, error) {
	if inner == nil {
		return nil, fmt.Errorf("%w: dialer is nil", ErrInvalidConfiguration)
	}
	cfg, err := resolveOptions(options)
	if err != nil {
		return nil, err
	}
	return &Dialer{inner: inner, cfg: cfg}, nil
}

// NewDialer is an explicit constructor alias for WrapDialer.
func NewDialer(inner transport.Dialer, options ...Option) (*Dialer, error) {
	return WrapDialer(inner, options...)
}

// Dial forwards endpoint and headers unchanged, then wraps the successful
// connection. A wrapping failure closes the raw connection before returning.
func (d *Dialer) Dial(endpoint string, headers map[string]string) (transport.Conn, error) {
	if d == nil || d.inner == nil {
		return nil, fmt.Errorf("%w: dialer is nil", ErrInvalidConfiguration)
	}
	conn, err := d.inner.Dial(endpoint, headers)
	if err != nil {
		return nil, err
	}
	wrapped, err := WrapConn(conn, optionFromConfig(d.cfg))
	if err != nil {
		if conn != nil {
			_ = conn.Close()
		}
		return nil, err
	}
	return wrapped, nil
}

func optionFromConfig(cfg config) Option {
	return func(dst *config) error {
		*dst = cloneConfig(cfg)
		return nil
	}
}

func cloneConfig(src config) config {
	dst := src
	if src.midStreamCloseAfter != nil {
		count := *src.midStreamCloseAfter
		dst.midStreamCloseAfter = &count
	}
	if src.drops != nil {
		dst.drops = make(map[FrameDirection]frameSelector, len(src.drops))
		for direction, selector := range src.drops {
			dst.drops[direction] = cloneFrameSelector(selector)
		}
	}
	if src.delays != nil {
		dst.delays = make(map[FrameDirection]frameDelay, len(src.delays))
		for direction, delay := range src.delays {
			delay.frameSelector = cloneFrameSelector(delay.frameSelector)
			dst.delays[direction] = delay
		}
	}
	dst.slowConsumer.frameSelector = cloneFrameSelector(src.slowConsumer.frameSelector)
	return dst
}

func cloneFrameSelector(src frameSelector) frameSelector {
	dst := src
	if src.frames != nil {
		dst.frames = make(map[int]struct{}, len(src.frames))
		for frame := range src.frames {
			dst.frames[frame] = struct{}{}
		}
	}
	return dst
}
