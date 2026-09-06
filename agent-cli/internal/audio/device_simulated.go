package audio

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/observability"
)

const SimulatedDuplexBackendName = "simulated-duplex"

// ClockSpec describes one deterministic device callback clock. Quanta repeats
// when more callbacks are advanced than entries supplied. JitterSamples is a
// deterministic diagnostic offset; it never relies on host scheduling.
type ClockSpec struct {
	NominalRate   int   `json:"nominal_rate"`
	Quanta        []int `json:"quanta"`
	DriftPPM      int   `json:"drift_ppm,omitempty"`
	JitterSamples []int `json:"jitter_samples,omitempty"`
}

type QueueSpec struct {
	LatencyNanos int64  `json:"latency_nanos"`
	DropPolicy   string `json:"drop_policy"`
}

// AcousticSpec is an integer-domain deterministic acoustic path. GainQ15 is
// 32768 for unity. ImpulseResponseQ15 may be omitted for a pure delayed tap.
type AcousticSpec struct {
	DelaySamples       int     `json:"delay_samples,omitempty"`
	GainQ15            int32   `json:"gain_q15,omitempty"`
	ImpulseResponseQ15 []int16 `json:"impulse_response_q15,omitempty"`
	NearEnd            []int16 `json:"-"`
	Background         []int16 `json:"-"`
}

type FaultType string

const (
	FaultMissingCallback   FaultType = "missing_callback"
	FaultDuplicateCallback FaultType = "duplicate_callback"
	FaultClockReset        FaultType = "clock_reset"
	FaultDeviceLoss        FaultType = "device_loss"
	FaultForwardJump       FaultType = "forward_jump"
	FaultBackwardJump      FaultType = "backward_jump"
)

type FaultEvent struct {
	Callback  uint64    `json:"callback"`
	Direction Direction `json:"direction"`
	Type      FaultType `json:"type"`
	Samples   int64     `json:"samples,omitempty"`
	ID        string    `json:"id,omitempty"`
}

type DuplexScenario struct {
	Seed          uint64       `json:"seed"`
	Render        ClockSpec    `json:"render"`
	Capture       ClockSpec    `json:"capture"`
	PlaybackQueue QueueSpec    `json:"playback_queue"`
	CaptureQueue  QueueSpec    `json:"capture_queue"`
	Acoustic      AcousticSpec `json:"acoustic"`
	Faults        []FaultEvent `json:"faults,omitempty"`
}

type DeviceTraceEvent struct {
	Tap             string   `json:"tap"`
	Sequence        uint64   `json:"sequence"`
	ClockEpoch      uint32   `json:"clock_epoch"`
	SampleRate      int      `json:"sample_rate"`
	StartSample     uint64   `json:"start_sample"`
	SampleCount     int      `json:"sample_count"`
	DeviceTick      uint64   `json:"device_tick"`
	HostMonoSamples int64    `json:"host_mono_samples"`
	QueueBefore     int      `json:"queue_before"`
	QueueAfter      int      `json:"queue_after"`
	Flags           []string `json:"flags,omitempty"`
	FaultID         string   `json:"fault_id,omitempty"`
	PayloadSHA256   string   `json:"payload_sha256"`
}

type CaptureQueueStats struct {
	QueuedSamples    int
	HighWaterSamples int
	CapturedSamples  uint64
	CompletedFrames  uint64
	DroppedFrames    uint64
	DroppedSamples   uint64
	DropPolicy       string
	SequenceGaps     uint64
}

// SimulatedDuplexRegistry is an explicitly advanced, callback-clocked device
// backend. It intentionally coexists with VirtualRegistry: exact loopback
// tests stay timeless while temporal tests call Advance.
type SimulatedDuplexRegistry struct {
	mu                              sync.Mutex
	scenario                        DuplexScenario
	format                          DeviceFormat
	input, output                   Device
	playback                        *PlaybackQueue
	capture                         []int16
	captureCapacity                 int
	captureStats                    CaptureQueueStats
	captureRemainder                int
	rendered, captured, acoustic    []int16
	acousticHistory                 []int16
	trace                           []DeviceTraceEvent
	renderCallback, captureCallback uint64
	renderPosition, capturePosition uint64
	renderEpoch, captureEpoch       uint32
	nearPosition, noisePosition     int
	open                            [2]int
	lost                            [2]bool
	changed                         chan struct{}
	metricSampler                   observability.MetricSampler
	logger                          observability.Logger
	observedPlayback                PlaybackQueueStats
	observedCapture                 CaptureQueueStats
}

func NewSimulatedDuplexRegistry(s DuplexScenario) (*SimulatedDuplexRegistry, error) {
	return NewSimulatedDuplexRegistryWithObservability(s, nil, nil)
}

// NewSimulatedDuplexRegistryWithObservability attaches the same application
// ports used by live composition. Sampling occurs after Advance releases the
// simulator lock, matching the native rule that observers never run in an
// audio callback critical section.
func NewSimulatedDuplexRegistryWithObservability(s DuplexScenario, sampler observability.MetricSampler, logger observability.Logger) (*SimulatedDuplexRegistry, error) {
	if err := validateClockSpec("render", s.Render); err != nil {
		return nil, err
	}
	if err := validateClockSpec("capture", s.Capture); err != nil {
		return nil, err
	}
	if s.Render.NominalRate != s.Capture.NominalRate {
		return nil, fmt.Errorf("simulated duplex currently requires one negotiated PCM rate; render=%d capture=%d", s.Render.NominalRate, s.Capture.NominalRate)
	}
	format := PCM16DeviceFormat(s.Render.NominalRate)
	latency := DefaultPlaybackLatencyTarget
	if s.PlaybackQueue.LatencyNanos > 0 {
		latency = durationFromNanos(s.PlaybackQueue.LatencyNanos)
	}
	playback, err := NewPlaybackQueueWithLatency(format, latency)
	if err != nil {
		return nil, err
	}
	captureCapacity, err := PlaybackQueueCapacity(format, DefaultPlaybackLatencyTarget)
	if err != nil {
		return nil, err
	}
	if s.CaptureQueue.LatencyNanos > 0 {
		captureCapacity, err = PlaybackQueueCapacity(format, durationFromNanos(s.CaptureQueue.LatencyNanos))
		if err != nil {
			return nil, err
		}
	}
	if s.CaptureQueue.DropPolicy == "" {
		s.CaptureQueue.DropPolicy = "drop_oldest"
	}
	if s.CaptureQueue.DropPolicy != "drop_oldest" && s.CaptureQueue.DropPolicy != "drop_newest" {
		return nil, fmt.Errorf("capture drop policy %q is unsupported", s.CaptureQueue.DropPolicy)
	}
	if s.Acoustic.GainQ15 == 0 {
		s.Acoustic.GainQ15 = 32768
	}
	input, _ := NewDevice(SimulatedDuplexBackendName, "input", "Simulated Duplex Input", DirectionInput)
	output, _ := NewDevice(SimulatedDuplexBackendName, "output", "Simulated Duplex Output", DirectionOutput)
	return &SimulatedDuplexRegistry{
		scenario: s, format: format, input: input, output: output,
		playback: playback, captureCapacity: captureCapacity,
		captureStats:  CaptureQueueStats{DropPolicy: s.CaptureQueue.DropPolicy},
		changed:       make(chan struct{}),
		acoustic:      make([]int16, s.Acoustic.DelaySamples),
		metricSampler: observability.EnsureMetricSampler(sampler),
		logger:        observability.EnsureLogger(logger),
	}, nil
}

func durationFromNanos(n int64) timeDuration { return timeDuration(n) }

// timeDuration is kept as an alias to make scenario JSON explicitly nanos
// while passing the standard duration type to the production queue.
type timeDuration = time.Duration

func validateClockSpec(name string, c ClockSpec) error {
	if c.NominalRate <= 0 {
		return fmt.Errorf("%s clock rate must be positive", name)
	}
	if len(c.Quanta) == 0 {
		return fmt.Errorf("%s clock needs at least one callback quantum", name)
	}
	for _, q := range c.Quanta {
		if q <= 0 {
			return fmt.Errorf("%s callback quantum must be positive", name)
		}
	}
	if c.DriftPPM <= -1_000_000 {
		return fmt.Errorf("%s drift %d ppm stops or reverses time", name, c.DriftPPM)
	}
	return nil
}

func (r *SimulatedDuplexRegistry) List() ([]Device, error) { return []Device{r.input, r.output}, nil }
func (r *SimulatedDuplexRegistry) Default(d Direction) (Device, error) {
	if d == DirectionInput {
		return r.input, nil
	}
	if d == DirectionOutput {
		return r.output, nil
	}
	return Device{}, &InvalidDirectionError{Direction: d}
}
func (r *SimulatedDuplexRegistry) Open(id DeviceID) (OpenedDevice, error) {
	return r.OpenWithFormat(id, r.format)
}
func (r *SimulatedDuplexRegistry) OpenWithFormat(id DeviceID, f DeviceFormat) (OpenedDevice, error) {
	if !f.equal(r.format) {
		return nil, &DeviceFormatError{ID: id, Requested: f, Available: []DeviceFormat{r.format}}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	direction := DirectionInput
	if id == r.output.ID {
		direction = DirectionOutput
	} else if id != r.input.ID {
		return nil, NewDeviceNotFoundError(id)
	}
	i := side(direction)
	if r.lost[i] {
		return nil, &DeviceLostError{ID: id, Direction: direction}
	}
	r.open[i]++
	return &SimulatedDuplexStream{registry: r, direction: direction, id: id}, nil
}

type SimulatedDuplexStream struct {
	registry  *SimulatedDuplexRegistry
	direction Direction
	id        DeviceID
	once      sync.Once
}

func (s *SimulatedDuplexStream) DeviceFormat() DeviceFormat { return s.registry.format }
func (s *SimulatedDuplexStream) WriteFrame(ctx context.Context, frame []int16) error {
	if err := validateFrame("write", frame); err != nil {
		return err
	}
	return s.WriteSamples(ctx, frame)
}
func (s *SimulatedDuplexStream) WriteSamples(ctx context.Context, samples []int16) error {
	if s.direction != DirectionOutput {
		return fmt.Errorf("audio device %q is input-only", s.id)
	}
	if err := contextError(ctx); err != nil {
		return err
	}
	s.registry.mu.Lock()
	defer s.registry.mu.Unlock()
	if s.registry.lost[side(s.direction)] {
		return &DeviceLostError{ID: s.id, Direction: s.direction}
	}
	s.registry.playback.Enqueue(append([]int16(nil), samples...))
	s.registry.signalLocked()
	return nil
}
func (s *SimulatedDuplexStream) ReadFrame(ctx context.Context, frame []int16) error {
	if s.direction != DirectionInput {
		return fmt.Errorf("audio device %q is output-only", s.id)
	}
	if err := validateFrame("read", frame); err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		s.registry.mu.Lock()
		if s.registry.lost[side(s.direction)] {
			s.registry.mu.Unlock()
			return &DeviceLostError{ID: s.id, Direction: s.direction}
		}
		if len(s.registry.capture) >= len(frame) {
			copy(frame, s.registry.capture[:len(frame)])
			s.registry.capture = s.registry.capture[len(frame):]
			s.registry.captureStats.QueuedSamples = len(s.registry.capture)
			s.registry.signalLocked()
			s.registry.mu.Unlock()
			return nil
		}
		wake := s.registry.changed
		s.registry.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-wake:
		}
	}
}
func (s *SimulatedDuplexStream) Read(ctx context.Context) ([]byte, error) {
	frame := make([]int16, FrameSize)
	if err := s.ReadFrame(ctx, frame); err != nil {
		return nil, err
	}
	out := make([]byte, FrameSize*2)
	encodePCM16(out, frame)
	return out, nil
}
func (s *SimulatedDuplexStream) Write(ctx context.Context, raw []byte) error {
	if len(raw)%2 != 0 {
		return fmt.Errorf("malformed PCM16 byte count %d", len(raw))
	}
	samples := make([]int16, len(raw)/2)
	decodePCM16(samples, raw)
	return s.WriteSamples(ctx, samples)
}
func (s *SimulatedDuplexStream) PlaybackStats() PlaybackQueueStats {
	return s.registry.playback.Snapshot()
}

func (s *SimulatedDuplexStream) SetPlaybackRenderObserver(observer PlaybackRenderObserver) {
	if s == nil || s.registry == nil || s.direction != DirectionOutput {
		return
	}
	s.registry.playback.SetRenderObserver(observer)
}
func (s *SimulatedDuplexStream) CaptureStats() CaptureQueueStats {
	return s.registry.CaptureStats()
}
func (s *SimulatedDuplexStream) DiscardPlayback() int {
	n := s.registry.playback.Discard()
	s.registry.mu.Lock()
	s.registry.signalLocked()
	s.registry.mu.Unlock()
	return n
}
func (s *SimulatedDuplexStream) WaitForPlaybackCapacity(ctx context.Context, samples int) error {
	if s.direction != DirectionOutput {
		return nil
	}
	_, high, err := PlaybackQueueWatermarks(s.registry.format)
	if err != nil {
		return err
	}
	if samples > high {
		return ErrInvalidPlaybackQueue
	}
	for {
		if s.registry.playback.Snapshot().QueuedSamples+samples <= high {
			return nil
		}
		s.registry.mu.Lock()
		wake := s.registry.changed
		s.registry.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-wake:
		}
	}
}
func (s *SimulatedDuplexStream) WaitForPlayback(ctx context.Context) error {
	for {
		if s.registry.playback.Snapshot().QueuedSamples == 0 {
			return nil
		}
		s.registry.mu.Lock()
		wake := s.registry.changed
		s.registry.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-wake:
		}
	}
}
func (s *SimulatedDuplexStream) Close() error {
	s.once.Do(func() {
		s.registry.mu.Lock()
		s.registry.open[side(s.direction)]--
		s.registry.signalLocked()
		s.registry.mu.Unlock()
	})
	return nil
}

// Advance executes count render/capture callback pairs in stable render-first
// ordering. All time and media positions are integer sample positions.
func (r *SimulatedDuplexRegistry) Advance(count int) error {
	if count < 0 {
		return errors.New("simulated callback count must not be negative")
	}
	r.mu.Lock()
	traceStart := len(r.trace)
	for i := 0; i < count; i++ {
		if err := r.advanceRenderLocked(); err != nil {
			r.mu.Unlock()
			return err
		}
		if err := r.advanceCaptureLocked(); err != nil {
			r.mu.Unlock()
			return err
		}
	}
	r.signalLocked()
	events := append([]DeviceTraceEvent(nil), r.trace[traceStart:]...)
	playback := r.playback.Snapshot()
	capture := r.captureStats
	previousPlayback := r.observedPlayback
	previousCapture := r.observedCapture
	r.observedPlayback = playback
	r.observedCapture = capture
	r.mu.Unlock()
	r.observeEvents(events)
	r.observeQueueDeltas(playback, previousPlayback, capture, previousCapture)
	return nil
}

func (r *SimulatedDuplexRegistry) observeEvents(events []DeviceTraceEvent) {
	for _, event := range events {
		fields := observability.Fields{
			"backend":     SimulatedDuplexBackendName,
			"tap":         event.Tap,
			"clock_epoch": strconv.FormatUint(uint64(event.ClockEpoch), 10),
			"sample_rate": strconv.Itoa(event.SampleRate),
		}
		_ = observability.TrySample(context.Background(), r.metricSampler, observability.MetricSample{
			Name: "audio.device.callbacks", Kind: "counter", Value: 1, Unit: "callbacks", Fields: fields,
		})
		_ = observability.TrySample(context.Background(), r.metricSampler, observability.MetricSample{
			Name: "audio.device.queue.depth", Kind: "gauge", Value: float64(event.QueueAfter), Unit: "samples", Fields: fields,
		})
		if len(event.Flags) == 0 {
			continue
		}
		faultFields := observability.Fields{
			"backend":     SimulatedDuplexBackendName,
			"tap":         event.Tap,
			"clock_epoch": strconv.FormatUint(uint64(event.ClockEpoch), 10),
			"flags":       strings.Join(event.Flags, ","),
			"fault_id":    event.FaultID,
		}
		_ = observability.TrySample(context.Background(), r.metricSampler, observability.MetricSample{
			Name: "audio.device.faults", Kind: "counter", Value: 1, Unit: "events", Fields: faultFields,
		})
		_ = observability.TryLog(context.Background(), r.logger, observability.LogRecord{
			Level: "warn", Message: "simulated audio device callback fault", Fields: faultFields,
		})
	}
}

func (r *SimulatedDuplexRegistry) observeQueueDeltas(playback, previousPlayback PlaybackQueueStats, capture, previousCapture CaptureQueueStats) {
	type deltaMetric struct {
		name  string
		unit  string
		value uint64
	}
	metrics := []deltaMetric{
		{name: "audio.playback.underflows", unit: "events", value: playback.UnderflowEvents - previousPlayback.UnderflowEvents},
		{name: "audio.playback.underflow", unit: "samples", value: playback.UnderflowSamples - previousPlayback.UnderflowSamples},
		{name: "audio.playback.overflows", unit: "events", value: playback.OverflowEvents - previousPlayback.OverflowEvents},
		{name: "audio.playback.dropped", unit: "samples", value: playback.DroppedSamples - previousPlayback.DroppedSamples},
		{name: "audio.capture.dropped", unit: "frames", value: capture.DroppedFrames - previousCapture.DroppedFrames},
		{name: "audio.capture.dropped", unit: "samples", value: capture.DroppedSamples - previousCapture.DroppedSamples},
		{name: "audio.capture.sequence_gaps", unit: "gaps", value: capture.SequenceGaps - previousCapture.SequenceGaps},
	}
	fields := observability.Fields{"backend": SimulatedDuplexBackendName}
	loss := false
	for _, metric := range metrics {
		if metric.value == 0 {
			continue
		}
		loss = true
		_ = observability.TrySample(context.Background(), r.metricSampler, observability.MetricSample{
			Name: metric.name, Kind: "counter", Value: float64(metric.value), Unit: metric.unit, Fields: fields,
		})
	}
	if loss {
		_ = observability.TryLog(context.Background(), r.logger, observability.LogRecord{
			Level: "warn", Message: "simulated audio buffer loss", Fields: fields,
		})
	}
}

func (r *SimulatedDuplexRegistry) advanceRenderLocked() error {
	callback := r.renderCallback
	r.renderCallback++
	faults := r.faults(callback, DirectionOutput)
	quantum := repeated(r.scenario.Render.Quanta, callback)
	if r.applyTimelineFaultsLocked(faults, DirectionOutput) {
		flags, faultID := faultFlags(faults)
		queued := r.playback.Snapshot().QueuedSamples
		start := r.renderPosition
		if hasFault(faults, FaultMissingCallback) {
			start -= uint64(quantum)
		}
		r.trace = append(r.trace, traceFor("render", callback, r.renderEpoch, r.format.SampleRate, start, make([]int16, quantum), queued, queued, jitter(r.scenario.Render, callback), append(flags, "gap"), faultID))
		return nil
	}
	before := r.playback.Snapshot().QueuedSamples
	block := make([]int16, quantum)
	got := r.playback.RenderInto(block)
	r.rendered = append(r.rendered, block...)
	flags, faultID := faultFlags(faults)
	if got < quantum {
		flags = append(flags, "underflow")
	}
	r.trace = append(r.trace, traceFor("render", callback, r.renderEpoch, r.format.SampleRate, r.renderPosition, block, before, r.playback.Snapshot().QueuedSamples, jitter(r.scenario.Render, callback), flags, faultID))
	r.renderPosition += uint64(quantum)
	r.appendAcousticLocked(block)
	if hasFault(faults, FaultDuplicateCallback) {
		// Duplicate delivery is observable but never forwarded twice.
		r.trace = append(r.trace, traceFor("render", callback, r.renderEpoch, r.format.SampleRate, r.renderPosition-uint64(quantum), block, before, r.playback.Snapshot().QueuedSamples, jitter(r.scenario.Render, callback), []string{"duplicate_rejected"}, faultID))
	}
	return nil
}

func (r *SimulatedDuplexRegistry) advanceCaptureLocked() error {
	callback := r.captureCallback
	r.captureCallback++
	faults := r.faults(callback, DirectionInput)
	quantum := repeated(r.scenario.Capture.Quanta, callback)
	if r.applyTimelineFaultsLocked(faults, DirectionInput) {
		flags, faultID := faultFlags(faults)
		start := r.capturePosition
		if hasFault(faults, FaultMissingCallback) {
			start -= uint64(quantum)
		}
		r.trace = append(r.trace, traceFor("capture", callback, r.captureEpoch, r.format.SampleRate, start, make([]int16, quantum), len(r.capture), len(r.capture), jitter(r.scenario.Capture, callback), append(flags, "gap"), faultID))
		return nil
	}
	block := make([]int16, quantum)
	for i := range block {
		if len(r.acoustic) > 0 {
			block[i] = r.acoustic[0]
			r.acoustic = r.acoustic[1:]
		}
		block[i] = saturatingAdd(block[i], stemSample(r.scenario.Acoustic.NearEnd, &r.nearPosition))
		block[i] = saturatingAdd(block[i], stemSample(r.scenario.Acoustic.Background, &r.noisePosition))
	}
	before := len(r.capture)
	// Retain the exact device-produced capture tap independently of the live
	// queue. Reads are destructive by design, so a replay/debug artifact built
	// from the queue would otherwise lose the very microphone samples that
	// reached (or were dropped before reaching) the provider.
	r.captured = append(r.captured, block...)
	r.enqueueCaptureLocked(block)
	flags, faultID := faultFlags(faults)
	r.trace = append(r.trace, traceFor("capture", callback, r.captureEpoch, r.format.SampleRate, r.capturePosition, block, before, len(r.capture), jitter(r.scenario.Capture, callback), flags, faultID))
	r.capturePosition += uint64(quantum)
	if hasFault(faults, FaultDuplicateCallback) {
		// Preserve one capture range and record the rejected duplicate.
		r.trace = append(r.trace, traceFor("capture", callback, r.captureEpoch, r.format.SampleRate, r.capturePosition-uint64(quantum), block, before, len(r.capture), jitter(r.scenario.Capture, callback), []string{"duplicate_rejected"}, faultID))
	}
	return nil
}

func (r *SimulatedDuplexRegistry) enqueueCaptureLocked(block []int16) {
	r.captureStats.CapturedSamples += uint64(len(block))
	assembled := r.captureRemainder + len(block)
	r.captureStats.CompletedFrames += uint64(assembled / FrameSize)
	r.captureRemainder = assembled % FrameSize
	overflow := len(r.capture) + len(block) - r.captureCapacity
	if overflow > 0 {
		r.captureStats.DroppedSamples += uint64(overflow)
		r.captureStats.DroppedFrames++
		r.captureStats.SequenceGaps++
		if r.captureStats.DropPolicy == "drop_newest" {
			block = block[:maxIntValue(0, len(block)-overflow)]
		} else if overflow >= len(r.capture) {
			fromBlock := overflow - len(r.capture)
			r.capture = r.capture[:0]
			block = block[min(fromBlock, len(block)):]
		} else {
			r.capture = r.capture[overflow:]
		}
	}
	r.capture = append(r.capture, block...)
	r.captureStats.QueuedSamples = len(r.capture)
	if len(r.capture) > r.captureStats.HighWaterSamples {
		r.captureStats.HighWaterSamples = len(r.capture)
	}
}

func (r *SimulatedDuplexRegistry) appendAcousticLocked(render []int16) {
	a := r.scenario.Acoustic
	if len(a.ImpulseResponseQ15) == 0 {
		for _, sample := range render {
			r.acoustic = append(r.acoustic, scaleQ15(sample, a.GainQ15))
		}
		return
	}
	input := append(append([]int16(nil), r.acousticHistory...), render...)
	for index := len(r.acousticHistory); index < len(input); index++ {
		var output int16
		for tap, coefficient := range a.ImpulseResponseQ15 {
			source := index - tap
			if source >= 0 {
				output = saturatingAdd(output, scaleQ15(input[source], int32(coefficient)))
			}
		}
		r.acoustic = append(r.acoustic, scaleQ15(output, a.GainQ15))
	}
	history := len(a.ImpulseResponseQ15) - 1
	if history > len(input) {
		history = len(input)
	}
	r.acousticHistory = append(r.acousticHistory[:0], input[len(input)-history:]...)
}

func (r *SimulatedDuplexRegistry) applyTimelineFaultsLocked(faults []FaultEvent, d Direction) bool {
	for _, f := range faults {
		switch f.Type {
		case FaultClockReset:
			if d == DirectionOutput {
				r.renderEpoch++
				r.renderPosition = 0
			} else {
				r.captureEpoch++
				r.capturePosition = 0
			}
		case FaultForwardJump:
			if d == DirectionOutput {
				r.renderPosition += uint64(max64(0, f.Samples))
			} else {
				r.capturePosition += uint64(max64(0, f.Samples))
			}
		case FaultBackwardJump:
			if d == DirectionOutput {
				r.renderEpoch++
				r.renderPosition = 0
			} else {
				r.captureEpoch++
				r.capturePosition = 0
			}
		case FaultDeviceLoss:
			r.lost[side(d)] = true
			r.signalLocked()
			return true
		case FaultMissingCallback:
			if d == DirectionOutput {
				r.renderPosition += uint64(repeated(r.scenario.Render.Quanta, r.renderCallback-1))
			} else {
				r.capturePosition += uint64(repeated(r.scenario.Capture.Quanta, r.captureCallback-1))
			}
			return true
		}
	}
	return false
}

func (r *SimulatedDuplexRegistry) faults(callback uint64, d Direction) []FaultEvent {
	out := []FaultEvent{}
	for _, f := range r.scenario.Faults {
		if f.Callback == callback && f.Direction == d {
			out = append(out, f)
		}
	}
	return out
}
func (r *SimulatedDuplexRegistry) signalLocked() { close(r.changed); r.changed = make(chan struct{}) }
func (r *SimulatedDuplexRegistry) Trace() []DeviceTraceEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]DeviceTraceEvent(nil), r.trace...)
}
func (r *SimulatedDuplexRegistry) RenderedSamples() []int16 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int16(nil), r.rendered...)
}

// CapturedSamples returns the immutable capture-device tap in callback order.
// Unlike the live capture queue this history is not consumed by stream reads,
// making it suitable for failure capsules and deterministic replay evidence.
func (r *SimulatedDuplexRegistry) CapturedSamples() []int16 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int16(nil), r.captured...)
}
func (r *SimulatedDuplexRegistry) CaptureStats() CaptureQueueStats {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.captureStats
}
func (r *SimulatedDuplexRegistry) PlaybackStats() PlaybackQueueStats { return r.playback.Snapshot() }

// InjectNearEnd appends microphone PCM to the unconsumed near-end timeline.
// Device-server harnesses use this to drive a live provider through the same
// callback-owned capture path as a physical microphone.
func (r *SimulatedDuplexRegistry) InjectNearEnd(samples []int16) {
	if r == nil || len(samples) == 0 {
		return
	}
	r.mu.Lock()
	r.scenario.Acoustic.NearEnd = append(r.scenario.Acoustic.NearEnd, samples...)
	r.signalLocked()
	r.mu.Unlock()
}

func repeated(values []int, index uint64) int { return values[index%uint64(len(values))] }
func jitter(c ClockSpec, index uint64) int64 {
	if len(c.JitterSamples) == 0 {
		return 0
	}
	return int64(c.JitterSamples[index%uint64(len(c.JitterSamples))])
}
func traceFor(tap string, sequence uint64, epoch uint32, rate int, start uint64, samples []int16, before, after int, jitter int64, flags []string, faultID string) DeviceTraceEvent {
	h := sha256.New()
	for _, sample := range samples {
		h.Write([]byte{byte(sample), byte(uint16(sample) >> 8)})
	}
	return DeviceTraceEvent{Tap: tap, Sequence: sequence, ClockEpoch: epoch, SampleRate: rate, StartSample: start, SampleCount: len(samples), DeviceTick: sequence, HostMonoSamples: int64(start) + jitter, QueueBefore: before, QueueAfter: after, Flags: flags, FaultID: faultID, PayloadSHA256: hex.EncodeToString(h.Sum(nil))}
}
func faultFlags(fs []FaultEvent) ([]string, string) {
	flags := []string{}
	id := ""
	for _, f := range fs {
		flags = append(flags, string(f.Type))
		if id == "" {
			id = f.ID
		}
	}
	return flags, id
}
func hasFault(fs []FaultEvent, kind FaultType) bool {
	for _, f := range fs {
		if f.Type == kind {
			return true
		}
	}
	return false
}
func stemSample(stem []int16, position *int) int16 {
	if *position >= len(stem) {
		return 0
	}
	value := stem[*position]
	*position++
	return value
}
func scaleQ15(sample int16, gain int32) int16 {
	product := int64(sample) * int64(gain)
	if product >= 0 {
		product += 16384
	} else {
		product -= 16384
	}
	return saturatePCM16Int64(product / 32768)
}
func saturatingAdd(a, b int16) int16 { return saturatePCM16Int64(int64(a) + int64(b)) }
func saturatePCM16Int64(v int64) int16 {
	if v < -32768 {
		return -32768
	}
	if v > 32767 {
		return 32767
	}
	return int16(v)
}
func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
