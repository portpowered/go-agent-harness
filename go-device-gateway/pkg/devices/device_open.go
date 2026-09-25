package devices

import audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"

import "fmt"

// NewDuplexDeviceSourceSinkWithFormat resolves both selectors and asks a
// duplex-capable registry to acquire them as one graph. The returned adapters
// preserve the same validation and ownership contract as the independent
// NewDeviceSourceWithFormat/NewDeviceSinkWithFormat constructors.
func NewDuplexDeviceSourceSinkWithFormat(registry DeviceRegistry, inputID DeviceID, inputFormat audio.DeviceFormat, outputID DeviceID, outputFormat audio.DeviceFormat) (*DeviceSource, *DeviceSink, error) {
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
		return nil, nil, withCleanupError(err, closeOpenedPair(input, output))
	}
	if input == nil || output == nil {
		return nil, nil, withCleanupError(ErrNilOpenedDevice, closeOpenedPair(input, output))
	}
	if err := validateDuplexOpenedDevice(input, resolvedInput, DirectionInput, inputFormat); err != nil {
		return nil, nil, withCleanupError(err, closeOpenedPair(input, output))
	}
	if err := validateDuplexOpenedDevice(output, resolvedOutput, DirectionOutput, outputFormat); err != nil {
		return nil, nil, withCleanupError(err, closeOpenedPair(input, output))
	}
	source, err := newDeviceSourceFromOpened(input, resolvedInput, inputFormat)
	if err != nil {
		return nil, nil, withCleanupError(err, output.Close())
	}
	sink, err := newDeviceSinkFromOpened(output, resolvedOutput, outputFormat)
	if err != nil {
		return nil, nil, withCleanupError(err, source.Close())
	}
	return source, sink, nil
}

// closeOpenedPair releases whichever duplex handles were acquired before a
// failed open and reports every release failure.
func closeOpenedPair(input, output OpenedDevice) error {
	var err error
	if input != nil {
		joinCleanupError(&err, input.Close())
	}
	if output != nil {
		joinCleanupError(&err, output.Close())
	}
	return err
}

func validateDuplexOpenedDevice(handle OpenedDevice, id DeviceID, direction Direction, format audio.DeviceFormat) error {
	if got, ok := openedDeviceDirection(handle); ok && got != direction {
		return &DeviceDirectionError{ID: id, Direction: direction, Want: direction, Got: got, Kind: ErrDeviceDirectionMismatch}
	}
	if provider, ok := handle.(DeviceFormatProvider); ok {
		actual := provider.DeviceFormat()
		if !actual.Equal(format) {
			return &DeviceFormatError{ID: id, Direction: direction, Requested: format, Available: []audio.DeviceFormat{actual}}
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

func acquireDeviceWithFormat(registry DeviceRegistry, id DeviceID, direction Direction, format audio.DeviceFormat) (OpenedDevice, error) {
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
	if opener, ok := registry.(DeviceFormatOpener); ok && !format.Equal(audio.DefaultDeviceFormat()) {
		handle, err = opener.OpenWithFormat(id, format)
	} else {
		if !format.Equal(audio.DefaultDeviceFormat()) {
			return nil, &DeviceFormatError{
				ID:        id,
				Direction: direction,
				Requested: format,
				Available: audio.DefaultDeviceFormatAvailability(),
				Err:       fmt.Errorf("registry does not support explicit device formats"),
			}
		}
		handle, err = registry.Open(id)
	}
	if err != nil {
		if !nilInterface(handle) {
			err = withCleanupError(err, handle.Close())
		}
		return nil, err
	}
	if nilInterface(handle) {
		return nil, &DeviceRegistryError{ID: id, Direction: direction, Err: ErrNilOpenedDevice}
	}
	if got, ok := openedDeviceDirection(handle); ok && got != direction {
		return nil, withCleanupError(&DeviceDirectionError{ID: id, Direction: direction, Want: direction, Got: got, Kind: ErrDeviceDirectionMismatch}, handle.Close())
	}
	if provider, ok := handle.(DeviceFormatProvider); ok {
		actual := provider.DeviceFormat()
		if !actual.Equal(format) {
			return nil, withCleanupError(&DeviceFormatError{ID: id, Direction: direction, Requested: format, Available: []audio.DeviceFormat{actual}}, handle.Close())
		}
	}
	return handle, nil
}

// constantDevice builds a device descriptor from compile-time constant
// identifiers. Invalid constants are a programming error, not a runtime state.
func constantDevice(backend, nativeID, name string, direction Direction) Device {
	device, err := NewDevice(backend, nativeID, name, direction)
	if err != nil {
		panic(fmt.Sprintf("devices: invalid constant device %s/%s: %v", backend, nativeID, err))
	}
	return device
}
