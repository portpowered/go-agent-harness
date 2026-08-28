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
	CodeUnsupportedWebMCP      Code = "unsupported_webmcp"
	CodeNoEligibleTab          Code = "no_eligible_tab"
	CodeAmbiguousBrowser       Code = "ambiguous_browser"
	CodeAmbiguousTab           Code = "ambiguous_tab"
	CodeStaleSelection         Code = "stale_selection"
	CodeTargetAttachFailed     Code = "target_attach_failed"
	CodeTargetDetached         Code = "target_detached"
	CodeBrowserDisconnected    Code = "browser_disconnected"
)

// DiscoveryError is safe for model/user display. Details are constrained to
// the C0 shape for endpoint and target-discovery classifications and never
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
	ErrUnsupportedWebMCP      error = &classifiedCode{code: CodeUnsupportedWebMCP}
	ErrNoEligibleTab          error = &classifiedCode{code: CodeNoEligibleTab}
	ErrAmbiguousBrowser       error = &classifiedCode{code: CodeAmbiguousBrowser}
	ErrAmbiguousTab           error = &classifiedCode{code: CodeAmbiguousTab}
	ErrStaleSelection         error = &classifiedCode{code: CodeStaleSelection}
	ErrTargetAttachFailed     error = &classifiedCode{code: CodeTargetAttachFailed}
	ErrTargetDetached         error = &classifiedCode{code: CodeTargetDetached}
	ErrBrowserDisconnected    error = &classifiedCode{code: CodeBrowserDisconnected}
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

func newUnsupportedWebMCP(browserID, targetID string) *DiscoveryError {
	return &DiscoveryError{
		Code:      CodeUnsupportedWebMCP,
		Message:   "target does not provide WebMCP",
		Retryable: false,
		Details: map[string]any{
			"browser_id":          boundedLabel(browserID, 64),
			"target_id":           boundedLabel(targetID, 64),
			"required_capability": "webmcp",
		},
	}
}

func newNoEligibleTab(browserID string, options TargetListOptions, candidateCount int) *DiscoveryError {
	details := map[string]any{
		"filters": map[string]any{
			"eligible_only":           options.resolvedEligibleOnly(),
			"include_zero_tool_pages": options.IncludeZeroToolPages,
		},
		"candidate_count": candidateCount,
	}
	if browserID != "" {
		details["browser_id"] = boundedLabel(browserID, 64)
	}
	if options.OriginContains != "" {
		details["filters"].(map[string]any)["origin_contains"] = boundedLabel(options.OriginContains, 128)
	}
	return &DiscoveryError{
		Code:      CodeNoEligibleTab,
		Message:   "no eligible browser tab matched the requested filters",
		Retryable: true,
		Details:   details,
	}
}

func newAmbiguousBrowser(candidateIDs []string) *DiscoveryError {
	ids := append([]string(nil), candidateIDs...)
	return &DiscoveryError{
		Code:      CodeAmbiguousBrowser,
		Message:   "multiple browsers matched; an exact browser ID is required",
		Retryable: true,
		Details: map[string]any{
			"candidate_browser_ids": ids,
		},
	}
}

func newAmbiguousTab(browserID string, candidateIDs []string) *DiscoveryError {
	ids := append([]string(nil), candidateIDs...)
	return &DiscoveryError{
		Code:      CodeAmbiguousTab,
		Message:   "multiple browser tabs matched; an exact target ID is required",
		Retryable: true,
		Details: map[string]any{
			"browser_id":           boundedLabel(browserID, 64),
			"candidate_target_ids": ids,
		},
	}
}

func newStaleSelection(browserID, targetID string, selectedGeneration uint64, reason string) *DiscoveryError {
	return &DiscoveryError{
		Code:      CodeStaleSelection,
		Message:   "the selected browser target is no longer current",
		Retryable: true,
		Details: map[string]any{
			"browser_id":          boundedLabel(browserID, 64),
			"target_id":           boundedLabel(targetID, 64),
			"selected_generation": selectedGeneration,
			"reason":              boundedLabel(reason, 64),
		},
	}
}

func newTargetDetached(browserID, targetID string, generation uint64, reason string) *DiscoveryError {
	return &DiscoveryError{
		Code:      CodeTargetDetached,
		Message:   "the selected browser target is detached",
		Retryable: false,
		Details: map[string]any{
			"browser_id": boundedLabel(browserID, 64),
			"target_id":  boundedLabel(targetID, 64),
			"generation": generation,
			"reason":     boundedLabel(reason, 64),
		},
	}
}

func newTargetAttachFailed(browserID, targetID, phase, reason string, cause error) *DiscoveryError {
	return &DiscoveryError{
		Code:      CodeTargetAttachFailed,
		Message:   "browser target could not be initialized",
		Retryable: true,
		Cause:     cause,
		Details: map[string]any{
			"browser_id":  boundedLabel(browserID, 64),
			"target_id":   boundedLabel(targetID, 64),
			"phase":       boundedLabel(phase, 32),
			"reason_code": boundedLabel(reason, 64),
		},
	}
}

func newBrowserDisconnected(browserID, targetID, phase string, cause error) *DiscoveryError {
	if !publicIDPattern.MatchString(strings.TrimSpace(browserID)) {
		browserID = "unknown"
	} else {
		browserID = strings.TrimSpace(browserID)
	}
	phase = boundedLabel(phase, 32)
	if phase == "" {
		phase = "disconnect"
	}
	details := map[string]any{
		"browser_id":         browserID,
		"phase":              phase,
		"reconnect_required": true,
	}
	if targetID = strings.TrimSpace(targetID); publicIDPattern.MatchString(targetID) {
		details["target_id"] = targetID
	}
	return &DiscoveryError{
		Code:      CodeBrowserDisconnected,
		Message:   "browser connection ended; an exact reconnect is required",
		Retryable: false,
		Cause:     cause,
		Details:   details,
	}
}

func classifiedFrom(err error, kind EndpointKind, source Source) *DiscoveryError {
	if err == nil {
		return newEndpointNotFound(kind, source)
	}
	if isBrowserDisconnected(err) {
		return newBrowserDisconnectedFromError(err, "", "", "resolve")
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
