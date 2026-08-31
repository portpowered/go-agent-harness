package audio

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"
)

const (
	// PCM16NormalizerFrameDuration is the default analysis/output window. It
	// bounds the amount of input retained while a response is streaming.
	PCM16NormalizerFrameDuration = 20 * time.Millisecond
	// PCM16NormalizerTargetRMSDBFS is the active-speech RMS target. -20 dBFS
	// leaves useful crest-factor headroom while matching the session baseline.
	PCM16NormalizerTargetRMSDBFS = -20.0
	// PCM16NormalizerSilenceFloorDBFS excludes quiet background material from
	// gain acquisition and recovery decisions.
	PCM16NormalizerSilenceFloorDBFS = -50.0
	// PCM16NormalizerPeakCeilingDBFS is an immediate safety ceiling. Gain
	// recovery remains rate limited after this ceiling attenuates a transient.
	PCM16NormalizerPeakCeilingDBFS = -1.0
	// PCM16NormalizerClipSampleThreshold is the repository's near-full-scale
	// guard. The normalizer's symmetric peak cap is kept below this value.
	PCM16NormalizerClipSampleThreshold = 32700
	// PCM16NormalizerMaxGainChangeDBPer100MS limits ordinary gain movement after
	// the first active frame has acquired a level.
	PCM16NormalizerMaxGainChangeDBPer100MS = 1.0
)

var (
	// ErrPCM16NormalizerInvalidPCM identifies a malformed PCM16 chunk. The
	// normalizer rejects the complete chunk before emitting any of its bytes.
	ErrPCM16NormalizerInvalidPCM = errors.New("invalid PCM16 normalizer input")
	// ErrPCM16NormalizerLifecycle identifies an operation made after a terminal
	// response state. Reset starts a fresh response-local state.
	ErrPCM16NormalizerLifecycle = errors.New("invalid PCM16 normalizer lifecycle")
	// ErrPCM16NormalizerConfig identifies a configuration that cannot preserve
	// the normalizer's bounded-delay and signal-safety contract.
	ErrPCM16NormalizerConfig = errors.New("invalid PCM16 normalizer configuration")
)

// PCM16NormalizerConfig controls one streaming PCM16Normalizer. Zero-valued
// fields select the corresponding default. Input is always mono interleaved
// PCM16; SampleRate exists to make the frame-duration and gain-rate contract
// explicit at the boundary.
type PCM16NormalizerConfig struct {
	SampleRate              int
	FrameDuration           time.Duration
	TargetRMSDBFS           float64
	SilenceFloorDBFS        float64
	PeakCeilingDBFS         float64
	ClipSampleThreshold     int
	MaxGainChangeDBPer100MS float64
}

// DefaultPCM16NormalizerConfig is the production profile used by
// NewPCM16Normalizer.
var DefaultPCM16NormalizerConfig = PCM16NormalizerConfig{
	SampleRate:              SampleRate,
	FrameDuration:           PCM16NormalizerFrameDuration,
	TargetRMSDBFS:           PCM16NormalizerTargetRMSDBFS,
	SilenceFloorDBFS:        PCM16NormalizerSilenceFloorDBFS,
	PeakCeilingDBFS:         PCM16NormalizerPeakCeilingDBFS,
	ClipSampleThreshold:     PCM16NormalizerClipSampleThreshold,
	MaxGainChangeDBPer100MS: PCM16NormalizerMaxGainChangeDBPer100MS,
}

// PCM16Normalizer is a response-local, stateful active-speech leveler. It
// keeps at most one configured analysis frame, emits complete frames as soon
// as they are available, and flushes the final partial frame on Finish.
//
// A normalizer starts in the active state. Finish and Cancel make its terminal
// state explicit; call Reset before feeding the next response. Keeping this
// lifecycle local to one instance prevents gain learned for one voice or
// response from leaking into another.
type PCM16Normalizer struct {
	mu sync.Mutex

	cfg          normalizedPCM16NormalizerConfig
	frameSamples int
	state        pcm16NormalizerState
	pending      []int16

	gain     float64
	acquired bool
}

type normalizedPCM16NormalizerConfig struct {
	PCM16NormalizerConfig
	frameSamples int
	maxOutputAbs int
	targetRMS    float64
	silenceFloor float64
	peakCeiling  float64
}

type pcm16NormalizerState uint8

const (
	pcm16NormalizerActive pcm16NormalizerState = iota + 1
	pcm16NormalizerFinished
	pcm16NormalizerCanceled
	pcm16NormalizerFailed
)

// NewPCM16Normalizer constructs a normalizer with the production PCM16
// profile. It cannot fail because the package-owned default is validated by
// NewPCM16NormalizerWithConfig during construction.
func NewPCM16Normalizer() *PCM16Normalizer {
	normalizer, err := NewPCM16NormalizerWithConfig(DefaultPCM16NormalizerConfig)
	if err != nil {
		// The default is a package invariant. Keep the constructor convenient for
		// the runtime boundary while making an accidental source edit visible.
		panic(err)
	}
	return normalizer
}

// NewPCM16NormalizerWithConfig constructs a normalizer with an explicit
// profile. A frame duration above 20 ms is rejected because it would violate
// the streaming delay contract.
func NewPCM16NormalizerWithConfig(config PCM16NormalizerConfig) (*PCM16Normalizer, error) {
	normalized, err := normalizePCM16NormalizerConfig(config)
	if err != nil {
		return nil, err
	}
	return &PCM16Normalizer{
		cfg:          normalized,
		frameSamples: normalized.frameSamples,
		state:        pcm16NormalizerActive,
		gain:         1,
	}, nil
}

// Process accepts PCM16 samples for the current response and returns every
// complete normalized frame made available by this call. It is equivalent to
// ProcessPCM16 after decoding the little-endian bytes.
func (n *PCM16Normalizer) Process(ctx context.Context, samples []int16) ([]int16, error) {
	if n == nil {
		return nil, fmt.Errorf("%w: process on nil normalizer", ErrPCM16NormalizerLifecycle)
	}
	if err := normalizerContextError(ctx); err != nil {
		return nil, err
	}

	n.mu.Lock()
	defer n.mu.Unlock()
	if err := n.activeErrorLocked("process"); err != nil {
		return nil, err
	}
	if len(samples) == 0 {
		return nil, nil
	}
	return n.processSamplesLocked(samples), nil
}

// ProcessPCM16 accepts one complete little-endian PCM16 chunk. An odd-length
// chunk is rejected before it can be appended or partially emitted.
func (n *PCM16Normalizer) ProcessPCM16(ctx context.Context, pcm []byte) ([]byte, error) {
	if n == nil {
		return nil, fmt.Errorf("%w: process PCM16 on nil normalizer", ErrPCM16NormalizerLifecycle)
	}
	if err := normalizerContextError(ctx); err != nil {
		return nil, err
	}

	n.mu.Lock()
	defer n.mu.Unlock()
	if err := n.activeErrorLocked("process PCM16"); err != nil {
		return nil, err
	}
	if len(pcm) == 0 {
		return nil, nil
	}
	if len(pcm)%2 != 0 {
		n.state = pcm16NormalizerFailed
		n.pending = nil
		return nil, fmt.Errorf("%w: chunk has odd byte length %d", ErrPCM16NormalizerInvalidPCM, len(pcm))
	}

	samples := make([]int16, len(pcm)/2)
	for index := range samples {
		samples[index] = int16(binary.LittleEndian.Uint16(pcm[index*2:]))
	}
	normalized := n.processSamplesLocked(samples)
	return encodePCM16Samples(normalized), nil
}

// Write is a concise alias for Process for callers that model the normalizer
// as a streaming writer.
func (n *PCM16Normalizer) Write(ctx context.Context, samples []int16) ([]int16, error) {
	return n.Process(ctx, samples)
}

// WritePCM16 is a concise alias for ProcessPCM16 for byte-oriented sinks.
func (n *PCM16Normalizer) WritePCM16(ctx context.Context, pcm []byte) ([]byte, error) {
	return n.ProcessPCM16(ctx, pcm)
}

// Finish flushes the bounded tail, marks the response finished, and returns
// the final normalized samples. A second Finish is an explicit lifecycle
// error; Reset starts the next response.
func (n *PCM16Normalizer) Finish(ctx context.Context) ([]int16, error) {
	if n == nil {
		return nil, fmt.Errorf("%w: finish on nil normalizer", ErrPCM16NormalizerLifecycle)
	}
	if err := normalizerContextError(ctx); err != nil {
		return nil, err
	}

	n.mu.Lock()
	defer n.mu.Unlock()
	if err := n.activeErrorLocked("finish"); err != nil {
		return nil, err
	}

	var normalized []int16
	if len(n.pending) > 0 {
		tail := n.pending
		n.pending = nil
		normalized = n.processFrameLocked(tail, len(tail))
	}
	// The returned tail is the last output owned by this response. Clear the
	// learned level before exposing the terminal state so even diagnostic reads
	// cannot observe gain that might be reused by the next response.
	n.gain = 1
	n.acquired = false
	n.state = pcm16NormalizerFinished
	return normalized, nil
}

// FinishPCM16 flushes the bounded tail and encodes it as little-endian
// PCM16. It is the byte-oriented counterpart to Finish.
func (n *PCM16Normalizer) FinishPCM16(ctx context.Context) ([]byte, error) {
	normalized, err := n.Finish(ctx)
	if err != nil {
		return nil, err
	}
	return encodePCM16Samples(normalized), nil
}

// Cancel discards the bounded tail and marks the response canceled. It emits
// no samples, so cancellation cannot leak a partial response into a sink.
func (n *PCM16Normalizer) Cancel() error {
	if n == nil {
		return fmt.Errorf("%w: cancel on nil normalizer", ErrPCM16NormalizerLifecycle)
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if err := n.activeErrorLocked("cancel"); err != nil {
		return err
	}
	n.resetLocked(pcm16NormalizerCanceled)
	return nil
}

// Reset starts a fresh response-local normalizer state. It is safe to call at
// an explicit response boundary, including while the prior state is active;
// any bounded tail in that state is discarded rather than reattributed.
func (n *PCM16Normalizer) Reset() error {
	if n == nil {
		return fmt.Errorf("%w: reset on nil normalizer", ErrPCM16NormalizerLifecycle)
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.resetLocked(pcm16NormalizerActive)
	return nil
}

// GainDB reports the current ordinary gain in dB. It is intended for
// diagnostics and deterministic envelope tests; it does not mutate state.
func (n *PCM16Normalizer) GainDB() float64 {
	if n == nil {
		return 0
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.gain <= 0 {
		return math.Inf(-1)
	}
	return linearToDBFS(n.gain)
}

// FrameSamples reports the number of samples retained at most before a frame
// is normalized and emitted.
func (n *PCM16Normalizer) FrameSamples() int {
	if n == nil {
		return 0
	}
	return n.frameSamples
}

func (n *PCM16Normalizer) activeErrorLocked(operation string) error {
	if n == nil {
		return fmt.Errorf("%w: %s on nil normalizer", ErrPCM16NormalizerLifecycle, operation)
	}
	switch n.state {
	case pcm16NormalizerActive:
		return nil
	case pcm16NormalizerFinished:
		return fmt.Errorf("%w: cannot %s after finish; call Reset", ErrPCM16NormalizerLifecycle, operation)
	case pcm16NormalizerCanceled:
		return fmt.Errorf("%w: cannot %s after cancel; call Reset", ErrPCM16NormalizerLifecycle, operation)
	case pcm16NormalizerFailed:
		return fmt.Errorf("%w: cannot %s after malformed PCM; call Reset", ErrPCM16NormalizerLifecycle, operation)
	default:
		return fmt.Errorf("%w: cannot %s in unknown state %d", ErrPCM16NormalizerLifecycle, operation, n.state)
	}
}

func (n *PCM16Normalizer) resetLocked(state pcm16NormalizerState) {
	n.pending = nil
	n.gain = 1
	n.acquired = false
	n.state = state
}

func (n *PCM16Normalizer) processSamplesLocked(samples []int16) []int16 {
	output := make([]int16, 0, len(samples))
	for len(samples) > 0 {
		if len(n.pending) > 0 {
			needed := n.frameSamples - len(n.pending)
			if len(samples) < needed {
				n.pending = append(n.pending, samples...)
				break
			}
			frame := make([]int16, n.frameSamples)
			copy(frame, n.pending)
			copy(frame[len(n.pending):], samples[:needed])
			n.pending = nil
			output = append(output, n.processFrameLocked(frame, len(frame))...)
			samples = samples[needed:]
			continue
		}

		if len(samples) >= n.frameSamples {
			output = append(output, n.processFrameLocked(samples[:n.frameSamples], n.frameSamples)...)
			samples = samples[n.frameSamples:]
			continue
		}

		n.pending = append(n.pending[:0], samples...)
		break
	}
	return output
}

func (n *PCM16Normalizer) processFrameLocked(frame []int16, sampleCount int) []int16 {
	if len(frame) == 0 {
		return nil
	}
	if sampleCount <= 0 || sampleCount > len(frame) {
		sampleCount = len(frame)
	}

	var sum int64
	for _, sample := range frame[:sampleCount] {
		sum += int64(sample)
	}
	mean := float64(sum) / float64(sampleCount)

	centered := make([]float64, sampleCount)
	var energy float64
	for index, sample := range frame[:sampleCount] {
		value := float64(sample) - mean
		centered[index] = value
		energy += value * value
	}
	rms := math.Sqrt(energy / float64(sampleCount))

	active := rms > n.cfg.silenceFloor
	frameGain := 1.0
	if active {
		targetGain := n.cfg.targetRMS / rms
		if !n.acquired {
			n.gain = targetGain
			n.acquired = true
		} else {
			n.gain = moveGain(n.gain, targetGain, n.maxGainRatio(sampleCount))
		}
		frameGain = n.gain
	}

	scaled := make([]float64, sampleCount)
	peak := 0.0
	for index, value := range centered {
		scaled[index] = value * frameGain
		if magnitude := math.Abs(scaled[index]); magnitude > peak {
			peak = magnitude
		}
	}

	// A safety reduction is allowed to act immediately, including on a late
	// transient. Persisting that reduced gain prevents the next frame from
	// immediately re-amplifying the same response; ordinary recovery below is
	// still limited by maxGainRatio.
	if peak > n.cfg.peakCeiling {
		safetyRatio := n.cfg.peakCeiling / peak
		for index := range scaled {
			scaled[index] *= safetyRatio
		}
		if active {
			safeGain := frameGain * safetyRatio
			if safeGain < n.gain {
				n.gain = safeGain
			}
		}
	}

	output := make([]int16, sampleCount)
	for index, value := range scaled {
		rounded := int(math.Round(value))
		if rounded > n.cfg.maxOutputAbs {
			rounded = n.cfg.maxOutputAbs
		} else if rounded < -n.cfg.maxOutputAbs {
			rounded = -n.cfg.maxOutputAbs
		}
		output[index] = int16(rounded)
	}
	// Centering before gain removes source DC. Rebalancing the rounded frame
	// also prevents an asymmetric transient cap or half-sample rounding from
	// accumulating a stream-level bias.
	rebalancePCM16Frame(output, n.cfg.maxOutputAbs)
	return output
}

func (n *PCM16Normalizer) maxGainRatio(sampleCount int) float64 {
	duration := time.Duration(float64(sampleCount) / float64(n.cfg.SampleRate) * float64(time.Second))
	maxDB := n.cfg.MaxGainChangeDBPer100MS * float64(duration) / float64(100*time.Millisecond)
	if maxDB <= 0 {
		return 1
	}
	return dbToLinear(maxDB)
}

func moveGain(current, target, maxRatio float64) float64 {
	if current <= 0 || math.IsNaN(current) || math.IsInf(current, 0) {
		return target
	}
	if target > current*maxRatio {
		return current * maxRatio
	}
	if target < current/maxRatio {
		return current / maxRatio
	}
	return target
}

func rebalancePCM16Frame(samples []int16, maxAbs int) {
	if len(samples) == 0 || maxAbs <= 0 {
		return
	}
	var sum int64
	for _, sample := range samples {
		sum += int64(sample)
	}
	if sum == 0 {
		return
	}

	if sum > 0 {
		remaining := sum
		for index, sample := range samples {
			room := int64(sample) + int64(maxAbs)
			if room <= 0 {
				continue
			}
			adjustment := room
			if adjustment > remaining {
				adjustment = remaining
			}
			samples[index] = int16(int64(sample) - adjustment)
			remaining -= adjustment
			if remaining == 0 {
				return
			}
		}
		return
	}

	remaining := -sum
	for index, sample := range samples {
		room := int64(maxAbs) - int64(sample)
		if room <= 0 {
			continue
		}
		adjustment := room
		if adjustment > remaining {
			adjustment = remaining
		}
		samples[index] = int16(int64(sample) + adjustment)
		remaining -= adjustment
		if remaining == 0 {
			return
		}
	}
}

func normalizePCM16NormalizerConfig(config PCM16NormalizerConfig) (normalizedPCM16NormalizerConfig, error) {
	if config.SampleRate == 0 {
		config.SampleRate = SampleRate
	}
	if config.FrameDuration == 0 {
		config.FrameDuration = PCM16NormalizerFrameDuration
	}
	if config.TargetRMSDBFS == 0 {
		config.TargetRMSDBFS = PCM16NormalizerTargetRMSDBFS
	}
	if config.SilenceFloorDBFS == 0 {
		config.SilenceFloorDBFS = PCM16NormalizerSilenceFloorDBFS
	}
	if config.PeakCeilingDBFS == 0 {
		config.PeakCeilingDBFS = PCM16NormalizerPeakCeilingDBFS
	}
	if config.ClipSampleThreshold == 0 {
		config.ClipSampleThreshold = PCM16NormalizerClipSampleThreshold
	}
	if config.MaxGainChangeDBPer100MS == 0 {
		config.MaxGainChangeDBPer100MS = PCM16NormalizerMaxGainChangeDBPer100MS
	}

	if config.SampleRate <= 0 {
		return normalizedPCM16NormalizerConfig{}, fmt.Errorf("%w: sample rate must be positive", ErrPCM16NormalizerConfig)
	}
	if config.FrameDuration <= 0 || config.FrameDuration > PCM16NormalizerFrameDuration {
		return normalizedPCM16NormalizerConfig{}, fmt.Errorf("%w: frame duration %s must be positive and at most %s", ErrPCM16NormalizerConfig, config.FrameDuration, PCM16NormalizerFrameDuration)
	}
	for field, value := range map[string]float64{
		"target RMS dBFS":    config.TargetRMSDBFS,
		"silence floor dBFS": config.SilenceFloorDBFS,
		"peak ceiling dBFS":  config.PeakCeilingDBFS,
		"gain rate dB":       config.MaxGainChangeDBPer100MS,
	} {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return normalizedPCM16NormalizerConfig{}, fmt.Errorf("%w: %s must be finite", ErrPCM16NormalizerConfig, field)
		}
	}
	if config.TargetRMSDBFS >= 0 {
		return normalizedPCM16NormalizerConfig{}, fmt.Errorf("%w: target RMS dBFS must be below zero", ErrPCM16NormalizerConfig)
	}
	if config.SilenceFloorDBFS >= config.TargetRMSDBFS {
		return normalizedPCM16NormalizerConfig{}, fmt.Errorf("%w: silence floor dBFS must be below target RMS", ErrPCM16NormalizerConfig)
	}
	if config.PeakCeilingDBFS >= 0 || config.PeakCeilingDBFS <= config.TargetRMSDBFS {
		return normalizedPCM16NormalizerConfig{}, fmt.Errorf("%w: peak ceiling dBFS must be between target RMS and zero", ErrPCM16NormalizerConfig)
	}
	if config.ClipSampleThreshold <= 0 || config.ClipSampleThreshold > PCM16NormalizerClipSampleThreshold {
		return normalizedPCM16NormalizerConfig{}, fmt.Errorf("%w: clip threshold %d must be between 1 and %d", ErrPCM16NormalizerConfig, config.ClipSampleThreshold, PCM16NormalizerClipSampleThreshold)
	}
	if config.MaxGainChangeDBPer100MS < 0 {
		return normalizedPCM16NormalizerConfig{}, fmt.Errorf("%w: gain rate cannot be negative", ErrPCM16NormalizerConfig)
	}

	frameSamples := int(math.Round(float64(config.SampleRate) * config.FrameDuration.Seconds()))
	if frameSamples <= 0 {
		return normalizedPCM16NormalizerConfig{}, fmt.Errorf("%w: frame duration %s is less than one sample at %d Hz", ErrPCM16NormalizerConfig, config.FrameDuration, config.SampleRate)
	}
	fullScale := float64(1 << 15)
	targetRMS := dbfsToLinear(config.TargetRMSDBFS, fullScale)
	silenceFloor := dbfsToLinear(config.SilenceFloorDBFS, fullScale)
	peakCeiling := dbfsToLinear(config.PeakCeilingDBFS, fullScale)
	maxOutputAbs := int(math.Floor(math.Min(peakCeiling, float64(config.ClipSampleThreshold-1))))
	if targetRMS <= 0 || silenceFloor <= 0 || peakCeiling <= 0 || maxOutputAbs <= 0 {
		return normalizedPCM16NormalizerConfig{}, fmt.Errorf("%w: profile produces a non-positive signal bound", ErrPCM16NormalizerConfig)
	}

	return normalizedPCM16NormalizerConfig{
		PCM16NormalizerConfig: config,
		frameSamples:          frameSamples,
		maxOutputAbs:          maxOutputAbs,
		targetRMS:             targetRMS,
		silenceFloor:          silenceFloor,
		peakCeiling:           peakCeiling,
	}, nil
}

func dbfsToLinear(dbfs, fullScale float64) float64 {
	return fullScale * math.Pow(10, dbfs/20)
}

func linearToDBFS(linear float64) float64 {
	if linear <= 0 {
		return math.Inf(-1)
	}
	return 20 * math.Log10(linear)
}

func dbToLinear(db float64) float64 {
	return math.Pow(10, db/20)
}

func encodePCM16Samples(samples []int16) []byte {
	if len(samples) == 0 {
		return nil
	}
	encoded := make([]byte, len(samples)*2)
	for index, sample := range samples {
		binary.LittleEndian.PutUint16(encoded[index*2:], uint16(sample))
	}
	return encoded
}

func normalizerContextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
