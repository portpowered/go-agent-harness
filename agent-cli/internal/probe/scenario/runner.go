package scenario

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/probe/replay"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/probe/scenariov2"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
)

// DefaultDeadline is the deadguard bound applied to every scenario
// execution: a session that never terminates within this window yields a
// failed result with a deadguard indication instead of blocking the runner.
// It is shared with the scenario.v2 executor.
const DefaultDeadline = scenariov2.DefaultDeadline

// Deadguard bounds one scenario execution by a wall-clock deadline so a hung
// session yields a failed result carrying a deadguard indication instead of
// blocking the runner.
func Deadguard(exec probe.ExecFunc, deadline time.Duration) probe.ExecFunc {
	return func(ctx context.Context, scenario probe.Scenario) (probe.ObservationSnapshot, error) {
		bounded, cancel := context.WithTimeout(ctx, deadline)
		defer cancel()
		type outcome struct {
			snapshot probe.ObservationSnapshot
			err      error
		}
		done := make(chan outcome, 1)
		go func() {
			snapshot, execErr := exec(bounded, scenario)
			done <- outcome{snapshot: snapshot, err: execErr}
		}()
		select {
		case result := <-done:
			return result.snapshot, result.err
		case <-bounded.Done():
			return probe.ObservationSnapshot{}, fmt.Errorf(
				"deadguard: scenario %q exceeded its %s deadline: %w",
				replay.ScenarioName(scenario), deadline, bounded.Err())
		}
	}
}

// NewRunner returns the JSONL probe runner over the offline corpus
// vocabulary, writing verbatim result and summary lines to out.
func NewRunner(exec probe.ExecFunc, out io.Writer) *probe.Runner {
	return &probe.Runner{
		Exec:          exec,
		Out:           out,
		CorpusLookups: []probe.CorpusLookup{replay.Lookup{}},
	}
}

// ResultRouter routes each verbatim runner line either to the results
// destination or, when it decodes as a run summary, to the summary
// destination.
type ResultRouter struct {
	Results io.Writer
	Summary io.Writer
}

func (r *ResultRouter) Write(p []byte) (int, error) {
	var candidate probe.RunSummary
	if json.Unmarshal(p, &candidate) == nil && candidate.Status != "" {
		return r.Summary.Write(p)
	}
	return r.Results.Write(p)
}
