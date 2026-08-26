package audio

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
