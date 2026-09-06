package runtime

import devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
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
	DeviceID  devicegw.DeviceID
	Operation string
	Err       error
}

// RTCDevicePlaybackObserver receives one final, synchronized playback queue
// snapshot when the sink closes. The callback is invoked outside the audio
// device callback and is therefore suitable for session diagnostics.
type RTCDevicePlaybackObserver func(devicegw.DeviceID, audio.PlaybackQueueStats)

// RTCDevicePlaybackSamplesObserver observes PCM accepted by the selected
// playback device. Implementations must consume samples before returning;
// the slice is owned by the sink and is not retained for the callback.
type RTCDevicePlaybackSamplesObserver func(context.Context, int, []int16) error

// RTCDeviceCaptureSamplesObserver observes one owned PCM block at a capture
// edge. Implementations must copy samples they retain and return promptly.
type RTCDeviceCaptureSamplesObserver func(sampleRate int, samples []int16)

// RTCDeviceRenderedSamplesObserver observes full PCM, including underflow silence, rendered by the
// selected device callback, after enqueue pacing and cancellation.
type RTCDeviceRenderedSamplesObserver func(sampleRate int, samples []int16)

// RTCDeviceCaptureObserver receives the corresponding input queue snapshot at
// source teardown, outside the native callback.
type RTCDeviceCaptureObserver func(devicegw.DeviceID, audio.CaptureQueueStats)

// RTCDevicePlaybackReceiptObserver receives the applied result of a queued
// playback control command. It runs on the playback worker after the command
// has mutated (or rejected) the sink state and should return promptly.
type RTCDevicePlaybackReceiptObserver func(audio.PlaybackReceipt)

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
	snapshotStats           atomic.Pointer[audio.PlaybackQueueStats]
	snapshotEpoch           atomic.Uint64
	snapshotClosed          atomic.Bool
	sink                    *devicegw.DeviceSink
	id                      devicegw.DeviceID
	observer                PlaybackObserver
	providerRate            int
	deviceRate              int
	playbackObserver        func(devicegw.DeviceID, audio.PlaybackQueueStats)
	playbackReceiptObserver RTCDevicePlaybackReceiptObserver
	playbackSamplesObserver RTCDevicePlaybackSamplesObserver
	// loudness applies this session's fixed, voice-specific gain (see
	// audio voice-loudness table) to synthesized audio before it reaches the
	// feedback-gate observer or the device, so --voice selection does not
	// change local playback volume. This sink only ever plays one
	// participant's own un-mixed provider audio (a room's human participant
	// plays the room mix through a separate audio.DeviceSink, not this
	// type), so per-voice gain here can never double-apply to already-mixed
	// content.
	loudness *audio.LoudnessNormalizer

	lifeCtx     context.Context
	lifeCancel  context.CancelCauseFunc
	commands    *audio.PlaybackCommands
	commandDone chan struct{}

	mu        sync.Mutex
	closed    bool
	running   bool
	runDone   chan struct{}
	closeOnce sync.Once
	closeErr  error

	playbackMu         sync.Mutex
	playbackReceiptMu  sync.RWMutex
	playbackGeneration uint64
	playbackBlocked    bool
	playbackResponse   audio.PlaybackResponse
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

// NewRTCDeviceSink opens an output device through the shared audio registry.
// An empty id selects the registry's output default; a non-empty id is passed
// through as an exact stable device ID.
func NewRTCDeviceSink(registry devicegw.DeviceRegistry, id devicegw.DeviceID) (*RTCDeviceSink, error) {
	return newRTCDeviceSinkAtRate(registry, id, audio.SampleRate, "", nil)
}

// NewRTCDeviceSinkAtRate opens an output device for mono PCM16 at rate. A
// zero rate retains the compatibility default used by NewRTCDeviceSink.
func NewRTCDeviceSinkAtRate(registry devicegw.DeviceRegistry, id devicegw.DeviceID, rate int) (*RTCDeviceSink, error) {
	return newRTCDeviceSinkAtRate(registry, id, rate, "", nil)
}

// newRTCDeviceSinkAtRate opens the sink's output device. voice selects the
// fixed per-voice loudness gain applied to every frame this sink plays (see
// LoudnessNormalizer); an empty voice is the documented 0 dB no-op.
func newRTCDeviceSinkAtRate(registry devicegw.DeviceRegistry, id devicegw.DeviceID, rate int, voice string, playbackObserver RTCDevicePlaybackObserver) (*RTCDeviceSink, error) {
	if rate == 0 {
		rate = audio.SampleRate
	}
	sink, deviceRate, err := openRTCDeviceSinkAtRate(registry, id, rate)
	if err != nil {
		return nil, err
	}
	return newRTCDeviceSinkFromOpened(sink, deviceRate, rate, voice, playbackObserver), nil
}

func newRTCDeviceSinkFromOpened(sink *devicegw.DeviceSink, deviceRate, providerRate int, voice string, playbackObserver RTCDevicePlaybackObserver) *RTCDeviceSink {
	lifeCtx, lifeCancel := context.WithCancelCause(context.Background())
	commands, _ := audio.NewPlaybackCommands(32)
	result := &RTCDeviceSink{
		sink:             sink,
		commands:         commands,
		commandDone:      make(chan struct{}),
		id:               sink.DeviceID(),
		providerRate:     providerRate,
		deviceRate:       deviceRate,
		playbackObserver: playbackObserver,
		loudness:         audio.NewLoudnessNormalizer(audio.LoudnessNormalizerConfig{GainDB: audio.VoiceLoudnessGainDB(voice)}),
		lifeCtx:          lifeCtx,
		lifeCancel:       lifeCancel,
		holdToneConfig:   audio.DefaultHoldToneConfig(),
		holdToneTick:     defaultRTCDeviceHoldToneTick,
	}
	commands.SetReceiptObserver(result.observePlaybackReceipt)
	go result.runPlaybackCommands()
	return result
}

// NewDefaultRTCDeviceSink opens the directional output default from registry.
func NewDefaultRTCDeviceSink(registry devicegw.DeviceRegistry) (*RTCDeviceSink, error) {
	return NewRTCDeviceSink(registry, "")
}

// NewRTCDeviceSinkAtRateWithOptions opens an output worker with its voice and
// final queue observer configured at construction time.
func NewRTCDeviceSinkAtRateWithOptions(registry devicegw.DeviceRegistry, id devicegw.DeviceID, rate int, voice string, observer func(devicegw.DeviceID, audio.PlaybackQueueStats)) (*RTCDeviceSink, error) {
	return newRTCDeviceSinkAtRate(registry, id, rate, voice, observer)
}

// NewRTCDeviceSinkFromOpened adopts an already-opened output endpoint.
func NewRTCDeviceSinkFromOpened(sink *devicegw.DeviceSink, deviceRate, providerRate int, voice string, observer func(devicegw.DeviceID, audio.PlaybackQueueStats)) *RTCDeviceSink {
	return newRTCDeviceSinkFromOpened(sink, deviceRate, providerRate, voice, observer)
}

func (s *RTCDeviceSink) SetPlaybackObserver(observer PlaybackObserver) {
	if s != nil {
		s.observer = observer
	}
}

// SetPlaybackReceiptObserver routes every applied or rejected queued control
// receipt to the session runtime. The callback is independent of PCM queue
// snapshots and is retained for requests already admitted to the command
// queue.
func (s *RTCDeviceSink) SetPlaybackReceiptObserver(observer RTCDevicePlaybackReceiptObserver) {
	if s == nil {
		return
	}
	s.playbackReceiptMu.Lock()
	s.playbackReceiptObserver = observer
	s.playbackReceiptMu.Unlock()
}

func (s *RTCDeviceSink) observePlaybackReceipt(receipt audio.PlaybackReceipt) {
	if s == nil {
		return
	}
	s.playbackReceiptMu.RLock()
	observer := s.playbackReceiptObserver
	s.playbackReceiptMu.RUnlock()
	if observer != nil {
		observer(receipt)
	}
}

func (s *RTCDeviceSink) SetPlaybackSamplesObserver(observer func(context.Context, int, []int16) error) {
	if s != nil {
		s.playbackSamplesObserver = observer
	}
}

func (s *RTCDeviceSink) SetRenderedSamplesObserver(observer func(int, []int16)) bool {
	if s == nil || s.sink == nil || observer == nil {
		return false
	}
	return s.setRenderedSamplesObserver(observer)
}

// DeviceID returns the stable ID acquired by this sink.
func (s *RTCDeviceSink) DeviceID() devicegw.DeviceID {
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
	stats := s.sink.PlaybackStats()
	s.snapshotStats.Store(&stats)
	return stats
}

func (s *RTCDeviceSink) setRenderedSamplesObserver(observer RTCDeviceRenderedSamplesObserver) bool {
	if s == nil || s.sink == nil || observer == nil {
		return false
	}
	return s.sink.SetPlaybackRenderObserver(audio.PlaybackRenderObserver(observer))
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
	s.snapshotEpoch.Store(s.playbackGeneration)
	return s.sink.DiscardPlayback()
}

// InterruptPlayback returns the exact duration of model audio consumed by the
// device callback, then invalidates racing frames and discards queued audio.
func (s *RTCDeviceSink) InterruptPlayback(response audio.PlaybackResponse) (int, bool) {
	interruption, ok := s.interruptPlayback(response, true)
	return interruption.AudioEndMS, ok
}

// InterruptActivePlayback resolves interruption against the callback-consumed
// response span instead of the media reader's latest dequeued response. This
// remains correct when a fast provider has queued a continuation behind speech
// that is still physically audible.
func (s *RTCDeviceSink) InterruptActivePlayback() (audio.PlaybackInterruption, bool) {
	return s.interruptPlayback(audio.PlaybackResponse{}, false)
}

func (s *RTCDeviceSink) interruptPlayback(requested audio.PlaybackResponse, requireRequested bool) (audio.PlaybackInterruption, bool) {
	if s == nil || s.sink == nil || requireRequested && requested.ItemID == "" {
		return audio.PlaybackInterruption{}, false
	}
	s.playbackMu.Lock()
	defer s.playbackMu.Unlock()
	// Clear the native queue before sampling its callback clock. A callback
	// that wins the queue lock is counted as heard; one that runs after the
	// discard observes underflow, which leaves rendered-minus-underflow
	// unchanged. This makes truncation linearizable with discard.
	s.sink.DiscardPlayback()
	current := consumedPlaybackSamples(s.PlaybackStats())
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
	s.snapshotEpoch.Store(s.playbackGeneration)
	s.playbackResponse = audio.PlaybackResponse{}
	s.playbackSpans = nil
	if !found || s.deviceRate <= 0 || requireRequested && active.response != requested {
		return audio.PlaybackInterruption{}, false
	}
	heard := uint64(0)
	if current > active.start {
		heard = current - active.start
	}
	if maximum := active.end - active.start; heard > maximum {
		heard = maximum
	}
	return audio.PlaybackInterruption{
		PlaybackResponse: active.response,
		AudioEndMS:       int(heard * 1000 / uint64(s.deviceRate)),
	}, true
}

// Pump reads PCM frames from inbound and synchronously writes them to the
// output device at the audio adapter's fixed frame size and sample rate. A
// finite inbound endpoint ending with io.EOF is a clean pump completion. The
// method does not close the RTC endpoint because that endpoint belongs to its
// caller.
func (s *RTCDeviceSink) Pump(ctx context.Context, inbound audio.InboundMedia) error {
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
	if controlled, ok := inbound.(audio.PlaybackControlledInbound); ok {
		controlled.SetPlaybackController(audio.BufferedPlaybackController{Context: s.lifeCtx, Commands: s.commands})
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

	pending, err := audio.NewPlaybackProcessor(audio.PCM16DeviceFormat(s.providerRate), audio.PCM16DeviceFormat(s.deviceRate), audio.FrameSize)
	if err != nil {
		return &RTCDeviceSinkError{DeviceID: s.id, Operation: "configure", Err: err}
	}
	for {
		generation, blocked := s.playbackState()
		frame, err := inbound.ReadFrame(operationCtx)
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, audio.ErrSessionMediaClosed) {
				if flushErr := s.flushProviderPlayback(operationCtx, pending); flushErr != nil {
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

		if err := s.writeProviderFrame(operationCtx, pending, frame, generation, blocked); err != nil {
			return &RTCDeviceSinkError{DeviceID: s.id, Operation: "write", Err: err}
		}
	}
}

func (s *RTCDeviceSink) writeProviderFrame(ctx context.Context, pending *audio.PlaybackProcessor, providerFrame audio.PCMFrame, generation uint64, blocked bool) error {
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
	if err := s.observeHoldToneRealFrame(ctx, generation, blocked); err != nil {
		return err
	}
	providerFrame.Samples = samples
	frames, err := pending.Process(providerFrame, generation, blocked)
	if err != nil {
		return err
	}
	for _, frame := range frames {
		if len(frame.Samples) == 0 {
			continue
		}
		if err := s.observedWritePlayback(ctx, frame.Samples, generation, blocked, true); err != nil {
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

func (s *RTCDeviceSink) flushProviderPlayback(ctx context.Context, pending *audio.PlaybackProcessor) error {
	generation, blocked := s.playbackState()
	frames, err := pending.Flush(generation, blocked)
	if err != nil {
		return err
	}
	for _, final := range frames {
		if len(final.Samples) == 0 {
			continue
		}
		if err := s.observedWritePlayback(ctx, final.Samples, generation, blocked, true); err != nil {
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

func (s *RTCDeviceSink) playbackStateFor(response audio.PlaybackResponse) (uint64, bool) {
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
	statsBefore := s.PlaybackStats()
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

func (s *RTCDeviceSink) recordPlaybackSpanLocked(response audio.PlaybackResponse, start uint64, samples int) {
	if response.ItemID == "" || samples <= 0 {
		return
	}
	s.prunePlaybackSpansLocked(consumedPlaybackSamples(s.PlaybackStats()))
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
func (s *RTCDeviceSink) Run(ctx context.Context, inbound audio.InboundMedia) error {
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
		s.snapshotClosed.Store(true)
		done := s.runDone
		s.mu.Unlock()

		s.lifeCancel(ErrRTCDeviceSinkClosed)
		if s.commands != nil {
			s.commands.Close()
			<-s.commandDone
		}
		s.closeErr = s.sink.Close()
		if done != nil {
			<-done
		}
		if s.playbackObserver != nil {
			s.playbackObserver(s.id, s.PlaybackStats())
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

func nilRTCInboundMedia(media audio.InboundMedia) bool {
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

// WaitForPump waits for the active playback pump to finish. It is useful to
// drain provider media before closing the owning session.
func (s *RTCDeviceSink) WaitForPump(ctx context.Context) error {
	return s.waitForPump(ctx)
}

// WritePlayback admits one device-rate playback chunk through the same
// capacity, generation, observer, and receipt path used by the pump workers.
func (s *RTCDeviceSink) WritePlayback(ctx context.Context, samples []int16) error {
	generation, blocked := s.playbackState()
	return s.observedWritePlayback(ctx, samples, generation, blocked, true)
}

// PlaybackState reports the current playback generation and whether it is
// blocked by an accepted interruption.
func (s *RTCDeviceSink) PlaybackState() (uint64, bool) {
	return s.playbackState()
}

// WritePlaybackCue admits a local cue through the device queue while keeping
// it outside the model-audio playback receipt timeline.
func (s *RTCDeviceSink) WritePlaybackCue(ctx context.Context, samples []int16) error {
	generation, blocked := s.playbackState()
	return s.observedWritePlayback(ctx, samples, generation, blocked, false)
}

// IsNilInboundMedia reports whether an inbound endpoint is nil, including a
// typed nil hidden behind the media interface.
func IsNilInboundMedia(media audio.InboundMedia) bool { return nilRTCInboundMedia(media) }

// ConsumedPlaybackSamples returns the monotonic device samples consumed from
// a playback queue snapshot.
func ConsumedPlaybackSamples(stats audio.PlaybackQueueStats) uint64 {
	return consumedPlaybackSamples(stats)
}

// WriteDeviceFrame enqueues a native device-rate frame directly on the
// underlying output handle. It is reserved for device-tier diagnostics and
// feeder paths; provider media should use Pump or WritePlayback.
func (s *RTCDeviceSink) WriteDeviceFrame(ctx context.Context, samples []int16) error {
	if s == nil || s.sink == nil {
		return ErrRTCDeviceSinkClosed
	}
	return s.sink.WriteFrame(ctx, samples)
}
