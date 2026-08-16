package metrics

import (
	"reflect"
	"testing"
)

// SinkFactory constructs a Sink with the supplied histogram bounds. It is the
// seam used by the shared S11 contract suite so future implementations can
// prove the same behavior without copying these assertions.
type SinkFactory func(bounds []int64) (Sink, error)

// RunSinkConformanceSuite runs the shared S11 behavioral contract for a sink.
func RunSinkConformanceSuite(t *testing.T, factory SinkFactory) {
	t.Helper()
	sink, err := factory([]int64{0, 10, 100})
	if err != nil {
		t.Fatalf("construct sink: %v", err)
	}

	initial := sink.Snapshot()
	if !reflect.DeepEqual(initial.HistogramBounds, []int64{0, 10, 100}) {
		t.Fatalf("initial bounds: got %v", initial.HistogramBounds)
	}
	if len(initial.Series) != len(orderedSeriesKeys()) {
		t.Fatalf("initial series count: got %d, want %d", len(initial.Series), len(orderedSeriesKeys()))
	}
	for _, series := range initial.Series {
		if series.EventCount != 0 || series.TotalBytes != 0 || series.Histogram.SampleCount != 0 {
			t.Fatalf("initial series %s is not zero: %+v", SeriesKey{series.Direction, series.Modality}, series)
		}
		if !reflect.DeepEqual(series.Histogram.Bounds, []int64{0, 10, 100}) {
			t.Fatalf("initial series %s bounds: got %v", SeriesKey{series.Direction, series.Modality}, series.Histogram.Bounds)
		}
	}

	observations := []struct {
		direction Direction
		modality  Modality
		bytes     int64
	}{
		{DirectionInput, ModalityAudio, 0},
		{DirectionInput, ModalityAudio, 10},
		{DirectionInput, ModalityAudio, 11},
		{DirectionInput, ModalityAudio, 100},
		{DirectionInput, ModalityAudio, 101},
		{DirectionInput, ModalityText, 7},
		{DirectionOutput, ModalityAudio, 100},
		{DirectionOutput, ModalityText, 101},
	}
	for _, observation := range observations {
		if err := sink.Record(observation.direction, observation.modality, observation.bytes); err != nil {
			t.Fatalf("record %+v: %v", observation, err)
		}
	}

	snapshot := sink.Snapshot()
	inputAudio := snapshot.SeriesFor(DirectionInput, ModalityAudio)
	if inputAudio.EventCount != 5 || inputAudio.TotalBytes != 222 {
		t.Fatalf("input/audio counters: got count=%d bytes=%d", inputAudio.EventCount, inputAudio.TotalBytes)
	}
	if inputAudio.Histogram.SampleCount != 5 || inputAudio.Histogram.ByteSum != 222 {
		t.Fatalf("input/audio histogram totals: got samples=%d bytes=%d", inputAudio.Histogram.SampleCount, inputAudio.Histogram.ByteSum)
	}
	if !reflect.DeepEqual(inputAudio.Histogram.BucketCounts, []uint64{1, 1, 2}) || inputAudio.Histogram.OverflowCount != 1 {
		t.Fatalf("input/audio histogram buckets: got %v overflow=%d", inputAudio.Histogram.BucketCounts, inputAudio.Histogram.OverflowCount)
	}

	inputText := snapshot.Get(DirectionInput, ModalityText)
	if inputText.EventCount != 1 || inputText.TotalBytes != 7 {
		t.Fatalf("input/text counters: got count=%d bytes=%d", inputText.EventCount, inputText.TotalBytes)
	}
	if snapshot.SeriesFor(DirectionOutput, ModalityAudio).EventCount != 1 {
		t.Fatalf("output/audio series did not record independently")
	}
	if snapshot.SeriesFor(DirectionOutput, ModalityText).Histogram.OverflowCount != 1 {
		t.Fatalf("output/text overflow was not recorded independently")
	}
	if snapshot.SeriesFor(DirectionInput, ModalityImage).EventCount != 0 || snapshot.SeriesFor(DirectionOutput, ModalityImage).EventCount != 0 {
		t.Fatalf("unobserved image series changed")
	}

	prior := sink.Snapshot()
	if err := sink.Record(DirectionInput, ModalityAudio, 10); err != nil {
		t.Fatalf("record after snapshot: %v", err)
	}
	if got := prior.SeriesFor(DirectionInput, ModalityAudio).EventCount; got != 5 {
		t.Fatalf("prior snapshot changed after recording: got count=%d", got)
	}
	if got := sink.Snapshot().SeriesFor(DirectionInput, ModalityAudio).EventCount; got != 6 {
		t.Fatalf("new snapshot did not advance: got count=%d", got)
	}

	prior.HistogramBounds[0] = 999
	prior.Series[0].Histogram.BucketCounts[0] = 999
	current := sink.Snapshot()
	if current.HistogramBounds[0] != 0 || current.Series[0].Histogram.BucketCounts[0] == 999 {
		t.Fatalf("snapshot exposed mutable internal state: %+v", current)
	}
}

func TestInMemorySinkConforms(t *testing.T) {
	RunSinkConformanceSuite(t, func(bounds []int64) (Sink, error) {
		return NewInMemorySinkWithBounds(bounds)
	})
}
