package audio_test

import (
	"context"
	"errors"
	"io"
	"reflect"
	"testing"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

func TestRouteEngineDrainsPartialTailThroughBuffers(t *testing.T) {
	format := audio.PCM16DeviceFormat(24000)
	e, ports, err := audio.NewRouteEngine(audio.RouteConfig{Input: format, Output: format, Quantum: 4, BufferFrames: 2, BufferSamples: 8})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- e.Run(context.Background()) }()
	if err := ports.Input.Submit(context.Background(), audio.PCMFrame{Samples: []int16{1, 2, 3, 4, 5}}); err != nil {
		t.Fatal(err)
	}
	ports.Input.Close()
	var samples []int16
	ends := 0
	for {
		f, err := ports.Output.Receive(context.Background())
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		samples = append(samples, f.Samples...)
		if f.EndOfResponse {
			ends++
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(samples, []int16{1, 2, 3, 4, 5}) || ends != 1 {
		t.Fatalf("samples=%v ends=%d", samples, ends)
	}
	if err := e.Run(context.Background()); err == nil {
		t.Fatal("route restarted after close")
	}
}

func TestRoutePortsExposeTheOwnedBufferControls(t *testing.T) {
	format := audio.PCM16DeviceFormat(24000)
	engine, ports, err := audio.NewRouteEngine(audio.RouteConfig{
		Input: format, Output: format, Quantum: 4, BufferFrames: 2, BufferSamples: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	if ports.InputControl.Snapshot() != (audio.BufferStats{CapacityFrames: 2, CapacitySamples: 8}) {
		t.Fatalf("input control snapshot = %+v", ports.InputControl.Snapshot())
	}
	if ports.OutputControl.Snapshot() != (audio.BufferStats{CapacityFrames: 2, CapacitySamples: 8}) {
		t.Fatalf("output control snapshot = %+v", ports.OutputControl.Snapshot())
	}
	in, out := engine.Snapshot()
	if in.CapacityFrames == 0 || out.CapacityFrames == 0 {
		t.Fatalf("route snapshot unexpectedly empty: in=%+v out=%+v", in, out)
	}
}

func TestRouteEngineBlockedConsumerDoesNotPreventInvalidation(t *testing.T) {
	format := audio.PCM16DeviceFormat(24000)
	e, ports, err := audio.NewRouteEngine(audio.RouteConfig{Input: format, Output: format, Quantum: 2, BufferFrames: 1, BufferSamples: 4})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx) }()
	if err := ports.Input.Submit(ctx, audio.PCMFrame{Samples: []int16{1, 2, 3, 4}}); err != nil {
		t.Fatal(err)
	}
	e.Invalidate(1)
	if err := ports.Input.Submit(ctx, audio.PCMFrame{Epoch: 1, Samples: []int16{9, 10}, EndOfResponse: true}); err != nil {
		t.Fatal(err)
	}
	ports.Input.Close()
	var got []int16
	for {
		f, err := ports.Output.Receive(ctx)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if f.Epoch != 1 {
			t.Fatalf("stale output=%+v", f)
		}
		got = append(got, f.Samples...)
	}
	if !reflect.DeepEqual(got, []int16{9, 10}) {
		t.Fatalf("output=%v", got)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	_, out := e.Snapshot()
	if out.Epoch != 1 {
		t.Fatalf("epoch=%d", out.Epoch)
	}
}

func TestRouteEngineCancellationStopsBackpressure(t *testing.T) {
	f := audio.PCM16DeviceFormat(24000)
	e, ports, err := audio.NewRouteEngine(audio.RouteConfig{Input: f, Output: f, Quantum: 1, BufferFrames: 1, BufferSamples: 4})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx) }()
	if err := ports.Input.Submit(ctx, audio.PCMFrame{Samples: []int16{1, 2, 3, 4}}); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("run=%v", err)
	}
}
