package agentruntime

import (
	"context"
	"errors"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/skills"
	cliTools "github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	si "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessioninstructions"
	sessioninstructionswire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessioninstructions/wire"
	"io"
	"os"
	"time"
)

// C110 Deprecated adapter: use the host-neutral sessioninstructions service for new integrations.
func RunSessionWithInstructions(ctx context.Context, out io.Writer, opts SessionRunOptions, systemPrompt string) error {
	return runWithResolvedInstructions(ctx, opts, systemPrompt, func(opts SessionRunOptions) error { return RunSession(ctx, out, opts) })
}
func RunSessionWithInstructionsAndAudioOutAndTextSeedAndMaxDuration(ctx context.Context, out io.Writer, opts SessionRunOptions, audioPath string, maxDuration time.Duration, seed SessionTextSeed, systemPrompt string) error {
	return runWithResolvedInstructions(ctx, opts, systemPrompt, func(opts SessionRunOptions) error {
		return RunSessionWithAudioOutAndTextSeedAndMaxDuration(ctx, out, opts, audioPath, maxDuration, seed)
	})
}
func runWithResolvedInstructions(ctx context.Context, opts SessionRunOptions, systemPrompt string, run func(SessionRunOptions) error) error {
	if opts.ReplayPath != "" && opts.SessionInferencer == nil {
		return run(opts)
	}
	instructions, err := resolveInstructions(ctx, opts, systemPrompt)
	if err != nil {
		return err
	}
	return run(withResolvedSessionInstructions(opts, instructions))
}
func resolveSessionInstructions(opts SessionRunOptions, systemPrompt string) (string, error) {
	return resolveInstructions(context.Background(), opts, systemPrompt)
}
func resolveInstructions(ctx context.Context, opts SessionRunOptions, systemPrompt string) (string, error) {
	request := newSessionInstructionRequest(opts, systemPrompt)
	result, resolveErr := sessioninstructionswire.NewInstructionService().Resolve(ctx, request)
	return result.Instructions, resolveErr
}
func newSessionInstructionRequest(opts SessionRunOptions, systemPrompt string) si.InstructionRequest {
	workDir := opts.WorkDir
	if workDir == "" {
		workDir = opts.ConfigDir
	}
	return si.InstructionRequest{Prompt: systemPrompt, WorkspaceDir: workDir, Loader: loader{w: workDir, c: opts.ConfigDir}, FilesystemScopeSet: opts.FilesystemPolicy != nil, FilesystemScopeDescription: opts.FilesystemPolicy.ScopeDescription()}
}
func withResolvedSessionInstructions(opts SessionRunOptions, instructions string) SessionRunOptions {
	instructions = composeSessionInstructions(opts, instructions)
	if opts.SessionInferencer != nil {
		if instructions != "" || len(opts.ToolDefinitions) > 0 {
			opts.SessionInferencer = newSessionInstructionsInferencer(opts.SessionInferencer, instructions, opts.ToolDefinitions)
		}
		return opts
	}
	if instructions != "" {
		factory := opts.runtimeFactory
		if !factory.configured() {
			factory = newDefaultSessionRuntimeFactory()
		}
		opts.runtimeFactory = sessionRuntimeFactoryWithInstructions(factory, instructions)
	}
	return opts
}

type loader struct{ w, c string }
type filesystemScopeError struct{ error }

func (e filesystemScopeError) As(target any) bool { return errors.As(e.error, target) }
func (e filesystemScopeError) Unwrap() error      { return e.error }
func (l loader) Stat(path string) error           { _, err := os.Stat(path); return err }
func closeInstructionFile(file *os.File) error    { return file.Close() }
func (l loader) ReadFile(path string) (data []byte, err error) {
	if _, err := cliTools.ResolveFilesystemPolicy(l.w); err != nil {
		return nil, filesystemScopeError{err}
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, closeInstructionFile(file)) }()
	return io.ReadAll(io.LimitReader(file, si.MaxInstructionBytes+1))
}
func (l loader) SkillsSummary() (string, error) { return skills.NewLoader(l.w, l.c).BuildSummary() }
func composeSessionInstructions(opts SessionRunOptions, instructions string) string {
	return sessioninstructionswire.NewInstructionService().Compose(si.InstructionComposition{Instructions: instructions, ToolDefinitions: opts.ToolDefinitions, BrowserCapabilityState: si.BrowserCapabilityState(string(opts.BrowserCapabilityState)), BrowserToolsEnabled: opts.BrowserToolsEnabled, PageSightToolID: cliTools.PageSightToolID})
}
