package webmcp

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// ErrorCode is the stable model-facing broker error vocabulary. These errors
// are returned as ordinary correlated tool results and do not normally end a
// session.
type ErrorCode string

const (
	ErrorCodeWebMCPDisabled         ErrorCode = "webmcp_disabled"
	ErrorCodeEndpointNotFound       ErrorCode = "endpoint_not_found"
	ErrorCodeEndpointUnreachable    ErrorCode = "endpoint_unreachable"
	ErrorCodeRemoteEndpointDenied   ErrorCode = "remote_endpoint_denied"
	ErrorCodeBrowserProtocolInvalid ErrorCode = "browser_protocol_invalid"
	ErrorCodeUnsupportedWebMCP      ErrorCode = "unsupported_webmcp"
	ErrorCodeNoEligibleTab          ErrorCode = "no_eligible_tab"
	ErrorCodeAmbiguousBrowser       ErrorCode = "ambiguous_browser"
	ErrorCodeAmbiguousTab           ErrorCode = "ambiguous_tab"
	ErrorCodeStaleSelection         ErrorCode = "stale_selection"
	ErrorCodeStaleToolRef           ErrorCode = "stale_tool_ref"
	ErrorCodeOriginDenied           ErrorCode = "origin_denied"
	ErrorCodeApprovalRequired       ErrorCode = "approval_required"
	ErrorCodeApprovalDenied         ErrorCode = "approval_denied"
	ErrorCodeInvalidToolInput       ErrorCode = "invalid_tool_input"
	ErrorCodeResultTooLarge         ErrorCode = "result_too_large"
	ErrorCodeTargetAttachFailed     ErrorCode = "target_attach_failed"
	ErrorCodeTargetDetached         ErrorCode = "target_detached"
	ErrorCodePageNavigated          ErrorCode = "page_navigated"
	ErrorCodeInvocationFailed       ErrorCode = "invocation_failed"
	ErrorCodeInvocationCanceled     ErrorCode = "invocation_canceled"
	ErrorCodeInvocationTimedOut     ErrorCode = "invocation_timed_out"
	ErrorCodeInvocationOrphaned     ErrorCode = "invocation_orphaned"
	ErrorCodeBrowserDisconnected    ErrorCode = "browser_disconnected"
)

// IsKnown reports whether code is part of the frozen C0 vocabulary.
func (c ErrorCode) IsKnown() bool {
	switch c {
	case ErrorCodeWebMCPDisabled,
		ErrorCodeEndpointNotFound,
		ErrorCodeEndpointUnreachable,
		ErrorCodeRemoteEndpointDenied,
		ErrorCodeBrowserProtocolInvalid,
		ErrorCodeUnsupportedWebMCP,
		ErrorCodeNoEligibleTab,
		ErrorCodeAmbiguousBrowser,
		ErrorCodeAmbiguousTab,
		ErrorCodeStaleSelection,
		ErrorCodeStaleToolRef,
		ErrorCodeOriginDenied,
		ErrorCodeApprovalRequired,
		ErrorCodeApprovalDenied,
		ErrorCodeInvalidToolInput,
		ErrorCodeResultTooLarge,
		ErrorCodeTargetAttachFailed,
		ErrorCodeTargetDetached,
		ErrorCodePageNavigated,
		ErrorCodeInvocationFailed,
		ErrorCodeInvocationCanceled,
		ErrorCodeInvocationTimedOut,
		ErrorCodeInvocationOrphaned,
		ErrorCodeBrowserDisconnected:
		return true
	default:
		return false
	}
}

// BrokerError is the classified error object carried by a failed textual
// result envelope. Details are redacted, JSON object-shaped data.
type BrokerError struct {
	Code      ErrorCode       `json:"code"`
	Message   string          `json:"message"`
	Retryable bool            `json:"retryable"`
	Details   json.RawMessage `json:"details"`
}

// NewBrokerError builds a classified error with an empty object when no safe
// details are needed. Unknown codes are rejected when the error is encoded.
func NewBrokerError(code ErrorCode, message string, retryable bool, details json.RawMessage) BrokerError {
	if len(bytes.TrimSpace(details)) == 0 {
		details = json.RawMessage(`{}`)
	}
	return BrokerError{Code: code, Message: message, Retryable: retryable, Details: append(json.RawMessage(nil), details...)}
}

// Error implements error for internal broker boundaries while preserving the
// stable code and human-readable message.
func (e BrokerError) Error() string {
	if e.Message == "" {
		return string(e.Code)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e BrokerError) validate() error {
	if !e.Code.IsKnown() {
		return fmt.Errorf("unknown WebMCP error code %q", e.Code)
	}
	if e.Message == "" {
		return fmt.Errorf("WebMCP error %q has an empty message", e.Code)
	}
	details := bytes.TrimSpace(e.Details)
	if len(details) == 0 || !json.Valid(details) || details[0] != '{' || details[len(details)-1] != '}' {
		return fmt.Errorf("WebMCP error %q details must be a JSON object", e.Code)
	}
	return nil
}

// MarshalJSON keeps the failed envelope's error object strict and ensures an
// error never silently introduces an unrecognized model-facing code.
func (e BrokerError) MarshalJSON() ([]byte, error) {
	if err := e.validate(); err != nil {
		return nil, err
	}
	type wireBrokerError struct {
		Code      ErrorCode       `json:"code"`
		Message   string          `json:"message"`
		Retryable bool            `json:"retryable"`
		Details   json.RawMessage `json:"details"`
	}
	return json.Marshal(wireBrokerError{
		Code: e.Code, Message: e.Message, Retryable: e.Retryable,
		Details: append(json.RawMessage(nil), e.Details...),
	})
}

// UnmarshalJSON rejects error-code drift and unknown error properties at the
// same boundary that rejects unknown envelope versions.
func (e *BrokerError) UnmarshalJSON(data []byte) error {
	if e == nil {
		return fmt.Errorf("cannot decode WebMCP error into nil receiver")
	}
	type wireBrokerError struct {
		Code      *ErrorCode      `json:"code"`
		Message   *string         `json:"message"`
		Retryable *bool           `json:"retryable"`
		Details   json.RawMessage `json:"details"`
	}
	var wire wireBrokerError
	if err := decodeStrictJSON(data, &wire); err != nil {
		return err
	}
	if wire.Code == nil || wire.Message == nil || wire.Retryable == nil || len(bytes.TrimSpace(wire.Details)) == 0 {
		return fmt.Errorf("invalid WebMCP error: code, message, retryable, and details are required")
	}
	value := BrokerError{
		Code: *wire.Code, Message: *wire.Message, Retryable: *wire.Retryable,
		Details: append(json.RawMessage(nil), wire.Details...),
	}
	if err := value.validate(); err != nil {
		return err
	}
	*e = value
	return nil
}

// Stable aliases make the envelope terminology explicit at call sites while
// retaining one error representation.
type ClassifiedError = BrokerError
type ToolResultError = BrokerError
