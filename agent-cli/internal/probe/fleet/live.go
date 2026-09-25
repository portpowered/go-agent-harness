package fleet

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/probe/replay"
	serviceSession "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	serviceProbes "github.com/portpowered/go-agent-harness/agent-cli/internal/services/probes"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
	runtimeReplay "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
)

// LiveSessionRunner runs one live session; callers may replace it with a
// hermetic fake without changing entry dispatch.
type LiveSessionRunner func(context.Context, io.Writer, serviceSession.Request, serviceSession.AudioInput) error

// LiveExecutor runs live entries through the existing session runtime,
// recording each session to a temporary capture that the replay service
// then analyzes like any recorded fixture.
type LiveExecutor struct {
	Options serviceSession.Request
	Run     LiveSessionRunner
	Replay  runtimeReplay.Service
	Metrics serviceProbes.MetricsCollector
	Corpus  replay.Corpus
}

// Execute runs one live manifest entry.
func (e LiveExecutor) Execute(ctx context.Context, entry Entry) (EntryOutcome, error) {
	return runEntry(ctx, entry, e.exec)
}

func (e LiveExecutor) exec(ctx context.Context, scenario probe.Scenario) (probe.ObservationSnapshot, error) {
	prompt, audioPath, err := e.inputs(scenario)
	if err != nil {
		return probe.ObservationSnapshot{}, err
	}
	captureDir, err := os.MkdirTemp("", "agent-probe-fleet-live-")
	if err != nil {
		return probe.ObservationSnapshot{}, fmt.Errorf("create live fleet capture directory: %w", err)
	}
	defer os.RemoveAll(captureDir) //nolint:errcheck // Best-effort removal of a private temporary capture; the entry outcome is already determined.

	capturePath := filepath.Join(captureDir, "session.json")
	options := e.Options
	options.RecordPath = capturePath
	options.Prompt = prompt
	audioInput := serviceSession.AudioInput{Path: audioPath, Present: audioPath != ""}
	if err := e.Run(ctx, io.Discard, options, audioInput); err != nil {
		return probe.ObservationSnapshot{}, fmt.Errorf("run live fleet scenario %q: %w", replay.ScenarioName(scenario), err)
	}
	return replay.Observe(ctx, scenario, e.Replay, runtimeReplay.CaptureProbeRequest{SourcePath: capturePath}, e.Metrics)
}

// liveInputs accumulates the one-turn text/audio shape the live session path
// supports: optional text before at most one audio input, then close.
type liveInputs struct {
	scenario   probe.Scenario
	corpus     replay.Corpus
	prompt     string
	audioPath  string
	audioSeen  bool
	inputSeen  bool
	stepNumber int
}

func (e LiveExecutor) inputs(scenario probe.Scenario) (prompt, audioPath string, err error) {
	state := liveInputs{scenario: scenario, corpus: e.Corpus}
	for index, step := range scenario.Steps {
		state.stepNumber = index
		if err := state.accept(step); err != nil {
			return "", "", err
		}
	}
	if !state.inputSeen {
		return "", "", fmt.Errorf("live fleet scenario %q has no send_text or send_audio input", replay.ScenarioName(scenario))
	}
	return state.prompt, state.audioPath, nil
}

func (s *liveInputs) accept(step probe.Step) error {
	kind := replay.StepKind(step)
	handlers := map[probe.StepKind]func(probe.Step) error{
		probe.StepSendText:  s.acceptText,
		probe.StepSendAudio: s.acceptAudio,
		// The existing session runtime closes after the response for the
		// supported one-turn text/audio shape.
		probe.StepClose: func(probe.Step) error { return nil },
	}
	handle, ok := handlers[kind]
	if !ok {
		return fmt.Errorf("live fleet scenario %q step %d uses %q; the existing live session path supports send_text, send_audio, and close", s.name(), s.stepNumber, kind)
	}
	return handle(step)
}

func (s *liveInputs) acceptText(step probe.Step) error {
	if s.audioSeen {
		return fmt.Errorf("live fleet scenario %q step %d sends text after audio; the live session path supports text before one audio input", s.name(), s.stepNumber)
	}
	if s.inputSeen {
		return fmt.Errorf("live fleet scenario %q step %d has more than one input", s.name(), s.stepNumber)
	}
	s.prompt = step.Text
	s.inputSeen = true
	return nil
}

func (s *liveInputs) acceptAudio(step probe.Step) error {
	if s.audioSeen {
		return fmt.Errorf("live fleet scenario %q step %d has more than one audio input", s.name(), s.stepNumber)
	}
	path, err := s.corpus.Path(replay.StepCorpusID(step))
	if err != nil {
		return fmt.Errorf("live fleet scenario %q step %d: %w", s.name(), s.stepNumber, err)
	}
	s.audioPath = path
	s.audioSeen = true
	s.inputSeen = true
	return nil
}

func (s *liveInputs) name() string {
	return replay.ScenarioName(s.scenario)
}
