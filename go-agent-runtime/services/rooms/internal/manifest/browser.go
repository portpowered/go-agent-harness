package manifest

import (
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
)

const defaultBrowserInvocationTimeout = 20 * time.Second

func normalizeBrowser(raw *rawBrowserTools) (rooms.BrowserToolsConfig, error) {
	result := defaultBrowserToolsConfig()
	if raw.Backend != nil {
		result.Backend = strings.TrimSpace(*raw.Backend)
	}
	applyBrowserConnection(&result, raw.Connection)
	applyBrowserSelection(&result, raw.Selection)
	applyBrowserPolicy(&result, raw.Policy)
	if err := applyBrowserLimits(&result, raw.Limits); err != nil {
		return rooms.BrowserToolsConfig{}, err
	}
	applyBrowserRecording(&result, raw.Recording)
	applyBrowserReplay(&result, raw.Replay)
	return result, nil
}

func applyBrowserConnection(result *rooms.BrowserToolsConfig, value *rawBrowserConnection) {
	if value == nil {
		return
	}
	result.Connection.CDPURL = optionalString(value.CDPURL)
	result.Connection.WSEndpoint = optionalString(value.WSEndpoint)
	result.Connection.UserDataDir = optionalString(value.UserDataDir)
	if value.AllowProcessScan != nil {
		result.Connection.AllowProcessScan = *value.AllowProcessScan
	}
	if value.AllowRemoteCDP != nil {
		result.Connection.AllowRemoteCDP = *value.AllowRemoteCDP
	}
}

func applyBrowserSelection(result *rooms.BrowserToolsConfig, value *rawBrowserSelection) {
	if value == nil {
		return
	}
	result.Selection.Browser = optionalString(value.Browser)
	result.Selection.Tab = optionalString(value.Tab)
	result.Selection.Origin = optionalString(value.Origin)
	if value.AutoSelect != nil {
		result.Selection.AutoSelect = strings.TrimSpace(*value.AutoSelect)
	}
	if value.ActivateTab != nil {
		result.Selection.ActivateTab = *value.ActivateTab
	}
	if value.Persist != nil {
		result.Selection.Persist = *value.Persist
	}
}

func applyBrowserPolicy(result *rooms.BrowserToolsConfig, value *rawBrowserPolicy) {
	if value == nil {
		return
	}
	result.Policy.AllowedOrigins = cloneStrings(value.AllowedOrigins)
	result.Policy.DeniedOrigins = cloneStrings(value.DeniedOrigins)
	if value.Approval != nil {
		result.Policy.Approval = strings.TrimSpace(*value.Approval)
	}
	if value.CancelOnInterrupt != nil {
		result.Policy.CancelOnInterrupt = strings.TrimSpace(*value.CancelOnInterrupt)
	}
}

func applyBrowserLimits(result *rooms.BrowserToolsConfig, value *rawBrowserLimits) error {
	if value == nil {
		return nil
	}
	if value.InvocationTimeout != nil {
		timeout, err := time.ParseDuration(strings.TrimSpace(*value.InvocationTimeout))
		if err != nil {
			return err
		}
		result.Limits.InvocationTimeout = timeout
	}
	if value.MaxInputBytes != nil {
		result.Limits.MaxInputBytes = *value.MaxInputBytes
	}
	if value.MaxResultBytes != nil {
		result.Limits.MaxResultBytes = *value.MaxResultBytes
	}
	if value.SerializePerTarget != nil {
		result.Limits.SerializePerTarget = *value.SerializePerTarget
	}
	return nil
}

func applyBrowserRecording(result *rooms.BrowserToolsConfig, value *rawBrowserRecording) {
	if value == nil {
		return
	}
	if value.Enabled != nil {
		result.Recording.Enabled = *value.Enabled
	}
	if value.IncludeArguments != nil {
		result.Recording.IncludeArguments = *value.IncludeArguments
	}
	if value.IncludeResults != nil {
		result.Recording.IncludeResults = *value.IncludeResults
	}
	if value.RedactURLQuery != nil {
		result.Recording.RedactURLQuery = *value.RedactURLQuery
	}
	if value.RedactURLFragment != nil {
		result.Recording.RedactURLFragment = *value.RedactURLFragment
	}
}

func applyBrowserReplay(result *rooms.BrowserToolsConfig, value *rawBrowserReplay) {
	if value == nil {
		return
	}
	result.Replay.Path = optionalString(value.Path)
	if value.Strict != nil {
		result.Replay.Strict = *value.Strict
	}
}

func defaultBrowserToolsConfig() rooms.BrowserToolsConfig {
	return rooms.BrowserToolsConfig{
		Backend:   rooms.BrowserToolsBackendWebMCP,
		Selection: rooms.BrowserSelectionConfig{AutoSelect: rooms.BrowserAutoSelectOff},
		Policy: rooms.BrowserPolicyConfig{
			Approval:          rooms.BrowserApprovalWrites,
			CancelOnInterrupt: rooms.BrowserCancelOnInterruptReadOnly,
		},
		Limits: rooms.BrowserLimitsConfig{InvocationTimeout: defaultBrowserInvocationTimeout},
	}
}

func cloneStrings(value *[]string) []string {
	if value == nil {
		return nil
	}
	result := make([]string, len(*value))
	for i, item := range *value {
		result[i] = strings.TrimSpace(item)
	}
	return result
}

func validateBrowserEndpoints(manifest rooms.Manifest) error {
	for index, participant := range manifest.Participants {
		if participant.BrowserTools == nil {
			continue
		}
		field := func(name string) string { return "participants[" + formatIndex(index) + "].browserTools." + name }
		if err := validateEndpoint(field("connection.cdp_url"), participant.BrowserTools.Connection.CDPURL, []string{"http", "https"}, false, participant.BrowserTools.Connection.AllowRemoteCDP); err != nil {
			return err
		}
		if err := validateEndpoint(field("connection.ws_endpoint"), participant.BrowserTools.Connection.WSEndpoint, []string{"ws", "wss"}, true, participant.BrowserTools.Connection.AllowRemoteCDP); err != nil {
			return err
		}
	}
	return nil
}

func validateEndpoint(field, raw string, schemes []string, websocket, allowRemote bool) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.User != nil || !oneOf(parsed.Scheme, schemes...) {
		return &rooms.ValidationError{Field: field, Problem: "must be a credential-free browser endpoint", Cause: rooms.ErrInvalidBrowserEndpoint}
	}
	if websocket && (!strings.HasPrefix(parsed.Path, "/devtools/browser/") || strings.TrimPrefix(parsed.Path, "/devtools/browser/") == "") {
		return &rooms.ValidationError{Field: field, Problem: "must identify a browser websocket", Cause: rooms.ErrInvalidBrowserEndpoint}
	}
	host := strings.Trim(parsed.Hostname(), "[]")
	if !allowRemote && !isLoopback(host) {
		return &rooms.ValidationError{Field: field, Problem: "must use a loopback host unless remote CDP is enabled", Cause: rooms.ErrInvalidBrowserEndpoint}
	}
	return nil
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") || strings.EqualFold(host, "localhost.") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func formatIndex(index int) string {
	return strconv.Itoa(index)
}
