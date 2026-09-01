package audio

import "context"

type devicePlaybackWaiter interface {
	WaitForPlayback(context.Context) error
}

type devicePlaybackCapacityWaiter interface {
	WaitForPlaybackCapacity(context.Context, int) error
}

type DeviceSink struct {
	adapter        *deviceAdapter
	frameWriter    deviceFrameWriter
	sampleWriter   deviceSampleWriter
	byteWriter     deviceByteWriter
	playbackWaiter devicePlaybackWaiter
	capacityWaiter devicePlaybackCapacityWaiter
	format         DeviceFormat
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
	return newDeviceSinkFromOpened(handle, resolvedID, format)
}

func newDeviceSinkFromOpened(handle OpenedDevice, resolvedID DeviceID, format DeviceFormat) (*DeviceSink, error) {
	frames, hasFrames := handle.(deviceFrameWriter)
	samples, hasSamples := handle.(deviceSampleWriter)
	bytes, hasBytes := handle.(deviceByteWriter)
	if !hasFrames && !hasSamples && !hasBytes {
		_ = handle.Close()
		return nil, &DeviceCapabilityError{ID: resolvedID, Direction: DirectionOutput, Operation: "write", Kind: ErrDeviceCapabilityMismatch}
	}
	waiter, _ := handle.(devicePlaybackWaiter)
	capacityWaiter, _ := handle.(devicePlaybackCapacityWaiter)
	return &DeviceSink{adapter: newDeviceAdapter(handle, resolvedID, DirectionOutput), frameWriter: frames, sampleWriter: samples, byteWriter: bytes, playbackWaiter: waiter, capacityWaiter: capacityWaiter, format: format}, nil
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

// PlaybackStats returns a consistent observation of samples queued for the
// device. Backends that do not expose a queue retain a zeroed, format-aware
// snapshot so callers can keep the optional capability source-compatible.
func (s *DeviceSink) PlaybackStats() PlaybackQueueStats {
	if s == nil {
		return PlaybackQueueStats{}
	}
	if s.adapter != nil {
		if provider, ok := s.adapter.handle.(PlaybackStatsProvider); ok {
			return provider.PlaybackStats()
		}
	}
	return emptyPlaybackQueueStats(s.format)
}

// DiscardPlayback removes samples queued for future device callbacks and
// returns the exact number removed. Audio already submitted to the device is
// outside this operation's recall boundary.
func (s *DeviceSink) DiscardPlayback() int {
	if s == nil || s.adapter == nil {
		return 0
	}
	if discarder, ok := s.adapter.handle.(PlaybackDiscarder); ok {
		return discarder.DiscardPlayback()
	}
	return 0
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

// WaitForPlaybackCapacity applies optional device-owned backpressure before
// samples are enqueued. Backends without a callback queue preserve their
// synchronous write behavior.
func (s *DeviceSink) WaitForPlaybackCapacity(ctx context.Context, samples int) error {
	if s == nil || s.capacityWaiter == nil || samples <= 0 {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return s.adapter.finish("wait for playback capacity", s.capacityWaiter.WaitForPlaybackCapacity(ctx, samples))
}

// WriteSamples queues a non-empty PCM16 chunk without imposing the legacy
// 480-sample processing frame. RTC playback uses this only to preserve an
// exact response-final remainder after sample-rate conversion.
func (s *DeviceSink) WriteSamples(ctx context.Context, samples []int16) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if len(samples) == 0 {
		return nil
	}
	if len(samples) == FrameSize {
		return s.WriteFrame(ctx, samples)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := s.adapter.begin("write"); err != nil {
		return err
	}
	if s.sampleWriter != nil {
		return s.adapter.finish("write", s.sampleWriter.WriteSamples(ctx, append([]int16(nil), samples...)))
	}
	if s.byteWriter != nil {
		encoded := make([]byte, len(samples)*2)
		encodePCM16(encoded, samples)
		return s.adapter.finish("write", s.byteWriter.Write(ctx, encoded))
	}
	return s.adapter.finish("write", &FrameSizeError{Operation: "device write samples", Got: len(samples), Want: FrameSize})
}

func (s *DeviceSink) Close() error {
	if s == nil || s.adapter == nil {
		return nil
	}
	return s.adapter.close()
}
