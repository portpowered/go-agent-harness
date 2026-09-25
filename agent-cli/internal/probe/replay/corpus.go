// Package replay executes legacy probe scenarios over recorded session
// fixtures. It owns the committed audio corpus vocabulary, fixture discovery
// and matching, the network-free execution function, and the projection of a
// replay-service capture analysis onto the probe runner's observation.
package replay

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

// corpusSpec maps one authored corpus ID to its committed WAV input.
type corpusSpec struct {
	filename   string
	sampleRate int
}

// corpusSpecs maps authored offline probe IDs to committed WAV inputs.
// Capture framing, append replacement, and replay ordering are owned by the
// replay service.
func corpusSpecs() map[string]corpusSpec {
	return map[string]corpusSpec{
		"utterance-hello-there": {filename: "utt_short_16k.wav", sampleRate: wavio.Rate16kHz},
		"truncated_16k":         {filename: "truncated_16k.wav", sampleRate: wavio.Rate16kHz},
		"truncated_24k":         {filename: "truncated_24k.wav", sampleRate: wavio.Rate24kHz},
		"overlap_16k":           {filename: "overlap_16k.wav", sampleRate: wavio.Rate16kHz},
		"overlap_24k":           {filename: "overlap_24k.wav", sampleRate: wavio.Rate24kHz},
	}
}

// syntheticCorpusIDs are authored multi-turn fixture IDs whose event streams
// are replayed as recorded rather than injected from a WAV file.
func syntheticCorpusIDs() map[string]struct{} {
	return map[string]struct{}{
		"v3c-utterance-1": {},
		"v3c-utterance-2": {},
		"v3c-utterance-3": {},
	}
}

// Lookup accepts only corpus IDs declared by the offline probe runner.
type Lookup struct{}

// Has reports whether id is a committed or synthetic offline corpus ID.
func (Lookup) Has(id string) bool {
	if _, ok := corpusSpecs()[id]; ok {
		return true
	}
	_, ok := syntheticCorpusIDs()[id]
	return ok
}

// Corpus resolves committed WAV inputs by searching upward from the host's
// working directory for go-agent-loop/testdata/audio.
type Corpus struct {
	// WorkingDir returns the directory the upward search starts from.
	WorkingDir func() (string, error)
}

// Path returns the committed WAV file for one corpus ID.
func (c Corpus) Path(id string) (string, error) {
	spec, ok := corpusSpecs()[id]
	if !ok {
		return "", fmt.Errorf("audio corpus %q has no committed WAV mapping", id)
	}
	workingDir, err := c.workingDir()
	if err != nil {
		return "", fmt.Errorf("locate committed audio corpus %q: %w", id, err)
	}
	for directory := workingDir; ; directory = filepath.Dir(directory) {
		candidate := filepath.Join(directory, "go-agent-loop", "testdata", "audio", spec.filename)
		if info, statErr := os.Stat(candidate); statErr == nil && !info.IsDir() {
			return candidate, nil
		}
		if filepath.Dir(directory) == directory {
			break
		}
	}
	return "", fmt.Errorf("audio corpus %q is not available under go-agent-loop/testdata/audio", id)
}

// Samples decodes one committed WAV corpus into PCM16 samples and its rate.
func (c Corpus) Samples(id string) ([]int16, int, error) {
	path, err := c.Path(id)
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

func (c Corpus) workingDir() (string, error) {
	if c.WorkingDir == nil {
		return "", fmt.Errorf("working directory lookup is not configured")
	}
	return c.WorkingDir()
}

// StepKind returns the step's kind, falling back to its legacy type field.
func StepKind(step probe.Step) probe.StepKind {
	if step.Kind != "" {
		return step.Kind
	}
	return step.Type
}

// StepCorpusID returns the step's corpus ID, falling back to the structured
// corpus reference.
func StepCorpusID(step probe.Step) string {
	if step.CorpusID != "" {
		return step.CorpusID
	}
	return step.Corpus.CorpusID
}

// scenarioCorpusID returns the one committed WAV corpus a replay can inject.
// Synthetic multi-turn fixture IDs keep their authored event streams.
func scenarioCorpusID(scenario probe.Scenario) (string, bool) {
	var corpusID string
	count := 0
	for _, step := range scenario.Steps {
		if StepKind(step) != probe.StepSendAudio {
			continue
		}
		count++
		candidate := StepCorpusID(step)
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
	if _, ok := corpusSpecs()[corpusID]; !ok {
		return "", false
	}
	return corpusID, true
}

// ScenarioName is the scenario's authored name, falling back to its ID.
func ScenarioName(scenario probe.Scenario) string {
	if scenario.Name != "" {
		return scenario.Name
	}
	return scenario.ID
}
