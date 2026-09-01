package audio

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
)

// TestDuplexCapsuleAdversarialAcousticMatrix makes the mock-device recording
// format earn its keep across the inputs that routinely expose EAC bugs:
// quiet/far-field speech, loud speech near clipping, shaped room noise,
// music-like periodic background, long echo, and callback irregularity.
// Every row must replay both sides of the device, not merely its event log.
func TestDuplexCapsuleAdversarialAcousticMatrix(t *testing.T) {
	const callbacks = 12
	provider := adversarialStem(callbacks*480, 17000, 47)
	tests := []struct {
		name       string
		near       []int16
		background []int16
		gain       int32
		delay      int
		impulse    []int16
		faults     []FaultEvent
	}{
		{name: "quiet_far_field", near: adversarialStem(callbacks*480, 700, 61), gain: 9000, delay: 3840, impulse: []int16{32767, 9000, -3500}},
		{name: "ordinary_voice", near: adversarialStem(callbacks*480, 6000, 67), gain: 16000, delay: 960, impulse: []int16{32767, 6000}},
		{name: "loud_near_clipping", near: adversarialStem(callbacks*480, 30000, 71), gain: 30000, delay: 240},
		{name: "noisy_room", near: adversarialStem(callbacks*480, 4500, 73), background: adversarialStem(callbacks*480, 9000, 79), gain: 19000, delay: 1440},
		{name: "music_like_background", near: adversarialStem(callbacks*480, 3500, 83), background: adversarialPeriodic(callbacks*480, 11000), gain: 21000, delay: 1920, impulse: []int16{28000, 8000, -5000, 2500}},
		{name: "jitter_missing_and_duplicate_callbacks", near: adversarialStem(callbacks*480, 5000, 89), background: adversarialStem(callbacks*480, 2500, 97), gain: 20000, delay: 720, faults: []FaultEvent{
			{Callback: 3, Direction: DirectionInput, Type: FaultMissingCallback, ID: "capture-gap"},
			{Callback: 7, Direction: DirectionOutput, Type: FaultDuplicateCallback, ID: "render-duplicate"},
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			scenario := DuplexScenario{
				Seed:     99,
				Render:   ClockSpec{NominalRate: 16000, Quanta: []int{480, 320, 640}, JitterSamples: []int{0, 11, -7}},
				Capture:  ClockSpec{NominalRate: 16000, Quanta: []int{320, 480, 640}, DriftPPM: 80, JitterSamples: []int{5, -9, 0}},
				Acoustic: AcousticSpec{DelaySamples: test.delay, GainQ15: test.gain, ImpulseResponseQ15: test.impulse, NearEnd: test.near, Background: test.background},
				Faults:   test.faults,
			}
			registry, output := openSimulatedOutput(t, scenario)
			if err := output.WriteSamples(context.Background(), provider); err != nil {
				t.Fatal(err)
			}
			if err := registry.Advance(callbacks); err != nil {
				t.Fatal(err)
			}
			if len(registry.CapturedSamples()) == 0 {
				t.Fatal("mock device retained no capture output")
			}

			dir := filepath.Join(t.TempDir(), "capsule")
			if err := WriteDuplexFailureCapsule(dir, scenario, provider, registry); err != nil {
				t.Fatal(err)
			}
			replayed, err := ReplayDuplexFailureCapsule(dir)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(replayed.Trace(), registry.Trace()) {
				t.Fatal("adversarial device trace changed during replay")
			}
			if !reflect.DeepEqual(replayed.CapturedSamples(), registry.CapturedSamples()) {
				t.Fatal("adversarial capture output changed during replay")
			}
		})
	}
}

func adversarialStem(count, peak, stride int) []int16 {
	result := make([]int16, count)
	state := uint32(stride)
	for index := range result {
		state = state*1664525 + 1013904223
		value := int(state>>16)%((peak*2)+1) - peak
		result[index] = int16(value) //nolint:gosec // peak is bounded by the callers above
	}
	return result
}

func adversarialPeriodic(count, peak int) []int16 {
	pattern := []int{0, peak / 2, peak, peak / 2, 0, -peak / 2, -peak, -peak / 2}
	result := make([]int16, count)
	for index := range result {
		result[index] = int16(pattern[index%len(pattern)]) //nolint:gosec // peak is bounded
	}
	return result
}
