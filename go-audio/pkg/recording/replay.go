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

func OpenReplay(directory string) (*Replay, error) {
	file, err := os.Open(filepath.Join(directory, "timeline.jsonl"))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	result := &Replay{streams: make(map[string][]int16)}
	scan := bufio.NewScanner(file)
	scan.Buffer(make([]byte, 4096), 2*MaxRuntimePayloadBytes)
	positions := make(map[string]uint64)
	rates := make(map[string]int)
	var base time.Time
	for scan.Scan() {
		var event Event
		if err := json.Unmarshal(scan.Bytes(), &event); err != nil {
			return nil, fmt.Errorf("%w: invalid timeline: %v", ErrIncomplete, err)
		}
		if event.Version != SchemaVersion {
			return nil, fmt.Errorf("unsupported audio trace schema %d", event.Version)
		}
		if event.Sequence != uint64(len(result.events)+1) || event.ElapsedNS < 0 {
			return nil, fmt.Errorf("%w: invalid timeline sequence/time", ErrIncomplete)
		}
		if event.Timestamp == "" && event.Kind != "recording_closed" {
			return nil, fmt.Errorf("%w: invalid timeline timestamp", ErrIncomplete)
		}
		if event.Timestamp != "" {
			timestamp, timestampErr := time.Parse(time.RFC3339Nano, event.Timestamp)
			if timestampErr != nil {
				return nil, fmt.Errorf("%w: invalid timeline timestamp", ErrIncomplete)
			}
			if len(result.events) == 0 {
				base = timestamp
			} else if timestamp.Sub(base) != time.Duration(event.ElapsedNS) {
				return nil, fmt.Errorf("%w: timeline timestamp does not match elapsed time", ErrIncomplete)
			}
		} else if len(result.events) == 0 {
			return nil, fmt.Errorf("%w: invalid recording epoch", ErrIncomplete)
		}
		switch event.Kind {
		case "audio":
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
				rate, samples, readErr := wavio.Read(wave)
				closeErr := wave.Close()
				if err := errors.Join(readErr, closeErr); err != nil {
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
		case "recording_started", "runtime", "recording_closed":
		default:
			return nil, fmt.Errorf("unsupported trace event %q", event.Kind)
		}
		result.events = append(result.events, event)
	}
	if err := scan.Err(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrIncomplete, err)
	}
	if len(result.events) < 2 || result.events[0].Kind != "recording_started" || result.events[len(result.events)-1].Kind != "recording_closed" || !result.events[len(result.events)-1].Clean {
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
	if event.Kind != "audio" {
		return event, nil, nil
	}
	samples := r.streams[event.Tap][event.StartSample : event.StartSample+uint64(event.SampleCount)]
	frame := &audio.PCMFrame{Samples: append([]int16(nil), samples...), Format: audio.PCM16DeviceFormat(event.SampleRate), StreamID: event.Tap, Sequence: event.Sequence, StartSample: event.StartSample}
	return event, frame, nil
}
