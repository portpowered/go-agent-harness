package transport

// MessageObservation reports a completed connection operation. Payload is
// borrowed for the duration of the callback. Observers must copy retained bytes
// and use bounded, nonblocking admission; they must not perform disk/network I/O.
// A successful write means the local transport accepted the complete message,
// not that the remote application processed it. Headers are never exposed.
type MessageObservation struct {
	Direction   string
	MessageType int
	Payload     []byte
	Err         error
}

type ObservingDialer struct {
	Inner   Dialer
	Observe func(MessageObservation)
}

func (d ObservingDialer) Dial(endpoint string, headers map[string]string) (Conn, error) {
	conn, err := d.Inner.Dial(endpoint, headers)
	if err != nil {
		return nil, err
	}
	if d.Observe == nil {
		return conn, nil
	}
	return &observingConn{Conn: conn, observe: d.Observe}, nil
}

type observingConn struct {
	Conn
	observe func(MessageObservation)
}

func (c *observingConn) ReadMessage() (int, []byte, error) {
	kind, payload, err := c.Conn.ReadMessage()
	c.observe(MessageObservation{Direction: "receive", MessageType: kind, Payload: payload, Err: err})
	return kind, payload, err
}

func (c *observingConn) WriteMessage(kind int, payload []byte) error {
	err := c.Conn.WriteMessage(kind, payload)
	c.observe(MessageObservation{Direction: "send", MessageType: kind, Payload: payload, Err: err})
	return err
}
