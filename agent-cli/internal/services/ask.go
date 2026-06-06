package services

import (
	"fmt"
	"io"

	"github.com/portpowered/agent-cli/internal/agent"
	"github.com/portpowered/agent-cli/internal/flags"
	"github.com/portpowered/agent-cli/internal/input"
	"github.com/portpowered/agent-cli/internal/tools"
	"github.com/portpowered/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-loop/pkg/messages"
)

// BuildExecuteInput constructs an ExecuteInput from optional piped stdin, a text arg prompt, and file paths.
// Pass nil for stdin when stdin is not piped; whitespace-only stdin is treated as absent.
//
// Stdin is classified by content type before use:
//   - text/plain → combined with argPrompt as text (stdin is context, arg is instruction)
//   - image/*    → added as an ImagePart in ContentParts
//   - audio/*    → added as an AudioPart in ContentParts
//   - video/*    → added as a VideoPart in ContentParts
//   - other      → added as a FilePart (Name: "stdin") in ContentParts
//
// Returns an error if no prompt and no content (file paths or binary stdin) are provided.
func BuildExecuteInput(stdin io.Reader, argPrompt string, filePaths []string) (agentloop.ExecuteInput, error) {
	prompt := argPrompt
	var stdinPart messages.ContentPart
	if stdin != nil {
		stdinContent, err := input.ReadStdinContent(stdin)
		if err != nil {
			return agentloop.ExecuteInput{}, fmt.Errorf("read stdin: %w", err)
		}
		if stdinContent.Part != nil {
			stdinPart = stdinContent.Part
		} else if stdinContent.Text != "" {
			if prompt != "" {
				// stdin is the context; the arg is the instruction.
				prompt = stdinContent.Text + "\n\n" + prompt
			} else {
				prompt = stdinContent.Text
			}
		}
	}
	if prompt == "" && len(filePaths) == 0 && stdinPart == nil {
		return agentloop.ExecuteInput{}, fmt.Errorf("no prompt: provide a prompt as an argument or pipe text via stdin")
	}
	execInput := agentloop.NewExecuteInput(prompt)
	if stdinPart != nil {
		execInput.ContentParts = append(execInput.ContentParts, stdinPart)
	}
	for _, path := range filePaths {
		part, err := input.LoadContentPart(path)
		if err != nil {
			return agentloop.ExecuteInput{}, fmt.Errorf("load file %s: %w", path, err)
		}
		execInput.ContentParts = append(execInput.ContentParts, part)
	}
	return execInput, nil
}

// BuildAgentConfigFromFlags converts CLI flags to agent.Config for use by the agent package.
func BuildAgentConfigFromFlags(globalFlags *flags.GlobalFlags, askFlags *flags.AskFlags, initialHistory []messages.Message, sessionID string) *agent.Config {
	cfg := &agent.Config{
		SystemPrompt:          askFlags.SystemPrompt,
		NoSystemInformation:   askFlags.NoSystemInformation,
		SessionID:             sessionID,
		ContinueLastSession:   askFlags.ContinueLastSession,
		InitialHistory:        initialHistory,
		Model:                 askFlags.Model,
		Provider:              askFlags.Provider,
		APIKey:                askFlags.APIKey,
		BaseURL:               askFlags.BaseURL,
		Stream:                askFlags.Stream,
		OutputJSON:            askFlags.OutputJSON,
		OutputReasoningTokens: askFlags.OutputReasoningTokens,
		OutputModality:        askFlags.OutputModality,
		ModelConfig:           askFlags.ModelConfig,
		RecordCapturePath:     askFlags.RecordCapturePath,
		ReplayCapturePath:     askFlags.ReplayCapturePath,
	}
	if globalFlags != nil {
		cfg.ConfigDir = globalFlags.ConfigDir()
		cfg.Verbose = globalFlags.VerboseMode > 0
		cfg.VerbosityLevel = globalFlags.VerboseMode
		cfg.LogToStdout = globalFlags.LogToStdout
	}
	if askFlags.SessionID != "" {
		cfg.SessionID = askFlags.SessionID
	}
	return cfg
}

// DefaultToolDefs returns the default tool definitions for use with agent.NewExecutor.
func DefaultToolDefs(registry *tools.ToolRegistry) []messages.ToolDefinition {
	return registry.ToAgentLoopDefs()
}
