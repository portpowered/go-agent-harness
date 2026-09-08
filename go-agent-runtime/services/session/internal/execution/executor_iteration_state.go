package agent

import (
	"context"
	"fmt"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/internal/persistence"
	"io"
)

const defaultMaxIterations = 5

type iterationState struct {
	trace          session.TraceRecord
	storage        Storage
	start, maximum int
	stopWord       string
	input          agentloop.ExecuteInput
	done           bool
	resumed        bool
}

func (e *Executor) prepareIterations(ctx context.Context, _ *Config, loopCfg IterativeLoopConfig, input agentloop.ExecuteInput, out io.Writer, interaction *IterativeInteraction) (iterationState, error) {
	maxIter := normalizeIterationMaximum(loopCfg.MaxIterations)

	// Get session storage for trace management.
	sessionStorage, err := e.getSessionStorage()
	if err != nil {
		return iterationState{}, fmt.Errorf("get session storage: %w", err)
	}

	trace, err := loadIterationTrace(sessionStorage, loopCfg.TraceID)
	if err != nil {
		return iterationState{}, err
	}

	startIter, resumed, err := restoreIterationTrace(&trace, &loopCfg, &input, out, interaction)
	if err != nil {
		return iterationState{}, err
	}
	if resumed {
		maxIter = loopCfg.MaxIterations
	}

	if trace.TraceID == "" {
		var done bool
		trace, input, done, err = e.createIterationTrace(ctx, loopCfg, input, maxIter, sessionStorage, interaction)
		if err != nil {
			return iterationState{}, err
		}
		if done {
			return iterationState{done: true}, nil
		}
	}
	return iterationState{trace: trace, storage: sessionStorage, start: startIter, maximum: maxIter, stopWord: loopCfg.StopWord, input: input, resumed: resumed}, nil
}

func normalizeIterationMaximum(value int) int {
	if value <= 0 {
		return defaultMaxIterations
	}
	return value
}

func restoreIterationTrace(trace *session.TraceRecord, loopCfg *IterativeLoopConfig, input *agentloop.ExecuteInput, out io.Writer, interaction *IterativeInteraction) (int, bool, error) {
	if trace.TraceID == "" {
		return 1, false, nil
	}
	loopCfg.MaxIterations = trace.Config.MaxIterations
	loopCfg.StopWord = trace.Config.StopWord
	input.Message = trace.Config.Prompt
	start := resumeIteration(trace)
	if interaction == nil {
		if _, err := fmt.Fprintf(out, "[Resuming trace %s from iteration %d/%d]\n", trace.TraceID, start, loopCfg.MaxIterations); err != nil {
			return 0, false, fmt.Errorf("write resume trace banner: %w", err)
		}
	}
	return start, true, nil
}

func (e *Executor) createIterationTrace(ctx context.Context, loopCfg IterativeLoopConfig, input agentloop.ExecuteInput, maxIter int, storage Storage, interaction *IterativeInteraction) (session.TraceRecord, agentloop.ExecuteInput, bool, error) {
	if input.Message == "" && interaction != nil && interaction.InitialPrompt != nil {
		prompt, done, err := interaction.InitialPrompt(ctx)
		if err != nil {
			return session.TraceRecord{}, input, false, err
		}
		if done {
			return session.TraceRecord{}, input, true, nil
		}
		input.Message = prompt
	}
	traceID, err := newTraceID(storage)
	if err != nil {
		return session.TraceRecord{}, input, false, fmt.Errorf("create trace ID: %w", err)
	}
	trace := session.TraceRecord{
		TraceID: traceID, Status: session.TraceStatusRunning,
		Config: session.TraceConfig{MaxIterations: maxIter, StopWord: loopCfg.StopWord, Prompt: input.Message},
	}
	if err := storage.SaveTrace(trace); err != nil {
		return session.TraceRecord{}, input, false, fmt.Errorf("save trace: %w", err)
	}
	return trace, input, false, nil
}

func loadIterationTrace(storage Storage, id string) (session.TraceRecord, error) {
	if id == "" {
		return session.TraceRecord{}, nil
	}
	trace, err := storage.LoadTrace(id)
	if err != nil {
		return session.TraceRecord{}, fmt.Errorf("load trace %s: %w", id, err)
	}
	if trace == nil {
		return session.TraceRecord{}, nil
	}
	return *trace, nil
}

func resumeIteration(trace *session.TraceRecord) int {
	if len(trace.Iterations) == 0 {
		return 1
	}
	last := trace.Iterations[len(trace.Iterations)-1]
	if last.Status != session.IterationStatusInterrupted {
		return last.Iteration + 1
	}
	trace.Iterations = trace.Iterations[:len(trace.Iterations)-1]
	return last.Iteration
}
