package openailive

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gorilla/websocket"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/logging"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/internal/realtime"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/internal/livesession"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

// ProviderName is the session provider name of the GPT-Live protocol.
const ProviderName = "openai-live"

// startEventID is the event id of the session.start this provider sends.
const startEventID = "evt_start_1"

// ErrCredentialProviderRequired reports a Provider built without a
// credential source. The provider never invents auth headers.
var ErrCredentialProviderRequired = errors.New("openailive: a credential provider is required")

// StartupError is the error event the server sent instead of
// session.started. The session never started, and the socket is closed.
type StartupError struct {
	Type    string
	Code    string
	Param   string
	Message string
}

func (e *StartupError) Error() string {
	detail := e.Code
	if e.Param != "" {
		detail += " (" + e.Param + ")"
	}
	return fmt.Sprintf("openailive: session.start rejected: %s: %s", detail, e.Message)
}

// Provider is the GPT-Live session provider: one primary WebSocket per
// session, client delegation, no tools in the voice loop.
type Provider struct {
	credentials      CredentialProvider
	endpoint         string
	dialer           transport.Dialer
	logger           logging.Logger
	clock            clock.TimerSource
	segmentGap       time.Duration
	delegationSettle time.Duration
	closeTimeout     time.Duration
}

var _ providers.SessionProvider = (*Provider)(nil)

// New returns a Provider configured by options.
func New(options ...Option) *Provider {
	p := &Provider{
		endpoint:         DefaultEndpoint,
		logger:           logging.DummyLogger(),
		clock:            clock.Real{},
		segmentGap:       DefaultSegmentGap,
		delegationSettle: DefaultDelegationSettle,
		closeTimeout:     DefaultCloseTimeout,
	}
	for _, option := range options {
		option(p)
	}
	if p.endpoint == "" {
		p.endpoint = DefaultEndpoint
	}
	if p.logger == nil {
		p.logger = logging.DummyLogger()
	}
	if p.clock == nil {
		p.clock = clock.Real{}
	}
	return p
}

// Name returns "openai-live".
func (*Provider) Name() string { return ProviderName }

// NewDefaultWebSocketDialer returns the live GPT-Live WebSocket dialer.
func NewDefaultWebSocketDialer() transport.Dialer { return realtime.NewDialer() }

// ConnectSession dials the primary WebSocket with the credential provider's
// headers, sends session.start built from cfg, and waits (bounded by ctx)
// for session.started. A startup error event fails with *StartupError.
func (p *Provider) ConnectSession(ctx context.Context, cfg models.SessionConfig) (messages.Session, error) {
	if p.dialer == nil {
		return nil, errors.New("openailive: websocket dialer is required")
	}
	if p.credentials == nil {
		return nil, ErrCredentialProviderRequired
	}
	start, err := BuildSessionStart(startEventID, cfg)
	if err != nil {
		return nil, err
	}
	frame, err := EncodeEvent(start)
	if err != nil {
		return nil, err
	}
	headers, err := p.credentials(ctx)
	if err != nil {
		return nil, fmt.Errorf("openailive: credentials: %w", err)
	}
	conn, err := p.dialer.Dial(p.endpoint, headers)
	if err != nil {
		return nil, fmt.Errorf("openailive: dial websocket: %w", err)
	}
	p.logger.Info("openai live: websocket connected", logging.Field{Key: "url", Value: p.endpoint})
	if err := conn.WriteMessage(websocket.TextMessage, frame); err != nil {
		return nil, errors.Join(fmt.Errorf("openailive: send session.start: %w", err), conn.Close())
	}
	started, err := awaitStarted(ctx, conn)
	if err != nil {
		return nil, errors.Join(err, conn.Close())
	}
	format := *start.Session.Audio.Format
	session := livesession.New(conn, p.logger, livesession.Settings{
		Name:             "openai live",
		MediaName:        "OpenAI Live",
		Format:           livesession.Format{Type: format.Type, Rate: format.Rate},
		Clock:            p.clock,
		SegmentGap:       p.segmentGap,
		DelegationSettle: p.delegationSettle,
		CloseTimeout:     p.closeTimeout,
	}, publicDialect{})
	session.Open(ctx, started.Session.ID, started.Session.Model)
	return session, nil
}

// awaitStarted reads frames until session.started or a startup error. The
// read runs on its own goroutine so ctx can abandon it by closing conn.
func awaitStarted(ctx context.Context, conn transport.Conn) (SessionStarted, error) {
	type result struct {
		started SessionStarted
		err     error
	}
	done := make(chan result, 1)
	go func() {
		started, err := readUntilStarted(conn)
		done <- result{started: started, err: err}
	}()
	select {
	case r := <-done:
		return r.started, r.err
	case <-ctx.Done():
		// Closing the connection releases the blocked read.
		closeErr := conn.Close()
		<-done
		return SessionStarted{}, realtime.JoinOnFailure(fmt.Errorf("openailive: wait for session.started: %w", ctx.Err()), closeErr)
	}
}

func readUntilStarted(conn transport.Conn) (SessionStarted, error) {
	for {
		_, payload, err := conn.ReadMessage()
		if err != nil {
			return SessionStarted{}, fmt.Errorf("openailive: wait for session.started: %w", err)
		}
		event, err := DecodeServerEvent(payload)
		if err != nil {
			return SessionStarted{}, err
		}
		switch typed := event.(type) {
		case SessionStarted:
			return typed, nil
		case ErrorEvent:
			startup := &StartupError{Type: typed.Error.Type, Code: typed.Error.Code, Message: typed.Error.Message}
			if typed.Error.Param != nil {
				startup.Param = *typed.Error.Param
			}
			return SessionStarted{}, startup
		}
		// Notices before the acknowledgement (info, usage) carry nothing the
		// handshake needs.
	}
}
