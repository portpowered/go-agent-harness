package room

import runtimeRooms "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"

const (
	BrowserToolsBackendWebMCP        = runtimeRooms.BrowserToolsBackendWebMCP
	BrowserAutoSelectOff             = runtimeRooms.BrowserAutoSelectOff
	BrowserAutoSelectSingle          = runtimeRooms.BrowserAutoSelectSingle
	BrowserAutoSelectPersisted       = runtimeRooms.BrowserAutoSelectPersisted
	BrowserApprovalAlways            = runtimeRooms.BrowserApprovalAlways
	BrowserApprovalWrites            = runtimeRooms.BrowserApprovalWrites
	BrowserApprovalNever             = runtimeRooms.BrowserApprovalNever
	BrowserCancelOnInterruptNever    = runtimeRooms.BrowserCancelOnInterruptNever
	BrowserCancelOnInterruptReadOnly = runtimeRooms.BrowserCancelOnInterruptReadOnly
	BrowserCancelOnInterruptAlways   = runtimeRooms.BrowserCancelOnInterruptAlways
)

var (
	ErrInvalidBrowserTools            = runtimeRooms.ErrInvalidBrowserTools
	ErrUnsupportedBrowserToolsBackend = runtimeRooms.ErrUnsupportedBrowserToolsBackend
	ErrInvalidBrowserToolsOption      = runtimeRooms.ErrInvalidBrowserToolsOption
	ErrInvalidBrowserEndpoint         = runtimeRooms.ErrInvalidBrowserEndpoint
)

type BrowserToolsConfig = runtimeRooms.BrowserToolsConfig
type BrowserConnectionConfig = runtimeRooms.BrowserConnectionConfig
type BrowserSelectionConfig = runtimeRooms.BrowserSelectionConfig
type BrowserPolicyConfig = runtimeRooms.BrowserPolicyConfig
type BrowserLimitsConfig = runtimeRooms.BrowserLimitsConfig
type BrowserRecordingConfig = runtimeRooms.BrowserRecordingConfig
type BrowserReplayConfig = runtimeRooms.BrowserReplayConfig

// DefaultBrowserToolsConfig retains the CLI helper while using runtime's
// canonical, configuration-independent defaults.
func DefaultBrowserToolsConfig() BrowserToolsConfig {
	return runtimeRooms.BrowserToolsDefaults{}.Config()
}
