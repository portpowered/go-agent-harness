package room_test

import (
	"errors"
	"math"
	"testing"
	"time"

	roomanalysis "github.com/portpowered/go-agent-harness/go-audio/pkg/analysis/room"
)

const unknownStreamID = "missing"

func TestPCM16RoomAnalysisConfigRejectsInvalidBounds(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*roomanalysis.PCM16RoomAnalysisConfig)
		field  string
	}{
		{
			name: "inverted correlation lag window",
			mutate: func(config *roomanalysis.PCM16RoomAnalysisConfig) {
				config.CorrelationLagWindow = roomanalysis.PCM16LagWindow{Min: 50 * time.Millisecond, Max: -50 * time.Millisecond}
			},
			field: "correlation_lag_window",
		},
		{
			name: "positive correlation silence floor",
			mutate: func(config *roomanalysis.PCM16RoomAnalysisConfig) {
				config.CorrelationLagWindow = roomanalysis.DefaultPCM16RoomAnalysisConfig().CorrelationLagWindow
				config.CorrelationSilenceFloorDBFS = 5
			},
			field: "correlation_silence_floor_dbfs",
		},
		{
			name: "min peer correlation above one",
			mutate: func(config *roomanalysis.PCM16RoomAnalysisConfig) {
				config.CorrelationSilenceFloorDBFS = roomanalysis.DefaultPCM16RoomAnalysisConfig().CorrelationSilenceFloorDBFS
				config.MinPeerCorrelation = 1.5
			},
			field: "min_peer_correlation",
		},
		{
			name: "max self correlation negative",
			mutate: func(config *roomanalysis.PCM16RoomAnalysisConfig) {
				config.MinPeerCorrelation = roomanalysis.DefaultPCM16RoomAnalysisConfig().MinPeerCorrelation
				config.MaxSelfCorrelation = -0.1
			},
			field: "max_self_correlation",
		},
		{
			name: "positive barge-in speech threshold",
			mutate: func(config *roomanalysis.PCM16RoomAnalysisConfig) {
				config.MaxSelfCorrelation = roomanalysis.DefaultPCM16RoomAnalysisConfig().MaxSelfCorrelation
				config.BargeInSpeechThresholdDBFS = 5
			},
			field: "barge_in_speech_threshold_dbfs",
		},
		{
			name: "non-positive max barge-in latency",
			mutate: func(config *roomanalysis.PCM16RoomAnalysisConfig) {
				config.BargeInSpeechThresholdDBFS = roomanalysis.DefaultPCM16RoomAnalysisConfig().BargeInSpeechThresholdDBFS
				config.MaxBargeInLatency = -time.Millisecond
			},
			field: "max_barge_in_latency",
		},
		{
			name: "negative max loudness difference",
			mutate: func(config *roomanalysis.PCM16RoomAnalysisConfig) {
				config.MaxBargeInLatency = roomanalysis.DefaultPCM16RoomAnalysisConfig().MaxBargeInLatency
				config.MaxLoudnessDifferenceDB = -1
			},
			field: "max_loudness_difference_db",
		},
		{
			name: "non-positive max drift absolute",
			mutate: func(config *roomanalysis.PCM16RoomAnalysisConfig) {
				config.MaxLoudnessDifferenceDB = roomanalysis.DefaultPCM16RoomAnalysisConfig().MaxLoudnessDifferenceDB
				config.MaxDriftAbsolute = -time.Millisecond
			},
			field: "max_drift_absolute",
		},
		{
			name: "negative max drift fraction",
			mutate: func(config *roomanalysis.PCM16RoomAnalysisConfig) {
				config.MaxDriftAbsolute = roomanalysis.DefaultPCM16RoomAnalysisConfig().MaxDriftAbsolute
				config.MaxDriftFraction = -0.1
			},
			field: "max_drift_fraction",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := roomanalysis.DefaultRoomAnalysisConfig()
			test.mutate(&config)
			_, err := roomanalysis.AnalyzePCM16Room(roomAnalysisFixture(), config)
			if err == nil || !errors.Is(err, roomanalysis.ErrInvalidPCM16RoomAnalysisInput) {
				t.Fatalf("AnalyzePCM16Room() error = %v, want invalid-room-input", err)
			}
			var inputErr *roomanalysis.InvalidPCM16RoomAnalysisInputError
			if !errors.As(err, &inputErr) || inputErr.Field != test.field {
				t.Fatalf("typed input error = %+v, want field %q", inputErr, test.field)
			}
		})
	}
}

func TestPCM16RoomCorrelationAndValidateAreConciseAliases(t *testing.T) {
	room := roomAnalysisFixture()
	interval := roomanalysis.PCM16TimeInterval{ID: "standalone-overlap", Start: 600 * time.Millisecond, End: 2 * time.Second}
	lagWindow := roomanalysis.PCM16LagWindow{Min: -100 * time.Millisecond, Max: 100 * time.Millisecond}

	want, err := roomanalysis.NormalizedPCM16CrossCorrelation(room.Streams[0], room.Streams[3], interval, lagWindow, -50)
	if err != nil {
		t.Fatalf("NormalizedPCM16CrossCorrelation() error = %v", err)
	}
	got, err := roomanalysis.MeasurePCM16Correlation(room.Streams[0], room.Streams[3], interval, lagWindow, -50)
	if err != nil {
		t.Fatalf("MeasurePCM16Correlation() error = %v", err)
	}
	if got.BestCorrelation != want.BestCorrelation || got.BestLag != want.BestLag || got.ComparedSamples != want.ComparedSamples {
		t.Fatalf("MeasurePCM16Correlation() = %+v, want the same measurement as NormalizedPCM16CrossCorrelation() = %+v", got, want)
	}

	config := roomanalysis.DefaultRoomAnalysisConfig()
	if err := roomanalysis.ValidatePCM16Room(room, config); err != nil {
		t.Fatalf("ValidatePCM16Room() error = %v, want the fixture room to pass", err)
	}

	broken := roomAnalysisFixture()
	broken.Streams[3].Samples = make([]int16, len(broken.Streams[3].Samples))
	broken.Streams[3].ExpectedSpeech = nil
	err = roomanalysis.ValidatePCM16Room(broken, config)
	if err == nil || !errors.Is(err, roomanalysis.ErrPCM16AnalysisFailed) {
		t.Fatalf("ValidatePCM16Room() error = %v, want ErrPCM16AnalysisFailed", err)
	}
	var assertionErr *roomanalysis.PCM16RoomAssertionError
	if !errors.As(err, &assertionErr) {
		t.Fatalf("ValidatePCM16Room() error = %T, want *PCM16RoomAssertionError", err)
	}
	if failures := assertionErr.FailuresCopy(); len(failures) == 0 {
		t.Fatalf("FailuresCopy() = %v, want the room's typed failures", failures)
	}
	var nilAssertionErr *roomanalysis.PCM16RoomAssertionError
	if failures := nilAssertionErr.FailuresCopy(); failures != nil {
		t.Fatalf("FailuresCopy() on a nil *PCM16RoomAssertionError = %v, want nil", failures)
	}
}

func TestPCM16RoomExplicitLoudnessIntervalValidatesAndMeasures(t *testing.T) {
	valid := roomanalysis.PCM16LoudnessInterval{
		PCM16TimeInterval: roomanalysis.PCM16TimeInterval{Start: 600 * time.Millisecond, End: 2 * time.Second},
		LeftStreamID:      "a-sent",
		RightStreamID:     "b-sent",
	}
	room := roomAnalysisFixture()
	room.Loudness = []roomanalysis.PCM16LoudnessInterval{valid}
	result, err := roomanalysis.AnalyzePCM16Room(room, roomanalysis.DefaultRoomAnalysisConfig())
	if err != nil {
		t.Fatalf("AnalyzePCM16Room() error = %v", err)
	}
	found := false
	for _, measurement := range result.Loudness {
		if measurement.IntervalID == "loudness-0" {
			found = true
			if measurement.LeftStreamID != "a-sent" || measurement.RightStreamID != "b-sent" {
				t.Errorf("explicit loudness measurement streams = %s/%s, want a-sent/b-sent", measurement.LeftStreamID, measurement.RightStreamID)
			}
		}
	}
	if !found {
		t.Fatalf("loudness measurements = %+v, want an explicit loudness-0 measurement", result.Loudness)
	}

	tests := []struct {
		name   string
		mutate func(*roomanalysis.PCM16LoudnessInterval)
		field  string
	}{
		{
			name:   "missing left stream id",
			mutate: func(interval *roomanalysis.PCM16LoudnessInterval) { interval.LeftStreamID = "" },
			field:  "loudness[0]",
		},
		{
			name:   "missing right stream id",
			mutate: func(interval *roomanalysis.PCM16LoudnessInterval) { interval.RightStreamID = "" },
			field:  "loudness[0]",
		},
		{
			name: "identical left and right streams",
			mutate: func(interval *roomanalysis.PCM16LoudnessInterval) {
				interval.RightStreamID = interval.LeftStreamID
			},
			field: "loudness[0]",
		},
		{
			name:   "unknown left stream",
			mutate: func(interval *roomanalysis.PCM16LoudnessInterval) { interval.LeftStreamID = unknownStreamID },
			field:  "loudness[0].left_stream_id",
		},
		{
			name:   "unknown right stream",
			mutate: func(interval *roomanalysis.PCM16LoudnessInterval) { interval.RightStreamID = unknownStreamID },
			field:  "loudness[0].right_stream_id",
		},
		{
			name:   "interval outside timeline",
			mutate: func(interval *roomanalysis.PCM16LoudnessInterval) { interval.End = 4 * time.Second },
			field:  "loudness[0].left_stream_id",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			interval := valid
			test.mutate(&interval)
			invalidRoom := roomAnalysisFixture()
			invalidRoom.Loudness = []roomanalysis.PCM16LoudnessInterval{interval}
			_, err := roomanalysis.AnalyzePCM16Room(invalidRoom, roomanalysis.DefaultRoomAnalysisConfig())
			if err == nil || !errors.Is(err, roomanalysis.ErrInvalidPCM16RoomAnalysisInput) {
				t.Fatalf("AnalyzePCM16Room() error = %v, want invalid-room-input", err)
			}
			var inputErr *roomanalysis.InvalidPCM16RoomAnalysisInputError
			if !errors.As(err, &inputErr) || inputErr.Field != test.field {
				t.Fatalf("typed input error = %+v, want field %q", inputErr, test.field)
			}
		})
	}
}

func roomAnalysisFixture() roomanalysis.PCM16RoomInput {
	const sampleRate = 1000
	const streamSamples = 3000
	const routeLag = 40

	aSent := patternedSamples(streamSamples, 300, 2500, 11)
	bSent := patternedSamples(streamSamples, 300, 2500, 29)
	aReceived := delayedCopy(bSent, routeLag)
	bReceived := delayedCopy(aSent, routeLag)
	interrupt := patternedSamples(2000, 1000, 1200, 47)
	response := patternedSamples(2000, 500, 1300, 71)

	return roomanalysis.PCM16RoomInput{
		Streams: []roomanalysis.PCM16TimedStream{
			pcm16TimedStream("a-sent", "alice", aSent, sampleRate, 300, 2500),
			pcm16TimedStream("b-sent", "bob", bSent, sampleRate, 300, 2500),
			pcm16TimedStream("a-received", "alice", aReceived, sampleRate, 340, 2540),
			pcm16TimedStream("b-received", "bob", bReceived, sampleRate, 340, 2540),
			pcm16TimedStream("interrupt", "alice", interrupt, sampleRate, 1000, 1200),
			pcm16TimedStream("response", "bob", response, sampleRate, 500, 1300),
		},
		Overlaps: []roomanalysis.PCM16OverlapInterval{
			{
				PCM16TimeInterval: roomanalysis.PCM16TimeInterval{ID: "overlap-1", Start: 600 * time.Millisecond, End: 2 * time.Second},
				A:                 roomanalysis.PCM16OverlapParticipant{ParticipantID: "alice", SentStreamID: "a-sent", ReceivedStreamID: "a-received"},
				B:                 roomanalysis.PCM16OverlapParticipant{ParticipantID: "bob", SentStreamID: "b-sent", ReceivedStreamID: "b-received"},
			},
		},
		BargeIns: []roomanalysis.PCM16BargeInAnnotation{
			{
				PCM16TimeInterval:   roomanalysis.PCM16TimeInterval{ID: "barge-1", Start: 0, End: 1800 * time.Millisecond},
				InterrupterStreamID: "interrupt",
				InterruptedStreamID: "response",
			},
		},
	}
}

func pcm16TimedStream(streamID, participantID string, samples []int16, sampleRate, speechStart, speechEnd int) roomanalysis.PCM16TimedStream {
	return roomanalysis.PCM16TimedStream{
		PCM16Input: roomanalysis.PCM16Input{
			StreamID:       streamID,
			ParticipantID:  participantID,
			SampleRate:     sampleRate,
			Samples:        samples,
			ExpectedSpeech: []roomanalysis.SpeechAnnotation{{StartSample: speechStart, EndSample: speechEnd}},
		},
		TimelineStart: 0,
		TimelineEnd:   time.Duration(len(samples)) * time.Second / time.Duration(sampleRate),
	}
}

func patternedSamples(sampleCount, start, end, seed int) []int16 {
	samples := make([]int16, sampleCount)
	state := uint32(seed)
	for index := start; index < end; index++ {
		state = state*1664525 + 1013904223
		value := int32((state>>8)%16001) - 8000
		if value == 0 {
			value = 1
		}
		samples[index] = int16(value)
	}
	return samples
}

func delayedCopy(source []int16, delay int) []int16 {
	delayed := make([]int16, len(source))
	for index, sample := range source {
		destination := index + delay
		if destination >= 0 && destination < len(delayed) {
			delayed[destination] = sample
		}
	}
	return delayed
}

func scaleSamples(samples []int16, factor int16) []int16 {
	scaled := make([]int16, len(samples))
	for index, sample := range samples {
		value := int32(sample) * int32(factor)
		if value > math.MaxInt16 {
			value = math.MaxInt16
		}
		if value < math.MinInt16 {
			value = math.MinInt16
		}
		scaled[index] = int16(value)
	}
	return scaled
}

// TestPCM16RoomLoudnessBarExpressesTightThreeDBBound proves the inter-speaker
// loudness comparison can enforce a tightened ~3 dB bar, not just the 6 dB
// suite default. A live probe measured --voice verse rendering ~7-8 dB
// quieter than --voice alloy across two runs; a 3 dB bar must catch that gap
// while still passing an ordinary ~2 dB natural difference.
func TestPCM16RoomLoudnessBarExpressesTightThreeDBBound(t *testing.T) {
	const tightBoundDB = 3.0

	config := roomanalysis.DefaultRoomAnalysisConfig()
	config.MaxLoudnessDifferenceDB = tightBoundDB

	// ~7.5 dB quieter reproduces the shape of the reported verse-vs-alloy
	// gap (10^(-7.5/20) =~ 0.4217).
	quiet := roomAnalysisFixture()
	quiet.Streams[1].Samples = scaleSamplesByFactor(quiet.Streams[1].Samples, 0.4217)
	result, err := roomanalysis.AnalyzePCM16Room(quiet, config)
	if err != nil {
		t.Fatalf("AnalyzePCM16Room() error = %v", err)
	}
	var failure roomanalysis.PropertyFailure
	var ok bool
	for _, candidate := range result.Failures {
		if candidate.Property == "inter-speaker-loudness" && candidate.StreamID == "b-sent" {
			failure, ok = candidate, true
			break
		}
	}
	if !ok {
		t.Fatalf("failures = %v, want inter-speaker-loudness for the ~7.5dB-quiet stream at a %vdB bar", result.Failures, tightBoundDB)
	}
	if failure.Bound != tightBoundDB || failure.Measured < 6 || failure.Measured > 9 {
		t.Fatalf("loudness failure = %+v, want bound=%vdB and measured in [6,9]dB", failure, tightBoundDB)
	}

	// An ordinary ~2 dB difference (10^(-2/20) =~ 0.7943) must still pass the
	// same 3 dB bar: the tightened bound must not be so tight it flags normal
	// speaker-to-speaker variation.
	mild := roomAnalysisFixture()
	mild.Streams[1].Samples = scaleSamplesByFactor(mild.Streams[1].Samples, 0.7943)
	if err := roomanalysis.ValidatePCM16Room(mild, config); err != nil {
		t.Fatalf("ValidatePCM16Room() with a ~2dB natural difference at a %vdB bar error = %v, want pass", tightBoundDB, err)
	}
}

func scaleSamplesByFactor(samples []int16, factor float64) []int16 {
	scaled := make([]int16, len(samples))
	for index, sample := range samples {
		value := math.Round(float64(sample) * factor)
		if value > math.MaxInt16 {
			value = math.MaxInt16
		}
		if value < math.MinInt16 {
			value = math.MinInt16
		}
		scaled[index] = int16(value)
	}
	return scaled
}
