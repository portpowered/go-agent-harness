package services

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

const sessionAudioTraceQueueSize = 4096

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

// SessionAudioTrace records the four externally meaningful local audio edges
// on one monotonic timeline. Producers only copy into a bounded channel;
// filesystem work is owned by the background writer and cannot pace audio.
type SessionAudioTrace struct {
	directory string
	started   time.Time
	events    chan sessionAudioTraceBlock
	done      chan struct{}

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
	elapsedNS   int64
	tap         int
	sampleRate  int
	startSample uint64
	samples     []int16
	runtime     *SessionRuntimeObservation
}

type sessionAudioTraceEvent struct {
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
	TurnsCompleted  int    `json:"turns_completed,omitempty"`
	Clean           bool   `json:"clean,omitempty"`
	Error           string `json:"error,omitempty"`
	DroppedBlocks   uint64 `json:"dropped_blocks,omitempty"`
	DroppedSamples  uint64 `json:"dropped_samples,omitempty"`
}

// NewSessionAudioTrace creates directory and begins an asynchronous trace.
func NewSessionAudioTrace(directory string) (*SessionAudioTrace, error) {
	if directory == "" {
		return nil, errors.New("audio trace directory is empty")
	}
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return nil, fmt.Errorf("create audio trace directory %q: %w", directory, err)
	}
	t := &SessionAudioTrace{
		directory: directory,
		started:   time.Now(),
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

func (t *SessionAudioTrace) capture(tap int, sampleRate int, samples []int16) {
	if t == nil || tap < 0 || tap >= audioTraceTapCount || sampleRate <= 0 || len(samples) == 0 {
		return
	}
	start := t.position[tap].Add(uint64(len(samples))) - uint64(len(samples))
	pooled := t.samplePool.Get().(*[]int16)
	buffer := *pooled
	if cap(buffer) < len(samples) {
		buffer = make([]int16, len(samples))
	} else {
		buffer = buffer[:len(samples)]
	}
	copy(buffer, samples)
	block := sessionAudioTraceBlock{
		elapsedNS: time.Since(t.started).Nanoseconds(),
		tap:       tap, sampleRate: sampleRate, startSample: start,
		samples: buffer,
	}
	select {
	case t.events <- block:
	default:
		t.droppedBlocks[tap].Add(1)
		t.droppedSamples[tap].Add(uint64(len(samples)))
		t.releaseSamples(buffer)
	}
}

func (t *SessionAudioTrace) CaptureMicrophonePreGate(sampleRate int, samples []int16) {
	t.capture(audioTraceMicPreGate, sampleRate, samples)
}

func (t *SessionAudioTrace) CaptureMicrophoneUploaded(sampleRate int, samples []int16) {
	t.capture(audioTraceMicUploaded, sampleRate, samples)
}

func (t *SessionAudioTrace) CaptureSpeakerEnqueued(_ context.Context, sampleRate int, samples []int16) error {
	t.capture(audioTraceSpeakerEnqueued, sampleRate, samples)
	return nil
}

func (t *SessionAudioTrace) CaptureSpeakerRendered(sampleRate int, samples []int16) {
	t.capture(audioTraceSpeakerRendered, sampleRate, samples)
}

// ObserveSessionRuntime adds provider/session timing boundaries without
// duplicating their audio payload in the WAV files.
func (t *SessionAudioTrace) ObserveSessionRuntime(observation SessionRuntimeObservation) {
	if t == nil {
		return
	}
	copyObservation := observation
	copyObservation.Payload = nil
	block := sessionAudioTraceBlock{
		elapsedNS: time.Since(t.started).Nanoseconds(),
		tap:       -1, runtime: &copyObservation,
	}
	select {
	case t.events <- block:
	default:
		// Runtime timing loss is represented by a tap-neutral summary record.
		t.droppedRuntime.Add(1)
	}
}

// Close drains queued blocks, finalizes WAV headers, and returns any artifact
// error. It is idempotent.
func (t *SessionAudioTrace) Close() error {
	if t == nil {
		return nil
	}
	t.closeOnce.Do(func() {
		close(t.events)
		<-t.done
	})
	t.errMu.Lock()
	defer t.errMu.Unlock()
	return t.err
}

func (t *SessionAudioTrace) writeLoop() {
	defer close(t.done)
	timeline, err := os.OpenFile(filepath.Join(t.directory, "timeline.jsonl"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		t.recordError(err)
		for block := range t.events {
			t.releaseSamples(block.samples)
		}
		return
	}
	wavs := [audioTraceTapCount]*sessionAudioTraceWAV{}
	encoder := json.NewEncoder(timeline)
	for block := range t.events {
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
		t.writeEvent(encoder, sessionAudioTraceEvent{
			ElapsedNS: block.elapsedNS, Timestamp: t.timestamp(block.elapsedNS),
			Kind: "audio", Tap: traceTapName(block.tap), SampleRate: block.sampleRate,
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
			t.writeEvent(encoder, sessionAudioTraceEvent{
				ElapsedNS: time.Since(t.started).Nanoseconds(),
				Timestamp: t.timestamp(time.Since(t.started).Nanoseconds()), Kind: "trace_overflow",
				Tap: traceTapName(tap), DroppedBlocks: dropped, DroppedSamples: t.droppedSamples[tap].Load(),
			})
		}
	}
	if dropped := t.droppedRuntime.Load(); dropped > 0 {
		now := time.Since(t.started).Nanoseconds()
		t.writeEvent(encoder, sessionAudioTraceEvent{
			ElapsedNS: now, Timestamp: t.timestamp(now),
			Kind: "trace_overflow", Tap: "runtime", DroppedBlocks: dropped,
		})
	}
	t.recordError(timeline.Close())
}

func (t *SessionAudioTrace) releaseSamples(samples []int16) {
	if cap(samples) == 4096 {
		samples = samples[:4096]
		t.samplePool.Put(&samples)
	}
}

func (t *SessionAudioTrace) writeRuntimeEvent(encoder *json.Encoder, block sessionAudioTraceBlock) {
	o := block.runtime
	t.writeEvent(encoder, sessionAudioTraceEvent{
		ElapsedNS: block.elapsedNS, Timestamp: t.timestamp(block.elapsedNS), Kind: "runtime",
		RuntimeKind: string(o.Kind), RuntimeTick: o.Tick, InputCommit: o.InputCommit,
		ResponseID: o.ResponseID, ResponsePurpose: string(o.ResponsePurpose), TurnsCompleted: o.TurnsCompleted,
		Clean: o.Clean, Error: o.Error,
	})
}

func (t *SessionAudioTrace) writeEvent(encoder *json.Encoder, event sessionAudioTraceEvent) {
	t.writtenSequence++
	event.Sequence = t.writtenSequence
	if err := encoder.Encode(event); err != nil {
		t.recordError(err)
	}
}

func (t *SessionAudioTrace) timestamp(elapsedNS int64) string {
	return t.started.Add(time.Duration(elapsedNS)).UTC().Format(time.RFC3339Nano)
}

func (t *SessionAudioTrace) recordError(err error) {
	if err == nil {
		return
	}
	t.errMu.Lock()
	t.err = errors.Join(t.err, err)
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
	samples    uint64
	closed     bool
}

func newSessionAudioTraceWAV(path string, sampleRate int) (*sessionAudioTraceWAV, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	w := &sessionAudioTraceWAV{file: file, sampleRate: sampleRate}
	if _, err := file.Write(make([]byte, 44)); err != nil {
		_ = file.Close()
		return nil, err
	}
	return w, nil
}

func (w *sessionAudioTraceWAV) Write(samples []int16) error {
	if w == nil || len(samples) == 0 {
		return nil
	}
	if (w.samples+uint64(len(samples)))*2 > uint64(^uint32(0))-36 {
		return fmt.Errorf("audio trace WAV exceeds RIFF size limit")
	}
	data := make([]byte, len(samples)*2)
	for index, sample := range samples {
		binary.LittleEndian.PutUint16(data[index*2:], uint16(sample))
	}
	if _, err := w.file.Write(data); err != nil {
		return err
	}
	w.samples += uint64(len(samples))
	return nil
}

func (w *sessionAudioTraceWAV) Close() error {
	if w == nil || w.closed {
		return nil
	}
	w.closed = true
	dataSize := uint32(w.samples * 2)
	header := make([]byte, 44)
	copy(header[0:4], "RIFF")
	binary.LittleEndian.PutUint32(header[4:8], 36+dataSize)
	copy(header[8:12], "WAVE")
	copy(header[12:16], "fmt ")
	binary.LittleEndian.PutUint32(header[16:20], 16)
	binary.LittleEndian.PutUint16(header[20:22], 1)
	binary.LittleEndian.PutUint16(header[22:24], 1)
	binary.LittleEndian.PutUint32(header[24:28], uint32(w.sampleRate))
	binary.LittleEndian.PutUint32(header[28:32], uint32(w.sampleRate*2))
	binary.LittleEndian.PutUint16(header[32:34], 2)
	binary.LittleEndian.PutUint16(header[34:36], 16)
	copy(header[36:40], "data")
	binary.LittleEndian.PutUint32(header[40:44], dataSize)
	if _, err := w.file.Seek(0, io.SeekStart); err != nil {
		return errors.Join(err, w.file.Close())
	}
	_, writeErr := w.file.Write(header)
	return errors.Join(writeErr, w.file.Close())
}

type sessionRuntimeObserverFanout []SessionRuntimeObserver

func (f sessionRuntimeObserverFanout) ObserveSessionRuntime(observation SessionRuntimeObservation) {
	for _, observer := range f {
		if observer != nil {
			observer.ObserveSessionRuntime(observation)
		}
	}
}

// CombineSessionRuntimeObservers preserves all non-nil observers in order.
func CombineSessionRuntimeObservers(observers ...SessionRuntimeObserver) SessionRuntimeObserver {
	filtered := make(sessionRuntimeObserverFanout, 0, len(observers))
	for _, observer := range observers {
		if observer != nil {
			filtered = append(filtered, observer)
		}
	}
	if len(filtered) == 0 {
		return nil
	}
	return filtered
}
