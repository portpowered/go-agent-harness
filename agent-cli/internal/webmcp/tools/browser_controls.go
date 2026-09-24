package tools

import (
	"context"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

// Browser controls are optional broker extensions. Each executor fails closed
// with a classified browser_protocol error when the connected broker does not
// implement the corresponding capability.

func (s *BrokerToolSet) executeOpenTab(ctx context.Context, args map[string]any) ([]byte, error) {
	opener, ok := s.broker.(webmcp.BrokerTabOpener)
	if !ok {
		return unsupportedBrowserOperation("The connected browser cannot open a new tab.", "open_tab")
	}
	selected, err := opener.OpenTab(ctx, webmcp.OpenTabRequest{
		BrowserID: webmcp.BrowserID(stringValue(args, "browser_id")),
		URL:       stringValue(args, "url"),
		Activate:  boolValueDefault(args, "activate", true),
	})
	if err != nil {
		return browserProtocolFailure(err, "open_tab")
	}
	return webmcp.EncodeToolResult(selectionDataFrom(selected), nil)
}

func (s *BrokerToolSet) executeNavigateTab(ctx context.Context, args map[string]any) ([]byte, error) {
	navigator, ok := s.broker.(webmcp.BrokerTabNavigator)
	if !ok {
		return unsupportedBrowserOperation("The connected browser cannot navigate the selected tab.", "navigate_tab")
	}
	selected, err := navigator.NavigateSelectedTab(ctx, stringValue(args, "url"))
	if err != nil {
		return browserProtocolFailure(err, "navigate_tab")
	}
	return webmcp.EncodeToolResult(navigationData{
		contextData: contextDataFrom(selected),
		Status:      "navigated",
	}, nil)
}

func (s *BrokerToolSet) executeListCastDevices(ctx context.Context) ([]byte, error) {
	controller, ok := s.broker.(webmcp.BrokerCastController)
	if !ok {
		return unsupportedBrowserOperation(castUnsupportedMessage, "list_cast_devices")
	}
	devices, err := controller.ListCastDevices(ctx)
	if err != nil {
		return browserProtocolFailure(err, "list_cast_devices")
	}
	return webmcp.EncodeToolResult(castDevicesData{Devices: devices}, nil)
}

func (s *BrokerToolSet) executeCastTab(ctx context.Context, args map[string]any) ([]byte, error) {
	deviceName := stringValue(args, "device_name")
	mode := webmcp.CastMode(stringValue(args, "mode"))
	switch mode {
	case webmcp.CastModeTab:
		controller, ok := s.broker.(webmcp.BrokerCastController)
		if !ok {
			return unsupportedBrowserOperation(castUnsupportedMessage, "cast_tab")
		}
		if err := controller.CastSelectedTab(ctx, deviceName); err != nil {
			return browserProtocolFailure(err, "cast_tab")
		}
	case webmcp.CastModeMedia:
		controller, ok := s.broker.(webmcp.BrokerMediaCastController)
		if !ok {
			return unsupportedBrowserOperation("The connected browser does not support native media casting.", "cast_media")
		}
		if err := controller.CastSelectedMedia(ctx, deviceName); err != nil {
			return browserProtocolFailure(err, "cast_media")
		}
	}
	return webmcp.EncodeToolResult(castActionData{DeviceName: deviceName, Mode: mode, Status: "cast_started"}, nil)
}

func (s *BrokerToolSet) executeStopCasting(ctx context.Context, args map[string]any) ([]byte, error) {
	controller, ok := s.broker.(webmcp.BrokerCastController)
	if !ok {
		return unsupportedBrowserOperation(castUnsupportedMessage, "stop_casting")
	}
	deviceName := stringValue(args, "device_name")
	if err := controller.StopCasting(ctx, deviceName); err != nil {
		return browserProtocolFailure(err, "stop_casting")
	}
	return webmcp.EncodeToolResult(castActionData{DeviceName: deviceName, Status: "cast_stopped"}, nil)
}

const castUnsupportedMessage = "The connected browser does not support Cast controls."

// unsupportedBrowserOperation reports a browser control the connected broker
// does not implement, preserving the phase in both error layers.
func unsupportedBrowserOperation(message, phase string) ([]byte, error) {
	return brokerFailure(webmcp.NewClassifiedError(webmcp.ErrorBrowserProtocol, message, map[string]any{
		"phase":  phase,
		"reason": "unsupported_operation",
	}), webmcp.ErrorBrowserProtocol, map[string]any{"phase": phase})
}

func browserProtocolFailure(err error, phase string) ([]byte, error) {
	return brokerFailure(err, webmcp.ErrorBrowserProtocol, map[string]any{"phase": phase})
}

type castDevicesData struct {
	Devices []webmcp.CastDevice `json:"devices"`
}

type castActionData struct {
	DeviceName string          `json:"device_name"`
	Mode       webmcp.CastMode `json:"mode,omitempty"`
	Status     string          `json:"status"`
}

type navigationData struct {
	contextData
	Status string `json:"status"`
}
