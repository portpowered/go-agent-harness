package config

import "time"

// BrowserOverrides contains only explicitly supplied command-line browser
// values. Pointer fields preserve the distinction between an omitted option
// and an explicit zero, false, empty string, or empty list.
type BrowserOverrides struct {
	ToolsBackend       *string
	CDPURL             *string
	WSEndpoint         *string
	UserDataDir        *string
	AllowProcessScan   *bool
	AllowRemoteCDP     *bool
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
	if overrides.CDPURL != nil {
		out.Connection.CDPURL = *overrides.CDPURL
	}
	if overrides.WSEndpoint != nil {
		out.Connection.WSEndpoint = *overrides.WSEndpoint
	}
	if overrides.UserDataDir != nil {
		out.Connection.UserDataDir = *overrides.UserDataDir
	}
	if overrides.AllowProcessScan != nil {
		out.Connection.AllowProcessScan = *overrides.AllowProcessScan
	}
	if overrides.AllowRemoteCDP != nil {
		out.Connection.AllowRemoteCDP = *overrides.AllowRemoteCDP
	}
	if overrides.Browser != nil {
		out.Selection.Browser = *overrides.Browser
	}
	if overrides.Tab != nil {
		out.Selection.Tab = *overrides.Tab
	}
	if overrides.Origin != nil {
		out.Selection.Origin = *overrides.Origin
	}
	if overrides.AutoSelect != nil {
		out.Selection.AutoSelect = *overrides.AutoSelect
	}
	if overrides.ActivateTab != nil {
		out.Selection.ActivateTab = *overrides.ActivateTab
	}
	if overrides.PersistSelection != nil {
		out.Selection.Persist = *overrides.PersistSelection
	}
	if overrides.AllowedOrigins != nil {
		out.Policy.AllowedOrigins = cloneBrowserStrings(*overrides.AllowedOrigins)
	}
	if overrides.DeniedOrigins != nil {
		out.Policy.DeniedOrigins = cloneBrowserStrings(*overrides.DeniedOrigins)
	}
	if overrides.Approval != nil {
		out.Policy.Approval = *overrides.Approval
	}
	if overrides.CancelOnInterrupt != nil {
		out.Policy.CancelOnInterrupt = *overrides.CancelOnInterrupt
	}
	if overrides.InvocationTimeout != nil {
		out.Limits.InvocationTimeout = *overrides.InvocationTimeout
	}
	if overrides.MaxInputBytes != nil {
		out.Limits.MaxInputBytes = *overrides.MaxInputBytes
	}
	if overrides.MaxResultBytes != nil {
		out.Limits.MaxResultBytes = *overrides.MaxResultBytes
	}
	if overrides.SerializePerTarget != nil {
		out.Limits.SerializePerTarget = *overrides.SerializePerTarget
	}
	if overrides.Record != nil {
		out.Recording.Enabled = *overrides.Record
	}
	if overrides.RecordArguments != nil {
		out.Recording.IncludeArguments = *overrides.RecordArguments
	}
	if overrides.RecordResults != nil {
		out.Recording.IncludeResults = *overrides.RecordResults
	}
	if overrides.RedactURLQuery != nil {
		out.Recording.RedactURLQuery = *overrides.RedactURLQuery
	}
	if overrides.RedactURLFragment != nil {
		out.Recording.RedactURLFragment = *overrides.RedactURLFragment
	}
	if overrides.Replay != nil {
		out.Replay.Path = *overrides.Replay
	}
	if overrides.ReplayStrict != nil {
		out.Replay.Strict = *overrides.ReplayStrict
	}

	if err := out.Validate(); err != nil {
		return BrowserConfig{}, err
	}
	return out, nil
}

func cloneBrowserStrings(values []string) []string {
	if values == nil {
		return nil
	}
	return append([]string{}, values...)
}
