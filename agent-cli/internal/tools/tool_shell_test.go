package tools

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

var (
	errShellStart      = errors.New("injected command not found")
	errShellExit       = errors.New("exit status 17")
	errShellTerminated = errors.New("injected process terminated")
)

type fakeShellProcess struct {
	startErr           error
	waitErr            error
	stdout             string
	stderr             string
	waitForTermination bool
	stdoutWriter       io.Writer
	stderrWriter       io.Writer
	command            string
	workingDir         string
	started            chan struct{}
	release            chan struct{}
	releaseOnce        sync.Once
	killCount          atomic.Int32
}

func newFakeShellProcess(stdout, stderr string, startErr, waitErr error, waitForTermination bool) *fakeShellProcess {
	return &fakeShellProcess{
		stdout:             stdout,
		stderr:             stderr,
		startErr:           startErr,
		waitErr:            waitErr,
		waitForTermination: waitForTermination,
		started:            make(chan struct{}),
		release:            make(chan struct{}),
	}
}

func (p *fakeShellProcess) Start() error {
	if p.startErr != nil {
		return p.startErr
	}
	if p.stdout != "" {
		_, _ = io.WriteString(p.stdoutWriter, p.stdout)
	}
	if p.stderr != "" {
		_, _ = io.WriteString(p.stderrWriter, p.stderr)
	}
	close(p.started)
	return nil
}

func (p *fakeShellProcess) Wait() error {
	if p.waitForTermination {
		<-p.release
	}
	return p.waitErr
}

func (p *fakeShellProcess) Terminate() error {
	p.killCount.Add(1)
	p.releaseOnce.Do(func() { close(p.release) })
	return nil
}

func (p *fakeShellProcess) Kill() error {
	p.killCount.Add(1)
	p.releaseOnce.Do(func() { close(p.release) })
	return nil
}

func injectFakeShellProcess(tool *ExecTool, process *fakeShellProcess) {
	tool.processFactory = func(_ context.Context, cwd, command string, stdout, stderr io.Writer) shellProcess {
		process.workingDir = cwd
		process.command = command
		process.stdoutWriter = stdout
		process.stderrWriter = stderr
		return process
	}
}

func TestExecTool_S5InjectedProcessLifecycleAndStreams(t *testing.T) {
	workDir := t.TempDir()
	process := newFakeShellProcess("stdout payload", "stderr payload", nil, nil, false)
	tool := NewExecTool("", false)
	injectFakeShellProcess(tool, process)
	tool.SetTimeout(0)

	got, err := tool.Execute(context.Background(), map[string]any{
		"command":     "synthetic command",
		"working_dir": workDir,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(got) != 1 || got[0].Role != messages.RoleTool {
		t.Fatalf("expected one tool message, got %#v", got)
	}
	if want := "stdout payload\nSTDERR:\nstderr payload"; got[0].TextContent() != want {
		t.Fatalf("output = %q, want %q", got[0].TextContent(), want)
	}
	if process.command != "synthetic command" || process.workingDir != workDir {
		t.Fatalf("process inputs = command %q, working directory %q", process.command, process.workingDir)
	}
	if got := process.killCount.Load(); got != 0 {
		t.Fatalf("successful process recorded %d kills", got)
	}
}

func TestExecTool_S4InjectedProcessOutcomes(t *testing.T) {
	const timeout = 20 * time.Millisecond

	tests := []struct {
		name          string
		process       *fakeShellProcess
		timeout       time.Duration
		cancelMidRun  bool
		wantError     string
		wantErrorIs   error
		wantOutput    string
		wantKillCount int32
	}{
		{
			name:          "non-zero exit status",
			process:       newFakeShellProcess("ran", "", nil, errShellExit, false),
			timeout:       0,
			wantOutput:    "ran\nExit code: exit status 17",
			wantKillCount: 0,
		},
		{
			name:          "command not found execution failure",
			process:       newFakeShellProcess("", "", errShellStart, nil, false),
			timeout:       0,
			wantError:     "failed to start command: injected command not found",
			wantErrorIs:   errShellStart,
			wantKillCount: 0,
		},
		{
			name:          "timeout terminates the process",
			process:       newFakeShellProcess("", "", nil, errShellTerminated, true),
			timeout:       timeout,
			wantError:     "command timed out after 20ms",
			wantKillCount: 1,
		},
		{
			name:          "context cancellation during execution",
			process:       newFakeShellProcess("", "", nil, errShellTerminated, true),
			timeout:       0,
			cancelMidRun:  true,
			wantOutput:    "\nExit code: injected process terminated",
			wantKillCount: 1,
		},
		{
			name:          "output above the size limit",
			process:       newFakeShellProcess(strings.Repeat("x", 10005), "", nil, nil, false),
			timeout:       0,
			wantOutput:    strings.Repeat("x", 10000) + "\n... (truncated, 5 more chars)",
			wantKillCount: 0,
		},
		{
			name:          "stderr-only output",
			process:       newFakeShellProcess("", "warning", nil, nil, false),
			timeout:       0,
			wantOutput:    "\nSTDERR:\nwarning",
			wantKillCount: 0,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tool := NewExecTool("", false)
			injectFakeShellProcess(tool, test.process)
			tool.SetTimeout(test.timeout)

			ctx := context.Background()
			var cancel context.CancelFunc
			if test.cancelMidRun {
				ctx, cancel = context.WithCancel(ctx)
				defer cancel()
			}

			var got []messages.Message
			var err error
			if test.cancelMidRun {
				result := make(chan struct{})
				go func() {
					got, err = tool.Execute(ctx, map[string]any{"command": "synthetic"})
					close(result)
				}()
				<-test.process.started
				cancel()
				<-result
			} else {
				got, err = tool.Execute(ctx, map[string]any{"command": "synthetic"})
			}

			if test.wantError != "" {
				if err == nil || err.Error() != test.wantError {
					t.Fatalf("error = %v, want %q", err, test.wantError)
				}
			} else if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if test.wantErrorIs != nil && !errors.Is(err, test.wantErrorIs) {
				t.Fatalf("error %v does not wrap %v", err, test.wantErrorIs)
			}
			if test.wantOutput != "" {
				if len(got) != 1 {
					t.Fatalf("messages = %#v, want one message", got)
				}
				if got[0].TextContent() != test.wantOutput {
					t.Fatalf("output = %q, want %q", got[0].TextContent(), test.wantOutput)
				}
			} else if len(got) != 0 {
				t.Fatalf("messages = %#v, want no messages", got)
			}
			if got := test.process.killCount.Load(); got != test.wantKillCount {
				t.Fatalf("kill count = %d, want %d", got, test.wantKillCount)
			}
		})
	}
}

func TestExecTool_ValidationGuardsAndConfiguration(t *testing.T) {
	ctx := context.Background()

	tool := NewExecTool("", false)
	for _, args := range []map[string]any{{}, {"command": 123}} {
		if _, err := tool.Execute(ctx, args); err == nil || err.Error() != "command is required" {
			t.Fatalf("Execute(%v) error = %v, want command-required error", args, err)
		}
	}
	if _, err := tool.Execute(ctx, map[string]any{"command": "rm -rf target"}); err == nil || !strings.Contains(err.Error(), "dangerous pattern") {
		t.Fatalf("dangerous command error = %v", err)
	}

	workspace := t.TempDir()
	restricted := NewExecTool(workspace, true)
	if _, err := restricted.Execute(ctx, map[string]any{"command": "echo ../outside"}); err == nil || !strings.Contains(err.Error(), "path traversal") {
		t.Fatalf("traversal command error = %v", err)
	}
	if _, err := restricted.Execute(ctx, map[string]any{
		"command":     "safe",
		"working_dir": filepath.Join(workspace, ".."),
	}); err == nil || !strings.Contains(err.Error(), "outside the workspace") {
		t.Fatalf("outside working directory error = %v", err)
	}
	outsidePath := filepath.Join(workspace, "..", "outside")
	if _, err := restricted.Execute(ctx, map[string]any{"command": "echo " + outsidePath}); err == nil || !strings.Contains(err.Error(), "outside working dir") {
		t.Fatalf("outside command path error = %v", err)
	}

	process := newFakeShellProcess("allowed", "", nil, nil, false)
	allowed := NewExecTool("", false)
	injectFakeShellProcess(allowed, process)
	if err := allowed.SetAllowPatterns([]string{"["}); err == nil {
		t.Fatal("SetAllowPatterns accepted an invalid pattern")
	}
	if err := allowed.SetAllowPatterns([]string{"^safe$"}); err != nil {
		t.Fatalf("SetAllowPatterns: %v", err)
	}
	if _, err := allowed.Execute(ctx, map[string]any{"command": "blocked"}); err == nil || !strings.Contains(err.Error(), "not in allowlist") {
		t.Fatalf("allowlist error = %v", err)
	}
	allowed.SetRestrictToWorkspace(true)
	allowed.SetRestrictToWorkspace(false)
	msgs, err := allowed.Execute(ctx, map[string]any{"command": "safe"})
	if err != nil || len(msgs) != 1 || msgs[0].TextContent() != "allowed" {
		t.Fatalf("allowed command = %#v, %v", msgs, err)
	}
	emptyProcess := newFakeShellProcess("", "", nil, nil, false)
	emptyTool := NewExecTool("", false)
	injectFakeShellProcess(emptyTool, emptyProcess)
	emptyTool.SetTimeout(0)
	msgs, err = emptyTool.Execute(ctx, map[string]any{"command": "empty"})
	if err != nil || len(msgs) != 1 || msgs[0].TextContent() != "(no output)" {
		t.Fatalf("empty command = %#v, %v", msgs, err)
	}

	custom := &config.Config{Tools: config.ToolsConfig{Exec: config.ExecConfig{
		EnableDenyPatterns: true,
		CustomDenyPatterns: []string{"forbidden", "["},
	}}}
	customTool := NewExecToolWithConfig("", false, custom)
	if _, err := customTool.Execute(ctx, map[string]any{"command": "forbidden"}); err == nil || !strings.Contains(err.Error(), "dangerous pattern") {
		t.Fatalf("custom deny error = %v", err)
	}
	defaults := &config.Config{Tools: config.ToolsConfig{Exec: config.ExecConfig{EnableDenyPatterns: true}}}
	defaultTool := NewExecToolWithConfig("", false, defaults)
	if _, err := defaultTool.Execute(ctx, map[string]any{"command": "rm -rf target"}); err == nil || !strings.Contains(err.Error(), "dangerous pattern") {
		t.Fatalf("default deny error = %v", err)
	}

	disabled := &config.Config{Tools: config.ToolsConfig{Exec: config.ExecConfig{EnableDenyPatterns: false}}}
	disabledProcess := newFakeShellProcess("deny disabled", "", nil, nil, false)
	disabledTool := NewExecToolWithConfig("", false, disabled)
	injectFakeShellProcess(disabledTool, disabledProcess)
	msgs, err = disabledTool.Execute(ctx, map[string]any{"command": "rm -rf target"})
	if err != nil || len(msgs) != 1 || msgs[0].TextContent() != "deny disabled" {
		t.Fatalf("disabled deny command = %#v, %v", msgs, err)
	}
}

func TestExecTool_Metadata(t *testing.T) {
	tool := NewExecTool("workspace", true)
	if tool.Name() != "exec" {
		t.Errorf("Name = %q, want exec", tool.Name())
	}
	if tool.Description() == "" {
		t.Error("Description should not be empty")
	}
	params := tool.Parameters()
	if params["type"] != "object" || params["properties"] == nil || params["required"] == nil {
		t.Fatalf("unexpected parameters: %#v", params)
	}
}

func TestExecTool_DefaultProcessFactoryDoesNotStart(t *testing.T) {
	workDir := t.TempDir()
	tool := NewExecTool(workDir, false)
	process := tool.processFactory(context.Background(), workDir, "synthetic", io.Discard, io.Discard)
	if _, ok := process.(*execShellProcess); !ok {
		t.Fatalf("default process type = %T, want *execShellProcess", process)
	}
	if err := process.Wait(); err == nil {
		t.Fatal("Wait on an unstarted process should fail")
	}
	if err := process.Terminate(); err != nil {
		t.Fatalf("Terminate: %v", err)
	}
	if err := process.Kill(); err != nil {
		t.Fatalf("Kill: %v", err)
	}
}
