package audio

import "fmt"

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
