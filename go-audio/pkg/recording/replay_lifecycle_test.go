package recording

import (
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"
)

func TestReplayLifecycleAcceptsOneCleanSession(t *testing.T) {
	directory := t.TempDir()
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	writeTimelineEvents(t, directory, []Event{
		{Version: SchemaVersion, Sequence: 1, Kind: replayEventStarted, Timestamp: base.Format(time.RFC3339Nano)},
		{Version: SchemaVersion, Sequence: 2, Kind: replayEventRuntime, ElapsedNS: int64(time.Millisecond), Timestamp: base.Add(time.Millisecond).Format(time.RFC3339Nano)},
		{Version: SchemaVersion, Sequence: 3, Kind: replayEventClosed, ElapsedNS: 2 * int64(time.Millisecond), Clean: true},
	})

	replay, err := OpenReplay(directory)
	if err != nil {
		t.Fatalf("OpenReplay(valid empty session): %v", err)
	}
	if replay == nil {
		t.Fatal("OpenReplay(valid empty session) returned nil replay")
	}
	if event, frame, nextErr := replay.Next(); nextErr != nil || frame != nil || event.Kind != replayEventStarted {
		t.Fatalf("first event = %#v, frame %v, err %v; want recording_started and nil frame", event, frame, nextErr)
	}
	if event, frame, nextErr := replay.Next(); nextErr != nil || frame != nil || event.Kind != replayEventRuntime {
		t.Fatalf("runtime event = %#v, frame %v, err %v; want runtime and nil frame", event, frame, nextErr)
	}
	if event, frame, nextErr := replay.Next(); nextErr != nil || frame != nil || event.Kind != replayEventClosed {
		t.Fatalf("terminal event = %#v, frame %v, err %v; want recording_closed and nil frame", event, frame, nextErr)
	}
	if _, _, nextErr := replay.Next(); !errors.Is(nextErr, io.EOF) {
		t.Fatalf("replay after final close = %v, want EOF", nextErr)
	}
}

func TestReplayLifecycleRejectsMalformedSessionBeforeExposure(t *testing.T) {
	tests := []struct {
		name  string
		build func(t *testing.T) string
	}{
		{
			name: "early close runtime later close",
			build: func(t *testing.T) string {
				directory := newRecordingFixture(t)
				events := decodeTimelineEvents(t, directory)
				terminal := events[len(events)-1]
				terminal.Kind = replayEventClosed
				terminal.Clean = true
				runtime := Event{Kind: replayEventRuntime, ElapsedNS: terminal.ElapsedNS, Timestamp: terminal.Timestamp, RuntimeKind: "after-close"}
				writeTimelineEvents(t, directory, append(events[:len(events)-1], terminal, runtime, terminal))
				return directory
			},
		},
		{
			name: "audio after early close",
			build: func(t *testing.T) string {
				directory := newRecordingFixture(t)
				events := decodeTimelineEvents(t, directory)
				terminal := events[len(events)-1]
				audio := events[1]
				writeTimelineEvents(t, directory, []Event{events[0], terminal, audio, terminal})
				return directory
			},
		},
		{
			name: "duplicate start",
			build: func(t *testing.T) string {
				directory := newRecordingFixture(t)
				events := decodeTimelineEvents(t, directory)
				writeTimelineEvents(t, directory, append([]Event{events[0], events[0]}, events[1:]...))
				return directory
			},
		},
		{
			name: "duplicate close",
			build: func(t *testing.T) string {
				directory := t.TempDir()
				base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
				closeEvent := Event{Kind: replayEventClosed, ElapsedNS: int64(time.Millisecond), Timestamp: base.Add(time.Millisecond).Format(time.RFC3339Nano), Clean: true}
				writeTimelineEvents(t, directory, []Event{
					{Kind: replayEventStarted, Timestamp: base.Format(time.RFC3339Nano)},
					closeEvent,
					closeEvent,
				})
				return directory
			},
		},
		{
			name: "nonleading start after runtime",
			build: func(t *testing.T) string {
				directory := t.TempDir()
				base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
				writeTimelineEvents(t, directory, []Event{
					{Kind: replayEventStarted, Timestamp: base.Format(time.RFC3339Nano)},
					{Kind: replayEventRuntime, ElapsedNS: int64(time.Millisecond), Timestamp: base.Add(time.Millisecond).Format(time.RFC3339Nano), RuntimeKind: "inside"},
					{Kind: replayEventStarted, Timestamp: base.Add(time.Millisecond).Format(time.RFC3339Nano)},
					{Kind: replayEventClosed, ElapsedNS: 2 * int64(time.Millisecond), Timestamp: base.Add(2 * time.Millisecond).Format(time.RFC3339Nano), Clean: true},
				})
				return directory
			},
		},
		{
			name: "missing leading start",
			build: func(t *testing.T) string {
				directory := t.TempDir()
				base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
				writeTimelineEvents(t, directory, []Event{
					{Kind: replayEventRuntime, Timestamp: base.Format(time.RFC3339Nano), RuntimeKind: "before-start"},
					{Kind: replayEventClosed, ElapsedNS: int64(time.Millisecond), Timestamp: base.Add(time.Millisecond).Format(time.RFC3339Nano), Clean: true},
				})
				return directory
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := test.build(t)
			replay, err := OpenReplay(directory)
			if replay != nil {
				t.Fatalf("OpenReplay returned replay before lifecycle validation: %#v", replay)
			}
			if !errors.Is(err, ErrIncomplete) {
				t.Fatalf("OpenReplay error = %v, want ErrIncomplete", err)
			}
		})
	}
}

func TestReplayLifecycleRejectsValidTraceMutatedAfterFirstClose(t *testing.T) {
	directory := newRecordingFixture(t)
	events := decodeTimelineEvents(t, directory)
	terminal := events[len(events)-1]
	mutated := append([]Event{}, events[:len(events)-1]...)
	mutated = append(mutated, terminal)
	mutated = append(mutated, Event{Kind: replayEventRuntime, ElapsedNS: terminal.ElapsedNS, Timestamp: terminal.Timestamp, RuntimeKind: "post-close-runtime"})
	mutated = append(mutated, terminal)
	writeTimelineEvents(t, directory, mutated)

	replay, err := OpenReplay(directory)
	if replay != nil {
		t.Fatalf("OpenReplay returned a replay for a trace mutated after its first close: %#v", replay)
	}
	if !errors.Is(err, ErrIncomplete) {
		t.Fatalf("mutated trace error = %v, want ErrIncomplete", err)
	}
}

func decodeTimelineEvents(t *testing.T, directory string) []Event {
	t.Helper()
	var events []Event
	for _, line := range readTimeline(t, directory) {
		var event Event
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatalf("decode timeline: %v", err)
		}
		events = append(events, event)
	}
	return events
}

func writeTimelineEvents(t *testing.T, directory string, events []Event) {
	t.Helper()
	base, err := time.Parse(time.RFC3339Nano, events[0].Timestamp)
	if err != nil {
		t.Fatalf("parse fixture epoch: %v", err)
	}
	for index := range events {
		events[index].Version = SchemaVersion
		events[index].Sequence = uint64(index + 1)
		if events[index].Timestamp != "" {
			events[index].Timestamp = base.Add(time.Duration(events[index].ElapsedNS)).Format(time.RFC3339Nano)
		}
	}
	lines := make([][]byte, 0, len(events))
	for _, event := range events {
		line, err := json.Marshal(event)
		if err != nil {
			t.Fatalf("encode timeline: %v", err)
		}
		lines = append(lines, line)
	}
	writeTimeline(t, directory, lines)
}
