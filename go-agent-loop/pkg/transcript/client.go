package transcript

import (
	"errors"
	"io"
	"sync"
	"time"
)

// ClientMetadata supplies the logical timing attached to the next client
// observation. The caller owns the logical clock.
type ClientMetadata func() (tick uint64, timestamp time.Time)

// ErrNilClientBoundary identifies a wrapper created without a live boundary.
var ErrNilClientBoundary = errors.New("transcript: nil client boundary")

// ClientCapture observes client device and WebSocket boundaries while leaving
// the wrapped live operation authoritative. All wrappers share one sink.
type ClientCapture struct {
	sink       RecordSink
	metadata   ClientMetadata
	reporter   func(error)
	reportOnce sync.Once
}

// Client is a short alias for ClientCapture.
type Client = ClientCapture

// NewClientCapture creates client boundary wrappers around sink. A nil
// metadata function uses tick zero and the current UTC time. The optional
// reporter receives the first transcript failure and never a live-path error.
func NewClientCapture(sink RecordSink, metadata ClientMetadata, reporters ...func(error)) *ClientCapture {
	if metadata == nil {
		metadata = func() (uint64, time.Time) { return 0, time.Now().UTC() }
	}
	var reporter func(error)
	if len(reporters) > 0 {
		reporter = reporters[0]
	}
	return &ClientCapture{sink: sink, metadata: metadata, reporter: reporter}
}

// NewClient is an alias for NewClientCapture.
func NewClient(sink RecordSink, metadata ClientMetadata, reporters ...func(error)) *ClientCapture {
	return NewClientCapture(sink, metadata, reporters...)
}

// WrapDeviceInput records bytes returned by source as client/in/device-in.
// The source's count and error are returned unchanged.
func (c *ClientCapture) WrapDeviceInput(source io.Reader) *ClientDeviceInput {
	return &ClientDeviceInput{inner: source, capture: c}
}

// WrapDeviceOutput records bytes accepted by sink as client/out/device-out.
// For a partial write, only the accepted prefix is recorded.
func (c *ClientCapture) WrapDeviceOutput(sink io.Writer) *ClientDeviceOutput {
	return &ClientDeviceOutput{inner: sink, capture: c}
}

// WebSocket is the minimal message-oriented transport contract needed by the
// client wrapper. Close is optional and is passed through when supported.
type WebSocket interface {
	ReadMessage() (messageType int, payload []byte, err error)
	WriteMessage(messageType int, payload []byte) error
}

// WrapWebSocket records successful sends as client/out/ws and successful
// receives as client/in/ws. Message types remain live transport metadata.
func (c *ClientCapture) WrapWebSocket(conn WebSocket) *ClientWebSocket {
	return &ClientWebSocket{inner: conn, capture: c}
}

// ClientDeviceInput is an io.Reader decorator for the client input device.
type ClientDeviceInput struct {
	inner   io.Reader
	capture *ClientCapture
}

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

func (w *ClientDeviceOutput) Write(source []byte) (int, error) {
	if w == nil || w.inner == nil {
		return 0, ErrNilClientBoundary
	}
	observed := append([]byte(nil), source...)
	n, err := w.inner.Write(source)
	if n > 0 && n <= len(source) {
		w.capture.observe(DirectionOut, StreamDeviceOut, observed[:n], n, err)
	}
	return n, err
}

// ClientWebSocket is a message-oriented WebSocket decorator.
type ClientWebSocket struct {
	inner   WebSocket
	capture *ClientCapture
}

func (c *ClientWebSocket) WriteMessage(messageType int, payload []byte) error {
	if c == nil || c.inner == nil {
		return ErrNilClientBoundary
	}
	observed := append([]byte(nil), payload...)
	if err := c.inner.WriteMessage(messageType, payload); err != nil {
		return err
	}
	c.capture.observe(DirectionOut, StreamWS, observed, 1, nil)
	return nil
}

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

// Close passes through to an optional live connection closer.
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
	c.reportOnce.Do(func() { c.reporter(err) })
}

var (
	_ io.Reader = (*ClientDeviceInput)(nil)
	_ io.Writer = (*ClientDeviceOutput)(nil)
)
