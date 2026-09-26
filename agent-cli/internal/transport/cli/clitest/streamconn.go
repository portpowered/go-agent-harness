package clitest

import (
	"io"
	"net"
	"os"
	"sync"
	"syscall"
	"time"
)

// newStreamConnPair returns two connected in-memory stream ends that behave
// like a loopback TCP connection rather than net.Pipe: writes are buffered
// and never wait for the peer's read; after the peer closed, the first write
// is accepted and dropped (as the kernel accepts it before the peer's reset)
// and later writes fail with EPIPE; reads drain buffered bytes before
// reporting io.EOF. Blocking uses only channels and timers, so both ends
// advance with a synctest bubble's virtual clock.
//
// Known differences from TCP:
//   - after the peer closed, TCP's first write may already fail (the reset
//     can arrive first) and later ones report EPIPE or ECONNRESET; here the
//     first always succeeds and later ones report EPIPE, and a read after
//     the peer closed reports io.EOF, never ECONNRESET;
//   - buffering is unbounded: there is no send window, so a writer is never
//     slowed by a peer that stops reading;
//   - there is no CloseWrite (half-close); Close ends both directions;
//   - a read whose deadline has passed still returns already-buffered bytes
//     (the deadline is checked only while waiting for data).
func newStreamConnPair() (net.Conn, net.Conn) {
	ab, ba := newStreamBuffer(), newStreamBuffer()
	return &streamConn{in: ba, out: ab}, &streamConn{in: ab, out: ba}
}

// streamBuffer is one direction of a stream connection.
type streamBuffer struct {
	mu         sync.Mutex
	data       []byte
	writerDone bool // the writing end closed: reads return EOF once drained
	readerDone bool // the reading end closed: writes are dropped, then refused
	reset      bool // a write after readerDone was dropped; the next fails
	changed    chan struct{}
}

func newStreamBuffer() *streamBuffer { return &streamBuffer{changed: make(chan struct{})} }

// signalLocked wakes every waiter; callers hold mu.
func (b *streamBuffer) signalLocked() {
	close(b.changed)
	b.changed = make(chan struct{})
}

func (b *streamBuffer) closeWriter() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.writerDone = true
	b.signalLocked()
}

func (b *streamBuffer) closeReader() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.readerDone = true
	b.data = nil
	b.signalLocked()
}

type streamConn struct {
	in, out   *streamBuffer
	closeOnce sync.Once

	deadlineMu    sync.Mutex
	readDeadline  time.Time
	writeDeadline time.Time
}

func (c *streamConn) Read(p []byte) (int, error) {
	for {
		c.in.mu.Lock()
		switch {
		case c.in.readerDone:
			c.in.mu.Unlock()
			return 0, net.ErrClosed
		case len(c.in.data) > 0:
			n := copy(p, c.in.data)
			c.in.data = c.in.data[n:]
			c.in.mu.Unlock()
			return n, nil
		case c.in.writerDone:
			c.in.mu.Unlock()
			return 0, io.EOF
		}
		wake := c.in.changed
		c.in.mu.Unlock()
		if err := c.wait(wake, c.deadline(false)); err != nil {
			return 0, err
		}
	}
}

func (c *streamConn) Write(p []byte) (int, error) {
	if deadline := c.deadline(true); !deadline.IsZero() && !time.Now().Before(deadline) {
		return 0, os.ErrDeadlineExceeded
	}
	c.out.mu.Lock()
	defer c.out.mu.Unlock()
	if c.out.writerDone {
		return 0, net.ErrClosed
	}
	if c.out.readerDone {
		if c.out.reset {
			return 0, &net.OpError{Op: "write", Net: pipeNetwork, Err: syscall.EPIPE}
		}
		c.out.reset = true
		return len(p), nil
	}
	c.out.data = append(c.out.data, p...)
	c.out.signalLocked()
	return len(p), nil
}

// wait blocks until wake fires or the deadline passes.
func (c *streamConn) wait(wake <-chan struct{}, deadline time.Time) error {
	if deadline.IsZero() {
		<-wake
		return nil
	}
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case <-wake:
		return nil
	case <-timer.C:
		return os.ErrDeadlineExceeded
	}
}

func (c *streamConn) deadline(write bool) time.Time {
	c.deadlineMu.Lock()
	defer c.deadlineMu.Unlock()
	if write {
		return c.writeDeadline
	}
	return c.readDeadline
}

// Close ends both directions: the peer reads EOF after the buffered bytes,
// and the peer's later writes are dropped.
func (c *streamConn) Close() error {
	c.closeOnce.Do(func() {
		c.out.closeWriter()
		c.in.closeReader()
	})
	return nil
}

func (c *streamConn) LocalAddr() net.Addr  { return pipeAddr{} }
func (c *streamConn) RemoteAddr() net.Addr { return pipeAddr{} }

func (c *streamConn) SetDeadline(t time.Time) error {
	c.deadlineMu.Lock()
	defer c.deadlineMu.Unlock()
	c.readDeadline, c.writeDeadline = t, t
	c.in.mu.Lock()
	c.in.signalLocked() // re-evaluate a blocked read against the new deadline
	c.in.mu.Unlock()
	return nil
}

func (c *streamConn) SetReadDeadline(t time.Time) error {
	c.deadlineMu.Lock()
	c.readDeadline = t
	c.deadlineMu.Unlock()
	c.in.mu.Lock()
	c.in.signalLocked()
	c.in.mu.Unlock()
	return nil
}

func (c *streamConn) SetWriteDeadline(t time.Time) error {
	c.deadlineMu.Lock()
	defer c.deadlineMu.Unlock()
	c.writeDeadline = t
	return nil
}
