package transcript

import (
	"errors"
	"io"
	"sync"
	"time"
)

// ClientMetadata supplies the logical timing attached to the next client
// observation. The caller owns the logical clock; capture only classifies and
// copies the bytes observed at the boundary.
type ClientMetadata func() (tick uint64, timestamp time.Time)

// ClientOption configures a ClientCapture.
type ClientOption func(*clientConfig)

// WithClientReporter installs the one-shot reporter for transcript failures.
// A reporter failure is recovered by the same boundary used by Tee reporters.
func WithClientReporter(reporter func(error)) ClientOption {
	return func(config *clientConfig) {
		config.reporter = reporter
	}
}

// WithClientCaptureReporter is a descriptive alias for WithClientReporter.
func WithClientCaptureReporter(reporter func(error)) ClientOption {
	return WithClientReporter(reporter)
}

// ErrNilClientBoundary identifies a wrapper created without a live boundary.
var ErrNilClientBoundary = errors.New("transcript: nil client boundary")

type clientConfig struct {
	reporter func(error)
}

// ClientCapture observes the four client-side session boundaries while
// leaving the wrapped live operation authoritative. Its sink is shared by all
// wrappers, so Writer's serialized append boundary supplies one observation
// order across device and WebSocket traffic.
type ClientCapture struct {
	sink     RecordSink
	metadata ClientMetadata
	reporter func(error)

	reportOnce sync.Once
	closeOnce  sync.Once
	closeErr   error
}

// Client is a short alias for ClientCapture.
type Client = ClientCapture

// NewClientCapture creates client boundary wrappers around sink. A nil
// metadata function uses tick zero and the current UTC time; deterministic
// callers should supply a function that returns their logical tick and time.
func NewClientCapture(sink RecordSink, metadata ClientMetadata, options ...ClientOption) *ClientCapture {
	config := clientConfig{}
	for _, option := range options {
		if option != nil {
			option(&config)
		}
	}
	if metadata == nil {
		metadata = func() (uint64, time.Time) {
			return 0, time.Now().UTC()
		}
	}
	return &ClientCapture{
		sink:     sink,
		metadata: metadata,
		reporter: config.reporter,
	}
}

// NewClient is an alias for NewClientCapture.
func NewClient(sink RecordSink, metadata ClientMetadata, options ...ClientOption) *ClientCapture {
	return NewClientCapture(sink, metadata, options...)
}

// Close closes the transcript sink when it owns an io.Closer. Wrapped live
// boundaries are never closed by this method.
func (c *ClientCapture) Close() error {
	if c == nil {
		return nil
	}
	c.closeOnce.Do(func() {
		if closer, ok := c.sink.(io.Closer); ok {
			c.closeErr = closer.Close()
		}
	})
	return c.closeErr
}

// WrapDeviceInput records bytes returned by source as client/in/device-in.
// The source's count and error are returned unchanged, including a nonzero
// count paired with an error.
func (c *ClientCapture) WrapDeviceInput(source io.Reader) *ClientDeviceInput {
	return &ClientDeviceInput{inner: source, capture: c}
}

// DeviceInput is an alias for WrapDeviceInput.
func (c *ClientCapture) DeviceInput(source io.Reader) *ClientDeviceInput {
	return c.WrapDeviceInput(source)
}

// WrapDeviceOutput records bytes accepted by sink as client/out/device-out.
// For a partial write, only the accepted prefix is recorded; unaccepted bytes
// are never synthesized into the transcript.
func (c *ClientCapture) WrapDeviceOutput(sink io.Writer) *ClientDeviceOutput {
	return &ClientDeviceOutput{inner: sink, capture: c}
}

// DeviceOutput is an alias for WrapDeviceOutput.
func (c *ClientCapture) DeviceOutput(sink io.Writer) *ClientDeviceOutput {
	return c.WrapDeviceOutput(sink)
}

// WebSocket is the minimal message-oriented transport contract needed by the
// client capture wrapper. Close is optional and is passed through when the
// wrapped value implements it.
type WebSocket interface {
	ReadMessage() (messageType int, payload []byte, err error)
	WriteMessage(messageType int, payload []byte) error
}

// WrapWebSocket records successful sends as client/out/ws and successful
// receives as client/in/ws. Message types are live transport metadata and are
// intentionally not inserted into the shared payload-only Record format.
func (c *ClientCapture) WrapWebSocket(conn WebSocket) *ClientWebSocket {
	return &ClientWebSocket{inner: conn, capture: c}
}

// WebSocket is an alias for WrapWebSocket.
func (c *ClientCapture) WebSocket(conn WebSocket) *ClientWebSocket {
	return c.WrapWebSocket(conn)
}

// ClientDeviceInput is an io.Reader decorator for the client input device.
type ClientDeviceInput struct {
	inner   io.Reader
	capture *ClientCapture
}

// Read preserves the live reader's exact result and records every non-empty
// returned byte range exactly once.
func (r *ClientDeviceInput) Read(destination []byte) (int, error) {
	if r == nil || r.inner == nil {
		return 0, ErrNilClientBoundary
	}
	n, err := r.inner.Read(destination)
	if n > 0 && n <= len(destination) {
		r.capture.observe(DirectionIn, StreamDeviceIn, destination[:n], n, err)
	}
	return n, err
}

// ClientDeviceOutput is an io.Writer decorator for the client output device.
type ClientDeviceOutput struct {
	inner   io.Writer
	capture *ClientCapture
}

// Write preserves the live writer's exact result and records every non-empty
// accepted prefix exactly once.
func (w *ClientDeviceOutput) Write(source []byte) (int, error) {
	if w == nil || w.inner == nil {
		return 0, ErrNilClientBoundary
	}
	n, err := w.inner.Write(source)
	if n > 0 && n <= len(source) {
		w.capture.observe(DirectionOut, StreamDeviceOut, source[:n], n, err)
	}
	return n, err
}

// ClientWebSocket is a message-oriented WebSocket decorator.
type ClientWebSocket struct {
	inner   WebSocket
	capture *ClientCapture
}

// WriteMessage preserves the live transport error and records the original
// payload only after the live send succeeds.
func (c *ClientWebSocket) WriteMessage(messageType int, payload []byte) error {
	if c == nil || c.inner == nil {
		return ErrNilClientBoundary
	}
	if err := c.inner.WriteMessage(messageType, payload); err != nil {
		return err
	}
	c.capture.observe(DirectionOut, StreamWS, payload, 1, nil)
	return nil
}

// ReadMessage preserves the live transport result and records the original
// payload only after the live receive succeeds.
func (c *ClientWebSocket) ReadMessage() (int, []byte, error) {
	if c == nil || c.inner == nil {
		return 0, nil, ErrNilClientBoundary
	}
	messageType, payload, err := c.inner.ReadMessage()
	if err == nil {
		c.capture.observe(DirectionIn, StreamWS, payload, 1, nil)
	}
	return messageType, payload, err
}

// Close passes through to a wrapped optional closer. The capture sink is
// closed by ClientCapture.Close so the live connection remains unowned.
func (c *ClientWebSocket) Close() error {
	if c == nil || c.inner == nil {
		return ErrNilClientBoundary
	}
	if closer, ok := c.inner.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

func (c *ClientCapture) observe(direction Direction, stream Stream, payload []byte, accepted int, liveErr error) {
	if c == nil || c.sink == nil || accepted <= 0 {
		return
	}
	tick, timestamp := c.metadata()
	record := NewRecord(tick, timestamp, PeerClient, direction, stream, payload)
	tee := NewTee(
		RecordConsumerFunc(func(Record) (int, error) { return accepted, liveErr }),
		c.sink,
		WithTeeReporter(c.reportTranscriptFailure),
	)
	_, _ = tee.Write(record)
}

func (c *ClientCapture) reportTranscriptFailure(err error) {
	if c == nil || c.reporter == nil || err == nil {
		return
	}
	c.reportOnce.Do(func() {
		c.reporter(err)
	})
}

var (
	_ io.Reader = (*ClientDeviceInput)(nil)
	_ io.Writer = (*ClientDeviceOutput)(nil)
)
