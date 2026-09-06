// Package devices contains the gateway-backed implementation of the public
// device service. It is deliberately private so callers cannot depend on the
// registry or opened-device types used by the implementation.
package devices

import (
	"context"
	"errors"
	"fmt"
	"sort"

	serviceDevices "github.com/portpowered/go-agent-harness/agent-cli/internal/services/devices"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
)

// Service adapts one gateway registry to the service use cases. The registry
// is owned by composition; this service never creates a fallback registry.
type Service struct {
	registry devicegw.DeviceRegistry
}

func New(registry devicegw.DeviceRegistry) *Service {
	return &Service{registry: registry}
}

var _ serviceDevices.DeviceService = (*Service)(nil)

func (s *Service) Enumerate(ctx context.Context) (serviceDevices.DeviceList, error) {
	if err := contextErr(ctx); err != nil {
		return serviceDevices.DeviceList{}, err
	}
	if s == nil || s.registry == nil {
		return serviceDevices.DeviceList{}, errors.New("audio device registry is required")
	}
	listed, err := s.registry.List()
	if err != nil {
		return serviceDevices.DeviceList{}, fmt.Errorf("list audio devices: %w", err)
	}
	result := serviceDevices.DeviceList{Devices: make([]serviceDevices.Device, 0, len(listed))}
	seen := make(map[string]struct{}, len(listed))
	for _, device := range listed {
		if err := device.Validate(); err != nil {
			return serviceDevices.DeviceList{}, fmt.Errorf("list audio device %q: %w", device.ID, err)
		}
		key := fmt.Sprintf("%s\x00%s", device.ID, device.Direction)
		if _, exists := seen[key]; exists {
			return serviceDevices.DeviceList{}, fmt.Errorf("list audio devices: duplicate device %q for %s", device.ID, device.Direction)
		}
		seen[key] = struct{}{}
		result.Devices = append(result.Devices, toServiceDevice(device))
	}
	if len(result.Devices) == 0 {
		return result, nil
	}
	for _, direction := range []devicegw.Direction{devicegw.DirectionInput, devicegw.DirectionOutput} {
		defaultDevice, err := s.registry.Default(direction)
		if err != nil {
			return serviceDevices.DeviceList{}, fmt.Errorf("resolve default %s audio device: %w", direction, err)
		}
		if err := defaultDevice.Validate(); err != nil {
			return serviceDevices.DeviceList{}, fmt.Errorf("resolve default %s audio device: %w", direction, err)
		}
		found := false
		for i := range result.Devices {
			if result.Devices[i].ID == defaultDevice.ID && result.Devices[i].Direction == serviceDevices.DeviceDirection(direction) {
				result.Devices[i].Default = true
				found = true
				break
			}
		}
		if !found {
			return serviceDevices.DeviceList{}, fmt.Errorf("resolve default %s audio device: %q was not returned by enumeration", direction, defaultDevice.ID)
		}
	}
	sort.SliceStable(result.Devices, func(i, j int) bool {
		if result.Devices[i].Direction != result.Devices[j].Direction {
			return result.Devices[i].Direction == serviceDevices.DeviceDirectionInput
		}
		if result.Devices[i].ID != result.Devices[j].ID {
			return result.Devices[i].ID < result.Devices[j].ID
		}
		return result.Devices[i].Name < result.Devices[j].Name
	})
	return result, nil
}

func (s *Service) Select(ctx context.Context, request serviceDevices.DeviceSelectionRequest) (serviceDevices.DeviceSelection, error) {
	if err := contextErr(ctx); err != nil {
		return serviceDevices.DeviceSelection{}, err
	}
	if s == nil || s.registry == nil {
		return serviceDevices.DeviceSelection{}, errors.New("audio device registry is required")
	}
	selection, err := devicegw.ResolveDeviceSelection(s.registry, devicegw.DeviceSelectionRequest{
		InputSelector: request.InputSelector, OutputSelector: request.OutputSelector,
		AudioInFile: request.AudioInFile, AudioInConfigured: request.AudioInConfigured,
		OnDeviceLoss: devicegw.DeviceLossPolicy(request.OnDeviceLoss),
	})
	if err != nil {
		return serviceDevices.DeviceSelection{}, err
	}
	return serviceDevices.DeviceSelection{
		Input: toServiceDevice(selection.Input), Output: toServiceDevice(selection.Output),
		InputSelected: selection.InputSelected, OutputSelected: selection.OutputSelected,
		LossPolicy: string(selection.LossPolicy),
	}, nil
}

func (s *Service) ProbeAvailability(ctx context.Context) (serviceDevices.DeviceProbeAvailability, error) {
	if err := contextErr(ctx); err != nil {
		return serviceDevices.DeviceProbeAvailability{}, err
	}
	if s == nil || s.registry == nil {
		return serviceDevices.DeviceProbeAvailability{}, errors.New("audio device registry is required")
	}
	availability, err := devicegw.ProbeDeviceAvailability(s.registry)
	if err != nil {
		return serviceDevices.DeviceProbeAvailability{}, err
	}
	result := serviceDevices.DeviceProbeAvailability{
		Status:            serviceDevices.DeviceProbeStatus(availability.Status),
		ReasonCode:        serviceDevices.DeviceProbeSkipCode(availability.ReasonCode),
		Reason:            availability.Reason,
		InputDeviceCount:  availability.InputDeviceCount,
		OutputDeviceCount: availability.OutputDeviceCount,
		Devices:           toServiceDevices(availability.Devices),
		InputDevices:      toServiceDevices(availability.InputDevices),
		OutputDevices:     toServiceDevices(availability.OutputDevices),
	}
	return result, nil
}

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func toServiceDevices(devices []devicegw.Device) []serviceDevices.Device {
	result := make([]serviceDevices.Device, len(devices))
	for i, device := range devices {
		result[i] = toServiceDevice(device)
	}
	return result
}

func toServiceDevice(device devicegw.Device) serviceDevices.Device {
	return serviceDevices.Device{
		ID: device.ID, Backend: device.Backend, NativeID: device.NativeID,
		Name: device.Display(), DisplayName: device.Display(),
		Direction: serviceDevices.DeviceDirection(device.Direction),
	}
}
