package webmcp

func staleSelectionForSession(selected *brokerSession, reason string) error {
	if selected == nil {
		if reason == "selection_not_connected" {
			return noPageSelectedError()
		}
		return staleSelectionError("", "", 0, reason)
	}
	if failure := sessionLifecycleFailure(selected); failure != nil {
		return failure
	}
	return staleSelectionError(selected.context.Key.BrowserID, selected.context.Key.TargetID, selected.context.Generation, reason)
}

func noPageSelectedError() error {
	return classified(ErrorStaleSelection, "no page is selected", map[string]any{
		"browser_id":          "",
		"target_id":           "",
		"selected_generation": uint64(0),
		"reason":              "selection_not_connected",
	}, ErrStaleSelection)
}

// reconcileTargetLossLocked preserves the lifecycle outcome that raced with
// an invocation's final adapter call. A target can disappear before the
// broker's event loop acquires dispatchMu, so the adapter may only return a
// generic closed/stale error even though the session is already terminal.
func reconcileTargetLossLocked(invocation *brokerInvocation, cause error) error {
	if invocation == nil || invocation.selected == nil {
		return cause
	}
	// Preserve a classified adapter result when it already carries the
	// operation phase that observed the loss (for example, list_targets).
	// Session state is the fallback for adapters that only report ErrClosed or
	// return a stale-selection error after the terminal event raced the call.
	if _, ok := lifecycleClassifiedError(cause); ok {
		return cause
	}
	if failure := sessionLifecycleFailure(invocation.selected); failure != nil {
		return failure
	}
	if targetSessionEnded(invocation.selected.session) {
		return classified(ErrorInvocationOrphaned, DefaultErrorMessage(ErrorInvocationOrphaned), map[string]any{
			"invocation_id":     string(invocation.invocation.ID),
			"target_id":         string(invocation.invocation.Tool.TargetID),
			"generation":        invocation.invocation.Tool.Generation,
			"terminal_observed": false,
		}, cause)
	}
	return cause
}

func targetSessionEnded(session TargetSession) bool {
	if session == nil {
		return false
	}
	done := session.Done()
	if done == nil {
		return false
	}
	select {
	case <-done:
		return true
	default:
		return false
	}
}

func targetAttachError(selector TargetSelector, phase string, cause error) error {
	if _, lifecycle := lifecycleClassifiedError(cause); lifecycle {
		return cause
	}
	return classified(ErrorTargetAttachFailed, "the selected browser target could not be initialized", map[string]any{
		"browser_id":  string(selector.BrowserID),
		"target_id":   string(selector.TargetID),
		"phase":       phase,
		"reason_code": "attach_failed",
	}, cause)
}

func invocationFailureResultForError(invocation *brokerInvocation, err error, fallback ErrorCode) InvokeResult {
	code := errorCodeFor(err, fallback)
	details := classifiedDetails(err)
	if len(details) == 0 {
		details = map[string]any{
			"invocation_id": string(invocation.invocation.ID),
			"phase":         "dispatch",
		}
		if code == ErrorInvocationFailed || code == ErrorBrowserDisconnected {
			details["side_effect_unknown"] = true
		}
	}
	return invocationFailureResult(invocation, invocationStateForError(code), code, details)
}

func invocationStateForError(code ErrorCode) InvocationState {
	switch code {
	case ErrorInvocationCanceled:
		return InvocationCanceled
	case ErrorInvocationTimedOut:
		return InvocationTimedOut
	case ErrorInvocationOrphaned:
		return InvocationOrphaned
	default:
		return InvocationError
	}
}
