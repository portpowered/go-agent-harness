package webmcp

import (
	"context"
	"errors"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

const (
	maxAmbiguityCandidates = 32
	maxAmbiguityTitle      = 160
	maxAmbiguityOrigin     = 256
)

var (
	ErrClosed             = errors.New("webmcp: closed")
	ErrBrowserNotFound    = errors.New("webmcp: browser not found")
	ErrTargetNotFound     = errors.New("webmcp: target not found")
	ErrInvocationNotFound = errors.New("webmcp: invocation not found")
	ErrEventBufferFull    = errors.New("webmcp: event buffer full")
	ErrInvalidToolInput   = errors.New("webmcp: invalid tool input")
	ErrStaleSelection     = errors.New("webmcp: stale selection")
	ErrStaleToolRef       = errors.New("webmcp: stale tool ref")
	ErrCloseTimeout       = errors.New("webmcp: close timed out")
	ErrClosePanic         = errors.New("webmcp: close panicked")
)

// ErrorCode is the model-facing, stable WebMCP failure vocabulary.
type ErrorCode string

const (
	ErrorWebMCPDisabled       ErrorCode = "webmcp_disabled"
	ErrorEndpointNotFound     ErrorCode = "endpoint_not_found"
	ErrorEndpointUnreachable  ErrorCode = "endpoint_unreachable"
	ErrorRemoteEndpointDenied ErrorCode = "remote_endpoint_denied"
	ErrorBrowserProtocol      ErrorCode = "browser_protocol_invalid"
	ErrorUnsupportedWebMCP    ErrorCode = "unsupported_webmcp"
	ErrorNoEligibleTab        ErrorCode = "no_eligible_tab"
	ErrorAmbiguousBrowser     ErrorCode = "ambiguous_browser"
	ErrorAmbiguousTab         ErrorCode = "ambiguous_tab"
	ErrorStaleSelection       ErrorCode = "stale_selection"
	ErrorStaleToolRef         ErrorCode = "stale_tool_ref"
	ErrorOriginDenied         ErrorCode = "origin_denied"
	ErrorApprovalRequired     ErrorCode = "approval_required"
	ErrorApprovalDenied       ErrorCode = "approval_denied"
	ErrorInvalidToolInput     ErrorCode = "invalid_tool_input"
	ErrorResultTooLarge       ErrorCode = "result_too_large"
	ErrorTargetAttachFailed   ErrorCode = "target_attach_failed"
	ErrorTargetDetached       ErrorCode = "target_detached"
	ErrorPageNavigated        ErrorCode = "page_navigated"
	ErrorInvocationFailed     ErrorCode = "invocation_failed"
	ErrorInvocationCanceled   ErrorCode = "invocation_canceled"
	ErrorInvocationTimedOut   ErrorCode = "invocation_timed_out"
	ErrorInvocationOrphaned   ErrorCode = "invocation_orphaned"
	ErrorBrowserDisconnected  ErrorCode = "browser_disconnected"
)

var knownErrorCodes = map[ErrorCode]struct{}{
	ErrorWebMCPDisabled: {}, ErrorEndpointNotFound: {}, ErrorEndpointUnreachable: {},
	ErrorRemoteEndpointDenied: {}, ErrorBrowserProtocol: {}, ErrorUnsupportedWebMCP: {},
	ErrorNoEligibleTab: {}, ErrorAmbiguousBrowser: {}, ErrorAmbiguousTab: {},
	ErrorStaleSelection: {}, ErrorStaleToolRef: {}, ErrorOriginDenied: {},
	ErrorApprovalRequired: {}, ErrorApprovalDenied: {}, ErrorInvalidToolInput: {},
	ErrorResultTooLarge: {}, ErrorTargetAttachFailed: {}, ErrorTargetDetached: {},
	ErrorPageNavigated: {}, ErrorInvocationFailed: {}, ErrorInvocationCanceled: {},
	ErrorInvocationTimedOut: {}, ErrorInvocationOrphaned: {}, ErrorBrowserDisconnected: {},
}

// IsKnownErrorCode reports whether code belongs to the frozen C0 vocabulary.
func IsKnownErrorCode(code ErrorCode) bool {
	_, ok := knownErrorCodes[code]
	return ok
}

// ClassifiedError carries safe, already-classified broker failure metadata
// across a neutral seam. Message and Details are model-visible and therefore
// must not contain secrets, raw input, or page output.
type ClassifiedError struct {
	Code      ErrorCode
	Message   string
	Retryable bool
	Details   map[string]any
	Cause     error
}

func (e *ClassifiedError) Error() string {
	if e == nil {
		return "webmcp classified error"
	}
	if e.Message != "" {
		return e.Message
	}
	return string(e.Code)
}

func (e *ClassifiedError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// NewClassifiedError creates a safe broker error. An empty message is filled
// with the stable default when it is converted to a result envelope.
func NewClassifiedError(code ErrorCode, message string, details map[string]any) *ClassifiedError {
	return &ClassifiedError{Code: code, Message: message, Retryable: defaultRetryable(code), Details: withAmbiguityRecovery(code, details)}
}

// ResultErrorFor converts an internal error into a stable model-facing error.
// Unknown errors receive a safe generic message and never expose Error().
func ResultErrorFor(err error, fallback ErrorCode, details map[string]any) ToolResultError {
	if details == nil {
		details = map[string]any{}
	}
	var classified *ClassifiedError
	if errors.As(err, &classified) && classified != nil {
		if classified.Details != nil {
			details = cloneDetails(classified.Details)
		}
		code := classified.Code
		if !IsKnownErrorCode(code) {
			code = fallback
		}
		message := classified.Message
		if message == "" {
			message = DefaultErrorMessage(code)
		}
		return ToolResultError{Code: string(code), Message: message, Retryable: classified.Retryable, Details: withAmbiguityRecovery(code, details)}
	}
	if errors.Is(err, context.Canceled) {
		fallback = ErrorInvocationCanceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		fallback = ErrorInvocationTimedOut
	}
	if !IsKnownErrorCode(fallback) {
		fallback = ErrorInvocationFailed
	}
	return ToolResultError{
		Code:      string(fallback),
		Message:   DefaultErrorMessage(fallback),
		Retryable: defaultRetryable(fallback),
		Details:   withAmbiguityRecovery(fallback, details),
	}
}

// withAmbiguityRecovery adds bounded, model-visible instructions at the
// result boundary. Ambiguity is retryable only after the customer supplies a
// new choice; repeating the same selector-free call cannot make the result
// more specific.
func withAmbiguityRecovery(code ErrorCode, details map[string]any) map[string]any {
	if code != ErrorAmbiguousBrowser && code != ErrorAmbiguousTab {
		return details
	}
	result := sanitizeAmbiguityDetails(code, details)
	if result == nil {
		result = map[string]any{}
	}
	instruction := "Ask the customer which browser they mean, then retry once with its exact browser ID; do not repeat this call until the customer provides a choice."
	if code == ErrorAmbiguousTab {
		instruction = "Ask the customer which named page they mean, then retry once with its exact target ID; do not repeat this call until the customer provides a choice."
	}
	result["recovery"] = map[string]any{
		"action":      "ask_customer",
		"retry_after": "customer_input",
		"instruction": instruction,
	}
	return result
}

func sanitizeAmbiguityDetails(code ErrorCode, details map[string]any) map[string]any {
	result := cloneDetails(details)
	if result == nil {
		result = map[string]any{}
	}
	switch code {
	case ErrorAmbiguousBrowser:
		result["candidate_browser_ids"] = boundedAmbiguityIDs(anyAmbiguityIDs(result["candidate_browser_ids"]))
	case ErrorAmbiguousTab:
		browserID := safeAmbiguityID(stringValue(result["browser_id"]))
		ids := boundedAmbiguityIDs(anyAmbiguityIDs(result["candidate_target_ids"]))
		result["browser_id"] = browserID
		result["candidate_target_ids"] = ids
		if choices := safeAmbiguityChoices(result["candidate_choices"], browserID, ids); len(choices) > 0 {
			result["candidate_choices"] = choices
		} else {
			delete(result, "candidate_choices")
		}
	}
	return result
}

func anyAmbiguityIDs(value any) []string {
	values := make([]string, 0)
	switch typed := value.(type) {
	case []string:
		values = append(values, typed...)
	case []any:
		for _, item := range typed {
			if text, ok := item.(string); ok {
				values = append(values, text)
			}
		}
	}
	ids := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if normalized := safeAmbiguityID(value); normalized != "" {
			if _, exists := seen[normalized]; exists {
				continue
			}
			seen[normalized] = struct{}{}
			ids = append(ids, normalized)
		}
	}
	sort.Strings(ids)
	return ids
}

func boundedAmbiguityIDs(ids []string) []string {
	if len(ids) > maxAmbiguityCandidates {
		return ids[:maxAmbiguityCandidates]
	}
	return ids
}

func safeAmbiguityID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 64 {
		return ""
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '_' || character == '-' {
			continue
		}
		return ""
	}
	return value
}

func safeAmbiguityChoices(value any, fallbackBrowserID string, candidateIDs []string) []map[string]any {
	type metadata struct {
		browserID string
		targetID  string
		title     string
		origin    string
	}
	items := make([]metadata, 0)
	appendChoice := func(choice map[string]any) {
		targetID := safeAmbiguityID(stringValue(choice["target_id"]))
		if targetID == "" {
			return
		}
		browserID := safeAmbiguityID(stringValue(choice["browser_id"]))
		if browserID == "" {
			browserID = safeAmbiguityID(fallbackBrowserID)
		}
		items = append(items, metadata{
			browserID: browserID,
			targetID:  targetID,
			title:     safeAmbiguityTitle(stringValue(choice["title"])),
			origin:    safeAmbiguityOrigin(stringValue(choice["origin"])),
		})
	}
	switch typed := value.(type) {
	case []map[string]any:
		for _, choice := range typed {
			appendChoice(choice)
		}
	case []any:
		for _, item := range typed {
			if choice, ok := item.(map[string]any); ok {
				appendChoice(choice)
			}
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].targetID != items[j].targetID {
			return items[i].targetID < items[j].targetID
		}
		if items[i].browserID != items[j].browserID {
			return items[i].browserID < items[j].browserID
		}
		if items[i].title != items[j].title {
			return items[i].title < items[j].title
		}
		return items[i].origin < items[j].origin
	})

	ids := append([]string(nil), candidateIDs...)
	if len(ids) == 0 {
		for _, item := range items {
			ids = append(ids, item.targetID)
		}
	}
	ids = boundedAmbiguityIDs(anyAmbiguityIDs(ids))
	if len(ids) == 0 {
		return nil
	}
	byID := make(map[string]metadata, len(items))
	for _, item := range items {
		if _, exists := byID[item.targetID]; !exists {
			byID[item.targetID] = item
		}
	}
	result := make([]map[string]any, 0, len(ids))
	for _, targetID := range ids {
		item := byID[targetID]
		browserID := item.browserID
		if browserID == "" {
			browserID = safeAmbiguityID(fallbackBrowserID)
		}
		choice := map[string]any{"browser_id": browserID, "target_id": targetID}
		if item.title != "" {
			choice["title"] = item.title
		}
		if item.origin != "" {
			choice["origin"] = item.origin
		}
		result = append(result, choice)
	}
	return result
}

func safeAmbiguityTitle(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if len(value) > maxAmbiguityTitle {
		value = value[:maxAmbiguityTitle]
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return "redacted"
		}
	}
	if strings.Contains(value, "://") || strings.ContainsAny(value, "?#@") {
		return "redacted"
	}
	return value
}

func safeAmbiguityOrigin(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 4096 {
		return ""
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed == nil || parsed.User != nil {
		return ""
	}
	scheme := strings.ToLower(parsed.Scheme)
	if (scheme != "http" && scheme != "https") || parsed.Hostname() == "" {
		return ""
	}
	port := parsed.Port()
	if port != "" {
		value, err := strconv.Atoi(port)
		if err != nil || value < 1 || value > 65535 {
			return ""
		}
		if scheme == "http" && port == "80" || scheme == "https" && port == "443" {
			port = ""
		}
	}
	host := strings.ToLower(parsed.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port != "" {
		host += ":" + port
	}
	origin := scheme + "://" + host
	if len(origin) > maxAmbiguityOrigin {
		return ""
	}
	return origin
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func defaultRetryable(code ErrorCode) bool {
	switch code {
	case ErrorWebMCPDisabled, ErrorEndpointUnreachable, ErrorNoEligibleTab,
		ErrorAmbiguousBrowser, ErrorAmbiguousTab, ErrorStaleSelection,
		ErrorStaleToolRef, ErrorApprovalRequired, ErrorInvalidToolInput,
		ErrorTargetAttachFailed:
		return true
	default:
		return false
	}
}

// DefaultErrorMessage returns the safe model-facing message for code.
func DefaultErrorMessage(code ErrorCode) string {
	switch code {
	case ErrorWebMCPDisabled:
		return "Browser tools are not enabled."
	case ErrorInvalidToolInput:
		return "The broker tool input is invalid."
	case ErrorStaleToolRef:
		return "The page tool reference is no longer current."
	case ErrorStaleSelection:
		return "The selected browser target is no longer current."
	case ErrorInvocationCanceled:
		return "The browser invocation was canceled."
	case ErrorInvocationTimedOut:
		return "The browser invocation timed out."
	case ErrorInvocationFailed:
		return "The browser invocation failed."
	case ErrorBrowserDisconnected:
		return "The browser connection ended before the operation completed."
	default:
		return "The WebMCP operation could not be completed."
	}
}

// ContextErrorCode returns the C0 operation class for a context failure.
// Adapter packages use this helper without exposing their transport errors.
func ContextErrorCode(err error) ErrorCode {
	if errors.Is(err, context.Canceled) {
		return ErrorInvocationCanceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrorInvocationTimedOut
	}
	return ErrorInvocationFailed
}
