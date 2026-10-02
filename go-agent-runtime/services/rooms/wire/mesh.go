package wire

import (
	"context"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms/internal/mesh"
)

// NewMesh constructs the pair-neutral room mesh from explicit dependencies;
// it closes when ctx ends. The lifecycle implementation remains private to
// the runtime module.
func NewMesh(ctx context.Context, config rooms.MeshConfig) (rooms.Mesh, error) {
	return mesh.NewMesh(ctx, config)
}

// NewPairSpec normalizes and orders an unordered pair deterministically.
func NewPairSpec(firstID, secondID string) (rooms.PairSpec, error) {
	return mesh.NewPairSpec(firstID, secondID)
}
