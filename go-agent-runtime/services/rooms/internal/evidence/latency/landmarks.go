package latency

import (
	"errors"
	"fmt"
	"math"
	"sort"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

func roomLatencySpeakerLandmark(event rooms.RoomLatencyEvent, format rooms.RoomLatencyPCMFormat) (rooms.RoomLatencyLandmark, error) {
	sampleRate := event.SampleRateHz
	if sampleRate <= 0 {
		sampleRate = format.SampleRateHz
	}
	channels := event.Channels
	if channels <= 0 {
		channels = format.Channels
	}
	if event.Timestamp.IsZero() || event.PCMBytes <= 0 {
		return rooms.RoomLatencyLandmark{}, errors.New("invalid speaker PCM segment")
	}
	duration, err := audio.PCM16Duration(event.PCMBytes, sampleRate, channels)
	if err != nil {
		return rooms.RoomLatencyLandmark{}, fmt.Errorf("invalid speaker PCM segment: %w", err)
	}
	return rooms.RoomLatencyLandmark{
		Sequence:     event.Sequence,
		Tick:         event.Tick,
		Timestamp:    event.Timestamp.Add(duration),
		PCMBytes:     event.PCMBytes,
		SampleRateHz: sampleRate,
		Channels:     channels,
	}, nil
}

func roomLatencyEventLandmark(event rooms.RoomLatencyEvent) rooms.RoomLatencyLandmark {
	return rooms.RoomLatencyLandmark{Sequence: event.Sequence, Tick: event.Tick, Timestamp: event.Timestamp}
}

func roomLatencyLandmarksOrdered(landmarks []*rooms.RoomLatencyLandmark) bool {
	for index := 1; index < len(landmarks); index++ {
		previous, current := landmarks[index-1], landmarks[index]
		if current.Timestamp.After(previous.Timestamp) || current.Timestamp.Equal(previous.Timestamp) && current.Sequence >= previous.Sequence {
			continue
		}
		return false
	}
	return true
}

func summarizeRoomLatency(transitions []rooms.RoomLatencyTransition) rooms.RoomLatencySummary {
	detection, dispatch, provider, localOutput, harnessOwned, total := collectLatencySamples(transitions)
	return rooms.RoomLatencySummary{
		Detection:    roomLatencyStatistics(detection),
		Dispatch:     roomLatencyStatistics(dispatch),
		Provider:     roomLatencyStatistics(provider),
		LocalOutput:  roomLatencyStatistics(localOutput),
		HarnessOwned: roomLatencyStatistics(harnessOwned),
		Total:        roomLatencyStatistics(total),
	}
}

func collectLatencySamples(transitions []rooms.RoomLatencyTransition) (detection, dispatch, provider, localOutput, harnessOwned, total []int64) {
	for _, transition := range transitions {
		if !transition.Eligible {
			continue
		}
		detection = append(detection, transition.DetectionMS)
		dispatch = append(dispatch, transition.DispatchMS)
		provider = append(provider, transition.ProviderMS)
		localOutput = append(localOutput, transition.LocalOutputMS)
		harnessOwned = append(harnessOwned, transition.HarnessOwnedMS)
		total = append(total, transition.TotalMS)
	}
	return detection, dispatch, provider, localOutput, harnessOwned, total
}

func roomLatencyStatistics(values []int64) rooms.RoomLatencyStatistics {
	if len(values) == 0 {
		return rooms.RoomLatencyStatistics{}
	}
	sorted := append([]int64(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	median := sorted[len(sorted)/2]
	if len(sorted)%2 == 0 {
		median = (sorted[len(sorted)/2-1] + sorted[len(sorted)/2]) / 2
	}
	const percentile95 = 0.95
	rank := int(math.Ceil(percentile95*float64(len(sorted)))) - 1
	if rank < 0 {
		rank = 0
	}
	if rank >= len(sorted) {
		rank = len(sorted) - 1
	}
	return rooms.RoomLatencyStatistics{SampleCount: len(sorted), MedianMS: median, P95MS: sorted[rank], MaxMS: sorted[len(sorted)-1]}
}
