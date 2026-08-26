// Package fault provides deterministic decorators for the probe transport
// seam. Faults are injected below the provider session so the session sees
// the same Conn contract it uses in production.
package fault

import (
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

var (
	// ErrInvalidConfiguration identifies an invalid fault option or a missing
	// transport to wrap.
	ErrInvalidConfiguration = errors.New("invalid transport fault configuration")
	// ErrMidStreamClose identifies a deliberately injected read-side close.
	ErrMidStreamClose = errors.New("injected mid-stream close")
)

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

type config struct {
	midStreamCloseAfter *int
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
	return cfg, nil
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
	readMu sync.Mutex
	mu     sync.Mutex

	readFrames int
	faultErr   error

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

// ReadMessage returns the next inner frame, or triggers the configured
// mid-stream close once the deterministic frame threshold is reached.
func (c *Conn) ReadMessage() (messageType int, payload []byte, err error) {
	if c == nil {
		return 0, nil, fmt.Errorf("%w: connection is nil", ErrInvalidConfiguration)
	}
	c.readMu.Lock()
	defer c.readMu.Unlock()

	if faultErr := c.err(); faultErr != nil {
		return 0, nil, faultErr
	}
	if c.cfg.midStreamCloseAfter != nil {
		c.mu.Lock()
		shouldClose := c.readFrames >= *c.cfg.midStreamCloseAfter
		observed := c.readFrames
		afterFrames := *c.cfg.midStreamCloseAfter
		c.mu.Unlock()
		if shouldClose {
			return 0, nil, c.triggerMidStreamClose(afterFrames, observed)
		}
	}

	messageType, payload, err = c.inner.ReadMessage()
	if err == nil {
		c.mu.Lock()
		c.readFrames++
		c.mu.Unlock()
	}
	return messageType, payload, err
}

// WriteMessage forwards writes until an injected close has terminated the
// connection. Once faulted, returning the same typed fault makes the session
// outcome stable even for transports whose Close is only advisory.
func (c *Conn) WriteMessage(messageType int, payload []byte) error {
	if c == nil {
		return fmt.Errorf("%w: connection is nil", ErrInvalidConfiguration)
	}
	if faultErr := c.err(); faultErr != nil {
		return faultErr
	}
	return c.inner.WriteMessage(messageType, payload)
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
	return c.readFrames
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
		if cfg.midStreamCloseAfter == nil {
			dst.midStreamCloseAfter = nil
			return nil
		}
		count := *cfg.midStreamCloseAfter
		dst.midStreamCloseAfter = &count
		return nil
	}
}
