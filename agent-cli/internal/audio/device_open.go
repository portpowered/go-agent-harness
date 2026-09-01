package audio

import "fmt"

// NewDuplexDeviceSourceSinkWithFormat resolves both selectors and asks a
// duplex-capable registry to acquire them as one graph. The returned adapters
// preserve the same validation and ownership contract as the independent
// NewDeviceSourceWithFormat/NewDeviceSinkWithFormat constructors.
func NewDuplexDeviceSourceSinkWithFormat(registry DeviceRegistry, inputID DeviceID, inputFormat DeviceFormat, outputID DeviceID, outputFormat DeviceFormat) (*DeviceSource, *DeviceSink, error) {
	if err := inputFormat.Validate(); err != nil {
		return nil, nil, err
	}
	if err := outputFormat.Validate(); err != nil {
		return nil, nil, err
	}
	opener, ok := registry.(DuplexDeviceFormatOpener)
	if !ok {
		return nil, nil, ErrDuplexDeviceUnavailable
	}
	resolvedInput, err := resolveDeviceIDForOpen(registry, inputID, DirectionInput)
	if err != nil {
		return nil, nil, err
	}
	resolvedOutput, err := resolveDeviceIDForOpen(registry, outputID, DirectionOutput)
	if err != nil {
		return nil, nil, err
	}
	input, output, err := opener.OpenDuplexWithFormat(resolvedInput, inputFormat, resolvedOutput, outputFormat)
	if err != nil {
		if input != nil {
			_ = input.Close()
		}
		if output != nil {
			_ = output.Close()
		}
		return nil, nil, err
	}
	if input == nil || output == nil {
		if input != nil {
			_ = input.Close()
		}
		if output != nil {
			_ = output.Close()
		}
		return nil, nil, ErrNilOpenedDevice
	}
	if err := validateDuplexOpenedDevice(input, resolvedInput, DirectionInput, inputFormat); err != nil {
		_ = input.Close()
		_ = output.Close()
		return nil, nil, err
	}
	if err := validateDuplexOpenedDevice(output, resolvedOutput, DirectionOutput, outputFormat); err != nil {
		_ = input.Close()
		_ = output.Close()
		return nil, nil, err
	}
	source, err := newDeviceSourceFromOpened(input, resolvedInput, inputFormat)
	if err != nil {
		_ = output.Close()
		return nil, nil, err
	}
	sink, err := newDeviceSinkFromOpened(output, resolvedOutput, outputFormat)
	if err != nil {
		_ = source.Close()
		return nil, nil, err
	}
	return source, sink, nil
}

func validateDuplexOpenedDevice(handle OpenedDevice, id DeviceID, direction Direction, format DeviceFormat) error {
	if got, ok := openedDeviceDirection(handle); ok && got != direction {
		return &DeviceDirectionError{ID: id, Direction: direction, Want: direction, Got: got, Kind: ErrDeviceDirectionMismatch}
	}
	if provider, ok := handle.(DeviceFormatProvider); ok {
		actual := provider.DeviceFormat()
		if !actual.equal(format) {
			return &DeviceFormatError{ID: id, Direction: direction, Requested: format, Available: []DeviceFormat{actual}}
		}
	}
	return nil
}

// resolveDeviceIDForOpen resolves only the directional default. Explicit
// selectors remain exact IDs and are handed directly to DeviceRegistry.Open,
// preserving the registry's native not-found, in-use, and direction errors.
// Both DeviceSource and DeviceSink use this helper so default selection has a
// single implementation at the shared audio boundary.
func resolveDeviceIDForOpen(registry DeviceRegistry, id DeviceID, direction Direction) (DeviceID, error) {
	if nilInterface(registry) {
		return "", &DeviceRegistryError{ID: id, Direction: direction, Err: ErrNilDeviceRegistry}
	}
	if id != "" {
		return id, nil
	}

	device, err := registry.Default(direction)
	if err != nil {
		return "", err
	}
	if device.Direction != direction {
		return "", &DeviceDirectionError{
			ID:        device.ID,
			Direction: direction,
			Want:      direction,
			Got:       device.Direction,
			Kind:      ErrDeviceDirectionMismatch,
		}
	}
	if device.ID == "" {
		return "", &InvalidDeviceError{Reason: "selected device ID must not be empty"}
	}
	return device.ID, nil
}

func acquireDeviceWithFormat(registry DeviceRegistry, id DeviceID, direction Direction, format DeviceFormat) (OpenedDevice, error) {
	if err := format.Validate(); err != nil {
		return nil, err
	}
	if nilInterface(registry) {
		return nil, &DeviceRegistryError{ID: id, Direction: direction, Err: ErrNilDeviceRegistry}
	}

	var (
		handle OpenedDevice
		err    error
	)
	if opener, ok := registry.(DeviceFormatOpener); ok && !format.equal(DefaultDeviceFormat()) {
		handle, err = opener.OpenWithFormat(id, format)
	} else {
		if !format.equal(DefaultDeviceFormat()) {
			return nil, &DeviceFormatError{
				ID:        id,
				Direction: direction,
				Requested: format,
				Available: defaultDeviceFormatAvailability(),
				Err:       fmt.Errorf("registry does not support explicit device formats"),
			}
		}
		handle, err = registry.Open(id)
	}
	if err != nil {
		if !nilInterface(handle) {
			_ = handle.Close()
		}
		return nil, err
	}
	if nilInterface(handle) {
		return nil, &DeviceRegistryError{ID: id, Direction: direction, Err: ErrNilOpenedDevice}
	}
	if got, ok := openedDeviceDirection(handle); ok && got != direction {
		_ = handle.Close()
		return nil, &DeviceDirectionError{ID: id, Direction: direction, Want: direction, Got: got, Kind: ErrDeviceDirectionMismatch}
	}
	if provider, ok := handle.(DeviceFormatProvider); ok {
		actual := provider.DeviceFormat()
		if !actual.equal(format) {
			_ = handle.Close()
			return nil, &DeviceFormatError{ID: id, Direction: direction, Requested: format, Available: []DeviceFormat{actual}}
		}
	}
	return handle, nil
}
