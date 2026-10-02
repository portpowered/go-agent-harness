// Package fakelive is a scripted, in-process fake of the GPT-Live primary
// WebSocket (wss://api.openai.com/v1/live/sessions). It is test support: only
// _test.go files may import it (docs/architecture/test-support-packages.md).
//
// A Server serves each connection the same way. It checks the auth headers a
// credential provider would supply (an API-key bearer or, for example, an
// OAuth token with an account header) and the empty query string, validates the strict session.start (answering
// session.started or an error such as unknown_parameter), records input
// audio and every client event, acknowledges mute and the three appends
// (with start_ms, end_ms and client_event_id), answers session.close with
// session.closed, and meanwhile runs a script of steps: send scripted server
// events, wait on an injected clock, wait for client events, emit
// session.closed with any reason, or drop the socket.
//
// Connections come from Dialer (an in-process transport.Dialer, for fast
// unit tests) or from ServeHTTP (a gorilla WebSocket upgrade, for
// httptest-based dialer tests).
package fakelive

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport/transporttest"
)

// ErrUnauthorized rejects a dial that lacks one of the required auth headers
// or carries a different value.
var ErrUnauthorized = errors.New("fakelive: missing or wrong auth header")

// ErrQueryNotAllowed rejects a dial whose URL has query parameters. GPT-Live
// takes the model in session.start, never in the URL.
var ErrQueryNotAllowed = errors.New("fakelive: the live endpoint takes no query parameters")

// Defaults for the resolved session the fake reports.
const (
	DefaultSessionID = "live_fake_1"
	DefaultExpiresAt = 1788555600
	DefaultVoice     = "marin"
	// DefaultAckSpan is the timeline span the fake gives each append ack.
	DefaultAckSpan = 200 * time.Millisecond
)

// ConnFaults are errors every in-process client connection returns from the
// matching operation. A nil field means the operation works.
type ConnFaults struct {
	Read  error
	Write error
	Close error
}

// Option configures a Server.
type Option func(*Server)

// WithAPIKey requires "Authorization: Bearer key" on every dial: the
// API-key credential.
func WithAPIKey(key string) Option {
	return WithAuthHeaders(map[string]string{"Authorization": "Bearer " + key})
}

// WithAuthHeaders requires every header in headers, with exactly that value,
// on every dial. Header names match case-insensitively. It stands for
// whatever a credential provider supplies, for example an OAuth bearer token
// plus an account header.
func WithAuthHeaders(headers map[string]string) Option {
	return func(s *Server) {
		for name, value := range headers {
			s.auth[http.CanonicalHeaderKey(name)] = value
		}
	}
}

// WithScript sets the steps run on every connection, in order, alongside the
// server's protocol handling.
func WithScript(steps ...Step) Option {
	return func(s *Server) { s.script = append([]Step(nil), steps...) }
}

// WithClock sets the clock Wait steps use. The default is the host clock,
// which is virtual inside a testing/synctest bubble.
func WithClock(source clock.TimerSource) Option { return func(s *Server) { s.clock = source } }

// WithoutAcks withholds the acknowledgement of the given client event types
// (for example openailive.TypeCommentaryAppend), to simulate a stalled
// session timeline.
func WithoutAcks(eventTypes ...string) Option {
	return func(s *Server) {
		for _, eventType := range eventTypes {
			s.withheld[eventType] = true
		}
	}
}

// WithDropOnClose makes the server answer session.close by dropping the
// socket without session.closed, so final usage is unconfirmed.
func WithDropOnClose() Option { return func(s *Server) { s.dropOnClose = true } }

// WithDialError makes every in-process dial fail with err.
func WithDialError(err error) Option { return func(s *Server) { s.dialErr = err } }

// WithConnFaults makes every in-process client connection fail as faults says.
func WithConnFaults(faults ConnFaults) Option { return func(s *Server) { s.faults = faults } }

// Server is a scripted fake GPT-Live server. It is safe for concurrent use
// and serves any number of connections.
type Server struct {
	auth        map[string]string
	script      []Step
	clock       clock.TimerSource
	withheld    map[string]bool
	dropOnClose bool
	dialErr     error
	faults      ConnFaults

	mu     sync.Mutex
	dials  []transporttest.DialCall
	writes []transporttest.Message
	closes int
	events []openailive.Event
	audio  []byte
	errs   []error
}

// New returns a Server configured by options.
func New(options ...Option) *Server {
	s := &Server{clock: clock.Real{}, withheld: map[string]bool{}, auth: map[string]string{}}
	for _, option := range options {
		option(s)
	}
	return s
}

// Dialer returns an in-process transport.Dialer. Each Dial checks the
// endpoint and headers, then starts a connection served by s.
func (s *Server) Dialer() transport.Dialer { return dialer{server: s} }

type dialer struct{ server *Server }

func (d dialer) Dial(endpoint string, headers map[string]string) (transport.Conn, error) {
	s := d.server
	s.recordDial(endpoint, headers)
	if s.dialErr != nil {
		return nil, fmt.Errorf("fakelive dial: %w", s.dialErr)
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("fakelive dial: %w", err)
	}
	if err := s.checkHandshake(parsed.RawQuery, func(name string) string { return headerValue(headers, name) }); err != nil {
		return nil, err
	}
	p := newPipe()
	go s.serve(serverEnd{p: p}, false)
	return &clientConn{p: p, server: s, faults: s.faults}, nil
}

// ServeHTTP upgrades a WebSocket request after the same checks Dial makes.
// A query string is refused with 400 and a missing or wrong auth header with
// 401.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	headers := make(map[string]string, len(r.Header))
	for key := range r.Header {
		headers[key] = r.Header.Get(key)
	}
	s.recordDial(r.URL.String(), headers)
	if err := s.checkHandshake(r.URL.RawQuery, r.Header.Get); err != nil {
		status := http.StatusUnauthorized
		if errors.Is(err, ErrQueryNotAllowed) {
			status = http.StatusBadRequest
		}
		http.Error(w, err.Error(), status)
		return
	}
	upgrader := websocket.Upgrader{}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.recordError(err)
		return
	}
	s.serve(conn, true)
}

func (s *Server) checkHandshake(rawQuery string, header func(string) string) error {
	if rawQuery != "" {
		return ErrQueryNotAllowed
	}
	for name, want := range s.auth {
		if header(name) != want {
			return fmt.Errorf("%w: %s", ErrUnauthorized, name)
		}
	}
	return nil
}

func headerValue(headers map[string]string, name string) string {
	for key, value := range headers {
		if strings.EqualFold(key, name) {
			return value
		}
	}
	return ""
}

// DialCalls returns every dial attempt, including refused ones.
func (s *Server) DialCalls() []transporttest.DialCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]transporttest.DialCall(nil), s.dials...)
}

// WrittenMessages returns every frame clients wrote, in order. In-process
// writes are recorded before WriteMessage returns.
func (s *Server) WrittenMessages() []transporttest.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]transporttest.Message(nil), s.writes...)
}

// CloseCount returns how many in-process client connections were closed.
func (s *Server) CloseCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closes
}

// ClientEvents returns every client frame the server decoded, in order.
func (s *Server) ClientEvents() []openailive.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]openailive.Event(nil), s.events...)
}

// InputAudio returns the bytes of every accepted session.input_audio.append.
func (s *Server) InputAudio() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.audio...)
}

// Errors returns failures inside the fake itself: a script step that could
// not run, or a WebSocket upgrade that failed.
func (s *Server) Errors() []error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]error(nil), s.errs...)
}

func (s *Server) recordDial(endpoint string, headers map[string]string) {
	clone := make(map[string]string, len(headers))
	for key, value := range headers {
		clone[key] = value
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dials = append(s.dials, transporttest.DialCall{Endpoint: endpoint, Headers: clone})
}

func (s *Server) recordWrite(messageType int, payload []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writes = append(s.writes, transporttest.Message{Type: messageType, Payload: append([]byte(nil), payload...)})
}

func (s *Server) recordClose() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closes++
}

func (s *Server) recordEvent(event openailive.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, event)
}

func (s *Server) recordAudio(audio []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.audio = append(s.audio, audio...)
}

func (s *Server) recordError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.errs = append(s.errs, err)
}
