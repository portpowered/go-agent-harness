package service

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

type diagnosticFanout []sessiontrace.DiagnosticSink

func CombineDiagnosticSinks(sinks ...sessiontrace.DiagnosticSink) sessiontrace.DiagnosticSink {
	filtered := make(diagnosticFanout, 0, len(sinks))
	for _, sink := range sinks {
		if sink != nil {
			filtered = append(filtered, sink)
		}
	}
	if len(filtered) == 0 {
		return nil
	}
	if len(filtered) == 1 {
		return filtered[0]
	}
	return filtered
}

func (f diagnosticFanout) RecordSessionDiagnostic(record sessiontrace.DiagnosticRecord) {
	for _, sink := range f {
		if sink != nil {
			sink.RecordSessionDiagnostic(record)
		}
	}
}

func MergeErrorChannels(ctx context.Context, first, second <-chan error) <-chan error {
	if first == nil {
		return second
	}
	if second == nil {
		return first
	}
	merged := make(chan error, 1)
	mergeContext, stop := context.WithCancel(ctx)
	var workers sync.WaitGroup
	forward := func(source <-chan error) {
		defer workers.Done()
		for err := range source {
			if err == nil {
				continue
			}
			select {
			case merged <- err:
				stop()
			case <-mergeContext.Done():
			}
			return
		}
	}
	workers.Add(2)
	go forward(first)
	go forward(second)
	go func() {
		workers.Wait()
		close(merged)
		stop()
	}()
	return merged
}

func NewCancellationIntent() *sessiontrace.CancellationIntent {
	return &sessiontrace.CancellationIntent{}
}

type sourceLivenessClock struct{ source clock.TimerSource }

func (c sourceLivenessClock) NewTimer(duration time.Duration) sessiontrace.LivenessTimer {
	return c.source.NewTimer(duration)
}

func LivenessClockFromSource(source clock.Source) sessiontrace.LivenessClock {
	source = clock.Ensure(source)
	timerSource, ok := source.(clock.TimerSource)
	if !ok {
		return nil
	}
	return sourceLivenessClock{source: timerSource}
}

func LivenessMetadata(err error) (string, messages.TerminalReason, messages.TerminalProvenance, messages.TerminalOutputState) {
	var value *sessiontrace.LivenessError
	if !errors.As(err, &value) || value == nil {
		return "", "", "", ""
	}
	return value.Classification, value.TerminalReason, value.TerminalProvenance, value.OutputState
}

func OutputStateForProgress(open bool, turns int) string {
	if !open || turns == 0 {
		return string(messages.TerminalOutputNone)
	}
	return string(messages.TerminalOutputPartial)
}
