package runtime

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

// ErrRTCSessionCaptureBufferUnavailable indicates that a source was handed to
// a session runtime without its owner-created capture handoff.
var ErrRTCSessionCaptureBufferUnavailable = errors.New("RTC session capture buffer is unavailable")

// captureWorkerJoinBound bounds how long a failing capture pump waits for its
// device worker to observe cancellation. A native read that ignores the
// context cannot hold the session terminal path longer than this; the
// source's Close remains the final join for such a worker.
const captureWorkerJoinBound = time.Second

// BufferedCapture owns the bounded capture handoff between the device worker
// and provider writer. The owner creates it before starting either worker so
// loop observations and cancellation control always refer to the live queue.
type BufferedCapture struct {
	producer audio.FrameProducer
	consumer audio.FrameConsumer
	control  audio.BufferControl
}

// NewBufferedCapture creates a provider-rate capture buffer for source. It
// performs no device I/O; the returned capabilities are safe to bind to the
// loop before PumpBufferedCaptureWithBuffer starts.
func NewBufferedCapture(source *RTCDeviceSource) (*BufferedCapture, error) {
	if source == nil {
		return nil, ErrRTCDeviceSourceClosed
	}
	producer, consumer, control, err := audio.NewFrameBuffer(64, max(source.providerRate*2, audio.FrameSize))
	if err != nil {
		return nil, err
	}
	return &BufferedCapture{producer: producer, consumer: consumer, control: control}, nil
}

func (b *BufferedCapture) Producer() audio.FrameProducer {
	if b == nil {
		return audio.FrameProducer{}
	}
	return b.producer
}

func (b *BufferedCapture) Consumer() audio.FrameConsumer {
	if b == nil {
		return audio.FrameConsumer{}
	}
	return b.consumer
}

func (b *BufferedCapture) Control() audio.BufferControl {
	if b == nil {
		return audio.BufferControl{}
	}
	return b.control
}

// pumpBufferedCapture separates device acquisition from provider transmission.
// Only this transport worker can call the provider. The source produces owned
// frames into a bounded memory port, so no agent tick performs device I/O.
func PumpBufferedCapture(ctx context.Context, source *RTCDeviceSource, outbound audio.OutboundMedia) error {
	buffer, err := NewBufferedCapture(source)
	if err != nil {
		return err
	}
	return PumpBufferedCaptureWithBuffer(ctx, source, outbound, buffer)
}

// PumpBufferedCaptureWithBuffer runs the capture worker against the supplied
// production buffer. No hidden queue is allocated, so the control and
// snapshots bound to buffer observe this exact handoff.
func PumpBufferedCaptureWithBuffer(ctx context.Context, source *RTCDeviceSource, outbound audio.OutboundMedia, buffer *BufferedCapture) error {
	if source == nil || buffer == nil {
		return ErrRTCDeviceSourceClosed
	}
	producer, consumer := buffer.producer, buffer.consumer
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	uploaded := source.uploadedSamplesObserver
	done := make(chan error, 1)
	go func() {
		// The outer loop invokes uploaded after the provider media boundary
		// accepts each frame. Suppress only this worker's callback through the
		// private argument; mutating source configuration here would race a
		// concurrent Pump/Close and restore evidence too early on failure.
		err := source.pumpWithUploadedObserver(runCtx, audio.BufferedOutbound{Producer: producer}, nil)
		producer.Close()
		done <- err
	}()
	for {
		frame, err := consumer.Receive(runCtx)
		if errors.Is(err, io.EOF) {
			return <-done
		}
		if err != nil {
			cancel()
			joinCaptureWorker(done)
			return err
		}
		if err := outbound.WriteFrame(runCtx, frame); err != nil {
			cancel()
			joinCaptureWorker(done)
			return &RTCDeviceSourceError{DeviceID: source.id, Operation: "write", Err: err}
		}
		if uploaded != nil {
			uploaded(source.providerRate, frame.Samples)
		}
	}
}

// joinCaptureWorker waits, after cancellation, for the capture worker to exit
// so a failed pump does not leave it running behind the caller. The wait is
// bounded: a native read that has not yet observed cancellation must not hold
// the session terminal path open indefinitely, and the source's Close joins
// any worker still running at teardown. The worker's own error is the
// cancellation consequence of the failure being returned and is dropped.
func joinCaptureWorker(done <-chan error) {
	timer := time.NewTimer(captureWorkerJoinBound)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
	}
}

// rtcCaptureUploader converts filtered device capture to the provider rate and
// writes each non-empty converted frame to the outbound media, reporting the
// uploaded samples to the optional observer after each successful write.
type rtcCaptureUploader struct {
	source    *RTCDeviceSource
	processor *audio.Processor
	outbound  audio.OutboundMedia
	observer  RTCDeviceCaptureSamplesObserver
}

func (u rtcCaptureUploader) writeAll(ctx context.Context, batches [][]int16) error {
	for _, samples := range batches {
		if len(samples) == 0 {
			continue
		}
		if err := u.write(ctx, samples, false); err != nil {
			return err
		}
	}
	return nil
}

func (u rtcCaptureUploader) write(ctx context.Context, samples []int16, final bool) error {
	frames, err := u.processor.ProcessAvailable(audio.PCMFrame{Samples: samples, EndOfResponse: final})
	if err != nil {
		return &RTCDeviceSourceError{DeviceID: u.source.id, Operation: "resample", Err: err}
	}
	for _, converted := range frames {
		if len(converted.Samples) == 0 {
			continue
		}
		if err := u.outbound.WriteFrame(ctx, converted); err != nil {
			return &RTCDeviceSourceError{DeviceID: u.source.id, Operation: "write", Err: err}
		}
		if u.observer != nil {
			u.observer(u.source.providerRate, converted.Samples)
		}
	}
	return nil
}

// filteredCapture returns the capture batches to upload for one device frame:
// an owned copy of the frame without a filter, otherwise the filter's output.
func (s *RTCDeviceSource) filteredCapture(ctx context.Context, frame []int16) ([][]int16, error) {
	if s.filter == nil {
		return [][]int16{append([]int16(nil), frame...)}, nil
	}
	samples, err := s.filter.FilterCapture(ctx, frame)
	if err != nil {
		return nil, &RTCDeviceSourceError{DeviceID: s.id, Operation: "filter", Err: err}
	}
	return samples, nil
}
