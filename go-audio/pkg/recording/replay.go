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

func OpenReplay(directory string) (*Replay, error) {
	file, err := os.Open(filepath.Join(directory, "timeline.jsonl"))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	result, scan := &Replay{streams: make(map[string][]int16)}, bufio.NewScanner(file)
	scan.Buffer(make([]byte, 4096), 2*MaxRuntimePayloadBytes)
	positions, rates := make(map[string]uint64), make(map[string]int)
	base, lifecycle := time.Time{}, replayLifecycle{}
	for scan.Scan() {
		var event Event
		eventCount := len(result.events)
		if err := json.Unmarshal(scan.Bytes(), &event); err != nil {
			return nil, fmt.Errorf("%w: invalid timeline: %v", ErrIncomplete, err)
		}
		if event.Version != SchemaVersion {
			return nil, fmt.Errorf("unsupported audio trace schema %d", event.Version)
		} else if event.Sequence != uint64(eventCount+1) ||
			event.ElapsedNS < 0 {
			return nil, fmt.Errorf("%w: invalid timeline sequence/time", ErrIncomplete)
		}
		if event.Timestamp == "" && event.Kind != replayEventClosed {
			return nil, fmt.Errorf("%w: invalid timeline timestamp", ErrIncomplete)
		} else if event.Timestamp != "" {
			timestamp, timestampErr := time.Parse(time.RFC3339Nano, event.Timestamp)
			if timestampErr != nil {
				return nil, fmt.Errorf("%w: invalid timeline timestamp", ErrIncomplete)
			}
			if eventCount == 0 {
				base = timestamp
			} else if timestamp.Sub(base) != time.Duration(event.ElapsedNS) {
				return nil, fmt.Errorf("%w: timeline timestamp does not match elapsed time", ErrIncomplete)
			}
		} else if eventCount == 0 {
			return nil, fmt.Errorf("%w: invalid recording epoch", ErrIncomplete)
		}
		if err := lifecycle.admit(event, eventCount); err != nil {
			return nil, err
		}
		switch event.Kind {
		case replayEventAudio:
			if event.SampleCount <= 0 || event.SampleCount > MaxBlockSamples || event.StartSample != positions[event.Tap] {
				return nil, fmt.Errorf("%w: audio gap or invalid block for %s", ErrIncomplete, event.Tap)
			}
			if _, ok := result.streams[event.Tap]; !ok {
				name := ""
				for i := 0; i < audioTraceTapCount; i++ {
					if traceTapName(i) == event.Tap {
						name = sessionAudioTraceFiles[i]
						break
					}
				}
				if name == "" {
					return nil, fmt.Errorf("unknown audio trace tap %q", event.Tap)
				}
				wave, err := os.Open(filepath.Join(directory, name))
				if err != nil {
					return nil, err
				}
				rate, samples, err := readReplayWave(wave)
				if err != nil {
					return nil, err
				}
				result.streams[event.Tap], rates[event.Tap] = samples, rate
			}
			samples := result.streams[event.Tap]
			end := event.StartSample + uint64(event.SampleCount)
			if event.SampleRate != rates[event.Tap] || end > uint64(len(samples)) || hashPCM(samples[event.StartSample:end]) != event.PCMHash {
				return nil, fmt.Errorf("%w: PCM integrity mismatch for %s at %d", ErrIncomplete, event.Tap, event.StartSample)
			}
			positions[event.Tap] = end
		case "trace_overflow":
			return nil, ErrIncomplete
		case replayEventStarted, replayEventRuntime, replayEventClosed:
		default:
			return nil, fmt.Errorf("unsupported trace event %q", event.Kind)
		}
		result.events = append(result.events, event)
	}
	if err := scan.Err(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrIncomplete, err)
	}
	if len(result.events) < 2 || !lifecycle.started || !lifecycle.closed {
		return nil, ErrIncomplete
	}
	for tap, samples := range result.streams {
		if positions[tap] != uint64(len(samples)) {
			return nil, fmt.Errorf("%w: unaccounted PCM in %s", ErrIncomplete, tap)
		}
	}
	result.Clock = clock.NewDeterministic(base, time.Millisecond)
	return result, nil
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
