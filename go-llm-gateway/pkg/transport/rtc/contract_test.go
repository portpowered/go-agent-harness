package rtc_test

import sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport/rtc"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport/transporttest"
)

// Transport operation names shared by the contract fixtures.
const (
	opDial  = "dial"
	opRead  = "read"
	opWrite = "write"
	opClose = "close"
)

var (
	_ transport.Dialer          = (*dataDialer)(nil)
	_ transport.Conn            = (*dataConn)(nil)
	_ rtc.Dialer                = (*dataDialer)(nil)
	_ rtc.Conn                  = (*dataConn)(nil)
	_ sharedaudio.InboundMedia  = (*inboundStub)(nil)
	_ sharedaudio.OutboundMedia = (*outboundStub)(nil)
)

func TestRTCDataS11Conformance(t *testing.T) { transporttest.RunS11(t, s11Harness()) }

func s11Harness() transporttest.ConformanceHarness {
	dialErr := &operationError{opDial}
	readErr := &operationError{opRead}
	writeErr := &operationError{opWrite}
	closeErr := &operationError{opClose}
	h := transporttest.ConformanceHarness{
		Endpoint: "rtc://memory/s11",
		Headers:  map[string]string{"Authorization": "test", "X-Trace": "s11"},
		Inbound:  []transporttest.Message{{Type: 7, Payload: []byte{0, 1, 2}}, {Type: -4, Payload: []byte("inbound-second")}},
		Outbound: []transporttest.Message{{Type: 3, Payload: []byte{9, 0, 8}}, {Type: 11, Payload: []byte("outbound-second")}},
	}
	h.NewValid = func() (transport.Dialer, transporttest.Observer) { return newData(h.Inbound, nil, nil, nil, nil) }
	h.DialFailure, h.ReadFailure, h.WriteFailure, h.CloseFailure = failure(opDial, dialErr), failure(opRead, readErr), failure(opWrite, writeErr), failure(opClose, closeErr)
	return h
}

func failure(op string, want error) transporttest.FailureCase {
	var dErr, rErr, wErr, cErr error
	switch op {
	case opDial:
		dErr = want
	case opRead:
		rErr = want
	case opWrite:
		wErr = want
	case opClose:
		cErr = want
	}
	return transporttest.FailureCase{
		New: func() transport.Dialer { return newDataOnly(nil, dErr, rErr, wErr, cErr) }, WantErr: want, MatchErr: match(op),
	}
}

func TestRTCS4OperationErrorIdentity(t *testing.T) {
	cases := []struct{ name string }{{opDial}, {opRead}, {opWrite}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := &operationError{tc.name}
			var dErr, rErr, wErr error
			switch tc.name {
			case opDial:
				dErr = want
			case opRead:
				rErr = want
			case opWrite:
				wErr = want
			}
			conn, err := newDataOnly(nil, dErr, rErr, wErr, nil).Dial("rtc://memory/s4", map[string]string{"X-Test": "s4"})
			if tc.name == opDial {
				if conn != nil {
					t.Fatal("failed Dial returned a connection")
				}
			} else {
				if err != nil || conn == nil {
					t.Fatalf("setup Dial = (%v, %v)", conn, err)
				}
				defer closeForTest(t, conn)
				if tc.name == opRead {
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

type inboundStub struct{}

func (*inboundStub) ReadFrame(context.Context) (sharedaudio.PCMFrame, error) {
	return sharedaudio.PCMFrame{}, nil
}
func (*inboundStub) Close() error { return nil }

type outboundStub struct{}

func (*outboundStub) WriteFrame(context.Context, sharedaudio.PCMFrame) error { return nil }
func (*outboundStub) Close() error                                           { return nil }

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

func closeForTest(t *testing.T, conn io.Closer) {
	t.Helper()
	if err := conn.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}
