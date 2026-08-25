package audio

import "context"

type DeviceSink struct {
	adapter     *deviceAdapter
	frameWriter deviceFrameWriter
	byteWriter  deviceByteWriter
}

var _ AudioSink = (*DeviceSink)(nil)

func NewDeviceSink(registry DeviceRegistry, id DeviceID) (*DeviceSink, error) {
	handle, err := acquireDevice(registry, id, DirectionOutput)
	if err != nil {
		return nil, err
	}
	frames, hasFrames := handle.(deviceFrameWriter)
	bytes, hasBytes := handle.(deviceByteWriter)
	if !hasFrames && !hasBytes {
		_ = handle.Close()
		return nil, &DeviceCapabilityError{ID: id, Direction: DirectionOutput, Operation: "write", Kind: ErrDeviceCapabilityMismatch}
	}
	return &DeviceSink{newDeviceAdapter(handle, id, DirectionOutput), frames, bytes}, nil
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
