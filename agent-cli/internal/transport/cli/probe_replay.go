package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	serviceprobes "github.com/portpowered/go-agent-harness/agent-cli/internal/services/probes"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

type replayCorpusSpec struct {
	filename   string
	sampleRate int
}

// replayCorpusSpecs maps authored offline probe IDs to committed WAV inputs.
// Capture framing, append replacement, and replay ordering are owned by the
// replay service.
var replayCorpusSpecs = map[string]replayCorpusSpec{
	"utterance-hello-there": {filename: "utt_short_16k.wav", sampleRate: wavio.Rate16kHz},
	"truncated_16k":         {filename: "truncated_16k.wav", sampleRate: wavio.Rate16kHz},
	"truncated_24k":         {filename: "truncated_24k.wav", sampleRate: wavio.Rate24kHz},
	"overlap_16k":           {filename: "overlap_16k.wav", sampleRate: wavio.Rate16kHz},
	"overlap_24k":           {filename: "overlap_24k.wav", sampleRate: wavio.Rate24kHz},
}

var replaySyntheticCorpusIDs = map[string]struct{}{
	"v3c-utterance-1": {},
	"v3c-utterance-2": {},
	"v3c-utterance-3": {},
}

// replayCorpusLookup accepts only IDs declared by the offline probe runner.
type replayCorpusLookup struct{}

func (replayCorpusLookup) Has(id string) bool {
	if _, ok := replayCorpusSpecs[id]; ok {
		return true
	}
	_, ok := replaySyntheticCorpusIDs[id]
	return ok
}

func replayCorpusPath(id string) (string, error) {
	spec, ok := replayCorpusSpecs[id]
	if !ok {
		return "", fmt.Errorf("audio corpus %q has no committed WAV mapping", id)
	}
	workingDir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("locate committed audio corpus %q: %w", id, err)
	}
	for directory := workingDir; ; directory = filepath.Dir(directory) {
		candidate := filepath.Join(directory, "go-agent-loop", "testdata", "audio", spec.filename)
		if info, statErr := os.Stat(candidate); statErr == nil && !info.IsDir() {
			return candidate, nil
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			break
		}
	}
	return "", fmt.Errorf("audio corpus %q is not available under go-agent-loop/testdata/audio", id)
}

func replayCorpusSamples(id string) ([]int16, int, error) {
	path, err := replayCorpusPath(id)
	if err != nil {
		return nil, 0, err
	}
	wavBytes, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, fmt.Errorf("read audio corpus %q: %w", id, err)
	}
	rate, samples, err := wavio.Read(bytes.NewReader(wavBytes))
	if err != nil {
		return nil, 0, fmt.Errorf("decode audio corpus %q: %w", id, err)
	}
	return samples, rate, nil
}

// scenarioReplayCorpusID returns the one committed WAV corpus a replay can
// inject. Synthetic multi-turn fixture IDs keep their authored event streams.
func scenarioReplayCorpusID(scenario probe.Scenario) (string, bool) {
	var corpusID string
	count := 0
	for _, step := range scenario.Steps {
		kind := step.Kind
		if kind == "" {
			kind = step.Type
		}
		if kind != probe.StepSendAudio {
			continue
		}
		count++
		candidate := step.CorpusID
		if candidate == "" {
			candidate = step.Corpus.CorpusID
		}
		if corpusID == "" {
			corpusID = candidate
		}
		if corpusID != candidate {
			return "", false
		}
	}
	if count != 1 {
		return "", false
	}
	if _, ok := replayCorpusSpecs[corpusID]; !ok {
		return "", false
	}
	return corpusID, true
}

// replayExecFunc returns a network-free ExecFunc backed by replay.Service.
func replayExecFunc(replayService replay.Service, fixtures map[string]string, collectors ...serviceprobes.MetricsCollector) probe.ExecFunc {
	return func(ctx context.Context, scenario probe.Scenario) (probe.ObservationSnapshot, error) {
		fixture, err := replayFixtureForScenario(fixtures, scenario)
		if err != nil {
			return probe.ObservationSnapshot{}, err
		}
		request := replay.CaptureProbeRequest{SourcePath: fixture, ValidateSource: true}
		if corpusID, inject := scenarioReplayCorpusID(scenario); inject {
			samples, rate, sampleErr := replayCorpusSamples(corpusID)
			if sampleErr != nil {
				return probe.ObservationSnapshot{}, sampleErr
			}
			request.ValidateSource = false
			request.CorpusID = corpusID
			request.AudioSamples = samples
			request.SampleRateHz = rate
			request.ExpectedSampleRateHz = replayCorpusSpecs[corpusID].sampleRate
		}
		return observationFromSessionCapture(ctx, scenario, replayService, request, collectors...)
	}
}

// replayFixtureForScenario resolves both the authored name and the filename
// spelling used by committed S2S fixtures.
func replayFixtureForScenario(fixtures map[string]string, scenario probe.Scenario) (string, error) {
	for _, candidate := range []string{scenario.Name, scenario.ID, scenarioName(scenario)} {
		if fixture, ok := fixtures[candidate]; ok {
			return fixture, nil
		}
	}

	want := normalizeReplayFixtureName(scenarioName(scenario))
	var matched string
	for key, fixture := range fixtures {
		if normalizeReplayFixtureName(key) != want {
			continue
		}
		if matched != "" && matched != fixture {
			return "", fmt.Errorf("multiple recorded fixtures match scenario %q", scenarioName(scenario))
		}
		matched = fixture
	}
	if matched != "" {
		return matched, nil
	}
	if len(fixtures) == 1 {
		for _, only := range fixtures {
			return only, nil
		}
	}
	return "", fmt.Errorf("no recorded fixture matches scenario %q", scenarioName(scenario))
}

func normalizeReplayFixtureName(value string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(value)), "_", "-")
}
