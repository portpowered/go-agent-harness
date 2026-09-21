package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomreplay"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms/internal/lifecycle"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms/internal/planning"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

type replayServiceStub struct {
	plan        roomreplay.RoomReplayPlan
	loadErr     error
	validateErr error
}

func (s replayServiceStub) Load(string) (roomreplay.RoomReplayPlan, error) {
	return s.plan, s.loadErr
}

func (s replayServiceStub) ValidateOutput(roomreplay.RoomReplayPlan, string) error {
	return s.validateErr
}

func (replayServiceStub) Build(context.Context, roomreplay.BuildRequest) (roomreplay.Schedule, error) {
	return nil, nil
}

func TestServiceExposesReplayAndEvidenceBoundaries(t *testing.T) {
	loadErr := errors.New("replay admission failed")
	validateErr := errors.New("replay output rejected")
	replay := replayServiceStub{
		plan: roomreplay.RoomReplayPlan{Participants: []roomreplay.RoomReplayParticipant{{
			ID: "agent", Kind: roomreplay.ParticipantKindAgent, Provider: "openai", Model: "gpt-realtime",
		}}},
		loadErr: loadErr, validateErr: validateErr,
	}
	svc := New(Dependencies{
		Planner: planning.New(), Replay: replay,
		Runner: lifecycle.New(lifecycle.Dependencies{}),
	})
	if svc == nil {
		t.Fatal("New() returned nil")
	}

	plan, err := svc.LoadReplayPlan("bundle")
	if !errors.Is(err, loadErr) || len(plan.Participants) != 1 {
		t.Fatalf("LoadReplayPlan() = %#v, %v; want delegated plan and stub error", plan, err)
	}
	if err := svc.ValidateReplayOutput(replay.plan, "output"); !errors.Is(err, validateErr) {
		t.Fatalf("ValidateReplayOutput() error = %v, want stub error", err)
	}
	manifest := svc.ReplayManifest(replay.plan)
	if len(manifest.Participants) != 1 || manifest.Participants[0].ID != "agent" {
		t.Fatalf("ReplayManifest() = %#v, want one projected participant", manifest)
	}

	evidenceDir := filepath.Join(t.TempDir(), "evidence")
	if err := svc.ValidateEvidenceOutput(evidenceDir); err != nil {
		t.Fatalf("ValidateEvidenceOutput() error = %v", err)
	}
	configDir := filepath.Join(t.TempDir(), "config")
	runDir, err := svc.CreateFreshRunDirectory(configDir)
	if err != nil {
		t.Fatalf("CreateFreshRunDirectory() error = %v", err)
	}
	info, err := os.Stat(runDir)
	if err != nil || !info.IsDir() || filepath.Dir(runDir) != configDir {
		t.Fatalf("run directory = %q, stat error = %v, want fresh directory under %q", runDir, err, configDir)
	}

	if _, err := svc.ResolveLaunchPlan(rooms.RoomLaunchOptions{}); !errors.Is(err, rooms.ErrRoomServiceUnavailable) {
		t.Fatalf("ResolveLaunchPlan() error = %v, want unavailable without a host plan", err)
	}
	if _, err := svc.Run(context.Background(), nil, rooms.RoomRunOptions{ReplayPath: "bundle"}); !errors.Is(err, loadErr) {
		t.Fatalf("Run() replay admission error = %v, want stub error", err)
	}
}

func TestServiceReportsUnavailableForMissingReplayOwner(t *testing.T) {
	var nilService *Service
	if _, err := nilService.Run(context.Background(), nil, rooms.RoomRunOptions{}); !errors.Is(err, rooms.ErrRoomServiceUnavailable) {
		t.Fatalf("nil Run() error = %v, want unavailable", err)
	}
	if _, err := nilService.LoadReplayPlan("bundle"); !errors.Is(err, rooms.ErrRoomServiceUnavailable) {
		t.Fatalf("nil LoadReplayPlan() error = %v, want unavailable", err)
	}
	if err := nilService.ValidateReplayOutput(rooms.RoomReplayPlan{}, "output"); !errors.Is(err, rooms.ErrRoomServiceUnavailable) {
		t.Fatalf("nil ValidateReplayOutput() error = %v, want unavailable", err)
	}

	svc := New(Dependencies{Planner: planning.New(), Runner: lifecycle.New(lifecycle.Dependencies{})})
	if _, err := svc.LoadReplayPlan("bundle"); !errors.Is(err, rooms.ErrRoomServiceUnavailable) {
		t.Fatalf("missing replay owner error = %v, want unavailable", err)
	}
	if err := svc.ValidateReplayOutput(rooms.RoomReplayPlan{}, "output"); !errors.Is(err, rooms.ErrRoomServiceUnavailable) {
		t.Fatalf("missing replay owner output error = %v, want unavailable", err)
	}
}

func TestPublicRunRejectsNonEmptyReplayOutputBeforeParticipantEffects(t *testing.T) {
	replay := replayServiceStub{plan: roomreplay.RoomReplayPlan{BundlePath: filepath.Join(t.TempDir(), "bundle")}}
	service := New(Dependencies{
		Planner: planning.New(), Replay: replay,
		Runner: lifecycle.New(lifecycle.Dependencies{Clock: platformclock.Real{}}),
	})
	var public rooms.Service = service

	output := filepath.Join(t.TempDir(), "output")
	if err := os.Mkdir(output, 0o700); err != nil {
		t.Fatalf("create output directory: %v", err)
	}
	marker := filepath.Join(output, "keep.txt")
	if err := os.WriteFile(marker, []byte("preserve"), 0o600); err != nil {
		t.Fatalf("write output marker: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := 0
	recordingDisabled := false
	manifest := rooms.Manifest{
		SchemaVersion: rooms.SchemaVersion,
		Room:          rooms.Room{MaxTurns: 1, Recording: &rooms.RoomRecordingConfig{Enabled: &recordingDisabled}},
		Participants: []rooms.Participant{
			{ID: "human-a", Kind: rooms.ParticipantKindHuman, SystemPrompt: "human", InputDevice: "mic", OutputDevice: "speaker", Tools: []string{}},
			{ID: "human-b", Kind: rooms.ParticipantKindHuman, SystemPrompt: "human", InputDevice: "mic", OutputDevice: "speaker", Tools: []string{}},
		},
	}
	_, err := public.Run(ctx, nil, rooms.RoomRunOptions{
		Manifest: manifest,
		ReplayPlan: &roomreplay.RoomReplayPlan{
			BundlePath: replay.plan.BundlePath,
			Finalized:  true,
		},
		OutputDir: output,
		OnParticipantReady: func(rooms.RoomParticipantReady) {
			ready++
			cancel()
		},
	})
	if err == nil || !strings.Contains(err.Error(), "must be empty") {
		t.Fatalf("Run() error = %v, want non-empty replay output rejection", err)
	}
	if ready != 0 {
		t.Fatalf("participant ready callbacks = %d, want no runner side effect", ready)
	}
	entries, err := os.ReadDir(output)
	if err != nil || len(entries) != 1 || entries[0].Name() != filepath.Base(marker) {
		t.Fatalf("output entries = %v, err = %v; want only preserved marker", entries, err)
	}
}

func TestPublicRunRejectsUnwritableReplayOutputBeforeParticipantEffects(t *testing.T) {
	replay := replayServiceStub{plan: roomreplay.RoomReplayPlan{BundlePath: filepath.Join(t.TempDir(), "bundle")}}
	service := New(Dependencies{
		Planner: planning.New(), Replay: replay,
		Runner: lifecycle.New(lifecycle.Dependencies{Clock: platformclock.Real{}}),
	})
	var public rooms.Service = service

	output := filepath.Join(t.TempDir(), "output")
	if err := os.Mkdir(output, 0o700); err != nil {
		t.Fatalf("create output directory: %v", err)
	}
	if err := os.Chmod(output, 0o500); err != nil {
		t.Fatalf("make output directory unwritable: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(output, 0o700); err != nil {
			t.Errorf("restore output directory permissions: %v", err)
		}
	})
	probe, err := os.CreateTemp(output, ".permission-check-")
	if err == nil {
		probePath := probe.Name()
		if closeErr := probe.Close(); closeErr != nil {
			t.Fatalf("close permission probe: %v", closeErr)
		}
		if removeErr := os.Remove(probePath); removeErr != nil {
			t.Fatalf("remove permission probe: %v", removeErr)
		}
		t.Skip("test filesystem does not enforce directory write permissions")
	}

	ready := 0
	recordingDisabled := false
	manifest := rooms.Manifest{
		SchemaVersion: rooms.SchemaVersion,
		Room:          rooms.Room{MaxTurns: 1, Recording: &rooms.RoomRecordingConfig{Enabled: &recordingDisabled}},
		Participants: []rooms.Participant{
			{ID: "human-a", Kind: rooms.ParticipantKindHuman, SystemPrompt: "human", InputDevice: "mic", OutputDevice: "speaker", Tools: []string{}},
			{ID: "human-b", Kind: rooms.ParticipantKindHuman, SystemPrompt: "human", InputDevice: "mic", OutputDevice: "speaker", Tools: []string{}},
		},
	}
	_, err = public.Run(context.Background(), nil, rooms.RoomRunOptions{
		Manifest: manifest, ReplayPlan: &replay.plan, OutputDir: output,
		OnParticipantReady: func(rooms.RoomParticipantReady) { ready++ },
	})
	if err == nil || !strings.Contains(err.Error(), "probe room evidence output target") {
		t.Fatalf("Run() error = %v, want unwritable output rejection during admission", err)
	}
	if ready != 0 {
		t.Fatalf("participant ready callbacks = %d, want no runner side effect", ready)
	}
}
