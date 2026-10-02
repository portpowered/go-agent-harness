package transcript

import (
	"bytes"
	"errors"
	"testing"
)

type closingWebSocket struct {
	closed   int
	closeErr error
}

func (*closingWebSocket) ReadMessage() (int, []byte, error) { return 0, nil, nil }
func (*closingWebSocket) WriteMessage(int, []byte) error    { return nil }
func (c *closingWebSocket) Close() error {
	c.closed++
	return c.closeErr
}

type nonClosingWebSocket struct{}

func (nonClosingWebSocket) ReadMessage() (int, []byte, error) { return 0, nil, nil }
func (nonClosingWebSocket) WriteMessage(int, []byte) error    { return nil }

func TestClientWebSocketClosePassesThroughToTheLiveConnection(t *testing.T) {
	closeErr := errors.New("close failed")
	conn := &closingWebSocket{closeErr: closeErr}
	wrapped := NewClientCapture(&clientRecordSink{}, nil).WrapWebSocket(conn)
	if err := wrapped.Close(); !errors.Is(err, closeErr) || conn.closed != 1 {
		t.Fatalf("Close() = %v after %d inner closes, want the inner error after one close", err, conn.closed)
	}
	if err := NewClientCapture(&clientRecordSink{}, nil).WrapWebSocket(nonClosingWebSocket{}).Close(); err != nil {
		t.Fatalf("Close() without an inner closer = %v, want nil", err)
	}
	var unset *ClientWebSocket
	if err := unset.Close(); !errors.Is(err, ErrNilClientBoundary) {
		t.Fatalf("nil Close() = %v, want ErrNilClientBoundary", err)
	}
}

func TestAgentCaptureInboundAndOutboundRecordAndReportSinkFailures(t *testing.T) {
	sink := &retainingRecordSink{}
	capture := NewAgentCapture(sink, nil)
	if err := capture.CaptureInbound(StreamWS, []byte("in")); err != nil {
		t.Fatalf("CaptureInbound() = %v", err)
	}
	if err := capture.CaptureOutbound(StreamRTCData, []byte("out")); err != nil {
		t.Fatalf("CaptureOutbound() = %v", err)
	}
	if len(sink.records) != 2 ||
		sink.records[0].Direction != DirectionIn || !bytes.Equal(sink.records[0].Payload, []byte("in")) ||
		sink.records[1].Direction != DirectionOut || !bytes.Equal(sink.records[1].Payload, []byte("out")) {
		t.Fatalf("records = %#v, want one inbound then one outbound record", sink.records)
	}
	sink.err = errors.New("sink full")
	if err := capture.CaptureInbound(StreamWS, []byte("x")); !errors.Is(err, sink.err) {
		t.Fatalf("CaptureInbound() with failing sink = %v, want the sink error", err)
	}
}

func TestDefaultWriterConfigUsesTheDocumentedBounds(t *testing.T) {
	config := DefaultWriterConfig()
	if config.SegmentSize != DefaultSegmentSize || config.MaxSegmentBytes != DefaultSegmentSize ||
		config.MaxBackups != DefaultMaxBackups || config.BackupCount != DefaultMaxBackups || config.Mode == 0 {
		t.Fatalf("DefaultWriterConfig() = %#v", config)
	}
}
