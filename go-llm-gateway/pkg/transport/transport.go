package transport

// Dialer establishes a message connection to endpoint using headers.
//
// On success, Dial returns a non-nil Conn and nil error. On failure, it
// returns a nil Conn and an error whose identity is preserved for errors.Is or
// errors.As. The dialer must not mutate caller-owned headers.
type Dialer interface {
	Dial(endpoint string, headers map[string]string) (Conn, error)
}

// Conn is an ordered, bidirectional message connection.
//
// ReadMessage returns the next received message. WriteMessage sends one
// message and preserves its type and payload bytes. Close releases the
// connection; callers own and close every successful connection. Implementors
// should preserve operation-error identity so callers can use errors.Is or
// errors.As.
type Conn interface {
	ReadMessage() (messageType int, payload []byte, err error)
	WriteMessage(messageType int, payload []byte) error
	Close() error
}
