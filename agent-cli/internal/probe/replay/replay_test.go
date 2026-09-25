package replay

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
	runtimeReplay "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	replaywire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay/wire"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

const (
	cliFixtureDir   = "../../transport/cli/testdata/probe-fixtures"
	v3aFixture16k   = cliFixtureDir + "/s2s-v3a-barge-in-basic-cancelled-16k.session.json"
	v3aFixture24k   = cliFixtureDir + "/s2s-v3a-barge-in-basic-cancelled-24k.session.json"
	committedAudio  = "../../../../go-agent-loop/testdata/audio"
	v3cFixtureDir   = "../../../test/integration/testdata"
	v3aCancelTick16 = 3
	v3aCancelTick24 = 4
)

func testCorpus() Corpus {
	return Corpus{WorkingDir: func() (string, error) { return filepath.Abs(".") }}
}

func TestLookupRejectsUnknownIDs(t *testing.T) {
	lookup := Lookup{}
	for _, id := range []string{"", "made-up-corpus", "overlap_16k.wav"} {
		if lookup.Has(id) {
			t.Fatalf("unknown corpus ID %q was accepted", id)
		}
	}
	for _, id := range []string{"overlap_16k", "overlap_24k", "truncated_16k", "truncated_24k", "utterance-hello-there", "v3c-utterance-1"} {
		if !lookup.Has(id) {
			t.Fatalf("known corpus ID %q was rejected", id)
		}
	}
}

func TestCommittedCorpusFilesExist(t *testing.T) {
	for _, name := range []string{"truncated_16k.wav", "truncated_24k.wav"} {
		path := filepath.Join(committedAudio, name)
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("committed truncated corpus fixture %s must be reused by the v2e scenarios: %v", path, err)
		}
	}
}

// TestCommittedOverlapCorpusCarriesSpeechEnergy proves both sample-rate
// variants of the interrupting barge-in input load from the committed corpus
// at their recorded rates and carry real speech energy.
func TestCommittedOverlapCorpusCarriesSpeechEnergy(t *testing.T) {
	for _, variant := range []struct {
		id       string
		wantRate int
	}{
		{id: "overlap_16k", wantRate: wavio.Rate16kHz},
		{id: "overlap_24k", wantRate: wavio.Rate24kHz},
	} {
		samples, rate, err := testCorpus().Samples(variant.id)
		if err != nil {
			t.Fatalf("committed overlap corpus %s must load: %v", variant.id, err)
		}
		if rate != variant.wantRate || len(samples) == 0 {
			t.Fatalf("overlap corpus %s = %d samples at %d Hz, want non-empty at %d Hz", variant.id, len(samples), rate, variant.wantRate)
		}
		var sum float64
		for _, sample := range samples {
			sum += float64(sample) * float64(sample)
		}
		if rms := math.Sqrt(sum / float64(len(samples))); rms <= probe.AudioEnergyThreshold {
			t.Fatalf("overlap corpus %s RMS = %.2f must exceed the VAD threshold %.2f", variant.id, rms, probe.AudioEnergyThreshold)
		}
	}
}

func TestCorpusPathReportsUnmappedMissingAndUnconfiguredLookups(t *testing.T) {
	if _, err := testCorpus().Path("v3c-utterance-1"); err == nil || !strings.Contains(err.Error(), "no committed WAV mapping") {
		t.Fatalf("synthetic corpus path error = %v, want no-mapping failure", err)
	}
	outside := Corpus{WorkingDir: func() (string, error) { return t.TempDir(), nil }}
	if _, err := outside.Path("overlap_16k"); err == nil || !strings.Contains(err.Error(), "is not available") {
		t.Fatalf("outside checkout path error = %v, want unavailable corpus", err)
	}
	if _, _, err := (Corpus{}).Samples("overlap_16k"); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("unconfigured corpus error = %v, want lookup failure", err)
	}
	failed := Corpus{WorkingDir: func() (string, error) { return "", errors.New("cwd gone") }}
	if _, err := failed.Path("overlap_16k"); err == nil || !strings.Contains(err.Error(), "cwd gone") {
		t.Fatalf("failed working directory error = %v, want wrapped cause", err)
	}
}

// TestReplayServiceInjectsOverlapCorpus preserves the provider cancel's
// recorded logical position while the service replays the full committed WAV.
func TestReplayServiceInjectsOverlapCorpus(t *testing.T) {
	tests := []struct {
		name       string
		fixture    string
		corpusID   string
		wantRate   int
		wantCancel probe.LogicalTime
	}{
		{name: "16k", fixture: v3aFixture16k, corpusID: "overlap_16k", wantRate: wavio.Rate16kHz, wantCancel: v3aCancelTick16},
		{name: "24k", fixture: v3aFixture24k, corpusID: "overlap_24k", wantRate: wavio.Rate24kHz, wantCancel: v3aCancelTick24},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			samples, rate, err := testCorpus().Samples(test.corpusID)
			if err != nil || rate != test.wantRate {
				t.Fatalf("load corpus = %d Hz, %v; want %d Hz", rate, err, test.wantRate)
			}
			observation, err := replaywire.NewService().AnalyzeProbe(t.Context(), runtimeReplay.CaptureProbeRequest{
				SourcePath: test.fixture, CorpusID: test.corpusID, AudioSamples: samples,
				SampleRateHz: rate, ExpectedSampleRateHz: test.wantRate,
			})
			if err != nil {
				t.Fatalf("replay injected corpus: %v", err)
			}
			if !observation.HasInterruptTick || observation.InterruptTick != 2 {
				t.Fatalf("interrupt tick = %d (present=%t), want actual first append tick 2", observation.InterruptTick, observation.HasInterruptTick)
			}
			if !observation.HasResponseCancel || probe.LogicalTime(observation.ResponseCancelTick) != test.wantCancel {
				t.Fatalf("cancel tick = %d (present=%t), want %d", observation.ResponseCancelTick, observation.HasResponseCancel, test.wantCancel)
			}
		})
	}
}

func registeredScenario(t *testing.T, id string) probe.Scenario {
	t.Helper()
	for _, scenario := range probe.Scenarios() {
		if scenario.ID == id {
			return scenario
		}
	}
	t.Fatalf("registered scenario %q not found", id)
	return probe.Scenario{}
}

// TestExecutorDerivesV3CBargeInComposition counts the exact composition the
// positive s2s-v3c fixture encodes: the numbers the proof doc records.
func TestExecutorDerivesV3CBargeInComposition(t *testing.T) {
	fixture := filepath.Join(v3cFixtureDir, "s2s-v3c-barge-in-repeated.session.json")
	executor := Executor{Service: replaywire.NewService(), Fixtures: map[string]string{"s2s-v3c-barge-in-repeated": fixture}, Corpus: testCorpus()}
	observation, err := executor.Exec(t.Context(), registeredScenario(t, "s2s-v3c-barge-in-repeated"))
	if err != nil {
		t.Fatalf("replay execution failed: %v", err)
	}
	observed := map[string]int{
		"user_turns": observation.UserTurnsCommitted, "assistant_delivered": observation.AssistantTurnsDelivered,
		"responses_created": observation.ResponsesCreated, "cancelled_responses": observation.ResponsesCancelled,
		"cancel_events": observation.ResponseCancels, "post_cancel_deltas": observation.PostCancelDeltas,
	}
	want := map[string]int{
		"user_turns": 7, "assistant_delivered": 4, "responses_created": 7,
		"cancelled_responses": 3, "cancel_events": 3, "post_cancel_deltas": 0,
	}
	for key, expected := range want {
		if got := observed[key]; got != expected {
			t.Fatalf("%s = %d, want %d", key, got, expected)
		}
	}
	if observation.SpuriousCancels != 0 || observation.InFlightAtEnd {
		t.Fatalf("clean session must have no stray cancels and nothing in flight: %+v", observation)
	}
}

func TestFixtureForScenarioMatchesNamesSpellingsAndSingleFixture(t *testing.T) {
	scenario := probe.Scenario{ID: "s2s-v2a-audio-in-basic", Name: "S2S_V2A_Audio_In_Basic"}
	fixtures := map[string]string{"s2s-v2a-audio-in-basic": "by-id", "other": "other"}
	if got, err := FixtureForScenario(fixtures, scenario); err != nil || got != "by-id" {
		t.Fatalf("exact ID match = %q, %v", got, err)
	}
	normalized := map[string]string{"s2s_v2a_audio_in_basic": "normalized", "other": "other"}
	if got, err := FixtureForScenario(normalized, probe.Scenario{Name: "S2S-V2A-Audio-In-Basic"}); err != nil || got != "normalized" {
		t.Fatalf("normalized match = %q, %v", got, err)
	}
	ambiguous := map[string]string{"a_b": "one", "a-b": "two"}
	if _, err := FixtureForScenario(ambiguous, probe.Scenario{Name: "a-b-c"}); err == nil || !strings.Contains(err.Error(), "no recorded fixture") {
		t.Fatalf("unmatched error = %v", err)
	}
	if _, err := FixtureForScenario(ambiguous, probe.Scenario{Name: "A_B "}); err == nil || !strings.Contains(err.Error(), "multiple recorded fixtures") {
		t.Fatalf("ambiguous error = %v", err)
	}
	if got, err := FixtureForScenario(map[string]string{"only": "single"}, scenario); err != nil || got != "single" {
		t.Fatalf("single fixture = %q, %v", got, err)
	}
}

func TestLoadFixturesAcceptsFileOrSessionDirectory(t *testing.T) {
	fixtures, err := LoadFixtures(v3aFixture16k)
	if err != nil || fixtures["s2s-v3a-barge-in-basic-cancelled-16k"] != v3aFixture16k {
		t.Fatalf("single fixture = %v, %v", fixtures, err)
	}
	fixtures, err = LoadFixtures(cliFixtureDir)
	if err != nil || len(fixtures) < 2 || fixtures["s2s_v2a_audio_in_basic"] == "" {
		t.Fatalf("fixture directory = %v, %v", fixtures, err)
	}
	if _, err := LoadFixtures(t.TempDir()); err == nil || !strings.Contains(err.Error(), "contains no recorded session fixtures") {
		t.Fatalf("empty directory error = %v", err)
	}
	if _, err := LoadFixtures(filepath.Join(t.TempDir(), "missing.json")); err == nil || !strings.Contains(err.Error(), "missing or unreadable") {
		t.Fatalf("missing fixture error = %v", err)
	}
}

type stubMetrics struct {
	series []probe.MetricsSeries
	err    error
}

func (s stubMetrics) Collect(context.Context, string, string) ([]probe.MetricsSeries, error) {
	return s.series, s.err
}

func TestMetricsEvidenceRequiresCollectorAndInjectsNegativeControlFault(t *testing.T) {
	scenario := probe.Scenario{
		ID:           probe.ScenarioIDS2SV7AMetricsModalityOvercount,
		Steps:        []probe.Step{{Type: probe.StepSendText, Text: "hello"}},
		Expectations: []probe.ExpectedBehavior{{Type: probe.ExpectMetricsReconcile}},
	}
	report := runtimeReplay.CaptureProbeObservation{}
	if _, err := observationFromReport(t.Context(), scenario, runtimeReplay.CaptureProbeRequest{}, report, nil); err == nil || !strings.Contains(err.Error(), "metrics collector is not configured") {
		t.Fatalf("missing collector error = %v", err)
	}
	collector := stubMetrics{series: []probe.MetricsSeries{
		{Direction: "output", Modality: "tool", ReportedTotal: 2},
		{Direction: "input", Modality: "text", ReportedTotal: 5},
	}}
	observation, err := observationFromReport(t.Context(), scenario, runtimeReplay.CaptureProbeRequest{}, report, collector)
	if err != nil {
		t.Fatalf("collect metrics: %v", err)
	}
	if observation.Metrics[0].ReportedTotal != 3 || observation.Metrics[1].ReportedTotal != 5 {
		t.Fatalf("overcount fault = %+v, want only output/tool raised by one", observation.Metrics)
	}
	if _, _, err := Analyze(t.Context(), nil, scenario, runtimeReplay.CaptureProbeRequest{}); err == nil || !strings.Contains(err.Error(), "replay service is not configured") {
		t.Fatalf("nil service error = %v", err)
	}
}
