package service

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

type diagnosticFanout []sessiontrace.DiagnosticSink

type cancellationIntent struct{ sigint atomic.Bool }

func NewCancellationIntent() sessiontrace.CancellationIntent {
	return &cancellationIntent{}
}

func (i *cancellationIntent) MarkSIGINT() {
	if i != nil {
		i.sigint.Store(true)
	}
}

func (i *cancellationIntent) SIGINTReceived() bool {
	return i != nil && i.sigint.Load()
}

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

func wrapAudioSource(source audio.AudioSource, rate int, observer sessiontrace.CaptureSamplesObserver) audio.AudioSource {
	if source == nil || observer == nil {
		return source
	}
	if rate <= 0 {
		rate = audio.SampleRate
	}
	if sampleSource, ok := source.(audio.SampleSource); ok {
		return &traceSampleSource{source: sampleSource, rate: rate, observer: observer}
	}
	return &traceAudioSource{source: source, rate: rate, observer: observer}
}

type traceAudioSource struct {
	source   audio.AudioSource
	rate     int
	observer sessiontrace.CaptureSamplesObserver
}

func (s *traceAudioSource) ReadFrame(ctx context.Context, buf []int16) error {
	if s == nil || s.source == nil {
		return io.EOF
	}
	if err := s.source.ReadFrame(ctx, buf); err != nil {
		return err
	}
	if len(buf) > 0 && s.observer != nil {
		s.observer(s.rate, append([]int16(nil), buf...))
	}
	return nil
}

func (s *traceAudioSource) Close() error {
	if s == nil || s.source == nil {
		return nil
	}
	return s.source.Close()
}

type traceSampleSource struct {
	source   audio.SampleSource
	rate     int
	observer sessiontrace.CaptureSamplesObserver
}

func (s *traceSampleSource) ReadFrame(ctx context.Context, buf []int16) error {
	if s == nil || s.source == nil {
		return io.EOF
	}
	if err := s.source.ReadFrame(ctx, buf); err != nil {
		return err
	}
	if len(buf) > 0 && s.observer != nil {
		s.observer(s.rate, append([]int16(nil), buf...))
	}
	return nil
}

func (s *traceSampleSource) ReadSamples(ctx context.Context, buf []int16) (int, error) {
	if s == nil || s.source == nil {
		return 0, io.EOF
	}
	count, err := s.source.ReadSamples(ctx, buf)
	if count > 0 && count <= len(buf) && s.observer != nil {
		s.observer(s.rate, append([]int16(nil), buf[:count]...))
	}
	return count, err
}

func (s *traceSampleSource) Close() error {
	if s == nil || s.source == nil {
		return nil
	}
	return s.source.Close()
}

var _ audio.AudioSource = (*traceAudioSource)(nil)
var _ audio.SampleSource = (*traceSampleSource)(nil)
