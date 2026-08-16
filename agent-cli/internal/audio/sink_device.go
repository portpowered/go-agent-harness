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
	frameWriter, hasFrames := handle.(deviceFrameWriter)
	byteWriter, hasBytes := handle.(deviceByteWriter)
	if !hasFrames && !hasBytes {
		_ = handle.Close()
		return nil, &DeviceCapabilityError{ID: id, Direction: DirectionOutput, Operation: "write", Kind: ErrDeviceCapabilityMismatch}
	}
	return &DeviceSink{
		adapter:     newDeviceAdapter(handle, id, DirectionOutput),
		frameWriter: frameWriter,
		byteWriter:  byteWriter,
	}, nil
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
		copyOfFrame := append([]int16(nil), frame...)
		return s.adapter.finish("write", s.frameWriter.WriteFrame(ctx, copyOfFrame))
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
