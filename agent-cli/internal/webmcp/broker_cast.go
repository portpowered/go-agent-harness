package webmcp

import (
	"context"
	"strings"
)

// ListCastDevices returns receivers observed by Chrome for the exact selected
// page. The dispatch lock prevents a concurrent selection change from moving
// discovery to a different tab halfway through the call.
func (b *StatefulBroker) ListCastDevices(ctx context.Context) ([]CastDevice, error) {
	var devices []CastDevice
	err := b.withSelectedCastController(ctx, "list_cast_devices", func(controller TargetCastController) error {
		var err error
		devices, err = controller.ListCastDevices(ctx)
		return err
	})
	if err != nil {
		return nil, err
	}
	return append(make([]CastDevice, 0, len(devices)), devices...), nil
}

// CastSelectedTab starts tab mirroring from the exact current selection.
func (b *StatefulBroker) CastSelectedTab(ctx context.Context, deviceName string) error {
	if strings.TrimSpace(deviceName) == "" {
		return classified(ErrorInvalidToolInput, "the Cast device name is required", map[string]any{"phase": "cast_tab", "reason_code": "device_name_required"}, nil)
	}
	return b.withSelectedCastController(ctx, "cast_tab", func(controller TargetCastController) error {
		return controller.CastTab(ctx, deviceName)
	})
}

// CastSelectedMedia asks the exact selected page to hand its active media to
// the named receiver. Unlike CastSelectedTab, this is page-initiated native
// playback rather than pixel mirroring.
func (b *StatefulBroker) CastSelectedMedia(ctx context.Context, deviceName string) error {
	if strings.TrimSpace(deviceName) == "" {
		return classified(ErrorInvalidToolInput, "the Cast device name is required", map[string]any{"phase": "cast_media", "reason_code": "device_name_required"}, nil)
	}
	return b.withSelectedMediaCastController(ctx, "cast_media", func(controller TargetMediaCastController) error {
		return controller.CastMedia(ctx, deviceName)
	})
}

// StopCasting terminates the route on the named receiver.
func (b *StatefulBroker) StopCasting(ctx context.Context, deviceName string) error {
	if strings.TrimSpace(deviceName) == "" {
		return classified(ErrorInvalidToolInput, "the Cast device name is required", map[string]any{"phase": "stop_casting", "reason_code": "device_name_required"}, nil)
	}
	return b.withSelectedCastController(ctx, "stop_casting", func(controller TargetCastController) error {
		return controller.StopCasting(ctx, deviceName)
	})
}

func (b *StatefulBroker) withSelectedCastController(ctx context.Context, phase string, call func(TargetCastController) error) error {
	return b.withSelectedSession(ctx, phase, func(session TargetSession) error {
		controller, ok := session.(TargetCastController)
		if !ok {
			return classified(ErrorBrowserProtocol, "the selected browser page does not support Cast controls", map[string]any{"phase": phase, "reason_code": "unsupported_operation"}, nil)
		}
		return call(controller)
	})
}

func (b *StatefulBroker) withSelectedSession(ctx context.Context, phase string, call func(TargetSession) error) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if b == nil {
		return ErrClosed
	}
	b.flushSelected()
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return ErrClosed
	}
	selected := b.selected
	b.mu.Unlock()
	if selected == nil {
		return staleSelectionError("", "", 0, "selection_not_connected")
	}

	selected.dispatchMu.Lock()
	defer selected.dispatchMu.Unlock()
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return ErrClosed
	}
	if b.selected != selected {
		err := staleSelectionForSession(selected, "selection_changed")
		b.mu.Unlock()
		return err
	}
	if err := b.captureSelectionStateErrorLocked(selected, phase, "selection_not_connected"); err != nil {
		b.mu.Unlock()
		return err
	}
	session := selected.session
	b.mu.Unlock()

	if err := call(session); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return ErrClosed
	}
	if b.selected != selected {
		return staleSelectionForSession(selected, "selection_changed")
	}
	return b.captureSelectionStateErrorLocked(selected, phase, "selection_changed")
}

func (b *StatefulBroker) withSelectedMediaCastController(ctx context.Context, phase string, call func(TargetMediaCastController) error) error {
	return b.withSelectedSession(ctx, phase, func(session TargetSession) error {
		controller, ok := session.(TargetMediaCastController)
		if !ok {
			return classified(ErrorBrowserProtocol, "the selected browser page does not support native media casting", map[string]any{"phase": phase, "reason_code": "unsupported_operation"}, nil)
		}
		return call(controller)
	})
}

var _ BrokerCastController = (*StatefulBroker)(nil)
