package audio

import "context"

type DeviceSink struct {
	adapter     *deviceAdapter
	frameWriter deviceFrameWriter
	byteWriter  deviceByteWriter
	format      DeviceFormat
}

var _ AudioSink = (*DeviceSink)(nil)

func NewDeviceSink(registry DeviceRegistry, id DeviceID) (*DeviceSink, error) {
	return NewDeviceSinkWithFormat(registry, id, DefaultDeviceFormat())
}

// NewDeviceSinkAtRate opens a playback device as mono PCM16 at rate.
func NewDeviceSinkAtRate(registry DeviceRegistry, id DeviceID, rate int) (*DeviceSink, error) {
	return NewDeviceSinkWithFormat(registry, id, PCM16DeviceFormat(rate))
}

// NewDeviceSinkWithFormat opens a playback device using an explicit format.
// Registries that do not expose DeviceFormatOpener retain compatibility for
// the default format but cannot safely claim support for another rate.
func NewDeviceSinkWithFormat(registry DeviceRegistry, id DeviceID, format DeviceFormat) (*DeviceSink, error) {
	if err := format.Validate(); err != nil {
		return nil, err
	}
	resolvedID, err := resolveDeviceIDForOpen(registry, id, DirectionOutput)
	if err != nil {
		return nil, err
	}
	handle, err := acquireDeviceWithFormat(registry, resolvedID, DirectionOutput, format)
	if err != nil {
		return nil, err
	}
	frames, hasFrames := handle.(deviceFrameWriter)
	bytes, hasBytes := handle.(deviceByteWriter)
	if !hasFrames && !hasBytes {
		_ = handle.Close()
		return nil, &DeviceCapabilityError{ID: resolvedID, Direction: DirectionOutput, Operation: "write", Kind: ErrDeviceCapabilityMismatch}
	}
	return &DeviceSink{adapter: newDeviceAdapter(handle, resolvedID, DirectionOutput), frameWriter: frames, byteWriter: bytes, format: format}, nil
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

// DeviceFormat reports the format selected when the sink was opened.
func (s *DeviceSink) DeviceFormat() DeviceFormat {
	if s == nil {
		return DeviceFormat{}
	}
	return s.format
}

// SampleRate reports the selected playback rate for callers that only need
// the pacing contract.
func (s *DeviceSink) SampleRate() int {
	return s.DeviceFormat().SampleRate
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
