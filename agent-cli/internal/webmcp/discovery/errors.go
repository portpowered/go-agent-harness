package discovery

import (
	"errors"
	"fmt"
	"strings"
)

// Code is the stable classified discovery error vocabulary.
type Code string

const (
	CodeEndpointNotFound       Code = "endpoint_not_found"
	CodeEndpointUnreachable    Code = "endpoint_unreachable"
	CodeRemoteEndpointDenied   Code = "remote_endpoint_denied"
	CodeBrowserProtocolInvalid Code = "browser_protocol_invalid"
)

// DiscoveryError is safe for model/user display. Details are constrained to
// the C0 shape for the four endpoint-discovery classifications and never
// include raw endpoint strings or underlying network error text.
type DiscoveryError struct {
	Code      Code           `json:"code"`
	Message   string         `json:"message"`
	Retryable bool           `json:"retryable"`
	Details   map[string]any `json:"details"`
	Cause     error          `json:"-"`
}

func (e *DiscoveryError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return e.Message
}

func (e *DiscoveryError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// Is allows callers to match either a classified Code or a sentinel code
// error. Code errors are intentionally not exposed as network details.
func (e *DiscoveryError) Is(target error) bool {
	if e == nil {
		return false
	}
	var codeErr *classifiedCode
	if errors.As(target, &codeErr) {
		return e.Code == codeErr.code
	}
	return false
}

type classifiedCode struct{ code Code }

func (e *classifiedCode) Error() string { return string(e.code) }

var (
	ErrEndpointNotFound       error = &classifiedCode{code: CodeEndpointNotFound}
	ErrEndpointUnreachable    error = &classifiedCode{code: CodeEndpointUnreachable}
	ErrRemoteEndpointDenied   error = &classifiedCode{code: CodeRemoteEndpointDenied}
	ErrBrowserProtocolInvalid error = &classifiedCode{code: CodeBrowserProtocolInvalid}
)

func newEndpointNotFound(kind EndpointKind, source Source) *DiscoveryError {
	return &DiscoveryError{
		Code:      CodeEndpointNotFound,
		Message:   "browser endpoint was not found",
		Retryable: false,
		Details: map[string]any{
			"endpoint_kind": string(kind),
			"source":        boundedLabel(string(source), 64),
		},
	}
}

func newEndpointUnreachable(kind EndpointKind, addressClass, phase string, cause error) *DiscoveryError {
	return &DiscoveryError{
		Code:      CodeEndpointUnreachable,
		Message:   "browser endpoint could not be reached",
		Retryable: true,
		Cause:     cause,
		Details: map[string]any{
			"endpoint_kind": string(kind),
			"address_class": addressClass,
			"phase":         boundedLabel(phase, 32),
		},
	}
}

func newRemoteEndpointDenied(kind EndpointKind) *DiscoveryError {
	return &DiscoveryError{
		Code:      CodeRemoteEndpointDenied,
		Message:   "remote browser endpoint is not permitted",
		Retryable: false,
		Details: map[string]any{
			"endpoint_kind": string(kind),
			"network_class": "non_loopback",
			"required_flag": "browser-allow-remote-cdp",
		},
	}
}

func newProtocolInvalid(protocol, reason string, cause error) *DiscoveryError {
	protocol = safeProtocolDetail(protocol)
	if protocol == "" {
		protocol = "unknown"
	}
	return &DiscoveryError{
		Code:      CodeBrowserProtocolInvalid,
		Message:   "browser endpoint returned invalid protocol metadata",
		Retryable: false,
		Cause:     cause,
		Details: map[string]any{
			"phase":       "version",
			"protocol":    protocol,
			"reason_code": boundedLabel(reason, 64),
		},
	}
}

func safeProtocolDetail(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if value == "unknown" || protocolVersionPattern.MatchString(value) || strings.HasPrefix(value, "http_") {
		return boundedLabel(value, 32)
	}
	return "invalid"
}

func newProtocolInvalidAt(phase, protocol, reason string, cause error) *DiscoveryError {
	err := newProtocolInvalid(protocol, reason, cause)
	err.Details["phase"] = boundedLabel(phase, 32)
	return err
}

func classifiedFrom(err error, kind EndpointKind, source Source) *DiscoveryError {
	if err == nil {
		return newEndpointNotFound(kind, source)
	}
	var discoveryErr *DiscoveryError
	if errors.As(err, &discoveryErr) {
		return discoveryErr
	}
	return newEndpointUnreachable(kind, addressClassFromEndpointKind(kind), "resolve", err)
}

func codeSentinel(code Code) error {
	return &classifiedCode{code: code}
}

func (e *DiscoveryError) String() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}
