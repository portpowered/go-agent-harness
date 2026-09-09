// Package probe contains source-only dependency-check fixtures for C25.
package probe

import (
	"time"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
)

// ForbiddenRoomGraph is a negative control. It models exactly the bypass
// shape found in the legacy CLI room implementation: host ticker, local PCM
// codec, and direct device gateway construction in room code.
func ForbiddenRoomGraph(registry devicegw.DeviceRegistry, pcm []int16) {
	_ = time.NewTicker(time.Millisecond)
	_ = codec.EncodePCM16(pcm)
	_, _ = devicegw.NewDeviceSink(registry, "speaker")
}
