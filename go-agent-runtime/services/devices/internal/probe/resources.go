package deviceprobe

import (
	"errors"
	"fmt"

	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
)

type liveDeviceProbeResources struct {
	source     *devicegw.DeviceSource
	sink       *devicegw.DeviceSink
	inputLink  *liveDeviceProbeMediaLink
	outputLink *liveDeviceProbeMediaLink
}

func openLiveDeviceProbeResources(registry devicegw.DeviceRegistry, inputDevice, outputDevice devicegw.Device) (liveDeviceProbeResources, error) {
	resources := liveDeviceProbeResources{}
	var err error
	resources.source, err = devicegw.NewDeviceSource(registry, inputDevice.ID)
	if err != nil {
		return liveDeviceProbeResources{}, fmt.Errorf("open selected input device %q (%s): %w", inputDevice.ID, inputDevice.Display(), err)
	}
	resources.sink, err = devicegw.NewDeviceSink(registry, outputDevice.ID)
	if err != nil {
		return liveDeviceProbeResources{}, errors.Join(fmt.Errorf("open selected output device %q (%s): %w", outputDevice.ID, outputDevice.Display(), err), resources.source.Close())
	}
	resources.inputLink, err = newLiveDeviceProbeMediaLink()
	if err != nil {
		return liveDeviceProbeResources{}, errors.Join(fmt.Errorf("create microphone WebRTC path: %w", err), resources.source.Close(), resources.sink.Close())
	}
	resources.outputLink, err = newLiveDeviceProbeMediaLink()
	if err != nil {
		return liveDeviceProbeResources{}, errors.Join(fmt.Errorf("create speaker WebRTC path: %w", err), resources.source.Close(), resources.sink.Close(), resources.inputLink.Close())
	}
	return resources, nil
}

func (r liveDeviceProbeResources) Close() error {
	return errors.Join(
		closeDeviceProbeResource("input device", r.source.Close),
		closeDeviceProbeResource("output device", r.sink.Close),
		closeDeviceProbeResource("microphone WebRTC path", r.inputLink.Close),
		closeDeviceProbeResource("speaker WebRTC path", r.outputLink.Close),
	)
}
