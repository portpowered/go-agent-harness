package admission

import "testing"

func TestLimitsDefaultToProductionBounds(t *testing.T) {
	t.Parallel()
	production := Limits{ManifestBytes: 8 << 20, ArtifactBytes: 64 << 20, TimelineBytes: 32 << 20, TimelineEvents: 100_000}
	if got := DefaultLimits(); got != production {
		t.Fatalf("default limits = %+v, want %+v", got, production)
	}
	if got := New(Limits{}).limits; got != production {
		t.Fatalf("zero limits normalized to %+v, want %+v", got, production)
	}
	if got := New(Limits{ManifestBytes: -1, TimelineEvents: -1}).limits; got != production {
		t.Fatalf("negative limits normalized to %+v, want %+v", got, production)
	}
}

func TestLimitsMayOnlyLowerBounds(t *testing.T) {
	t.Parallel()
	smaller := Limits{ManifestBytes: 1, ArtifactBytes: 2, TimelineBytes: 3, TimelineEvents: 4}
	if got := New(smaller).limits; got != smaller {
		t.Fatalf("smaller limits = %+v, want %+v", got, smaller)
	}
	larger := Limits{ManifestBytes: MaxManifestBytes + 1, ArtifactBytes: MaxArtifactBytes + 1, TimelineBytes: MaxTimelineBytes + 1, TimelineEvents: MaxTimelineEvents + 1}
	if got := New(larger).limits; got != DefaultLimits() {
		t.Fatalf("larger limits = %+v, want capped defaults", got)
	}
}
