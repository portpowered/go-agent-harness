package tools

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/discovery"
)

func laneBInvalidEnvelope(toolName string, values map[string]any, issues []ToolResultIssue) ([]byte, error) {
	toolRef := ""
	if values != nil {
		// Only an opaque-looking value could be returned; Lane B has no
		// tool_ref input, so this remains empty for all three tools.
		if value, ok := values["tool_ref"].(string); ok && laneBNormalizedIDPattern.MatchString(value) {
			toolRef = value
		}
	}
	inputSchema := objectSchema()
	for _, definition := range StableToolDefinitions() {
		if definition.Name == toolName {
			inputSchema = laneBCloneMap(definition.Parameters)
			break
		}
	}
	return EncodeToolResult(nil, &ToolResultError{
		Code:      string(ErrorInvalidToolInput),
		Message:   "The broker tool input is invalid.",
		Retryable: true,
		Details: map[string]any{
			"tool":         boundedOutputLabel(toolName, 64),
			"tool_ref":     toolRef,
			"input_schema": inputSchema,
			"issues":       issues,
		},
	})
}

func laneBDisabledEnvelope() ([]byte, error) {
	return EncodeToolResult(nil, &ToolResultError{
		Code:      string(ErrorWebMCPDisabled),
		Message:   "Browser tools are not enabled.",
		Retryable: true,
		Details:   map[string]any{"activation": "browser-tools"},
	})
}

func laneBBrokerFailure(err error, fallback ErrorCode, details map[string]any) ([]byte, error) {
	resultError := resultErrorFor(err, fallback, details)
	return EncodeToolResult(nil, &resultError)
}

func resultErrorFor(err error, fallback ErrorCode, fallbackDetails map[string]any) ToolResultError {
	if fallbackDetails == nil {
		fallbackDetails = map[string]any{}
	}
	var discoveryErr *discovery.DiscoveryError
	if errors.As(err, &discoveryErr) && discoveryErr != nil {
		code := ErrorCode(discoveryErr.Code)
		if !IsKnownErrorCode(code) {
			code = fallback
		}
		message := discoveryErr.Message
		if message == "" {
			message = defaultErrorMessage(code)
		}
		detailValues := safeDiscoveryDetails(code, discoveryErr.Details)
		if len(detailValues) == 0 {
			detailValues = safeDetails(fallbackDetails)
		}
		return ToolResultError{Code: string(code), Message: safeMessage(message, code), Retryable: discoveryErr.Retryable, Details: withAmbiguityRecovery(code, detailValues)}
	}
	if discovery.IsBrowserDisconnected(err) {
		return ToolResultError{Code: string(ErrorBrowserDisconnected), Message: defaultErrorMessage(ErrorBrowserDisconnected), Retryable: false, Details: safeDetails(fallbackDetails)}
	}
	if errors.Is(err, context.Canceled) {
		fallback = ErrorEndpointUnreachable
	}
	if !IsKnownErrorCode(fallback) {
		fallback = ErrorNoEligibleTab
	}
	return ToolResultError{Code: string(fallback), Message: defaultErrorMessage(fallback), Retryable: retryable(fallback), Details: withAmbiguityRecovery(fallback, safeDetails(fallbackDetails))}
}

func discoveryCode(err error) ErrorCode {
	var discoveryErr *discovery.DiscoveryError
	if errors.As(err, &discoveryErr) && discoveryErr != nil {
		return ErrorCode(discoveryErr.Code)
	}
	return ""
}

func safeDiscoveryDetails(code ErrorCode, details map[string]any) map[string]any {
	result := make(map[string]any)
	if details == nil {
		details = map[string]any{}
	}
	copyField := func(key string, value any) {
		if value != nil {
			result[key] = value
		}
	}
	switch code {
	case ErrorEndpointNotFound:
		copyField("endpoint_kind", safeLabel(details["endpoint_kind"], 32))
		copyField("source", safeLabel(details["source"], 64))
	case ErrorEndpointUnreachable:
		copyField("endpoint_kind", safeLabel(details["endpoint_kind"], 32))
		copyField("address_class", safeLabel(details["address_class"], 32))
		copyField("phase", safeLabel(details["phase"], 32))
	case ErrorRemoteEndpointDenied:
		copyField("endpoint_kind", safeLabel(details["endpoint_kind"], 32))
		copyField("network_class", safeLabel(details["network_class"], 32))
		copyField("required_flag", safeLabel(details["required_flag"], 64))
	case ErrorBrowserProtocol:
		copyField("phase", safeLabel(details["phase"], 32))
		copyField("protocol", safeLabel(details["protocol"], 32))
		copyField("reason_code", safeLabel(details["reason_code"], 64))
	case ErrorUnsupportedWebMCP:
		copyField("browser_id", safeIDValue(details["browser_id"]))
		copyField("target_id", safeIDValue(details["target_id"]))
		copyField("required_capability", safeLabel(details["required_capability"], 32))
	case ErrorNoEligibleTab:
		copyField("browser_id", safeIDValue(details["browser_id"]))
		if filters, ok := details["filters"].(map[string]any); ok {
			result["filters"] = safeFilterDetails(filters)
		}
		copyField("candidate_count", nonNegativeInt(details["candidate_count"]))
	case ErrorAmbiguousBrowser:
		copyField("candidate_browser_ids", boundedAmbiguityIDs(safeIDList(details["candidate_browser_ids"])))
	case ErrorAmbiguousTab:
		copyField("browser_id", safeIDValue(details["browser_id"]))
		ids := boundedAmbiguityIDs(safeIDList(details["candidate_target_ids"]))
		copyField("candidate_target_ids", ids)
		if choices := safeCandidateChoices(details["candidate_choices"], safeIDValue(details["browser_id"]), ids); len(choices) > 0 {
			result["candidate_choices"] = choices
		}
	case ErrorStaleSelection:
		copyField("browser_id", safeIDValue(details["browser_id"]))
		copyField("target_id", safeIDValue(details["target_id"]))
		copyField("selected_generation", nonNegativeUint(details["selected_generation"]))
		copyField("reason", safeLabel(details["reason"], 64))
	case ErrorTargetAttachFailed, ErrorTargetDetached:
		copyField("browser_id", safeIDValue(details["browser_id"]))
		copyField("target_id", safeIDValue(details["target_id"]))
		copyField("phase", safeLabel(details["phase"], 32))
		copyField("reason_code", safeLabel(details["reason_code"], 64))
		copyField("generation", nonNegativeUint(details["generation"]))
		copyField("reason", safeLabel(details["reason"], 64))
	case ErrorBrowserDisconnected:
		copyField("browser_id", safeIDValue(details["browser_id"]))
		copyField("target_id", safeIDValue(details["target_id"]))
		copyField("phase", safeLabel(details["phase"], 32))
		result["reconnect_required"] = true
	}
	return result
}

const (
	maxAmbiguityCandidates = 32
	maxAmbiguityTitle      = 160
	maxAmbiguityOrigin     = 256
)

type ambiguityCandidateMetadata struct {
	browserID string
	targetID  string
	title     string
	origin    string
}

func boundedAmbiguityIDs(ids []string) []string {
	if len(ids) > maxAmbiguityCandidates {
		return ids[:maxAmbiguityCandidates]
	}
	return ids
}

func safeCandidateChoices(value any, fallbackBrowserID string, candidateIDs []string) []map[string]any {
	metadata := make([]ambiguityCandidateMetadata, 0)
	switch values := value.(type) {
	case []map[string]any:
		metadata = append(metadata, candidateMetadataValues(values, fallbackBrowserID)...)
	case []any:
		maps := make([]map[string]any, 0, len(values))
		for _, item := range values {
			if choice, ok := item.(map[string]any); ok {
				maps = append(maps, choice)
			}
		}
		metadata = append(metadata, candidateMetadataValues(maps, fallbackBrowserID)...)
	}

	ids := append([]string(nil), candidateIDs...)
	if len(ids) == 0 {
		for _, item := range metadata {
			ids = append(ids, item.targetID)
		}
	}
	ids = boundedAmbiguityIDs(uniqueSortedIDs(ids))
	if len(ids) == 0 {
		return nil
	}

	byTarget := make(map[string]ambiguityCandidateMetadata, len(metadata))
	for _, item := range metadata {
		if _, exists := byTarget[item.targetID]; !exists {
			byTarget[item.targetID] = item
		}
	}
	result := make([]map[string]any, 0, len(ids))
	for _, targetID := range ids {
		item := byTarget[targetID]
		browserID := safeID(item.browserID)
		if browserID == "" {
			browserID = safeID(fallbackBrowserID)
		}
		choice := map[string]any{
			"browser_id": browserID,
			"target_id":  targetID,
		}
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

func candidateMetadataValues(values []map[string]any, fallbackBrowserID string) []ambiguityCandidateMetadata {
	metadata := make([]ambiguityCandidateMetadata, 0, len(values))
	for _, value := range values {
		targetID := safeIDValue(value["target_id"])
		if targetID == "" {
			continue
		}
		browserID := safeIDValue(value["browser_id"])
		if browserID == "" {
			browserID = safeID(fallbackBrowserID)
		}
		metadata = append(metadata, ambiguityCandidateMetadata{
			browserID: browserID,
			targetID:  targetID,
			title:     safeCandidateTitle(value["title"]),
			origin:    safeCandidateOrigin(value["origin"]),
		})
	}
	sort.SliceStable(metadata, func(i, j int) bool {
		if metadata[i].targetID != metadata[j].targetID {
			return metadata[i].targetID < metadata[j].targetID
		}
		if metadata[i].browserID != metadata[j].browserID {
			return metadata[i].browserID < metadata[j].browserID
		}
		if metadata[i].title != metadata[j].title {
			return metadata[i].title < metadata[j].title
		}
		return metadata[i].origin < metadata[j].origin
	})
	return metadata
}

func uniqueSortedIDs(values []string) []string {
	ids := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if normalized := safeID(value); normalized != "" {
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

func safeCandidateTitle(value any) string {
	return safeLabel(value, maxAmbiguityTitle)
}

func safeCandidateOrigin(value any) string {
	raw, ok := value.(string)
	if !ok {
		return ""
	}
	_, origin := safePageMetadata("", raw)
	if len(origin) > maxAmbiguityOrigin {
		return ""
	}
	return origin
}

func withAmbiguityRecovery(code ErrorCode, details map[string]any) map[string]any {
	if code != ErrorAmbiguousBrowser && code != ErrorAmbiguousTab {
		return details
	}
	if details == nil {
		details = map[string]any{}
	}
	result := make(map[string]any, len(details)+1)
	for key, value := range details {
		result[key] = value
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

func safeFilterDetails(details map[string]any) map[string]any {
	result := map[string]any{}
	if value, ok := details["eligible_only"].(bool); ok {
		result["eligible_only"] = value
	}
	if value, ok := details["include_zero_tool_pages"].(bool); ok {
		result["include_zero_tool_pages"] = value
	}
	if value, ok := details["origin_contains"].(string); ok {
		result["origin_contains"] = safeOriginFilter(value)
	}
	return result
}

func safeDetails(details map[string]any) map[string]any {
	result := map[string]any{}
	for key, value := range details {
		keyLabel := safeLabel(key, 64)
		if keyLabel == "" {
			continue
		}
		switch typed := value.(type) {
		case string:
			result[keyLabel] = safeLabel(typed, 128)
		case bool:
			result[keyLabel] = typed
		case int:
			if typed >= 0 {
				result[keyLabel] = typed
			}
		case int64:
			if typed >= 0 {
				result[keyLabel] = typed
			}
		case uint64:
			result[keyLabel] = typed
		}
	}
	return result
}

func noSelectionError() error {
	return &discovery.DiscoveryError{
		Code:      discovery.CodeNoEligibleTab,
		Message:   "no selected browser tab is available",
		Retryable: true,
		Details: map[string]any{
			"filters":         map[string]any{"selection": "current"},
			"candidate_count": 0,
		},
	}
}

func noEligibleError(browserID string, options listOptions, candidateCount int) error {
	filters := map[string]any{
		"eligible_only":           options.EligibleOnly,
		"include_zero_tool_pages": options.IncludeZeroToolPages,
	}
	if options.OriginContains != "" {
		filters["origin_contains"] = safeOriginFilter(options.OriginContains)
	}
	details := map[string]any{"filters": filters, "candidate_count": maxInt(candidateCount, 0)}
	if laneBNormalizedIDPattern.MatchString(browserID) {
		details["browser_id"] = browserID
	}
	return &discovery.DiscoveryError{
		Code:      discovery.CodeNoEligibleTab,
		Message:   "no eligible browser tab matched the requested filters",
		Retryable: true,
		Details:   details,
	}
}

func unsupportedError(browserID, targetID string) error {
	return &discovery.DiscoveryError{
		Code:      discovery.CodeUnsupportedWebMCP,
		Message:   "target does not provide WebMCP",
		Retryable: false,
		Details: map[string]any{
			"browser_id":          safeID(browserID),
			"target_id":           safeID(targetID),
			"required_capability": "webmcp",
		},
	}
}

func disconnectedError(browserID, targetID, phase string) error {
	return &discovery.DiscoveryError{
		Code:      discovery.CodeBrowserDisconnected,
		Message:   "browser connection ended; an exact reconnect is required",
		Retryable: false,
		Details: map[string]any{
			"browser_id":         safeID(browserID),
			"target_id":          safeID(targetID),
			"phase":              boundedOutputLabel(phase, 32),
			"reconnect_required": true,
		},
	}
}

func retryable(code ErrorCode) bool {
	switch code {
	case ErrorWebMCPDisabled, ErrorEndpointUnreachable, ErrorNoEligibleTab, ErrorAmbiguousBrowser, ErrorAmbiguousTab, ErrorStaleSelection, ErrorInvalidToolInput:
		return true
	default:
		return false
	}
}

func defaultErrorMessage(code ErrorCode) string {
	switch code {
	case ErrorWebMCPDisabled:
		return "Browser tools are not enabled."
	case ErrorEndpointNotFound:
		return "No authorized browser endpoint was found."
	case ErrorEndpointUnreachable:
		return "The browser endpoint could not be reached."
	case ErrorRemoteEndpointDenied:
		return "The remote browser endpoint is not permitted."
	case ErrorBrowserProtocol:
		return "The browser endpoint returned invalid protocol metadata."
	case ErrorUnsupportedWebMCP:
		return "The selected target does not provide WebMCP."
	case ErrorNoEligibleTab:
		return "No eligible browser tab matched the request."
	case ErrorAmbiguousBrowser:
		return "Multiple browsers matched; an exact browser ID is required."
	case ErrorAmbiguousTab:
		return "Multiple browser tabs matched; an exact target ID is required."
	case ErrorStaleSelection:
		return "The selected browser target is no longer current."
	case ErrorTargetAttachFailed:
		return "The selected browser target could not be initialized."
	case ErrorTargetDetached:
		return "The selected browser target was detached."
	case ErrorBrowserDisconnected:
		return "The browser connection ended before the operation completed."
	case ErrorInvalidToolInput:
		return "The broker tool input is invalid."
	default:
		return "The WebMCP operation could not be completed."
	}
}

func safeMessage(message string, code ErrorCode) string {
	message = strings.TrimSpace(message)
	if message == "" || len(message) > 160 || strings.ContainsAny(message, "\r\n") || strings.Contains(message, "://") || strings.ContainsAny(message, "?#") {
		return defaultErrorMessage(code)
	}
	return message
}

func safeID(value string) string {
	if laneBNormalizedIDPattern.MatchString(strings.TrimSpace(value)) {
		return strings.TrimSpace(value)
	}
	return ""
}

func safeIDValue(value any) string {
	text, _ := value.(string)
	return safeID(text)
}

func safeIDList(value any) []string {
	var result []string
	switch values := value.(type) {
	case []string:
		result = make([]string, 0, len(values))
		for _, value := range values {
			if normalized := safeID(value); normalized != "" {
				result = append(result, normalized)
			}
		}
	case []any:
		result = make([]string, 0, len(values))
		for _, value := range values {
			if text, ok := value.(string); ok {
				if normalized := safeID(text); normalized != "" {
					result = append(result, normalized)
				}
			}
		}
	}
	sort.Strings(result)
	return result
}

func safeLabel(value any, max int) string {
	text, _ := value.(string)
	if strings.Contains(text, "://") || strings.ContainsAny(text, "?#@") {
		return "redacted"
	}
	return boundedOutputLabel(text, max)
}

func safeOriginFilter(value string) string {
	if strings.ContainsAny(value, "?#") || strings.Contains(value, "@") {
		return "redacted"
	}
	return boundedOutputLabel(value, 128)
}

func boundedOutputLabel(value string, max int) string {
	value = strings.TrimSpace(value)
	if max < 1 {
		return ""
	}
	if len(value) > max {
		value = value[:max]
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return "redacted"
		}
	}
	return value
}

func nonNegativeInt(value any) int {
	switch typed := value.(type) {
	case int:
		if typed >= 0 {
			return typed
		}
	case int64:
		if typed >= 0 && typed <= int64(^uint(0)>>1) {
			return int(typed)
		}
	case float64:
		if typed >= 0 && typed <= float64(^uint(0)>>1) {
			return int(typed)
		}
	}
	return 0
}

func nonNegativeUint(value any) uint64 {
	switch typed := value.(type) {
	case uint64:
		return typed
	case int:
		if typed >= 0 {
			return uint64(typed)
		}
	case float64:
		if typed >= 0 {
			return uint64(typed)
		}
	}
	return 0
}
