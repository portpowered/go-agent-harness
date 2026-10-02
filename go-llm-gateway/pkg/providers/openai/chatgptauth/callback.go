package chatgptauth

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	callbackPath              = "/auth/callback"
	cancelPath                = "/cancel"
	callbackReadHeaderTimeout = 10 * time.Second
	callbackReadTimeout       = 10 * time.Second
	callbackWriteTimeout      = 10 * time.Second
	callbackIdleTimeout       = 30 * time.Second
	callbackShutdownTimeout   = 2 * time.Second
	missingEntitlementMarker  = "missing_codex_entitlement"
	htmlContentType           = "text/html; charset=utf-8"
)

// CallbackServer is the loopback HTTP listener that receives the OAuth
// redirect. It accepts exactly one outcome: a code with the expected state,
// an issuer error, or a cancel request.
type CallbackServer struct {
	server   *http.Server
	listener net.Listener
	state    string
	port     int

	once    sync.Once
	results chan callbackResult
	served  chan struct{}
}

type callbackResult struct {
	code string
	err  error
}

// ListenCallback binds the first free port of ports on 127.0.0.1 and starts
// serving the callback for state. Port 0 picks any free port (tests).
func ListenCallback(ctx context.Context, state string, ports ...int) (*CallbackServer, error) {
	if len(ports) == 0 {
		ports = []int{DefaultCallbackPort, FallbackCallbackPort}
	}
	var lc net.ListenConfig
	var errs []error
	for _, port := range ports {
		listener, err := lc.Listen(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if err != nil {
			errs = append(errs, err)
			continue
		}
		return startCallbackServer(listener, state), nil
	}
	return nil, fmt.Errorf("bind ChatGPT login callback: %w", errors.Join(errs...))
}

func startCallbackServer(listener net.Listener, state string) *CallbackServer {
	s := &CallbackServer{
		listener: listener,
		state:    state,
		results:  make(chan callbackResult, 1),
		served:   make(chan struct{}),
	}
	if addr, ok := listener.Addr().(*net.TCPAddr); ok {
		s.port = addr.Port
	}
	mux := http.NewServeMux()
	mux.HandleFunc(callbackPath, s.handleCallback)
	mux.HandleFunc(cancelPath, s.handleCancel)
	// Bounded timeouts keep a stalled or hostile local client from holding
	// the listener open for the rest of the login.
	s.server = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: callbackReadHeaderTimeout,
		ReadTimeout:       callbackReadTimeout,
		WriteTimeout:      callbackWriteTimeout,
		IdleTimeout:       callbackIdleTimeout,
	}
	go s.serve()
	return s
}

func (s *CallbackServer) serve() {
	defer close(s.served)
	if err := s.server.Serve(s.listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		s.deliver(callbackResult{err: fmt.Errorf("ChatGPT login callback server: %w", err)})
	}
}

// Port is the bound loopback port.
func (s *CallbackServer) Port() int { return s.port }

// RedirectURI is the redirect_uri registered for this listener. It names
// localhost, which is what the issuer's allow-list holds.
func (s *CallbackServer) RedirectURI() string {
	return "http://localhost:" + strconv.Itoa(s.port) + callbackPath
}

// Wait returns the authorization code, the issuer's error, ErrLoginCancelled,
// or ctx's error.
func (s *CallbackServer) Wait(ctx context.Context) (string, error) {
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case result := <-s.results:
		return result.code, result.err
	}
}

// Close stops the listener gracefully: it lets in-flight responses, such
// as the success page, reach the browser before the connections close, for
// at most callbackShutdownTimeout. It then waits for the serve loop.
func (s *CallbackServer) Close(ctx context.Context) error {
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), callbackShutdownTimeout)
	defer cancel()
	err := s.server.Shutdown(shutdownCtx)
	if err != nil {
		err = errors.Join(err, s.server.Close())
	}
	<-s.served
	return err
}

func (s *CallbackServer) deliver(result callbackResult) {
	s.once.Do(func() { s.results <- result })
}

func (s *CallbackServer) handleCallback(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	if subtle.ConstantTimeCompare([]byte(query.Get("state")), []byte(s.state)) != 1 {
		// A stale or forged redirect must not end the login: keep waiting.
		http.Error(w, "State mismatch", http.StatusBadRequest)
		return
	}
	if code := query.Get("error"); code != "" {
		err := callbackError(code, query.Get("error_description"))
		writePage(w, http.StatusForbidden, "Sign-in failed", err.Error())
		flushPage(w)
		s.deliver(callbackResult{err: err})
		return
	}
	code := query.Get("code")
	if code == "" {
		err := errors.New("ChatGPT login callback is missing the authorization code")
		writePage(w, http.StatusBadRequest, "Sign-in failed", err.Error())
		flushPage(w)
		s.deliver(callbackResult{err: err})
		return
	}
	writePage(w, http.StatusOK, "Signed in", "ChatGPT sign-in finished. You can close this window and return to the terminal.")
	// Push the page to the browser before waking the login, which then
	// closes the server.
	flushPage(w)
	s.deliver(callbackResult{code: code})
}

func (s *CallbackServer) handleCancel(w http.ResponseWriter, _ *http.Request) {
	writePage(w, http.StatusOK, "Cancelled", "Login cancelled.")
	flushPage(w)
	s.deliver(callbackResult{err: ErrLoginCancelled})
}

func callbackError(code, description string) error {
	if code == "access_denied" && strings.Contains(strings.ToLower(description), missingEntitlementMarker) {
		return errors.New("codex is not enabled for this ChatGPT workspace; ask the workspace administrator for Codex access")
	}
	if description != "" {
		return fmt.Errorf("ChatGPT sign-in failed: %s: %s", code, truncateDetail(description))
	}
	return fmt.Errorf("ChatGPT sign-in failed: %s", code)
}

func writePage(w http.ResponseWriter, status int, title, message string) {
	w.Header().Set(contentTypeHeader, htmlContentType)
	w.Header().Set("Connection", "close")
	w.WriteHeader(status)
	page := "<!doctype html><html><head><meta charset=\"utf-8\"><title>" + html.EscapeString(title) +
		"</title></head><body><h1>" + html.EscapeString(title) + "</h1><p>" + html.EscapeString(message) + "</p></body></html>"
	if _, err := w.Write([]byte(page)); err != nil {
		return
	}
}

// flushPage sends a written page now. A flush failure means the browser has
// gone; the login outcome does not depend on it.
func flushPage(w http.ResponseWriter) {
	if err := http.NewResponseController(w).Flush(); err != nil {
		return
	}
}
