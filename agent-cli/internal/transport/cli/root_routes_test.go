package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/probe/fleet"
	sessionservicewire "github.com/portpowered/go-agent-harness/agent-cli/internal/services/wire"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
	"github.com/spf13/cobra"
)

type cliExecution struct {
	exitCode int
	stdout   string
	stderr   string
}

func newTestRootCommand(fleetExecutor ...fleet.EntryExecutor) *cobra.Command {
	return newTestRootCommandWithProbeFleetCommand(NewProbeFleetCommand(nil, nil, newReplayRuntimeServiceForTest(), fleetExecutor...))
}

func newTestRootCommandWithProbeFleetCommand(probeFleetCommand *ProbeFleetCommand, sessionInferencer ...messages.SessionInferencer) *cobra.Command {
	globalFlags := flags.NewGlobalFlags()
	askFlags := flags.NewAskFlags()
	loopFlags := flags.NewLoopFlags()
	chatFlags := flags.NewChatFlags()
	var injectedSessionInferencer messages.SessionInferencer
	if len(sessionInferencer) > 0 {
		injectedSessionInferencer = sessionInferencer[0]
	}
	registry := defaultTestDeviceRegistry{}
	probeReplay := newReplayRuntimeServiceForTest()
	sessionCommand := newTestSessionCommand(askFlags, globalFlags, testSessionDeps{Inferencer: injectedSessionInferencer, Registry: registry})

	router := NewRouter(
		globalFlags,
		NewRootCommand(globalFlags),
		NewAskCommand(nil, askFlags, loopFlags, globalFlags),
		NewChatCommand(nil, askFlags, loopFlags, chatFlags, globalFlags, testFileStoreFactory()),
		NewToolCommand(globalFlags),
		NewInteractionCommand(),
		NewInteractionReplayCommand(),
		NewProbeCommand(),
		NewProbeRunCommandWithDeviceService(newDevicesTestService(), nil, sessionservicewire.NewMetricsCollector(NewSessionRequestService(sessionCommand), probeReplay), probeReplay),
		NewProbeGateCommand(),
		NewProbeReportCommand(),
		probeFleetCommand,
		sessionCommand,
		NewSessionShowCommand(globalFlags, testFileStoreFactory()),
		NewSessionListCommand(globalFlags, testFileStoreFactory()),
		NewSessionDeleteCommand(globalFlags, testFileStoreFactory()),
		NewSessionReplayCommand(nil),
		newTestRoomRunCommand(globalFlags, defaultTestDeviceRegistry{}),
		NewConfigCommand(),
		NewConfigAddLocalCommand(globalFlags),
		registry,
		newDevicesTestService(),
	)
	// Root command tests inject the device service so they do not depend on
	// workstation audio endpoints.
	return NewAgentCLI(router).Generate()
}

type defaultTestDeviceRegistry struct{}

func (defaultTestDeviceRegistry) List() ([]devicegw.Device, error) { return nil, nil }

func (defaultTestDeviceRegistry) Default(direction devicegw.Direction) (devicegw.Device, error) {
	return devicegw.Device{}, devicegw.NewNoDefaultDeviceError(direction)
}

func (defaultTestDeviceRegistry) Open(id devicegw.DeviceID) (devicegw.OpenedDevice, error) {
	return nil, devicegw.NewDeviceNotFoundError(id)
}

func executeCLI(args ...string) cliExecution {
	root := newTestRootCommand()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(args)

	exitCode := 0
	if root.Execute() != nil {
		exitCode = 1
	}
	return cliExecution{exitCode: exitCode, stdout: stdout.String(), stderr: stderr.String()}
}

func TestRootCommandExecutionContracts(t *testing.T) {
	noArgs := executeCLI()
	help := executeCLI("--help")

	if noArgs.exitCode != 0 {
		t.Fatalf("no-argument exit code = %d, want 0; stdout=%q stderr=%q", noArgs.exitCode, noArgs.stdout, noArgs.stderr)
	}
	if help.exitCode != 0 {
		t.Fatalf("--help exit code = %d, want 0; stdout=%q stderr=%q", help.exitCode, help.stdout, help.stderr)
	}
	if noArgs.stdout == "" || help.stdout == "" {
		t.Fatalf("root help must be non-empty: no-args=%q help=%q", noArgs.stdout, help.stdout)
	}
	if noArgs.stdout != help.stdout {
		t.Fatalf("no-argument help differs from --help:\nno args: %q\n--help: %q", noArgs.stdout, help.stdout)
	}
	if noArgs.stderr != "" || help.stderr != "" {
		t.Fatalf("root help stderr: no-args=%q help=%q", noArgs.stderr, help.stderr)
	}
	for _, want := range []string{
		"Yui is a cross-platform voice-agent CLI",
		"export OPENAI_API_KEY=\"your-openai-api-key\"",
		"yui session",
		"yui session --browser-tools webmcp",
	} {
		if !strings.Contains(noArgs.stdout, want) {
			t.Fatalf("root help missing %q:\n%s", want, noArgs.stdout)
		}
	}

	unknownCommand := executeCLI("unknown-command")
	if unknownCommand.exitCode != 1 {
		t.Fatalf("unknown-command exit code = %d, want 1; stdout=%q stderr=%q", unknownCommand.exitCode, unknownCommand.stdout, unknownCommand.stderr)
	}
	if unknownCommand.stdout != "" {
		t.Fatalf("unknown-command stdout = %q, want empty", unknownCommand.stdout)
	}
	if unknownCommand.stderr != "Error: unknown command \"unknown-command\" for \"yui\"\nRun 'yui --help' for usage.\n" {
		t.Fatalf("unknown-command stderr = %q, want exact message", unknownCommand.stderr)
	}

	unknownFlag := executeCLI("--unknown-flag")
	if unknownFlag.exitCode != 1 {
		t.Fatalf("unknown-flag exit code = %d, want 1; stdout=%q stderr=%q", unknownFlag.exitCode, unknownFlag.stdout, unknownFlag.stderr)
	}
	if !strings.Contains(unknownFlag.stdout, "Usage:\n  yui [command]") {
		t.Fatalf("unknown-flag stdout = %q, want yui usage", unknownFlag.stdout)
	}
	if unknownFlag.stderr != "Error: unknown flag: --unknown-flag\n" {
		t.Fatalf("unknown-flag stderr = %q, want exact message", unknownFlag.stderr)
	}
}
