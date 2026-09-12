package agentruntime

import (
	"context"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
)

// Deprecated: room participant classification is retained only as a legacy
// orchestration adapter; roomplanning owns construction policy.
func roomParticipantIsHuman(plan *roomParticipantPlan) bool {
	return roomParticipantIsHumanAdapter(plan)
}

// Deprecated: callers remain source-compatible while roomplanning owns plan construction.
func buildRoomParticipantPlans(opts RoomRunOptions, validation room.ValidationOptions, evidences ...*roomEvidence) ([]*roomParticipantPlan, []string, error) {
	return buildRoomParticipantPlansWithContext(context.Background(), opts, validation, evidences...)
}

// Deprecated: callers remain source-compatible while roomplanning owns plan construction.
func buildRoomParticipantPlansWithContext(ctx context.Context, opts RoomRunOptions, validation room.ValidationOptions, evidences ...*roomEvidence) (plans []*roomParticipantPlan, secrets []string, planErr error) {
	return buildRoomParticipantPlansAdapter(ctx, opts, validation, evidences...)
}

// Deprecated: callers remain source-compatible while roomplanning owns admission.
func awaitRoomParticipantConnections(ctx context.Context, coordinator *roomCoordinator, plans []*roomParticipantPlan, timer *time.Timer, secrets []string, outcomes <-chan roomConnectionOutcome, cleanup *roomCleanupWaiter) error {
	return awaitRoomParticipantConnectionsAdapter(ctx, coordinator, plans, timer, secrets, outcomes, cleanup)
}
