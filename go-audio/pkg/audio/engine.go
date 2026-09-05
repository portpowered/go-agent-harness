package audio

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
)

// RouteConfig declares one signal path. Each intermediate queue is bounded
// by both samples and frames; this allocation is session-scoped.
type RouteConfig struct {
	Input, Output               DeviceFormat
	Quantum                     int
	BufferFrames, BufferSamples int
}

// RoutePorts exposes only memory buffers. Device and provider workers live
// outside this engine and possess the actual I/O capabilities.
type RoutePorts struct {
	Input         FrameProducer
	Output        FrameConsumer
	InputControl  BufferControl
	OutputControl BufferControl
}

// RouteEngine executes one stream on an independent media worker. It never
// calls into a device, provider, loop tick, tool executor or recording sink.
// Interrupt affects memory queues; the owning runtime separately commands
// its device worker and correlates the applied/consumed receipts.
type RouteEngine struct {
	processor             *Processor
	input                 FrameConsumer
	output                FrameProducer
	inControl, outControl BufferControl
	mu                    sync.Mutex
	running               bool
	finished              bool
	processorEpoch        uint64
}

func NewRouteEngine(config RouteConfig) (*RouteEngine, RoutePorts, error) {
	processor, err := NewProcessor(config.Input, config.Output, config.Quantum)
	if err != nil {
		return nil, RoutePorts{}, err
	}
	if config.BufferSamples < config.Quantum {
		return nil, RoutePorts{}, fmt.Errorf("audio route buffer must hold one output quantum")
	}
	ip, ic, ictl, err := NewFrameBuffer(config.BufferFrames, config.BufferSamples)
	if err != nil {
		return nil, RoutePorts{}, err
	}
	op, oc, octl, err := NewFrameBuffer(config.BufferFrames, config.BufferSamples)
	if err != nil {
		return nil, RoutePorts{}, err
	}
	return &RouteEngine{processor: processor, input: ic, output: op, inControl: ictl, outControl: octl}, RoutePorts{
		Input: ip, Output: oc, InputControl: ictl, OutputControl: octl,
	}, nil
}

// Run is called once by the runtime supervisor, never synchronously in a tick.
// Normal ingress close drains the processor tail. Cancellation discards work
// by stopping admission; a normal close does not invent or drop tail samples.
func (e *RouteEngine) Run(ctx context.Context) (runErr error) {
	e.mu.Lock()
	if e.running || e.finished {
		e.mu.Unlock()
		return errors.New("audio route can only run once")
	}
	e.running = true
	e.mu.Unlock()
	defer func() {
		FrameProducer{q: e.input.q}.Close()
		if runErr != nil {
			e.Invalidate(e.outControl.Snapshot().Epoch + 1)
			_, _ = e.processor.Reset()
		}
		e.output.Close()
		e.mu.Lock()
		e.running = false
		e.finished = true
		e.mu.Unlock()
	}()
	var last PCMFrame
	ended := true
	for {
		frame, err := e.input.Receive(ctx)
		if err != nil {
			if err == io.EOF && !ended {
				last.Samples = nil
				last.EndOfResponse = true
				if flushErr := e.process(ctx, last); flushErr != nil && !errors.Is(flushErr, ErrStaleEpoch) {
					return flushErr
				}
			}
			if err == io.EOF {
				return nil
			}
			return err
		}
		if err := e.process(ctx, frame); err != nil && !errors.Is(err, ErrStaleEpoch) {
			return err
		}
		last, ended = frame, frame.EndOfResponse
	}
}

func (e *RouteEngine) process(ctx context.Context, frame PCMFrame) error {
	// The media worker is the sole mutable DSP owner. Invalidation only
	// touches buffer state, and therefore cannot race filter mutation.
	if frame.Epoch < e.outControl.Snapshot().Epoch {
		return ErrStaleEpoch
	}
	if frame.Epoch != e.processorEpoch || e.processor.ended {
		if _, err := e.processor.Reset(); err != nil {
			return err
		}
		e.processorEpoch = frame.Epoch
	}
	frames, err := e.processor.Process(frame)
	if err != nil {
		return err
	}
	for _, out := range frames {
		if err := e.output.Submit(ctx, out); err != nil {
			return err
		}
	}
	return nil
}

// Invalidate performs a nonblocking memory control operation. It cannot stop
// hardware playback; runtime device workers must consume a separate command.
// The returned count is queued samples, excluding any in-flight DSP state.
func (e *RouteEngine) Invalidate(epoch uint64) int {
	// Output first ensures an old in-flight frame cannot be re-admitted
	// after ingress is invalidated while the worker is processing it.
	n := e.outControl.Invalidate(epoch)
	return n + e.inControl.Invalidate(epoch)
}

func (e *RouteEngine) Snapshot() (ingress, egress BufferStats) {
	return e.inControl.Snapshot(), e.outControl.Snapshot()
}
