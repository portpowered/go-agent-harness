package cli

import sessionclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"

import sessionservicewire "github.com/portpowered/go-agent-harness/agent-cli/internal/services/wire"

import (
	"bytes"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/probe/fleet"
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
	return newTestRootCommandWithProbeFleetCommand(NewProbeFleetCommand(nil, nil, fleetExecutor...))
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

	router := NewRouter(
		globalFlags,
		NewRootCommand(globalFlags),
		NewAskCommand(nil, askFlags, loopFlags, globalFlags),
		NewChatCommand(nil, askFlags, loopFlags, chatFlags, globalFlags),
		NewToolCommand(globalFlags),
		NewInteractionCommand(),
		NewInteractionReplayCommand(),
		NewProbeCommand(),
		NewProbeRunCommandWithDeviceService(newDevicesTestService(), nil, sessionservicewire.NewMetricsCollector(sessionclock.Real{}, sessionservicewire.NewSessionRuntimeFactory())),
		NewProbeGateCommand(),
		NewProbeReportCommand(),
		probeFleetCommand,
		NewSessionCommand(askFlags, globalFlags, newTestSessionService(sessionservicewire.SessionDependencies{Clock: sessionclock.Real{}, SessionInferencer: injectedSessionInferencer, DeviceRegistry: registry}), nil),
		NewSessionShowCommand(globalFlags),
		NewSessionListCommand(globalFlags),
		NewSessionDeleteCommand(globalFlags),
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

func TestRouteHelpExecutionContracts(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		usage       string
		description string
	}{
		{name: "ask", args: []string{"ask", "--help"}, usage: "yui ask [prompt] [files...]", description: "One-shot queries."},
		{name: "chat", args: []string{"chat", "--help"}, usage: "yui chat", description: "Interactive multi-turn conversation."},
		{name: "tool", args: []string{"tool", "--help"}, usage: "yui tool <tool-id> [key=value...]", description: "Invoke a tool directly"},
		{name: "interaction", args: []string{"interaction", "--help"}, usage: "yui interaction", description: "Inspect provider-neutral gateway interactions."},
		{name: "interaction replay", args: []string{"interaction", "replay", "--help"}, usage: "yui interaction replay <fixture-path>", description: "Load a normalized PNIG interaction fixture"},
		{name: "probe fleet", args: []string{"probe", "fleet", "--help"}, usage: "yui probe fleet --manifest <file>", description: "Execute every entry in a fleet manifest"},
		{name: "media", args: []string{"media", "--help"}, usage: "yui media", description: "Inspect external media sources"},
		{name: "media probe", args: []string{"media", "probe", "--help"}, usage: "yui media probe <url>", description: "Probe an external go2rtc or RTSP media source"},
		{name: "media look", args: []string{"media", "look", "--help"}, usage: "yui media look <url>", description: "Observe one visual frame from an external media source"},
		{name: "probe report", args: []string{"probe", "report", "--help"}, usage: "yui probe report --out <result.jsonl>...", description: "Aggregate probe result artifacts into a friction report"},
		{name: "room", args: []string{"room", "--help"}, usage: "yui room", description: "Run participant rooms"},
		{name: "room run", args: []string{"room", "run", "--help"}, usage: "yui room run [--config <file>] [--replay <bundle>] [--out <dir>] [--stream <addr>]", description: "Run an N-participant room from --config (or the legacy --manifest spelling)."},
		{name: "session", args: []string{"session", "--help"}, usage: "yui session", description: "Run a bidirectional session inference capture"},
		{name: "session show", args: []string{"session", "show", "--help"}, usage: "yui session show <session-id>", description: "Load and print the conversation history"},
		{name: "session list", args: []string{"session", "list", "--help"}, usage: "yui session list", description: "List session IDs with last modified time"},
		{name: "session delete", args: []string{"session", "delete", "--help"}, usage: "yui session delete <session-id>", description: "Remove the session file."},
		{name: "config", args: []string{"config", "--help"}, usage: "yui config", description: "Commands to manage agent CLI configuration."},
		{name: "config add-local", args: []string{"config", "add-local", "--help"}, usage: "yui config add-local", description: "Add a local inference provider entry"},
		{name: "webmcp", args: []string{"webmcp", "--help"}, usage: "yui webmcp", description: "Inspect WebMCP browser readiness"},
		{name: "webmcp doctor", args: []string{"webmcp", "doctor", "--help"}, usage: "yui webmcp doctor", description: "Diagnose WebMCP browser readiness"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := executeCLI(tt.args...)
			if result.exitCode != 0 {
				t.Fatalf("exit code = %d, want 0; stdout=%q stderr=%q", result.exitCode, result.stdout, result.stderr)
			}
			if result.stdout == "" {
				t.Fatal("stdout is empty")
			}
			if result.stderr != "" {
				t.Fatalf("stderr = %q, want empty", result.stderr)
			}
			if !strings.Contains(result.stdout, "Usage:\n  "+tt.usage) {
				t.Fatalf("stdout = %q, want usage path %q", result.stdout, tt.usage)
			}
			if !strings.Contains(result.stdout, tt.description) {
				t.Fatalf("stdout = %q, want description %q", result.stdout, tt.description)
			}
		})
	}
}
