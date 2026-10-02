package room

import runtimeRooms "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"

var (
	ErrUnsupportedBrowserToolsBackend = runtimeRooms.ErrUnsupportedBrowserToolsBackend
	ErrInvalidBrowserToolsOption      = runtimeRooms.ErrInvalidBrowserToolsOption
	ErrInvalidBrowserEndpoint         = runtimeRooms.ErrInvalidBrowserEndpoint
)

type BrowserToolsConfig = runtimeRooms.BrowserToolsConfig
