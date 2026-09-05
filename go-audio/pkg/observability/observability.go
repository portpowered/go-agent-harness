// Package observability defines the application-owned metric and logging
// seams shared by composition, services, and device implementations.
package observability

import (
	"context"
	"fmt"
)

// Fields carries stable, low-cardinality correlation values. Implementations
// must treat new keys as additive schema growth.
type Fields map[string]string

// MetricSample is one counter, gauge, or measured value sampled at a safe
// application boundary outside a native audio callback.
type MetricSample struct {
	Name   string
	Kind   string
	Value  float64
	Unit   string
	Fields Fields
}

// MetricSampler is the application metric port. MetricSamplerFunc lets tests
// and embedders inject an ordinary function while Wire depends on an interface.
type MetricSampler interface {
	Sample(context.Context, MetricSample) error
}

type MetricSamplerFunc func(context.Context, MetricSample) error

func (f MetricSamplerFunc) Sample(ctx context.Context, sample MetricSample) error {
	if f == nil {
		return nil
	}
	return f(ctx, sample)
}

// LogRecord is one structured application log record.
type LogRecord struct {
	Level   string
	Message string
	Fields  Fields
}

// Logger is the application logging port. LoggerFunc is its function adapter.
type Logger interface {
	Log(context.Context, LogRecord) error
}

type LoggerFunc func(context.Context, LogRecord) error

func (f LoggerFunc) Log(ctx context.Context, record LogRecord) error {
	if f == nil {
		return nil
	}
	return f(ctx, record)
}

// Dependencies carries the two application observability ports through
// service option values without introducing a dependency bag at Wire's named
// composition boundary.
type Dependencies struct {
	MetricSampler MetricSampler
	Logger        Logger
}

func NewDependencies(sampler MetricSampler, logger Logger) Dependencies {
	return Dependencies{MetricSampler: EnsureMetricSampler(sampler), Logger: EnsureLogger(logger)}
}

type noopMetricSampler struct{}

func (noopMetricSampler) Sample(context.Context, MetricSample) error { return nil }

type noopLogger struct{}

func (noopLogger) Log(context.Context, LogRecord) error { return nil }

func NewNoopMetricSampler() MetricSampler { return noopMetricSampler{} }
func NewNoopLogger() Logger               { return noopLogger{} }

func EnsureMetricSampler(sampler MetricSampler) MetricSampler {
	if sampler == nil {
		return NewNoopMetricSampler()
	}
	return sampler
}

func EnsureLogger(logger Logger) Logger {
	if logger == nil {
		return NewNoopLogger()
	}
	return logger
}

// TrySample contains observer failures and panics so instrumentation can never
// change an audio/session result. The returned error is diagnostic only.
func TrySample(ctx context.Context, sampler MetricSampler, sample MetricSample) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("metric sampler panic: %v", recovered)
		}
	}()
	return EnsureMetricSampler(sampler).Sample(ctx, cloneMetricSample(sample))
}

// TryLog provides the same failure isolation for structured logging.
func TryLog(ctx context.Context, logger Logger, record LogRecord) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("logger panic: %v", recovered)
		}
	}()
	return EnsureLogger(logger).Log(ctx, cloneLogRecord(record))
}

func cloneMetricSample(sample MetricSample) MetricSample {
	sample.Fields = cloneFields(sample.Fields)
	return sample
}

func cloneLogRecord(record LogRecord) LogRecord {
	record.Fields = cloneFields(record.Fields)
	return record
}

func cloneFields(fields Fields) Fields {
	if fields == nil {
		return nil
	}
	cloned := make(Fields, len(fields))
	for key, value := range fields {
		cloned[key] = value
	}
	return cloned
}
