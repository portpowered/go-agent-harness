package shell

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	public "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/internal/filesystem"
)

type ExecTool struct {
	workingDir          string
	timeout             time.Duration
	denyPatterns        []*regexp.Regexp
	allowPatterns       []*regexp.Regexp
	restrictToWorkspace bool
	processFactory      shellProcessFactory
}

type shellProcess interface {
	Start() error
	Wait() error
	Terminate() error
	Kill() error
}

type shellProcessFactory func(context.Context, string, string, io.Writer, io.Writer) shellProcess

type execShellProcess struct {
	cmd *exec.Cmd
}

func newExecShellProcess(ctx context.Context, cwd, command string, stdout, stderr io.Writer) shellProcess {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", command)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", command)
	}
	if cwd != "" {
		cmd.Dir = cwd
	}
	prepareCommandForTermination(cmd)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return &execShellProcess{cmd: cmd}
}

func (p *execShellProcess) Start() error {
	return p.cmd.Start()
}

func (p *execShellProcess) Wait() error {
	return p.cmd.Wait()
}

func (p *execShellProcess) Terminate() error {
	return terminateProcessTree(p.cmd)
}

func (p *execShellProcess) Kill() error {
	if p.cmd == nil || p.cmd.Process == nil {
		return nil
	}
	return p.cmd.Process.Kill()
}

const defaultShellTimeoutSeconds = 60

func defaultDenyPatterns() []*regexp.Regexp {
	return []*regexp.Regexp{
		regexp.MustCompile(`\brm\s+-[rf]{1,2}\b`),
		regexp.MustCompile(`\bdel\s+/[fq]\b`),
		regexp.MustCompile(`\brmdir\s+/s\b`),
		regexp.MustCompile(`\b(format|mkfs|diskpart)\b\s`), // Match disk wiping commands (must be followed by space/args)
		regexp.MustCompile(`\bdd\s+if=`),
		regexp.MustCompile(`>\s*/dev/sd[a-z]\b`), // Block writes to disk devices (but allow /dev/null)
		regexp.MustCompile(`\b(shutdown|reboot|poweroff)\b`),
		regexp.MustCompile(`:\(\)\s*\{.*\};\s*:`),
		regexp.MustCompile(`\$\([^)]+\)`),
		regexp.MustCompile(`\$\{[^}]+\}`),
		regexp.MustCompile("`[^`]+`"),
		regexp.MustCompile(`\|\s*sh\b`),
		regexp.MustCompile(`\|\s*bash\b`),
		regexp.MustCompile(`;\s*rm\s+-[rf]`),
		regexp.MustCompile(`&&\s*rm\s+-[rf]`),
		regexp.MustCompile(`\|\|\s*rm\s+-[rf]`),
		regexp.MustCompile(`>\s*/dev/null\s*>&?\s*\d?`),
		regexp.MustCompile(`<<\s*EOF`),
		regexp.MustCompile(`\$\(\s*cat\s+`),
		regexp.MustCompile(`\$\(\s*curl\s+`),
		regexp.MustCompile(`\$\(\s*wget\s+`),
		regexp.MustCompile(`\$\(\s*which\s+`),
		regexp.MustCompile(`\bsudo\b`),
		regexp.MustCompile(`\bchmod\s+[0-7]{3,4}\b`),
		regexp.MustCompile(`\bchown\b`),
		regexp.MustCompile(`\bpkill\b`),
		regexp.MustCompile(`\bkillall\b`),
		regexp.MustCompile(`\bkill\s+-[9]\b`),
		regexp.MustCompile(`\bcurl\b.*\|\s*(sh|bash)`),
		regexp.MustCompile(`\bwget\b.*\|\s*(sh|bash)`),
		regexp.MustCompile(`\bnpm\s+install\s+-g\b`),
		regexp.MustCompile(`\bpip\s+install\s+--user\b`),
		regexp.MustCompile(`\bapt\s+(install|remove|purge)\b`),
		regexp.MustCompile(`\byum\s+(install|remove)\b`),
		regexp.MustCompile(`\bdnf\s+(install|remove)\b`),
		regexp.MustCompile(`\bdocker\s+run\b`),
		regexp.MustCompile(`\bdocker\s+exec\b`),
		regexp.MustCompile(`\bgit\s+push\b`),
		regexp.MustCompile(`\bgit\s+force\b`),
		regexp.MustCompile(`\bssh\b.*@`),
		regexp.MustCompile(`\beval\b`),
		regexp.MustCompile(`\bsource\s+.*\.sh\b`),
	}
}

func newExecToolWithDiagnosticWriter(workingDir string, restrict bool, policy public.ExecPolicy, diagnosticWriter io.Writer) *ExecTool {
	if diagnosticWriter == nil {
		diagnosticWriter = io.Discard
	}
	denyPatterns := make([]*regexp.Regexp, 0)
	if !policy.Configured {
		denyPatterns = append(denyPatterns, defaultDenyPatterns()...)
	} else {
		if policy.EnableDenyPatterns {
			if len(policy.CustomDenyPatterns) > 0 {
				writeDiagnostic(diagnosticWriter, "Using custom deny patterns: %v\n", policy.CustomDenyPatterns)
				for _, pattern := range policy.CustomDenyPatterns {
					re, err := regexp.Compile(pattern)
					if err != nil {
						writeDiagnostic(diagnosticWriter, "Invalid custom deny pattern %q: %v\n", pattern, err)
						continue
					}
					denyPatterns = append(denyPatterns, re)
				}
			} else {
				denyPatterns = append(denyPatterns, defaultDenyPatterns()...)
			}
		} else {
			// If deny patterns are disabled, shell commands are not filtered by
			// this pattern policy. Filesystem tools retain their own boundary.
			writeDiagnostic(diagnosticWriter, "Warning: shell-command deny patterns are disabled. This affects shell-command policy only; filesystem tools remain confined to the effective filesystem scope, and the process is not running inside an operating-system sandbox.\n")
		}
	}
	return &ExecTool{
		workingDir:          workingDir,
		timeout:             defaultShellTimeoutSeconds * time.Second,
		denyPatterns:        denyPatterns,
		allowPatterns:       nil,
		restrictToWorkspace: restrict,
		processFactory:      newExecShellProcess,
	}
}

func (t *ExecTool) Name() string {
	return "exec"
}

func (t *ExecTool) Description() string {
	return "Execute a shell command on the local machine and return its output. Use with caution. Only for real shell work: never for browser-page actions, which have their own directly callable page tools."
}

func (t *ExecTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command": map[string]any{
				"type":        "string",
				"description": "The shell command to execute",
			},
			"working_dir": map[string]any{
				"type":        "string",
				"description": "Optional working directory for the command",
			},
		},
		"required": []string{"command"},
	}
}

func (t *ExecTool) Execute(ctx context.Context, args map[string]any) ([]messages.Message, error) {
	command, err := commandArgument(args)
	if err != nil {
		return nil, err
	}
	cwd, err := t.resolveWorkingDirectory(args)
	if err != nil {
		return nil, err
	}
	if guardError := t.guardCommand(command, cwd); guardError != "" {
		return nil, fmt.Errorf("%s", guardError)
	}
	output, err := t.runCommand(ctx, cwd, command)
	if err != nil {
		return nil, err
	}
	return []messages.Message{messages.NewTextMessage(messages.RoleTool, output)}, nil
}

func commandArgument(args map[string]any) (string, error) {
	command, ok := args["command"].(string)
	if !ok {
		return "", fmt.Errorf("command is required")
	}
	return command, nil
}

func (t *ExecTool) resolveWorkingDirectory(args map[string]any) (string, error) {
	cwd := t.workingDir
	wd, ok := args["working_dir"].(string)
	if ok && wd != "" {
		if t.restrictToWorkspace && t.workingDir != "" {
			resolvedWD, err := filesystem.ValidatePath(wd, t.workingDir, true)
			if err != nil {
				return "", fmt.Errorf("command blocked by safety guard: %w", err)
			}
			return resolvedWD, nil
		}
		return wd, nil
	}
	return cwd, nil
}

func (t *ExecTool) runCommand(ctx context.Context, cwd, command string) (string, error) {
	cmdCtx, cancel := t.commandContext(ctx)
	defer cancel()
	var stdout, stderr bytes.Buffer
	cmd := t.processFactory(cmdCtx, cwd, command, &stdout, &stderr)
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("failed to start command: %w", err)
	}
	err, timedOut := waitForShellProcess(cmdCtx, cmd)
	if timedOut {
		return "", fmt.Errorf("command timed out after %v", t.timeout)
	}
	output := shellOutput(stdout.String(), stderr.String(), err)
	return truncateShellOutput(output), nil
}

func (t *ExecTool) commandContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if t.timeout > 0 {
		return context.WithTimeout(ctx, t.timeout)
	}
	return context.WithCancel(ctx)
}

func waitForShellProcess(ctx context.Context, cmd shellProcess) (error, bool) {
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err, false
	case <-ctx.Done():
		terminateErr := cmd.Terminate()
		select {
		case err := <-done:
			return errors.Join(terminateErr, err), errors.Is(ctx.Err(), context.DeadlineExceeded)
		case <-time.After(2 * time.Second):
			killErr := cmd.Kill()
			return errors.Join(terminateErr, killErr, <-done), errors.Is(ctx.Err(), context.DeadlineExceeded)
		}
	}
}

func writeDiagnostic(writer io.Writer, format string, args ...any) {
	if _, err := fmt.Fprintf(writer, format, args...); err != nil {
		return
	}
}

func shellOutput(stdout, stderr string, err error) string {
	output := stdout
	if stderr != "" {
		output += "\nSTDERR:\n" + stderr
	}
	if err != nil {
		output += fmt.Sprintf("\nExit code: %v", err)
	}
	if output == "" {
		return "(no output)"
	}
	return output
}

func truncateShellOutput(output string) string {
	const maxLen = 10000
	if len(output) <= maxLen {
		return output
	}
	return output[:maxLen] + fmt.Sprintf("\n... (truncated, %d more chars)", len(output)-maxLen)
}

func (t *ExecTool) guardCommand(command, cwd string) string {
	cmd := strings.TrimSpace(command)
	lower := strings.ToLower(cmd)

	if t.matchesDenyPattern(lower) {
		return "Command blocked by safety guard (dangerous pattern detected)"
	}
	if !t.matchesAllowPattern(lower) {
		return "Command blocked by safety guard (not in allowlist)"
	}
	if message := t.workspaceGuardMessage(cmd, cwd); message != "" {
		return message
	}
	return ""
}

func (t *ExecTool) matchesDenyPattern(command string) bool {
	for _, pattern := range t.denyPatterns {
		if pattern.MatchString(command) {
			return true
		}
	}
	return false
}

func (t *ExecTool) matchesAllowPattern(command string) bool {
	if len(t.allowPatterns) == 0 {
		return true
	}
	for _, pattern := range t.allowPatterns {
		if pattern.MatchString(command) {
			return true
		}
	}
	return false
}

func (t *ExecTool) workspaceGuardMessage(command, cwd string) string {
	if !t.restrictToWorkspace {
		return ""
	}
	if strings.Contains(command, "..\\") || strings.Contains(command, "../") {
		return "Command blocked by safety guard (path traversal detected)"
	}
	cwdPath, err := filepath.Abs(cwd)
	if err != nil {
		return ""
	}
	pathPattern := regexp.MustCompile(`[A-Za-z]:\\[^\\\"']+|/[^\s\"']+`)
	for _, raw := range pathPattern.FindAllString(command, -1) {
		if pathOutsideWorkingDir(cwdPath, raw) {
			return "Command blocked by safety guard (path outside working dir)"
		}
	}
	return ""
}

func pathOutsideWorkingDir(cwdPath, raw string) bool {
	p, err := filepath.Abs(raw)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(cwdPath, p)
	return err == nil && strings.HasPrefix(rel, "..")
}

func (t *ExecTool) SetTimeout(timeout time.Duration) {
	t.timeout = timeout
}

func (t *ExecTool) SetRestrictToWorkspace(restrict bool) {
	t.restrictToWorkspace = restrict
}

func (t *ExecTool) SetAllowPatterns(patterns []string) error {
	t.allowPatterns = make([]*regexp.Regexp, 0, len(patterns))
	for _, p := range patterns {
		re, err := regexp.Compile(p)
		if err != nil {
			return fmt.Errorf("invalid allow pattern %q: %w", p, err)
		}
		t.allowPatterns = append(t.allowPatterns, re)
	}
	return nil
}
