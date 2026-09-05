package transport

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

type observationTestConn struct {
	wrote    bool
	closed   bool
	writeErr error
}

func (c *observationTestConn) WriteMessage(_ int, _ []byte) error { c.wrote = true; return c.writeErr }
func (c *observationTestConn) ReadMessage() (int, []byte, error)  { return 2, []byte{0, 1, 2}, io.EOF }
func (c *observationTestConn) Close() error                       { c.closed = true; return nil }

type observationTestDialer struct {
	conn Conn
	err  error
}

func (d observationTestDialer) Dial(string, map[string]string) (Conn, error) { return d.conn, d.err }

func TestObservingDialerReportsCompletedOperations(t *testing.T) {
	failure := errors.New("write rejected")
	inner := &observationTestConn{writeErr: failure}
	var events []MessageObservation
	d := ObservingDialer{Inner: observationTestDialer{conn: inner}, Observe: func(event MessageObservation) {
		if event.Direction == "send" && !inner.wrote {
			t.Fatal("observed send before transport completion")
		}
		event.Payload = append([]byte(nil), event.Payload...)
		events = append(events, event)
	}}
	conn, err := d.Dial("unused", nil)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte{4, 5}
	if err := conn.WriteMessage(2, payload); !errors.Is(err, failure) {
		t.Fatalf("write error = %v", err)
	}
	payload[0] = 9
	kind, got, err := conn.ReadMessage()
	if kind != 2 || !bytes.Equal(got, []byte{0, 1, 2}) || !errors.Is(err, io.EOF) {
		t.Fatalf("read = %d %v %v", kind, got, err)
	}
	if len(events) != 2 || events[0].Direction != "send" || events[1].Direction != "receive" || !bytes.Equal(events[0].Payload, []byte{4, 5}) || !errors.Is(events[0].Err, failure) {
		t.Fatalf("events = %+v", events)
	}
	if err := conn.Close(); err != nil || !inner.closed {
		t.Fatal("close did not reach inner connection")
	}
}

func TestObservingDialerPreservesDialFailure(t *testing.T) {
	failure := errors.New("dial rejected")
	d := ObservingDialer{Inner: observationTestDialer{err: failure}, Observe: func(MessageObservation) { t.Fatal("observed nonexistent connection") }}
	conn, err := d.Dial("unused", nil)
	if conn != nil || !errors.Is(err, failure) {
		t.Fatalf("dial = %v %v", conn, err)
	}
}
