package chrome

import (
	"context"
	"sort"
	"strings"
	"time"

	cdpCast "github.com/chromedp/cdproto/cast"
	"github.com/chromedp/chromedp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

const castDiscoveryWait = 3 * time.Second

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
	s.castSinksKnown = true
	if s.castUpdate != nil {
		close(s.castUpdate)
	}
	s.castUpdate = make(chan struct{})
	s.mu.Unlock()
	return true
}

func (s *targetSession) ListCastDevices(ctx context.Context) ([]webmcp.CastDevice, error) {
	s.mu.Lock()
	update := s.castUpdate
	known := s.castSinksKnown
	s.mu.Unlock()
	err := s.run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		s.recordWireBeforeDispatch(webmcp.CastEnableMethod, "")
		return cdpCast.Enable().Do(ctx)
	}))
	if err != nil {
		return nil, classifySessionError(s, webmcp.ErrorBrowserProtocol, "list_cast_devices", err)
	}
	if !known {
		timer := time.NewTimer(castDiscoveryWait)
		defer timer.Stop()
		select {
		case <-update:
		case <-timer.C:
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-s.done:
			return nil, webmcp.ErrClosed
		}
	}
	s.mu.Lock()
	devices := append(make([]webmcp.CastDevice, 0, len(s.castSinks)), s.castSinks...)
	issue := strings.TrimSpace(s.castIssue)
	s.mu.Unlock()
	if len(devices) == 0 && issue != "" {
		return nil, webmcp.NewClassifiedError(webmcp.ErrorBrowserProtocol, "Chrome could not discover Cast devices.", map[string]any{"phase": "list_cast_devices", "reason_code": "cast_issue", "issue": issue})
	}
	return devices, nil
}

func (s *targetSession) CastTab(ctx context.Context, deviceName string) error {
	deviceName = strings.TrimSpace(deviceName)
	if deviceName == "" {
		return webmcp.NewClassifiedError(webmcp.ErrorInvalidToolInput, "the Cast device name is required", map[string]any{"phase": "cast_tab", "reason_code": "device_name_required"})
	}
	err := s.run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		s.recordWireBeforeDispatch(webmcp.CastStartTabMirroringMethod, "")
		return cdpCast.StartTabMirroring(deviceName).Do(ctx)
	}))
	if err != nil {
		return classifySessionError(s, webmcp.ErrorBrowserProtocol, "cast_tab", err)
	}
	return nil
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
