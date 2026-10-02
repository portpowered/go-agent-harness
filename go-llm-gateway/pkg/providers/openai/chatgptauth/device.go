package chatgptauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	// DeviceCodeLifetime is how long a device code stays valid.
	DeviceCodeLifetime   = 15 * time.Minute
	defaultDevicePolling = 5 * time.Second
	minDevicePolling     = time.Second
)

// ErrDeviceCodeUnavailable reports that the issuer does not offer device
// code login (it answered the user-code request with 404).
var ErrDeviceCodeUnavailable = errors.New("device code login is not enabled for this issuer; use the browser login")

// Sleeper waits d or until ctx ends. Tests inject a virtual clock.
type Sleeper func(ctx context.Context, d time.Duration) error

// DeviceCode is a pending device-code login.
type DeviceCode struct {
	// VerificationURL is the page where the user enters UserCode.
	VerificationURL string
	UserCode        string
	// Interval is the issuer's polling interval.
	Interval time.Duration
	// Deadline is when the code expires on the client clock.
	Deadline time.Time

	deviceAuthID string
}

type deviceUserCodeResponse struct {
	DeviceAuthID string          `json:"device_auth_id"`
	UserCode     string          `json:"user_code"`
	UserCodeAlt  string          `json:"usercode"`
	Interval     json.RawMessage `json:"interval"`
}

type deviceTokenResponse struct {
	AuthorizationCode string `json:"authorization_code"`
	CodeVerifier      string `json:"code_verifier"`
}

// RequestDeviceCode starts a device-code login.
func (c *Client) RequestDeviceCode(ctx context.Context) (DeviceCode, error) {
	status, payload, err := c.postJSON(ctx, deviceUserCodePath, map[string]string{"client_id": c.cfg.ClientID})
	if err != nil {
		return DeviceCode{}, fmt.Errorf("request device code: %w", err)
	}
	if status == http.StatusNotFound {
		return DeviceCode{}, ErrDeviceCodeUnavailable
	}
	if status != http.StatusOK {
		return DeviceCode{}, newTokenError("device code", status, payload)
	}
	var resp deviceUserCodeResponse
	if err := json.Unmarshal(payload, &resp); err != nil {
		return DeviceCode{}, fmt.Errorf("decode device code response: %w", err)
	}
	userCode := firstNonEmpty(resp.UserCode, resp.UserCodeAlt)
	if resp.DeviceAuthID == "" || userCode == "" {
		return DeviceCode{}, errors.New("device code response is missing device_auth_id or user_code")
	}
	return DeviceCode{
		VerificationURL: c.cfg.Issuer + deviceVerifyPath,
		UserCode:        userCode,
		Interval:        parseInterval(resp.Interval),
		Deadline:        c.cfg.Now().Add(DeviceCodeLifetime),
		deviceAuthID:    resp.DeviceAuthID,
	}, nil
}

// CompleteDeviceCode polls until the user approves code, then exchanges the
// issued authorization code for a credential. A 403 or 404 poll answer means
// "still pending"; any other failure ends the login.
func (c *Client) CompleteDeviceCode(ctx context.Context, code DeviceCode, sleep Sleeper) (Credential, error) {
	if sleep == nil {
		sleep = WaitContext
	}
	body := map[string]string{"device_auth_id": code.deviceAuthID, "user_code": code.UserCode}
	for {
		status, payload, err := c.postJSON(ctx, deviceTokenPath, body)
		if err != nil {
			return Credential{}, fmt.Errorf("poll device code: %w", err)
		}
		switch status {
		case http.StatusOK:
			return c.exchangeDeviceAuthorization(ctx, payload)
		case http.StatusForbidden, http.StatusNotFound:
		default:
			return Credential{}, newTokenError("device authorization", status, payload)
		}
		remaining := code.Deadline.Sub(c.cfg.Now())
		if remaining <= 0 {
			return Credential{}, fmt.Errorf("device code expired after %s without approval", DeviceCodeLifetime)
		}
		if err := sleep(ctx, min(code.Interval, remaining)); err != nil {
			return Credential{}, err
		}
	}
}

func (c *Client) exchangeDeviceAuthorization(ctx context.Context, payload []byte) (Credential, error) {
	var resp deviceTokenResponse
	if err := json.Unmarshal(payload, &resp); err != nil {
		return Credential{}, fmt.Errorf("decode device authorization: %w", err)
	}
	if resp.AuthorizationCode == "" || resp.CodeVerifier == "" {
		return Credential{}, errors.New("device authorization is missing the exchange code")
	}
	return c.ExchangeCode(ctx, resp.AuthorizationCode, resp.CodeVerifier, c.cfg.Issuer+deviceCallbackPath)
}

// parseInterval reads the issuer's polling interval, which Codex receives as
// a string of seconds; a number is accepted too.
func parseInterval(raw json.RawMessage) time.Duration {
	text := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	seconds, err := strconv.ParseFloat(text, 64)
	if err != nil || seconds <= 0 {
		return defaultDevicePolling
	}
	return max(time.Duration(seconds*float64(time.Second)), minDevicePolling)
}

// WaitContext is the production Sleeper: it waits d or until ctx ends.
func WaitContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
