package wire

import (
	"context"
	"errors"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
)

func TestPublicRoomConstructorsPreserveFailureAndCleanupBoundaries(t *testing.T) {
	failure := NewFailureService()
	const secret = "provider-secret"
	if got := failure.Sanitize(errors.New("provider failed: "+secret), []string{secret}); got == "" || contains(got, secret) {
		t.Fatalf("Sanitize(%q) = %q, secret leaked or message was empty", secret, got)
	}
	result := failure.FailureResult(errors.New("provider failed"), nil)
	if result.TerminationReason != rooms.RoomTerminationFailed || result.Error == "" {
		t.Fatalf("FailureResult = %#v, want failed public result", result)
	}

	lifecycle := NewParticipantLifecycle(rooms.ParticipantLifecycleOptions{})
	if lifecycle == nil {
		t.Fatal("NewParticipantLifecycle returned nil")
	}
	if lifecycle.DeviceHasReady() || !lifecycle.AdmitResponseTerminal() {
		t.Fatal("new participant lifecycle has unexpected readiness or terminal state")
	}
	tracked := NewTrackedSession(nil, lifecycle, nil)
	if tracked == nil || tracked.SessionAdmissionClosed() || tracked.SupportsCompleteMessages() || tracked.SupportsCompleteMessagesWithoutResponse() {
		t.Fatal("new tracked session exposed unexpected capabilities or admission state")
	}
	if err := tracked.Close(); err != nil {
		t.Fatalf("tracked nil session Close: %v", err)
	}

	tracker := NewConnectionTracker(nil, lifecycle, nil)
	if tracker == nil {
		t.Fatal("NewConnectionTracker returned nil")
	}
	if _, err := tracker.ConnectSession(context.Background()); err == nil {
		t.Fatal("nil connection tracker unexpectedly connected")
	}
	if outcome, ready := tracker.Outcome(); !ready || outcome == nil {
		t.Fatalf("connection tracker outcome = (%v, %t), want one failure", outcome, ready)
	}

	mesh := NewMesh(rooms.MeshConfig{})
	if mesh == nil {
		t.Fatal("NewMesh returned nil")
	}
	if err := mesh.Close(); err != nil {
		t.Fatalf("empty mesh Close: %v", err)
	}
	if _, err := NewPairSpec(" zeta ", " alpha "); err != nil {
		t.Fatalf("NewPairSpec: %v", err)
	}
}

func contains(value, part string) bool {
	for i := 0; i+len(part) <= len(value); i++ {
		if value[i:i+len(part)] == part {
			return true
		}
	}
	return false
}
