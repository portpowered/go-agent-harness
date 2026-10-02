package codexrtc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/quicksilver"
)

// Endpoints and limits of the route.
const (
	// DefaultBackendURL is the ChatGPT Codex backend base URL.
	DefaultBackendURL = "https://chatgpt.com/backend-api/codex"
	// CallPath and CallQuery complete the call-creation URL under the
	// backend base URL.
	CallPath  = "/realtime/calls"
	CallQuery = "intent=quicksilver&architecture=avas"
	// DefaultSidebandBaseURL is the sideband base; the call id is appended
	// as one path segment. The sideband stays on api.openai.com whatever the
	// backend URL is.
	DefaultSidebandBaseURL = "wss://api.openai.com/v1/live"
	// HeaderOpenAISessionID carries the call id when Location does not.
	HeaderOpenAISessionID = "Openai-Session-Id"

	// MaxAnswerBytes bounds the SDP answer.
	MaxAnswerBytes = 256 << 10
	// maxErrorBodyBytes bounds how much of a failed response is drained.
	maxErrorBodyBytes = 16 << 10
	// maxLocationBytes bounds the Location header.
	maxLocationBytes = 512
)

// Errors of call creation and the sideband.
var (
	// ErrCallRejected reports a non-success call-creation response; the
	// error is a *StatusError.
	ErrCallRejected = errors.New("codexrtc: call creation rejected")
	// ErrBadCallResponse reports a success response without a usable SDP
	// answer or call id.
	ErrBadCallResponse = errors.New("codexrtc: malformed call-creation response")
	// ErrCallEnded reports a sideband handshake answered 404 or 410: the
	// call no longer exists, so retrying cannot help.
	ErrCallEnded = errors.New("codexrtc: call has ended")
	// ErrUnauthorized reports an HTTP 401 on either request.
	ErrUnauthorized = errors.New("codexrtc: credential rejected")
)

// StatusError is a non-success HTTP status. The provider's response body is
// drained but never kept: it can echo identifiers or transcript text.
type StatusError struct {
	// Op is "call creation" or "sideband".
	Op         string
	StatusCode int
}

const (
	opCall     = "call creation"
	opSideband = "sideband"
)

func (e *StatusError) Error() string {
	return fmt.Sprintf("codexrtc: %s failed with HTTP %d", e.Op, e.StatusCode)
}

// Is matches ErrUnauthorized for 401, ErrCallRejected for any call-creation
// status, and ErrCallEnded for a sideband 404 or 410.
func (e *StatusError) Is(target error) bool {
	switch {
	case errors.Is(target, ErrUnauthorized):
		return e.StatusCode == http.StatusUnauthorized
	case errors.Is(target, ErrCallRejected):
		return e.Op == opCall
	case errors.Is(target, ErrCallEnded):
		return e.Op == opSideband && (e.StatusCode == http.StatusNotFound || e.StatusCode == http.StatusGone)
	}
	return false
}

// CallConfig configures a CallClient. Only Credential is required.
type CallConfig struct {
	// Credential supplies the ChatGPT login before every request.
	Credential CredentialSource
	// BackendURL defaults to DefaultBackendURL.
	BackendURL string
	// SidebandBaseURL defaults to DefaultSidebandBaseURL.
	SidebandBaseURL string
	// HTTPClient defaults to http.DefaultClient.
	HTTPClient *http.Client
	// Dialer opens the sideband; it defaults to websocket.DefaultDialer.
	Dialer *websocket.Dialer
	// Originator defaults to DefaultOriginator.
	Originator string
	// Version is sent as the version header when set.
	Version string
	// SidebandWriteTimeout bounds every sideband write; it defaults to
	// DefaultSidebandWriteTimeout.
	SidebandWriteTimeout time.Duration
}

// CallClient creates calls and dials their sidebands.
type CallClient struct {
	credential   CredentialSource
	callURL      string
	sidebandBase string
	http         *http.Client
	dialer       *websocket.Dialer
	originator   string
	version      string
	writeTimeout time.Duration
}

// NewCallClient validates config and applies its defaults.
func NewCallClient(config CallConfig) (*CallClient, error) {
	if config.Credential == nil {
		return nil, fmt.Errorf("%w: CallConfig.Credential is nil", ErrNoCredential)
	}
	backend, err := parseBaseURL(config.BackendURL, DefaultBackendURL, "https", "http")
	if err != nil {
		return nil, fmt.Errorf("codexrtc: backend URL: %w", err)
	}
	sideband, err := parseBaseURL(config.SidebandBaseURL, DefaultSidebandBaseURL, "wss", "ws")
	if err != nil {
		return nil, fmt.Errorf("codexrtc: sideband URL: %w", err)
	}
	client := &CallClient{
		credential:   config.Credential,
		callURL:      backend + CallPath + "?" + CallQuery,
		sidebandBase: sideband,
		http:         config.HTTPClient,
		dialer:       config.Dialer,
		originator:   defaultString(config.Originator, DefaultOriginator),
		version:      config.Version,
		writeTimeout: config.SidebandWriteTimeout,
	}
	if client.writeTimeout <= 0 {
		client.writeTimeout = DefaultSidebandWriteTimeout
	}
	if client.http == nil {
		client.http = http.DefaultClient
	}
	if client.dialer == nil {
		client.dialer = websocket.DefaultDialer
	}
	return client, nil
}

// CallRequest is one call to create.
type CallRequest struct {
	// OfferSDP is the local offer (Peer.CreateOffer).
	OfferSDP string
	// Session is the session to start (quicksilver.BuildSession). It must
	// name the model.
	Session quicksilver.SessionConfig
	// IDs are generated when zero.
	IDs RequestIDs
}

// Call is a created call.
type Call struct {
	// ID is the call id, for example rtc_...
	ID string
	// AnswerSDP is the remote answer for Peer.ApplyAnswer.
	AnswerSDP string
	// SidebandURL is the control WebSocket of this call.
	SidebandURL string
	// IDs are the session identifiers the call was created with; the
	// sideband sends the same ones.
	IDs RequestIDs
}

type callBody struct {
	SDP     string                    `json:"sdp"`
	Session quicksilver.SessionConfig `json:"session"`
}

// Create posts the offer and session and returns the call.
func (c *CallClient) Create(ctx context.Context, request CallRequest) (Call, error) {
	if strings.TrimSpace(request.OfferSDP) == "" {
		return Call{}, fmt.Errorf("%w: the offer SDP is empty", quicksilver.ErrInvalidSessionConfig)
	}
	if strings.TrimSpace(request.Session.Model) == "" {
		return Call{}, fmt.Errorf("%w: the session names no model", quicksilver.ErrInvalidSessionConfig)
	}
	ids := request.IDs
	if ids == (RequestIDs{}) {
		generated, err := NewRequestIDs(nil)
		if err != nil {
			return Call{}, err
		}
		ids = generated
	}
	header, err := c.identityHeaders(ctx, ids)
	if err != nil {
		return Call{}, err
	}
	body, err := json.Marshal(callBody{SDP: request.OfferSDP, Session: request.Session})
	if err != nil {
		return Call{}, fmt.Errorf("codexrtc: encode call: %w", err)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, c.callURL, bytes.NewReader(body))
	if err != nil {
		return Call{}, fmt.Errorf("codexrtc: build call request: %w", err)
	}
	httpRequest.Header = header
	httpRequest.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(httpRequest)
	if err != nil {
		return Call{}, fmt.Errorf("codexrtc: create call: %w", err)
	}
	call, err := parseCallResponse(response, c.sidebandBase, ids)
	if closeErr := response.Body.Close(); closeErr != nil && err == nil {
		return Call{}, fmt.Errorf("codexrtc: close call response: %w", closeErr)
	}
	return call, err
}

func parseCallResponse(response *http.Response, sidebandBase string, ids RequestIDs) (Call, error) {
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		rejected := &StatusError{Op: opCall, StatusCode: response.StatusCode}
		if _, err := io.Copy(io.Discard, io.LimitReader(response.Body, maxErrorBodyBytes)); err != nil {
			return Call{}, errors.Join(rejected, fmt.Errorf("codexrtc: drain rejected call: %w", err))
		}
		return Call{}, rejected
	}
	answer, err := readAnswer(response.Body)
	if err != nil {
		return Call{}, err
	}
	callID, err := callIDFromResponse(response.Header)
	if err != nil {
		return Call{}, err
	}
	return Call{ID: callID, AnswerSDP: answer, SidebandURL: sidebandBase + "/" + callID, IDs: ids}, nil
}

func readAnswer(body io.Reader) (string, error) {
	answer, err := io.ReadAll(io.LimitReader(body, MaxAnswerBytes+1))
	if err != nil {
		return "", fmt.Errorf("%w: read SDP answer: %w", ErrBadCallResponse, err)
	}
	if len(answer) > MaxAnswerBytes {
		return "", fmt.Errorf("%w: SDP answer exceeds %d bytes", ErrBadCallResponse, MaxAnswerBytes)
	}
	if strings.TrimSpace(string(answer)) == "" {
		return "", fmt.Errorf("%w: empty SDP answer", ErrBadCallResponse)
	}
	return string(answer), nil
}

// callIDPattern bounds a call id to one safe path segment.
var callIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// uuidPattern matches a canonical UUID call id.
var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// IsCallID reports whether segment is a call id: rtc_ followed by at least one
// character, or a UUID, and safe as one URL path segment.
func IsCallID(segment string) bool {
	if !callIDPattern.MatchString(segment) {
		return false
	}
	return (strings.HasPrefix(segment, "rtc_") && len(segment) > len("rtc_")) || uuidPattern.MatchString(segment)
}

// callIDFromResponse takes the last call-id path segment of Location (any
// query is ignored), falling back to the openai-session-id header when
// Location is absent, unparsable or holds no call id.
func callIDFromResponse(header http.Header) (string, error) {
	if location := header.Get("Location"); location != "" && len(location) <= maxLocationBytes {
		if parsed, err := url.Parse(location); err == nil {
			segments := strings.Split(parsed.Path, "/")
			for i := len(segments) - 1; i >= 0; i-- {
				if IsCallID(segments[i]) {
					return segments[i], nil
				}
			}
		}
	}
	if sessionID := strings.TrimSpace(header.Get(HeaderOpenAISessionID)); IsCallID(sessionID) {
		return sessionID, nil
	}
	return "", fmt.Errorf("%w: neither Location nor openai-session-id holds a call id", ErrBadCallResponse)
}

// parseBaseURL applies fallback to an empty value and requires an absolute
// URL with one of schemes, no query and no fragment. The result has no
// trailing slash.
func parseBaseURL(value, fallback string, schemes ...string) (string, error) {
	parsed, err := url.Parse(strings.TrimRight(defaultString(value, fallback), "/"))
	if err != nil {
		return "", err
	}
	if !slices.Contains(schemes, parsed.Scheme) || parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("%q is not an absolute %s URL without a query", parsed.Redacted(), strings.Join(schemes, " or "))
	}
	return parsed.String(), nil
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
