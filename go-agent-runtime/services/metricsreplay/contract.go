// Package metricsreplay exposes the host-neutral metrics reconciliation
// service used by credential-free replay probes.
package metricsreplay

import (
	"context"
	"encoding/json"
	"time"
)

type errorCode string

func (e errorCode) Error() string { return string(e) }

const (
	ErrMissingClock       errorCode = "metrics replay clock is required"
	ErrMissingRunner      errorCode = "metrics replay runner is required"
	ErrMissingLoader      errorCode = "metrics replay fixture loader is required"
	ErrMissingSinkFactory errorCode = "metrics replay sink factory is required"
	ErrMissingFixture     errorCode = "metrics replay fixture path is required"
	ErrMalformedFixture   errorCode = "metrics replay fixture is malformed"
	ErrInvalidSnapshot    errorCode = "metrics replay sink snapshot is invalid"
)

// Clock is the timestamp source supplied to the replay runner. The service
// never falls back to a package or process clock.
type Clock interface {
	Now() time.Time
}

// Direction identifies the semantic direction of a counted stream.
type Direction string

const (
	DirectionInput  Direction = "input"
	DirectionOutput Direction = "output"
)

func (d Direction) Valid() bool {
	return d == DirectionInput || d == DirectionOutput
}

// Modality identifies the kind of counted data.
type Modality string

const (
	ModalityAudio Modality = "audio"
	ModalityText  Modality = "text"
	ModalityImage Modality = "image"
	ModalityTool  Modality = "tool"
)

func (m Modality) Valid() bool {
	switch m {
	case ModalityAudio, ModalityText, ModalityImage, ModalityTool:
		return true
	default:
		return false
	}
}

// WireDirection identifies the side of a captured provider event.
type WireDirection string

const (
	WireDirectionClientToServer WireDirection = "client_to_server"
	WireDirectionServerToClient WireDirection = "server_to_client"
)

func (d WireDirection) Valid() bool {
	return d == WireDirectionClientToServer || d == WireDirectionServerToClient
}

const (
	// PayloadTypeWebSocketMessage is the raw provider JSON representation.
	PayloadTypeWebSocketMessage = "websocket_message"
	// PayloadTypeStreamMessage is the normalized stream-message envelope.
	PayloadTypeStreamMessage = "stream_message"
)

// Record is the small immutable fixture view needed by the independent wire
// oracle. The loader owns conversion from any on-disk capture envelope.
type Record struct {
	Sequence    int
	Direction   WireDirection
	Type        string
	PayloadType string
	Payload     json.RawMessage
}

// Fixture is the loader result consumed by the independent wire oracle.
type Fixture struct {
	Records []Record
}

// Series is the public reconciliation result. A zero-valued series is
// meaningful and is retained in the deterministic projection.
type Series struct {
	Direction      Direction
	Modality       Modality
	ObservedDeltas int64
	ReportedTotal  int64
}

// SnapshotSeries is the sink's emitted total for one direction/modality key.
type SnapshotSeries struct {
	Direction  Direction
	Modality   Modality
	TotalBytes int64
}

// Snapshot is the read-only sink view needed by the service.
type Snapshot struct {
	Series []SnapshotSeries
}

// Recorder is the write-only observation seam passed to a replay runner.
type Recorder interface {
	Record(Direction, Modality, int64) error
}

// Sink owns one invocation's emitted metrics. Close is explicit so a host
// implementation can release resources even when replay or reconciliation
// fails.
type Sink interface {
	Recorder
	Snapshot() (Snapshot, error)
	Close() error
}

// SinkFactory constructs one invocation-scoped sink.
type SinkFactory func() (Sink, error)

// FixtureLoader loads and validates the capture envelope outside the runner.
type FixtureLoader interface {
	Load(context.Context, string) (Fixture, error)
}

// RunRequest is the host-neutral replay invocation contract.
type RunRequest struct {
	Fixture  string
	Prompt   string
	Clock    Clock
	Recorder Recorder
}

// ReplayRunner drives the production replay path with the service-owned
// recorder and clock.
type ReplayRunner interface {
	Run(context.Context, RunRequest) error
}

// Dependencies contains every side effect needed by the private service.
// Hosts and embedders can provide deterministic fakes without importing the
// CLI or its private runtime packages.
type Dependencies struct {
	Clock   Clock
	Runner  ReplayRunner
	Loader  FixtureLoader
	NewSink SinkFactory
}

// Service reconciles emitted metrics from one replay with an independent
// wire-level sum of the same fixture.
type Service interface {
	Collect(context.Context, string, string) ([]Series, error)
}
