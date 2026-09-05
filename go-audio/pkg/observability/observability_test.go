package observability

import (
	"context"
	"errors"
	"testing"
)

func TestFunctionAdaptersAndNoops(t *testing.T) {
	want := errors.New("observer failed")
	if got := (MetricSamplerFunc(func(context.Context, MetricSample) error { return want })).Sample(context.Background(), MetricSample{}); !errors.Is(got, want) {
		t.Fatalf("MetricSamplerFunc error = %v, want %v", got, want)
	}
	if got := (LoggerFunc(func(context.Context, LogRecord) error { return want })).Log(context.Background(), LogRecord{}); !errors.Is(got, want) {
		t.Fatalf("LoggerFunc error = %v, want %v", got, want)
	}
	if err := NewNoopMetricSampler().Sample(context.Background(), MetricSample{}); err != nil {
		t.Fatalf("noop metric sampler: %v", err)
	}
	if err := NewNoopLogger().Log(context.Background(), LogRecord{}); err != nil {
		t.Fatalf("noop logger: %v", err)
	}
	if dependencies := NewDependencies(nil, nil); dependencies.MetricSampler == nil || dependencies.Logger == nil {
		t.Fatalf("nil dependencies were not defaulted: %+v", dependencies)
	}
	sampler := MetricSamplerFunc(func(context.Context, MetricSample) error { return nil })
	logger := LoggerFunc(func(context.Context, LogRecord) error { return nil })
	dependencies := NewDependencies(sampler, logger)
	if _, ok := dependencies.MetricSampler.(MetricSamplerFunc); !ok {
		t.Fatalf("sampler identity type = %T", dependencies.MetricSampler)
	}
	if _, ok := dependencies.Logger.(LoggerFunc); !ok {
		t.Fatalf("logger identity type = %T", dependencies.Logger)
	}
}

func TestTryObserversContainPanicsAndDefensivelyCopyFields(t *testing.T) {
	fields := Fields{"device_id": "simulated:output"}
	metricErr := TrySample(context.Background(), MetricSamplerFunc(func(_ context.Context, sample MetricSample) error {
		sample.Fields["device_id"] = "mutated"
		panic("metric boom")
	}), MetricSample{Fields: fields})
	if metricErr == nil || fields["device_id"] != "simulated:output" {
		t.Fatalf("TrySample error=%v fields=%v", metricErr, fields)
	}
	logErr := TryLog(context.Background(), LoggerFunc(func(_ context.Context, record LogRecord) error {
		record.Fields["device_id"] = "mutated"
		panic("log boom")
	}), LogRecord{Fields: fields})
	if logErr == nil || fields["device_id"] != "simulated:output" {
		t.Fatalf("TryLog error=%v fields=%v", logErr, fields)
	}
}
