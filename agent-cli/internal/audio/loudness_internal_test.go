package audio

import "testing"

// TestClampInt16Bounds exercises clampInt16's saturation branches directly.
// applyGain's peak-safety ceiling keeps normal normalizer output well inside
// int16 range, so these extremes are a defensive belt-and-suspenders bound
// that is otherwise unreachable through the public API; testing the
// unexported helper directly is the only way to cover them.
func TestClampInt16Bounds(t *testing.T) {
	cases := []struct {
		name  string
		value float64
		want  int16
	}{
		{"above positive full scale", 40000, 32767},
		{"exactly positive full scale", 32767, 32767},
		{"below negative full scale", -40000, -32768},
		{"exactly negative full scale", -32768, -32768},
		{"mid-range rounds", 100.4, 100},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := clampInt16(tc.value); got != tc.want {
				t.Fatalf("clampInt16(%v) = %d, want %d", tc.value, got, tc.want)
			}
		})
	}
}
