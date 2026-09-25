package config

import runtimeRooms "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"

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
