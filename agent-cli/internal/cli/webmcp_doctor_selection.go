package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"net"
	"net/url"
	"sort"
	"strings"
)

func doctorEndpointFor(browser config.BrowserConfig) WebMCPDoctorEndpoint {
	endpoint := WebMCPDoctorEndpoint{Source: "none configured", Scope: "unknown"}
	switch {
	case browser.Connection.CDPURL != "":
		endpoint.Source = "explicit HTTP URL"
		endpoint.Address = redactDoctorEndpoint(browser.Connection.CDPURL)
	case browser.Connection.WSEndpoint != "":
		endpoint.Source = "explicit WebSocket URL"
		endpoint.Address = redactDoctorEndpoint(browser.Connection.WSEndpoint)
	case browser.Connection.UserDataDir != "":
		endpoint.Source = "browser profile DevToolsActivePort"
		endpoint.Address = "<profile redacted>"
		endpoint.Scope = "local profile"
	case browser.Connection.AllowProcessScan:
		endpoint.Source = "process discovery"
		endpoint.Scope = "local process"
	}
	if endpoint.Address != "" && endpoint.Scope == "unknown" {
		endpoint.Scope = doctorEndpointScope(endpoint.Address)
	}
	return endpoint
}

func endpointKindFor(browser config.BrowserConfig) string {
	switch {
	case browser.Connection.CDPURL != "":
		return "http"
	case browser.Connection.WSEndpoint != "":
		return "websocket"
	case browser.Connection.UserDataDir != "":
		return "profile"
	default:
		return "discovery"
	}
}

func validateDoctorEndpoints(browser config.BrowserConfig) error {
	for _, endpoint := range []struct {
		name    string
		value   string
		schemes []string
	}{
		{name: "browser.connection.cdp_url", value: browser.Connection.CDPURL, schemes: []string{"http", "https"}},
		{name: "browser.connection.ws_endpoint", value: browser.Connection.WSEndpoint, schemes: []string{"ws", "wss"}},
	} {
		if endpoint.value == "" {
			continue
		}
		parsed, err := url.Parse(endpoint.value)
		if err != nil || parsed.Host == "" || !doctorContainsString(endpoint.schemes, strings.ToLower(parsed.Scheme)) {
			return fmt.Errorf("%s is not a valid browser endpoint", endpoint.name)
		}
		if parsed.User != nil {
			return fmt.Errorf("%s must not contain endpoint credentials", endpoint.name)
		}
	}
	return nil
}

func doctorContainsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func doctorEndpointScope(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" {
		return "unknown"
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "localhost" {
		return "loopback"
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return "loopback"
	}
	return "non_loopback"
}

func redactDoctorEndpoint(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return "<redacted endpoint>"
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	if parsed.Scheme == "ws" || parsed.Scheme == "wss" {
		parsed.Path = "/<redacted>"
		parsed.RawPath = ""
	}
	return parsed.String()
}

func chooseDoctorCandidate(candidates []webmcp.BrowserCandidate, browserID string) (webmcp.BrowserCandidate, error) {
	if browserID != "" {
		for _, candidate := range candidates {
			if string(candidate.ID) == browserID {
				return candidate, nil
			}
		}
		return webmcp.BrowserCandidate{}, webmcp.NewClassifiedError(webmcp.ErrorStaleSelection, "the selected browser is no longer current", map[string]any{
			"browser_id":          browserID,
			"target_id":           "",
			"selected_generation": uint64(0),
			"reason":              "browser_not_found",
		})
	}
	if len(candidates) != 1 {
		ids := make([]string, 0, len(candidates))
		for _, candidate := range candidates {
			ids = append(ids, string(candidate.ID))
		}
		sort.Strings(ids)
		return webmcp.BrowserCandidate{}, webmcp.NewClassifiedError(webmcp.ErrorAmbiguousBrowser, "multiple browsers matched; an exact browser ID is required", map[string]any{
			"candidate_browser_ids": ids,
		})
	}
	return candidates[0], nil
}

func setBrowserVersion(report *WebMCPDoctorReport, candidate webmcp.BrowserCandidate) {
	if report == nil {
		return
	}
	for index := range report.Browsers {
		if report.Browsers[index].ID == string(candidate.ID) {
			if report.Browsers[index].Product == "" {
				report.Browsers[index].Product = boundedDoctorText(candidate.Product, 160)
			}
			if report.Browsers[index].Protocol == "" {
				report.Browsers[index].Protocol = boundedDoctorText(candidate.Protocol, 80)
			}
			return
		}
	}
}

func doctorVersion(ctx context.Context, runtime WebMCPDoctorRuntime, candidate webmcp.BrowserCandidate) (webmcp.BrowserVersion, error, bool) {
	if runtime.VersionFunc != nil {
		version, err := runtime.VersionFunc(ctx, candidate)
		return version, err, true
	}
	if runtime.Catalog != nil {
		version, err := runtime.Catalog.Version(ctx, candidate)
		return version, err, true
	}
	if candidate.Product != "" || candidate.Protocol != "" {
		return webmcp.BrowserVersion{Browser: candidate.Product, ProtocolVersion: candidate.Protocol, WebSocketDebuggerURL: candidate.BrowserWSURL, BrowserInstanceID: candidate.BrowserInstanceID}, nil, true
	}
	return webmcp.BrowserVersion{}, nil, false
}

func doctorBrowserFromCandidate(candidate webmcp.BrowserCandidate) WebMCPDoctorBrowser {
	scope := "unknown"
	if doctorCandidateIsLoopback(candidate) {
		scope = "loopback"
	} else if candidate.HTTPURL != "" {
		scope = doctorEndpointScope(candidate.HTTPURL)
	} else if candidate.BrowserWSURL != "" {
		scope = doctorEndpointScope(candidate.BrowserWSURL)
	}
	return WebMCPDoctorBrowser{
		ID:       string(candidate.ID),
		Product:  boundedDoctorText(candidate.Product, 160),
		Protocol: boundedDoctorText(candidate.Protocol, 80),
		Scope:    scope,
	}
}

func doctorCandidateIsLoopback(candidate webmcp.BrowserCandidate) bool {
	if candidate.Loopback {
		return true
	}
	if candidate.HTTPURL != "" {
		return doctorEndpointScope(candidate.HTTPURL) == "loopback"
	}
	if candidate.BrowserWSURL != "" {
		return doctorEndpointScope(candidate.BrowserWSURL) == "loopback"
	}
	return false
}

func doctorEndpointForCandidate(candidate webmcp.BrowserCandidate) WebMCPDoctorEndpoint {
	endpoint := WebMCPDoctorEndpoint{Source: "discovered browser endpoint", Scope: "unknown"}
	switch {
	case candidate.HTTPURL != "":
		endpoint.Source = "discovered HTTP URL"
		endpoint.Address = redactDoctorEndpoint(candidate.HTTPURL)
	case candidate.BrowserWSURL != "":
		endpoint.Source = "discovered WebSocket URL"
		endpoint.Address = redactDoctorEndpoint(candidate.BrowserWSURL)
	}
	if endpoint.Address != "" {
		endpoint.Scope = doctorEndpointScope(endpoint.Address)
	} else if candidate.Loopback {
		endpoint.Scope = "loopback"
	}
	return endpoint
}

func doctorTargetsFrom(targets []webmcp.Target, browserID webmcp.BrowserID) ([]WebMCPDoctorTarget, int) {
	normalized := append([]webmcp.Target(nil), targets...)
	for index := range normalized {
		if normalized[index].BrowserID == "" {
			normalized[index].BrowserID = browserID
		}
	}
	sort.SliceStable(normalized, func(i, j int) bool {
		leftBrowser, rightBrowser := normalized[i].BrowserID, normalized[j].BrowserID
		if leftBrowser != rightBrowser {
			return leftBrowser < rightBrowser
		}
		return normalized[i].ID < normalized[j].ID
	})

	result := make([]WebMCPDoctorTarget, 0, len(normalized))
	pageCount := 0
	for _, target := range normalized {
		if target.Type == "" || strings.EqualFold(target.Type, "page") {
			pageCount++
		}
		result = append(result, doctorTargetFromTarget(target))
	}
	return result, pageCount
}

func doctorTargetFromTarget(target webmcp.Target) WebMCPDoctorTarget {
	typeName := target.Type
	if typeName == "" {
		typeName = "page"
	}
	return WebMCPDoctorTarget{
		BrowserID:             string(target.BrowserID),
		TargetID:              string(target.ID),
		Type:                  boundedDoctorText(typeName, 40),
		Title:                 boundedDoctorText(target.Title, 160),
		Origin:                safeOrigin(target.Origin),
		Eligible:              target.Eligible,
		EligibilityReason:     boundedDoctorText(target.EligibilityReason, 160),
		Attached:              target.Attached,
		WebMCPDomainSupported: target.WebMCPDomainSupported,
		PageToolsReady:        target.PageToolsReady,
		PageToolsKnown:        target.PageToolsKnown,
		PageToolsEvidence:     target.PageToolsEvidence,
	}
}

func countEligibleDoctorPages(targets []webmcp.Target) int {
	count := 0
	for _, target := range targets {
		if (target.Type == "" || strings.EqualFold(target.Type, "page")) && target.Eligible {
			count++
		}
	}
	return count
}

func doctorSelectionTargets(targets []webmcp.Target, browser config.BrowserConfig) ([]webmcp.Target, error) {
	eligible := make([]webmcp.Target, 0, len(targets))
	var deniedTarget *webmcp.Target
	explicitDenied := false
	for _, target := range targets {
		if target.Type != "" && !strings.EqualFold(target.Type, "page") {
			continue
		}
		if !target.Eligible {
			continue
		}
		origin := safeOrigin(target.Origin)
		if browser.Selection.Origin != "" && origin != safeOrigin(browser.Selection.Origin) {
			continue
		}
		if deniedOrigin(origin, browser.Policy) {
			if browser.Selection.Tab != "" && string(target.ID) == browser.Selection.Tab {
				explicitDenied = true
			}
			if deniedTarget == nil {
				copyTarget := target
				deniedTarget = &copyTarget
			}
			continue
		}
		eligible = append(eligible, target)
	}
	if len(browser.Policy.AllowedOrigins) > 0 {
		allowed := make([]webmcp.Target, 0, len(eligible))
		for _, target := range eligible {
			if allowedOrigin(safeOrigin(target.Origin), browser.Policy.AllowedOrigins) {
				allowed = append(allowed, target)
				continue
			}
			if browser.Selection.Tab != "" && string(target.ID) == browser.Selection.Tab {
				explicitDenied = true
				deniedTarget = &target
			} else if deniedTarget == nil {
				copyTarget := target
				deniedTarget = &copyTarget
			}
		}
		eligible = allowed
	}
	if (explicitDenied || len(eligible) == 0) && deniedTarget != nil {
		origin := safeOrigin(deniedTarget.Origin)
		return nil, webmcp.NewClassifiedError(webmcp.ErrorOriginDenied, "the selected page origin is denied by policy", map[string]any{
			"origin_digest": originDigest(origin),
			"policy":        originPolicyName(origin, browser.Policy),
		})
	}
	sort.SliceStable(eligible, func(i, j int) bool { return eligible[i].ID < eligible[j].ID })
	return eligible, nil
}

func chooseDoctorTarget(targets []webmcp.Target, selection config.BrowserSelectionConfig) (*webmcp.Target, string, error) {
	if selection.Tab != "" {
		for index := range targets {
			if string(targets[index].ID) == selection.Tab {
				selected := targets[index]
				return &selected, "", nil
			}
		}
		return nil, "", webmcp.NewClassifiedError(webmcp.ErrorStaleSelection, "the selected browser target is no longer current", map[string]any{
			"browser_id":          string(selection.Browser),
			"target_id":           selection.Tab,
			"selected_generation": uint64(0),
			"reason":              "target_not_found",
		})
	}
	switch selection.AutoSelect {
	case config.BrowserAutoSelectSingle:
		if len(targets) == 0 {
			return nil, "", webmcp.NewClassifiedError(webmcp.ErrorNoEligibleTab, "no eligible WebMCP target was found", map[string]any{"candidate_count": 0})
		}
		if len(targets) > 1 {
			return nil, "", webmcp.NewClassifiedError(webmcp.ErrorAmbiguousTab, "multiple eligible browser targets matched; an exact target ID is required", map[string]any{
				"browser_id":           normalizeDirectOpaqueID(string(targets[0].BrowserID)),
				"candidate_target_ids": directAmbiguityTargetIDs(targets),
				"candidate_choices":    directCandidateChoicesForTargets(string(targets[0].BrowserID), targets),
			})
		}
		selected := targets[0]
		return &selected, "", nil
	case config.BrowserAutoSelectPersisted:
		return nil, "", webmcp.NewClassifiedError(webmcp.ErrorStaleSelection, "persisted browser target selection is not current", map[string]any{
			"browser_id":          string(selection.Browser),
			"target_id":           "",
			"selected_generation": uint64(0),
			"reason":              "persisted_selection_missing",
		})
	case config.BrowserAutoSelectOff, "":
		if len(targets) == 0 {
			return nil, "", webmcp.NewClassifiedError(webmcp.ErrorNoEligibleTab, "no eligible WebMCP target was found", map[string]any{"candidate_count": 0})
		}
		return nil, "No target selected; run `yui webmcp tabs` and `yui webmcp select` or set browser.selection.auto_select.", nil
	default:
		return nil, "", webmcp.NewClassifiedError(webmcp.ErrorStaleSelection, "browser target auto-selection is invalid", map[string]any{"reason": "invalid_auto_select"})
	}
}

func selectDoctorTarget(ctx context.Context, broker webmcp.Broker, target *webmcp.Target, activate bool) (webmcp.PageContext, error) {
	selector := webmcp.TargetSelector{BrowserID: target.BrowserID, TargetID: target.ID}
	if selectorWithOptions, ok := broker.(interface {
		SelectWithOptions(context.Context, webmcp.TargetSelector, webmcp.SelectOptions) (webmcp.PageContext, error)
	}); ok {
		return selectorWithOptions.SelectWithOptions(ctx, selector, webmcp.SelectOptions{Activate: activate})
	}
	return broker.Select(ctx, selector)
}

func deniedOrigin(origin string, policy config.BrowserPolicyConfig) bool {
	for _, denied := range policy.DeniedOrigins {
		if safeOrigin(denied) == origin {
			return true
		}
	}
	return false
}

func allowedOrigin(origin string, allowed []string) bool {
	for _, value := range allowed {
		if safeOrigin(value) == origin {
			return true
		}
	}
	return false
}

func originPolicyName(origin string, policy config.BrowserPolicyConfig) string {
	if deniedOrigin(origin, policy) {
		return "denied_origins"
	}
	return "allowed_origins"
}

func safeOrigin(raw string) string {
	parsed, err := url.Parse(raw)
	if err == nil && parsed.Scheme != "" && parsed.Host != "" {
		return strings.ToLower(parsed.Scheme) + "://" + strings.ToLower(parsed.Host)
	}
	cleaned := raw
	if index := strings.IndexAny(cleaned, "?#"); index >= 0 {
		cleaned = cleaned[:index]
	}
	return boundedDoctorText(cleaned, 200)
}

func originDigest(origin string) string {
	digest := sha256.Sum256([]byte(origin))
	return hex.EncodeToString(digest[:])
}

func boundedDoctorText(value string, limit int) string {
	value = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' || r >= ' ' {
			return r
		}
		return -1
	}, value)
	if limit > 0 && len(value) > limit {
		return value[:limit]
	}
	return value
}
