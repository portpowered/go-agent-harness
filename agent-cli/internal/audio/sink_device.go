package audio

import "context"

type devicePlaybackWaiter interface {
	WaitForPlayback(context.Context) error
}

type DeviceSink struct {
	adapter        *deviceAdapter
	frameWriter    deviceFrameWriter
	byteWriter     deviceByteWriter
	playbackWaiter devicePlaybackWaiter
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
	waiter, _ := handle.(devicePlaybackWaiter)
	return &DeviceSink{adapter: newDeviceAdapter(handle, resolvedID, DirectionOutput), frameWriter: frames, byteWriter: bytes, playbackWaiter: waiter}, nil
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

// WaitForPlayback waits for an output backend that exposes physical drain
// progress. Backends without that optional capability retain their existing
// asynchronous write behavior.
func (s *DeviceSink) WaitForPlayback(ctx context.Context) error {
	if s == nil || s.playbackWaiter == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return s.adapter.finish("wait for playback", s.playbackWaiter.WaitForPlayback(ctx))
}

func (s *DeviceSink) Close() error {
	if s == nil || s.adapter == nil {
		return nil
	}
	return s.adapter.close()
}
