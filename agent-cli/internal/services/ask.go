package services

import (
	"fmt"
	"io"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/input"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
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
	fileParts, err := input.LoadAskContentParts(filePaths)
	if err != nil {
		return agentloop.ExecuteInput{}, err
	}

	prompt, stdinPart, err := applyStdinInput(stdin, prompt)
	if err != nil {
		return agentloop.ExecuteInput{}, err
	}
	if prompt == "" && len(filePaths) == 0 && stdinPart == nil {
		return agentloop.ExecuteInput{}, fmt.Errorf("no prompt: provide a prompt as an argument or pipe text via stdin")
	}
	execInput := agentloop.NewExecuteInput(prompt)
	if stdinPart != nil {
		execInput.ContentParts = append(execInput.ContentParts, stdinPart)
	}
	execInput.ContentParts = append(execInput.ContentParts, fileParts...)
	return execInput, nil
}

// applyStdinInput reads piped stdin (when present). Binary stdin becomes a
// content part; text stdin becomes the prompt context ahead of the argument
// instruction.
func applyStdinInput(stdin io.Reader, prompt string) (string, messages.ContentPart, error) {
	if stdin == nil {
		return prompt, nil, nil
	}
	stdinContent, err := input.ReadStdinContent(stdin)
	if err != nil {
		return "", nil, fmt.Errorf("read stdin: %w", err)
	}
	switch {
	case stdinContent.Part != nil:
		return prompt, stdinContent.Part, nil
	case stdinContent.Text == "":
		return prompt, nil, nil
	case prompt != "":
		// stdin is the context; the arg is the instruction.
		return stdinContent.Text + "\n\n" + prompt, nil, nil
	default:
		return stdinContent.Text, nil, nil
	}
}

// BuildAgentConfigFromFlags converts CLI flags to the public, normalized
// session request. Global flags are consumed by the host resolver injected at
// composition time; they do not cross into the runtime request.
func BuildAgentConfigFromFlags(_ *flags.GlobalFlags, askFlags *flags.AskFlags, initialHistory []messages.Message, sessionID string) *session.Request {
	cfg := &session.Request{
		SystemPrompt:          askFlags.SystemPrompt,
		SessionID:             sessionID,
		ContinueLastSession:   askFlags.ContinueLastSession,
		InitialHistory:        initialHistory,
		Model:                 askFlags.Model,
		Provider:              askFlags.Provider,
		APIKey:                askFlags.APIKey,
		BaseURL:               askFlags.BaseURL,
		OutputModality:        askFlags.OutputModality,
		ModelConfig:           askFlags.ModelConfig,
		OutputReasoningTokens: askFlags.OutputReasoningTokens,
		RecordCapturePath:     askFlags.RecordCapturePath,
		ReplayCapturePath:     askFlags.ReplayCapturePath,
	}
	if askFlags.SessionID != "" {
		cfg.SessionID = askFlags.SessionID
	}
	return cfg
}

