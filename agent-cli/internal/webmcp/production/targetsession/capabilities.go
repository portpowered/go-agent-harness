package targetsession

import (
	"context"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

const (
	detailPhase              = "phase"
	detailReasonCode         = "reason_code"
	reasonUnsupportedOp      = "unsupported_operation"
	messageCastUnsupported   = "the selected browser page does not support Cast controls"
	messageMediaUnsupported  = "the selected browser page does not support native media casting"
	messageNavigateUnsupport = "the selected browser page does not support in-place navigation"
)

var (
	_ webmcp.TargetSession             = (*Session)(nil)
	_ webmcp.PageScreenshotter         = (*Session)(nil)
	_ webmcp.TargetCastController      = (*Session)(nil)
	_ webmcp.TargetMediaCastController = (*Session)(nil)
	_ webmcp.TargetTabNavigator        = (*Session)(nil)
	_ webmcp.PageFocusLeaser           = (*Session)(nil)
)

func unsupported(message, phase string) error {
	return webmcp.NewClassifiedError(webmcp.ErrorBrowserProtocol, message, map[string]any{detailPhase: phase, detailReasonCode: reasonUnsupportedOp})
}

// CapturePageScreenshot captures the raw page and remaps the result onto the
// public browser/target identity.
func (s *Session) CapturePageScreenshot(ctx context.Context) (webmcp.PageScreenshot, error) {
	if s == nil || s.raw == nil {
		return webmcp.PageScreenshot{}, webmcp.ErrClosed
	}
	capturer, ok := s.raw.(webmcp.PageScreenshotter)
	if !ok {
		return webmcp.PageScreenshot{}, webmcp.NewClassifiedError(
			webmcp.ErrorUnsupportedWebMCP,
			"the selected browser page does not support screenshot capture",
			map[string]any{"capability": webmcp.PageCaptureScreenshotMethod},
		)
	}
	screenshot, err := capturer.CapturePageScreenshot(ctx)
	if err != nil {
		return webmcp.PageScreenshot{}, err
	}
	// The raw Chrome adapter reports the protocol target ID. This bridge owns
	// the public discovery identity, so remap the successful capture before it
	// reaches the neutral broker's exact-selection check.
	screenshot.BrowserID = s.target.BrowserID
	screenshot.TargetID = s.target.ID
	screenshot.Bytes = append([]byte(nil), screenshot.Bytes...)
	return screenshot, nil
}

// ListCastDevices forwards Cast discovery to the raw session.
func (s *Session) ListCastDevices(ctx context.Context) ([]webmcp.CastDevice, error) {
	if s == nil || s.raw == nil {
		return nil, webmcp.ErrClosed
	}
	controller, ok := s.raw.(webmcp.TargetCastController)
	if !ok {
		return nil, unsupported(messageCastUnsupported, "list_cast_devices")
	}
	devices, err := controller.ListCastDevices(ctx)
	return append(make([]webmcp.CastDevice, 0, len(devices)), devices...), err
}

// CastTab forwards tab casting to the raw session.
func (s *Session) CastTab(ctx context.Context, deviceName string) error {
	if s == nil || s.raw == nil {
		return webmcp.ErrClosed
	}
	controller, ok := s.raw.(webmcp.TargetCastController)
	if !ok {
		return unsupported(messageCastUnsupported, "cast_tab")
	}
	return controller.CastTab(ctx, deviceName)
}

// CastMedia forwards native media casting to the raw session.
func (s *Session) CastMedia(ctx context.Context, deviceName string) error {
	if s == nil || s.raw == nil {
		return webmcp.ErrClosed
	}
	controller, ok := s.raw.(webmcp.TargetMediaCastController)
	if !ok {
		return unsupported(messageMediaUnsupported, "cast_media")
	}
	return controller.CastMedia(ctx, deviceName)
}

// AcquirePageFocus forwards a bounded focus lease to the raw session.
func (s *Session) AcquirePageFocus(ctx context.Context) (func(context.Context) error, error) {
	if s == nil || s.raw == nil {
		return nil, webmcp.ErrClosed
	}
	controller, ok := s.raw.(webmcp.PageFocusLeaser)
	if !ok {
		return nil, webmcp.NewClassifiedError(webmcp.ErrorBrowserProtocol, "selected page does not support bounded focus", nil)
	}
	return controller.AcquirePageFocus(ctx)
}

// StopCasting forwards Cast shutdown to the raw session.
func (s *Session) StopCasting(ctx context.Context, deviceName string) error {
	if s == nil || s.raw == nil {
		return webmcp.ErrClosed
	}
	controller, ok := s.raw.(webmcp.TargetCastController)
	if !ok {
		return unsupported(messageCastUnsupported, "stop_casting")
	}
	return controller.StopCasting(ctx, deviceName)
}

// NavigateTab preserves the raw target's optional in-place navigation
// capability through the production identity/event bridge.
func (s *Session) NavigateTab(ctx context.Context, targetURL string) error {
	if s == nil || s.raw == nil {
		return webmcp.ErrClosed
	}
	navigator, ok := s.raw.(webmcp.TargetTabNavigator)
	if !ok {
		return unsupported(messageNavigateUnsupport, "navigate_tab")
	}
	if err := navigator.NavigateTab(ctx, targetURL); err != nil {
		return err
	}
	// Navigation events cross this production identity bridge before the
	// broker consumes them. Synchronize that forwarding hop so the successful
	// tool result reports the new URL/generation rather than stale target
	// metadata from before the navigation.
	return s.flushEvents(ctx)
}
