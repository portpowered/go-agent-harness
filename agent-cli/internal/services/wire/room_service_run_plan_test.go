package wire

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
)

func TestServiceRunPlanKeepsReplayAndLiveSourcesExclusive(t *testing.T) {
	service := planService()
	bundle := filepath.Join(t.TempDir(), "bundle")
	_, err := service.ResolveRunPlan(rooms.RoomRunPlanOptions{ReplayPath: bundle, Launch: rooms.RoomLaunchOptions{ConfigPath: "room.json"}})
	if !errors.Is(err, rooms.ErrReplaySourceConflict) {
		t.Fatalf("replay with config = %v, want source conflict", err)
	}
	if _, err := service.ResolveRunPlan(rooms.RoomRunPlanOptions{ReplayPath: bundle}); err == nil {
		t.Fatal("missing replay bundle was admitted")
	}
	path := writeHumanRoom(t, micID, speakerID, "")
	plan, err := service.ResolveRunPlan(rooms.RoomRunPlanOptions{Launch: rooms.RoomLaunchOptions{ConfigPath: path, Devices: newLaunchDevices(), CredentialLookup: presentCredentials}})
	if err != nil || plan.Replay() || plan.LaunchPlan == nil || plan.ReplayPath != "" || len(plan.Manifest.Participants) != 2 {
		t.Fatalf("live run plan = %+v / %v, want only the launch decision", plan, err)
	}
}

func TestServiceRunOutputPolicyFollowsRecordingAndLaunchMode(t *testing.T) {
	service := planService()
	configured := func(recording string) rooms.RoomRunPlan {
		plan, err := service.ResolveRunPlan(rooms.RoomRunPlanOptions{Launch: rooms.RoomLaunchOptions{
			ConfigPath: writeHumanRoom(t, micID, speakerID, recording), Devices: newLaunchDevices(), CredentialLookup: presentCredentials,
		}})
		if err != nil {
			t.Fatalf("resolve configured room: %v", err)
		}
		return plan
	}
	cases := []struct {
		name      string
		plan      rooms.RoomRunPlan
		requested string
		explicit  bool
		want      string
	}{
		{name: "disabled", plan: configured(`,"recording":{"enabled":false}`), requested: "out", explicit: true, want: ""},
		{name: "manifest directory", plan: configured(`,"recording":{"directory":"from-manifest"}`), requested: "flag-default", want: "from-manifest"},
		{name: "explicit beats manifest", plan: configured(`,"recording":{"directory":"from-manifest"}`), requested: "chosen", explicit: true, want: "chosen"},
		{name: "configured default", plan: configured(""), requested: " ", want: rooms.DefaultRoomOutputDir},
		{name: "replay default", plan: rooms.RoomRunPlan{ReplayPlan: &rooms.RoomReplayPlan{}}, requested: "", want: rooms.DefaultRoomOutputDir},
	}
	for _, tc := range cases {
		got, err := service.ResolveRunOutput(tc.plan, tc.requested, tc.explicit)
		if err != nil || got != tc.want {
			t.Fatalf("%s: output = %q / %v, want %q", tc.name, got, err, tc.want)
		}
	}
}

func TestServiceBareRunOutputIsAFreshConfigChildEveryRun(t *testing.T) {
	service := planService()
	configDir := t.TempDir()
	plan, err := service.ResolveRunPlan(rooms.RoomRunPlanOptions{Launch: rooms.RoomLaunchOptions{
		ConfigDir: configDir, Devices: newLaunchDevices(), CredentialLookup: func(string) (string, bool) { return "key", true },
	}})
	if err != nil {
		t.Fatalf("resolve bare room: %v", err)
	}
	first, err := service.ResolveRunOutput(plan, rooms.DefaultRoomOutputDir, false)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.ResolveRunOutput(plan, rooms.DefaultRoomOutputDir, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, output := range []string{first, second} {
		if filepath.Dir(output) != configDir || !strings.HasPrefix(filepath.Base(output), "room-run-") {
			t.Fatalf("bare output %q is not a fresh child of %q", output, configDir)
		}
	}
	if first == second {
		t.Fatalf("bare runs reused output %q", first)
	}
	explicit, err := service.ResolveRunOutput(plan, "chosen", true)
	if err != nil || explicit != "chosen" {
		t.Fatalf("explicit bare output = %q / %v, want the caller's directory", explicit, err)
	}
}

func TestServiceValidatesRunOutputBeforeAnyParticipantStarts(t *testing.T) {
	service := planService()
	live := rooms.RoomRunPlan{LaunchPlan: &rooms.RoomLaunchPlan{}}
	if err := service.ValidateRunOutput(live, ""); err != nil {
		t.Fatalf("disabled output = %v, want no validation", err)
	}
	occupied := t.TempDir()
	if err := os.WriteFile(filepath.Join(occupied, "keep"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := service.ValidateRunOutput(live, occupied); err == nil || !strings.Contains(err.Error(), "must be empty") {
		t.Fatalf("occupied output = %v, want an empty-directory requirement", err)
	}
	fresh := filepath.Join(t.TempDir(), "evidence")
	if err := service.ValidateRunOutput(live, fresh); err != nil {
		t.Fatalf("fresh output = %v, want accepted", err)
	}
	bundle := t.TempDir()
	replay := rooms.RoomRunPlan{ReplayPlan: &rooms.RoomReplayPlan{BundlePath: bundle}}
	if err := service.ValidateRunOutput(replay, bundle); err == nil {
		t.Fatal("replay output inside its own bundle was accepted")
	}
}
