package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/probe/fleet"
	"github.com/spf13/cobra"
)

type cliExecution struct {
	exitCode int
	stdout   string
	stderr   string
}

const expectedRootHelp = "A CLI that runs Port OS agentic loops with configurable LLM providers.\n\n" +
	"Usage:\n  agent [command]\n\n" +
	"Available Commands:\n" +
	"  ask         Ask the agent a question and get a response\n" +
	"  chat        Start an interactive chat session with the agent\n" +
	"  completion  Generate the autocompletion script for the specified shell\n" +
	"  config      Configuration management commands\n" +
	"  devices     Discover available audio devices\n" +
	"  help        Help about any command\n" +
	"  interaction Inspect provider-neutral gateway interactions\n" +
	"  probe       Run deterministic offline probes\n" +
	"  session     Run or manage agent sessions\n" +
	"  tool        Invoke a tool directly by name and key=value args (for debugging)\n\n" +
	"Flags:\n" +
	"  -C, --config-dir string   Directory for agent CLI config (default: ~/.agent-cli)\n" +
	"  -h, --help                help for agent\n" +
	"      --log-to-stdout       Log to stdout/stderr instead of file (default: logs to file in config directory)\n" +
	"  -v, --verbose count       Enable verbose output (use -v for info, -vv for debug)\n\n" +
	"Use \"agent [command] --help\" for more information about a command.\n"

const expectedRootUsage = "Usage:\n  agent [command]\n\n" +
	"Available Commands:\n" +
	"  ask         Ask the agent a question and get a response\n" +
	"  chat        Start an interactive chat session with the agent\n" +
	"  completion  Generate the autocompletion script for the specified shell\n" +
	"  config      Configuration management commands\n" +
	"  devices     Discover available audio devices\n" +
	"  help        Help about any command\n" +
	"  interaction Inspect provider-neutral gateway interactions\n" +
	"  probe       Run deterministic offline probes\n" +
	"  session     Run or manage agent sessions\n" +
	"  tool        Invoke a tool directly by name and key=value args (for debugging)\n\n" +
	"Flags:\n" +
	"  -C, --config-dir string   Directory for agent CLI config (default: ~/.agent-cli)\n" +
	"  -h, --help                help for agent\n" +
	"      --log-to-stdout       Log to stdout/stderr instead of file (default: logs to file in config directory)\n" +
	"  -v, --verbose count       Enable verbose output (use -v for info, -vv for debug)\n\n" +
	"Use \"agent [command] --help\" for more information about a command.\n"

func newTestRootCommand(fleetExecutor ...fleet.EntryExecutor) *cobra.Command {
	globalFlags := flags.NewGlobalFlags()
	askFlags := flags.NewAskFlags()
	loopFlags := flags.NewLoopFlags()
	chatFlags := flags.NewChatFlags()

	router := NewRouter(
		globalFlags,
		NewRootCommand(globalFlags),
		NewAskCommand(nil, askFlags, loopFlags, globalFlags),
		NewChatCommand(nil, askFlags, loopFlags, chatFlags, globalFlags),
		NewToolCommand(globalFlags),
		NewInteractionCommand(),
		NewInteractionReplayCommand(),
		NewProbeCommand(),
		NewProbeRunCommand(),
		NewProbeGateCommand(),
		NewProbeReportCommand(),
		NewProbeFleetCommand(fleetExecutor...),
		NewSessionCommand(askFlags, globalFlags, nil, nil),
		NewSessionShowCommand(globalFlags),
		NewSessionListCommand(globalFlags),
		NewSessionDeleteCommand(globalFlags),
		NewConfigCommand(),
		NewConfigAddLocalCommand(globalFlags),
	)
	return NewAgentCLI(router).Generate()
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
	if noArgs.stdout != expectedRootHelp || help.stdout != expectedRootHelp {
		t.Fatalf("root help changed:\nno args: %q\n--help: %q\nwant: %q", noArgs.stdout, help.stdout, expectedRootHelp)
	}

	unknownCommand := executeCLI("unknown-command")
	if unknownCommand.exitCode != 1 {
		t.Fatalf("unknown-command exit code = %d, want 1; stdout=%q stderr=%q", unknownCommand.exitCode, unknownCommand.stdout, unknownCommand.stderr)
	}
	if unknownCommand.stdout != "" {
		t.Fatalf("unknown-command stdout = %q, want empty", unknownCommand.stdout)
	}
	if unknownCommand.stderr != "Error: unknown command \"unknown-command\" for \"agent\"\nRun 'agent --help' for usage.\n" {
		t.Fatalf("unknown-command stderr = %q, want exact message", unknownCommand.stderr)
	}

	unknownFlag := executeCLI("--unknown-flag")
	if unknownFlag.exitCode != 1 {
		t.Fatalf("unknown-flag exit code = %d, want 1; stdout=%q stderr=%q", unknownFlag.exitCode, unknownFlag.stdout, unknownFlag.stderr)
	}
	if unknownFlag.stdout != expectedRootUsage+"\n" {
		t.Fatalf("unknown-flag stdout = %q, want exact usage", unknownFlag.stdout)
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
		{name: "ask", args: []string{"ask", "--help"}, usage: "agent ask [prompt] [files...]", description: "One-shot queries."},
		{name: "chat", args: []string{"chat", "--help"}, usage: "agent chat", description: "Interactive multi-turn conversation."},
		{name: "tool", args: []string{"tool", "--help"}, usage: "agent tool <tool-id> [key=value...]", description: "Invoke a tool directly."},
		{name: "interaction", args: []string{"interaction", "--help"}, usage: "agent interaction", description: "Inspect provider-neutral gateway interactions."},
		{name: "interaction replay", args: []string{"interaction", "replay", "--help"}, usage: "agent interaction replay <fixture-path>", description: "Load a normalized PNIG interaction fixture"},
		{name: "probe fleet", args: []string{"probe", "fleet", "--help"}, usage: "agent probe fleet --manifest <file>", description: "Execute every entry in a fleet manifest"},
		{name: "probe report", args: []string{"probe", "report", "--help"}, usage: "agent probe report --out <result.jsonl>...", description: "Aggregate probe result artifacts into a friction report"},
		{name: "session", args: []string{"session", "--help"}, usage: "agent session", description: "Run a bidirectional session inference capture"},
		{name: "session show", args: []string{"session", "show", "--help"}, usage: "agent session show <session-id>", description: "Load and print the conversation history"},
		{name: "session list", args: []string{"session", "list", "--help"}, usage: "agent session list", description: "List session IDs with last modified time"},
		{name: "session delete", args: []string{"session", "delete", "--help"}, usage: "agent session delete <session-id>", description: "Remove the session file."},
		{name: "config", args: []string{"config", "--help"}, usage: "agent config", description: "Commands to manage agent CLI configuration."},
		{name: "config add-local", args: []string{"config", "add-local", "--help"}, usage: "agent config add-local", description: "Add a local inference provider entry"},
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
