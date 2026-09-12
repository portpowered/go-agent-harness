package service

import (
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/metricsreplay"
)

type seriesKey struct {
	direction metricsreplay.Direction
	modality  metricsreplay.Modality
}

func (k seriesKey) String() string {
	return string(k.direction) + "/" + string(k.modality)
}

func orderedKeys() []seriesKey {
	return []seriesKey{
		{direction: metricsreplay.DirectionInput, modality: metricsreplay.ModalityAudio},
		{direction: metricsreplay.DirectionInput, modality: metricsreplay.ModalityText},
		{direction: metricsreplay.DirectionInput, modality: metricsreplay.ModalityImage},
		{direction: metricsreplay.DirectionInput, modality: metricsreplay.ModalityTool},
		{direction: metricsreplay.DirectionOutput, modality: metricsreplay.ModalityAudio},
		{direction: metricsreplay.DirectionOutput, modality: metricsreplay.ModalityText},
		{direction: metricsreplay.DirectionOutput, modality: metricsreplay.ModalityImage},
		{direction: metricsreplay.DirectionOutput, modality: metricsreplay.ModalityTool},
	}
}

func project(snapshot metricsreplay.Snapshot, observed map[seriesKey]int64) ([]metricsreplay.Series, error) {
	reported, err := validateSnapshot(snapshot)
	if err != nil {
		return nil, err
	}
	if err := validateObserved(observed); err != nil {
		return nil, err
	}
	series := make([]metricsreplay.Series, 0, len(orderedKeys()))
	for _, key := range orderedKeys() {
		series = append(series, metricsreplay.Series{
			Direction:      key.direction,
			Modality:       key.modality,
			ObservedDeltas: observed[key],
			ReportedTotal:  reported[key],
		})
	}
	return series, nil
}

func validateSnapshot(snapshot metricsreplay.Snapshot) (map[seriesKey]int64, error) {
	reported := make(map[seriesKey]int64, len(snapshot.Series))
	for index, entry := range snapshot.Series {
		key := seriesKey{direction: entry.Direction, modality: entry.Modality}
		if !entry.Direction.Valid() || !entry.Modality.Valid() {
			return nil, fmt.Errorf("%w: unsupported series %q at index %d", metricsreplay.ErrInvalidSnapshot, key, index)
		}
		if entry.TotalBytes < 0 {
			return nil, fmt.Errorf("%w: negative total for %s", metricsreplay.ErrInvalidSnapshot, key)
		}
		if _, exists := reported[key]; exists {
			return nil, fmt.Errorf("%w: duplicate series %s", metricsreplay.ErrInvalidSnapshot, key)
		}
		reported[key] = entry.TotalBytes
	}
	return reported, nil
}

func validateObserved(observed map[seriesKey]int64) error {
	for key, deltaSum := range observed {
		if !isKnownKey(key) {
			return fmt.Errorf("%w: unsupported observed series %s", metricsreplay.ErrMalformedFixture, key)
		}
		if deltaSum < 0 {
			return fmt.Errorf("%w: negative observed total for %s", metricsreplay.ErrMalformedFixture, key)
		}
	}
	return nil
}

func isKnownKey(key seriesKey) bool {
	for _, known := range orderedKeys() {
		if known == key {
			return true
		}
	}
	return false
}
