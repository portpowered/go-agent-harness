package openailive

import (
	"context"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/logging"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

// Session timing defaults.
const (
	// DefaultSegmentGap is the quiet gap G that ends a speech segment or a
	// user utterance (design open question Q5).
	DefaultSegmentGap = 600 * time.Millisecond
	// DefaultDelegationSettle is the settle window D: how long a client
	// delegation waits for the user transcript that covers its offset
	// before it is reported anyway (design open question Q5).
	DefaultDelegationSettle = 400 * time.Millisecond
	// DefaultCloseTimeout bounds the session.close handshake, as the
	// official SDK examples do.
	DefaultCloseTimeout = 15 * time.Second
)

// CredentialProvider returns the auth headers for one WebSocket dial. The
// provider calls it once per dial, so a refreshed credential is used on every
// reconnect. It never builds an Authorization header itself.
type CredentialProvider func(ctx context.Context) (map[string]string, error)

// APIKeyCredentials is the API-key credential source: it returns
// "Authorization: Bearer key". A blank key returns no headers, for
// transports that need none (a replay dialer or a local fake).
func APIKeyCredentials(key string) CredentialProvider {
	key = strings.TrimSpace(key)
	return func(context.Context) (map[string]string, error) {
		if key == "" {
			return map[string]string{}, nil
		}
		return map[string]string{"Authorization": "Bearer " + key}, nil
	}
}

// Option configures a Provider.
type Option func(*Provider)

// WithCredentialProvider sets the source of the dial's auth headers.
func WithCredentialProvider(credentials CredentialProvider) Option {
	return func(p *Provider) { p.credentials = credentials }
}

// WithEndpoint overrides the primary WebSocket URL (DefaultEndpoint), for a
// fake server or an Azure Foundry resource.
func WithEndpoint(endpoint string) Option {
	return func(p *Provider) { p.endpoint = strings.TrimSpace(endpoint) }
}

// WithWebSocketDialer sets the WebSocket transport.
func WithWebSocketDialer(dialer transport.Dialer) Option {
	return func(p *Provider) { p.dialer = dialer }
}

// WithLogger sets the provider logger.
func WithLogger(logger logging.Logger) Option {
	return func(p *Provider) { p.logger = logger }
}

// WithClock sets the clock of the segment, utterance, delegation settle and
// close timers. The
// default is the host clock, which is virtual inside a testing/synctest
// bubble.
func WithClock(source clock.TimerSource) Option {
	return func(p *Provider) { p.clock = source }
}

// WithSegmentGap sets the quiet gap G that ends a speech segment or a user
// utterance. A non-positive value keeps DefaultSegmentGap.
func WithSegmentGap(gap time.Duration) Option {
	return func(p *Provider) {
		if gap > 0 {
			p.segmentGap = gap
		}
	}
}

// WithDelegationSettle sets the settle window D a client delegation waits
// for its user transcript. A non-positive value keeps
// DefaultDelegationSettle.
func WithDelegationSettle(settle time.Duration) Option {
	return func(p *Provider) {
		if settle > 0 {
			p.delegationSettle = settle
		}
	}
}

// WithCloseTimeout bounds the wait for session.closed after session.close. A
// non-positive value keeps DefaultCloseTimeout.
func WithCloseTimeout(timeout time.Duration) Option {
	return func(p *Provider) {
		if timeout > 0 {
			p.closeTimeout = timeout
		}
	}
}
