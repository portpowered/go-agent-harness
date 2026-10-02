package fakelive

import (
	"errors"
	"sync"
)

// ErrClosed is returned by an in-process connection after either side closed
// it and every frame sent before the close was read.
var ErrClosed = errors.New("fakelive: connection closed")

// wireConn is the server's side of one connection: an in-process pipe end or
// a gorilla WebSocket.
type wireConn interface {
	ReadMessage() (int, []byte, error)
	WriteMessage(int, []byte) error
	Close() error
}

type frame struct {
	messageType int
	payload     []byte
}

// queue is an unbounded FIFO of frames, so a writer never blocks on a reader
// that has stopped reading.
type queue struct {
	mu    sync.Mutex
	items []frame
	ready chan struct{}
}

func newQueue() *queue { return &queue{ready: make(chan struct{}, 1)} }

func (q *queue) push(f frame) {
	q.mu.Lock()
	q.items = append(q.items, f)
	q.mu.Unlock()
	select {
	case q.ready <- struct{}{}:
	default:
	}
}

func (q *queue) take() (frame, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) == 0 {
		return frame{}, false
	}
	f := q.items[0]
	q.items = q.items[1:]
	return f, true
}

// pop returns the next frame. Frames queued before a close stay readable.
func (q *queue) pop(closed <-chan struct{}) (frame, error) {
	for {
		if f, ok := q.take(); ok {
			return f, nil
		}
		select {
		case <-q.ready:
			continue
		case <-closed:
		}
		if f, ok := q.take(); ok {
			return f, nil
		}
		return frame{}, ErrClosed
	}
}

// pipe is one in-process connection between a client and the fake server.
type pipe struct {
	toClient *queue
	toServer *queue
	once     sync.Once
	closed   chan struct{}
}

func newPipe() *pipe {
	return &pipe{toClient: newQueue(), toServer: newQueue(), closed: make(chan struct{})}
}

func (p *pipe) close() { p.once.Do(func() { close(p.closed) }) }

func (p *pipe) isClosed() bool {
	select {
	case <-p.closed:
		return true
	default:
		return false
	}
}

// serverEnd is the fake server's side of a pipe.
type serverEnd struct{ p *pipe }

func (e serverEnd) ReadMessage() (int, []byte, error) {
	f, err := e.p.toServer.pop(e.p.closed)
	return f.messageType, f.payload, err
}

func (e serverEnd) WriteMessage(messageType int, payload []byte) error {
	if e.p.isClosed() {
		return ErrClosed
	}
	e.p.toClient.push(frame{messageType: messageType, payload: append([]byte(nil), payload...)})
	return nil
}

func (e serverEnd) Close() error {
	e.p.close()
	return nil
}

// clientConn is the transport.Conn handed to the client. It records every
// write and close on the server synchronously, and fails with the injected
// connection errors.
type clientConn struct {
	p      *pipe
	server *Server
	faults ConnFaults
}

func (c *clientConn) ReadMessage() (int, []byte, error) {
	if c.faults.Read != nil {
		return 0, nil, c.faults.Read
	}
	f, err := c.p.toClient.pop(c.p.closed)
	return f.messageType, f.payload, err
}

func (c *clientConn) WriteMessage(messageType int, payload []byte) error {
	if c.faults.Write != nil {
		return c.faults.Write
	}
	if c.p.isClosed() {
		return ErrClosed
	}
	owned := append([]byte(nil), payload...)
	c.server.recordWrite(messageType, owned)
	c.p.toServer.push(frame{messageType: messageType, payload: owned})
	return nil
}

func (c *clientConn) Close() error {
	c.server.recordClose()
	c.p.close()
	return c.faults.Close
}
