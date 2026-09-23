package livehost

import (
	"context"

	runtimeDevices "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
)

// BindRTC is the stateless host-to-service seam for an admitted RTC request.
// Device selection, media workers and shutdown remain service-owned.
func BindRTC(ctx context.Context, service runtimeDevices.Service, request runtimeDevices.RTCBindingRequest) (runtimeDevices.RTCBinding, error) {
	return service.BindRTC(ctx, request)
}
