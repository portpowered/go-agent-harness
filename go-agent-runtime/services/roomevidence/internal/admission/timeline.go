package admission

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomevidence/internal/pathguard"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
)

const (
	timelineScannerInitialBytes = 4 << 10
	timelineScannerMaxBytes     = 1 << 20
	timelineInitialCapacity     = 64
)

const (
	// MaxTimelineBytes and MaxTimelineEvents bound retained timeline memory.
	MaxTimelineBytes  int64 = 32 << 20
	MaxTimelineEvents       = 100_000
)

func loadTimeline(path string, participants []rooms.RoomReplayParticipant, clockBase, started, ended time.Time) (result []rooms.RoomReplayTimelineEvent, err error) {
	if err := pathguard.ValidateRegularFile(path); err != nil {
		return nil, incomplete("room_timeline", err)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, incomplete("room_timeline", err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			err = errors.Join(err, incomplete("room_timeline", fmt.Errorf("close timeline: %w", closeErr)))
		}
	}()
	known := timelineParticipants(participants)
	limited := &io.LimitedReader{R: file, N: MaxTimelineBytes + 1}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, timelineScannerInitialBytes), timelineScannerMaxBytes)
	result = make([]rooms.RoomReplayTimelineEvent, 0, timelineInitialCapacity)
	var previous int64 = -1
	for line := 1; scanner.Scan(); line++ {
		raw := append([]byte(nil), scanner.Bytes()...)
		if strings.TrimSpace(string(raw)) == "" {
			continue
		}
		if len(result) >= MaxTimelineEvents {
			return nil, mismatch("room_timeline", fmt.Errorf("timeline exceeds the %d-event limit", MaxTimelineEvents))
		}
		event, err := parseTimelineLine(raw, line, int64(len(result)), previous, known, clockBase, started, ended)
		if err != nil {
			return nil, err
		}
		previous = event.Sequence
		result = append(result, event)
	}
	if err := scanner.Err(); err != nil {
		return nil, incomplete("room_timeline", err)
	}
	if limited.N == 0 {
		return nil, mismatch("room_timeline", fmt.Errorf("timeline exceeds the %d-byte limit", MaxTimelineBytes))
	}
	return result, nil
}

func timelineParticipants(participants []rooms.RoomReplayParticipant) map[string]struct{} {
	known := make(map[string]struct{}, len(participants))
	for _, participant := range participants {
		known[participant.ID] = struct{}{}
	}
	return known
}

func parseTimelineLine(raw []byte, line int, fallback, previous int64, known map[string]struct{}, clockBase, started, ended time.Time) (rooms.RoomReplayTimelineEvent, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return rooms.RoomReplayTimelineEvent{}, mismatch("room_timeline.line."+strconv.Itoa(line), err)
	}
	sequence := timelineSequence(object, fallback)
	if sequence <= previous {
		return rooms.RoomReplayTimelineEvent{}, mismatch("room_timeline.sequence", fmt.Errorf("sequence is not increasing"))
	}
	offsetNanos, offsetErr := timelineOffsetNanos(object)
	if offsetErr != nil {
		return rooms.RoomReplayTimelineEvent{}, mismatch("room_timeline.offset_ms", offsetErr)
	}
	participantID := rawString(object, "participant_id")
	if participantID == "" {
		participantID = rawString(object, "participant")
	}
	if err := validateTimelineParticipant(participantID, known); err != nil {
		return rooms.RoomReplayTimelineEvent{}, err
	}
	at := clockBase.Add(time.Duration(offsetNanos))
	if at.Before(started) || at.After(ended) {
		return rooms.RoomReplayTimelineEvent{}, mismatch("room_timeline.offset_ms", fmt.Errorf("event is outside recorded timing"))
	}
	eventType := rawString(object, "type")
	if eventType == "" {
		eventType = rawString(object, "event")
	}
	return rooms.RoomReplayTimelineEvent{Sequence: sequence, OffsetMS: offsetNanos / int64(time.Millisecond), OffsetNanos: offsetNanos, UnixMS: at.UnixMilli(), Type: eventType, ParticipantID: participantID, Raw: raw}, nil
}

func timelineSequence(object map[string]json.RawMessage, fallback int64) int64 {
	sequence, ok := rawInt64(object, "sequence")
	if !ok {
		return fallback
	}
	return sequence
}

func timelineOffsetNanos(object map[string]json.RawMessage) (int64, error) {
	for _, name := range []string{"t_offset_ms", "offset_ms", "monotonic_offset_ms"} {
		raw, ok := object[name]
		if !ok {
			continue
		}
		var milliseconds float64
		if err := json.Unmarshal(raw, &milliseconds); err != nil {
			return 0, err
		}
		if math.IsNaN(milliseconds) || math.IsInf(milliseconds, 0) || milliseconds < 0 {
			return 0, fmt.Errorf("offset must be a finite non-negative number")
		}
		nanos := milliseconds * float64(time.Millisecond)
		if nanos > float64(math.MaxInt64) {
			return 0, fmt.Errorf("offset is too large")
		}
		return int64(math.Round(nanos)), nil
	}
	return 0, nil
}

func validateTimelineParticipant(participantID string, known map[string]struct{}) error {
	if participantID == "" {
		return nil
	}
	if _, exists := known[participantID]; !exists {
		return mismatch("room_timeline.participant_id", fmt.Errorf("unknown participant %q", participantID))
	}
	return nil
}

func rawString(value map[string]json.RawMessage, name string) string {
	var result string
	if raw, ok := value[name]; ok {
		if err := json.Unmarshal(raw, &result); err != nil {
			return ""
		}
	}
	return result
}

func rawInt64(value map[string]json.RawMessage, name string) (int64, bool) {
	var result int64
	raw, ok := value[name]
	if !ok || json.Unmarshal(raw, &result) != nil {
		return 0, false
	}
	return result, true
}
