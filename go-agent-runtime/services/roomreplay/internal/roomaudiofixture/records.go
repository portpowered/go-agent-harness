package roomaudiofixture

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

type deltaRecord struct {
	Type          string `json:"type"`
	Sequence      int    `json:"sequence"`
	DeltaID       string `json:"delta_id"`
	OffsetMS      int    `json:"offset_ms"`
	StreamID      string `json:"stream_id"`
	ParticipantID string `json:"participant_id"`
	TurnID        string `json:"turn_id,omitempty"`
	Delta         string `json:"delta"`
}

func deltaJSONL(id string, turns []turn, samples []int16) []byte {
	return deltaJSONLWithBoundaries(id, turns, samples, []int{0, 1200, 2200, 3200, len(samples)})
}

func deltaJSONLWithBoundaries(id string, turns []turn, samples []int16, boundaries []int) []byte {
	var result bytes.Buffer
	for index := 0; index < len(boundaries)-1; index++ {
		start, end := boundaries[index], boundaries[index+1]
		record := deltaRecord{
			Type:          "AUDIO.DELTA",
			Sequence:      index,
			DeltaID:       fmt.Sprintf("%s-delta-%d", id, index),
			OffsetMS:      start,
			StreamID:      id + ":output",
			ParticipantID: id,
			TurnID:        turnAt(turns, start),
			Delta:         base64.StdEncoding.EncodeToString(pcmBytes(samples[start:end])),
		}
		data, err := json.Marshal(record)
		if err != nil {
			panic(err)
		}
		result.Write(data)
		result.WriteByte('\n')
	}
	return result.Bytes()
}

func eventJSONL(id string, turns []turn) []byte {
	values := make([]map[string]any, 0, len(turns)*2)
	for _, currentTurn := range turns {
		values = append(values,
			map[string]any{"event": "turn_started", "participant_id": id, "turn_id": currentTurn.ID, "timestamp_ms": currentTurn.Start},
			map[string]any{"event": "turn_ended", "participant_id": id, "turn_id": currentTurn.ID, "timestamp_ms": currentTurn.End},
		)
	}
	return jsonLines(values)
}

func diagnosticJSONL(id string, turns []turn) []byte {
	values := make([]map[string]any, 0, len(turns))
	for _, currentTurn := range turns {
		values = append(values, map[string]any{
			"event":          "turn",
			"participant_id": id,
			"turn_id":        currentTurn.ID,
			"start_ms":       currentTurn.Start,
			"end_ms":         currentTurn.End,
			"status":         "complete",
		})
	}
	return jsonLines(values)
}

func timelineJSONL(clock time.Time, turns []turn) []byte {
	type timelineEntry struct {
		turn          turn
		participantID string
		typeName      string
		offset        int
	}
	entries := make([]timelineEntry, 0, len(turns)*2)
	for _, currentTurn := range turns {
		participantID := participantA
		if len(currentTurn.ID) <= len(participantA) || currentTurn.ID[:len(participantA)] != participantA {
			participantID = participantB
		}
		entries = append(entries,
			timelineEntry{turn: currentTurn, participantID: participantID, typeName: "turn_started", offset: currentTurn.Start},
			timelineEntry{turn: currentTurn, participantID: participantID, typeName: "turn_ended", offset: currentTurn.End},
		)
	}
	sort.SliceStable(entries, func(left, right int) bool {
		return entries[left].offset < entries[right].offset
	})
	values := make([]map[string]any, 0, len(entries))
	sequence := 0
	for _, entry := range entries {
		values = append(values, map[string]any{
			"sequence":            sequence,
			"monotonic_offset_ms": entry.offset,
			"unix_ms":             clock.UnixMilli() + int64(entry.offset),
			"type":                entry.typeName,
			"participant_id":      entry.participantID,
			"turn_id":             entry.turn.ID,
		})
		sequence++
	}
	return jsonLines(values)
}

func speechAnnotations(turns []turn) []map[string]any {
	result := make([]map[string]any, 0, len(turns))
	for _, currentTurn := range turns {
		result = append(result, map[string]any{"label": currentTurn.ID, "start_ms": currentTurn.Start, "end_ms": currentTurn.End})
	}
	return result
}

func chunkBoundaries(id string) []map[string]any {
	return chunkBoundariesFor(id, []int{0, 1200, 2200, 3200, duration})
}

func chunkBoundariesFor(id string, boundaries []int) []map[string]any {
	result := make([]map[string]any, 0, len(boundaries)-2)
	for index := 1; index < len(boundaries)-1; index++ {
		result = append(result, map[string]any{
			"id":           fmt.Sprintf("%s-chunk-%d", id, index),
			"sample_index": boundaries[index],
		})
	}
	return result
}

func turnAt(turns []turn, sample int) string {
	for _, currentTurn := range turns {
		if sample >= currentTurn.Start && sample < currentTurn.End {
			return currentTurn.ID
		}
	}
	return ""
}

func artifactRefFor(path string, data []byte) artifactRef {
	digest := sha256.Sum256(data)
	return artifactRef{Path: path, Size: len(data), SHA256: hex.EncodeToString(digest[:])}
}

func jsonLines(values []map[string]any) []byte {
	var result bytes.Buffer
	for _, value := range values {
		data, err := json.Marshal(value)
		if err != nil {
			panic(err)
		}
		result.Write(data)
		result.WriteByte('\n')
	}
	return result.Bytes()
}
