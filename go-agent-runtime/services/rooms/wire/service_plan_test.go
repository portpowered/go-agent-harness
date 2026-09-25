package wire

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

// planService admits launches without live, replay, or evidence peers:
// launch planning must never need them.
func planService() rooms.Service {
	return NewService(Dependencies{Clock: clock.Real{}})
}

// presentCredentials admits every named credential without exposing a value.
func presentCredentials(string) (string, bool) { return "present", true }

func noCredentials(string) (string, bool) { return "", false }

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

func TestServiceValidatesConfiguredHumanDevicesBeforeRun(t *testing.T) {
	service := planService()
	devices := newLaunchDevices()
	path := writeHumanRoom(t, micID, speakerID, "")
	plan, err := service.ResolveLaunchPlan(rooms.RoomLaunchOptions{ConfigPath: path, Devices: devices, CredentialLookup: presentCredentials})
	if err != nil {
		t.Fatalf("resolve configured human room: %v", err)
	}
	customer, _ := plan.Participant(humanCustomerID)
	if plan.Mode != rooms.RoomLaunchModeConfigured || customer.InputDevice != micID || customer.OutputDevice != speakerID || devices.lists != 1 || devices.defaults != 0 {
		t.Fatalf("plan = %+v devices=%+v, want one snapshot and no defaults", plan, devices)
	}
	cases := []struct {
		name  string
		input string
		want  error
		field string
	}{
		{name: "missing", input: "fake:gone", want: rooms.ErrLaunchDeviceUnavailable, field: "participants[0].input_device"},
		{name: "direction", input: speakerID, want: rooms.ErrLaunchDeviceDirectionMismatch, field: "participants[0].input_device"},
	}
	for _, tc := range cases {
		_, err := service.ResolveLaunchPlan(rooms.RoomLaunchOptions{ConfigPath: writeHumanRoom(t, tc.input, speakerID, ""), Devices: newLaunchDevices(), CredentialLookup: presentCredentials})
		var validation *rooms.ValidationError
		if !errors.Is(err, tc.want) || !errors.As(err, &validation) || validation.Field != tc.field || !errors.Is(err, rooms.ErrInvalidManifest) {
			t.Fatalf("%s: error = %v, want field-specific %v", tc.name, err, tc.want)
		}
	}
	if _, err := service.ResolveLaunchPlan(rooms.RoomLaunchOptions{ConfigPath: path, CredentialLookup: presentCredentials}); !errors.Is(err, rooms.ErrLaunchDeviceInventoryUnavailable) {
		t.Fatalf("human room without host devices = %v, want inventory unavailable", err)
	}
	failing := newLaunchDevices()
	failing.listErr = errors.New("coreaudio offline")
	if _, err := service.ResolveLaunchPlan(rooms.RoomLaunchOptions{ConfigPath: path, Devices: failing, CredentialLookup: presentCredentials}); err == nil || !strings.Contains(err.Error(), "could not inspect available devices: coreaudio offline") {
		t.Fatalf("device listing failure = %v, want the host cause", err)
	}
}

func TestServiceConfiguredAgentRoomNeverConsultsHostDevices(t *testing.T) {
	devices := newLaunchDevices()
	path := filepath.Join(t.TempDir(), "room.json")
	document := `{"schema_version":1,"room":{"max_turns":1},"participants":[` +
		`{"id":"alice","system_prompt":"a","opening_prompt":"start","provider":"openai","model":"m","api_key_env":"ROOM_PLAN_ALICE","tools":[]},` +
		`{"id":"bob","system_prompt":"b","provider":"openai","model":"m","api_key_env":"ROOM_PLAN_BOB","tools":[]}]}`
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := planService().ResolveLaunchPlan(rooms.RoomLaunchOptions{ConfigPath: path, Devices: devices, CredentialLookup: presentCredentials})
	if err != nil || devices.lists != 0 || devices.defaults != 0 {
		t.Fatalf("agent room plan err=%v devices=%+v, want no host device queries", err, devices)
	}
	alice, _ := plan.Participant("alice")
	bob, _ := plan.Participant("bob")
	if alice.CredentialProvenance != rooms.RoomCredentialFromEnvironment || bob.CredentialReference != "ROOM_PLAN_BOB" || plan.ConfigDir != filepath.Dir(path) {
		t.Fatalf("plan = %+v, want credential references, provenance, and the config directory", plan)
	}
}

func TestServiceBareLaunchChecksCredentialBeforeDevices(t *testing.T) {
	service := planService()
	devices := newLaunchDevices()
	_, err := service.ResolveLaunchPlan(rooms.RoomLaunchOptions{ConfigDir: t.TempDir(), Devices: devices, CredentialLookup: noCredentials})
	if err == nil || !strings.Contains(err.Error(), rooms.DefaultOpenAIAPIKeyEnv) || strings.Contains(err.Error(), "--api-key") || devices.defaults != 0 {
		t.Fatalf("bare launch without credential = %v (defaults=%d), want an env remedy before any device query", err, devices.defaults)
	}
	configErr := errors.New("config unreadable")
	failing := func(string) (string, error) { return "", configErr }
	if _, err := service.ResolveLaunchPlan(rooms.RoomLaunchOptions{Devices: devices, CredentialLookup: noCredentials, ConfigCredential: failing}); !errors.Is(err, configErr) {
		t.Fatalf("bare launch config failure = %v, want the host config cause", err)
	}
	if _, err := service.ResolveLaunchPlan(rooms.RoomLaunchOptions{CredentialLookup: noCredentials}); !errors.Is(err, rooms.ErrLaunchDeviceInventoryUnavailable) {
		t.Fatalf("bare launch without host devices = %v, want inventory unavailable", err)
	}
}

func TestServiceBareLaunchSynthesizesCustomerAndAgentWithoutRetainingCredential(t *testing.T) {
	const secret = "bare-config-secret"
	configDir := t.TempDir()
	devices := newLaunchDevices()
	plan, err := planService().ResolveLaunchPlan(rooms.RoomLaunchOptions{
		ConfigDir: configDir, Devices: devices, CredentialLookup: noCredentials,
		ConfigCredential: func(name string) (string, error) {
			if name != rooms.DefaultOpenAIAPIKeyEnv {
				t.Fatalf("config credential name = %q", name)
			}
			return secret, nil
		},
	})
	if err != nil {
		t.Fatalf("resolve bare launch: %v", err)
	}
	customer, _ := plan.Participant(humanCustomerID)
	agent, _ := plan.Participant(agentID)
	if plan.Mode != rooms.RoomLaunchModeBare || !plan.Manifest.Room.Interactive || plan.ConfigDir != configDir || devices.defaults != 2 {
		t.Fatalf("bare plan = %+v defaults=%d, want interactive room in the config directory", plan, devices.defaults)
	}
	if customer.Kind != rooms.ParticipantKindHuman || customer.InputDevice != micID || customer.OutputDevice != speakerID {
		t.Fatalf("customer = %+v, want host default devices", customer)
	}
	if agent.Provider != "openai" || agent.Model != rooms.DefaultRealtimeModel || agent.CredentialReference != rooms.DefaultOpenAIAPIKeyEnv || agent.CredentialProvenance != rooms.RoomCredentialFromConfig {
		t.Fatalf("agent = %+v, want bare OpenAI defaults with config provenance", agent)
	}
	if strings.Contains(fmt.Sprintf("%+v", plan), secret) {
		t.Fatal("bare launch plan retained the resolved credential")
	}
	env := func(name string) (string, bool) { return "env-secret", name == rooms.DefaultOpenAIAPIKeyEnv }
	plan, err = planService().ResolveLaunchPlan(rooms.RoomLaunchOptions{Devices: newLaunchDevices(), CredentialLookup: env})
	if agent, _ = plan.Participant(agentID); err != nil || agent.CredentialProvenance != rooms.RoomCredentialFromEnvironment {
		t.Fatalf("env bare plan = %+v / %v, want environment provenance", agent, err)
	}
}

func TestServiceBareLaunchRejectsUnusableHostDefaults(t *testing.T) {
	env := func(name string) (string, bool) { return "env-secret", true }
	reversed := &launchDevices{devices: []rooms.LaunchDevice{{ID: speakerID, Direction: rooms.LaunchDeviceOutput}}}
	_, err := planService().ResolveLaunchPlan(rooms.RoomLaunchOptions{Devices: reversed, CredentialLookup: env})
	if err == nil || !strings.Contains(err.Error(), "bare room customer input device is unavailable") {
		t.Fatalf("missing default input = %v, want the device remedy", err)
	}
	mislabeled := &mislabeledDevices{}
	if _, err := planService().ResolveLaunchPlan(rooms.RoomLaunchOptions{Devices: mislabeled, CredentialLookup: env}); !errors.Is(err, rooms.ErrLaunchDeviceDirectionMismatch) {
		t.Fatalf("mislabeled default = %v, want direction mismatch", err)
	}
}

type mislabeledDevices struct{}

func (mislabeledDevices) List() ([]rooms.LaunchDevice, error) { return nil, nil }

func (mislabeledDevices) Default(rooms.LaunchDeviceDirection) (rooms.LaunchDevice, error) {
	return rooms.LaunchDevice{ID: speakerID, Direction: rooms.LaunchDeviceOutput}, nil
}
