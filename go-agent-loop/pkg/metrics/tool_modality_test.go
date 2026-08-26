package metrics

import (
	"reflect"
	"testing"
)

// The tool modality joins the audio/text/image series model with identical
// validation, counter, and histogram behavior.
func TestToolModalityJoinsSeriesModel(t *testing.T) {
	if !ModalityTool.Valid() {
		t.Fatalf("modality %q must be valid", ModalityTool)
	}
	if got, want := string(Tool), "tool"; got != want {
		t.Fatalf("alias Tool = %q, want %q", got, want)
	}
	if got := SupportedModalities(); !reflect.DeepEqual(got, []Modality{ModalityAudio, ModalityText, ModalityImage, ModalityTool}) {
		t.Fatalf("SupportedModalities = %v, want audio, text, image, tool in deterministic order", got)
	}

	sink, err := NewInMemorySink([]int64{0, 4})
	if err != nil {
		t.Fatalf("construct sink: %v", err)
	}
	if err := sink.Record(DirectionOutput, ModalityTool, 16); err != nil {
		t.Fatalf("record output/tool: %v", err)
	}
	if err := sink.Record(DirectionOutput, ModalityTool, 1); err != nil {
		t.Fatalf("record second output/tool: %v", err)
	}
	if err := sink.Record(DirectionInput, Modality("sideways"), 1); err == nil {
		t.Fatalf("unknown modality must be rejected")
	}

	series := sink.Snapshot().SeriesFor(DirectionOutput, ModalityTool)
	if series.EventCount != 2 || series.TotalBytes != 17 {
		t.Fatalf("output/tool counters = (count=%d, bytes=%d), want (2, 17)", series.EventCount, series.TotalBytes)
	}
	if !reflect.DeepEqual(series.Histogram.BucketCounts, []uint64{0, 1}) || series.Histogram.OverflowCount != 1 {
		t.Fatalf("output/tool histogram = %+v, want the 1-byte sample in the bound-4 bucket and the 16-byte sample in overflow", series.Histogram)
	}

	keys := orderedSeriesKeys()
	if !containsSeries(keys, SeriesKey{Direction: DirectionOutput, Modality: ModalityTool}) ||
		!containsSeries(keys, SeriesKey{Direction: DirectionInput, Modality: ModalityTool}) {
		t.Fatalf("ordered series keys missing tool entries: %v", keys)
	}
}

func containsSeries(keys []SeriesKey, want SeriesKey) bool {
	for _, key := range keys {
		if key == want {
			return true
		}
	}
	return false
}
