package replay

import (
	"context"

	serviceprobes "github.com/portpowered/go-agent-harness/agent-cli/internal/services/probes"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
	runtimeReplay "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
)

// Executor runs legacy scenarios over recorded fixtures without network
// access. Its Exec method is the probe runner's execution function.
type Executor struct {
	Service  runtimeReplay.Service
	Fixtures map[string]string
	// Metrics collects reconciliation evidence for scenarios that declare a
	// metrics-reconcile expectation; it may be nil otherwise.
	Metrics serviceprobes.MetricsCollector
	Corpus  Corpus
}

// Exec replays the scenario's matching fixture. A scenario with exactly one
// committed WAV input has that corpus injected in place of the recorded
// audio.
func (e Executor) Exec(ctx context.Context, scenario probe.Scenario) (probe.ObservationSnapshot, error) {
	fixture, err := FixtureForScenario(e.Fixtures, scenario)
	if err != nil {
		return probe.ObservationSnapshot{}, err
	}
	request := runtimeReplay.CaptureProbeRequest{SourcePath: fixture, ValidateSource: true}
	if corpusID, inject := scenarioCorpusID(scenario); inject {
		samples, rate, sampleErr := e.Corpus.Samples(corpusID)
		if sampleErr != nil {
			return probe.ObservationSnapshot{}, sampleErr
		}
		request.ValidateSource = false
		request.CorpusID = corpusID
		request.AudioSamples = samples
		request.SampleRateHz = rate
		request.ExpectedSampleRateHz = corpusSpecs()[corpusID].sampleRate
	}
	return Observe(ctx, scenario, e.Service, request, e.Metrics)
}
