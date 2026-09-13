package rooms

import (
	"context"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

// RoomBoundCancelledClassification is the stable classification for a
// response deliberately cancelled by the room after its bound grace period.
// It is part of the room contract rather than a provider taxonomy.
const RoomBoundCancelledClassification = "room_bound_cancelled"

// ParticipantLifecycleOptions wires the invocation-owned progress signal and
// the admission boundary into one participant lifecycle. The channel is only
// a wake-up hint; lifecycle state remains synchronized internally.
type ParticipantLifecycleOptions struct {
	StateChanged    chan<- struct{}
	AdmissionClosed <-chan struct{}
}

// ParticipantLivenessMetadata carries already-classified, provider-neutral
// facts from a host's liveness detector.
type ParticipantLivenessMetadata struct {
	Classification     string
	TerminalReason     messages.TerminalReason
	TerminalProvenance messages.TerminalProvenance
	OutputState        messages.TerminalOutputState
}

// SessionTerminalObservation is the bounded terminal observation exchanged
// between a session observer and the participant lifecycle. Err remains
// in-process so callers can preserve causal identity through cleanup.
type SessionTerminalObservation struct {
	ResponseID         string
	Classification     string
	TerminalReason     string
	TerminalProvenance string
	OutputState        string
	Err                error
	Failure            bool
	RoomBound          bool
	Code               string
	FailingEvent       string
}

// ParticipantTerminalObservation is the immutable-shaped result of the
// participant terminal ledger. Empty fields are filled by the lifecycle when
// a bound or transport supplies the missing context.
type ParticipantTerminalObservation struct {
	TerminationTrigger     string
	TerminationDisposition string
	Classification         string
	TerminalReason         string
	TerminalProvenance     string
	OutputState            string
	Err                    error
	Failure                bool
}

// ParticipantLifecycleSnapshot is the progress snapshot used by a room host
// while assembling a participant result.
type ParticipantLifecycleSnapshot struct {
	Connected      bool
	SessionOpened  bool
	SessionClosed  bool
	CloseReason    string
	TerminalReason messages.TerminalReason
	Turns          int
	ConnectErr     error
}

// ParticipantOwnedSessionSnapshot describes the session ownership boundary
// independently from the provider transport's terminal observation.
type ParticipantOwnedSessionSnapshot struct {
	Created       bool
	Closed        bool
	TransportDone <-chan struct{}
	CloseErr      error
}

// ParticipantLifecycle owns one participant's connection, readiness,
// response/tool admission, terminal classification, and cleanup state. The
// public room contract intentionally contains no device, provider, or CLI
// types; hosts adapt their local result vocabulary at the edge.
type ParticipantLifecycle interface {
	MarkConnected(error)
	MarkDeviceReady()
	DeviceHasReady() bool
	MarkSessionCreated()
	SetOwnedSession(TrackedSession)
	CloseOwnedSession() error
	MarkOwnedSessionClosed(error)
	MarkParticipantFailure(error)
	MarkLivenessFailure(error, ParticipantLivenessMetadata)
	RecordToolResultSend(string, bool, bool)
	RecordToolContinuationRequest(bool)
	RecordResponseCancellation()
	CancelActiveResponse()
	AdmitResponseTerminal() bool
	AdmitSessionMessageAfterBound(messages.StreamMessage) bool
	AdmitCompleteToolResultAfterBound(messages.Message) bool
	ObserveTerminal(SessionTerminalObservation) bool
	Observe(messages.StreamMessage) int
	ObserveAdmittedTurn() int
	MarkTransportEndedWithError(error)
	SetTransportDone(<-chan struct{}, func() error)
	TransportHasEnded() bool
	TransportTerminalError() error
	MarkCoordinatorStopping(bool, ...RoomTerminationReason)
	MarkBoundCancellation()
	MarkRunDone(error)
	RunHasFinished() bool
	Snapshot() ParticipantLifecycleSnapshot
	Terminal() (ParticipantTerminationReason, error, bool)
	TerminalMetadata() (string, messages.TerminalReason, messages.TerminalProvenance, messages.TerminalOutputState)
	TerminalObservationSnapshot() ParticipantTerminalObservation
	OwnedSessionSnapshot() ParticipantOwnedSessionSnapshot
	ToolCallID(messages.StreamMessage) string
}

// TrackedSession keeps admission and close ownership attached to a provider
// session while preserving the optional session capabilities used by image and
// audio-only tool continuations.
type TrackedSession interface {
	messages.Session
	SessionAdmissionClosed() bool
	SessionAdmissionAllows(messages.StreamMessage) bool
	SessionAdmissionAllowsCompleteMessage(messages.Message) bool
	messages.SessionSendOutcomeSender
	messages.SessionResponseRequester
	messages.SessionResponseCapability
	SendMessage(context.Context, messages.Message) bool
	SendMessageWithoutResponse(context.Context, messages.Message) bool
	SupportsCompleteMessages() bool
	SupportsCompleteMessagesWithoutResponse() bool
	RTCMedia() (audio.MediaEndpoints, bool)
	TerminalError() error
}

// ParticipantConnectionTracker preserves SessionInferencer while publishing
// its first connection outcome to the room admission barrier.
type ParticipantConnectionTracker interface {
	messages.SessionInferencer
	SetOutcomeSink(func(error))
	Outcome() (error, bool)
}
