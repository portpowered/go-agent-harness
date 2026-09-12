package agentruntime

import (
	"context"
	"fmt"

	cliTools "github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessioninstructions"
	sessioninstructionswire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessioninstructions/wire"
)

// resolveSessionInstructions is a compatibility adapter around the reusable
// session instruction service. Filesystem policy normalization remains at the
// CLI host edge; prompt selection, skills ordering, scope formatting and all
// model-facing policy decisions live behind the runtime contract.
// Deprecated: this adapter remains only for the CLI compatibility surface.
func resolveSessionInstructions(opts SessionRunOptions, systemPrompt string) (string, error) {
	return resolveSessionInstructionsWithContext(context.Background(), opts, systemPrompt)
}

func resolveSessionInstructionsWithContext(ctx context.Context, opts SessionRunOptions, systemPrompt string) (string, error) {
	request, err := newSessionInstructionRequest(opts, systemPrompt)
	if err != nil {
		return "", err
	}
	result, err := sessioninstructionswire.NewInstructionService().Resolve(ctx, request)
	if err != nil {
		return "", err
	}
	return result.Instructions, nil
}

func newSessionInstructionRequest(opts SessionRunOptions, systemPrompt string) (sessioninstructions.InstructionRequest, error) {
	workDir := opts.WorkDir
	if workDir == "" && opts.FilesystemPolicy == nil {
		// Preserve the direct service API's historical workspace behavior. CLI
		// sessions always supply the launch-captured policy explicitly.
		workDir = opts.ConfigDir
	}
	if workDir != "" && opts.FilesystemPolicy == nil {
		// Validate the host-selected workspace before attempting prompt
		// discovery. A missing workspace is a startup/configuration error, not
		// an empty prompt, and must prevent provider/session admission.
		policy, policyErr := cliTools.ResolveFilesystemPolicy(workDir, opts.AllowPaths...)
		if policyErr != nil {
			return sessioninstructions.InstructionRequest{}, fmt.Errorf("resolve filesystem scope: %w", policyErr)
		}
		workDir = policy.PrimaryRoot()
	}
	request := sessioninstructions.InstructionRequest{
		Prompt:       systemPrompt,
		WorkspaceDir: workDir,
		Loader:       sessionInstructionLoader{workspaceDir: workDir, configDir: opts.ConfigDir},
	}
	if opts.FilesystemPolicy != nil {
		request.FilesystemScopeSet = true
		request.FilesystemScopeDescription = opts.FilesystemPolicy.ScopeDescription()
	}
	return request, nil
}

// composeSessionInstructions is a compatibility adapter around the runtime
// service. The CLI keeps this planner seam but retains no policy copy.
func composeSessionInstructions(opts SessionRunOptions, instructions string) string {
	return sessioninstructionswire.NewInstructionService().Compose(sessioninstructions.InstructionComposition{
		Instructions:           instructions,
		ToolDefinitions:        append([]messages.ToolDefinition(nil), opts.ToolDefinitions...),
		BrowserCapabilityState: sessioninstructions.BrowserCapabilityState(string(opts.BrowserCapabilityState)),
		BrowserToolsEnabled:    opts.BrowserToolsEnabled,
		PageSightToolID:        cliTools.PageSightToolID,
	})
}
