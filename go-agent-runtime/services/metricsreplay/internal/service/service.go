package service

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/metricsreplay"
)

type Dependencies struct {
	Clock   metricsreplay.Clock
	Runner  metricsreplay.ReplayRunner
	Loader  metricsreplay.FixtureLoader
	NewSink metricsreplay.SinkFactory
}

// Service owns the replay orchestration and the independent wire oracle.
type Service struct {
	clock   metricsreplay.Clock
	runner  metricsreplay.ReplayRunner
	loader  metricsreplay.FixtureLoader
	newSink metricsreplay.SinkFactory
}

func New(deps Dependencies) *Service {
	return &Service{clock: deps.Clock, runner: deps.Runner, loader: deps.Loader, newSink: deps.NewSink}
}

func (s *Service) Collect(ctx context.Context, fixture, prompt string) ([]metricsreplay.Series, error) {
	if err := s.validate(ctx, fixture); err != nil {
		return nil, err
	}
	loaded, err := s.loader.Load(ctx, fixture)
	if err != nil {
		return nil, fmt.Errorf("load replay fixture %q: %w", fixture, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("metrics replay canceled after loading fixture: %w", err)
	}
	observed, err := observedDeltaSums(loaded)
	if err != nil {
		return nil, fmt.Errorf("reconcile replay fixture %q: %w", fixture, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("metrics replay canceled before sink construction: %w", err)
	}
	sink, err := s.constructSink()
	if err != nil {
		return nil, err
	}
	return s.run(ctx, fixture, prompt, sink, observed)
}

func (s *Service) validate(ctx context.Context, fixture string) error {
	if s == nil {
		return fmt.Errorf("metrics replay service is nil")
	}
	if isNilValue(ctx) {
		return fmt.Errorf("metrics replay requires a non-nil context")
	}
	if isNilValue(s.clock) {
		return metricsreplay.ErrMissingClock
	}
	if isNilValue(s.runner) {
		return metricsreplay.ErrMissingRunner
	}
	if isNilValue(s.loader) {
		return metricsreplay.ErrMissingLoader
	}
	if s.newSink == nil {
		return metricsreplay.ErrMissingSinkFactory
	}
	if strings.TrimSpace(fixture) == "" {
		return metricsreplay.ErrMissingFixture
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("metrics replay canceled before loading fixture: %w", err)
	}
	return nil
}

func (s *Service) constructSink() (metricsreplay.Sink, error) {
	sink, err := s.newSink()
	if err != nil {
		if !isNilSink(sink) {
			if closeErr := sink.Close(); closeErr != nil {
				err = errors.Join(err, fmt.Errorf("close metrics sink after construction failure: %w", closeErr))
			}
		}
		return nil, fmt.Errorf("construct metrics sink: %w", err)
	}
	if isNilSink(sink) {
		return nil, fmt.Errorf("construct metrics sink: factory returned nil sink")
	}
	return sink, nil
}

func (s *Service) run(ctx context.Context, fixture, prompt string, sink metricsreplay.Sink, observed map[seriesKey]int64) (result []metricsreplay.Series, returnErr error) {
	defer func() {
		if closeErr := sink.Close(); closeErr != nil {
			closeErr = fmt.Errorf("close metrics sink: %w", closeErr)
			if returnErr == nil {
				returnErr = closeErr
			} else {
				returnErr = errors.Join(returnErr, closeErr)
			}
		}
	}()
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("metrics replay canceled before run: %w", err)
	}
	if err := s.runner.Run(ctx, metricsreplay.RunRequest{Fixture: fixture, Prompt: prompt, Clock: s.clock, Recorder: sink}); err != nil {
		return nil, fmt.Errorf("replay %q for metrics: %w", fixture, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("metrics replay canceled after run: %w", err)
	}
	snapshot, err := sink.Snapshot()
	if err != nil {
		return nil, fmt.Errorf("snapshot metrics sink: %w", err)
	}
	return project(snapshot, observed)
}

func isNilSink(sink metricsreplay.Sink) bool {
	return isNilValue(sink)
}

func isNilValue(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	kind := reflected.Kind()
	nilable := kind == reflect.Chan || kind == reflect.Func || kind == reflect.Interface || kind == reflect.Map || kind == reflect.Pointer || kind == reflect.Slice
	return nilable && reflected.IsNil()
}

var _ metricsreplay.Service = (*Service)(nil)
