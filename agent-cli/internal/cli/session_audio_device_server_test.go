package cli

import (
	"errors"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
)

func TestSessionCommandRejectsNonLoopbackAudioDeviceServerBeforeSessionSetup(t *testing.T) {
	command := NewSessionCommand(flags.NewAskFlags(), flags.NewGlobalFlags(), nil, nil).Generate()
	command.SetArgs([]string{
		"--replay", "not-opened.session.json",
		"--audio-device-server", "192.0.2.10:19090",
	})
	err := command.Execute()
	if !errors.Is(err, audio.ErrRemoteDeviceServerEndpoint) {
		t.Fatalf("session remote device error = %v, want ErrRemoteDeviceServerEndpoint", err)
	}
}

func TestSessionCommandAudioDeviceServerFlagIsDiscoverable(t *testing.T) {
	command := NewSessionCommand(flags.NewAskFlags(), flags.NewGlobalFlags(), nil, nil).Generate()
	flag := command.Flags().Lookup("audio-device-server")
	if flag == nil || flag.DefValue != "" {
		t.Fatalf("audio-device-server flag = %#v, want optional empty default", flag)
	}
}
