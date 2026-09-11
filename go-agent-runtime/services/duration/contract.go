// Package duration defines the host-neutral bounded-session contracts.
//
// Concrete lifecycle, timer admission, artifact, and Wire composition
// implementations remain below the package's private boundary.
package duration

import (
	"context"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

const (
	MaxDurationReason     messages.TerminalReason = "max_duration"
	DefaultDrainPeriod                            = 25 * time.Millisecond
	DefaultCleanupTimeout                         = 5 * time.Second
)

type sentinelError string

const (
	ErrInvalidMaxDuration sentinelError = "session max duration must be non-negative"
	ErrClockRequired      sentinelError = "duration clock is required for bounded sessions"
)

func (e sentinelError) Error() string { return string(e) }

type InvalidDurationError struct{ Duration time.Duration }

func (e *InvalidDurationError) Error() string {
	if e == nil {
		return ErrInvalidMaxDuration.Error()
	}
	return e.Duration.String() + ": " + ErrInvalidMaxDuration.Error()
}

func (e *InvalidDurationError) Unwrap() error { return ErrInvalidMaxDuration }

type Clock interface {
	NewTimer(time.Duration) platformclock.Timer
}

type Runner interface {
	Start(context.Context, messages.SessionInferencer) (Handle, error)
}

type Handle interface {
	Deltas() *messages.TypedBuffer[messages.StreamMessage]
	Result() <-chan error
	SendClose(context.Context) error
	Stop(context.Context) error
}

type ArtifactLifecycle interface {
	Accept(messages.StreamMessage) error
	Flush() error
	Close() error
}

type ArtifactPaths struct {
	AudioPath      string
	TranscriptPath string
}

type AudioSink interface {
	WriteSamples([]int16) error
	Flush() error
	Close() error
}

type TranscriptSink interface {
	Write(transcript.Record) error
	Flush() error
	Close() error
}

type TerminalRecorder interface {
	RecordTerminalSummary(transcript.RecordingTerminalSummary) error
}

type TerminalSummaryDecoder struct{}

func (TerminalSummaryDecoder) FromMessage(msg messages.StreamMessage) (*transcript.RecordingTerminalSummary, bool, error) {
	if msg.Type != messages.StreamTypeSessionClose {
		return nil, false, nil
	}
	value, ok := msg.Value.(*messages.SessionCloseValue)
	if !ok || value == nil || !hasTerminalMetadata(value) {
		return nil, false, nil
	}
	summary := &transcript.RecordingTerminalSummary{
		Reason:             value.Reason,
		Classification:     value.Classification,
		TerminalReason:     value.TerminalReason,
		TerminalProvenance: value.TerminalProvenance,
		OutputState:        value.OutputState,
	}
	if err := summary.Validate(); err != nil {
		return nil, false, err
	}
	return summary, true, nil
}

type MessageState struct {
	DurationExpired  bool
	TerminalWritten  bool
	ResponseOutput   bool
	ResponseComplete bool
	Deadline         <-chan time.Time
}

type MessageResult struct {
	Stop    bool
	Planned bool
}

type MessageHandler func(context.Context, messages.StreamMessage, MessageState) (MessageResult, error)

type RunRequest struct {
	MaxDuration          time.Duration
	Inferencer           messages.SessionInferencer
	Runner               Runner
	Clock                Clock
	Artifacts            ArtifactLifecycle
	ArtifactPaths        *ArtifactPaths
	TerminalRecorder     TerminalRecorder
	Handle               MessageHandler
	Quiesce              func() error
	DrainPeriod          time.Duration
	CleanupTimeout       time.Duration
	OnFinish             func(planned bool, outputState messages.TerminalOutputState)
	OnArtifactsFinalized func(error)
}

type Service interface {
	Run(context.Context, RunRequest) error
}

func hasTerminalMetadata(value *messages.SessionCloseValue) bool {
	return value.Classification != "" || value.TerminalReason != "" || value.TerminalProvenance != "" || value.OutputState != ""
}
