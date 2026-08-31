package audio_test

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
)

func TestAnalyzePCM16MeasuresFramesWithoutPaddingTheTail(t *testing.T) {
	samples := make([]int16, 45)
	for index := range samples {
		samples[index] = 1000
	}
	for index := 20; index < 40; index++ {
		samples[index] = 2000
	}
	samples[25] = 32700

	result, err := audio.AnalyzePCM16(audio.PCM16Input{
		StreamID:   "levels",
		SampleRate: 1000,
		Samples:    samples,
	}, audio.DefaultAnalysisConfig())
	if err != nil {
		t.Fatalf("AnalyzePCM16() error = %v", err)
	}
	if got, want := len(result.Frames), 3; got != want {
		t.Fatalf("frame count = %d, want %d", got, want)
	}
	if got, want := result.Frames[0].SampleCount, 20; got != want {
		t.Fatalf("first frame sample count = %d, want %d", got, want)
	}
	if result.Frames[0].Partial {
		t.Fatal("full first frame marked partial")
	}
	last := result.Frames[2]
	if !last.Partial || last.SampleCount != 5 || last.StartSample != 40 || last.EndSample != 45 {
		t.Fatalf("partial tail = %+v, want five unpadded samples at 40..45", last)
	}
	if !almostEqual(last.RMS, 1000, 1e-9) {
		t.Fatalf("partial tail RMS = %.6f, want 1000 without zero padding", last.RMS)
	}
	if got, want := len(result.ClippedSamples), 1; got != want {
		t.Fatalf("clipped sample count = %d, want %d", got, want)
	}
	clip := result.ClippedSamples[0]
	if clip.SampleIndex != 25 || clip.FrameIndex != 1 || clip.Timestamp != 25*time.Millisecond || clip.Value != 32700 {
		t.Fatalf("clipped sample location = %+v, want sample 25/frame 1 at 25ms", clip)
	}
	wantRMSDBFS := 20 * math.Log10(1000.0/32768.0)
	if !almostEqual(last.RMSDBFS, wantRMSDBFS, 1e-9) {
		t.Fatalf("partial tail RMS dBFS = %.9f, want %.9f", last.RMSDBFS, wantRMSDBFS)
	}
	if result.AbsolutePeak != 32700 || result.ClipCount != 1 {
		t.Fatalf("stream peak/clips = %d/%d, want 32700/1", result.AbsolutePeak, result.ClipCount)
	}
}

func TestAnalyzePCM16SyntheticDefects(t *testing.T) {
	tests := []struct {
		name             string
		mutate           func(*audio.PCM16Input)
		wantProperties   []string
		wantNoProperties []string
	}{
		{
			name: "clean",
			wantNoProperties: []string{
				"clipping", "quiet-boundary-click", "dropout", "leading-click", "trailing-click", "probable-truncation-pop",
			},
		},
		{
			name: "clipping",
			mutate: func(input *audio.PCM16Input) {
				input.Samples[800] = -32700
			},
			wantProperties: []string{"clipping"},
		},
		{
			name: "quiet boundary click",
			mutate: func(input *audio.PCM16Input) {
				input.ChunkBoundaries = []audio.ChunkBoundary{{ID: "quiet-chunk", SampleIndex: 100}}
				input.Samples[100] = 7000
			},
			wantProperties: []string{"quiet-boundary-click"},
		},
		{
			name: "natural pause",
			mutate: func(input *audio.PCM16Input) {
				for index := 600; index < 800; index++ {
					input.Samples[index] = 0
				}
			},
			wantNoProperties: []string{"dropout"},
		},
		{
			name: "in-speech dropout",
			mutate: func(input *audio.PCM16Input) {
				for index := 600; index < 1400; index++ {
					input.Samples[index] = 0
				}
			},
			wantProperties: []string{"dropout"},
		},
		{
			name: "bad edges",
			mutate: func(input *audio.PCM16Input) {
				input.Samples[0] = 2000
				input.Samples[len(input.Samples)-1] = 2000
			},
			wantProperties: []string{"leading-click", "trailing-click", "probable-truncation-pop"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := cleanAnalysisInput()
			if test.mutate != nil {
				test.mutate(&input)
			}
			result, err := audio.AnalyzePCM16(input, audio.DefaultAnalysisConfig())
			if err != nil {
				t.Fatalf("AnalyzePCM16() error = %v", err)
			}
			properties := make(map[string]bool, len(result.Failures))
			for _, failure := range result.Failures {
				properties[failure.Property] = true
			}
			for _, property := range test.wantProperties {
				if !properties[property] {
					t.Errorf("missing property %q in failures: %v", property, result.Failures)
				}
			}
			for _, property := range test.wantNoProperties {
				if properties[property] {
					t.Errorf("unexpected property %q in failures: %v", property, result.Failures)
				}
			}
			if test.name == "clean" && !result.Passed() {
				t.Fatalf("clean signal failed: %v", result.Failures)
			}
			if test.name == "natural pause" {
				if len(result.Dropouts) != 0 {
					t.Fatalf("natural pause reported as dropout: %+v", result.Dropouts)
				}
				foundNatural := false
				for _, run := range result.SilentRuns {
					if run.ExpectedSpeechOverlap == 200*time.Millisecond && run.NaturalPause {
						foundNatural = true
						break
					}
				}
				if !foundNatural {
					t.Fatalf("silent runs = %+v, want a 200ms natural expected-speech pause", result.SilentRuns)
				}
			}
			if test.name == "in-speech dropout" {
				if len(result.Dropouts) != 1 || result.Dropouts[0].ExpectedSpeechOverlap != 800*time.Millisecond {
					t.Fatalf("dropouts = %+v, want one 800ms dropout", result.Dropouts)
				}
				failure := result.Failures[0]
				if failure.Measured != 800 || failure.Bound != 750 || failure.Unit != "milliseconds of expected-speech silence" {
					t.Fatalf("dropout failure = %+v, want measured 800, bound 750 milliseconds", failure)
				}
			}
		})
	}
}

func TestAnalyzePCM16QuietBoundaryAndLoudImpulseClassification(t *testing.T) {
	quiet := cleanAnalysisInput()
	quiet.ChunkBoundaries = []audio.ChunkBoundary{{ID: "quiet", SampleIndex: 100}}
	quiet.Samples[100] = 7000
	quietResult, err := audio.AnalyzePCM16(quiet, audio.DefaultAnalysisConfig())
	if err != nil {
		t.Fatalf("quiet AnalyzePCM16() error = %v", err)
	}
	if len(quietResult.BoundaryClicks) != 1 || len(quietResult.ImpulseCandidates) != 0 {
		t.Fatalf("quiet boundary checks/candidates = %d/%d, want one click and no candidate", len(quietResult.BoundaryClicks), len(quietResult.ImpulseCandidates))
	}
	click := quietResult.BoundaryClicks[0]
	if click.Delta != 7000 || click.SampleIndex != 100 || click.Timestamp != 100*time.Millisecond {
		t.Fatalf("quiet boundary evidence = %+v, want delta 7000 at sample 100/100ms", click)
	}
	clickFailure := quietResult.Failures[0]
	message := clickFailure.Error()
	for _, want := range []string{"quiet-boundary-click", `stream="clean-stream"`, `boundary-id="quiet"`, "sample=100", "frame=5", "measured=7000.000", "bound=6000.000"} {
		if !strings.Contains(message, want) {
			t.Errorf("quiet boundary diagnostic %q missing %q", message, want)
		}
	}

	loud := cleanAnalysisInput()
	loud.ChunkBoundaries = []audio.ChunkBoundary{{ID: "loud", SampleIndex: 100}}
	for index := 80; index < 120; index++ {
		loud.Samples[index] = 8000
	}
	loud.Samples[100] = 16000
	loudResult, err := audio.AnalyzePCM16(loud, audio.DefaultAnalysisConfig())
	if err != nil {
		t.Fatalf("loud AnalyzePCM16() error = %v", err)
	}
	if len(loudResult.BoundaryClicks) != 0 || len(loudResult.ImpulseCandidates) != 1 || !loudResult.Passed() {
		t.Fatalf("loud boundary clicks/candidates/pass = %d/%d/%t, want 0/1/true; failures=%v", len(loudResult.BoundaryClicks), len(loudResult.ImpulseCandidates), loudResult.Passed(), loudResult.Failures)
	}
	if loudResult.ImpulseCandidates[0].PreviousWindowRMSDBFS < -24 && loudResult.ImpulseCandidates[0].NextWindowRMSDBFS < -24 {
		t.Fatalf("loud impulse windows = %.2f/%.2f dBFS, want at least one loud window", loudResult.ImpulseCandidates[0].PreviousWindowRMSDBFS, loudResult.ImpulseCandidates[0].NextWindowRMSDBFS)
	}
}

func TestAssertPCM16ReturnsStructuredActionableFailure(t *testing.T) {
	input := cleanAnalysisInput()
	input.Samples[800] = 32700

	err := audio.AssertPCM16(input, audio.DefaultAnalysisConfig())
	if err == nil {
		t.Fatal("AssertPCM16() error = nil, want clipping failure")
	}
	if !errors.Is(err, audio.ErrPCM16AnalysisFailed) {
		t.Fatalf("AssertPCM16() error = %v, want ErrPCM16AnalysisFailed", err)
	}
	var assertionErr *audio.PCM16AssertionError
	if !errors.As(err, &assertionErr) || len(assertionErr.Failures) != 1 {
		t.Fatalf("AssertPCM16() error = %T/%v, want one typed failure", err, err)
	}
	failure := assertionErr.Failures[0]
	if failure.Property != "clipping" || failure.StreamID != "clean-stream" || failure.SampleIndex != 800 || failure.FrameIndex != 40 || failure.Measured != 32700 || failure.Bound != 32700 {
		t.Fatalf("clipping failure = %+v, want stream/sample/frame/measured/bound populated", failure)
	}
	for _, want := range []string{"clipping", `stream="clean-stream"`, "sample=800", "frame=40", "measured=32700.000", "bound=32700.000"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("assertion diagnostic %q missing %q", err, want)
		}
	}
}

func TestAnalyzeAndValidatePCM16AreConciseAliases(t *testing.T) {
	clean := cleanAnalysisInput()
	config := audio.DefaultAnalysisConfig()

	want, err := audio.AnalyzePCM16(clean, config)
	if err != nil {
		t.Fatalf("AnalyzePCM16() error = %v", err)
	}
	got, err := audio.Analyze(clean, config)
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	if got.StreamID != want.StreamID || len(got.Failures) != len(want.Failures) || !got.Passed() {
		t.Fatalf("Analyze() = %+v, want the same report as AnalyzePCM16() = %+v", got, want)
	}
	if failures := got.FailuresCopy(); len(failures) != 0 {
		t.Fatalf("FailuresCopy() on a passing analysis = %v, want empty", failures)
	}
	if err := audio.ValidatePCM16(clean, config); err != nil {
		t.Fatalf("ValidatePCM16() error = %v, want clean input to pass", err)
	}

	clipped := cleanAnalysisInput()
	clipped.Samples[800] = 32700
	if err := audio.ValidatePCM16(clipped, config); err == nil || !errors.Is(err, audio.ErrPCM16AnalysisFailed) {
		t.Fatalf("ValidatePCM16() error = %v, want ErrPCM16AnalysisFailed for a clipped stream", err)
	}
	clippedAnalysis, err := audio.Analyze(clipped, config)
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	if failures := clippedAnalysis.FailuresCopy(); len(failures) != 1 || failures[0].Property != "clipping" {
		t.Fatalf("FailuresCopy() on a clipped analysis = %v, want one clipping failure", failures)
	}
}

func TestPCM16AnalysisErrorTypesHandleNilAndEmptyState(t *testing.T) {
	var nilAssertionErr *audio.PCM16AssertionError
	if got, want := nilAssertionErr.Error(), "<nil>"; got != want {
		t.Errorf("nil *PCM16AssertionError.Error() = %q, want %q", got, want)
	}
	if failures := nilAssertionErr.FailuresCopy(); failures != nil {
		t.Errorf("nil *PCM16AssertionError.FailuresCopy() = %v, want nil", failures)
	}
	emptyAssertionErr := &audio.PCM16AssertionError{StreamID: "s"}
	if got, want := emptyAssertionErr.Error(), audio.ErrPCM16AnalysisFailed.Error(); got != want {
		t.Errorf("empty-failures *PCM16AssertionError.Error() = %q, want %q", got, want)
	}
	populatedAssertionErr := &audio.PCM16AssertionError{StreamID: "s", Failures: []audio.PropertyFailure{{Property: "clipping"}}}
	if failures := populatedAssertionErr.FailuresCopy(); len(failures) != 1 || failures[0].Property != "clipping" {
		t.Errorf("populated *PCM16AssertionError.FailuresCopy() = %v, want one clipping failure", failures)
	}

	var nilInputErr *audio.InvalidPCM16AnalysisInputError
	if got, want := nilInputErr.Error(), "<nil>"; got != want {
		t.Errorf("nil *InvalidPCM16AnalysisInputError.Error() = %q, want %q", got, want)
	}
	fieldlessInputErr := &audio.InvalidPCM16AnalysisInputError{Reason: "broken"}
	if got, want := fieldlessInputErr.Error(), audio.ErrInvalidPCM16AnalysisInput.Error()+": broken"; got != want {
		t.Errorf("fieldless *InvalidPCM16AnalysisInputError.Error() = %q, want %q", got, want)
	}
}

func TestPCM16AnalysisConfigRejectsInvalidBounds(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*audio.PCM16AnalysisConfig)
		field  string
	}{
		{
			name:   "non-positive frame duration",
			mutate: func(config *audio.PCM16AnalysisConfig) { config.FrameDuration = -time.Millisecond },
			field:  "frame_duration",
		},
		{
			name:   "positive silence floor",
			mutate: func(config *audio.PCM16AnalysisConfig) { config.SilenceFloorDBFS = 5 },
			field:  "silence_floor_dbfs",
		},
		{
			name:   "non-positive max natural pause",
			mutate: func(config *audio.PCM16AnalysisConfig) { config.MaxNaturalPause = -time.Millisecond },
			field:  "max_natural_pause",
		},
		{
			name:   "non-positive boundary delta",
			mutate: func(config *audio.PCM16AnalysisConfig) { config.BoundaryDelta = -1 },
			field:  "boundary_delta",
		},
		{
			name:   "non-negative boundary quiet dbfs",
			mutate: func(config *audio.PCM16AnalysisConfig) { config.BoundaryQuietDBFS = 0.5 },
			field:  "boundary_quiet_dbfs",
		},
		{
			name:   "clip threshold above int16 max",
			mutate: func(config *audio.PCM16AnalysisConfig) { config.ClipSampleThreshold = 40000 },
			field:  "clip_sample_threshold",
		},
		{
			name:   "negative edge threshold",
			mutate: func(config *audio.PCM16AnalysisConfig) { config.EdgeSampleThreshold = -1 },
			field:  "edge_sample_threshold",
		},
		{
			name:   "positive final frame max rms",
			mutate: func(config *audio.PCM16AnalysisConfig) { config.FinalFrameMaxRMSDBFS = 5 },
			field:  "final_frame_max_rms_dbfs",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := audio.DefaultAnalysisConfig()
			test.mutate(&config)
			_, err := audio.AnalyzePCM16(cleanAnalysisInput(), config)
			if err == nil || !errors.Is(err, audio.ErrInvalidPCM16AnalysisInput) {
				t.Fatalf("AnalyzePCM16() error = %v, want invalid-input error", err)
			}
			var inputErr *audio.InvalidPCM16AnalysisInputError
			if !errors.As(err, &inputErr) || inputErr.Field != test.field {
				t.Fatalf("AnalyzePCM16() typed error = %+v, want field %q", inputErr, test.field)
			}
		})
	}
}

func TestAnalyzePCM16RejectsInconsistentInputs(t *testing.T) {
	tests := []struct {
		name   string
		input  audio.PCM16Input
		config audio.PCM16AnalysisConfig
		field  string
	}{
		{
			name:  "missing sample rate",
			input: audio.PCM16Input{Samples: []int16{1}},
			field: "sample_rate",
		},
		{
			name:  "empty samples",
			input: audio.PCM16Input{SampleRate: 1000},
			field: "samples",
		},
		{
			name:  "fractional frame",
			input: audio.PCM16Input{SampleRate: 44100, Samples: []int16{1}},
			config: audio.PCM16AnalysisConfig{
				FrameDuration: time.Millisecond,
			},
			field: "frame_duration",
		},
		{
			name: "out of range speech annotation",
			input: audio.PCM16Input{
				SampleRate:     1000,
				Samples:        make([]int16, 100),
				ExpectedSpeech: []audio.SpeechAnnotation{{StartSample: 80, EndSample: 101}},
			},
			field: "expected_speech[0]",
		},
		{
			name: "duplicate boundary",
			input: audio.PCM16Input{
				SampleRate:      1000,
				Samples:         make([]int16, 100),
				ChunkBoundaries: []audio.ChunkBoundary{{SampleIndex: 20}, {SampleIndex: 20}},
			},
			field: "chunk_boundaries[1].sample_index",
		},
		{
			name: "boundary lacks neighboring windows",
			input: audio.PCM16Input{
				SampleRate:      1000,
				Samples:         make([]int16, 100),
				ChunkBoundaries: []audio.ChunkBoundary{{SampleIndex: 10}},
			},
			field: "chunk_boundaries[0].sample_index",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := audio.AnalyzePCM16(test.input, test.config)
			if err == nil || !errors.Is(err, audio.ErrInvalidPCM16AnalysisInput) {
				t.Fatalf("AnalyzePCM16() error = %v, want invalid-input error", err)
			}
			var inputErr *audio.InvalidPCM16AnalysisInputError
			if !errors.As(err, &inputErr) || inputErr.Field != test.field {
				t.Fatalf("AnalyzePCM16() typed error = %+v, want field %q", inputErr, test.field)
			}
			if !strings.Contains(err.Error(), test.field) {
				t.Fatalf("AnalyzePCM16() error = %q, want field name", err)
			}
		})
	}
}

func cleanAnalysisInput() audio.PCM16Input {
	samples := make([]int16, 2000)
	for index := 200; index < 1600; index++ {
		samples[index] = 6000
	}
	return audio.PCM16Input{
		StreamID:       "clean-stream",
		ParticipantID:  "alice",
		SampleRate:     1000,
		Samples:        samples,
		ExpectedSpeech: []audio.SpeechAnnotation{{Label: "turn-1", StartSample: 200, EndSample: 1600}},
	}
}

func almostEqual(left, right, tolerance float64) bool {
	return math.Abs(left-right) <= tolerance
}
