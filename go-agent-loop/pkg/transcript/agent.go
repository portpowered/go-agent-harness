package transcript

import (
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// ErrUnsupportedAgentBoundary identifies a live provider boundary that does
// not expose one of the supported byte-oriented contracts.
var ErrUnsupportedAgentBoundary = errors.New("transcript: unsupported agent boundary")

// AgentClock supplies the timestamp used by an agent-side record. A clock
// that also implements AgentTickSource supplies the shared logical tick.
type AgentClock interface {
	Now() time.Time
}

// AgentTickSource supplies the logical tick shared by client- and agent-side
// transcript records.
type AgentTickSource interface {
	Tick() uint64
}

// AgentCaptureOption configures an AgentCapture.
type AgentCaptureOption func(*AgentCapture)

// WithAgentCaptureReporter installs the one-shot callback used when the
// transcript sink fails. The callback is observability-only and never changes
// the result returned by the live provider boundary.
func WithAgentCaptureReporter(reporter func(error)) AgentCaptureOption {
	return func(capture *AgentCapture) {
		capture.reporter = reporter
	}
}

// AgentCapture records the two directions at the provider boundary. DirectionIn
// is traffic arriving at the provider boundary from the client, while
// DirectionOut is traffic leaving it toward the client.
//
// The capture sink is optional. When it is nil, the adapter is transparent and
// calls the live boundary without taking a clock reading or copying data.
type AgentCapture struct {
	sink       RecordSink
	clock      AgentClock
	sequence   atomic.Uint64
	reporter   func(error)
	reportOnce sync.Once
}

// NewAgentCapture creates an agent-side capture adapter. A nil clock uses the
// host clock and a local monotonic sequence for records that do not share an
// external logical tick. Pass the same clock used by the client-side adapter
// when correlating both sides of a deterministic scenario.
func NewAgentCapture(sink RecordSink, clock AgentClock, options ...AgentCaptureOption) *AgentCapture {
	capture := &AgentCapture{sink: sink, clock: clock}
	if capture.clock == nil {
		capture.clock = realAgentClock{}
	}
	for _, option := range options {
		if option != nil {
			option(capture)
		}
	}
	return capture
}

// NewAgentCaptureWithReporter is a convenience constructor for a capture
// adapter with a one-shot sink degradation reporter.
func NewAgentCaptureWithReporter(sink RecordSink, clock AgentClock, reporter func(error)) *AgentCapture {
	return NewAgentCapture(sink, clock, WithAgentCaptureReporter(reporter))
}

// CaptureInbound records one provider-boundary ingress frame before any live
// processing can mutate the caller's payload. Its return value is the sink
// result; use Inbound when sink failures must remain isolated from the live
// path.
func (capture *AgentCapture) CaptureInbound(stream Stream, payload []byte) error {
	return capture.capture(DirectionIn, stream, payload)
}

// CaptureOutbound records one provider-boundary egress frame. Callers should
// invoke it only after the live boundary has accepted the complete payload;
// Outbound provides that acceptance gate for byte-oriented boundaries.
func (capture *AgentCapture) CaptureOutbound(stream Stream, payload []byte) error {
	return capture.capture(DirectionOut, stream, payload)
}

// Inbound records an ingress frame before invoking live. The live argument may
// be a func([]byte) (int, error), func([]byte) error, io.Writer, or an object
// exposing one of the equivalent Write, Consume, Send, or WriteMessage
// methods. Live results are returned unchanged and transcript failures are
// ignored.
func (capture *AgentCapture) Inbound(stream Stream, payload []byte, live any) (int, error) {
	consumer, err := normalizeAgentBoundary(live)
	if err != nil {
		return 0, err
	}
	if capture == nil || capture.sink == nil {
		return callAgentBoundary(consumer, payload, len(payload))
	}
	_ = capture.capture(DirectionIn, stream, payload)
	return callAgentBoundary(consumer, payload, len(payload))
}

// Outbound invokes live first and records the frame through the shared Tee
// only when the complete payload is accepted. Partial and rejected writes are
// returned unchanged but do not become complete transcript crossings.
func (capture *AgentCapture) Outbound(stream Stream, payload []byte, live any) (int, error) {
	consumer, err := normalizeAgentBoundary(live)
	if err != nil {
		return 0, err
	}
	if consumer == nil {
		return 0, nil
	}
	if capture == nil || capture.sink == nil {
		return consumer(payload)
	}

	// Keep the original payload on the live side. NewTee copies it for the
	// transcript before calling live, matching the transparent tee contract.
	record := capture.newRecord(DirectionOut, stream, payload)
	var accepted int
	var liveErr error
	teeLive := RecordConsumerFunc(func(input Record) (int, error) {
		accepted, liveErr = consumer(input.Payload)
		if accepted == len(input.Payload) {
			// Tee counts complete record observations, while the adapter returns
			// the byte count captured above to preserve the live API exactly.
			return 1, liveErr
		}
		return 0, liveErr
	})
	tee := capture.newTee(teeLive)
	_, _ = tee.Write(record)
	return accepted, liveErr
}

// ProviderIngress is a descriptive alias for Inbound.
func (capture *AgentCapture) ProviderIngress(stream Stream, payload []byte, live any) (int, error) {
	return capture.Inbound(stream, payload, live)
}

// ProviderEgress is a descriptive alias for Outbound.
func (capture *AgentCapture) ProviderEgress(stream Stream, payload []byte, live any) (int, error) {
	return capture.Outbound(stream, payload, live)
}

func (capture *AgentCapture) newTee(live RecordConsumer) *Tee {
	if capture.reporter == nil {
		return NewTee(live, capture.sink)
	}
	return NewTee(live, capture.sink, WithTeeReporter(func(err error) {
		capture.reportOnce.Do(func() {
			notifyDegradation(capture.reporter, err)
		})
	}))
}

func (capture *AgentCapture) capture(direction Direction, stream Stream, payload []byte) error {
	if capture == nil || capture.sink == nil {
		return nil
	}
	record := capture.newRecord(direction, stream, payload)
	record.Payload = append([]byte(nil), payload...)
	err := capture.sink.Write(record)
	if err != nil && capture.reporter != nil {
		capture.reportOnce.Do(func() {
			notifyDegradation(capture.reporter, err)
		})
	}
	return err
}

func (capture *AgentCapture) newRecord(direction Direction, stream Stream, payload []byte) Record {
	tick, timestamp := capture.snapshot()
	record := NewRecord(tick, timestamp, PeerAgent, direction, stream, payload)
	// Tee must receive the caller's original payload so the live path has the
	// same mutation/ownership behavior as an uncaptured call. Its transcript
	// side takes an owned copy before invoking live.
	record.Payload = payload
	return record
}

func (capture *AgentCapture) snapshot() (uint64, time.Time) {
	tick := capture.sequence.Add(1)
	clock := capture.clock
	if clock == nil {
		clock = realAgentClock{}
	}
	if source, ok := clock.(AgentTickSource); ok {
		tick = source.Tick()
	}
	return tick, clock.Now()
}

type agentBoundaryConsumer func([]byte) (int, error)

func normalizeAgentBoundary(live any) (agentBoundaryConsumer, error) {
	switch consumer := live.(type) {
	case nil:
		return nil, nil
	case func([]byte) (int, error):
		return consumer, nil
	case func([]byte) error:
		return func(payload []byte) (int, error) {
			if err := consumer(payload); err != nil {
				return 0, err
			}
			return len(payload), nil
		}, nil
	case interface{ Consume([]byte) (int, error) }:
		return consumer.Consume, nil
	case interface{ Consume([]byte) error }:
		return func(payload []byte) (int, error) {
			if err := consumer.Consume(payload); err != nil {
				return 0, err
			}
			return len(payload), nil
		}, nil
	case interface{ Send([]byte) error }:
		return func(payload []byte) (int, error) {
			if err := consumer.Send(payload); err != nil {
				return 0, err
			}
			return len(payload), nil
		}, nil
	case interface{ WriteMessage([]byte) error }:
		return func(payload []byte) (int, error) {
			if err := consumer.WriteMessage(payload); err != nil {
				return 0, err
			}
			return len(payload), nil
		}, nil
	case io.Writer:
		return consumer.Write, nil
	default:
		return nil, ErrUnsupportedAgentBoundary
	}
}

func callAgentBoundary(consumer agentBoundaryConsumer, payload []byte, acceptedWithoutLive int) (int, error) {
	if consumer == nil {
		return acceptedWithoutLive, nil
	}
	return consumer(payload)
}

type realAgentClock struct{}

func (realAgentClock) Now() time.Time { return time.Now() }
