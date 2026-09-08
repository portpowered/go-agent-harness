package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
	"github.com/spf13/cobra"
)

// ChatCommand wraps the chat subcommand for interactive conversations.
type ChatCommand struct {
	service      session.Service
	storeFactory session.FileStoreFactory
	askFlags     *flags.AskFlags
	loopFlags    *flags.LoopFlags
	chatFlags    *flags.ChatFlags
	globalFlags  *flags.GlobalFlags
}

// chatFlagParseError preserves Cobra's flag-error message while giving callers
// a stable type for command-level error handling.
type chatFlagParseError struct{ cause error }

func (e *chatFlagParseError) Error() string { return e.cause.Error() }
func (e *chatFlagParseError) Unwrap() error { return e.cause }

// newMicrophoneSource keeps audio-input command tests hardware-free; production
// uses the real microphone constructor by default.
var newMicrophoneSource = func() (audio.AudioSource, error) {
	return devicegw.NewMicrophoneSource()
}

//lint:ignore ST1005 the terminal admission message is an exact customer-facing CLI contract.
var errChatRequiresInteractiveTerminal = errors.New(chatInteractiveTerminalMessage)

// NewChatCommand composes the interactive transport with the runtime-owned
// durable store used by loop trace steering.
func NewChatCommand(service session.Service, askFlags *flags.AskFlags, loopFlags *flags.LoopFlags, chatFlags *flags.ChatFlags, globalFlags *flags.GlobalFlags, storeFactory session.FileStoreFactory) *ChatCommand {
	return &ChatCommand{service: service, storeFactory: storeFactory, askFlags: askFlags, loopFlags: loopFlags, chatFlags: chatFlags, globalFlags: globalFlags}
}

func validateChatFlags(cmd *cobra.Command, loopFlags *flags.LoopFlags, chatFlags *flags.ChatFlags) error {
	if !loopFlags.Loop {
		for _, flag := range []string{"max-iterations", "stop-word", "context-pressure-threshold", "context-pressure-message", "trace-id"} {
			if cmd.Flags().Changed(flag) {
				return fmt.Errorf("--%s requires --loop", flag)
			}
		}
	} else if err := validateLoopFlagRanges(loopFlags); err != nil {
		return err
	}

	if loopFlags.Loop && chatFlags.ActivateAudioIn {
		return errors.New("--activate-audio-in cannot be combined with --loop because loop steering requires an interactive terminal")
	}
	return nil
}

func validateChatInvocation(cmd *cobra.Command, loopFlags *flags.LoopFlags, chatFlags *flags.ChatFlags) error {
	if err := validateChatFlags(cmd, loopFlags, chatFlags); err != nil {
		return err
	}

	// Audio-only chat does not consume keyboard input. Every other chat flow,
	// including loop steering, must prove terminal input before constructing a
	// session or starting Bubble Tea.
	if !chatFlags.ActivateAudioIn && !chatInputIsInteractive(cmd) {
		return errChatRequiresInteractiveTerminal
	}
	return nil
}

// Generate returns the cobra command for chat.
func (c *ChatCommand) Generate() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "chat",
		Short:   "Start an interactive chat session with the agent",
		Long:    "Interactive multi-turn conversation. Type 'exit' or 'quit' to leave.\nWith --activate-audio-in the agent listens on the default microphone instead of stdin.\nWith --loop, runs in iterative mode with user steering between iterations.",
		Example: "  yui chat\n  yui chat --activate-audio-in",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateChatInvocation(cmd, c.loopFlags, c.chatFlags); err != nil {
				// Chat preflight failures are already actionable. Leave final
				// process rendering to the CLI entrypoint without Cobra's usage
				// dump or a second error prefix.
				cmd.SilenceErrors = true
				cmd.SilenceUsage = true
				return err
			}
			if c.loopFlags.Loop {
				return c.runLoopChat(cmd)
			}
			if c.chatFlags.ActivateAudioIn {
				src, err := newMicrophoneSource()
				if err != nil {
					return fmt.Errorf("open microphone: %w", err)
				}
				return RunChatWithAudio(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), c.service, c.globalFlags, c.askFlags, src)
			}
			chatService := services.NewChatService(c.service, c.globalFlags, c.askFlags)
			return chatService.Run(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
	cmd.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return &chatFlagParseError{cause: err}
	})

	cmd.Flags().BoolVar(&c.chatFlags.ActivateAudioIn, "activate-audio-in", false, "Enable audio input from the default microphone")
	cmd.Flags().BoolVar(&c.chatFlags.ActivateAudioOut, "activate-audio-out", false, "Enable audio output")

	cmd.Flags().BoolVar(&c.loopFlags.Loop, "loop", false, "Enable iterative loop mode (re-instantiates fresh sessions up to --max-iterations)")
	cmd.Flags().IntVar(&c.loopFlags.MaxIterations, "max-iterations", 5, "Maximum number of loop iterations (requires --loop)")
	cmd.Flags().StringVar(&c.loopFlags.StopWord, "stop-word", "", "Stop the loop when this word appears in the response (requires --loop)")
	cmd.Flags().Float64Var(&c.loopFlags.ContextPressureThreshold, "context-pressure-threshold", 0.8, "Context pressure threshold 0-1 that triggers a context-full warning (requires --loop)")
	cmd.Flags().StringVar(&c.loopFlags.ContextPressureMessage, "context-pressure-message", "", "Custom warning message when context pressure threshold is exceeded (requires --loop)")
	cmd.Flags().StringVar(&c.loopFlags.TraceID, "trace-id", "", "Resume an existing loop run by trace ID (requires --loop)")

	return cmd
}

// RunChatWithAudio runs an interactive audio chat session. Audio input is
// owned by the CLI host; each detected utterance is sent as one runtime turn.
func RunChatWithAudio(ctx context.Context, out, errOut io.Writer, service session.Service, globalFlags *flags.GlobalFlags, askFlags *flags.AskFlags, src audio.AudioSource) error {
	if service == nil {
		return fmt.Errorf("session service is not configured")
	}
	defer closeAudioSource(src)
	if done := writeAudioGoodbyeOnCancellation(ctx, out); done {
		return nil
	}

	cfg := services.BuildAgentConfigFromFlags(globalFlags, askFlags, nil, "")
	sessionID, done, err := createAudioChatSession(ctx, out, service, *cfg)
	if err != nil || done {
		return err
	}
	pipeline := audio.NewPipeline(src, audio.NewVAD(audio.DefaultVADConfig), audio.DefaultPipelineConfig)
	if err := writeAudioChatHeader(out); err != nil {
		return err
	}
	return runAudioChatLoop(ctx, out, errOut, service, globalFlags, askFlags, sessionID, pipeline)
}

func writeAudioGoodbyeOnCancellation(ctx context.Context, out io.Writer) bool {
	if err := ctx.Err(); err != nil {
		writeAudioBestEffort(out, "Goodbye!\n")
		return true
	}
	return false
}

func createAudioChatSession(ctx context.Context, out io.Writer, service session.Service, cfg session.Request) (string, bool, error) {
	sessionID, err := service.NewSessionID(ctx, cfg)
	if err == nil {
		return sessionID, false, nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		writeAudioBestEffort(out, "Goodbye!\n")
		return "", true, nil
	}
	return "", false, fmt.Errorf("create chat session: %w", err)
}

func writeAudioChatHeader(out io.Writer) error {
	if _, err := fmt.Fprintln(out, "Port OS Agent Chat - Audio Mode (Ctrl+C to exit)"); err != nil {
		return fmt.Errorf("write audio chat header: %w", err)
	}
	if _, err := fmt.Fprintln(out, "---"); err != nil {
		return fmt.Errorf("write audio chat header separator: %w", err)
	}
	return nil
}

func closeAudioSource(src audio.AudioSource) {
	if err := src.Close(); err != nil {
		return
	}
}

func writeAudioBestEffort(out io.Writer, format string, args ...any) {
	if _, err := fmt.Fprintf(out, format, args...); err != nil {
		return
	}
}

func runAudioChatLoop(ctx context.Context, out, errOut io.Writer, service session.Service, globalFlags *flags.GlobalFlags, askFlags *flags.AskFlags, sessionID string, pipeline *audio.Pipeline) error {
	for {
		if err := writeAudioListening(out); err != nil {
			return err
		}
		samples, done, err := readAudioUtterance(ctx, pipeline)
		if done {
			writeAudioBestEffort(out, "Goodbye!\n")
			return nil
		}
		if err != nil {
			writeAudioBestEffort(errOut, "Audio pipeline error: %v\n", err)
			continue
		}
		if _, err := fmt.Fprintln(out, "(speech detected, processing...)"); err != nil {
			return fmt.Errorf("write speech detected status: %w", err)
		}
		if err := runAudioTurn(ctx, out, errOut, service, globalFlags, askFlags, sessionID, samples); err != nil {
			return err
		}
	}
}

func writeAudioListening(out io.Writer) error {
	if _, err := fmt.Fprintln(out, "\nListening..."); err != nil {
		return fmt.Errorf("write listening status: %w", err)
	}
	return nil
}

func readAudioUtterance(ctx context.Context, pipeline *audio.Pipeline) ([]int16, bool, error) {
	samples, err := pipeline.ReadUtterance(ctx)
	if err == nil {
		return samples, false, nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.EOF) {
		return nil, true, nil
	}
	return nil, false, err
}

func runAudioTurn(ctx context.Context, out, errOut io.Writer, service session.Service, globalFlags *flags.GlobalFlags, askFlags *flags.AskFlags, sessionID string, samples []int16) error {
	execInput := agentloop.NewExecuteInput("")
	execInput.Audio = &agentloop.Audio{Samples: samples, SampleRate: audio.SampleRate, Channels: audio.Channels}
	cfg := services.BuildAgentConfigFromFlags(globalFlags, askFlags, nil, sessionID)
	cfg.Input = execInput
	result, err := service.Run(ctx, *cfg)
	if err == nil {
		_, err = fmt.Fprintln(out, result.Text)
	}
	if err != nil {
		writeAudioBestEffort(errOut, "Error: %v\n", err)
	}
	return nil
}
