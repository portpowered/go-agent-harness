// Package fakecodex is an in-process fake of the ChatGPT-credential GPT-Live
// backend (route C of docs/architecture/chatgpt-oauth.md). It is test
// support: only _test.go files may import it
// (docs/architecture/test-support-packages.md).
//
// A Backend is an http.Handler for an httptest server. It serves:
//
//   - POST .../backend-api/codex/realtime/calls?intent=quicksilver&architecture=avas:
//     it checks the query, the JSON body {sdp, session} and the identity
//     headers (bearer, chatgpt-account-id, OpenAI-Alpha: quicksilver=v2,
//     originator and three distinct session ids), answers the offer with an
//     in-process pion peer (codexrtc.Peer) on a VirtualNetwork, and replies
//     201 with the SDP answer and Location: /v1/live/{call_id};
//   - GET /v1/live/{call_id}: the sideband WebSocket. It requires the same
//     identity headers as the call, sends the scripted server events, records
//     every client event, and answers session.close with a normal close.
//
// Failures are configurable (call status, Location, answer, sideband
// status). Every request that breaks the protocol is answered 400 and
// recorded in Errors.
package fakecodex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/codexrtc"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/quicksilver"
)

// Defaults of the fake.
const (
	DefaultCallID    = "rtc_fake_1"
	DefaultToken     = "chatgpt-access-token"
	DefaultAccountID = "acct_fake_1"

	callSuffix   = "/backend-api/codex" + codexrtc.CallPath
	sidebandPath = "/v1/live/"
)

// CallRecord is one accepted call-creation request.
type CallRecord struct {
	Header   http.Header
	OfferSDP string
	Session  quicksilver.SessionConfig
}

// Option configures a Backend.
type Option func(*Backend)

// WithCredential sets the bearer token and account id every request must
// carry. The defaults are DefaultToken and DefaultAccountID.
func WithCredential(token, accountID string) Option {
	return func(b *Backend) { b.token, b.accountID = token, accountID }
}

// WithPeerNetwork makes call creation answer with a pion peer on network's
// server host. Without it, or WithAnswer, call creation fails.
func WithPeerNetwork(network *VirtualNetwork) Option {
	return func(b *Backend) { b.network = network }
}

// WithAnswer replies with answer instead of a real peer's SDP.
func WithAnswer(answer string) Option { return func(b *Backend) { b.answer = &answer } }

// WithCallStatus rejects call creation with status.
func WithCallStatus(status int) Option { return func(b *Backend) { b.callStatus = status } }

// WithLocation replies with location instead of /v1/live/{call_id}; an empty
// location omits the header.
func WithLocation(location string) Option { return func(b *Backend) { b.location = &location } }

// WithSessionIDHeader adds an openai-session-id response header.
func WithSessionIDHeader(id string) Option { return func(b *Backend) { b.sessionIDHeader = id } }

// WithSidebandStatus rejects the sideband handshake with status.
func WithSidebandStatus(status int) Option { return func(b *Backend) { b.sidebandStatus = status } }

// WithSidebandEvents sets the server events sent when a sideband connects.
func WithSidebandEvents(events ...quicksilver.Event) Option {
	return func(b *Backend) { b.script = append([]quicksilver.Event(nil), events...) }
}

// WithSidebandDrop drops each sideband's TCP connection, without a close
// frame, right after the scripted events.
func WithSidebandDrop() Option { return func(b *Backend) { b.dropSideband = true } }

// WithSidebandStall makes each sideband stop reading after the scripted
// events, like a peer that hangs, until Backend.Close.
func WithSidebandStall() Option { return func(b *Backend) { b.stallSideband = true } }

// Backend is the fake. It serves one call; later calls replace it.
type Backend struct {
	token, accountID string
	network          *VirtualNetwork
	answer           *string
	callStatus       int
	location         *string
	sessionIDHeader  string
	sidebandStatus   int
	script           []quicksilver.Event
	dropSideband     bool
	stallSideband    bool
	stop             chan struct{}
	stopOnce         sync.Once
	upgrader         websocket.Upgrader

	mu      sync.Mutex
	call    *CallRecord
	peer    *codexrtc.Peer
	events  []quicksilver.Event
	errs    []error
	changed chan struct{}
}

// New returns a Backend configured by options.
func New(options ...Option) *Backend {
	b := &Backend{token: DefaultToken, accountID: DefaultAccountID, changed: make(chan struct{}), stop: make(chan struct{})}
	for _, option := range options {
		option(b)
	}
	return b
}

// ServeHTTP routes call creation and the sideband.
func (b *Backend) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasSuffix(r.URL.Path, callSuffix):
		b.serveCall(w, r)
	case strings.HasPrefix(r.URL.Path, sidebandPath):
		b.serveSideband(w, r)
	default:
		b.reject(w, http.StatusNotFound, fmt.Errorf("unexpected path %s", r.URL.Path))
	}
}

// Calls returns the accepted call-creation requests (zero or one).
func (b *Backend) Calls() []CallRecord {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.call == nil {
		return nil
	}
	return []CallRecord{*b.call}
}

// Peer returns the answer peer of the current call, or nil.
func (b *Backend) Peer() *codexrtc.Peer {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.peer
}

// Errors returns every protocol violation seen so far.
func (b *Backend) Errors() []error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]error(nil), b.errs...)
}

// ClientEvents returns the client events received on sidebands, in order.
func (b *Backend) ClientEvents() []quicksilver.Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]quicksilver.Event(nil), b.events...)
}

// WaitClientEvents waits until at least n client events have arrived.
func (b *Backend) WaitClientEvents(ctx context.Context, n int) ([]quicksilver.Event, error) {
	for {
		b.mu.Lock()
		events, changed := append([]quicksilver.Event(nil), b.events...), b.changed
		b.mu.Unlock()
		if len(events) >= n {
			return events, nil
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return events, fmt.Errorf("fakecodex: %d of %d client events: %w", len(events), n, context.Cause(ctx))
		}
	}
}

// Close releases stalled sidebands and closes the answer peer.
func (b *Backend) Close() error {
	b.stopOnce.Do(func() { close(b.stop) })
	if peer := b.Peer(); peer != nil {
		return peer.Close()
	}
	return nil
}

func (b *Backend) reject(w http.ResponseWriter, status int, err error) {
	b.record(err)
	http.Error(w, err.Error(), status)
}

// record keeps a non-nil error for Errors.
func (b *Backend) record(err error) {
	if err == nil {
		return
	}
	b.mu.Lock()
	b.errs = append(b.errs, err)
	b.mu.Unlock()
}

// ignoreClosed drops the error of using a connection the client already
// closed.
func ignoreClosed(err error) error {
	if errors.Is(err, net.ErrClosed) || errors.Is(err, websocket.ErrCloseSent) {
		return nil
	}
	return err
}

func (b *Backend) serveCall(w http.ResponseWriter, r *http.Request) {
	record, err := b.validateCall(r)
	if err != nil {
		b.reject(w, http.StatusBadRequest, err)
		return
	}
	if b.callStatus != 0 {
		http.Error(w, "Authorization: Bearer "+b.token+" rejected", b.callStatus)
		return
	}
	answer, peer, err := b.answerOffer(r.Context(), record.OfferSDP)
	if err != nil {
		b.reject(w, http.StatusInternalServerError, err)
		return
	}
	b.mu.Lock()
	previous := b.peer
	b.call, b.peer = &record, peer
	b.mu.Unlock()
	if previous != nil {
		b.record(previous.Close())
	}
	location := sidebandPath + DefaultCallID
	if b.location != nil {
		location = *b.location
	}
	if location != "" {
		w.Header().Set("Location", location)
	}
	if b.sessionIDHeader != "" {
		w.Header().Set(codexrtc.HeaderOpenAISessionID, b.sessionIDHeader)
	}
	w.Header().Set("Content-Type", "application/sdp")
	w.WriteHeader(http.StatusCreated)
	_, err = w.Write([]byte(answer))
	b.record(err)
}

func (b *Backend) validateCall(r *http.Request) (CallRecord, error) {
	if r.Method != http.MethodPost {
		return CallRecord{}, fmt.Errorf("call creation method %s, want POST", r.Method)
	}
	if r.URL.RawQuery != codexrtc.CallQuery {
		return CallRecord{}, fmt.Errorf("call creation query %q, want %q", r.URL.RawQuery, codexrtc.CallQuery)
	}
	if contentType := r.Header.Get("Content-Type"); contentType != "application/json" {
		return CallRecord{}, fmt.Errorf("call creation content type %q, want application/json", contentType)
	}
	if err := b.validateIdentity(r.Header); err != nil {
		return CallRecord{}, err
	}
	var body struct {
		SDP     string                    `json:"sdp"`
		Session quicksilver.SessionConfig `json:"session"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		return CallRecord{}, fmt.Errorf("call creation body: %w", err)
	}
	if body.SDP == "" || body.Session.Model == "" || body.Session.Delegation.Type != quicksilver.DelegationClient {
		return CallRecord{}, errors.New("call creation needs an offer, a model and client delegation")
	}
	return CallRecord{Header: r.Header.Clone(), OfferSDP: body.SDP, Session: body.Session}, nil
}

func (b *Backend) validateIdentity(header http.Header) error {
	want := map[string]string{
		codexrtc.HeaderAuthorization: "Bearer " + b.token,
		codexrtc.HeaderAccountID:     b.accountID,
		codexrtc.HeaderAlpha:         codexrtc.AlphaQuicksilverV2,
	}
	for name, value := range want {
		if got := header.Get(name); got != value {
			return fmt.Errorf("header %s = %q, want %q", name, got, value)
		}
	}
	if header.Get(codexrtc.HeaderOriginator) == "" {
		return errors.New("header Originator is missing")
	}
	ids := map[string]bool{}
	for _, name := range []string{codexrtc.HeaderSessionID, codexrtc.HeaderThreadID, codexrtc.HeaderXSessionID} {
		id := header.Get(name)
		if id == "" || ids[id] {
			return fmt.Errorf("header %s = %q is missing or repeats another session id", name, id)
		}
		ids[id] = true
	}
	return nil
}

func (b *Backend) answerOffer(ctx context.Context, offer string) (string, *codexrtc.Peer, error) {
	if b.answer != nil {
		return *b.answer, nil, nil
	}
	if b.network == nil {
		return "", nil, errors.New("no peer network: use WithPeerNetwork or WithAnswer")
	}
	peer, err := codexrtc.NewPeer(codexrtc.PeerConfig{SettingEngine: b.network.Server})
	if err != nil {
		return "", nil, err
	}
	answer, err := peer.Answer(ctx, offer)
	if err != nil {
		return "", nil, errors.Join(err, peer.Close())
	}
	return answer, peer, nil
}

func (b *Backend) serveSideband(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	call := b.call
	b.mu.Unlock()
	if b.sidebandStatus != 0 {
		http.Error(w, "sideband rejected", b.sidebandStatus)
		return
	}
	if call == nil || strings.TrimPrefix(r.URL.Path, sidebandPath) != DefaultCallID {
		http.Error(w, "no such call", http.StatusNotFound)
		return
	}
	if err := sameIdentity(call.Header, r.Header); err != nil {
		b.reject(w, http.StatusUnauthorized, err)
		return
	}
	conn, err := b.upgrader.Upgrade(w, r, nil)
	if err != nil {
		b.record(fmt.Errorf("sideband upgrade: %w", err))
		return
	}
	defer func() { b.record(ignoreClosed(conn.Close())) }()
	for _, event := range b.script {
		frame, err := quicksilver.EncodeEvent(event)
		if err != nil || conn.WriteMessage(websocket.TextMessage, frame) != nil {
			return
		}
	}
	if b.dropSideband {
		return
	}
	if b.stallSideband {
		<-b.stop
		return
	}
	b.readClientEvents(conn)
}

// sameIdentity requires the sideband to repeat the call's identity headers.
func sameIdentity(call, sideband http.Header) error {
	for _, name := range []string{
		codexrtc.HeaderAuthorization, codexrtc.HeaderAccountID, codexrtc.HeaderAlpha, codexrtc.HeaderOriginator,
		codexrtc.HeaderSessionID, codexrtc.HeaderThreadID, codexrtc.HeaderXSessionID,
	} {
		if got, want := sideband.Get(name), call.Get(name); got != want {
			return fmt.Errorf("sideband header %s = %q, want the call's %q", name, got, want)
		}
	}
	return nil
}

func (b *Backend) readClientEvents(conn *websocket.Conn) {
	for {
		_, frame, err := conn.ReadMessage()
		if err != nil {
			return
		}
		event, err := quicksilver.DecodeClientEvent(frame)
		b.mu.Lock()
		if err != nil {
			b.errs = append(b.errs, err)
		} else {
			b.events = append(b.events, event)
		}
		close(b.changed)
		b.changed = make(chan struct{})
		b.mu.Unlock()
		if _, closing := event.(quicksilver.SessionClose); closing {
			b.record(ignoreClosed(conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "session closed"))))
			return
		}
	}
}
