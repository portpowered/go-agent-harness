// Package transport defines the provider-neutral contract for establishing and
// using bidirectional message connections.
//
// Dialer.Dial receives an endpoint and request headers. A successful dial
// returns a non-nil Conn and a nil error; a failed dial returns a nil Conn and
// preserves the underlying error for errors.Is or errors.As. The caller owns
// every successful connection and must close it when finished.
//
// Conn delivers messages in received order. ReadMessage and WriteMessage
// preserve the integer message type and payload bytes exactly. Operation
// errors must preserve their underlying identity. Endpoint, header maps, and
// write payloads remain caller-owned; implementations must not mutate them or
// retain them without first making a copy. A write completes before it returns
// successfully, so the message may be reused after the call returns.
//
// Provider implementations and shared record/replay helpers consume this
// contract directly. Provider packages may retain compatibility aliases, but
// no provider-specific adapter is required at this boundary.
package transport
