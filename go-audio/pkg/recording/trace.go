package recording

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

const sessionAudioTraceQueueSize = 4096
const MaxBlockSamples = 4096
const MaxRuntimePayloadBytes = 1024 * 1024
const SchemaVersion = 1

var ErrIncomplete = errors.New("audio trace is incomplete")

const (
	audioTraceMicPreGate = iota
	audioTraceMicUploaded
	audioTraceSpeakerEnqueued
	audioTraceSpeakerRendered
	audioTraceTapCount
)

var sessionAudioTraceFiles = [audioTraceTapCount]string{
	"microphone-pre-gate.wav",
	"microphone-uploaded.wav",
	"speaker-enqueued.wav",
	"speaker-rendered.wav",
}

// Trace records the four externally meaningful local audio edges
// on one monotonic timeline. Producers only copy into a bounded channel;
// filesystem work is owned by the background writer and cannot pace audio.
const MaxQueuedBytes int64 = 16 << 20

type Trace struct {
	queuedBytes atomic.Int64
	admission   sync.RWMutex
	closed      bool
	clock       clock.Source
	timeline    *os.File
	directory   string
	started     time.Time
	events      chan sessionAudioTraceBlock
	done        chan struct{}

	captureOrder   [audioTraceTapCount]sync.Mutex
	position       [audioTraceTapCount]atomic.Uint64
	droppedBlocks  [audioTraceTapCount]atomic.Uint64
	droppedSamples [audioTraceTapCount]atomic.Uint64
	droppedRuntime atomic.Uint64
	samplePool     sync.Pool

	closeOnce       sync.Once
	writtenSequence uint64
	errMu           sync.Mutex
	err             error
}

type sessionAudioTraceBlock struct {
	cost        int64
	elapsedNS   int64
	tap         int
	sampleRate  int
	startSample uint64
	samples     []int16
	runtime     *RuntimeEvent
}

type Event struct {
	PCMHash         string `json:"pcm_sha256,omitempty"`
	Version         int    `json:"version"`
	Sequence        uint64 `json:"sequence"`
	ElapsedNS       int64  `json:"elapsed_ns"`
	Timestamp       string `json:"timestamp"`
	Kind            string `json:"kind"`
	Tap             string `json:"tap,omitempty"`
	SampleRate      int    `json:"sample_rate,omitempty"`
	StartSample     uint64 `json:"start_sample,omitempty"`
	SampleCount     int    `json:"sample_count,omitempty"`
	DurationNS      int64  `json:"duration_ns,omitempty"`
	RuntimeKind     string `json:"runtime_kind,omitempty"`
	RuntimeTick     uint64 `json:"runtime_tick,omitempty"`
	InputCommit     int    `json:"input_commit,omitempty"`
	ResponseID      string `json:"response_id,omitempty"`
	ResponsePurpose string `json:"response_purpose,omitempty"`
	StreamID        string `json:"stream_id,omitempty"`
	LoopPassID      int    `json:"loop_pass_id,omitempty"`
	Epoch           uint64 `json:"epoch,omitempty"`
	TurnsCompleted  int    `json:"turns_completed,omitempty"`
	Clean           bool   `json:"clean,omitempty"`
	Error           string `json:"error,omitempty"`
	Payload         []byte `json:"payload,omitempty"`
	DroppedBlocks   uint64 `json:"dropped_blocks,omitempty"`
	DroppedSamples  uint64 `json:"dropped_samples,omitempty"`
}

// NewTrace creates directory and begins an asynchronous trace.
func NewTrace(directory string, source clock.Source) (*Trace, error) {
	if source == nil {
		return nil, errors.New("audio trace clock is required")
	}
	if directory == "" {
		return nil, errors.New("audio trace directory is empty")
	}
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return nil, fmt.Errorf("create audio trace directory %q: %w", directory, err)
	}
	// Refuse to reuse any artifact from a prior recording, including a WAV tap
	// that this trace might never receive a block for. Lazy WAV creation still
	// uses O_EXCL as the race-safe final guard once a tap's sample rate is known.
	for _, name := range sessionAudioTraceFiles {
		if _, err := os.Stat(filepath.Join(directory, name)); err == nil {
			return nil, fmt.Errorf("audio trace output %q already exists", name)
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("check audio trace output %q: %w", name, err)
		}
	}
	timeline, err := os.OpenFile(filepath.Join(directory, "timeline.jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}

	t := &Trace{
		directory: directory,
		timeline:  timeline,
		started:   source.Now(),
		clock:     source,
		events:    make(chan sessionAudioTraceBlock, sessionAudioTraceQueueSize),
		done:      make(chan struct{}),
	}
	t.samplePool.New = func() any {
		samples := make([]int16, 4096)
		return &samples
	}
	go t.writeLoop()
	return t, nil
}

func (t *Trace) capture(tap int, sampleRate int, samples []int16) {
	for len(samples) > 0 {
		n := min(len(samples), MaxBlockSamples)
		t.captureBlock(tap, sampleRate, samples[:n])
		samples = samples[n:]
	}
}

func (t *Trace) captureBlock(tap int, sampleRate int, samples []int16) {
	if t == nil || tap < 0 || tap >= audioTraceTapCount || sampleRate <= 0 || len(samples) == 0 {
		return
	}
	t.admission.RLock()
	defer t.admission.RUnlock()
	if t.closed {
		return
	}
	// A tap may have multiple producers (speech and local cues). Assign sample
	// offsets in the same order as queue admission, or valid concurrent capture
	// can otherwise appear to contain a gap during replay.
	t.captureOrder[tap].Lock()
	defer t.captureOrder[tap].Unlock()
	start := t.position[tap].Add(uint64(len(samples))) - uint64(len(samples))
	cost := int64(MaxBlockSamples*2 + 128)
	if !t.reserve(cost) {
		t.droppedBlocks[tap].Add(1)
		t.droppedSamples[tap].Add(uint64(len(samples)))
		return
	}
	pooled := t.samplePool.Get().(*[]int16)
	buffer := *pooled
	if cap(buffer) < len(samples) {
		buffer = make([]int16, len(samples))
	} else {
		buffer = buffer[:len(samples)]
	}
	copy(buffer, samples)
	block := sessionAudioTraceBlock{
		elapsedNS: t.clock.Now().Sub(t.started).Nanoseconds(),
		tap:       tap, sampleRate: sampleRate, startSample: start,
		samples: buffer, cost: cost,
	}
	select {
	case t.events <- block:
	default:
		t.droppedBlocks[tap].Add(1)
		t.droppedSamples[tap].Add(uint64(len(samples)))
		t.releaseSamples(buffer)
		t.queuedBytes.Add(-cost)
	}
}

func (t *Trace) CaptureMicrophonePreGate(sampleRate int, samples []int16) {
	t.capture(audioTraceMicPreGate, sampleRate, samples)
}

func (t *Trace) CaptureMicrophoneUploaded(sampleRate int, samples []int16) {
	t.capture(audioTraceMicUploaded, sampleRate, samples)
}

func (t *Trace) CaptureSpeakerEnqueued(_ context.Context, sampleRate int, samples []int16) error {
	t.capture(audioTraceSpeakerEnqueued, sampleRate, samples)
	return nil
}

func (t *Trace) CaptureSpeakerRendered(sampleRate int, samples []int16) {
	t.capture(audioTraceSpeakerRendered, sampleRate, samples)
}

// ObserveRuntime adds provider/session timing boundaries without
// duplicating their audio payload in the WAV files.
func (t *Trace) ObserveRuntime(observation RuntimeEvent) {
	if t == nil {
		return
	}
	t.admission.RLock()
	defer t.admission.RUnlock()
	if t.closed {
		return
	}
	cost := int64(len(observation.Payload) + len(observation.Kind) + len(observation.Error) + len(observation.ResponseID) + len(observation.ResponsePurpose) + len(observation.StreamID) + 128)
	if cost > MaxRuntimePayloadBytes || !t.reserve(cost) {
		t.droppedRuntime.Add(1)
		return
	}
	copyObservation := observation
	copyObservation.Payload = append([]byte(nil), observation.Payload...)
	block := sessionAudioTraceBlock{
		elapsedNS: t.clock.Now().Sub(t.started).Nanoseconds(),
		tap:       -1, runtime: &copyObservation, cost: cost,
	}
	select {
	case t.events <- block:
	default:
		t.queuedBytes.Add(-cost)
		// Runtime timing loss is represented by a tap-neutral summary record.
		t.droppedRuntime.Add(1)
	}
}

// Close drains queued blocks, finalizes WAV headers, and returns any artifact
// error. It is idempotent.
func (t *Trace) Close() error {
	if t == nil {
		return nil
	}
	t.closeOnce.Do(func() {
		t.admission.Lock()
		t.closed = true
		close(t.events)
		t.admission.Unlock()
		<-t.done
	})
	t.errMu.Lock()
	defer t.errMu.Unlock()
	return t.err
}

func (t *Trace) writeLoop() {
	defer close(t.done)
	timeline := t.timeline
	var err error
	wavs := [audioTraceTapCount]*sessionAudioTraceWAV{}
	encoder := json.NewEncoder(timeline)
	t.writeEvent(encoder, Event{Kind: "recording_started", Timestamp: t.timestamp(0)})
	for block := range t.events {
		t.queuedBytes.Add(-block.cost)
		if block.runtime != nil {
			t.writeRuntimeEvent(encoder, block)
			continue
		}
		if wavs[block.tap] == nil {
			wavs[block.tap], err = newSessionAudioTraceWAV(filepath.Join(t.directory, sessionAudioTraceFiles[block.tap]), block.sampleRate)
			if err != nil {
				t.recordError(err)
				t.releaseSamples(block.samples)
				continue
			}
		}
		if wavs[block.tap].sampleRate != block.sampleRate {
			t.recordError(fmt.Errorf("audio trace %s changed sample rate from %d to %d", sessionAudioTraceFiles[block.tap], wavs[block.tap].sampleRate, block.sampleRate))
			t.releaseSamples(block.samples)
			continue
		}
		if err := wavs[block.tap].Write(block.samples); err != nil {
			t.recordError(err)
		}
		t.writeEvent(encoder, Event{
			ElapsedNS: block.elapsedNS, Timestamp: t.timestamp(block.elapsedNS),
			PCMHash: hashPCM(block.samples),
			Kind:    "audio", Tap: traceTapName(block.tap), SampleRate: block.sampleRate,
			StartSample: block.startSample, SampleCount: len(block.samples),
			DurationNS: int64(len(block.samples)) * int64(time.Second) / int64(block.sampleRate),
		})
		t.releaseSamples(block.samples)
	}
	for tap := range wavs {
		if wavs[tap] != nil {
			t.recordError(wavs[tap].Close())
		}
		if dropped := t.droppedBlocks[tap].Load(); dropped > 0 {
			t.recordError(ErrIncomplete)
			t.writeEvent(encoder, Event{
				ElapsedNS: t.clock.Now().Sub(t.started).Nanoseconds(),
				Timestamp: t.timestamp(t.clock.Now().Sub(t.started).Nanoseconds()), Kind: "trace_overflow",
				Tap: traceTapName(tap), DroppedBlocks: dropped, DroppedSamples: t.droppedSamples[tap].Load(),
			})
		}
	}
	if dropped := t.droppedRuntime.Load(); dropped > 0 {
		t.recordError(ErrIncomplete)
		now := t.clock.Now().Sub(t.started).Nanoseconds()
		t.writeEvent(encoder, Event{
			ElapsedNS: now, Timestamp: t.timestamp(now),
			Kind: "trace_overflow", Tap: "runtime", DroppedBlocks: dropped,
		})
	}
	t.errMu.Lock()
	clean := t.err == nil
	t.errMu.Unlock()
	now := t.clock.Now().Sub(t.started).Nanoseconds()
	t.writeEvent(encoder, Event{Kind: "recording_closed", ElapsedNS: now, Timestamp: t.timestamp(now), Clean: clean})
	t.recordError(timeline.Close())
}

func (t *Trace) releaseSamples(samples []int16) {
	if cap(samples) == 4096 {
		samples = samples[:4096]
		t.samplePool.Put(&samples)
	}
}

func (t *Trace) writeRuntimeEvent(encoder *json.Encoder, block sessionAudioTraceBlock) {
	o := block.runtime
	t.writeEvent(encoder, Event{
		ElapsedNS: block.elapsedNS, Timestamp: t.timestamp(block.elapsedNS), Kind: "runtime",
		RuntimeKind: string(o.Kind), RuntimeTick: o.Tick, InputCommit: o.InputCommit,
		ResponseID: o.ResponseID, ResponsePurpose: string(o.ResponsePurpose), StreamID: o.StreamID, LoopPassID: o.LoopPassID, Epoch: o.Epoch, TurnsCompleted: o.TurnsCompleted,
		Clean: o.Clean, Error: o.Error, Payload: o.Payload,
	})
}

func (t *Trace) writeEvent(encoder *json.Encoder, event Event) {
	t.writtenSequence++
	event.Sequence = t.writtenSequence
	event.Version = SchemaVersion
	if err := encoder.Encode(event); err != nil {
		t.recordError(err)
	}
}

func (t *Trace) timestamp(elapsedNS int64) string {
	return t.started.Add(time.Duration(elapsedNS)).UTC().Format(time.RFC3339Nano)
}

func (t *Trace) recordError(err error) {
	if err == nil {
		return
	}
	t.errMu.Lock()
	if t.err == nil {
		t.err = err
	}
	t.errMu.Unlock()
}

func traceTapName(tap int) string {
	switch tap {
	case audioTraceMicPreGate:
		return "microphone_pre_gate"
	case audioTraceMicUploaded:
		return "microphone_uploaded"
	case audioTraceSpeakerEnqueued:
		return "speaker_enqueued"
	case audioTraceSpeakerRendered:
		return "speaker_rendered"
	default:
		return "unknown"
	}
}

type sessionAudioTraceWAV struct {
	file       *os.File
	sampleRate int
	writer     *wavio.StreamWriter
}

func newSessionAudioTraceWAV(path string, sampleRate int) (*sessionAudioTraceWAV, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	writer, err := wavio.NewStreamWriter(file, sampleRate)
	if err != nil {
		return nil, errors.Join(err, file.Close())
	}
	return &sessionAudioTraceWAV{file: file, sampleRate: sampleRate, writer: writer}, nil
}
func (w *sessionAudioTraceWAV) Write(samples []int16) error {
	if err := w.writer.WriteSamples(samples); err != nil {
		return err
	}
	return w.writer.Checkpoint()
}
func (w *sessionAudioTraceWAV) Close() error { return errors.Join(w.writer.Close(), w.file.Close()) }

// RuntimeEvent is a provider-neutral observation on the same recording clock.
type RuntimeEvent struct {
	Kind                        string
	Tick                        uint64
	InputCommit                 int
	ResponseID, ResponsePurpose string
	StreamID                    string
	LoopPassID                  int
	Epoch                       uint64
	TurnsCompleted              int
	Clean                       bool
	Error                       string
	Payload                     []byte
}

func hashPCM(samples []int16) string {
	sum := sha256.Sum256(codec.EncodePCM16(samples))
	return hex.EncodeToString(sum[:])
}

// reserve bounds retained media and event bytes independently of packet count.
// Failure is reported in the same explicit incomplete-recording accounting as
// channel saturation; diagnostic pressure never blocks the audio workers.
func (t *Trace) reserve(cost int64) bool {
	for {
		current := t.queuedBytes.Load()
		if cost < 0 || cost > MaxQueuedBytes-current {
			return false
		}
		if t.queuedBytes.CompareAndSwap(current, current+cost) {
			return true
		}
	}
}
