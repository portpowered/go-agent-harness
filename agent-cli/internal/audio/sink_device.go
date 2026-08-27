package audio

import "context"

type DeviceSink struct {
	adapter     *deviceAdapter
	frameWriter deviceFrameWriter
	byteWriter  deviceByteWriter
}

var _ AudioSink = (*DeviceSink)(nil)

func NewDeviceSink(registry DeviceRegistry, id DeviceID) (*DeviceSink, error) {
	resolvedID, err := resolveDeviceIDForOpen(registry, id, DirectionOutput)
	if err != nil {
		return nil, err
	}
	handle, err := acquireDevice(registry, resolvedID, DirectionOutput)
	if err != nil {
		return nil, err
	}
	frames, hasFrames := handle.(deviceFrameWriter)
	bytes, hasBytes := handle.(deviceByteWriter)
	if !hasFrames && !hasBytes {
		_ = handle.Close()
		return nil, &DeviceCapabilityError{ID: resolvedID, Direction: DirectionOutput, Operation: "write", Kind: ErrDeviceCapabilityMismatch}
	}
	return &DeviceSink{newDeviceAdapter(handle, resolvedID, DirectionOutput), frames, bytes}, nil
}

// DeviceID returns the stable ID acquired by the sink. When the sink was
// opened with an empty selector, this is the ID returned by the registry's
// directional default.
func (s *DeviceSink) DeviceID() DeviceID {
	if s == nil || s.adapter == nil {
		return ""
	}
	return s.adapter.id
}

func (s *DeviceSink) WriteFrame(ctx context.Context, frame []int16) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateFrame("write", frame); err != nil {
		return err
	}
	if err := s.adapter.begin("write"); err != nil {
		return err
	}
	if s.frameWriter != nil {
		return s.adapter.finish("write", s.frameWriter.WriteFrame(ctx, append([]int16(nil), frame...)))
	}
	encoded := make([]byte, rawFrameBytes)
	encodePCM16(encoded, frame)
	return s.adapter.finish("write", s.byteWriter.Write(ctx, encoded))
}

func (s *DeviceSink) Close() error {
	if s == nil || s.adapter == nil {
		return nil
	}
	return s.adapter.close()
}
