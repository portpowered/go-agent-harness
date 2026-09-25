package recording

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
	"sync"
)

// Replay validates the complete evidence before exposing any frame. Stepping
// advances only its virtual clock; no device, provider, tool or network is
// available to this package. Callers may feed returned frames to route buffers.
type Replay struct {
	events  []Event
	streams map[string][]int16
	cursor  int
	Clock   *clock.Deterministic
}

const (
	replayEventAudio   = "audio"
	replayEventRuntime = "runtime"
	replayEventStarted = "recording_started"
	replayEventClosed  = "recording_closed"
)

type replayLifecycle struct {
	started bool
	closed  bool
}

func (lifecycle *replayLifecycle) admit(event Event, eventCount int) error {
	if lifecycle.closed {
		return fmt.Errorf("%w: event %q follows recording_closed", ErrIncomplete, event.Kind)
	}
	switch event.Kind {
	case replayEventStarted:
		if lifecycle.started || eventCount != 0 {
			return fmt.Errorf("%w: duplicate or nonleading recording_started", ErrIncomplete)
		}
		lifecycle.started = true
	case replayEventClosed:
		if !lifecycle.started || !event.Clean {
			return fmt.Errorf("%w: recording_closed must be one final clean close", ErrIncomplete)
		}
		lifecycle.closed = true
	case replayEventAudio, replayEventRuntime:
		if !lifecycle.started {
			return fmt.Errorf("%w: recording must begin with recording_started", ErrIncomplete)
		}
	case "trace_overflow":
		return ErrIncomplete
	}
	return nil
}

func readReplayWave(wave io.ReadCloser) (int, []int16, error) {
	rate, samples, readErr := wavio.Read(wave)
	closeErr := wave.Close()
	return rate, samples, errors.Join(readErr, closeErr)
}

// replayScanInitialBufferBytes is the scanner's starting line buffer; lines
// may grow up to twice the runtime payload budget.
const replayScanInitialBufferBytes = 4096

// OpenReplay loads and validates the complete recording in directory.
func OpenReplay(directory string) (result *Replay, err error) {
	file, err := os.Open(filepath.Join(directory, "timeline.jsonl"))
	if err != nil {
		return nil, err
	}
	defer func() {
		// The timeline is opened read-only and every record has been validated
		// before success is returned, so a close failure cannot invalidate a
		// successful replay; it is only reported alongside an open failure.
		if closeErr := file.Close(); closeErr != nil && err != nil {
			err = errors.Join(err, closeErr)
		}
	}()
	return readReplayTimeline(directory, file)
}

// replayBuilder accumulates validated timeline state while OpenReplay scans.
type replayBuilder struct {
	directory string
	result    *Replay
	positions map[string]uint64
	rates     map[string]int
	base      time.Time
	lifecycle replayLifecycle
}

func readReplayTimeline(directory string, timeline io.Reader) (*Replay, error) {
	builder := &replayBuilder{
		directory: directory,
		result:    &Replay{streams: make(map[string][]int16)},
		positions: make(map[string]uint64),
		rates:     make(map[string]int),
	}
	scan := bufio.NewScanner(timeline)
	scan.Buffer(make([]byte, replayScanInitialBufferBytes), 2*MaxRuntimePayloadBytes)
	for scan.Scan() {
		if err := builder.admit(scan.Bytes()); err != nil {
			return nil, err
		}
	}
	if err := scan.Err(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrIncomplete, err)
	}
	return builder.finish()
}

func (b *replayBuilder) admit(line []byte) error {
	var event Event
	eventCount := len(b.result.events)
	if err := json.Unmarshal(line, &event); err != nil {
		return fmt.Errorf("%w: invalid timeline: %w", ErrIncomplete, err)
	}
	if event.Version != SchemaVersion {
		return fmt.Errorf("unsupported audio trace schema %d", event.Version)
	} else if event.Sequence != uint64(eventCount+1) ||
		event.ElapsedNS < 0 {
		return fmt.Errorf("%w: invalid timeline sequence/time", ErrIncomplete)
	}
	if err := b.admitTimestamp(event, eventCount); err != nil {
		return err
	}
	if err := b.lifecycle.admit(event, eventCount); err != nil {
		return err
	}
	if err := b.admitKind(event); err != nil {
		return err
	}
	b.result.events = append(b.result.events, event)
	return nil
}

func (b *replayBuilder) admitTimestamp(event Event, eventCount int) error {
	if event.Timestamp == "" && event.Kind != replayEventClosed {
		return fmt.Errorf("%w: invalid timeline timestamp", ErrIncomplete)
	} else if event.Timestamp != "" {
		timestamp, timestampErr := time.Parse(time.RFC3339Nano, event.Timestamp)
		if timestampErr != nil {
			return fmt.Errorf("%w: invalid timeline timestamp", ErrIncomplete)
		}
		if eventCount == 0 {
			b.base = timestamp
		} else if timestamp.Sub(b.base) != time.Duration(event.ElapsedNS) {
			return fmt.Errorf("%w: timeline timestamp does not match elapsed time", ErrIncomplete)
		}
	} else if eventCount == 0 {
		return fmt.Errorf("%w: invalid recording epoch", ErrIncomplete)
	}
	return nil
}

func (b *replayBuilder) admitKind(event Event) error {
	switch event.Kind {
	case replayEventAudio:
		return b.admitAudio(event)
	case "trace_overflow":
		return ErrIncomplete
	case replayEventStarted, replayEventRuntime, replayEventClosed:
		return nil
	default:
		return fmt.Errorf("unsupported trace event %q", event.Kind)
	}
}

func (b *replayBuilder) admitAudio(event Event) error {
	if event.SampleCount <= 0 || event.SampleCount > MaxBlockSamples || event.StartSample != b.positions[event.Tap] {
		return fmt.Errorf("%w: audio gap or invalid block for %s", ErrIncomplete, event.Tap)
	}
	if _, ok := b.result.streams[event.Tap]; !ok {
		if err := b.loadStream(event.Tap); err != nil {
			return err
		}
	}
	samples := b.result.streams[event.Tap]
	end := event.StartSample + uint64(event.SampleCount)
	if event.SampleRate != b.rates[event.Tap] || end > uint64(len(samples)) || hashPCM(samples[event.StartSample:end]) != event.PCMHash {
		return fmt.Errorf("%w: PCM integrity mismatch for %s at %d", ErrIncomplete, event.Tap, event.StartSample)
	}
	b.positions[event.Tap] = end
	return nil
}

func (b *replayBuilder) loadStream(tap string) error {
	name := replayTraceFileName(tap)
	if name == "" {
		return fmt.Errorf("unknown audio trace tap %q", tap)
	}
	wave, err := os.Open(filepath.Join(b.directory, name))
	if err != nil {
		return err
	}
	rate, samples, err := readReplayWave(wave)
	if err != nil {
		return err
	}
	b.result.streams[tap], b.rates[tap] = samples, rate
	return nil
}

func replayTraceFileName(tap string) string {
	for i := 0; i < audioTraceTapCount; i++ {
		if traceTapName(i) == tap {
			return sessionAudioTraceFiles[i]
		}
	}
	return ""
}

func (b *replayBuilder) finish() (*Replay, error) {
	if len(b.result.events) < 2 || !b.lifecycle.started || !b.lifecycle.closed {
		return nil, ErrIncomplete
	}
	for tap, samples := range b.result.streams {
		if b.positions[tap] != uint64(len(samples)) {
			return nil, fmt.Errorf("%w: unaccounted PCM in %s", ErrIncomplete, tap)
		}
	}
	b.result.Clock = clock.NewDeterministic(b.base, time.Millisecond)
	return b.result, nil
}

// Next returns an owned PCM frame for audio records and a nil frame for
// control/runtime records. Arrival sequence is authoritative; simultaneous
// producers may carry earlier capture timestamps, so virtual time never
// moves backwards when processing their recorded admission order.
func (r *Replay) Next() (Event, *audio.PCMFrame, error) {
	if r.cursor == len(r.events) {
		return Event{}, nil, io.EOF
	}
	event := r.events[r.cursor]
	r.cursor++
	r.Clock.AdvanceToElapsed(time.Duration(event.ElapsedNS))
	if event.Kind != replayEventAudio {
		return event, nil, nil
	}
	samples := r.streams[event.Tap][event.StartSample : event.StartSample+uint64(event.SampleCount)]
	frame := &audio.PCMFrame{Samples: append([]int16(nil), samples...), Format: audio.PCM16DeviceFormat(event.SampleRate), StreamID: event.Tap, Sequence: event.Sequence, StartSample: event.StartSample}
	return event, frame, nil
}

// pooledSamples returns a pooled sample buffer. The trace pool only stores
// *[]int16; any other value is replaced by an empty buffer that the caller
// grows to the required size.
func pooledSamples(pool *sync.Pool) *[]int16 {
	if buffer, ok := pool.Get().(*[]int16); ok {
		return buffer
	}
	return new([]int16)
}
