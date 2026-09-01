package services

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sync"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport/rtc"
)

var (
	// ErrRTCDeviceSinkClosed identifies a pump that was stopped by closing
	// its device endpoint.
	ErrRTCDeviceSinkClosed = errors.New("RTC device sink is closed")
	// ErrRTCDeviceSinkRunning prevents two pumps from writing to one exclusive
	// device concurrently.
	ErrRTCDeviceSinkRunning = errors.New("RTC device sink is already running")
	// ErrNilRTCInboundMedia identifies a missing incoming track seam.
	ErrNilRTCInboundMedia = errors.New("RTC inbound media endpoint is nil")
)

// RTCDeviceSinkError identifies a runtime operation at the RTC-to-device
// boundary while preserving the underlying device, context, and media error.
type RTCDeviceSinkError struct {
	DeviceID  audio.DeviceID
	Operation string
	Err       error
}

// RTCDevicePlaybackObserver receives one final, synchronized playback queue
// snapshot when the sink closes. The callback is invoked outside the audio
// device callback and is therefore suitable for session diagnostics.
type RTCDevicePlaybackObserver func(audio.DeviceID, audio.PlaybackQueueStats)

func (e *RTCDeviceSinkError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("RTC device sink %q %s failed: %v", e.DeviceID, e.Operation, e.Err)
}

func (e *RTCDeviceSinkError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// RTCDeviceSink owns a registry-backed output device and pumps PCM frames from
// an incoming RTC media endpoint into it. The RTC endpoint remains
// caller-owned; Close only stops this sink and releases its device.
type RTCDeviceSink struct {
	sink             *audio.DeviceSink
	id               audio.DeviceID
	observer         rtcDevicePlaybackObserver
	playbackObserver RTCDevicePlaybackObserver

	lifeCtx    context.Context
	lifeCancel context.CancelCauseFunc

	mu        sync.Mutex
	closed    bool
	running   bool
	runDone   chan struct{}
	closeOnce sync.Once
	closeErr  error

	playbackMu         sync.Mutex
	playbackGeneration uint64
	playbackBlocked    bool
}

// NewRTCDeviceSink opens an output device through the shared audio registry.
// An empty id selects the registry's output default; a non-empty id is passed
// through as an exact stable device ID.
func NewRTCDeviceSink(registry audio.DeviceRegistry, id audio.DeviceID) (*RTCDeviceSink, error) {
	return newRTCDeviceSinkAtRate(registry, id, audio.SampleRate, nil)
}

// NewRTCDeviceSinkAtRate opens an output device for mono PCM16 at rate. A
// zero rate retains the compatibility default used by NewRTCDeviceSink.
func NewRTCDeviceSinkAtRate(registry audio.DeviceRegistry, id audio.DeviceID, rate int) (*RTCDeviceSink, error) {
	return newRTCDeviceSinkAtRate(registry, id, rate, nil)
}

func newRTCDeviceSinkAtRate(registry audio.DeviceRegistry, id audio.DeviceID, rate int, playbackObserver RTCDevicePlaybackObserver) (*RTCDeviceSink, error) {
	if rate == 0 {
		rate = audio.SampleRate
	}
	sink, err := audio.NewDeviceSinkAtRate(registry, id, rate)
	if err != nil {
		return nil, err
	}
	lifeCtx, lifeCancel := context.WithCancelCause(context.Background())
	return &RTCDeviceSink{
		sink:             sink,
		id:               sink.DeviceID(),
		playbackObserver: playbackObserver,
		lifeCtx:          lifeCtx,
		lifeCancel:       lifeCancel,
	}, nil
}

// NewDefaultRTCDeviceSink opens the directional output default from registry.
func NewDefaultRTCDeviceSink(registry audio.DeviceRegistry) (*RTCDeviceSink, error) {
	return NewRTCDeviceSink(registry, "")
}

// DeviceID returns the stable ID acquired by this sink.
func (s *RTCDeviceSink) DeviceID() audio.DeviceID {
	if s == nil {
		return ""
	}
	return s.id
}

// PlaybackStats returns the current synchronized local playback observation.
func (s *RTCDeviceSink) PlaybackStats() audio.PlaybackQueueStats {
	if s == nil || s.sink == nil {
		return audio.PlaybackQueueStats{}
	}
	return s.sink.PlaybackStats()
}

// DiscardPlayback removes only queued local speaker samples that have not
// reached a device callback yet. It is safe to race with Pump and Close.
func (s *RTCDeviceSink) DiscardPlayback() int {
	if s == nil || s.sink == nil {
		return 0
	}
	s.playbackMu.Lock()
	defer s.playbackMu.Unlock()
	s.playbackBlocked = true
	s.playbackGeneration++
	return s.sink.DiscardPlayback()
}

// resumePlayback opens a new local response boundary. Frames read under a
// prior generation remain stale even if they race with this transition.
func (s *RTCDeviceSink) resumePlayback() {
	if s == nil || s.sink == nil {
		return
	}
	s.playbackMu.Lock()
	s.playbackBlocked = false
	s.playbackGeneration++
	s.playbackMu.Unlock()
}

// Pump reads PCM frames from inbound and synchronously writes them to the
// output device at the audio adapter's fixed frame size and sample rate. A
// finite inbound endpoint ending with io.EOF is a clean pump completion. The
// method does not close the RTC endpoint because that endpoint belongs to its
// caller.
func (s *RTCDeviceSink) Pump(ctx context.Context, inbound rtc.InboundMedia) error {
	if s == nil {
		return ErrRTCDeviceSinkClosed
	}
	if nilRTCInboundMedia(inbound) {
		return ErrNilRTCInboundMedia
	}
	if ctx == nil {
		ctx = context.Background()
	}

	lifeCtx, finish, err := s.beginPump()
	if err != nil {
		return err
	}
	defer finish()

	operationCtx, cancel := context.WithCancelCause(ctx)
	stopLifeHook := context.AfterFunc(lifeCtx, func() {
		cancel(ErrRTCDeviceSinkClosed)
	})
	defer func() {
		stopLifeHook()
		cancel(nil)
	}()

	for {
		generation, blocked := s.playbackState()
		frame, err := inbound.ReadFrame(operationCtx)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return &RTCDeviceSinkError{DeviceID: s.id, Operation: "read", Err: err}
		}

		// InboundMedia returns receiver-owned storage that may be reused after
		// ReadFrame returns. Keep a private copy at this boundary so the device
		// adapter can never observe storage owned by the RTC implementation.
		samples := append([]int16(nil), frame.Samples...)
		write := func() error {
			return s.writePlayback(operationCtx, samples, generation, blocked)
		}
		var writeErr error
		if s.observer != nil {
			writeErr = s.observer.WritePlayback(operationCtx, samples, write)
		} else {
			writeErr = write()
		}
		if writeErr != nil {
			return &RTCDeviceSinkError{DeviceID: s.id, Operation: "write", Err: writeErr}
		}
	}
}

func (s *RTCDeviceSink) playbackState() (uint64, bool) {
	if s == nil {
		return 0, true
	}
	s.playbackMu.Lock()
	defer s.playbackMu.Unlock()
	return s.playbackGeneration, s.playbackBlocked
}

// writePlayback admits a frame only if the playback boundary is unchanged
// since its inbound read. Holding playbackMu across the device enqueue makes
// cancel and enqueue linearizable: cancellation either removes this frame or
// marks it stale before it can reach the local queue. The optional physical
// drain wait runs after the enqueue decision is settled and the mutex is
// released, so a concurrent barge-in cancel is never held up behind a slow
// native backend's playback pacing.
func (s *RTCDeviceSink) writePlayback(ctx context.Context, samples []int16, generation uint64, blocked bool) error {
	if s == nil || s.sink == nil {
		return ErrRTCDeviceSinkClosed
	}
	s.playbackMu.Lock()
	if blocked || s.playbackBlocked || generation != s.playbackGeneration {
		s.playbackMu.Unlock()
		return nil
	}
	err := s.sink.WriteFrame(ctx, samples)
	s.playbackMu.Unlock()
	if err != nil {
		return err
	}
	// Some native output backends accept frames into a queue before the
	// speaker consumes them. Wait for the optional drain boundary before the
	// feedback gate timestamps this frame, otherwise a fast provider response
	// can outrun the physical speaker by seconds.
	return s.sink.WaitForPlayback(ctx)
}

// Run is an alias for Pump for callers that model the binding as a lifecycle
// worker.
func (s *RTCDeviceSink) Run(ctx context.Context, inbound rtc.InboundMedia) error {
	return s.Pump(ctx, inbound)
}

// Close stops an active pump and releases the acquired output handle exactly
// once. The call waits for an in-flight ReadFrame or WriteFrame to observe the
// cancellation before returning.
func (s *RTCDeviceSink) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		done := s.runDone
		s.mu.Unlock()

		s.lifeCancel(ErrRTCDeviceSinkClosed)
		s.closeErr = s.sink.Close()
		if done != nil {
			<-done
		}
		if s.playbackObserver != nil {
			s.playbackObserver(s.id, s.sink.PlaybackStats())
		}
	})
	return s.closeErr
}

func (s *RTCDeviceSink) beginPump() (context.Context, func(), error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, nil, ErrRTCDeviceSinkClosed
	}
	if s.running {
		s.mu.Unlock()
		return nil, nil, ErrRTCDeviceSinkRunning
	}
	s.running = true
	s.runDone = make(chan struct{})
	lifeCtx := s.lifeCtx
	s.mu.Unlock()

	return lifeCtx, func() {
		s.mu.Lock()
		if s.running {
			s.running = false
			close(s.runDone)
			s.runDone = nil
		}
		s.mu.Unlock()
	}, nil
}

func nilRTCInboundMedia(media rtc.InboundMedia) bool {
	if media == nil {
		return true
	}
	value := reflect.ValueOf(media)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}
