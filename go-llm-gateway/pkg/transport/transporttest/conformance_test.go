package transporttest

import (
	"errors"
	"fmt"
	"testing"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

func TestRunS11(t *testing.T) {
	dialErr := errors.New("dial sentinel")
	readErr := errors.New("read sentinel")
	writeErr := errors.New("write sentinel")
	h := ConformanceHarness{
		Endpoint: "memory://transport-s11",
		Headers:  map[string]string{"Authorization": "test", "X-Trace": "s11"},
		Inbound:  []Message{{Type: 7, Payload: []byte{0, 1, 2}}, {Type: -4, Payload: []byte("inbound-2")}},
		Outbound: []Message{{Type: 3, Payload: []byte{9, 0, 8}}, {Type: 11, Payload: []byte("outbound-2")}},
	}
	h.NewValid = func() (transport.Dialer, Observer) { return newFixture(h.Inbound, nil, nil, nil) }
	h.DialFailure = FailureCase{New: func() transport.Dialer { return newFailureFixture(dialErr, nil, nil) }, WantErr: dialErr}
	h.ReadFailure = FailureCase{New: func() transport.Dialer { return newFailureFixture(nil, readErr, nil) }, WantErr: readErr}
	h.WriteFailure = FailureCase{New: func() transport.Dialer { return newFailureFixture(nil, nil, writeErr) }, WantErr: writeErr}
	RunS11(t, h)
}

type fixtureObserver struct {
	dials  []DialCall
	writes []Message
	closes int
}

func (o *fixtureObserver) DialCalls() []DialCall      { return o.dials }
func (o *fixtureObserver) WrittenMessages() []Message { return o.writes }
func (o *fixtureObserver) CloseCount() int            { return o.closes }

type fixtureDialer struct {
	observer                   *fixtureObserver
	inbound                    []Message
	dialErr, readErr, writeErr error
}

func newFixture(inbound []Message, dialErr, readErr, writeErr error) (transport.Dialer, Observer) {
	o := &fixtureObserver{}
	return &fixtureDialer{observer: o, inbound: inbound, dialErr: dialErr, readErr: readErr, writeErr: writeErr}, o
}

func newFailureFixture(dialErr, readErr, writeErr error) transport.Dialer {
	dialer, _ := newFixture(nil, dialErr, readErr, writeErr)
	return dialer
}

func (d *fixtureDialer) Dial(endpoint string, headers map[string]string) (transport.Conn, error) {
	if d.dialErr != nil {
		return nil, fmt.Errorf("fixture dial: %w", d.dialErr)
	}
	if d.observer != nil {
		d.observer.dials = append(d.observer.dials, DialCall{Endpoint: endpoint, Headers: cloneHeaders(headers)})
	}
	return &fixtureConn{observer: d.observer, inbound: append([]Message(nil), d.inbound...), readErr: d.readErr, writeErr: d.writeErr}, nil
}

type fixtureConn struct {
	observer          *fixtureObserver
	inbound           []Message
	readErr, writeErr error
}

func (c *fixtureConn) ReadMessage() (int, []byte, error) {
	if c.readErr != nil {
		return 0, nil, fmt.Errorf("fixture read: %w", c.readErr)
	}
	message := c.inbound[0]
	c.inbound = c.inbound[1:]
	return message.Type, append([]byte(nil), message.Payload...), nil
}

func (c *fixtureConn) WriteMessage(messageType int, payload []byte) error {
	if c.writeErr != nil {
		return fmt.Errorf("fixture write: %w", c.writeErr)
	}
	if c.observer != nil {
		c.observer.writes = append(c.observer.writes, Message{Type: messageType, Payload: append([]byte(nil), payload...)})
	}
	return nil
}

func (c *fixtureConn) Close() error {
	if c.observer != nil {
		c.observer.closes++
	}
	return nil
}
