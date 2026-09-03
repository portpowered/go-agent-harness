package rtc_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport/rtc"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport/transporttest"
)

var (
	_ transport.Dialer  = (*dataDialer)(nil)
	_ transport.Conn    = (*dataConn)(nil)
	_ rtc.Dialer        = (*dataDialer)(nil)
	_ rtc.Conn          = (*dataConn)(nil)
	_ rtc.InboundMedia  = (*inboundStub)(nil)
	_ rtc.OutboundMedia = (*outboundStub)(nil)
)

func TestRTCDataS11Conformance(t *testing.T) { transporttest.RunS11(t, s11Harness()) }

func TestSessionMediaConsumerContext(t *testing.T) {
	if rtc.SessionMediaConsumerRequested(context.Background()) {
		t.Fatal("unmarked context unexpectedly requests session media")
	}
	ctx := rtc.WithSessionMediaConsumer(context.Background())
	if !rtc.SessionMediaConsumerRequested(ctx) {
		t.Fatal("marked context did not request session media")
	}
}

func s11Harness() transporttest.ConformanceHarness {
	dialErr := &operationError{"dial"}
	readErr := &operationError{"read"}
	writeErr := &operationError{"write"}
	closeErr := &operationError{"close"}
	h := transporttest.ConformanceHarness{
		Endpoint: "rtc://memory/s11",
		Headers:  map[string]string{"Authorization": "test", "X-Trace": "s11"},
		Inbound:  []transporttest.Message{{Type: 7, Payload: []byte{0, 1, 2}}, {Type: -4, Payload: []byte("inbound-second")}},
		Outbound: []transporttest.Message{{Type: 3, Payload: []byte{9, 0, 8}}, {Type: 11, Payload: []byte("outbound-second")}},
	}
	h.NewValid = func() (transport.Dialer, transporttest.Observer) { return newData(h.Inbound, nil, nil, nil, nil) }
	h.DialFailure, h.ReadFailure, h.WriteFailure, h.CloseFailure = failure("dial", dialErr), failure("read", readErr), failure("write", writeErr), failure("close", closeErr)
	return h
}

func failure(op string, want error) transporttest.FailureCase {
	var dErr, rErr, wErr, cErr error
	switch op {
	case "dial":
		dErr = want
	case "read":
		rErr = want
	case "write":
		wErr = want
	case "close":
		cErr = want
	}
	return transporttest.FailureCase{
		New: func() transport.Dialer { return newDataOnly(nil, dErr, rErr, wErr, cErr) }, WantErr: want, MatchErr: match(op),
	}
}

func TestRTCDataS11NegativeControlRejectsNoOp(t *testing.T) {
	conn, err := (&noOpDialer{}).Dial("rtc://memory/no-op", map[string]string{"X-Test": "negative-control"})
	if err != nil || conn == nil {
		t.Fatalf("no-op Dial = (%v, %v), want a connection", conn, err)
	}
	gotType, gotPayload, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("no-op ReadMessage: %v", err)
	}
	if gotType == 7 && bytes.Equal(gotPayload, []byte{0, 1, 2}) {
		t.Fatal("dead/no-op connection unexpectedly produced the first S11 message")
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("no-op Close: %v", err)
	}
}

func TestRTCS4OperationErrorIdentity(t *testing.T) {
	cases := []struct{ name string }{{"dial"}, {"read"}, {"write"}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := &operationError{tc.name}
			var dErr, rErr, wErr error
			switch tc.name {
			case "dial":
				dErr = want
			case "read":
				rErr = want
			case "write":
				wErr = want
			}
			conn, err := newDataOnly(nil, dErr, rErr, wErr, nil).Dial("rtc://memory/s4", map[string]string{"X-Test": "s4"})
			if tc.name == "dial" {
				if conn != nil {
					t.Fatal("failed Dial returned a connection")
				}
			} else {
				if err != nil || conn == nil {
					t.Fatalf("setup Dial = (%v, %v)", conn, err)
				}
				defer func() { _ = conn.Close() }()
				if tc.name == "read" {
					_, _, err = conn.ReadMessage()
				} else {
					err = conn.WriteMessage(4, []byte("s4-write"))
				}
			}
			if err == nil || !errors.Is(err, want) {
				t.Fatalf("%s error = %v, want errors.Is(..., %v)", tc.name, err, want)
			}
			var typed *operationError
			if !errors.As(err, &typed) || typed.Operation != tc.name {
				t.Fatalf("%s error = %v, want typed identity", tc.name, err)
			}
		})
	}
}

type operationError struct{ Operation string }

func (e *operationError) Error() string { return e.Operation + " operation failed" }

func match(operation string) func(error) bool {
	return func(err error) bool {
		var typed *operationError
		return errors.As(err, &typed) && typed.Operation == operation
	}
}

type observer struct {
	dials  []transporttest.DialCall
	writes []transporttest.Message
	closes int
}

func (o *observer) DialCalls() []transporttest.DialCall {
	return append([]transporttest.DialCall(nil), o.dials...)
}
func (o *observer) WrittenMessages() []transporttest.Message {
	return append([]transporttest.Message(nil), o.writes...)
}
func (o *observer) CloseCount() int { return o.closes }

type dataDialer struct {
	o        *observer
	in       []transporttest.Message
	dialErr  error
	readErr  error
	writeErr error
	closeErr error
}

func newData(in []transporttest.Message, dialErr, readErr, writeErr, closeErr error) (transport.Dialer, transporttest.Observer) {
	o := &observer{}
	return &dataDialer{o: o, in: cloneMessages(in), dialErr: dialErr, readErr: readErr, writeErr: writeErr, closeErr: closeErr}, o
}
func newDataOnly(in []transporttest.Message, dialErr, readErr, writeErr, closeErr error) transport.Dialer {
	dialer, _ := newData(in, dialErr, readErr, writeErr, closeErr)
	return dialer
}
func (d *dataDialer) Dial(endpoint string, headers map[string]string) (transport.Conn, error) {
	if d.dialErr != nil {
		return nil, fmt.Errorf("rtc dial: %w", d.dialErr)
	}
	d.o.dials = append(d.o.dials, transporttest.DialCall{Endpoint: endpoint, Headers: cloneHeaders(headers)})
	return &dataConn{o: d.o, in: cloneMessages(d.in), readErr: d.readErr, writeErr: d.writeErr, closeErr: d.closeErr}, nil
}

type dataConn struct {
	o                           *observer
	in                          []transporttest.Message
	readErr, writeErr, closeErr error
}

func (c *dataConn) ReadMessage() (int, []byte, error) {
	if c.readErr != nil {
		return 0, nil, fmt.Errorf("rtc read: %w", c.readErr)
	}
	if len(c.in) == 0 {
		return 0, nil, io.EOF
	}
	m := c.in[0]
	c.in = c.in[1:]
	return m.Type, append([]byte(nil), m.Payload...), nil
}
func (c *dataConn) WriteMessage(messageType int, payload []byte) error {
	if c.writeErr != nil {
		return fmt.Errorf("rtc write: %w", c.writeErr)
	}
	c.o.writes = append(c.o.writes, transporttest.Message{Type: messageType, Payload: append([]byte(nil), payload...)})
	return nil
}
func (c *dataConn) Close() error {
	c.o.closes++
	if c.closeErr != nil {
		return fmt.Errorf("rtc close: %w", c.closeErr)
	}
	return nil
}

type noOpDialer struct{}

func (*noOpDialer) Dial(string, map[string]string) (transport.Conn, error) { return &noOpConn{}, nil }

type noOpConn struct{}

func (*noOpConn) ReadMessage() (int, []byte, error) { return 0, nil, nil }
func (*noOpConn) WriteMessage(int, []byte) error    { return nil }
func (*noOpConn) Close() error                      { return nil }

type inboundStub struct{}

func (*inboundStub) ReadFrame(context.Context) (rtc.PCMFrame, error) { return rtc.PCMFrame{}, nil }
func (*inboundStub) Close() error                                    { return nil }

type outboundStub struct{}

func (*outboundStub) WriteFrame(context.Context, rtc.PCMFrame) error { return nil }
func (*outboundStub) Close() error                                   { return nil }

func cloneMessages(messages []transporttest.Message) []transporttest.Message {
	cloned := make([]transporttest.Message, len(messages))
	for i, message := range messages {
		cloned[i] = transporttest.Message{Type: message.Type, Payload: append([]byte(nil), message.Payload...)}
	}
	return cloned
}
func cloneHeaders(headers map[string]string) map[string]string {
	cloned := make(map[string]string, len(headers))
	for key, value := range headers {
		cloned[key] = value
	}
	return cloned
}
