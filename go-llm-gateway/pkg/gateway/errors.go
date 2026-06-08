package gateway

import (
	"errors"
	"fmt"
	"net/http"
)

// ErrAuthentication identifies failures where provider credentials are
// missing, invalid, expired, or otherwise rejected by the provider.
var ErrAuthentication = errors.New("gateway authentication failure")

// ErrAuthorization identifies failures where credentials are valid but do not
// permit the requested provider, model, account, region, or operation.
var ErrAuthorization = errors.New("gateway authorization failure")

// ErrRateLimit identifies provider or gateway throttling failures. Callers may
// use this class to choose retry or backoff behavior.
var ErrRateLimit = errors.New("gateway rate limit failure")

// ErrInvalidRequest identifies malformed, unsupported, or provider-rejected
// request input that the caller should fix before retrying.
var ErrInvalidRequest = errors.New("gateway invalid request failure")

// ErrUnsupportedModel identifies failures where the selected model is unknown
// or unsupported for the provider or requested capability.
var ErrUnsupportedModel = errors.New("gateway unsupported model failure")

// ErrProviderHTTPStatus identifies provider responses with non-success HTTP
// statuses. Use errors.As with ProviderHTTPStatusError for status details.
var ErrProviderHTTPStatus = errors.New("gateway provider http status failure")

// ErrTransport identifies failures while creating, sending, receiving, or
// decoding provider transport data before a provider response is available.
var ErrTransport = errors.New("gateway transport failure")

// ErrReplayMismatch identifies deterministic replay requests that did not
// match any recorded capture or fixture event.
var ErrReplayMismatch = errors.New("gateway replay mismatch")

// ErrCancellation identifies caller cancellation or timeout failures. Callers
// can use this class to keep shutdown and timeout handling separate from
// provider, transport, and replay failures.
var ErrCancellation = errors.New("gateway cancellation")

// GatewayError wraps an underlying failure with a public gateway error class.
// Use errors.Is against one of the Err* values to branch on stable meaning.
type GatewayError struct {
	Class    error
	Provider string
	Message  string
	Err      error
}

// Error returns the operator-readable gateway error message.
func (e *GatewayError) Error() string {
	if e == nil {
		return "<nil>"
	}
	msg := e.Message
	if msg == "" && e.Class != nil {
		msg = e.Class.Error()
	}
	if e.Provider != "" {
		msg = e.Provider + ": " + msg
	}
	if e.Err != nil && msg != "" {
		return msg + ": " + e.Err.Error()
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	return msg
}

// Unwrap returns the underlying cause wrapped by the gateway classification.
func (e *GatewayError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// Is reports whether target matches this gateway error's public class.
func (e *GatewayError) Is(target error) bool {
	return e != nil && e.Class != nil && target == e.Class
}

// NewGatewayError returns an error that preserves a public gateway class while
// wrapping an underlying cause. The class should be one of the exported Err*
// values in this package.
func NewGatewayError(class error, provider, message string, err error) error {
	return &GatewayError{
		Class:    class,
		Provider: provider,
		Message:  message,
		Err:      err,
	}
}

// ProviderHTTPStatusError describes a provider HTTP response with a non-success
// status. Use errors.As to inspect StatusCode, Provider, and Body.
type ProviderHTTPStatusError struct {
	Provider   string
	StatusCode int
	Body       string
	Err        error
}

// Error returns a readable provider HTTP status error message.
func (e *ProviderHTTPStatusError) Error() string {
	if e == nil {
		return "<nil>"
	}
	provider := e.Provider
	if provider == "" {
		provider = "provider"
	}
	if e.Body != "" {
		return fmt.Sprintf("%s: api error %d: %s", provider, e.StatusCode, e.Body)
	}
	return fmt.Sprintf("%s: api error %d", provider, e.StatusCode)
}

// Unwrap returns the lower-level cause for the provider status failure.
func (e *ProviderHTTPStatusError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// Is reports the stable gateway classes implied by the HTTP status code.
func (e *ProviderHTTPStatusError) Is(target error) bool {
	if e == nil {
		return false
	}
	if target == ErrProviderHTTPStatus {
		return true
	}
	return target == HTTPStatusClass(e.StatusCode)
}

// NewProviderHTTPStatusError returns a typed error for a provider non-success
// HTTP status. The returned error matches ErrProviderHTTPStatus and, when the
// status has a known caller action, the corresponding class such as
// ErrAuthentication or ErrRateLimit.
func NewProviderHTTPStatusError(provider string, statusCode int, body string, err error) error {
	return &ProviderHTTPStatusError{
		Provider:   provider,
		StatusCode: statusCode,
		Body:       body,
		Err:        err,
	}
}

// HTTPStatusClass maps common provider HTTP status codes to gateway error
// classes. It returns nil when the status has no more specific public class.
func HTTPStatusClass(statusCode int) error {
	switch statusCode {
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return ErrInvalidRequest
	case http.StatusUnauthorized:
		return ErrAuthentication
	case http.StatusForbidden:
		return ErrAuthorization
	case http.StatusNotFound:
		return ErrUnsupportedModel
	case http.StatusTooManyRequests:
		return ErrRateLimit
	default:
		return nil
	}
}

// TransportError describes a provider transport failure before a provider HTTP
// status or structured provider error is available.
type TransportError struct {
	Provider  string
	Operation string
	Err       error
}

// Error returns a readable transport error message.
func (e *TransportError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Provider != "" && e.Operation != "" {
		return fmt.Sprintf("%s: %s transport failed: %v", e.Provider, e.Operation, e.Err)
	}
	if e.Provider != "" {
		return fmt.Sprintf("%s: transport failed: %v", e.Provider, e.Err)
	}
	if e.Operation != "" {
		return fmt.Sprintf("%s transport failed: %v", e.Operation, e.Err)
	}
	return fmt.Sprintf("transport failed: %v", e.Err)
}

// Unwrap returns the underlying transport cause.
func (e *TransportError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// Is reports whether target is ErrTransport.
func (e *TransportError) Is(target error) bool {
	return e != nil && target == ErrTransport
}

// NewTransportError returns a typed transport failure that matches
// ErrTransport and unwraps to the lower-level transport cause.
func NewTransportError(provider, operation string, err error) error {
	return &TransportError{
		Provider:  provider,
		Operation: operation,
		Err:       err,
	}
}

// ReplayMismatchError describes a deterministic replay request that could not
// be matched to a committed capture or fixture event.
type ReplayMismatchError struct {
	Expected string
	Actual   string
	Err      error
}

// Error returns a readable replay mismatch error message.
func (e *ReplayMismatchError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Expected != "" || e.Actual != "" {
		return fmt.Sprintf("replay mismatch: expected %s, actual %s", e.Expected, e.Actual)
	}
	if e.Err != nil {
		return "replay mismatch: " + e.Err.Error()
	}
	return "replay mismatch"
}

// Unwrap returns the lower-level replay mismatch cause.
func (e *ReplayMismatchError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// Is reports whether target is ErrReplayMismatch.
func (e *ReplayMismatchError) Is(target error) bool {
	return e != nil && target == ErrReplayMismatch
}

// NewReplayMismatchError returns a typed replay mismatch failure that matches
// ErrReplayMismatch.
func NewReplayMismatchError(expected, actual string, err error) error {
	return &ReplayMismatchError{
		Expected: expected,
		Actual:   actual,
		Err:      err,
	}
}

// NewCancellationError returns a gateway cancellation wrapper that matches
// ErrCancellation while preserving the underlying context cancellation or
// deadline error for callers that also check context.Canceled or
// context.DeadlineExceeded.
func NewCancellationError(message string, err error) error {
	return NewGatewayError(ErrCancellation, "", message, err)
}
