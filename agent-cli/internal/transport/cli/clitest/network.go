package clitest

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

// PipeListener is an in-memory net.Listener. Every DialContext is one
// net.Pipe whose server end Accept returns, so HTTP and WebSocket servers and
// clients inside a synctest bubble block only on bubble channels and advance
// with its virtual clock. Any address dials the listener.
type PipeListener struct {
	conns     chan net.Conn
	closed    chan struct{}
	closeOnce sync.Once
}

// NewPipeListener returns an open in-memory listener.
func NewPipeListener() *PipeListener {
	return &PipeListener{conns: make(chan net.Conn), closed: make(chan struct{})}
}

// Accept returns the server end of the next dialed pipe.
func (l *PipeListener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.conns:
		return conn, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

// Close stops Accept and future dials; established pipes stay open.
func (l *PipeListener) Close() error {
	l.closeOnce.Do(func() { close(l.closed) })
	return nil
}

// Addr reports the listener's placeholder address.
func (l *PipeListener) Addr() net.Addr { return pipeAddr{} }

// DialContext connects a new pipe to the listener, whatever the address.
func (l *PipeListener) DialContext(ctx context.Context, _, _ string) (net.Conn, error) {
	client, server := net.Pipe()
	select {
	case l.conns <- server:
		return client, nil
	case <-l.closed:
		return nil, errors.Join(net.ErrClosed, client.Close(), server.Close())
	case <-ctx.Done():
		return nil, errors.Join(ctx.Err(), client.Close(), server.Close())
	}
}

type pipeAddr struct{}

func (pipeAddr) Network() string { return "pipe" }
func (pipeAddr) String() string  { return "pipe" }

// WebSocketDialer is a transport.Dialer (the CLI's provider transport port)
// that performs the real WebSocket handshake over the listener's pipes,
// exactly as the provider's default gorilla dialer does over TCP.
func WebSocketDialer(listener *PipeListener) transport.Dialer {
	return webSocketDialer{dialer: &websocket.Dialer{NetDialContext: listener.DialContext}}
}

type webSocketDialer struct {
	dialer *websocket.Dialer
}

func (d webSocketDialer) Dial(url string, headers map[string]string) (transport.Conn, error) {
	header := http.Header{}
	for key, value := range headers {
		header.Set(key, value)
	}
	conn, response, err := d.dialer.Dial(url, header)
	if response != nil {
		// Gorilla replaces the handshake body with an in-memory reader.
		err = errors.Join(err, response.Body.Close())
	}
	if err != nil {
		return nil, errors.Join(err, closeIfOpen(conn))
	}
	return conn, nil
}

func closeIfOpen(conn *websocket.Conn) error {
	if conn == nil {
		return nil
	}
	return conn.Close()
}

// Serve runs handler on listener until the test ends. The server and every
// connection it accepted are closed in t's cleanup, so no bubble goroutine
// outlives the test.
func Serve(t interface{ Cleanup(func()) }, listener *PipeListener, handler http.Handler) {
	server := &http.Server{Handler: handler}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = server.Serve(listener) //nolint:errcheck // Serve returns ErrServerClosed or net.ErrClosed once cleanup closes it.
	}()
	t.Cleanup(func() {
		_ = server.Close() //nolint:errcheck // closing an in-memory server cannot fail in a way the test acts on.
		<-done
	})
}
