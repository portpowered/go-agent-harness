package events

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
)

const (
	readHeaderTimeout = 5 * time.Second
	shutdownTimeout   = 5 * time.Second
)

// Server owns the TCP listener that serves one room stream.
type Server struct {
	stream   rooms.RoomEventStream
	server   *http.Server
	listener net.Listener
	done     chan error
}

// Start listens on address and serves stream at GET /events.
func Start(address string, stream rooms.RoomEventStream) (*Server, error) {
	address = strings.TrimSpace(address)
	if address == "" {
		return nil, errors.New("room stream address is empty")
	}
	if stream == nil {
		return nil, errors.New("room stream is nil")
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, fmt.Errorf("listen room stream on %q: %w", address, err)
	}
	server := &http.Server{Handler: NewHandler(stream), ReadHeaderTimeout: readHeaderTimeout}
	eventServer := &Server{stream: stream, server: server, listener: listener, done: make(chan error, 1)}
	go func() {
		serveErr := server.Serve(listener)
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		}
		eventServer.done <- serveErr
	}()
	return eventServer, nil
}

// URL is the stream's client address.
func (s *Server) URL() string { return "http://" + s.listener.Addr().String() + Path }

// Shutdown closes the room stream, which ends every open response, then
// stops the listener within a bounded grace period. Caller cancellation does
// not cut the grace period short.
func (s *Server) Shutdown(ctx context.Context) error {
	if s == nil {
		return nil
	}
	closeErr := s.stream.Close()
	shutdownContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	shutdownErr := s.server.Shutdown(shutdownContext)
	if shutdownErr != nil {
		shutdownErr = errors.Join(shutdownErr, s.server.Close())
	}
	serveErr := <-s.done
	return errors.Join(closeErr, shutdownErr, serveErr)
}
