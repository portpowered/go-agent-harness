package rooms

import (
	"context"
	"fmt"
)

// meshSentinel keeps public errors immutable while preserving errors.Is
// identity across the room contract boundary.
type meshSentinel string

func (e meshSentinel) Error() string { return string(e) }

const (
	ErrMeshClosed                 meshSentinel = "room participant mesh is closed"
	ErrMeshEmptyParticipantID     meshSentinel = "room participant ID must not be empty"
	ErrMeshInvalidParticipantID   meshSentinel = "room participant ID contains a control character"
	ErrMeshDuplicateParticipant   meshSentinel = "room participant is already joined"
	ErrMeshUnknownParticipant     meshSentinel = "room participant is not joined"
	ErrMeshPairNotFound           meshSentinel = "room participant pair is not present"
	ErrMeshInvalidPair            meshSentinel = "room participant pair must contain two distinct IDs"
	ErrMeshNilPairResource        meshSentinel = "room pair factory returned a nil resource"
	ErrMeshPairFactoryUnavailable meshSentinel = "room pair factory is unavailable"
)

// Mesh is the pair-neutral participant lifecycle contract. Implementations
// are provided by rooms/wire; mutable membership and cleanup stay private.
type Mesh interface {
	Context() context.Context
	Done() <-chan struct{}
	Join(context.Context, string) error
	AddParticipant(context.Context, string) error
	Remove(string) error
	RemoveParticipant(string) error
	Leave(string) error
	Participants() []string
	ParticipantIDs() []string
	Peers(string) (map[string]PeerView, error)
	RemotePeers(string) (map[string]PeerView, error)
	Pair(string, string) (PairResource, error)
	Pairs() []PairSnapshot
	PairCount() int
	Close() error
}

// MeshError identifies the operation and participants involved in a mesh
// failure without retaining provider, credential, or transport details.
type MeshError struct {
	Operation     string
	ParticipantID string
	RemoteID      string
	Cause         error
}

func (e *MeshError) Error() string {
	if e == nil {
		return "<nil>"
	}
	message := "room mesh"
	if e.Operation != "" {
		message += " " + e.Operation
	}
	if e.ParticipantID != "" {
		message += fmt.Sprintf(" participant %q", e.ParticipantID)
	}
	if e.RemoteID != "" {
		message += fmt.Sprintf(" with %q", e.RemoteID)
	}
	if e.Cause != nil {
		message += ": " + e.Cause.Error()
	}
	return message
}

func (e *MeshError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// PairSpec is the canonical identity of one unordered participant pair.
type PairSpec struct {
	FirstID  string
	SecondID string
}

// PairKey is a graph-oriented alias for PairSpec.
type PairKey = PairSpec

func (s PairSpec) String() string { return fmt.Sprintf("%q<->%q", s.FirstID, s.SecondID) }

// PairResource owns one logical participant pair. Connect establishes it;
// Close releases it and must unblock a pending Connect.
type PairResource interface {
	Connect(context.Context) error
	Close() error
}

// PairFactory creates an inert resource for one canonical PairSpec.
type PairFactory func(context.Context, PairSpec) (PairResource, error)

// MeshConfig supplies explicit lifecycle context and pair construction.
// A nil PairFactory is unavailable; host compatibility defaults belong in
// the CLI adapter, not in the reusable runtime package.
type MeshConfig struct {
	Context     context.Context
	PairFactory PairFactory
}

// PeerView is a participant-local view of one remote pair.
type PeerView struct {
	LocalID  string
	RemoteID string
	Pair     PairResource
}

// PairSnapshot is a stable, read-only pair listing entry.
type PairSnapshot struct {
	Spec     PairSpec
	Resource PairResource
}
