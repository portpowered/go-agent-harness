package providers

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

var (
	// ErrProviderRejected marks a request rejected by the upstream provider.
	ErrProviderRejected = errors.New("provider rejected request")
	// ErrAuthentication marks credential or authorization failures.
	ErrAuthentication = errors.New("authentication failed")
	// ErrRateLimited marks provider throttling.
	ErrRateLimited = errors.New("rate limited")
	// ErrInvalidRequest marks locally invalid or provider-rejected request shape.
	ErrInvalidRequest = errors.New("invalid request")
	// ErrUnsupportedRequest marks a requested provider, model, feature, or mode
	// that the gateway surface does not support.
	ErrUnsupportedRequest = errors.New("unsupported request")
	// ErrTransport marks failures before a provider response was received.
	ErrTransport = errors.New("transport failure")
	// ErrCancellation marks caller-initiated cancellation.
	ErrCancellation = errors.New("cancellation")
	// ErrReplayMismatch marks deterministic replay divergence.
	ErrReplayMismatch = errors.New("replay mismatch")
	// ErrReplayIncomplete marks deterministic replay that ended before all
	// required capture or fixture events were consumed.
	ErrReplayIncomplete = errors.New("replay incomplete")
	// ErrPartialOutput marks an interrupted stream or event sequence that
	// delivered caller-visible output before terminal failure or cancellation.
	ErrPartialOutput = errors.New("partial output")
)

const (
	ErrorClassProviderRejected   = "provider_rejected"
	ErrorClassAuthentication     = "authentication"
	ErrorClassRateLimited        = "rate_limited"
	ErrorClassInvalidRequest     = "invalid_request"
	ErrorClassUnsupportedRequest = "unsupported_request"
	ErrorClassTransport          = "transport"
	ErrorClassCancellation       = "cancellation"
	ErrorClassReplayMismatch     = "replay_mismatch"
	ErrorClassReplayIncomplete   = "replay_incomplete"
	ErrorClassPartialOutput      = "partial_output"
	ErrorClassUnknown            = "unknown"
)

// ProviderError carries provider rejection details while preserving errors.Is
// classification through the wrapped taxonomy errors.
type ProviderError struct {
	Provider   string
	StatusCode int
	Detail     string
	Err        error
}

func (e *ProviderError) Error() string {
	provider := e.Provider
	if provider == "" {
		provider = "provider"
	}
	if e.StatusCode != 0 {
		if e.Detail != "" {
			return fmt.Sprintf("%s: api error %d: %s", provider, e.StatusCode, e.Detail)
		}
		return fmt.Sprintf("%s: api error %d", provider, e.StatusCode)
	}
	if e.Detail != "" {
		return fmt.Sprintf("%s: %s", provider, e.Detail)
	}
	return provider + ": provider error"
}

func (e *ProviderError) Unwrap() error {
	return e.Err
}

// ValidationError describes a local request validation failure with
// machine-readable fields for caller policy.
type ValidationError struct {
	Provider  string
	Feature   string
	Requested string
	Supported []string
	Detail    string
	Err       error
}

func (e *ValidationError) Error() string {
	if e.Detail != "" {
		return e.Detail
	}
	var b strings.Builder
	if e.Provider != "" {
		b.WriteString(e.Provider)
		b.WriteString(": ")
	}
	if e.Feature != "" && e.Requested != "" {
		fmt.Fprintf(&b, "%s %q is not supported", e.Feature, e.Requested)
	} else if e.Feature != "" {
		fmt.Fprintf(&b, "%s is invalid", e.Feature)
	} else {
		b.WriteString("invalid request")
	}
	if len(e.Supported) > 0 {
		b.WriteString(" (supported: ")
		b.WriteString(strings.Join(e.Supported, ", "))
		b.WriteString(")")
	}
	return b.String()
}

func (e *ValidationError) Unwrap() error {
	return e.Err
}

// NewProviderHTTPError classifies a non-2xx provider HTTP response.
func NewProviderHTTPError(provider string, statusCode int, detail string) *ProviderError {
	return &ProviderError{
		Provider:   provider,
		StatusCode: statusCode,
		Detail:     detail,
		Err:        errors.Join(ErrProviderRejected, classifyHTTPStatus(statusCode)),
	}
}

// NewInvalidRequestError classifies a local request validation failure.
func NewInvalidRequestError(provider, feature, detail string) *ValidationError {
	return &ValidationError{
		Provider: provider,
		Feature:  feature,
		Detail:   detail,
		Err:      ErrInvalidRequest,
	}
}

// NewUnsupportedRequestError classifies unsupported local provider behavior.
func NewUnsupportedRequestError(provider, feature, requested string, supported []string, detail string) *ValidationError {
	return &ValidationError{
		Provider:  provider,
		Feature:   feature,
		Requested: requested,
		Supported: supported,
		Detail:    detail,
		Err:       ErrUnsupportedRequest,
	}
}

// ErrorClassification maps public provider taxonomy errors to the structured
// stream/event classification strings exposed to consumers.
func ErrorClassification(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.Canceled), errors.Is(err, ErrCancellation):
		return ErrorClassCancellation
	case errors.Is(err, ErrReplayIncomplete):
		return ErrorClassReplayIncomplete
	case errors.Is(err, ErrReplayMismatch):
		return ErrorClassReplayMismatch
	case errors.Is(err, ErrPartialOutput):
		return ErrorClassPartialOutput
	case errors.Is(err, ErrAuthentication):
		return ErrorClassAuthentication
	case errors.Is(err, ErrRateLimited):
		return ErrorClassRateLimited
	case errors.Is(err, ErrInvalidRequest):
		return ErrorClassInvalidRequest
	case errors.Is(err, ErrUnsupportedRequest):
		return ErrorClassUnsupportedRequest
	case errors.Is(err, ErrTransport):
		return ErrorClassTransport
	case errors.Is(err, ErrProviderRejected):
		return ErrorClassProviderRejected
	default:
		return ErrorClassUnknown
	}
}

// IsRetryable reports whether err represents a transient provider failure
// that callers may retry. Caller deadlines are retryable at the gateway
// boundary, while caller cancellation and all non-transient taxonomy classes
// are not.
func IsRetryable(err error) bool {
	classification := ErrorClassification(err)
	if classification == ErrorClassCancellation {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	switch classification {
	case ErrorClassRateLimited, ErrorClassTransport:
		return true
	default:
		return false
	}
}

// SessionErrorClassification refines the public classification for a provider
// session error event from its wire error type and code. Well-known
// authentication and rate-limit identifiers map to their taxonomy classes;
// everything else remains a generic provider rejection.
func SessionErrorClassification(errorType, code string) string {
	text := strings.ToLower(errorType + " " + code)
	switch {
	case containsAuthIdentifier(text):
		return ErrorClassAuthentication
	case strings.Contains(text, "rate_limit"):
		return ErrorClassRateLimited
	default:
		return ErrorClassProviderRejected
	}
}

func containsAuthIdentifier(text string) bool {
	for _, identifier := range []string{
		"invalid_api_key",
		"api_key",
		"authentication",
		"unauthorized",
		"forbidden",
		"permission_denied",
	} {
		if strings.Contains(text, identifier) {
			return true
		}
	}
	return false
}

// NewStreamErrorValue preserves readable stream error text while exposing the
// public gateway taxonomy classification carried by typed provider errors.
func NewStreamErrorValue(err error) *messages.ErrorValue {
	if err == nil {
		return messages.NewErrorValueWithTerminal("", "", messages.TerminalReasonTerminalFailure, messages.TerminalProvenanceProvider, messages.TerminalOutputNone)
	}
	value := messages.NewErrorValueWithTerminal(
		err.Error(),
		ErrorClassification(err),
		messages.TerminalReasonTerminalFailure,
		messages.TerminalProvenanceProvider,
		messages.TerminalOutputNone,
	)
	value.Err = err
	return value
}

// NewStreamTransportErrorValue classifies untyped stream reader/runtime
// failures as transport while preserving any more specific typed class.
func NewStreamTransportErrorValue(err error) *messages.ErrorValue {
	classification := ErrorClassification(err)
	if classification == ErrorClassUnknown {
		classification = ErrorClassTransport
	}
	if err == nil {
		return messages.NewErrorValueWithTerminal("", classification, messages.TerminalReasonTerminalFailure, messages.TerminalProvenanceProvider, messages.TerminalOutputNone)
	}
	value := messages.NewErrorValueWithTerminal(
		err.Error(),
		classification,
		messages.TerminalReasonTerminalFailure,
		messages.TerminalProvenanceProvider,
		messages.TerminalOutputNone,
	)
	value.Err = err
	return value
}

func classifyHTTPStatus(statusCode int) error {
	switch statusCode {
	case 400, 422:
		return ErrInvalidRequest
	case 401, 403:
		return ErrAuthentication
	case 408, 409, 425, 429:
		return ErrRateLimited
	default:
		if statusCode >= 500 {
			return ErrTransport
		}
		return ErrProviderRejected
	}
}
