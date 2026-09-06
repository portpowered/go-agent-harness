package agentruntime

import (
	"context"
	"sync"

	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
)

type recordingRTCOutboundMedia struct {
	mu               sync.Mutex
	frames           []audio.PCMFrame
	writeErr         error
	cancelAfterFirst context.CancelFunc
}

func (m *recordingRTCOutboundMedia) WriteFrame(_ context.Context, frame audio.PCMFrame) error {
	m.mu.Lock()
	copyFrame := audio.PCMFrame{Samples: append([]int16(nil), frame.Samples...)}
	m.frames = append(m.frames, copyFrame)
	cancel := m.cancelAfterFirst
	if len(m.frames) == 1 {
		m.cancelAfterFirst = nil
	} else {
		cancel = nil
	}
	err := m.writeErr
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return err
}

func (m *recordingRTCOutboundMedia) Close() error { return nil }

func newRTCDeviceSinkRateRegistry(t interface {
	Helper()
	Fatal(...any)
}, rate int) *devicegw.VirtualRegistry {
	t.Helper()
	capability := devicegw.VirtualCapability{
		SampleRate: rate,
		Channels:   audio.Channels,
		BitDepth:   audio.DeviceBitDepthPCM16,
		Format:     audio.DeviceEncodingPCM16,
	}
	registry, err := devicegw.NewVirtualRegistry(devicegw.VirtualBackendConfig{
		Devices: []devicegw.VirtualDeviceConfig{
			{ID: "input", Name: "Input", Direction: devicegw.DirectionInput, Capabilities: []devicegw.VirtualCapability{capability}, LoopbackID: "output"},
			{ID: "output", Name: "Output", Direction: devicegw.DirectionOutput, Capabilities: []devicegw.VirtualCapability{capability}, LoopbackID: "input"},
		},
		Defaults: map[devicegw.Direction]string{devicegw.DirectionInput: "input", devicegw.DirectionOutput: "output"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

var _ audio.OutboundMedia = (*recordingRTCOutboundMedia)(nil)
