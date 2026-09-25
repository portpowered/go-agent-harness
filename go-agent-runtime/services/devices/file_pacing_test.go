package devices

import (
	"errors"
	"math"
	"testing"
)

func TestParseFilePacingAcceptsHostSpellings(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		value    string
		want     FilePacing
		realtime bool
		text     string
	}{
		{value: "", realtime: true, text: "realtime"},
		{value: " RealTime ", realtime: true, text: "realtime"},
		{value: "1x", want: FilePacing{Speed: 1}, realtime: true, text: "realtime"},
		{value: "unpaced", want: FilePacing{Unpaced: true}, text: "unpaced"},
		{value: "10x", want: FilePacing{Speed: 10}, text: "10x"},
		{value: "2.5X", want: FilePacing{Speed: 2.5}, text: "2.5x"},
	} {
		got, err := ParseFilePacing(test.value)
		if err != nil {
			t.Fatalf("ParseFilePacing(%q): %v", test.value, err)
		}
		if got != test.want || got.Realtime() != test.realtime || got.String() != test.text {
			t.Fatalf("ParseFilePacing(%q) = %+v (realtime %v, %q), want %+v (realtime %v, %q)", test.value, got, got.Realtime(), got.String(), test.want, test.realtime, test.text)
		}
	}
}

func TestParseFilePacingRejectsUnusableSpellings(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"fast", "10", "0x", "-2x", "x", "infx", "nanx"} {
		if got, err := ParseFilePacing(value); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("ParseFilePacing(%q) = (%+v, %v), want ErrInvalidRequest", value, got, err)
		}
	}
}

func TestFilePacingValidateRejectsNonFiniteOrNegativeSpeed(t *testing.T) {
	t.Parallel()
	for _, speed := range []float64{-0.5, math.NaN(), math.Inf(1), math.Inf(-1)} {
		if err := (FilePacing{Speed: speed}).Validate(); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("Validate(speed %v) = %v, want ErrInvalidRequest", speed, err)
		}
	}
	if err := (FilePacing{}).Validate(); err != nil {
		t.Fatalf("zero pacing Validate = %v, want nil", err)
	}
}
