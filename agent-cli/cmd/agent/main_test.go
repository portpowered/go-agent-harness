package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	agentMainTestHelperEnv = "AGENT_CLI_MAIN_TEST_HELPER"
	agentMainTestArgsEnv   = "AGENT_CLI_MAIN_TEST_ARGS"
)

func TestAgentExecutableRendersExistingCommandFailuresOnce(t *testing.T) {
	if os.Getenv(agentMainTestHelperEnv) == "1" {
		var args []string
		if err := json.Unmarshal([]byte(os.Getenv(agentMainTestArgsEnv)), &args); err != nil {
			fmt.Fprintln(os.Stderr, "decode CLI test arguments:", err)
			os.Exit(2)
		}
		os.Args = append([]string{"agent"}, args...)
		main()
		return
	}

	tests := []struct {
		name string
		args func(string) []string
		want string
	}{
		{
			name: "ask",
			args: func(configDir string) []string {
				return []string{
					"--config-dir", configDir,
					"ask", "--record", filepath.Join(configDir, "record.json"),
					"--replay", filepath.Join(configDir, "replay.json"),
				}
			},
			want: "cannot use --record and --replay together",
		},
		{
			name: "chat",
			args: func(configDir string) []string {
				return []string{"--config-dir", configDir, "chat"}
			},
			want: "agent chat requires an interactive terminal",
		},
		{
			name: "probe report",
			args: func(configDir string) []string {
				return []string{
					"--config-dir", configDir,
					"probe", "report", "--out", filepath.Join(configDir, "missing.jsonl"),
				}
			},
			want: "open input",
		},
		{
			name: "session",
			args: func(configDir string) []string {
				return []string{"--config-dir", configDir, "session", "--transport", "quic"}
			},
			want: `--transport must be one of "ws" or "webrtc", got "quic"`,
		},
		{
			name: "probe run",
			args: func(configDir string) []string {
				return []string{
					"--config-dir", configDir,
					"probe", "run", "missing-scenario.json", "--replay", "missing-fixture.session.json",
				}
			},
			want: `replay fixture "missing-fixture.session.json" is missing or unreadable`,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			stdout, stderr, exitCode := runAgentMainTestProcess(t, testCase.args(t.TempDir()))
			if exitCode != 1 {
				t.Fatalf("exit code = %d, want 1; stdout=%q stderr=%q", exitCode, stdout, stderr)
			}
			if stdout != "" {
				t.Fatalf("stdout = %q, want empty", stdout)
			}
			if got := strings.Count(stderr, "Error:"); got != 1 {
				t.Fatalf("customer-facing Error: count = %d, want 1; stderr=%q", got, stderr)
			}
			if got := strings.Count(stderr, testCase.want); got != 1 {
				t.Fatalf("failure text %q count = %d, want 1; stderr=%q", testCase.want, got, stderr)
			}
			if strings.Contains(stderr, "Usage:") {
				t.Fatalf("ordinary runtime failure unexpectedly included usage: %q", stderr)
			}
		})
	}
}

func runAgentMainTestProcess(t *testing.T, args []string) (string, string, int) {
	t.Helper()
	encodedArgs, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("encode CLI test arguments: %v", err)
	}

	command := exec.Command(os.Args[0], "-test.run=TestAgentExecutableRendersExistingCommandFailuresOnce")
	command.Env = append(append([]string{}, os.Environ()...),
		agentMainTestHelperEnv+"=1",
		agentMainTestArgsEnv+"="+string(encodedArgs),
	)
	command.Stdin = strings.NewReader("")
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr

	execErr := command.Run()
	if execErr == nil {
		return stdout.String(), stderr.String(), 0
	}
	var exitErr *exec.ExitError
	if !errors.As(execErr, &exitErr) {
		t.Fatalf("run CLI test process: %v", execErr)
	}
	return stdout.String(), stderr.String(), exitErr.ExitCode()
}
