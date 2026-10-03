package codexrtc

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/uuid"
)

// Header names this route sends on call creation and on the sideband, in
// canonical form (HTTP header names are case-insensitive).
const (
	HeaderAuthorization = "Authorization"
	HeaderAccountID     = "Chatgpt-Account-Id"
	HeaderAlpha         = "Openai-Alpha"
	HeaderOriginator    = "Originator"
	HeaderVersion       = "Version"
	HeaderSessionID     = "Session-Id"
	HeaderThreadID      = "Thread-Id"
	HeaderXSessionID    = "X-Session-Id"

	// AlphaQuicksilverV2 selects the quicksilver (frameless bidi) dialect.
	AlphaQuicksilverV2 = "quicksilver=v2"
	// DefaultOriginator names this client to the backend.
	DefaultOriginator = "yui"
)

// ErrNoCredential reports a missing or incomplete ChatGPT credential.
var ErrNoCredential = errors.New("codexrtc: no ChatGPT credential")

// Credential is the ChatGPT login this route authenticates with.
type Credential struct {
	// AccessToken is sent as the bearer token.
	AccessToken string
	// AccountID is the chatgpt_account_id claim, sent as chatgpt-account-id.
	AccountID string
}

// String describes the credential with the access token and the account id
// redacted: the account id identifies the ChatGPT account, so it stays out of
// logs too.
func (c Credential) String() string {
	return fmt.Sprintf("Credential{AccessToken:%s AccountID:%s}", redacted(c.AccessToken), redacted(c.AccountID))
}

// GoString is String, so %#v redacts too.
func (c Credential) GoString() string { return "codexrtc." + c.String() }

// LogValue keeps the token and the account id out of structured logs.
func (c Credential) LogValue() slog.Value {
	return slog.GroupValue(slog.String("access_token", redacted(c.AccessToken)), slog.String("account_id", redacted(c.AccountID)))
}

// redacted is redactedMark for a set secret and "" for an empty one.
func redacted(secret string) string {
	if secret == "" {
		return ""
	}
	return redactedMark
}

// redactedMark replaces a secret in formatted output.
const redactedMark = "[REDACTED]"

// CredentialSource returns the current credential. It is called before every
// request, so an implementation can refresh the token.
type CredentialSource interface {
	Credential(ctx context.Context) (Credential, error)
}

// CredentialFunc adapts a function, for example one that wraps the ChatGPT
// token manager, to CredentialSource.
type CredentialFunc func(ctx context.Context) (Credential, error)

// Credential calls f.
func (f CredentialFunc) Credential(ctx context.Context) (Credential, error) { return f(ctx) }

// RequestIDs are the session identifiers sent as session-id, thread-id and
// x-session-id. The same values go on call creation and on the sideband.
type RequestIDs struct {
	SessionID         string
	ThreadID          string
	RealtimeSessionID string
}

// NewRequestIDs returns three distinct random UUIDs read from random, or from
// crypto/rand when random is nil.
func NewRequestIDs(random io.Reader) (RequestIDs, error) {
	if random == nil {
		random = rand.Reader
	}
	var ids [3]string
	for i := range ids {
		id, err := uuid.NewRandomFromReader(random)
		if err != nil {
			return RequestIDs{}, fmt.Errorf("codexrtc: request id: %w", err)
		}
		ids[i] = id.String()
	}
	return RequestIDs{SessionID: ids[0], ThreadID: ids[1], RealtimeSessionID: ids[2]}, nil
}

// identityHeaders builds the headers shared by call creation and the
// sideband from a fresh credential.
func (c *CallClient) identityHeaders(ctx context.Context, ids RequestIDs) (http.Header, error) {
	credential, err := c.credential.Credential(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNoCredential, err)
	}
	if strings.TrimSpace(credential.AccessToken) == "" || strings.TrimSpace(credential.AccountID) == "" {
		return nil, fmt.Errorf("%w: the access token and account id are both required", ErrNoCredential)
	}
	header := http.Header{}
	header.Set(HeaderAuthorization, "Bearer "+credential.AccessToken)
	header.Set(HeaderAccountID, credential.AccountID)
	header.Set(HeaderAlpha, AlphaQuicksilverV2)
	header.Set(HeaderOriginator, c.originator)
	if c.version != "" {
		header.Set(HeaderVersion, c.version)
	}
	header.Set(HeaderSessionID, ids.SessionID)
	header.Set(HeaderThreadID, ids.ThreadID)
	header.Set(HeaderXSessionID, ids.RealtimeSessionID)
	return header, nil
}
