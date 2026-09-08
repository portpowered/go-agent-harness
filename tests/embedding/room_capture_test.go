package embedding_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	roomswire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	sessionwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/wire"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

func TestExternalRoomRejectsMissingProviderTrace(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	scheduler := clock.NewDeterministic(time.Unix(123, 0), time.Millisecond)
	live := sessionwire.NewLiveService(sessionwire.LiveDependencies{
		Clock:     scheduler.Now,
		Scheduler: scheduler,
		InferencerFactory: func(context.Context, session.LiveRequest) (messages.SessionInferencer, error) {
			return newEmbeddedLiveProvider(), nil
		},
	})
	host := roomswire.NewService(roomswire.Dependencies{Clock: scheduler, Live: live})
	manifest := rooms.Manifest{SchemaVersion: rooms.SchemaVersion, Room: rooms.Room{MaxDuration: time.Second}}
	for _, id := range []string{"alice", "bob"} {
		manifest.Participants = append(manifest.Participants, rooms.Participant{ID: id, SystemPrompt: "agent", OpeningPrompt: "start", Provider: "fixture", Model: "fixture", APIKeyEnv: "UNRESOLVED_TEST_SELECTOR", Tools: []string{}})
	}
	output := t.TempDir()
	ready := make(chan struct{}, 2)
	finished := make(chan rooms.RoomResult, 1)
	go func() {
		result, err := host.Run(ctx, nil, rooms.RoomRunOptions{Manifest: manifest, OutputDir: output, OnParticipantReady: func(rooms.RoomParticipantReady) { ready <- struct{}{} }})
		if err != nil {
			t.Errorf("run room: %v", err)
		}
		finished <- result
	}()
	for range manifest.Participants {
		select {
		case <-ready:
		case <-ctx.Done():
			t.Fatal("room participants were not admitted")
		}
	}
	scheduler.AdvanceBy(time.Second)
	select {
	case result := <-finished:
		if result.RecordingStatus == nil || result.RecordingStatus.State != "partial" {
			t.Fatalf("missing capture status=%+v, want partial", result.RecordingStatus)
		}
	case <-ctx.Done():
		t.Fatal("room did not finalize")
	}
	if _, err := host.LoadReplayPlan(output); !errors.Is(err, rooms.ErrReplayBundleIncomplete) {
		t.Fatalf("replay admission=%v, want incomplete evidence", err)
	}
	for _, participant := range manifest.Participants {
		path := filepath.Join(output, "participants", participant.ID, "capture.json")
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("missing provider trace was fabricated at %s: %v", path, err)
		}
	}
}
