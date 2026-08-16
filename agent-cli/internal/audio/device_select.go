package audio

import (
	"errors"
	"fmt"
	"strings"
)

var (
	ErrInvalidDeviceLossPolicy = errors.New("invalid device loss policy")
	ErrDeviceSelectionConflict = errors.New("device selection conflict")
)

// DeviceLossPolicy controls what happens after a selected device disappears.
type DeviceLossPolicy string

const (
	DeviceLossPolicyFail    DeviceLossPolicy = "fail"
	DeviceLossPolicyDefault DeviceLossPolicy = "default"
	DeviceLossPolicyStop    DeviceLossPolicy = "stop"
	DefaultDeviceLossPolicy                  = DeviceLossPolicyFail
)

// InvalidDeviceLossPolicyError identifies an unsupported on-device-loss value.
type InvalidDeviceLossPolicyError struct{ Policy DeviceLossPolicy }

func (e *InvalidDeviceLossPolicyError) Error() string {
	return fmt.Sprintf("invalid --on-device-loss value %q; want fail, default, or stop", e.Policy)
}
func (e *InvalidDeviceLossPolicyError) Unwrap() error { return ErrInvalidDeviceLossPolicy }

// DeviceSelectionConflictError identifies mutually exclusive file and device inputs.
type DeviceSelectionConflictError struct{ FileOption, DeviceOption string }

func (e *DeviceSelectionConflictError) Error() string {
	return fmt.Sprintf("%s and %s cannot be used together", e.FileOption, e.DeviceOption)
}
func (e *DeviceSelectionConflictError) Unwrap() error { return ErrDeviceSelectionConflict }

// DeviceSelectionRequest describes directional selectors and optional file input.
type DeviceSelectionRequest struct {
	InputSelector, OutputSelector string
	AudioInFile                   string
	AudioInConfigured             bool
	OnDeviceLoss                  DeviceLossPolicy
}

// DeviceSelection contains concrete devices and any handles acquired for them.
type DeviceSelection struct {
	Input, Output                 Device
	InputSelected, OutputSelected bool
	InputHandle, OutputHandle     OpenedDevice
	LossPolicy                    DeviceLossPolicy
}

func closeDevice(h OpenedDevice) error {
	if h == nil {
		return nil
	}
	return h.Close()
}

// Close releases acquired handles and is safe to repeat.
func (s DeviceSelection) Close() error {
	return errors.Join(closeDevice(s.InputHandle), closeDevice(s.OutputHandle))
}

// ResolveDeviceSelection resolves input before output without acquiring devices.
func ResolveDeviceSelection(registry DeviceRegistry, request DeviceSelectionRequest) (DeviceSelection, error) {
	policy, fileInput, err := validateSelectionRequest(request)
	if err != nil {
		return DeviceSelection{}, err
	}
	selection := DeviceSelection{LossPolicy: policy}
	if !fileInput {
		selection.Input, err = resolveDevice(registry, DirectionInput, request.InputSelector)
		if err != nil {
			return DeviceSelection{}, err
		}
		selection.InputSelected = true
	}
	selection.Output, err = resolveDevice(registry, DirectionOutput, request.OutputSelector)
	if err != nil {
		return DeviceSelection{}, err
	}
	selection.OutputSelected = true
	return selection, nil
}

// OpenDeviceSelection resolves and opens every required direction, cleaning up
// earlier acquisitions when a later open fails.
func OpenDeviceSelection(registry DeviceRegistry, request DeviceSelectionRequest) (DeviceSelection, error) {
	selection, err := ResolveDeviceSelection(registry, request)
	if err != nil {
		return DeviceSelection{}, err
	}
	if selection.InputSelected {
		selection.InputHandle, err = openDevice(registry, selection.Input)
		if err != nil {
			return DeviceSelection{}, err
		}
	}
	selection.OutputHandle, err = openDevice(registry, selection.Output)
	if err != nil {
		return DeviceSelection{}, errors.Join(err, selection.Close())
	}
	return selection, nil
}

// DeviceLossOutcome distinguishes failure, default replacement, and clean stop.
type DeviceLossOutcome string

const (
	DeviceLossOutcomeFailed    DeviceLossOutcome = "failed"
	DeviceLossOutcomeDefaulted DeviceLossOutcome = "defaulted"
	DeviceLossOutcomeStopped   DeviceLossOutcome = "stopped"
)

// DeviceLossResult carries an explicit outcome and replacement handle.
type DeviceLossResult struct {
	Outcome      DeviceLossOutcome
	Lost, Device Device
	Handle       OpenedDevice
}

func (r DeviceLossResult) Close() error { return closeDevice(r.Handle) }

// HandleDeviceLoss applies one policy. Default opens one different, current
// same-direction default and never retries or substitutes a null sink.
func HandleDeviceLoss(registry DeviceRegistry, lost Device, policy DeviceLossPolicy) (DeviceLossResult, error) {
	result := DeviceLossResult{Lost: lost, Outcome: DeviceLossOutcomeFailed}
	policy, err := normalizeDeviceLossPolicy(policy)
	if err != nil {
		return result, err
	}
	if err = validateSelectedDevice(lost, lost.Direction); err != nil {
		return result, err
	}
	switch policy {
	case DeviceLossPolicyFail:
		return result, &DeviceLostError{ID: lost.ID, Direction: lost.Direction}
	case DeviceLossPolicyStop:
		result.Outcome = DeviceLossOutcomeStopped
		return result, nil
	case DeviceLossPolicyDefault:
		replacement, err := registry.Default(lost.Direction)
		if err != nil {
			return result, err
		}
		if err = validateSelectedDevice(replacement, lost.Direction); err != nil {
			return result, err
		}
		if replacement.ID == lost.ID {
			return result, &DeviceLostError{ID: lost.ID, Direction: lost.Direction}
		}
		result.Handle, err = openDevice(registry, replacement)
		if err != nil {
			return result, err
		}
		result.Outcome, result.Device = DeviceLossOutcomeDefaulted, replacement
		return result, nil
	}
	return result, &InvalidDeviceLossPolicyError{Policy: policy}
}

func validateSelectionRequest(request DeviceSelectionRequest) (DeviceLossPolicy, bool, error) {
	fileInput := request.AudioInFile != "" || request.AudioInConfigured
	if fileInput && request.InputSelector != "" {
		return "", false, &DeviceSelectionConflictError{"--audio-in", "--audio-in-device"}
	}
	policy, err := normalizeDeviceLossPolicy(request.OnDeviceLoss)
	return policy, fileInput, err
}

func normalizeDeviceLossPolicy(policy DeviceLossPolicy) (DeviceLossPolicy, error) {
	if policy == "" {
		return DefaultDeviceLossPolicy, nil
	}
	switch policy {
	case DeviceLossPolicyFail, DeviceLossPolicyDefault, DeviceLossPolicyStop:
		return policy, nil
	default:
		return "", &InvalidDeviceLossPolicyError{Policy: policy}
	}
}

func resolveDevice(registry DeviceRegistry, direction Direction, selector string) (Device, error) {
	if selector == "" {
		device, err := registry.Default(direction)
		if err != nil {
			return Device{}, err
		}
		return device, validateSelectedDevice(device, direction)
	}
	devices, err := registry.List()
	if err != nil {
		return Device{}, err
	}
	for _, device := range devices {
		if device.Direction == direction && device.ID == selector {
			return device, validateSelectedDevice(device, direction)
		}
	}
	query, matches := strings.ToLower(selector), make([]Device, 0, 1)
	for _, device := range devices {
		if device.Direction == direction && strings.Contains(strings.ToLower(device.Display()), query) {
			matches = append(matches, device)
		}
	}
	switch len(matches) {
	case 0:
		return Device{}, NewDeviceNotFoundError(selector)
	case 1:
		return matches[0], validateSelectedDevice(matches[0], direction)
	default:
		return Device{}, NewAmbiguousDeviceNameError(selector, matches)
	}
}

func validateSelectedDevice(device Device, direction Direction) error {
	if device.Direction != direction {
		return &InvalidDeviceError{ID: device.ID, Reason: fmt.Sprintf("device direction is %s, want %s", device.Direction, direction)}
	}
	if device.ID == "" {
		return &InvalidDeviceError{Reason: "selected device ID must not be empty"}
	}
	return nil
}

func openDevice(registry DeviceRegistry, device Device) (OpenedDevice, error) {
	handle, err := registry.Open(device.ID)
	if err != nil {
		return nil, err
	}
	if handle == nil {
		return nil, &InvalidDeviceError{ID: device.ID, Reason: "registry returned a nil opened-device handle"}
	}
	return handle, nil
}
