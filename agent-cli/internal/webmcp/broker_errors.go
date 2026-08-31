package webmcp

func staleSelectionError(browserID BrowserID, targetID TargetID, generation uint64, reason string) error {
	return classified(ErrorStaleSelection, "the selected browser target is no longer current", map[string]any{
		"browser_id":          string(browserID),
		"target_id":           string(targetID),
		"selected_generation": generation,
		"reason":              reason,
	}, ErrStaleSelection)
}

func classified(code ErrorCode, message string, details map[string]any, cause error) error {
	err := NewClassifiedError(code, message, details)
	err.Cause = cause
	return err
}
