package tools

// Stable names and result markers for physical and browser-page sight. These
// values are part of the public tool contract; the display implementation and
// platform adapters remain private to the tools service.
const (
	ScreenToolID          = "show"
	HostDisplayToolID     = "show_screen"
	PhysicalDisplayToolID = HostDisplayToolID
	PageSightToolID       = "show_page"

	ScreenRecordingPermissionDeniedErrorCode = "screen_recording_permission_denied"
)
