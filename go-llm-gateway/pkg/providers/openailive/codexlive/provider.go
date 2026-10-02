package codexlive

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v4"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/logging"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/codexrtc"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/internal/livesession"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/quicksilver"
)

// ProviderName is the session provider name: gpt-live-1-codex is a model of
// the same "openai-live" provider as gpt-live-1.
const ProviderName = "openai-live"

// Session timing defaults, shared with the public route.
const (
	// DefaultSegmentGap is the quiet gap G that ends a speech segment or a
	// user utterance when no turn.done arrives.
	DefaultSegmentGap = 600 * time.Millisecond
	// DefaultCloseTimeout bounds the wait for the backend to close the
	// sideband after session.close.
	DefaultCloseTimeout = 15 * time.Second
	// DefaultConnectTimeout bounds the media connection after the answer is
	// applied, as OpenClaw bounds its sideband connect.
	DefaultConnectTimeout = 15 * time.Second
)

// ErrCredentialRequired reports a Provider built without a ChatGPT
// credential source.
var ErrCredentialRequired = errors.New("codexlive: a ChatGPT credential source is required")

// Transport overrides the route's network edges. The zero value is the
// production route: the ChatGPT backend, the api.openai.com sideband, the
// default HTTP client and dialer, the host network and no ICE servers.
type Transport struct {
	// BackendURL replaces codexrtc.DefaultBackendURL.
	BackendURL string
	// SidebandBaseURL replaces codexrtc.DefaultSidebandBaseURL.
	SidebandBaseURL string
	// HTTPClient creates calls.
	HTTPClient *http.Client
	// SidebandDialer opens the sideband.
	SidebandDialer *websocket.Dialer
	// PeerSettings replaces pion's network settings, for example a virtual
	// network.
	PeerSettings *webrtc.SettingEngine
	// ICEServers are STUN or TURN servers; none are used by default.
	ICEServers []webrtc.ICEServer
}

// Provider is the gpt-live-1-codex session provider.
type Provider struct {
	credentials    codexrtc.CredentialSource
	transport      Transport
	version        string
	logger         logging.Logger
	clock          clock.TimerSource
	segmentGap     time.Duration
	closeTimeout   time.Duration
	connectTimeout time.Duration
	reconnect      reconnectPolicy
}

var _ providers.SessionProvider = (*Provider)(nil)

// Option configures a Provider.
type Option func(*Provider)

// WithCredentials sets the ChatGPT credential source. It is called before
// call creation and before every sideband dial, so a refreshed token is used
// on reconnect.
func WithCredentials(source codexrtc.CredentialSource) Option {
	return func(p *Provider) { p.credentials = source }
}

// WithTransport overrides the network edges.
func WithTransport(transport Transport) Option {
	return func(p *Provider) { p.transport = transport }
}

// WithClientVersion sets the version header: the build version of the client.
func WithClientVersion(version string) Option {
	return func(p *Provider) { p.version = version }
}

// WithLogger sets the provider logger.
func WithLogger(logger logging.Logger) Option {
	return func(p *Provider) { p.logger = logger }
}

// WithClock sets the clock of the pacer, the segment, reconnect and close
// timers. The default is the host clock.
func WithClock(source clock.TimerSource) Option {
	return func(p *Provider) { p.clock = source }
}

// WithCloseTimeout bounds the close handshake. A non-positive value keeps the
// default.
func WithCloseTimeout(timeout time.Duration) Option {
	return func(p *Provider) {
		if timeout > 0 {
			p.closeTimeout = timeout
		}
	}
}

// New returns a Provider configured by options.
func New(options ...Option) *Provider {
	p := &Provider{
		logger:         logging.DummyLogger(),
		clock:          clock.Real{},
		segmentGap:     DefaultSegmentGap,
		closeTimeout:   DefaultCloseTimeout,
		connectTimeout: DefaultConnectTimeout,
		reconnect:      defaultReconnectPolicy(),
	}
	for _, option := range options {
		option(p)
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

// ConnectSession creates the call and returns the running session: it
// creates the WebRTC offer, creates the call with it, applies the answer,
// dials the sideband and waits, bounded by ctx, for the media connection.
// Any failure releases everything created so far.
func (p *Provider) ConnectSession(ctx context.Context, cfg models.SessionConfig) (messages.Session, error) {
	if p.credentials == nil {
		return nil, ErrCredentialRequired
	}
	rate, err := sessionRate(cfg)
	if err != nil {
		return nil, err
	}
	if _, err := p.credentials.Credential(ctx); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSignInAgain, err)
	}
	session, err := quicksilver.BuildSession(cfg)
	if err != nil {
		return nil, err
	}
	client, err := codexrtc.NewCallClient(codexrtc.CallConfig{
		Credential: p.credentials, BackendURL: p.transport.BackendURL, SidebandBaseURL: p.transport.SidebandBaseURL,
		HTTPClient: p.transport.HTTPClient, Dialer: p.transport.SidebandDialer, Version: p.version,
	})
	if err != nil {
		return nil, err
	}
	peer, err := codexrtc.NewPeer(codexrtc.PeerConfig{SettingEngine: p.transport.PeerSettings, ICEServers: p.transport.ICEServers})
	if err != nil {
		return nil, err
	}
	call, side, err := p.establish(ctx, client, peer, session)
	if err != nil {
		return nil, errors.Join(err, peer.Close())
	}
	dial := func(ctx context.Context) (control, error) { return client.DialSideband(ctx, call) }
	transport, err := newConn(ctx, peer, side, dial, connConfig{rate: rate, clock: p.clock, logger: p.logger, policy: p.reconnect})
	if err != nil {
		return nil, errors.Join(err, side.Close(), peer.Close())
	}
	live := livesession.New(transport, p.logger, livesession.Settings{
		Name:         "openai live codex",
		MediaName:    "OpenAI Live (ChatGPT)",
		Format:       livesession.Format{Type: livesession.AudioTypePCM, Rate: rate},
		Clock:        p.clock,
		SegmentGap:   p.segmentGap,
		CloseTimeout: p.closeTimeout,
	}, dialect{})
	live.Open(ctx, call.ID, session.Model)
	p.logger.Info("openai live codex: call connected", logging.Field{Key: "call_id", Value: call.ID})
	return live, nil
}

// establish runs offer, call creation, answer, sideband and media
// connection in that order.
func (p *Provider) establish(ctx context.Context, client *codexrtc.CallClient, peer *codexrtc.Peer, session quicksilver.SessionConfig) (codexrtc.Call, *codexrtc.Sideband, error) {
	offer, err := peer.CreateOffer(ctx)
	if err != nil {
		return codexrtc.Call{}, nil, err
	}
	call, err := client.Create(ctx, codexrtc.CallRequest{OfferSDP: offer, Session: session})
	if err != nil {
		return codexrtc.Call{}, nil, signInError(err)
	}
	if err := peer.ApplyAnswer(call.AnswerSDP); err != nil {
		return codexrtc.Call{}, nil, err
	}
	side, err := p.dialSideband(ctx, client, call)
	if err != nil {
		return codexrtc.Call{}, nil, signInError(err)
	}
	connectCtx, cancel, err := clock.WithTimeout(ctx, p.clock, p.connectTimeout)
	if err != nil {
		return codexrtc.Call{}, nil, errors.Join(err, side.Close())
	}
	defer cancel()
	if err := peer.WaitConnected(connectCtx); err != nil {
		return codexrtc.Call{}, nil, errors.Join(fmt.Errorf("codexlive: media connection: %w", err), side.Close())
	}
	return call, side, nil
}

// dialSideband attaches the first sideband with the reconnect policy's
// attempts and backoff.
func (p *Provider) dialSideband(ctx context.Context, client *codexrtc.CallClient, call codexrtc.Call) (*codexrtc.Sideband, error) {
	policy := p.reconnect
	var lastErr error
	for attempt := range policy.attempts {
		if attempt > 0 {
			if err := clock.Wait(ctx, p.clock, policy.retryDelay(attempt)); err != nil {
				return nil, errors.Join(lastErr, err)
			}
		}
		side, err := client.DialSideband(ctx, call)
		if err == nil {
			return side, nil
		}
		lastErr = err
		if permanentDialError(err) {
			break
		}
	}
	return nil, lastErr
}

// signInError marks a credential failure with the sign-in hint.
func signInError(err error) error {
	if credentialError(err) {
		return fmt.Errorf("%w: %w", ErrSignInAgain, err)
	}
	return err
}

// sessionRate is the PCM16 rate shared by input and output.
func sessionRate(cfg models.SessionConfig) (int, error) {
	for _, format := range []models.AudioFormat{cfg.InputAudioFormat, cfg.OutputAudioFormat} {
		if format != "" && format != models.AudioFormatPCM16 {
			return 0, fmt.Errorf("%w: audio format %q; the WebRTC route carries pcm16", quicksilver.ErrInvalidSessionConfig, format)
		}
	}
	input, output := cfg.InputAudioSampleRate, cfg.OutputAudioSampleRate
	if input == 0 {
		input = output
	}
	if output == 0 {
		output = input
	}
	if input == 0 {
		return int(models.SampleRate24000), nil
	}
	if input != output || !sessionRateSupported(input) {
		return 0, fmt.Errorf("%w: sample rates %d/%d; the route takes one shared rate of 16000 or 24000 Hz", quicksilver.ErrInvalidSessionConfig, input, output)
	}
	return int(input), nil
}

// sessionRateSupported reports a session rate the route resamples: the peer
// always carries 48 kHz.
func sessionRateSupported(rate models.SampleRate) bool {
	return rate == models.SampleRate24000 || rate == models.SampleRate16000
}
