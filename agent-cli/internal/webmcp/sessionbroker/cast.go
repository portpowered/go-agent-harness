package sessionbroker

import (
	"context"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

// Unsupported-extension messages and the shared reason value.
const (
	unsupportedOperation = "unsupported_operation"
	castUnsupported      = "the connected browser does not support Cast controls"
	openTabUnsupported   = "The connected browser cannot open a new tab."
)

// castUnsupportedError reports a delegate without the requested Cast control.
func castUnsupportedError(message, phase string) error {
	return webmcp.NewClassifiedError(webmcp.ErrorBrowserProtocol, message, map[string]any{"phase": phase, "reason_code": unsupportedOperation})
}

// tabUnsupportedError reports a delegate without the requested tab operation.
func tabUnsupportedError(message, phase string) error {
	return webmcp.NewClassifiedError(webmcp.ErrorBrowserProtocol, message, map[string]any{
		"phase":  phase,
		"reason": unsupportedOperation,
	})
}

// ListCastDevices forwards the Cast extension after the capability bootstrap.
func (b *Broker) ListCastDevices(ctx context.Context) ([]webmcp.CastDevice, error) {
	if err := b.ensureInitialized(ctx); err != nil {
		return nil, err
	}
	controller, ok := b.Broker.(webmcp.BrokerCastController)
	if !ok {
		return nil, castUnsupportedError(castUnsupported, "list_cast_devices")
	}
	return controller.ListCastDevices(ctx)
}

// CastSelectedTab forwards the Cast extension after the capability bootstrap.
func (b *Broker) CastSelectedTab(ctx context.Context, deviceName string) error {
	if err := b.ensureInitialized(ctx); err != nil {
		return err
	}
	controller, ok := b.Broker.(webmcp.BrokerCastController)
	if !ok {
		return castUnsupportedError(castUnsupported, "cast_tab")
	}
	return controller.CastSelectedTab(ctx, deviceName)
}

// CastSelectedMedia forwards native media casting after the capability
// bootstrap.
func (b *Broker) CastSelectedMedia(ctx context.Context, deviceName string) error {
	if err := b.ensureInitialized(ctx); err != nil {
		return err
	}
	controller, ok := b.Broker.(webmcp.BrokerMediaCastController)
	if !ok {
		return castUnsupportedError("the connected browser does not support native media casting", "cast_media")
	}
	return controller.CastSelectedMedia(ctx, deviceName)
}

// StopCasting forwards the Cast extension after the capability bootstrap.
func (b *Broker) StopCasting(ctx context.Context, deviceName string) error {
	if err := b.ensureInitialized(ctx); err != nil {
		return err
	}
	controller, ok := b.Broker.(webmcp.BrokerCastController)
	if !ok {
		return castUnsupportedError(castUnsupported, "stop_casting")
	}
	return controller.StopCasting(ctx, deviceName)
}
