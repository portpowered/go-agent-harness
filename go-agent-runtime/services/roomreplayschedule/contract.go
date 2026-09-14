// Package roomreplayschedule constructs and runs deterministic room replay
// schedules without depending on a CLI, provider, or device implementation.
package roomreplayschedule

import (
	"context"
	"time"
)

type errorCode string

func (e errorCode) Error() string { return string(e) }

const (
	// ErrInvalidRequest identifies an incomplete or internally inconsistent
	// replay request.
	ErrInvalidRequest errorCode = "room replay schedule request is invalid"
	// ErrInvalidFormat identifies a PCM format that cannot produce aligned
	// signed PCM16 frames.
	ErrInvalidFormat errorCode = "room replay schedule PCM format is invalid"
	// ErrParticipantMissing identifies a target without an admitted participant
	// source.
	ErrParticipantMissing errorCode = "room replay participant is missing"
	// ErrCaptureUnavailable identifies a target capture that cannot be loaded.
	ErrCaptureUnavailable errorCode = "room replay participant capture is unavailable"
	// ErrSentPCMUnavailable identifies a participant whose sent PCM artifact is
	// absent or unreadable.
	ErrSentPCMUnavailable errorCode = "room replay sent PCM is unavailable"
	// ErrInvalidPCM identifies malformed or unsupported source PCM.
	ErrInvalidPCM errorCode = "room replay PCM is invalid"
	// ErrTargetMissing identifies a target required by a schedule but absent
	// from a run request.
	ErrTargetMissing errorCode = "room replay target is missing"
	// ErrTargetUncontrolled identifies a target without the callbacks required
	// for deterministic scheduling.
	ErrTargetUncontrolled errorCode = "room replay target is not scheduler-controlled"
	// ErrTargetInactive identifies a target that stopped before a logical frame
	// could be released.
	ErrTargetInactive errorCode = "room replay target is inactive"
	// ErrTargetStopped identifies a target acknowledgement or advancement that
	// terminated before accepting a logical frame.
	ErrTargetStopped errorCode = "room replay target stopped"
)

// PCM16Format is the target format and cadence used by a replay schedule.
// PCM samples are signed little-endian 16-bit values with interleaved
// channels.
type PCM16Format struct {
	SampleRate    int
	Channels      int
	FrameDuration time.Duration
}

// SourcePCM16Format describes the raw signed PCM16 format of sent artifacts.
// SampleWidthBit is retained as a compatibility alias for older manifests.
type SourcePCM16Format struct {
	SampleRate      int
	Channels        int
	SampleWidthBits int
	SampleWidthBit  int
	ByteOrder       string
	Encoding        string
}

// Participant identifies one admitted replay participant and its file-backed
// evidence. CapturePath is used to determine the provider frame barrier;
// SentPCMPath supplies the bytes released into target mixers.
type Participant struct {
	ID          string
	CapturePath string
	SentPCMPath string
}

// TimelineEvent is a lossless room-timeline projection used to place speech
// segments on logical mixer frames.
type TimelineEvent struct {
	Sequence      int64
	OffsetMS      int64
	OffsetNanos   int64
	Type          string
	ParticipantID string
}

// BuildRequest contains only validated, credential-free replay inputs.
type BuildRequest struct {
	SourceFormat SourcePCM16Format
	TargetFormat PCM16Format
	Participants []Participant
	Timeline     []TimelineEvent
	TargetIDs    []string
}

// Service builds deterministic schedules from admitted replay evidence.
type Service interface {
	Build(context.Context, BuildRequest) (Schedule, error)
}

// Schedule releases every logical frame in deterministic order and waits for
// each target's provider acknowledgement before advancing.
type Schedule interface {
	Run(context.Context, RunRequest) error
}

// Target is the narrow adapter-owned control surface for one provider mixer.
// Active must report whether the target can still accept a frame. Release and
// Advance must honor the context; AwaitAcknowledgement must return after the
// corresponding provider frame is accepted.
type Target struct {
	ID                   string
	Active               func() bool
	Release              func(context.Context, string, []byte) error
	Advance              func(context.Context) error
	AwaitAcknowledgement func(context.Context) error
}

// Waiter identifies a participant completion barrier. A nil Done channel is
// ignored so adapters can omit completion for synthetic targets.
type Waiter struct {
	ID   string
	Done <-chan struct{}
}

// RunRequest supplies adapter callbacks and final participant barriers.
type RunRequest struct {
	Targets        []Target
	WaitFor        []Waiter
	IsStopping     func() bool
	OnContribution func(Contribution)
}

// Contribution describes one immutable source-to-target release. PCM is a
// private copy owned by the callback invocation.
type Contribution struct {
	Frame    int
	SourceID string
	TargetID string
	PCM      []byte
}
