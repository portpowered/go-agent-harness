package roomevidence

import (
	"errors"
	"testing"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/gateway"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
)

func TestRoomReplayErrorsRetainStableClassifications(t *testing.T) {
	mismatch := &BundleError{Kind: BundleMismatch, Field: "pcm", Expected: "16", Actual: "8", Err: gateway.ErrReplayMismatch}
	if !errors.Is(mismatch, ErrInvalidRoomReplayBundle) || !errors.Is(mismatch, gateway.ErrReplayMismatch) || errors.Is(mismatch, gateway.ErrReplayIncomplete) {
		t.Fatalf("mismatch classification failed: %v", mismatch)
	}
	incomplete := &BundleError{Kind: BundleIncomplete, Err: providers.ErrReplayIncomplete}
	if !errors.Is(incomplete, ErrRoomReplayBundleIncomplete) || !errors.Is(incomplete, providers.ErrReplayIncomplete) || !errors.Is(incomplete, gateway.ErrReplayIncomplete) {
		t.Fatalf("incomplete classification failed: %v", incomplete)
	}
	if (&BundleError{}).Error() == "" || (*BundleError)(nil).Error() != roomReplayNilString {
		t.Fatal("bundle error formatting lost its nil/empty contract")
	}
	detail := &DeltaReconstructionError{ParticipantID: "alpha", StreamID: "alpha:output", DeltaID: "d0", DeltaIndex: 0, ByteOffset: 2, ExpectedByte: -1, ActualByte: 1, ExpectedLength: 4, ActualLength: 2, ExpectedSampleCount: 2, ActualSampleCount: 1, Cause: ErrRoomReplayDeltaReconstruction}
	if !errors.Is(detail, ErrRoomReplayDeltaReconstruction) || !errors.Is(detail, ErrInvalidRoomReplayBundle) || detail.Error() == "" {
		t.Fatalf("delta classification failed: %v", detail)
	}
}
