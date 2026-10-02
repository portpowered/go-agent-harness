package chatgptauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	maxResponseBytes   = 1 << 20
	maxErrorDetailRune = 300
	contentTypeHeader  = "Content-Type"
	formContentType    = "application/x-www-form-urlencoded"
	jsonContentType    = "application/json"
	grantRefreshToken  = "refresh_token"
	operationExchange  = "exchange"
	operationRefresh   = "refresh"
)

// Client talks to the OAuth issuer: it builds the authorize URL, exchanges
// codes, refreshes and revokes tokens, and runs the device-code flow.
type Client struct {
	cfg Config
}

// NewClient returns a Client for cfg; empty fields take their defaults.
func NewClient(cfg Config) *Client {
	return &Client{cfg: cfg.withDefaults()}
}

// AuthorizeURL returns the browser URL that starts a PKCE login whose
// callback is redirectURI.
func (c *Client) AuthorizeURL(redirectURI string, pkce PKCE, state string) string {
	query := url.Values{}
	query.Set("response_type", "code")
	query.Set("client_id", c.cfg.ClientID)
	query.Set("redirect_uri", redirectURI)
	query.Set("scope", c.cfg.Scope)
	query.Set("code_challenge", pkce.Challenge)
	query.Set("code_challenge_method", "S256")
	query.Set("id_token_add_organizations", "true")
	query.Set("codex_cli_simplified_flow", "true")
	query.Set("state", state)
	query.Set("originator", c.cfg.Originator)
	return c.cfg.Issuer + authorizePath + "?" + query.Encode()
}

type tokenResponse struct {
	IDToken      string `json:"id_token"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

// ExchangeCode trades an authorization code and its PKCE verifier for a
// credential.
func (c *Client) ExchangeCode(ctx context.Context, code, verifier, redirectURI string) (Credential, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)
	form.Set("client_id", c.cfg.ClientID)
	form.Set("code_verifier", verifier)
	resp, err := c.requestToken(ctx, operationExchange, form)
	if err != nil {
		return Credential{}, err
	}
	if resp.AccessToken == "" || resp.RefreshToken == "" {
		return Credential{}, &TokenError{Operation: operationExchange, Message: "token response is missing access_token or refresh_token"}
	}
	return c.credentialFrom(resp, Credential{}), nil
}

// Refresh exchanges cred's refresh token for new tokens. Fields the issuer
// omits (a rotated refresh token, a new id token) keep their old values.
func (c *Client) Refresh(ctx context.Context, cred Credential) (Credential, error) {
	if cred.RefreshToken == "" {
		return Credential{}, fmt.Errorf("refresh: %w", ErrReauthRequired)
	}
	form := url.Values{}
	form.Set("grant_type", grantRefreshToken)
	form.Set("refresh_token", cred.RefreshToken)
	form.Set("client_id", c.clientIDFor(cred))
	resp, err := c.requestToken(ctx, operationRefresh, form)
	if err != nil {
		return Credential{}, err
	}
	if resp.AccessToken == "" {
		return Credential{}, &TokenError{Operation: operationRefresh, Message: "token response is missing access_token"}
	}
	return c.credentialFrom(resp, cred), nil
}

// Revoke revokes cred's refresh token (or, without one, its access token)
// at the issuer, as Codex does on logout.
func (c *Client) Revoke(ctx context.Context, cred Credential) error {
	body := map[string]string{"token": cred.RefreshToken, "token_type_hint": grantRefreshToken, "client_id": c.clientIDFor(cred)}
	if cred.RefreshToken == "" {
		body = map[string]string{"token": cred.AccessToken, "token_type_hint": "access_token"}
	}
	if body["token"] == "" {
		return nil
	}
	status, payload, err := c.postJSON(ctx, revokePath, body)
	if err != nil {
		return fmt.Errorf("revoke ChatGPT token: %w", err)
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return newTokenError("revoke", status, payload)
	}
	return nil
}

func (c *Client) clientIDFor(cred Credential) string {
	if cred.ClientID != "" {
		return cred.ClientID
	}
	return c.cfg.ClientID
}

func (c *Client) credentialFrom(resp tokenResponse, previous Credential) Credential {
	now := c.cfg.Now()
	cred := Credential{
		Issuer:       c.cfg.Issuer,
		ClientID:     c.clientIDFor(previous),
		IDToken:      firstNonEmpty(resp.IDToken, previous.IDToken),
		AccessToken:  resp.AccessToken,
		RefreshToken: firstNonEmpty(resp.RefreshToken, previous.RefreshToken),
		LastRefresh:  now.UTC(),
	}
	if resp.ExpiresIn > 0 {
		cred.ExpiresAt = now.Add(time.Duration(resp.ExpiresIn) * time.Second).UTC()
	}
	cred = withClaims(cred)
	cred.AccountID = firstNonEmpty(cred.AccountID, previous.AccountID)
	cred.Email = firstNonEmpty(cred.Email, previous.Email)
	cred.PlanType = firstNonEmpty(cred.PlanType, previous.PlanType)
	return cred
}

func (c *Client) requestToken(ctx context.Context, operation string, form url.Values) (tokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.Issuer+tokenPath, strings.NewReader(form.Encode()))
	if err != nil {
		return tokenResponse{}, fmt.Errorf("build token %s request: %w", operation, err)
	}
	req.Header.Set(contentTypeHeader, formContentType)
	status, payload, err := c.do(req)
	if err != nil {
		return tokenResponse{}, fmt.Errorf("token %s: %w", operation, err)
	}
	if status != http.StatusOK {
		return tokenResponse{}, newTokenError(operation, status, payload)
	}
	var resp tokenResponse
	if err := json.Unmarshal(payload, &resp); err != nil {
		return tokenResponse{}, &TokenError{Operation: operation, Status: status, Message: "token response is not valid JSON"}
	}
	return resp, nil
}

func (c *Client) postJSON(ctx context.Context, path string, body any) (int, []byte, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.Issuer+path, bytes.NewReader(encoded))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set(contentTypeHeader, jsonContentType)
	return c.do(req)
}

func (c *Client) do(req *http.Request) (int, []byte, error) {
	resp, err := c.cfg.HTTPClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	payload, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	closeErr := resp.Body.Close()
	if readErr != nil {
		return resp.StatusCode, nil, readErr
	}
	if closeErr != nil {
		return resp.StatusCode, nil, closeErr
	}
	return resp.StatusCode, payload, nil
}

// TokenError is a failed issuer request. Permanent errors (an expired,
// reused or revoked refresh token, invalid_grant, or 401) match
// ErrReauthRequired with errors.Is.
type TokenError struct {
	Operation string
	Status    int
	Code      string
	Message   string
	Permanent bool
}

func (e *TokenError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "ChatGPT token %s failed", e.Operation)
	if e.Status != 0 {
		fmt.Fprintf(&b, " (HTTP %d)", e.Status)
	}
	if e.Code != "" {
		fmt.Fprintf(&b, ": %s", e.Code)
	}
	if e.Message != "" {
		fmt.Fprintf(&b, ": %s", e.Message)
	}
	return b.String()
}

// Is matches ErrReauthRequired for permanent failures.
func (e *TokenError) Is(target error) bool {
	return e.Permanent && errors.Is(target, ErrReauthRequired)
}

type issuerErrorBody struct {
	Error            json.RawMessage `json:"error"`
	ErrorDescription string          `json:"error_description"`
	Code             string          `json:"code"`
	Message          string          `json:"message"`
}

type issuerNestedError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func newTokenError(operation string, status int, payload []byte) *TokenError {
	code, message := parseIssuerError(payload)
	return &TokenError{
		Operation: operation,
		Status:    status,
		Code:      code,
		Message:   message,
		Permanent: operation == operationRefresh && isPermanentRefreshFailure(status, code),
	}
}

func parseIssuerError(payload []byte) (code, message string) {
	var body issuerErrorBody
	if err := json.Unmarshal(payload, &body); err != nil {
		return "", truncateDetail(strings.TrimSpace(string(payload)))
	}
	code, message = body.Code, firstNonEmpty(body.ErrorDescription, body.Message)
	var nested issuerNestedError
	var plain string
	switch {
	case json.Unmarshal(body.Error, &plain) == nil:
		code = firstNonEmpty(plain, code)
	case json.Unmarshal(body.Error, &nested) == nil:
		code = firstNonEmpty(nested.Code, code)
		message = firstNonEmpty(nested.Message, message)
	}
	return strings.ToLower(code), truncateDetail(message)
}

func isPermanentRefreshFailure(status int, code string) bool {
	switch code {
	case "refresh_token_expired", "refresh_token_reused", "refresh_token_invalidated", "invalid_refresh_token":
		return true
	case "invalid_grant":
		return status == http.StatusBadRequest
	}
	return status == http.StatusUnauthorized
}

func truncateDetail(detail string) string {
	runes := []rune(detail)
	if len(runes) <= maxErrorDetailRune {
		return detail
	}
	return string(runes[:maxErrorDetailRune]) + "..."
}
