package services

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sync"
	"time"

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

// RTCDevicePlaybackSamplesObserver observes PCM accepted by the selected
// playback device. Implementations must consume samples before returning;
// the slice is owned by the sink and is not retained for the callback.
type RTCDevicePlaybackSamplesObserver func(context.Context, int, []int16) error

// RTCDeviceCaptureObserver receives the corresponding input queue snapshot at
// source teardown, outside the native callback.
type RTCDeviceCaptureObserver func(audio.DeviceID, audio.CaptureQueueStats)

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
	sink                    *audio.DeviceSink
	id                      audio.DeviceID
	observer                rtcDevicePlaybackObserver
	providerRate            int
	deviceRate              int
	playbackObserver        RTCDevicePlaybackObserver
	playbackSamplesObserver RTCDevicePlaybackSamplesObserver
	// loudness applies this session's fixed, voice-specific gain (see
	// VoiceLoudnessGainDB) to synthesized audio before it reaches the
	// feedback-gate observer or the device, so --voice selection does not
	// change local playback volume. This sink only ever plays one
	// participant's own un-mixed provider audio (a room's human participant
	// plays the room mix through a separate audio.DeviceSink, not this
	// type), so per-voice gain here can never double-apply to already-mixed
	// content.
	loudness *audio.LoudnessNormalizer

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
	playbackResponse   rtc.PlaybackResponse
	playbackSpans      []rtcDevicePlaybackSpan
	// pacingMu serializes producers across capacity admission and enqueue.
	// The provider pump and hold-tone filler are independent goroutines; without
	// this boundary they can both observe space below the high watermark and
	// then over-admit. DiscardPlayback intentionally does not take pacingMu so
	// cancellation can wake a producer blocked in capacity admission.
	pacingMu sync.Mutex

	// holdToneConfig and holdToneTick configure the "still here" cue that
	// fills a customer-facing gap once no real assistant audio has reached
	// this device for longer than an ordinary conversational pause (see
	// rtc_device_hold_tone.go). Tests may override both directly since this
	// file and its tests share package services.
	holdToneConfig audio.HoldToneConfig
	holdToneTick   time.Duration

	holdToneMu     sync.Mutex
	holdToneFiller *audio.HoldToneFiller
}

// rtcDevicePlaybackSpan maps one provider response onto the monotonic count
// of non-underflow samples consumed by the physical device. Multiple response
// spans may be queued at once; that is what allows a tool continuation to stay
// gapless without confusing the latest provider response with the one that is
// currently audible.
type rtcDevicePlaybackSpan struct {
	response rtc.PlaybackResponse
	start    uint64
	end      uint64
	complete bool
}

// NewRTCDeviceSink opens an output device through the shared audio registry.
// An empty id selects the registry's output default; a non-empty id is passed
// through as an exact stable device ID.
func NewRTCDeviceSink(registry audio.DeviceRegistry, id audio.DeviceID) (*RTCDeviceSink, error) {
	return newRTCDeviceSinkAtRate(registry, id, audio.SampleRate, "", nil)
}

// NewRTCDeviceSinkAtRate opens an output device for mono PCM16 at rate. A
// zero rate retains the compatibility default used by NewRTCDeviceSink.
func NewRTCDeviceSinkAtRate(registry audio.DeviceRegistry, id audio.DeviceID, rate int) (*RTCDeviceSink, error) {
	return newRTCDeviceSinkAtRate(registry, id, rate, "", nil)
}

// newRTCDeviceSinkAtRate opens the sink's output device. voice selects the
// fixed per-voice loudness gain applied to every frame this sink plays (see
// LoudnessNormalizer); an empty voice is the documented 0 dB no-op.
func newRTCDeviceSinkAtRate(registry audio.DeviceRegistry, id audio.DeviceID, rate int, voice string, playbackObserver RTCDevicePlaybackObserver) (*RTCDeviceSink, error) {
	if rate == 0 {
		rate = audio.SampleRate
	}
	sink, deviceRate, err := openRTCDeviceSinkAtRate(registry, id, rate)
	if err != nil {
		return nil, err
	}
	return newRTCDeviceSinkFromOpened(sink, deviceRate, rate, voice, playbackObserver), nil
}

func newRTCDeviceSinkFromOpened(sink *audio.DeviceSink, deviceRate, providerRate int, voice string, playbackObserver RTCDevicePlaybackObserver) *RTCDeviceSink {
	lifeCtx, lifeCancel := context.WithCancelCause(context.Background())
	return &RTCDeviceSink{
		sink:             sink,
		id:               sink.DeviceID(),
		providerRate:     providerRate,
		deviceRate:       deviceRate,
		playbackObserver: playbackObserver,
		loudness:         audio.NewLoudnessNormalizer(audio.LoudnessNormalizerConfig{GainDB: VoiceLoudnessGainDB(voice)}),
		lifeCtx:          lifeCtx,
		lifeCancel:       lifeCancel,
		holdToneConfig:   audio.DefaultHoldToneConfig(),
		holdToneTick:     defaultRTCDeviceHoldToneTick,
	}
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

// SampleRate reports the negotiated PCM16 rate the output device was opened
// at. Callers that must declare the true rate of frames accepted by this sink
// (rather than assume the provider's requested rate) should use this instead
// of the request's OutputSampleRate, since device negotiation can differ.
func (s *RTCDeviceSink) SampleRate() int {
	if s == nil || s.sink == nil {
		return 0
	}
	return s.sink.SampleRate()
}

// ProviderSampleRate reports the rate supplied by the provider media endpoint.
func (s *RTCDeviceSink) ProviderSampleRate() int {
	if s == nil {
		return 0
	}
	return s.providerRate
}

// DeviceSampleRate reports the native rate selected for local playback.
func (s *RTCDeviceSink) DeviceSampleRate() int {
	if s == nil {
		return 0
	}
	return s.deviceRate
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

// StartPlayback opens a provider response on the local device clock. The
// consumed-sample baseline is captured immediately before the first model
// frame is admitted, so idle underflow and hold-tone samples are excluded.
func (s *RTCDeviceSink) StartPlayback(response rtc.PlaybackResponse) {
	if s == nil || s.sink == nil || response.ItemID == "" {
		return
	}
	s.playbackMu.Lock()
	if s.playbackResponse == response && !s.playbackBlocked {
		s.playbackMu.Unlock()
		return
	}
	if s.playbackBlocked {
		s.playbackBlocked = false
		s.playbackGeneration++
	}
	s.playbackResponse = response
	s.playbackMu.Unlock()
}

// InterruptPlayback returns the exact duration of model audio consumed by the
// device callback, then invalidates racing frames and discards queued audio.
func (s *RTCDeviceSink) InterruptPlayback(response rtc.PlaybackResponse) (int, bool) {
	interruption, ok := s.interruptPlayback(response, true)
	return interruption.AudioEndMS, ok
}

// InterruptActivePlayback resolves interruption against the callback-consumed
// response span instead of the media reader's latest dequeued response. This
// remains correct when a fast provider has already queued a tool continuation
// behind speech that is still physically audible.
func (s *RTCDeviceSink) InterruptActivePlayback() (rtc.PlaybackInterruption, bool) {
	return s.interruptPlayback(rtc.PlaybackResponse{}, false)
}

func (s *RTCDeviceSink) interruptPlayback(requested rtc.PlaybackResponse, requireRequested bool) (rtc.PlaybackInterruption, bool) {
	if s == nil || s.sink == nil || requireRequested && requested.ItemID == "" {
		return rtc.PlaybackInterruption{}, false
	}
	s.playbackMu.Lock()
	defer s.playbackMu.Unlock()
	// Clear the native queue before sampling its callback clock. A callback
	// that wins the queue lock is counted as heard; one that runs after the
	// discard observes underflow, which leaves rendered-minus-underflow
	// unchanged. This makes the truncation cursor linearizable with discard.
	s.sink.DiscardPlayback()
	current := consumedPlaybackSamples(s.sink.PlaybackStats())
	var active rtcDevicePlaybackSpan
	found := false
	for _, span := range s.playbackSpans {
		if requireRequested {
			if span.response == requested && current > span.start && !(span.complete && current >= span.end) {
				active = span
				found = true
				break
			}
		} else if current < span.end {
			active = span
			found = true
			break
		}
	}
	if !found && !requireRequested && s.playbackResponse.ItemID != "" {
		active = rtcDevicePlaybackSpan{response: s.playbackResponse}
		found = true
	}
	s.playbackBlocked = true
	s.playbackGeneration++
	s.playbackResponse = rtc.PlaybackResponse{}
	s.playbackSpans = nil
	if !found || s.deviceRate <= 0 || requireRequested && active.response != requested {
		return rtc.PlaybackInterruption{}, false
	}
	heard := uint64(0)
	if current > active.start {
		heard = current - active.start
	}
	if maximum := active.end - active.start; heard > maximum {
		heard = maximum
	}
	return rtc.PlaybackInterruption{
		PlaybackResponse: active.response,
		AudioEndMS:       int(heard * 1000 / uint64(s.deviceRate)),
	}, true
}

// finishPlayback retires a fully drained provider response from the device
// clock. A later server-VAD event belongs to a new user turn and must not
// truncate the completed response, even if local hold-tone audio has played
// during the intervening silence.
func (s *RTCDeviceSink) finishPlayback(response rtc.PlaybackResponse) {
	if s == nil || response.ItemID == "" {
		return
	}
	s.playbackMu.Lock()
	defer s.playbackMu.Unlock()
	if s.playbackResponse != response {
		return
	}
	for index := len(s.playbackSpans) - 1; index >= 0; index-- {
		if s.playbackSpans[index].response == response {
			s.playbackSpans[index].complete = true
			break
		}
	}
	s.playbackGeneration++
	s.playbackResponse = rtc.PlaybackResponse{}
}

func consumedPlaybackSamples(stats audio.PlaybackQueueStats) uint64 {
	if stats.RenderedSamples < stats.UnderflowSamples {
		return 0
	}
	return stats.RenderedSamples - stats.UnderflowSamples
}

// resumePlayback opens a new local response boundary. Frames read under a
// prior generation remain stale even if they race with this transition.
func (s *RTCDeviceSink) resumePlayback() {
	if s == nil || s.sink == nil {
		return
	}
	s.playbackMu.Lock()
	// A normal tool-result continuation can request another response while the
	// preceding response is still draining to the physical device. Playback is
	// already open in that case: advancing the generation would make a frame
	// read just before response.create stale and discard its samples. Only a
	// prior accepted cancellation sets playbackBlocked and requires a new
	// generation boundary.
	if !s.playbackBlocked {
		s.playbackMu.Unlock()
		return
	}
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
	if controlled, ok := inbound.(rtc.PlaybackControlledInbound); ok {
		controlled.SetPlaybackController(s)
		defer controlled.SetPlaybackController(nil)
	}

	operationCtx, cancel := context.WithCancelCause(ctx)
	stopLifeHook := context.AfterFunc(lifeCtx, func() {
		cancel(ErrRTCDeviceSinkClosed)
	})
	defer func() {
		stopLifeHook()
		cancel(nil)
	}()

	stopHoldTone := s.startHoldTone(operationCtx)
	defer stopHoldTone()

	var pending rtcDevicePlaybackBuffer
	for {
		generation, blocked := s.playbackState()
		frame, err := inbound.ReadFrame(operationCtx)
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, rtc.ErrSessionMediaClosed) {
				if flushErr := s.flushProviderPlayback(operationCtx, &pending); flushErr != nil {
					return &RTCDeviceSinkError{DeviceID: s.id, Operation: "write", Err: flushErr}
				}
				if drainErr := s.sink.WaitForPlayback(operationCtx); drainErr != nil {
					return &RTCDeviceSinkError{DeviceID: s.id, Operation: "drain", Err: drainErr}
				}
				return nil
			}
			return &RTCDeviceSinkError{DeviceID: s.id, Operation: "read", Err: err}
		}
		if frame.PlaybackResponse.ItemID != "" {
			generation, blocked = s.playbackStateFor(frame.PlaybackResponse)
		}

		if err := s.writeProviderFrame(operationCtx, &pending, frame, generation, blocked); err != nil {
			return &RTCDeviceSinkError{DeviceID: s.id, Operation: "write", Err: err}
		}
	}
}

func (s *RTCDeviceSink) writeProviderFrame(ctx context.Context, pending *rtcDevicePlaybackBuffer, providerFrame rtc.PCMFrame, generation uint64, blocked bool) error {
	// InboundMedia returns receiver-owned storage that may be reused after
	// ReadFrame returns. Normalizing (when enabled) and resampling to the
	// device rate below always produce a private copy, so no additional
	// defensive copy is needed at this boundary.
	samples := providerFrame.Samples
	if s.loudness != nil {
		// Normalize at the provider's own rate, before resampling, so the
		// feedback-suppression gate (which observes exactly what this sink
		// writes to the device) sees the same corrected audio a listener
		// actually hears.
		samples = s.loudness.Process(samples)
	}
	converted, err := s.deviceFrame(samples)
	if err != nil {
		return err
	}
	if err := s.observeHoldToneRealFrame(ctx, generation, blocked); err != nil {
		return err
	}
	frames := pending.add(converted, generation, blocked)
	for _, frame := range frames {
		if err := s.observedWritePlayback(ctx, frame, generation, blocked, true); err != nil {
			return err
		}
	}
	if providerFrame.EndOfResponse {
		if err := s.flushProviderPlayback(ctx, pending); err != nil {
			return err
		}
		s.finishPlayback(providerFrame.PlaybackResponse)
	}
	return nil
}

func (s *RTCDeviceSink) flushProviderPlayback(ctx context.Context, pending *rtcDevicePlaybackBuffer) error {
	generation, blocked := s.playbackState()
	final := pending.flush(generation, blocked)
	if len(final) > 0 {
		if err := s.observedWritePlayback(ctx, final, generation, blocked, true); err != nil {
			return err
		}
	}
	// Response boundaries are metadata, not physical drain points. Capacity
	// pacing keeps the queue bounded while the response-span ledger preserves
	// exact callback-clock interruption identity across a queued continuation.
	return nil
}

// observedWritePlayback routes a device-rate playback chunk through the
// optional self-hearing observer before it reaches the device adapter, so a
// local feedback gate always sees exactly the PCM this sink accepted.
func (s *RTCDeviceSink) observedWritePlayback(ctx context.Context, samples []int16, generation uint64, blocked, modelAudio bool) error {
	write := func() error {
		return s.writePlayback(ctx, samples, generation, blocked, modelAudio)
	}
	if s.observer != nil {
		return s.observer.WritePlayback(ctx, samples, write)
	}
	return write()
}

func (s *RTCDeviceSink) playbackState() (uint64, bool) {
	if s == nil {
		return 0, true
	}
	s.playbackMu.Lock()
	defer s.playbackMu.Unlock()
	return s.playbackGeneration, s.playbackBlocked
}

func (s *RTCDeviceSink) playbackStateFor(response rtc.PlaybackResponse) (uint64, bool) {
	if s == nil {
		return 0, true
	}
	s.playbackMu.Lock()
	defer s.playbackMu.Unlock()
	blocked := s.playbackBlocked
	if response.ItemID != "" && response != s.playbackResponse {
		blocked = true
	}
	return s.playbackGeneration, blocked
}

// writePlayback admits a frame only if the playback boundary is unchanged
// since its inbound read. Holding playbackMu across the device enqueue makes
// cancel and enqueue linearizable: cancellation either removes this frame or
// marks it stale before it can reach the local queue. The optional physical
// drain wait runs after the enqueue decision is settled and the mutex is
// released, so a concurrent barge-in cancel is never held up behind a slow
// native backend's playback pacing.
func (s *RTCDeviceSink) writePlayback(ctx context.Context, samples []int16, generation uint64, blocked, modelAudio bool) error {
	if s == nil || s.sink == nil {
		return ErrRTCDeviceSinkClosed
	}
	// Serialize producers across capacity admission and enqueue. Apply
	// backpressure before taking playbackMu so a device callback or
	// barge-in discard can keep draining while a provider burst is throttled.
	// Revalidate the response generation after the wait before admitting the
	// samples to the device queue.
	s.pacingMu.Lock()
	defer s.pacingMu.Unlock()
	if err := s.sink.WaitForPlaybackCapacity(ctx, len(samples)); err != nil {
		return err
	}
	s.playbackMu.Lock()
	if blocked || s.playbackBlocked || generation != s.playbackGeneration {
		s.playbackMu.Unlock()
		return nil
	}
	statsBefore := s.sink.PlaybackStats()
	response := s.playbackResponse
	var err error
	if len(samples) == audio.FrameSize {
		err = s.sink.WriteFrame(ctx, samples)
	} else {
		err = s.sink.WriteSamples(ctx, samples)
	}
	if err == nil && modelAudio && response.ItemID != "" {
		s.recordPlaybackSpanLocked(response, consumedPlaybackSamples(statsBefore)+uint64(statsBefore.QueuedSamples), len(samples))
	}
	if err == nil && s.playbackSamplesObserver != nil {
		err = s.playbackSamplesObserver(ctx, s.deviceRate, samples)
	}
	s.playbackMu.Unlock()
	return err
}

func (s *RTCDeviceSink) recordPlaybackSpanLocked(response rtc.PlaybackResponse, start uint64, samples int) {
	if response.ItemID == "" || samples <= 0 {
		return
	}
	s.prunePlaybackSpansLocked(consumedPlaybackSamples(s.sink.PlaybackStats()))
	end := start + uint64(samples)
	if count := len(s.playbackSpans); count > 0 {
		last := &s.playbackSpans[count-1]
		if last.response == response && last.end == start {
			last.end = end
			return
		}
	}
	s.playbackSpans = append(s.playbackSpans, rtcDevicePlaybackSpan{response: response, start: start, end: end})
}

func (s *RTCDeviceSink) prunePlaybackSpansLocked(consumed uint64) {
	first := 0
	for first < len(s.playbackSpans) && s.playbackSpans[first].end <= consumed {
		first++
	}
	if first > 0 {
		copy(s.playbackSpans, s.playbackSpans[first:])
		s.playbackSpans = s.playbackSpans[:len(s.playbackSpans)-first]
	}
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

// waitForPump waits for the provider-media pump to finish without cancelling
// it. A graceful session close first seals the provider-owned inbound media;
// the pump can then consume its already-accepted FIFO and wait for the native
// playback queue to reach the physical device before the binding is released.
func (s *RTCDeviceSink) waitForPump(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	done := s.runDone
	s.mu.Unlock()
	if done == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
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
