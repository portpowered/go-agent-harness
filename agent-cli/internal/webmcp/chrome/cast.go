package chrome

import (
	"context"
	"sort"
	"strings"
	"time"

	cdpCast "github.com/chromedp/cdproto/cast"
	cdpRuntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

const castActiveMediaExpression = `(async () => {
  const videos = Array.from(document.querySelectorAll("video"));
  const media = videos.find((item) => {
    const rect = item.getBoundingClientRect();
    return rect.width > 0 && rect.height > 0;
  }) || videos[0] || document.querySelector("audio");
  let remoteError = "";
  if (media && media.remote && typeof media.remote.prompt === "function") {
    try {
      await media.remote.prompt();
      return { route: "remote_playback", state: media.remote.state || "unknown" };
    } catch (error) {
      remoteError = error && error.message ? error.message : String(error);
    }
  }
  const castButton = document.querySelector(".ytp-remote-button, .ytp-cast-button");
  if (castButton instanceof HTMLElement && castButton.getAttribute("aria-disabled") !== "true") {
    castButton.click();
    return { route: "page_cast_control", state: "requested" };
  }
  const detail = remoteError ? ": " + remoteError : "";
  throw new Error("The selected page has no usable native media Cast route" + detail);
})()`

const (
	castDiscoveryWait       = 3 * time.Second
	castDiscoverySettleWait = 500 * time.Millisecond
)

// observeCastProtocolEvent owns Cast's asynchronous sink snapshot. These
// events are operational state for the cast tools, not WebMCP page events, so
// they deliberately do not enter the broker recording/catalog stream.
func (s *targetSession) observeCastProtocolEvent(event any) bool {
	var sinks []*cdpCast.Sink
	switch value := event.(type) {
	case *cdpCast.EventSinksUpdated:
		if value != nil {
			sinks = value.Sinks
		}
	case cdpCast.EventSinksUpdated:
		sinks = value.Sinks
	case *cdpCast.EventIssueUpdated:
		s.mu.Lock()
		if value != nil {
			s.castIssue = value.IssueMessage
		}
		s.mu.Unlock()
		return true
	case cdpCast.EventIssueUpdated:
		s.mu.Lock()
		s.castIssue = value.IssueMessage
		s.mu.Unlock()
		return true
	default:
		return false
	}

	devices := make([]webmcp.CastDevice, 0, len(sinks))
	for _, sink := range sinks {
		if sink == nil || strings.TrimSpace(sink.Name) == "" {
			continue
		}
		devices = append(devices, webmcp.CastDevice{Name: sink.Name, ID: sink.ID, Session: sink.Session})
	}
	sort.Slice(devices, func(left, right int) bool {
		if devices[left].Name == devices[right].Name {
			return devices[left].ID < devices[right].ID
		}
		return devices[left].Name < devices[right].Name
	})
	s.mu.Lock()
	s.castSinks = devices
	if s.castUpdate != nil {
		close(s.castUpdate)
	}
	s.castUpdate = make(chan struct{})
	s.mu.Unlock()
	return true
}

func (s *targetSession) ListCastDevices(ctx context.Context) ([]webmcp.CastDevice, error) {
	err := s.run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		s.recordWireBeforeDispatch(webmcp.CastEnableMethod, "")
		return cdpCast.Enable().Do(ctx)
	}))
	if err != nil {
		return nil, classifySessionError(s, webmcp.ErrorBrowserProtocol, "list_cast_devices", err)
	}
	timer := time.NewTimer(castDiscoveryWait)
	defer timer.Stop()
	var settleTimer *time.Timer
	var settle <-chan time.Time
	stopSettleTimer := func() {
		if settleTimer == nil {
			return
		}
		if !settleTimer.Stop() {
			select {
			case <-settleTimer.C:
			default:
			}
		}
		settleTimer = nil
		settle = nil
	}
	defer stopSettleTimer()
	for {
		s.mu.Lock()
		devices := append(make([]webmcp.CastDevice, 0, len(s.castSinks)), s.castSinks...)
		issue := strings.TrimSpace(s.castIssue)
		update := s.castUpdate
		s.mu.Unlock()
		if issue != "" {
			return nil, webmcp.NewClassifiedError(webmcp.ErrorBrowserProtocol, "Chrome could not discover Cast devices.", map[string]any{"phase": "list_cast_devices", "reason_code": "cast_issue", "issue": issue})
		}
		if len(devices) > 0 && settleTimer == nil {
			settleTimer = time.NewTimer(castDiscoverySettleWait)
			settle = settleTimer.C
		}
		select {
		case <-update:
			stopSettleTimer()
		case <-settle:
			select {
			case <-update:
				stopSettleTimer()
				continue
			default:
			}
			return devices, nil
		case <-timer.C:
			return devices, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-s.done:
			return nil, webmcp.ErrClosed
		}
	}
}

func (s *targetSession) CastTab(ctx context.Context, deviceName string) error {
	deviceName, err := s.requireCastDevice(ctx, deviceName, "cast_tab")
	if err != nil {
		return err
	}
	// Models may call cast_tab directly when the customer already named a
	// receiver. Chrome requires Cast.enable before StartTabMirroring, so make
	// the mutation independently usable instead of relying on list_devices as
	// an undocumented ordering prerequisite.
	err = s.run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		s.recordWireBeforeDispatch(webmcp.CastStartTabMirroringMethod, "")
		return cdpCast.StartTabMirroring(deviceName).Do(ctx)
	}))
	if err != nil {
		return classifySessionError(s, webmcp.ErrorBrowserProtocol, "cast_tab", err)
	}
	return nil
}

// CastMedia selects the receiver for the page's next Cast/Remote Playback
// request, then asks its active media element to hand playback off natively.
// YouTube's visible Cast control is a fallback for pages whose player uses the
// Cast SDK instead of exposing HTMLMediaElement.remote.
func (s *targetSession) CastMedia(ctx context.Context, deviceName string) error {
	deviceName, err := s.requireCastDevice(ctx, deviceName, "cast_media")
	if err != nil {
		return err
	}
	err = s.run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		s.recordWireBeforeDispatch(webmcp.CastSetSinkToUseMethod, "")
		if err := cdpCast.SetSinkToUse(deviceName).Do(ctx); err != nil {
			return err
		}
		_, exception, err := cdpRuntime.Evaluate(castActiveMediaExpression).
			WithAwaitPromise(true).
			WithReturnByValue(true).
			WithUserGesture(true).
			Do(ctx)
		if err != nil {
			return err
		}
		if exception != nil {
			return exception
		}
		return nil
	}))
	if err != nil {
		return classifySessionError(s, webmcp.ErrorBrowserProtocol, "cast_media", err)
	}
	return nil
}

func (s *targetSession) requireCastDevice(ctx context.Context, deviceName, phase string) (string, error) {
	deviceName = strings.TrimSpace(deviceName)
	if deviceName == "" {
		return "", webmcp.NewClassifiedError(webmcp.ErrorInvalidToolInput, "the Cast device name is required", map[string]any{"phase": phase, "reason_code": "device_name_required"})
	}
	devices, err := s.ListCastDevices(ctx)
	if err != nil {
		return "", err
	}
	for _, device := range devices {
		if device.Name == deviceName {
			return deviceName, nil
		}
	}
	return "", webmcp.NewClassifiedError(webmcp.ErrorInvalidToolInput, "the requested Cast device is not available", map[string]any{"phase": phase, "reason_code": "device_not_found", "device_name": deviceName})
}

func (s *targetSession) StopCasting(ctx context.Context, deviceName string) error {
	deviceName = strings.TrimSpace(deviceName)
	if deviceName == "" {
		return webmcp.NewClassifiedError(webmcp.ErrorInvalidToolInput, "the Cast device name is required", map[string]any{"phase": "stop_casting", "reason_code": "device_name_required"})
	}
	err := s.run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		s.recordWireBeforeDispatch(webmcp.CastStopCastingMethod, "")
		return cdpCast.StopCasting(deviceName).Do(ctx)
	}))
	if err != nil {
		return classifySessionError(s, webmcp.ErrorBrowserProtocol, "stop_casting", err)
	}
	return nil
}

var _ webmcp.TargetCastController = (*targetSession)(nil)
var _ webmcp.TargetMediaCastController = (*targetSession)(nil)
