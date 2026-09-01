package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/spf13/cobra"
)

func TestFilesystemScopeHelpIsExplicitOnToolAndSessionCommands(t *testing.T) {
	tests := []struct {
		name    string
		command func() *cobra.Command
	}{
		{name: "direct tool", command: func() *cobra.Command { return NewToolCommand(flags.NewGlobalFlags()).Generate() }},
		{name: "session", command: func() *cobra.Command {
			return NewSessionCommand(flags.NewAskFlags(), flags.NewGlobalFlags(), nil, nil).Generate()
		}},
	}
	wants := []string{
		"--workdir <directory>",
		"process current directory",
		"--allow-path <directory>",
		"relative allow-path values resolve from the effective workdir",
		"protected system and credential locations",
		"Shell-command deny-pattern policy is separate",
		"not an operating-system sandbox",
		"allowed: agent --workdir ./project tool write_file",
		"refused: agent --workdir ./project tool write_file path=../outside.txt",
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			command := test.command()
			var help bytes.Buffer
			command.SetOut(&help)
			command.SetArgs([]string{"--help"})
			if err := command.ExecuteContext(context.Background()); err != nil {
				t.Fatalf("help: %v", err)
			}
			got := help.String()
			for _, want := range wants {
				if !strings.Contains(got, want) {
					t.Errorf("help is missing %q:\n%s", want, got)
				}
			}
			if strings.Contains(got, "All commands will be allowed") {
				t.Fatalf("help contains the removed unrestricted-command claim:\n%s", got)
			}
		})
	}
}
