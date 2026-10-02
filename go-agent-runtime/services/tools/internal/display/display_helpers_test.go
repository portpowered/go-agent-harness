package display

// ScreenToolErrorResult creates the pixel-free result sent when the screen
// boundary is denied, unavailable, canceled, or otherwise fails. The direct
// ScreenTool contract still returns the original typed Go error; session
// adapters use this envelope when they need to keep the session alive.
func ScreenToolErrorResult(err error) string {
	return encodeScreenToolErrorResult(err, true)
}
