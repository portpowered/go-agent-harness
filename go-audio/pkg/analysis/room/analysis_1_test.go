package room_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	roomanalysis "github.com/portpowered/go-agent-harness/go-audio/pkg/analysis/room"
)

func TestAnalyzePCM16RoomMeasuresPeerDeliverySelfHearingBargeBalanceAndDrift(t *testing.T) {
	room := roomAnalysisFixture()
	result, err := roomanalysis.AnalyzePCM16Room(room, roomanalysis.DefaultRoomAnalysisConfig())
	if err != nil {
		t.Fatalf("AnalyzePCM16Room() error = %v", err)
	}
	assertPositiveRoomAnalysis(t, result)
	assertRoomMeasurements(t, result)
}

func assertPositiveRoomAnalysis(t *testing.T, result roomanalysis.PCM16RoomAnalysis) {
	t.Helper()
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
}

func assertRoomMeasurements(t *testing.T, result roomanalysis.PCM16RoomAnalysis) {
	t.Helper()
	assertPeerDeliveries(t, result.PeerDeliveries)
	assertSelfHearings(t, result.SelfHearings)
	assertBargeIn(t, result.BargeIns[0])
	assertLoudness(t, result.Loudness[0])
	assertDrift(t, result.Drift)
}

func assertPeerDeliveries(t *testing.T, deliveries []roomanalysis.PCM16PeerDeliveryMeasurement) {
	t.Helper()
	for _, delivery := range deliveries {
		if !delivery.Passed || delivery.ComparedSamples == 0 || delivery.BestCorrelation < 0.99 {
			t.Errorf("delivery %+v did not prove known-lag routing", delivery)
		}
		if delivery.BestLag != 40*time.Millisecond {
			t.Errorf("delivery %s best lag = %s, want 40ms", delivery.Direction, delivery.BestLag)
		}
	}
}

func assertSelfHearings(t *testing.T, hearings []roomanalysis.PCM16SelfHearingMeasurement) {
	t.Helper()
	for _, self := range hearings {
		if !self.Passed || self.BestAbsoluteCorrelation >= 0.30 {
			t.Errorf("self-hearing %+v should be below the default bound", self)
		}
	}
}

func assertBargeIn(t *testing.T, barge roomanalysis.PCM16BargeInMeasurement) {
	t.Helper()
	if !barge.Passed || !barge.InterrupterOnsetFound || !barge.InterruptedStopFound || barge.Latency != 300*time.Millisecond {
		t.Errorf("barge-in = %+v, want a 300ms measured stop latency", barge)
	}
}

func assertLoudness(t *testing.T, loudness roomanalysis.PCM16LoudnessMeasurement) {
	t.Helper()
	if !loudness.Passed || loudness.DifferenceDB > 6 || loudness.LeftSamples == 0 || loudness.RightSamples == 0 {
		t.Errorf("loudness = %+v, want balanced active speech", loudness)
	}
}

func assertDrift(t *testing.T, drifts []roomanalysis.PCM16DriftMeasurement) {
	t.Helper()
	for _, drift := range drifts {
		if !drift.Passed || drift.Drift != 0 || drift.Bound != 20*time.Millisecond {
			t.Errorf("drift = %+v, want zero drift with a 20ms bound", drift)
		}
	}
}

type roomSyntheticDefectCase struct {
	name       string
	mutate     func(*roomanalysis.PCM16RoomInput)
	properties []string
}

func roomSyntheticDefectCases() []roomSyntheticDefectCase {
	return []roomSyntheticDefectCase{
		{
			name: "self-hearing",
			mutate: func(room *roomanalysis.PCM16RoomInput) {
				room.Streams[2].Samples = delayedCopy(room.Streams[0].Samples, 40)
			},
			properties: []string{"self-hearing"},
		},
		{
			name: "missing forward delivery",
			mutate: func(room *roomanalysis.PCM16RoomInput) {
				room.Streams[3].Samples = make([]int16, len(room.Streams[3].Samples))
				room.Streams[3].ExpectedSpeech = nil
			},
			properties: []string{"overlap-delivery"},
		},
		{
			name: "over-shifted forward delivery",
			mutate: func(room *roomanalysis.PCM16RoomInput) {
				room.Streams[3].Samples = delayedCopy(room.Streams[0].Samples, 300)
				room.Streams[3].ExpectedSpeech = []roomanalysis.SpeechAnnotation{{StartSample: 600, EndSample: 2800}}
			},
			properties: []string{"overlap-delivery"},
		},
		{
			name: "swapped received stream",
			mutate: func(room *roomanalysis.PCM16RoomInput) {
				room.Streams[3].Samples = delayedCopy(room.Streams[1].Samples, 40)
			},
			properties: []string{"overlap-delivery", "self-hearing"},
		},
		{
			name: "barge-in stop too slow",
			mutate: func(room *roomanalysis.PCM16RoomInput) {
				room.Streams[5].Samples = patternedSamples(2000, 500, 1800, 23)
				room.Streams[5].ExpectedSpeech = []roomanalysis.SpeechAnnotation{{StartSample: 500, EndSample: 1800}}
				room.BargeIns[0].End = 1900 * time.Millisecond
			},
			properties: []string{"barge-in-latency"},
		},
		{
			name: "missing barge-in stop",
			mutate: func(room *roomanalysis.PCM16RoomInput) {
				room.Streams[5].Samples = patternedSamples(2000, 500, 1800, 23)
				room.Streams[5].ExpectedSpeech = []roomanalysis.SpeechAnnotation{{StartSample: 500, EndSample: 1800}}
			},
			properties: []string{"barge-in-stop"},
		},
		{
			name: "missing barge-in onset",
			mutate: func(room *roomanalysis.PCM16RoomInput) {
				room.Streams[4].Samples = make([]int16, len(room.Streams[4].Samples))
				room.Streams[4].ExpectedSpeech = nil
			},
			properties: []string{"barge-in-onset"},
		},
		{
			name: "loudness imbalance",
			mutate: func(room *roomanalysis.PCM16RoomInput) {
				room.Streams[1].Samples = scaleSamples(room.Streams[1].Samples, 3)
				room.Streams[3].Samples = scaleSamples(room.Streams[3].Samples, 3)
			},
			properties: []string{"inter-speaker-loudness"},
		},
		{
			name: "timeline drift",
			mutate: func(room *roomanalysis.PCM16RoomInput) {
				room.Streams[0].TimelineEnd += 100 * time.Millisecond
			},
			properties: []string{"timing-drift"},
		},
	}
}

func TestAnalyzePCM16RoomSyntheticSingleDefects(t *testing.T) {
	for _, test := range roomSyntheticDefectCases() {
		t.Run(test.name, func(t *testing.T) {
			runRoomSyntheticDefectCase(t, test)
		})
	}
}

func runRoomSyntheticDefectCase(t *testing.T, test roomSyntheticDefectCase) {
	t.Helper()
	room := roomAnalysisFixture()
	test.mutate(&room)
	result, err := roomanalysis.AnalyzePCM16Room(room, roomanalysis.DefaultRoomAnalysisConfig())
	if err != nil {
		t.Fatalf("AnalyzePCM16Room() error = %v", err)
	}
	properties := roomFailureProperties(t, result.Failures)
	for _, property := range test.properties {
		if !properties[property] {
			t.Errorf("missing property %q in failures: %v", property, result.Failures)
		}
	}
	if result.Passed() {
		t.Fatalf("defect %q unexpectedly passed", test.name)
	}
}

func roomFailureProperties(t *testing.T, failures []roomanalysis.PropertyFailure) map[string]bool {
	t.Helper()
	properties := make(map[string]bool, len(failures))
	for _, failure := range failures {
		properties[failure.Property] = true
		message := failure.Error()
		for _, want := range []string{"interval=", "timestamp=", "measured=", "bound="} {
			if !strings.Contains(message, want) {
				t.Errorf("failure %q missing %q: %s", failure.Property, want, message)
			}
		}
	}
	return properties
}

func TestPCM16RoomCorrelationHonorsExplicitLagWindow(t *testing.T) {
	room := roomAnalysisFixture()
	config := roomanalysis.DefaultRoomAnalysisConfig()
	config.CorrelationLagWindow = roomanalysis.PCM16LagWindow{Min: -20 * time.Millisecond, Max: 20 * time.Millisecond}
	result, err := roomanalysis.AnalyzePCM16Room(room, config)
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
	interval := roomanalysis.PCM16TimeInterval{ID: "standalone-overlap", Start: 600 * time.Millisecond, End: 2 * time.Second}
	correlation, err := roomanalysis.NormalizedPCM16CrossCorrelation(
		room.Streams[0], room.Streams[3], interval,
		roomanalysis.PCM16LagWindow{Min: -100 * time.Millisecond, Max: 100 * time.Millisecond},
		-50,
	)
	if err != nil {
		t.Fatalf("NormalizedPCM16CrossCorrelation() error = %v", err)
	}
	if correlation.BestCorrelation < 0.99 || correlation.BestLag != 40*time.Millisecond || correlation.ComparedSamples == 0 {
		t.Fatalf("correlation = %+v, want known 40ms positive evidence", correlation)
	}

	drift, err := roomanalysis.MeasurePCM16Drift(room.Streams[0])
	if err != nil {
		t.Fatalf("MeasurePCM16Drift() error = %v", err)
	}
	if drift.Drift != 0 || drift.SampleDuration != 3*time.Second || drift.TimestampSpan != 3*time.Second {
		t.Fatalf("drift = %+v, want exact three-second timing", drift)
	}

	loudness, err := roomanalysis.MeasurePCM16Loudness(room.Streams[0], room.Streams[1], interval)
	if err != nil {
		t.Fatalf("MeasurePCM16Loudness() error = %v", err)
	}
	if loudness.LeftSamples == 0 || loudness.RightSamples == 0 || loudness.DifferenceDB > 6 {
		t.Fatalf("loudness = %+v, want balanced annotated active speech", loudness)
	}

	barge, err := roomanalysis.MeasurePCM16BargeIn(room.Streams[4], room.Streams[5], room.BargeIns[0], -40)
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
		mutate func(*roomanalysis.PCM16RoomInput)
		field  string
	}{
		{
			name: "missing stable stream identity",
			mutate: func(room *roomanalysis.PCM16RoomInput) {
				room.Streams[0].StreamID = ""
			},
			field: "streams[0].stream_id",
		},
		{
			name: "unknown overlap stream",
			mutate: func(room *roomanalysis.PCM16RoomInput) {
				room.Overlaps[0].B.ReceivedStreamID = "missing"
			},
			field: "overlaps[0].b.received_stream_id",
		},
		{
			name: "mismatched sample rate",
			mutate: func(room *roomanalysis.PCM16RoomInput) {
				room.Streams[3].SampleRate = 2000
			},
			field: "overlaps[0].sample_rate",
		},
		{
			name: "interval outside timeline",
			mutate: func(room *roomanalysis.PCM16RoomInput) {
				room.Overlaps[0].End = 4 * time.Second
			},
			field: "overlaps[0].a.sent_stream_id",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			room := roomAnalysisFixture()
			test.mutate(&room)
			_, err := roomanalysis.AnalyzePCM16Room(room, roomanalysis.DefaultRoomAnalysisConfig())
			if err == nil || !errors.Is(err, roomanalysis.ErrInvalidPCM16RoomAnalysisInput) {
				t.Fatalf("AnalyzePCM16Room() error = %v, want invalid-room-input", err)
			}
			var inputErr *roomanalysis.InvalidPCM16RoomAnalysisInputError
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
	err := roomanalysis.AssertPCM16Room(room, roomanalysis.DefaultRoomAnalysisConfig())
	if err == nil || !errors.Is(err, roomanalysis.ErrPCM16AnalysisFailed) {
		t.Fatalf("AssertPCM16Room() error = %v, want analysis failure", err)
	}
	var assertionErr *roomanalysis.PCM16RoomAssertionError
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

func TestMeasurePCM16DriftRejectsInvalidTimedStream(t *testing.T) {
	room := roomAnalysisFixture()
	valid := room.Streams[0]

	tests := []struct {
		name   string
		mutate func(*roomanalysis.PCM16TimedStream)
		field  string
	}{
		{
			name:   "missing participant id",
			mutate: func(stream *roomanalysis.PCM16TimedStream) { stream.ParticipantID = "" },
			field:  "stream.participant_id",
		},
		{
			name:   "non-positive sample rate",
			mutate: func(stream *roomanalysis.PCM16TimedStream) { stream.SampleRate = 0 },
			field:  "stream.sample_rate",
		},
		{
			name:   "empty samples",
			mutate: func(stream *roomanalysis.PCM16TimedStream) { stream.Samples = nil },
			field:  "stream.samples",
		},
		{
			name:   "negative timeline start",
			mutate: func(stream *roomanalysis.PCM16TimedStream) { stream.TimelineStart = -time.Millisecond },
			field:  "stream.timeline_start",
		},
		{
			name:   "timeline end before start",
			mutate: func(stream *roomanalysis.PCM16TimedStream) { stream.TimelineEnd = stream.TimelineStart },
			field:  "stream.timeline_end",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stream := valid
			test.mutate(&stream)
			_, err := roomanalysis.MeasurePCM16Drift(stream)
			if err == nil || !errors.Is(err, roomanalysis.ErrInvalidPCM16RoomAnalysisInput) {
				t.Fatalf("MeasurePCM16Drift() error = %v, want invalid-room-input", err)
			}
			var inputErr *roomanalysis.InvalidPCM16RoomAnalysisInputError
			if !errors.As(err, &inputErr) || inputErr.Field != test.field {
				t.Fatalf("typed input error = %+v, want field %q", inputErr, test.field)
			}
		})
	}
}

func TestPCM16RoomRejectsInvalidBargeInAndOverlapEndpoints(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*roomanalysis.PCM16RoomInput)
		field  string
	}{
		{
			name: "negative barge-in interval start",
			mutate: func(room *roomanalysis.PCM16RoomInput) {
				room.BargeIns[0].Start = -time.Millisecond
			},
			field: "barge_ins[0].start",
		},
		{
			name: "missing barge-in interrupter and interrupted ids",
			mutate: func(room *roomanalysis.PCM16RoomInput) {
				room.BargeIns[0].InterrupterStreamID = ""
				room.BargeIns[0].InterruptedStreamID = ""
			},
			field: "barge_ins[0]",
		},
		{
			name: "identical barge-in interrupter and interrupted streams",
			mutate: func(room *roomanalysis.PCM16RoomInput) {
				room.BargeIns[0].InterruptedStreamID = room.BargeIns[0].InterrupterStreamID
			},
			field: "barge_ins[0]",
		},
		{
			name: "unknown barge-in interrupted stream",
			mutate: func(room *roomanalysis.PCM16RoomInput) {
				room.BargeIns[0].InterruptedStreamID = "missing"
			},
			field: "barge_ins[0].interrupted_stream_id",
		},
		{
			name: "missing overlap sent/received ids",
			mutate: func(room *roomanalysis.PCM16RoomInput) {
				room.Overlaps[0].A.SentStreamID = ""
				room.Overlaps[0].A.ReceivedStreamID = ""
			},
			field: "overlaps[0].a",
		},
		{
			name: "overlap endpoint owned by a different participant",
			mutate: func(room *roomanalysis.PCM16RoomInput) {
				room.Overlaps[0].A.ParticipantID = "someone-else"
			},
			field: "overlaps[0].a.participant_id",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			room := roomAnalysisFixture()
			test.mutate(&room)
			_, err := roomanalysis.AnalyzePCM16Room(room, roomanalysis.DefaultRoomAnalysisConfig())
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

func TestMeasurePCM16BargeInRejectsInvalidInputsAndDefaultsThreshold(t *testing.T) {
	room := roomAnalysisFixture()

	if _, err := roomanalysis.MeasurePCM16BargeIn(room.Streams[4], room.Streams[4], room.BargeIns[0], -40); err == nil {
		t.Fatal("MeasurePCM16BargeIn() error = nil, want identical-stream failure")
	}

	if _, err := roomanalysis.MeasurePCM16BargeIn(room.Streams[4], room.Streams[5], room.BargeIns[0], 5); err == nil || !errors.Is(err, roomanalysis.ErrInvalidPCM16RoomAnalysisInput) {
		t.Fatalf("MeasurePCM16BargeIn() error = %v, want invalid-threshold failure", err)
	}

	defaulted, err := roomanalysis.MeasurePCM16BargeIn(room.Streams[4], room.Streams[5], room.BargeIns[0], 0)
	if err != nil {
		t.Fatalf("MeasurePCM16BargeIn() with zero threshold error = %v", err)
	}
	explicit, err := roomanalysis.MeasurePCM16BargeIn(room.Streams[4], room.Streams[5], room.BargeIns[0], roomanalysis.PCM16AnalysisDefaultBargeInSpeechThresholdDBFS)
	if err != nil {
		t.Fatalf("MeasurePCM16BargeIn() error = %v", err)
	}
	if defaulted.Latency != explicit.Latency || defaulted.Passed != explicit.Passed {
		t.Fatalf("MeasurePCM16BargeIn() zero threshold = %+v, want the same result as the explicit default = %+v", defaulted, explicit)
	}
}

func TestPCM16RoomErrorTypesHandleNilAndEmptyState(t *testing.T) {
	var nilAssertionErr *roomanalysis.PCM16RoomAssertionError
	if got, want := nilAssertionErr.Error(), "<nil>"; got != want {
		t.Errorf("nil *PCM16RoomAssertionError.Error() = %q, want %q", got, want)
	}
	emptyAssertionErr := &roomanalysis.PCM16RoomAssertionError{}
	if got, want := emptyAssertionErr.Error(), roomanalysis.ErrPCM16AnalysisFailed.Error(); got != want {
		t.Errorf("empty-failures *PCM16RoomAssertionError.Error() = %q, want %q", got, want)
	}

	var nilInputErr *roomanalysis.InvalidPCM16RoomAnalysisInputError
	if got, want := nilInputErr.Error(), "<nil>"; got != want {
		t.Errorf("nil *InvalidPCM16RoomAnalysisInputError.Error() = %q, want %q", got, want)
	}
}
