package agentruntime

import "github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"

import (
	"encoding/json"
	"errors"
	"fmt"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	"os"
	"sync"
	"time"
)

// roomClock stamps room evidence records with both a monotonic offset from
// the room's real start time and a Unix-millisecond wall-clock timestamp.
// Earlier room bundles had no shared clock at all: agent-<id>.deltas.jsonl
// carried only a per-session monotonic index, so cross-participant latency
// (barge-in response time, turn-to-turn gaps) could not be computed from the
// bundle. Every recorded stream now carries both fields from this one clock.
type roomClock struct {
	start  time.Time
	source platformclock.Source
}

// newRoomClock anchors a clock at the room's actual start time rather than
// the Unix epoch, so manifest.clock_base is a real, comparable wall-clock
// timestamp.
// newRoomClock anchors offsets to the room's start time. The source must be
// the same clock that produced start: a room running on an injected clock would
// otherwise measure elapsed time against real wall time, and the difference
// between the two epochs — not the elapsed room time — would become the offset.
func newRoomClock(start time.Time, sources ...platformclock.Source) roomClock {
	var source platformclock.Source
	if len(sources) > 0 {
		source = sources[0]
	}
	clock := platformclock.Ensure(source)
	if start.IsZero() {
		start = clock.Now().UTC()
	}
	return roomClock{start: start.UTC(), source: clock}
}

// now returns the elapsed time since room start and the current wall-clock
// time in Unix milliseconds.
func (c roomClock) now() (time.Duration, int64) {
	current := platformclock.Ensure(c.source).Now().UTC()
	return current.Sub(c.start), current.UnixMilli()
}

// roomClockOffsetMillis renders a duration as fractional milliseconds so
// 20ms-cadence audio frames remain distinguishable in the recorded offset.
func roomClockOffsetMillis(offset time.Duration) float64 {
	return float64(offset) / float64(time.Millisecond)
}

// injectRoomWallClock adds t_offset_ms and t_unix_ms fields to an encoded
// JSON object without disturbing its existing shape, so every event in the
// per-participant delta/diagnostics streams carries both a monotonic offset
// and a wall-clock timestamp. data must already be a JSON object.
func injectRoomWallClock(data []byte, offset time.Duration, unixMs int64) ([]byte, error) {
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, fmt.Errorf("decode JSONL record for wall-clock stamping: %w", err)
	}
	if decoded == nil {
		decoded = make(map[string]any, 2)
	}
	decoded["t_offset_ms"] = roomClockOffsetMillis(offset)
	decoded["t_unix_ms"] = unixMs
	return json.Marshal(decoded)
}

// pcm16HasSignal reports whether a raw little-endian PCM16 buffer contains
// any non-zero byte. The room mixer emits an exact all-zero frame whenever no
// active input contributed samples, so this doubles as a cheap silence test
// for both the room's own energy-based speech-segment tracking and the
// dropped-audio diagnostic below.
func pcm16HasSignal(pcm []byte) bool {
	for _, value := range pcm {
		if value != 0 {
			return true
		}
	}
	return false
}

// roomSpeechTracker turns a stream of per-frame silent/non-silent
// observations into speech_start/speech_end transitions. It is intentionally
// energy-based rather than dependent on provider-specific framing events
// (AUDIO.START/END, VAD.SPEECH_STARTED/STOPPED), so it works uniformly for
// every provider, for human participants, and for hermetic scripted-provider
// tests that never emit those framing events at all.
type roomSpeechTracker struct {
	mu     sync.Mutex
	active bool
}

// transition returns "start" or "end" when nonSilent flips the tracked
// state, or "" when the frame continues the current state.
func (t *roomSpeechTracker) transition(nonSilent bool) string {
	if t == nil {
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if nonSilent && !t.active {
		t.active = true
		return "start"
	}
	if !nonSilent && t.active {
		t.active = false
		return "end"
	}
	return ""
}

// rawPCMWriter appends raw little-endian PCM16 bytes to a file with no
// header, matching the "raw PCM16, format/rate named in the manifest"
// contract for participants/<id>/sent.pcm and participants/<id>/received.pcm.
type rawPCMWriter struct {
	path string
	file *os.File

	mu     sync.Mutex
	bytes  uint64
	closed bool
	err    error
}

func newRawPCMWriter(path string) (*rawPCMWriter, error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	return &rawPCMWriter{path: path, file: file}, nil
}

func (w *rawPCMWriter) write(pcm []byte) error {
	if w == nil {
		return errors.New("raw PCM writer is not initialized")
	}
	if len(pcm) == 0 {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		if w.err != nil {
			return w.err
		}
		return errors.New("raw PCM writer is closed")
	}
	if w.err != nil {
		return w.err
	}
	written, err := writeSelfPlayAllCount(w.file, pcm)
	w.bytes += uint64(written)
	if err != nil {
		w.err = fmt.Errorf("write %s: %w", w.path, err)
	}
	return w.err
}

func (w *rawPCMWriter) close() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return w.err
	}
	w.closed = true
	if syncErr := w.file.Sync(); syncErr != nil {
		w.err = errors.Join(w.err, fmt.Errorf("sync %s: %w", w.path, syncErr))
	}
	if closeErr := w.file.Close(); closeErr != nil {
		w.err = errors.Join(w.err, fmt.Errorf("close %s: %w", w.path, closeErr))
	}
	return w.err
}

// roomMixBuffer accumulates every participant's own spoken audio onto one
// shared, wall-clock-addressed timeline, producing the composite "fly on the
// wall" room-mix.wav. Samples are summed (not concatenated) at the sample
// position implied by each chunk's real arrival offset from room start, so
// overlapping speech from multiple participants is audible together, exactly
// as a listener physically present in the room would hear it.
type roomMixBuffer struct {
	sampleRate int

	mu      sync.Mutex
	samples []int32
}

func newRoomMixBuffer(sampleRate int) *roomMixBuffer {
	if sampleRate <= 0 {
		sampleRate = DefaultRoomMixSampleRate
	}
	return &roomMixBuffer{sampleRate: sampleRate}
}

// DefaultRoomMixSampleRate is used only if a room somehow finalizes with no
// configured PCM16 format; ordinary rooms always pass their real mixer rate.
const DefaultRoomMixSampleRate = 24000

// mixAt sums an interleaved little-endian PCM16 chunk into the composite
// buffer starting at the sample position implied by offset, extending the
// buffer with silence as needed. The running sum is int32 so that transient
// overlap of several loud speakers does not clip prematurely; final clipping
// happens once, at finalize.
func (b *roomMixBuffer) mixAt(offset time.Duration, pcm []byte) error {
	if b == nil || len(pcm) == 0 {
		return nil
	}
	decoded, err := codec.DecodePCM16WithLimit(pcm, len(pcm))
	if err != nil {
		return fmt.Errorf("decode room mix PCM16: %w", err)
	}
	sampleCount := len(pcm) / 2
	startSample := int(offset.Seconds() * float64(b.sampleRate))
	if startSample < 0 {
		startSample = 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	needed := startSample + sampleCount
	if needed > len(b.samples) {
		grown := make([]int32, needed)
		copy(grown, b.samples)
		b.samples = grown
	}
	for index := 0; index < sampleCount; index++ {
		b.samples[startSample+index] += int32(decoded[index])
	}
	return nil
}

// finalize pads the composite buffer to match the room's total wall-clock
// span (so room-mix.wav duration matches the room's real duration even when
// the last chunk of audio ended before the room did), clips to PCM16 range,
// and writes it as a mono WAV file.
func (b *roomMixBuffer) finalize(span time.Duration, path string) error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	targetSamples := int(span.Seconds() * float64(b.sampleRate))
	if targetSamples < 0 {
		targetSamples = 0
	}
	if targetSamples > len(b.samples) {
		grown := make([]int32, targetSamples)
		copy(grown, b.samples)
		b.samples = grown
	}
	samples := append([]int32(nil), b.samples...)
	b.mu.Unlock()

	pcmSamples := make([]int16, len(samples))
	for index, sample := range samples {
		if sample > 32767 {
			sample = 32767
		} else if sample < -32768 {
			sample = -32768
		}
		pcmSamples[index] = int16(sample)
	}
	pcm := codec.EncodePCM16(pcmSamples)

	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	header, err := wavio.PCM16Header(b.sampleRate, uint64(len(pcm)))
	if err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return err
	}
	if _, err := writeSelfPlayAllCount(file, header[:]); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return fmt.Errorf("write room mix WAV header: %w", err)
	}
	if _, err := writeSelfPlayAllCount(file, pcm); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return fmt.Errorf("write room mix WAV data: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync room mix WAV: %w", err)
	}
	return file.Close()
}

// roomTimelineEntry is one machine-readable room-timeline.jsonl record: an
// ordered, wall-clock-stamped observation of the room's conversational
// shape (who spoke when, response boundaries, barge-in/cancel outcomes, tool
// calls, and turn transitions) independent of any single participant's own
// stream.
type roomTimelineEntry struct {
	TOffsetMS   float64           `json:"t_offset_ms"`
	TUnixMS     int64             `json:"t_unix_ms"`
	Event       string            `json:"event"`
	Participant string            `json:"participant,omitempty"`
	Fields      map[string]string `json:"fields,omitempty"`
}

// roomTimeline is the room-level companion to the per-participant delta and
// diagnostics streams: one ordered JSONL log of room-wide events, so a whole
// conversation's shape (including cross-participant overlap) is machine
// readable without reconstructing it from N separate per-participant files.
type roomTimeline struct {
	writer *selfPlayJSONLWriter
	clock  roomClock

	// mu serializes the clock read and the resulting write. record is called
	// concurrently from every participant goroutine; without this lock, two
	// concurrent callers can read their timestamps in one order (A before B)
	// but land their writes in the other order (B's write completing before
	// A's), producing a room-timeline.jsonl whose offsets are not
	// monotonically increasing even though each individual timestamp is
	// accurate. Replay admission validates strict offset/sequence ordering,
	// so that race makes an otherwise-valid recording unreplayable.
	mu sync.Mutex
}

func newRoomTimeline(path string, clock roomClock) (*roomTimeline, error) {
	writer, err := newSelfPlayJSONLWriter(path)
	if err != nil {
		return nil, err
	}
	return &roomTimeline{writer: writer, clock: clock}, nil
}

func (t *roomTimeline) record(event, participant string, fields map[string]string) error {
	_, _, err := t.recordNow(event, participant, fields)
	return err
}

// recordNow is the primitive record builds on; it additionally returns the
// exact clock reading (offset since room start, and the equivalent Unix
// millisecond timestamp) that was written for this event, so a caller that
// needs to reason about that specific instant -- rather than re-reading the
// clock a moment later, which is necessarily a different instant -- can do
// so without a second, racing clock read.
func (t *roomTimeline) recordNow(event, participant string, fields map[string]string) (time.Duration, int64, error) {
	if t == nil || t.writer == nil {
		return 0, 0, nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	offset, unixMs := t.clock.now()
	err := t.writer.write(roomTimelineEntry{
		TOffsetMS:   roomClockOffsetMillis(offset),
		TUnixMS:     unixMs,
		Event:       event,
		Participant: participant,
		Fields:      fields,
	})
	return offset, unixMs, err
}

func (t *roomTimeline) close() error {
	if t == nil || t.writer == nil {
		return nil
	}
	return t.writer.close()
}
