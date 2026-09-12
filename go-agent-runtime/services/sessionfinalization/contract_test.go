package sessionfinalization

import (
	"errors"
	"testing"
)

func TestContractDefaults(t *testing.T) {
	got := DrainPolicy{QuietPeriod: DefaultStragglerDrainQuietPeriod, WallSafety: DefaultStragglerDrainWallSafety}
	if got.QuietPeriod != DefaultStragglerDrainQuietPeriod || got.WallSafety != DefaultStragglerDrainWallSafety {
		t.Fatalf("default policy = %#v", got)
	}
	first, second := (ErrorTreeOptions{}).WithDefaults().CancellationErrors, (ErrorTreeOptions{}).WithDefaults().CancellationErrors
	if len(first) == 0 || len(first) != len(second) {
		t.Fatalf("default cancellation errors = %v/%v", first, second)
	}
	first[0] = errors.New("mutated")
	if errors.Is(second[0], first[0]) {
		t.Fatal("default cancellation errors share slice storage")
	}
}
