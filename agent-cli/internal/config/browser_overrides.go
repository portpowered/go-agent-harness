package config

import (
	"time"

	runtimeRooms "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
)

// BrowserOverrides contains only explicitly supplied command-line browser
// values. Pointer fields preserve the distinction between an omitted option
// and an explicit zero, false, empty string, or empty list.
type BrowserOverrides struct {
	ToolsBackend       *string
	WebCast            *bool
	CDPURL             *string
	WSEndpoint         *string
	UserDataDir        *string
	AllowProcessScan   *bool
	AllowRemoteCDP     *bool
	ManagedHeadless    *bool
	ManagedOpen        *string
	ManagedCloseOnExit *bool
	Browser            *string
	Tab                *string
	Origin             *string
	AutoSelect         *string
	ActivateTab        *bool
	PersistSelection   *bool
	AllowedOrigins     *[]string
	DeniedOrigins      *[]string
	Approval           *string
	CancelOnInterrupt  *string
	InvocationTimeout  *time.Duration
	MaxInputBytes      *int
	MaxResultBytes     *int
	SerializePerTarget *bool
	Record             *bool
	RecordArguments    *bool
	RecordResults      *bool
	RedactURLQuery     *bool
	RedactURLFragment  *bool
	Replay             *string
	ReplayStrict       *bool
}

// ApplyBrowserOverrides returns a copy of c with only the supplied CLI
// browser values applied. The returned config is validated before it is
// handed to provider or browser construction.
func (c BrowserConfig) ApplyBrowserOverrides(overrides BrowserOverrides) (BrowserConfig, error) {
	out := c
	out.Policy.AllowedOrigins = cloneBrowserStrings(c.Policy.AllowedOrigins)
	out.Policy.DeniedOrigins = cloneBrowserStrings(c.Policy.DeniedOrigins)

	if overrides.ToolsBackend != nil {
		out.Tools.Enabled = true
		out.Tools.Backend = *overrides.ToolsBackend
	}
	applyBrowserOverride(&out.Tools.WebCast, overrides.WebCast)
	applyBrowserOverride(&out.Connection.CDPURL, overrides.CDPURL)
	applyBrowserOverride(&out.Connection.WSEndpoint, overrides.WSEndpoint)
	applyBrowserOverride(&out.Connection.UserDataDir, overrides.UserDataDir)
	applyBrowserOverride(&out.Connection.AllowProcessScan, overrides.AllowProcessScan)
	applyBrowserOverride(&out.Connection.AllowRemoteCDP, overrides.AllowRemoteCDP)
	applyBrowserOverride(&out.Managed.Headless, overrides.ManagedHeadless)
	applyBrowserOverride(&out.Managed.Open, overrides.ManagedOpen)
	applyBrowserOverride(&out.Managed.CloseOnExit, overrides.ManagedCloseOnExit)
	applyBrowserOverride(&out.Selection.Browser, overrides.Browser)
	applyBrowserOverride(&out.Selection.Tab, overrides.Tab)
	applyBrowserOverride(&out.Selection.Origin, overrides.Origin)
	applyBrowserOverride(&out.Selection.AutoSelect, overrides.AutoSelect)
	applyBrowserOverride(&out.Selection.ActivateTab, overrides.ActivateTab)
	applyBrowserOverride(&out.Selection.Persist, overrides.PersistSelection)
	if overrides.AllowedOrigins != nil {
		out.Policy.AllowedOrigins = cloneBrowserStrings(*overrides.AllowedOrigins)
	}
	if overrides.DeniedOrigins != nil {
		out.Policy.DeniedOrigins = cloneBrowserStrings(*overrides.DeniedOrigins)
	}
	applyBrowserOverride(&out.Policy.Approval, overrides.Approval)
	applyBrowserOverride(&out.Policy.CancelOnInterrupt, overrides.CancelOnInterrupt)
	applyBrowserOverride(&out.Limits.InvocationTimeout, overrides.InvocationTimeout)
	applyBrowserOverride(&out.Limits.MaxInputBytes, overrides.MaxInputBytes)
	applyBrowserOverride(&out.Limits.MaxResultBytes, overrides.MaxResultBytes)
	applyBrowserOverride(&out.Limits.SerializePerTarget, overrides.SerializePerTarget)
	applyBrowserOverride(&out.Recording.Enabled, overrides.Record)
	applyBrowserOverride(&out.Recording.IncludeArguments, overrides.RecordArguments)
	applyBrowserOverride(&out.Recording.IncludeResults, overrides.RecordResults)
	applyBrowserOverride(&out.Recording.RedactURLQuery, overrides.RedactURLQuery)
	applyBrowserOverride(&out.Recording.RedactURLFragment, overrides.RedactURLFragment)
	applyBrowserOverride(&out.Replay.Path, overrides.Replay)
	applyBrowserOverride(&out.Replay.Strict, overrides.ReplayStrict)

	if err := out.Validate(); err != nil {
		return BrowserConfig{}, err
	}
	return out, nil
}

// applyBrowserOverride replaces *dst only when the CLI supplied a value.
func applyBrowserOverride[T any](dst *T, value *T) {
	if value != nil {
		*dst = *value
	}
}

func cloneBrowserStrings(values []string) []string {
	if values == nil {
		return nil
	}
	return append([]string{}, values...)
}

// BrowserConfigForRoomTools projects one room participant's admitted browser
// tool policy onto a complete, enabled host browser configuration. Fields
// the room policy does not carry keep their host defaults.
func BrowserConfigForRoomTools(value runtimeRooms.BrowserToolsConfig) BrowserConfig {
	result := DefaultBrowserConfig()
	result.Tools.Enabled = true
	result.Tools.Backend = value.Backend
	result.Connection.CDPURL = value.Connection.CDPURL
	result.Connection.WSEndpoint = value.Connection.WSEndpoint
	result.Connection.UserDataDir = value.Connection.UserDataDir
	result.Connection.AllowProcessScan = value.Connection.AllowProcessScan
	result.Connection.AllowRemoteCDP = value.Connection.AllowRemoteCDP
	result.Selection.Browser = value.Selection.Browser
	result.Selection.Tab = value.Selection.Tab
	result.Selection.Origin = value.Selection.Origin
	result.Selection.AutoSelect = value.Selection.AutoSelect
	result.Selection.ActivateTab = value.Selection.ActivateTab
	result.Selection.Persist = value.Selection.Persist
	result.Policy.AllowedOrigins = append([]string(nil), value.Policy.AllowedOrigins...)
	result.Policy.DeniedOrigins = append([]string(nil), value.Policy.DeniedOrigins...)
	result.Policy.Approval = value.Policy.Approval
	result.Policy.CancelOnInterrupt = value.Policy.CancelOnInterrupt
	result.Limits.InvocationTimeout = value.Limits.InvocationTimeout
	result.Limits.MaxInputBytes = value.Limits.MaxInputBytes
	result.Limits.MaxResultBytes = value.Limits.MaxResultBytes
	result.Limits.SerializePerTarget = value.Limits.SerializePerTarget
	result.Recording.Enabled = value.Recording.Enabled
	result.Recording.IncludeArguments = value.Recording.IncludeArguments
	result.Recording.IncludeResults = value.Recording.IncludeResults
	result.Recording.RedactURLQuery = value.Recording.RedactURLQuery
	result.Recording.RedactURLFragment = value.Recording.RedactURLFragment
	result.Replay.Path = value.Replay.Path
	result.Replay.Strict = value.Replay.Strict
	return result
}
