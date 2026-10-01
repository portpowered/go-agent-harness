package discovery

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

const (
	maxAmbiguityCandidates = 32
	maxAmbiguityTitle      = 160
	maxAmbiguityOrigin     = 256
	// maxOriginFilterLabelBytes bounds the origin filter echoed in errors.
	maxOriginFilterLabelBytes = 128
	// maxPhaseLabelBytes bounds enum-like phase and kind labels in details.
	maxPhaseLabelBytes = 32
	// maxDetailLabelBytes bounds IDs, sources and reason codes in details.
	maxDetailLabelBytes = 64
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
		return nilErrorText
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
	var codeErr *classifiedCodeError
	if errors.As(target, &codeErr) {
		return e.Code == codeErr.code
	}
	return false
}

type classifiedCodeError struct{ code Code }

func (e *classifiedCodeError) Error() string { return string(e.code) }

var (
	ErrEndpointNotFound       error = &classifiedCodeError{code: CodeEndpointNotFound}
	ErrEndpointUnreachable    error = &classifiedCodeError{code: CodeEndpointUnreachable}
	ErrRemoteEndpointDenied   error = &classifiedCodeError{code: CodeRemoteEndpointDenied}
	ErrBrowserProtocolInvalid error = &classifiedCodeError{code: CodeBrowserProtocolInvalid}
	ErrUnsupportedWebMCP      error = &classifiedCodeError{code: CodeUnsupportedWebMCP}
	ErrNoEligibleTab          error = &classifiedCodeError{code: CodeNoEligibleTab}
	ErrAmbiguousBrowser       error = &classifiedCodeError{code: CodeAmbiguousBrowser}
	ErrAmbiguousTab           error = &classifiedCodeError{code: CodeAmbiguousTab}
	ErrStaleSelection         error = &classifiedCodeError{code: CodeStaleSelection}
	ErrTargetAttachFailed     error = &classifiedCodeError{code: CodeTargetAttachFailed}
	ErrTargetDetached         error = &classifiedCodeError{code: CodeTargetDetached}
	ErrBrowserDisconnected    error = &classifiedCodeError{code: CodeBrowserDisconnected}
)

func newEndpointNotFound(kind EndpointKind, source Source) *DiscoveryError {
	return &DiscoveryError{
		Code:      CodeEndpointNotFound,
		Message:   "browser endpoint was not found",
		Retryable: false,
		Details: map[string]any{
			"endpoint_kind": string(kind),
			"source":        boundedLabel(string(source), maxDetailLabelBytes),
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
			"phase":         boundedLabel(phase, maxPhaseLabelBytes),
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
		protocol = unknownValue
	}
	return &DiscoveryError{
		Code:      CodeBrowserProtocolInvalid,
		Message:   "browser endpoint returned invalid protocol metadata",
		Retryable: false,
		Cause:     cause,
		Details: map[string]any{
			"phase":       "version",
			"protocol":    protocol,
			"reason_code": boundedLabel(reason, maxDetailLabelBytes),
		},
	}
}

func safeProtocolDetail(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if value == unknownValue || protocolVersionPattern.MatchString(value) || strings.HasPrefix(value, "http_") {
		return boundedLabel(value, maxPhaseLabelBytes)
	}
	return "invalid"
}

func newProtocolInvalidAt(phase, protocol, reason string, cause error) *DiscoveryError {
	err := newProtocolInvalid(protocol, reason, cause)
	err.Details["phase"] = boundedLabel(phase, maxPhaseLabelBytes)
	return err
}

func newUnsupportedWebMCP(browserID, targetID string) *DiscoveryError {
	return &DiscoveryError{
		Code:      CodeUnsupportedWebMCP,
		Message:   "target does not provide WebMCP",
		Retryable: false,
		Details: map[string]any{
			"browser_id":          boundedLabel(browserID, maxDetailLabelBytes),
			"target_id":           boundedLabel(targetID, maxDetailLabelBytes),
			"required_capability": "webmcp",
		},
	}
}

func newNoEligibleTab(browserID string, options TargetListOptions, candidateCount int) *DiscoveryError {
	filters := map[string]any{
		"eligible_only":           options.resolvedEligibleOnly(),
		"include_zero_tool_pages": options.IncludeZeroToolPages,
	}
	details := map[string]any{"filters": filters, "candidate_count": candidateCount}
	if browserID != "" {
		details["browser_id"] = boundedLabel(browserID, maxDetailLabelBytes)
	}
	if options.OriginContains != "" {
		filters["origin_contains"] = boundedLabel(options.OriginContains, maxOriginFilterLabelBytes)
	}
	return &DiscoveryError{
		Code:      CodeNoEligibleTab,
		Message:   "no eligible browser tab matched the requested filters",
		Retryable: true,
		Details:   details,
	}
}

func newAmbiguousBrowser(candidateIDs []string) *DiscoveryError {
	ids := safeAmbiguityIDs(candidateIDs)
	return &DiscoveryError{
		Code:      CodeAmbiguousBrowser,
		Message:   "multiple browsers matched; an exact browser ID is required",
		Retryable: true,
		Details: map[string]any{
			"candidate_browser_ids": ids,
			"recovery":              ambiguityRecovery(CodeAmbiguousBrowser),
		},
	}
}

func newAmbiguousTab(browserID string, candidateIDs []string) *DiscoveryError {
	ids := safeAmbiguityIDs(candidateIDs)
	return &DiscoveryError{
		Code:      CodeAmbiguousTab,
		Message:   "multiple browser tabs matched; an exact target ID is required",
		Retryable: true,
		Details: map[string]any{
			"browser_id":           safeAmbiguityID(browserID),
			"candidate_target_ids": ids,
			"recovery":             ambiguityRecovery(CodeAmbiguousTab),
		},
	}
}

func newAmbiguousTabForTargets(browserID string, targets []Target) *DiscoveryError {
	ordered := append([]Target(nil), targets...)
	sort.SliceStable(ordered, func(i, j int) bool {
		leftID := safeAmbiguityID(ordered[i].ID)
		rightID := safeAmbiguityID(ordered[j].ID)
		if leftID != rightID {
			return leftID < rightID
		}
		if ordered[i].Origin != ordered[j].Origin {
			return ordered[i].Origin < ordered[j].Origin
		}
		return ordered[i].Title < ordered[j].Title
	})

	ids := make([]string, 0, len(ordered))
	choices := make([]map[string]any, 0, len(ordered))
	seen := make(map[string]struct{}, len(ordered))
	for _, target := range ordered {
		targetID := safeAmbiguityID(target.ID)
		if targetID == "" {
			continue
		}
		if _, exists := seen[targetID]; exists {
			continue
		}
		seen[targetID] = struct{}{}
		ids = append(ids, targetID)
		choice := map[string]any{
			"browser_id": safeAmbiguityID(browserID),
			"target_id":  targetID,
		}
		if title := safeAmbiguityTitle(target.Title); title != "" {
			choice["title"] = title
		}
		if origin := safeAmbiguityOrigin(target); origin != "" {
			choice["origin"] = origin
		}
		choices = append(choices, choice)
		if len(choices) == maxAmbiguityCandidates {
			break
		}
	}

	failure := newAmbiguousTab(browserID, ids)
	if len(choices) > 0 {
		failure.Details["candidate_choices"] = choices
	}
	return failure
}

func safeAmbiguityIDs(values []string) []string {
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
	if len(ids) > maxAmbiguityCandidates {
		ids = ids[:maxAmbiguityCandidates]
	}
	return ids
}

func safeAmbiguityID(value string) string {
	value = strings.TrimSpace(value)
	if publicIDPattern.MatchString(value) {
		return value
	}
	return ""
}

func safeAmbiguityTitle(value string) string {
	value = boundedLabel(value, maxAmbiguityTitle)
	if value == "" {
		return ""
	}
	if strings.Contains(value, "://") || strings.ContainsAny(value, "?#@") {
		return redactedValue
	}
	return value
}

func safeAmbiguityOrigin(target Target) string {
	for _, value := range []string{target.Origin, target.URL} {
		origin := canonicalOriginValue(value)
		if origin == "" || len(origin) > maxAmbiguityOrigin {
			continue
		}
		return origin
	}
	return ""
}

func ambiguityRecovery(code Code) map[string]any {
	instruction := "Ask the customer which browser they mean, then retry once with its exact browser ID; do not repeat this call until the customer provides a choice."
	if code == CodeAmbiguousTab {
		instruction = "Ask the customer which named page they mean, then retry once with its exact target ID; do not repeat this call until the customer provides a choice."
	}
	return map[string]any{
		"action":      "ask_customer",
		"retry_after": "customer_input",
		"instruction": instruction,
	}
}

func newStaleSelection(browserID, targetID string, selectedGeneration uint64, reason string) *DiscoveryError {
	return &DiscoveryError{
		Code:      CodeStaleSelection,
		Message:   "the selected browser target is no longer current",
		Retryable: true,
		Details: map[string]any{
			"browser_id":          boundedLabel(browserID, maxDetailLabelBytes),
			"target_id":           boundedLabel(targetID, maxDetailLabelBytes),
			"selected_generation": selectedGeneration,
			"reason":              boundedLabel(reason, maxDetailLabelBytes),
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
			"browser_id":  boundedLabel(browserID, maxDetailLabelBytes),
			"target_id":   boundedLabel(targetID, maxDetailLabelBytes),
			"phase":       boundedLabel(phase, maxPhaseLabelBytes),
			"reason_code": boundedLabel(reason, maxDetailLabelBytes),
		},
	}
}

func newBrowserDisconnected(browserID, targetID, phase string, cause error) *DiscoveryError {
	if !publicIDPattern.MatchString(strings.TrimSpace(browserID)) {
		browserID = unknownValue
	} else {
		browserID = strings.TrimSpace(browserID)
	}
	phase = boundedLabel(phase, maxPhaseLabelBytes)
	if phase == "" {
		phase = phaseDisconnect
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

func (e *DiscoveryError) String() string {
	if e == nil {
		return nilErrorText
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}
