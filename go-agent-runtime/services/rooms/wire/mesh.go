package wire

import (
	"context"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms/internal/mesh"
)

// NewMesh constructs the pair-neutral room mesh from explicit dependencies.
// The lifecycle implementation remains private to the runtime module.
func NewMesh(config rooms.MeshConfig) rooms.Mesh { return mesh.NewMesh(config) }

// NewParticipantMesh is the concise composition-root constructor used by the
// CLI adapter and standalone runtime consumers.
func NewParticipantMesh(ctx context.Context, factory rooms.PairFactory) rooms.Mesh {
	return NewMesh(rooms.MeshConfig{Context: ctx, PairFactory: factory}) //nolint:contextcheck // MeshConfig carries the caller context as an explicit dependency.
}

// NewPairSpec normalizes and orders an unordered pair deterministically.
func NewPairSpec(firstID, secondID string) (rooms.PairSpec, error) {
	return mesh.NewPairSpec(firstID, secondID)
}
