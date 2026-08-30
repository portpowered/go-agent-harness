package audio_test

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
)

func TestAnalyzePCM16RoomMeasuresPeerDeliverySelfHearingBargeBalanceAndDrift(t *testing.T) {
	room := roomAnalysisFixture()
	result, err := audio.AnalyzePCM16Room(room, audio.DefaultRoomAnalysisConfig())
	if err != nil {
		t.Fatalf("AnalyzePCM16Room() error = %v", err)
	}
	if !result.Passed() {
		t.Fatalf("positive room failed: %v", result.Failures)
	}
	if got, want := len(result.Streams), 6; got != want {
		t.Fatalf("stream analyses = %d, want %d", got, want)
	}
	if got, want := len(result.PeerDeliveries), 2; got != want {
		t.Fatalf("peer deliveries = %d, want %d", got, want)
	}
	if got, want := len(result.SelfHearings), 2; got != want {
		t.Fatalf("self-hearing measurements = %d, want %d", got, want)
	}
	if got, want := len(result.BargeIns), 1; got != want {
		t.Fatalf("barge-in measurements = %d, want %d", got, want)
	}
	if got, want := len(result.Loudness), 1; got != want {
		t.Fatalf("loudness measurements = %d, want %d", got, want)
	}
	if got, want := len(result.Drift), 6; got != want {
		t.Fatalf("drift measurements = %d, want %d", got, want)
	}

	for _, delivery := range result.PeerDeliveries {
		if !delivery.Passed || delivery.ComparedSamples == 0 || delivery.BestCorrelation < 0.99 {
			t.Errorf("delivery %+v did not prove known-lag routing", delivery)
		}
		if delivery.BestLag != 40*time.Millisecond {
			t.Errorf("delivery %s best lag = %s, want 40ms", delivery.Direction, delivery.BestLag)
		}
	}
	for _, self := range result.SelfHearings {
		if !self.Passed || self.BestAbsoluteCorrelation >= 0.30 {
			t.Errorf("self-hearing %+v should be below the default bound", self)
		}
	}
	barge := result.BargeIns[0]
	if !barge.Passed || !barge.InterrupterOnsetFound || !barge.InterruptedStopFound || barge.Latency != 300*time.Millisecond {
		t.Errorf("barge-in = %+v, want a 300ms measured stop latency", barge)
	}
	loudness := result.Loudness[0]
	if !loudness.Passed || loudness.DifferenceDB > 6 || loudness.LeftSamples == 0 || loudness.RightSamples == 0 {
		t.Errorf("loudness = %+v, want balanced active speech", loudness)
	}
	for _, drift := range result.Drift {
		if !drift.Passed || drift.Drift != 0 || drift.Bound != 20*time.Millisecond {
			t.Errorf("drift = %+v, want zero drift with a 20ms bound", drift)
		}
	}
}

func TestAnalyzePCM16RoomSyntheticSingleDefects(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(*audio.PCM16RoomInput)
		properties []string
	}{
		{
			name: "self-hearing",
			mutate: func(room *audio.PCM16RoomInput) {
				room.Streams[2].Samples = delayedCopy(room.Streams[0].Samples, 40)
			},
			properties: []string{"self-hearing"},
		},
		{
			name: "missing forward delivery",
			mutate: func(room *audio.PCM16RoomInput) {
				room.Streams[3].Samples = make([]int16, len(room.Streams[3].Samples))
				room.Streams[3].ExpectedSpeech = nil
			},
			properties: []string{"overlap-delivery"},
		},
		{
			name: "over-shifted forward delivery",
			mutate: func(room *audio.PCM16RoomInput) {
				room.Streams[3].Samples = delayedCopy(room.Streams[0].Samples, 300)
				room.Streams[3].ExpectedSpeech = []audio.SpeechAnnotation{{StartSample: 600, EndSample: 2800}}
			},
			properties: []string{"overlap-delivery"},
		},
		{
			name: "swapped received stream",
			mutate: func(room *audio.PCM16RoomInput) {
				room.Streams[3].Samples = delayedCopy(room.Streams[1].Samples, 40)
			},
			properties: []string{"overlap-delivery", "self-hearing"},
		},
		{
			name: "barge-in stop too slow",
			mutate: func(room *audio.PCM16RoomInput) {
				room.Streams[5].Samples = patternedSamples(2000, 500, 1800, 23)
				room.Streams[5].ExpectedSpeech = []audio.SpeechAnnotation{{StartSample: 500, EndSample: 1800}}
				room.BargeIns[0].End = 1900 * time.Millisecond
			},
			properties: []string{"barge-in-latency"},
		},
		{
			name: "missing barge-in stop",
			mutate: func(room *audio.PCM16RoomInput) {
				room.Streams[5].Samples = patternedSamples(2000, 500, 1800, 23)
				room.Streams[5].ExpectedSpeech = []audio.SpeechAnnotation{{StartSample: 500, EndSample: 1800}}
			},
			properties: []string{"barge-in-stop"},
		},
		{
			name: "missing barge-in onset",
			mutate: func(room *audio.PCM16RoomInput) {
				room.Streams[4].Samples = make([]int16, len(room.Streams[4].Samples))
				room.Streams[4].ExpectedSpeech = nil
			},
			properties: []string{"barge-in-onset"},
		},
		{
			name: "loudness imbalance",
			mutate: func(room *audio.PCM16RoomInput) {
				room.Streams[1].Samples = scaleSamples(room.Streams[1].Samples, 3)
				room.Streams[3].Samples = scaleSamples(room.Streams[3].Samples, 3)
			},
			properties: []string{"inter-speaker-loudness"},
		},
		{
			name: "timeline drift",
			mutate: func(room *audio.PCM16RoomInput) {
				room.Streams[0].TimelineEnd += 100 * time.Millisecond
			},
			properties: []string{"timing-drift"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			room := roomAnalysisFixture()
			test.mutate(&room)
			result, err := audio.AnalyzePCM16Room(room, audio.DefaultRoomAnalysisConfig())
			if err != nil {
				t.Fatalf("AnalyzePCM16Room() error = %v", err)
			}
			properties := make(map[string]bool, len(result.Failures))
			for _, failure := range result.Failures {
				properties[failure.Property] = true
				message := failure.Error()
				for _, want := range []string{"interval=", "timestamp=", "measured=", "bound="} {
					if !strings.Contains(message, want) {
						t.Errorf("failure %q missing %q: %s", failure.Property, want, message)
					}
				}
			}
			for _, property := range test.properties {
				if !properties[property] {
					t.Errorf("missing property %q in failures: %v", property, result.Failures)
				}
			}
			if result.Passed() {
				t.Fatalf("defect %q unexpectedly passed", test.name)
			}
		})
	}
}

func TestPCM16RoomCorrelationHonorsExplicitLagWindow(t *testing.T) {
	room := roomAnalysisFixture()
	config := audio.DefaultRoomAnalysisConfig()
	config.CorrelationLagWindow = audio.PCM16LagWindow{Min: -20 * time.Millisecond, Max: 20 * time.Millisecond}
	result, err := audio.AnalyzePCM16Room(room, config)
	if err != nil {
		t.Fatalf("AnalyzePCM16Room() error = %v", err)
	}
	if result.Passed() {
		t.Fatal("known 40ms route unexpectedly passed a +/-20ms lag window")
	}
	foundForward := false
	for _, failure := range result.Failures {
		if failure.Property == "overlap-delivery" {
			foundForward = true
			if failure.Measured >= failure.Bound || !strings.Contains(failure.Detail, "best-lag=") {
				t.Errorf("lag-window failure = %+v, want measured coefficient below bound and best lag", failure)
			}
		}
	}
	if !foundForward {
		t.Fatalf("failures = %v, want overlap-delivery failures", result.Failures)
	}
}

func TestPCM16StandaloneMeasurementsUseExplicitTimedEvidence(t *testing.T) {
	room := roomAnalysisFixture()
	interval := audio.PCM16TimeInterval{ID: "standalone-overlap", Start: 600 * time.Millisecond, End: 2 * time.Second}
	correlation, err := audio.NormalizedPCM16CrossCorrelation(
		room.Streams[0], room.Streams[3], interval,
		audio.PCM16LagWindow{Min: -100 * time.Millisecond, Max: 100 * time.Millisecond},
		-50,
	)
	if err != nil {
		t.Fatalf("NormalizedPCM16CrossCorrelation() error = %v", err)
	}
	if correlation.BestCorrelation < 0.99 || correlation.BestLag != 40*time.Millisecond || correlation.ComparedSamples == 0 {
		t.Fatalf("correlation = %+v, want known 40ms positive evidence", correlation)
	}

	drift, err := audio.MeasurePCM16Drift(room.Streams[0])
	if err != nil {
		t.Fatalf("MeasurePCM16Drift() error = %v", err)
	}
	if drift.Drift != 0 || drift.SampleDuration != 3*time.Second || drift.TimestampSpan != 3*time.Second {
		t.Fatalf("drift = %+v, want exact three-second timing", drift)
	}

	loudness, err := audio.MeasurePCM16Loudness(room.Streams[0], room.Streams[1], interval)
	if err != nil {
		t.Fatalf("MeasurePCM16Loudness() error = %v", err)
	}
	if loudness.LeftSamples == 0 || loudness.RightSamples == 0 || loudness.DifferenceDB > 6 {
		t.Fatalf("loudness = %+v, want balanced annotated active speech", loudness)
	}

	barge, err := audio.MeasurePCM16BargeIn(room.Streams[4], room.Streams[5], room.BargeIns[0], -40)
	if err != nil {
		t.Fatalf("MeasurePCM16BargeIn() error = %v", err)
	}
	if !barge.Passed || barge.Latency != 300*time.Millisecond {
		t.Fatalf("barge-in = %+v, want a 300ms standalone measurement", barge)
	}
}

func TestPCM16RoomRejectsInconsistentIdentityFormatAndTiming(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*audio.PCM16RoomInput)
		field  string
	}{
		{
			name: "missing stable stream identity",
			mutate: func(room *audio.PCM16RoomInput) {
				room.Streams[0].StreamID = ""
			},
			field: "streams[0].stream_id",
		},
		{
			name: "unknown overlap stream",
			mutate: func(room *audio.PCM16RoomInput) {
				room.Overlaps[0].B.ReceivedStreamID = "missing"
			},
			field: "overlaps[0].b.received_stream_id",
		},
		{
			name: "mismatched sample rate",
			mutate: func(room *audio.PCM16RoomInput) {
				room.Streams[3].SampleRate = 2000
			},
			field: "overlaps[0].sample_rate",
		},
		{
			name: "interval outside timeline",
			mutate: func(room *audio.PCM16RoomInput) {
				room.Overlaps[0].End = 4 * time.Second
			},
			field: "overlaps[0].a.sent_stream_id",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			room := roomAnalysisFixture()
			test.mutate(&room)
			_, err := audio.AnalyzePCM16Room(room, audio.DefaultRoomAnalysisConfig())
			if err == nil || !errors.Is(err, audio.ErrInvalidPCM16RoomAnalysisInput) {
				t.Fatalf("AnalyzePCM16Room() error = %v, want invalid-room-input", err)
			}
			var inputErr *audio.InvalidPCM16RoomAnalysisInputError
			if !errors.As(err, &inputErr) || inputErr.Field != test.field {
				t.Fatalf("typed input error = %+v, want field %q", inputErr, test.field)
			}
			if !strings.Contains(err.Error(), test.field) {
				t.Fatalf("input error = %q, want field name", err)
			}
		})
	}
}

func TestAssertPCM16RoomReturnsActionableTypedFailure(t *testing.T) {
	room := roomAnalysisFixture()
	room.Streams[3].Samples = make([]int16, len(room.Streams[3].Samples))
	room.Streams[3].ExpectedSpeech = nil
	err := audio.AssertPCM16Room(room, audio.DefaultRoomAnalysisConfig())
	if err == nil || !errors.Is(err, audio.ErrPCM16AnalysisFailed) {
		t.Fatalf("AssertPCM16Room() error = %v, want analysis failure", err)
	}
	var assertionErr *audio.PCM16RoomAssertionError
	if !errors.As(err, &assertionErr) || len(assertionErr.Failures) == 0 {
		t.Fatalf("AssertPCM16Room() error = %T/%v, want typed failures", err, err)
	}
	message := err.Error()
	for _, want := range []string{"overlap-delivery", `source="a-sent"`, `received="b-received"`, `direction="alice->bob"`, `interval="overlap-1"`, "measured=", "bound="} {
		if !strings.Contains(message, want) {
			t.Errorf("room assertion diagnostic missing %q: %s", want, message)
		}
	}
}

func roomAnalysisFixture() audio.PCM16RoomInput {
	const sampleRate = 1000
	const streamSamples = 3000
	const routeLag = 40

	aSent := patternedSamples(streamSamples, 300, 2500, 11)
	bSent := patternedSamples(streamSamples, 300, 2500, 29)
	aReceived := delayedCopy(bSent, routeLag)
	bReceived := delayedCopy(aSent, routeLag)
	interrupt := patternedSamples(2000, 1000, 1200, 47)
	response := patternedSamples(2000, 500, 1300, 71)

	return audio.PCM16RoomInput{
		Streams: []audio.PCM16TimedStream{
			pcm16TimedStream("a-sent", "alice", aSent, sampleRate, 300, 2500),
			pcm16TimedStream("b-sent", "bob", bSent, sampleRate, 300, 2500),
			pcm16TimedStream("a-received", "alice", aReceived, sampleRate, 340, 2540),
			pcm16TimedStream("b-received", "bob", bReceived, sampleRate, 340, 2540),
			pcm16TimedStream("interrupt", "alice", interrupt, sampleRate, 1000, 1200),
			pcm16TimedStream("response", "bob", response, sampleRate, 500, 1300),
		},
		Overlaps: []audio.PCM16OverlapInterval{
			{
				PCM16TimeInterval: audio.PCM16TimeInterval{ID: "overlap-1", Start: 600 * time.Millisecond, End: 2 * time.Second},
				A:                 audio.PCM16OverlapParticipant{ParticipantID: "alice", SentStreamID: "a-sent", ReceivedStreamID: "a-received"},
				B:                 audio.PCM16OverlapParticipant{ParticipantID: "bob", SentStreamID: "b-sent", ReceivedStreamID: "b-received"},
			},
		},
		BargeIns: []audio.PCM16BargeInAnnotation{
			{
				PCM16TimeInterval:   audio.PCM16TimeInterval{ID: "barge-1", Start: 0, End: 1800 * time.Millisecond},
				InterrupterStreamID: "interrupt",
				InterruptedStreamID: "response",
			},
		},
	}
}

func pcm16TimedStream(streamID, participantID string, samples []int16, sampleRate, speechStart, speechEnd int) audio.PCM16TimedStream {
	return audio.PCM16TimedStream{
		PCM16Input: audio.PCM16Input{
			StreamID:       streamID,
			ParticipantID:  participantID,
			SampleRate:     sampleRate,
			Samples:        samples,
			ExpectedSpeech: []audio.SpeechAnnotation{{StartSample: speechStart, EndSample: speechEnd}},
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
