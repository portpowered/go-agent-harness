package webmcp

import (
	"context"
	"strings"
)

const capturePagePhase = "capture_page"

// CapturePageScreenshot captures the currently selected page through the
// existing target session. The selection's dispatch lock remains held from
// the final lifecycle check through the adapter call, so a late detach or
// replacement cannot make this operation silently target another page.
func (b *StatefulBroker) CapturePageScreenshot(ctx context.Context) (PageScreenshot, error) {
	if err := contextError(ctx); err != nil {
		return PageScreenshot{}, err
	}
	if b == nil {
		return PageScreenshot{}, ErrClosed
	}
	b.flushSelected()

	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return PageScreenshot{}, ErrClosed
	}
	selected := b.selected
	b.mu.Unlock()
	if selected == nil {
		return PageScreenshot{}, staleSelectionError("", "", 0, "selection_not_connected")
	}

	selected.dispatchMu.Lock()
	defer selected.dispatchMu.Unlock()

	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return PageScreenshot{}, ErrClosed
	}
	if b.selected != selected {
		err := staleSelectionError(selected.context.Key.BrowserID, selected.context.Key.TargetID, selected.context.Generation, "selection_changed")
		b.mu.Unlock()
		return PageScreenshot{}, err
	}
	if err := b.captureSelectionStateErrorLocked(selected, capturePagePhase, "selection_not_connected"); err != nil {
		b.mu.Unlock()
		return PageScreenshot{}, err
	}
	target := selected.target
	session := selected.session
	b.mu.Unlock()

	capturer, err := pageScreenshotCapturer(target, session)
	if err != nil {
		return PageScreenshot{}, err
	}

	screenshot, err := capturer.CapturePageScreenshot(ctx)
	if captureErr := contextError(ctx); captureErr != nil {
		return PageScreenshot{}, captureErr
	}
	if err != nil {
		return PageScreenshot{}, b.pageCaptureFailure(selected, session, err)
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	return b.finishPageScreenshotLocked(selected, screenshot)
}

func pageScreenshotCapturer(target Target, session TargetSession) (PageScreenshotter, error) {
	if !strings.EqualFold(strings.TrimSpace(target.Type), "page") {
		return nil, classified(ErrorUnsupportedWebMCP, "the selected browser target does not support page capture", map[string]any{
			"browser_id":  string(target.BrowserID),
			"target_id":   string(target.ID),
			"phase":       capturePagePhase,
			"target_type": strings.ToLower(strings.TrimSpace(target.Type)),
		}, nil)
	}
	capturer, ok := session.(PageScreenshotter)
	if !ok {
		return nil, classified(ErrorUnsupportedWebMCP, "the selected browser page does not support screenshot capture", map[string]any{
			"browser_id": string(target.BrowserID),
			"target_id":  string(target.ID),
			"phase":      capturePagePhase,
			"capability": PageCaptureScreenshotMethod,
		}, nil)
	}
	return capturer, nil
}

// pageCaptureFailure classifies an adapter capture error. A neutral adapter
// may report a transport error before its lifecycle event reaches the broker
// loop. Preserve that stronger classification at the capture boundary when
// the session already knows the cause.
func (b *StatefulBroker) pageCaptureFailure(selected *brokerSession, session TargetSession, err error) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.selected != selected {
		return err
	}
	if failure := sessionLifecycleFailure(selected); failure != nil {
		return failure
	}
	if isBrowserEndpointLossError(session.Err()) {
		b.invalidateSessionWithCodeLocked(selected, ErrorBrowserDisconnected, capturePagePhase)
		return browserDisconnectedErrorForSession(selected, capturePagePhase, session.Err())
	}
	return err
}

func (b *StatefulBroker) finishPageScreenshotLocked(selected *brokerSession, screenshot PageScreenshot) (PageScreenshot, error) {
	if b.closed {
		return PageScreenshot{}, ErrClosed
	}
	if b.selected != selected {
		return PageScreenshot{}, staleSelectionForSession(selected, "selection_changed")
	}
	if err := b.captureSelectionStateErrorLocked(selected, capturePagePhase, "selection_changed"); err != nil {
		return PageScreenshot{}, err
	}
	if screenshot.BrowserID != "" && screenshot.BrowserID != selected.context.Key.BrowserID {
		return PageScreenshot{}, classified(ErrorBrowserProtocol, "the browser returned a screenshot for a different browser", map[string]any{
			"phase":       capturePagePhase,
			"reason_code": "browser_id_mismatch",
		}, nil)
	}
	if screenshot.TargetID != "" && screenshot.TargetID != selected.context.Key.TargetID {
		return PageScreenshot{}, classified(ErrorBrowserProtocol, "the browser returned a screenshot for a different target", map[string]any{
			"phase":       capturePagePhase,
			"reason_code": "target_id_mismatch",
		}, nil)
	}
	screenshot.BrowserID = selected.context.Key.BrowserID
	screenshot.TargetID = selected.context.Key.TargetID
	screenshot.Bytes = append([]byte(nil), screenshot.Bytes...)
	return screenshot, nil
}

func (b *StatefulBroker) captureSelectionStateErrorLocked(selected *brokerSession, phase, reason string) error {
	if selected == nil {
		return staleSelectionForSession(nil, reason)
	}
	if selected.invalidatedCode == ErrorBrowserDisconnected {
		return browserDisconnectedErrorForSession(selected, phase, nil)
	}
	if selected.invalidatedCode == ErrorTargetDetached {
		return classified(ErrorTargetDetached, "the selected browser target is closed", map[string]any{
			"browser_id": string(selected.context.Key.BrowserID),
			"target_id":  string(selected.context.Key.TargetID),
			"phase":      safeBrokerPhase(phase),
			"reason":     selected.invalidatedReason,
		}, nil)
	}
	if cause := selected.session.Err(); isBrowserEndpointLossError(cause) {
		b.invalidateSessionWithCodeLocked(selected, ErrorBrowserDisconnected, phase)
		return browserDisconnectedErrorForSession(selected, phase, cause)
	}
	if failure := sessionLifecycleFailure(selected); failure != nil {
		if isLifecycleClassifiedError(failure) {
			return failure
		}
	}
	if !selected.active || !selected.context.Connected {
		return staleSelectionForSession(selected, reason)
	}
	return nil
}
