package customersim

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/probe"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

const (
	wavExtension = ".wav"
	pcm16Width   = 2
)

// turnAudioExtensions are the accepted per-turn recording spellings, in the
// order an audio directory is searched.
func turnAudioExtensions() []string {
	return []string{wavExtension, ".pcm", ".raw"}
}

// RunSpecs pairs every selected scenario with its scripted customer turns and
// their PCM16 recordings. Turn audio comes either from --audio, one file per
// selected turn in order, or from an --audio-dir layout. Family E also needs
// exactly one natural check-in recording.
func RunSpecs(scenarios []probe.CustomerScenario, audioPaths []string, audioDir string, patienceRepromptAudioPaths ...string) ([]probe.CustomerSimulationRunSpec, error) {
	if len(audioPaths) > 0 && strings.TrimSpace(audioDir) != "" {
		return nil, errors.New("--audio and --audio-dir cannot be combined")
	}
	reprompt, err := loadPatienceReprompt(scenarios, patienceRepromptAudioPaths)
	if err != nil {
		return nil, err
	}
	totalTurns := 0
	for _, scenario := range scenarios {
		totalTurns += len(probe.CustomerSimulationScenarioScript(scenario))
	}
	if len(audioPaths) > 0 && len(audioPaths) != totalTurns {
		return nil, fmt.Errorf("--audio needs exactly one file per selected customer turn: got %d, want %d", len(audioPaths), totalTurns)
	}
	runs := make([]probe.CustomerSimulationRunSpec, 0, len(scenarios))
	audioIndex := 0
	for _, scenario := range scenarios {
		script := probe.CustomerSimulationScenarioScript(scenario)
		paths, err := scenarioTurnPaths(scenario, script, audioPaths, audioIndex, audioDir)
		if err != nil {
			return nil, err
		}
		audioIndex += len(script)
		spec, err := runSpec(scenario, script, paths, reprompt)
		if err != nil {
			return nil, err
		}
		runs = append(runs, spec)
	}
	return runs, nil
}

// loadPatienceReprompt admits the check-in recording only when Family E is
// selected, and requires it in that case.
func loadPatienceReprompt(scenarios []probe.CustomerScenario, paths []string) ([]byte, error) {
	if len(paths) > 1 {
		return nil, errors.New("only one --patience-reprompt-audio path is supported")
	}
	path := ""
	if len(paths) == 1 {
		path = strings.TrimSpace(paths[0])
	}
	hasFamilyE := false
	for _, scenario := range scenarios {
		if scenario.Family == probe.ScenarioFamilyE {
			hasFamilyE = true
			break
		}
	}
	if hasFamilyE && path == "" {
		return nil, errors.New("family E requires --patience-reprompt-audio with a natural check-in recording")
	}
	if !hasFamilyE && path != "" {
		return nil, errors.New("--patience-reprompt-audio is only valid when Family E is selected")
	}
	if path == "" {
		return nil, nil
	}
	data, err := readPCM16(path)
	if err != nil {
		return nil, fmt.Errorf("load Family E patience re-prompt audio: %w", err)
	}
	return data, nil
}

func scenarioTurnPaths(scenario probe.CustomerScenario, script []probe.CustomerScriptTurn, audioPaths []string, audioIndex int, audioDir string) ([]string, error) {
	if len(audioPaths) > 0 {
		paths := make([]string, len(script))
		copy(paths, audioPaths[audioIndex:audioIndex+len(script)])
		return paths, nil
	}
	if strings.TrimSpace(audioDir) != "" {
		return resolveAudioDirPaths(audioDir, scenario, script)
	}
	return nil, fmt.Errorf("audio is required for scenario %q; pass --audio once per turn or --audio-dir", scenario.ID)
}

func runSpec(scenario probe.CustomerScenario, script []probe.CustomerScriptTurn, paths []string, reprompt []byte) (probe.CustomerSimulationRunSpec, error) {
	pcm := make([][]byte, len(paths))
	for index, path := range paths {
		data, err := readPCM16(path)
		if err != nil {
			return probe.CustomerSimulationRunSpec{}, fmt.Errorf("load audio for scenario %q turn %d: %w", scenario.ID, index+1, err)
		}
		pcm[index] = data
	}
	spec := probe.CustomerSimulationRunSpec{Scenario: scenario, Script: script, Audio: pcm}
	if scenario.Family == probe.ScenarioFamilyE {
		spec.PatienceRepromptAudio = append([]byte(nil), reprompt...)
	}
	return spec, nil
}

// resolveAudioDirPaths finds one recording per turn, trying the
// scenario/action, scenario/NN, scenario-action, and bare action names.
func resolveAudioDirPaths(root string, scenario probe.CustomerScenario, script []probe.CustomerScriptTurn) ([]string, error) {
	paths := make([]string, len(script))
	for index, turn := range script {
		bases := []string{
			filepath.Join(root, scenario.ID, turn.ActionID),
			filepath.Join(root, scenario.ID, fmt.Sprintf("%02d", index+1)),
			filepath.Join(root, scenario.ID+"-"+turn.ActionID),
			filepath.Join(root, turn.ActionID),
		}
		found := firstRegularFile(bases)
		if found == "" {
			return nil, fmt.Errorf("audio directory %q has no file for scenario %q turn %q; tried scenario/action .wav/.pcm/.raw names", root, scenario.ID, turn.ActionID)
		}
		paths[index] = found
	}
	return paths, nil
}

func firstRegularFile(bases []string) string {
	for _, base := range bases {
		for _, extension := range turnAudioExtensions() {
			candidate := base + extension
			if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
				return candidate
			}
		}
	}
	return ""
}

// readPCM16 loads raw PCM16 or a 16 kHz WAV file as non-empty PCM16 bytes.
func readPCM16(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if strings.EqualFold(filepath.Ext(path), wavExtension) {
		rate, samples, err := wavio.Read(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		if rate != wavio.Rate16kHz {
			return nil, fmt.Errorf("WAV sample rate is %d Hz; customer simulation requires %d Hz", rate, wavio.Rate16kHz)
		}
		data = codec.EncodePCM16(samples)
	}
	if len(data) == 0 || len(data)%pcm16Width != 0 {
		return nil, errors.New("audio must be non-empty, even-length PCM16")
	}
	return append([]byte(nil), data...), nil
}
