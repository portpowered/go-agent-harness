package wire

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	captureReplayWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

const (
	micID     = "fake:mic"
	speakerID = "fake:speaker"
	agentKey  = "ROOM_PLAN_AGENT_KEY"
)

// launchDevices is a host device snapshot that records every query so tests
// can prove when planning does and does not consult the host.
type launchDevices struct {
	devices  []rooms.LaunchDevice
	listErr  error
	lists    int
	defaults int
}

func newLaunchDevices() *launchDevices {
	return &launchDevices{devices: []rooms.LaunchDevice{{ID: micID, Direction: rooms.LaunchDeviceInput}, {ID: speakerID, Direction: rooms.LaunchDeviceOutput}}}
}

func (d *launchDevices) List() ([]rooms.LaunchDevice, error) {
	d.lists++
	return d.devices, d.listErr
}

func (d *launchDevices) Default(direction rooms.LaunchDeviceDirection) (rooms.LaunchDevice, error) {
	d.defaults++
	for _, device := range d.devices {
		if device.Direction == direction {
			return device, nil
		}
	}
	return rooms.LaunchDevice{}, errors.New("no default " + string(direction))
}

// planService composes the CLI room service with the replay and evidence
// peers that run admission and output policy consult.
func planService() rooms.Service {
	return NewRoomService(nil, nil, clock.Real{}, NewRoomReplayService(captureReplayWire.NewService()), NewRoomEvidenceService(), NewRoomLatencyService())
}

// presentCredentials admits every named credential without exposing a value.
func presentCredentials(string) (string, bool) { return "present", true }

func writeHumanRoom(t *testing.T, input, output string, recording string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "room.json")
	document := fmt.Sprintf(`{"schema_version":1,"room":{"max_turns":1%s},"participants":[`+
		`{"kind":"human","id":"customer","system_prompt":"customer","tools":[],"input_device":%q,"output_device":%q},`+
		`{"id":"agent","system_prompt":"agent","opening_prompt":"start","provider":"openai","model":"m","api_key_env":%q,"tools":[]}]}`,
		recording, input, output, agentKey)
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatalf("write room: %v", err)
	}
	return path
}
