package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/session"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	platformclock "github.com/portpowered/go-agent-harness/go-agent-loop/pkg/platform/clock"
	"github.com/spf13/cobra"
)

// SessionCommand is the session group (parent command); subcommands are wired in routes.go.
type SessionCommand struct {
	askFlags                  *flags.AskFlags
	globalFlags               *flags.GlobalFlags
	toolExecutorOverride      messages.ToolExecutor
	sessionInferencerOverride messages.SessionInferencer
	streamObserver            services.SessionStreamObserver
	clockSource               platformclock.Source
	runtimeObserver           services.SessionRuntimeObserver
	imagePaths                []string
}

// NewSessionCommand returns the session group command constructor. The tool
// executor is the single composed instance from the wire graph (the same value
// given to agent.NewExecutor); callers without one pass nil so session runs
// keep their no-tools behavior.
func NewSessionCommand(askFlags *flags.AskFlags, globalFlags *flags.GlobalFlags, toolExecutorOverride messages.ToolExecutor, sessionInferencerOverride messages.SessionInferencer) *SessionCommand {
	return NewSessionCommandWithRuntime(askFlags, globalFlags, toolExecutorOverride, sessionInferencerOverride, nil, nil)
}

// NewSessionCommandWithRuntime constructs the session command with the
// composed clock and optional runtime observation sink. The legacy constructor
// above remains for callers that do not need runtime evidence.
func NewSessionCommandWithRuntime(
	askFlags *flags.AskFlags,
	globalFlags *flags.GlobalFlags,
	toolExecutorOverride messages.ToolExecutor,
	sessionInferencerOverride messages.SessionInferencer,
	clockSource platformclock.Source,
	runtimeObserver services.SessionRuntimeObserver,
) *SessionCommand {
	return &SessionCommand{
		askFlags:                  askFlags,
		globalFlags:               globalFlags,
		toolExecutorOverride:      toolExecutorOverride,
		sessionInferencerOverride: sessionInferencerOverride,
		clockSource:               clockSource,
		runtimeObserver:           runtimeObserver,
	}
}

// SetSessionStreamObserver adds an optional observer for deltas consumed by a
// session loop. It is primarily useful to verify emitted tool-result streams
// through the CLI composition root without changing normal command output.
func (c *SessionCommand) SetSessionStreamObserver(observer services.SessionStreamObserver) {
	if c == nil {
		return
	}
	c.streamObserver = observer
}

// Generate returns the cobra command for the session group.
func (c *SessionCommand) Generate() *cobra.Command {
	var prompt string
	recordDirPath := ""
	audioOutPath := ""
	var maxDuration time.Duration
	var audioIn string
	var audioInTurns []string
	cmd := &cobra.Command{
		Use:   "session [message]",
		Short: "Run or manage agent sessions",
		Long: "Run a bidirectional session inference capture or replay a session capture file.\n" +
			"Use --record <file>.json to capture live session traffic, --record-dir <dir> for a complete both-side recording directory, or --replay <file>.json to replay a saved capture without live provider network calls.\n" +
			"Use repeatable audio-in-turn paths with record-dir to replay multiple finite spoken turns through one persistent session.\n\n" +
			"Session history management remains available through the show, list, and delete subcommands.",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := services.ValidateSessionMaxDuration(maxDuration); err != nil {
				return err
			}
			if c.askFlags.RecordCapturePath == "" && c.askFlags.ReplayCapturePath == "" && recordDirPath == "" && len(audioInTurns) == 0 {
				return cmd.Help()
			}
			sessionContext := cmd.Context()
			if maxDuration > 0 {
				capturePath := c.askFlags.RecordCapturePath
				if capturePath == "" {
					capturePath = c.askFlags.ReplayCapturePath
				}
				if capturePath != "" {
					artifactBase := strings.TrimSuffix(capturePath, filepath.Ext(capturePath))
					sessionContext = services.WithSessionDurationArtifactPaths(sessionContext, services.SessionDurationArtifactPaths{
						AudioPath:      artifactBase + ".wav",
						TranscriptPath: artifactBase + ".jsonl",
					})
				}
			}
			sessionOptions := services.SessionRunOptions{
				RecordPath:        c.askFlags.RecordCapturePath,
				ReplayPath:        c.askFlags.ReplayCapturePath,
				Provider:          c.askFlags.Provider,
				Model:             c.askFlags.Model,
				ModelProvided:     cmd.Flags().Changed("model"),
				APIKey:            c.askFlags.APIKey,
				BaseURL:           c.askFlags.BaseURL,
				ConfigDir:         c.globalFlags.ConfigDir(),
				Prompt:            strings.Join(args, " "),
				SessionInferencer: c.sessionInferencerOverride,
				ToolExecutor:      c.toolExecutorOverride,
				StreamObserver:    c.streamObserver,
				Clock:             c.clockSource,
				RuntimeObserver:   c.runtimeObserver,
			}
			seed := services.SessionTextSeed{
				Value:   prompt,
				Present: cmd.Flags().Changed("prompt"),
			}
			audioInput := services.SessionAudioInput{
				Path:          audioIn,
				Stdin:         cmd.InOrStdin(),
				Present:       cmd.Flags().Changed("audio-in"),
				DevicePresent: cmd.Flags().Lookup("audio-in-device") != nil && cmd.Flags().Changed("audio-in-device"),
			}
			if len(audioInTurns) > 0 {
				if audioInput.Present || audioInput.DevicePresent {
					return fmt.Errorf("--audio-in and --audio-in-turn cannot be used together")
				}
				if recordDirPath == "" {
					return fmt.Errorf("--audio-in-turn requires --record-dir")
				}
				if len(c.imagePaths) > 0 {
					return fmt.Errorf("--audio-in-turn cannot be combined with --image")
				}
				return services.RunSessionWithRecordingDirectoryAndInstructionsAndAudioFilesAndOutputAndTextSeedAndMaxDuration(
					sessionContext,
					cmd.OutOrStdout(),
					sessionOptions,
					recordDirPath,
					audioOutPath,
					maxDuration,
					seed,
					audioInTurns,
					c.askFlags.SystemPrompt,
				)
			}
			if len(c.imagePaths) > 0 {
				if recordDirPath != "" {
					if audioInput.Present {
						return services.RunSessionWithImagesAndRecordingDirectoryAndAudioInput(sessionContext, cmd.OutOrStdout(), services.SessionImageRunOptions{
							SessionRunOptions: sessionOptions,
							ImagePaths:        append([]string(nil), c.imagePaths...),
							AudioOutPath:      audioOutPath,
							MaxDuration:       maxDuration,
							TextSeed:          seed,
							SystemPrompt:      c.askFlags.SystemPrompt,
						}, recordDirPath, audioInput)
					}
					return services.RunSessionWithImagesAndRecordingDirectory(sessionContext, cmd.OutOrStdout(), services.SessionImageRunOptions{
						SessionRunOptions: sessionOptions,
						ImagePaths:        append([]string(nil), c.imagePaths...),
						AudioOutPath:      audioOutPath,
						MaxDuration:       maxDuration,
						TextSeed:          seed,
						SystemPrompt:      c.askFlags.SystemPrompt,
					}, recordDirPath)
				}
				if audioInput.Present {
					return services.RunSessionWithImagesAndAudioInput(sessionContext, cmd.OutOrStdout(), services.SessionImageRunOptions{
						SessionRunOptions: sessionOptions,
						ImagePaths:        append([]string(nil), c.imagePaths...),
						AudioOutPath:      audioOutPath,
						MaxDuration:       maxDuration,
						TextSeed:          seed,
						SystemPrompt:      c.askFlags.SystemPrompt,
					}, audioInput)
				}
				return services.RunSessionWithImages(sessionContext, cmd.OutOrStdout(), services.SessionImageRunOptions{
					SessionRunOptions: sessionOptions,
					ImagePaths:        append([]string(nil), c.imagePaths...),
					AudioOutPath:      audioOutPath,
					MaxDuration:       maxDuration,
					TextSeed:          seed,
					SystemPrompt:      c.askFlags.SystemPrompt,
				})
			}
			if audioInput.Present {
				if recordDirPath != "" {
					return services.RunSessionWithRecordingDirectoryAndInstructionsAndAudioInputAndOutputAndTextSeedAndMaxDuration(sessionContext, cmd.OutOrStdout(), sessionOptions, recordDirPath, audioOutPath, maxDuration, seed, audioInput, c.askFlags.SystemPrompt)
				}
				return services.RunSessionWithInstructionsAndAudioInputAndOutputAndTextSeedAndMaxDuration(sessionContext, cmd.OutOrStdout(), sessionOptions, audioOutPath, maxDuration, seed, audioInput, c.askFlags.SystemPrompt)
			}
			if recordDirPath != "" {
				return services.RunSessionWithRecordingDirectoryAndInstructionsAndAudioOutAndTextSeedAndMaxDuration(sessionContext, cmd.OutOrStdout(), sessionOptions, recordDirPath, audioOutPath, maxDuration, seed, c.askFlags.SystemPrompt)
			}
			return services.RunSessionWithInstructionsAndAudioOutAndTextSeedAndMaxDuration(sessionContext, cmd.OutOrStdout(), sessionOptions, audioOutPath, maxDuration, seed, c.askFlags.SystemPrompt)
		},
	}
	cmd.Flags().StringVar(&c.askFlags.RecordCapturePath, "record", "", "Record bidirectional session traffic to a JSON capture file")
	cmd.Flags().StringVar(&recordDirPath, "record-dir", "", "Record a complete both-side session directory separately from --record")
	cmd.Flags().StringVar(&c.askFlags.ReplayCapturePath, "replay", "", "Replay bidirectional session traffic from a JSON capture file without live provider network calls")
	cmd.Flags().StringVar(&prompt, "prompt", "", "Seed the realtime session with text")
	cmd.Flags().StringVar(&c.askFlags.SystemPrompt, "system-prompt", "", "Path to system prompt file or literal text")
	cmd.Flags().StringVar(&c.askFlags.Provider, "provider", "", "Session provider ID (use grok or openai for live record mode)")
	cmd.Flags().DurationVar(&maxDuration, "max-duration", 0, "Maximum session duration as a Go duration; exits cleanly when the bound is reached")
	cmd.Flags().StringVar(&c.askFlags.Model, "model", "", "Session model ID for live record mode")
	cmd.Flags().StringVar(&c.askFlags.APIKey, "api-key", "", "Session provider API key for live record mode")
	cmd.Flags().StringVar(&audioIn, "audio-in", "", "Stream a .wav/.pcm/.raw file incrementally; use - for raw PCM16 standard input")
	cmd.Flags().StringArrayVar(&audioInTurns, "audio-in-turn", nil, "Add a finite .wav/.pcm/.raw spoken turn to one persistent --record-dir session (repeatable)")
	cmd.Flags().StringVar(&audioOutPath, "audio-out", "", "Write assistant PCM16 audio to a .wav/.pcm/.raw path or - for stdout")
	cmd.Flags().StringVar(&c.askFlags.BaseURL, "base-url", "", "Session provider base URL override")
	cmd.Flags().StringArrayVar(&c.imagePaths, "image", nil, "Attach a local image to the realtime user turn (repeatable; order is preserved)")
	return cmd
}

// getSessionStorage resolves workspace from global flags and returns session storage.
func getSessionStorage(globalFlags *flags.GlobalFlags) (*session.Storage, error) {
	workspaceDir := globalFlags.ConfigDir()
	if workspaceDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("get workspace dir: %w", err)
		}
		workspaceDir = filepath.Join(home, config.ConfigDirName)
	}
	workspaceDir, err := filepath.Abs(workspaceDir)
	if err != nil {
		return nil, fmt.Errorf("get workspace dir: %w", err)
	}
	return session.NewStorage(workspaceDir), nil
}

// SessionShowCommand wraps the session show subcommand.
type SessionShowCommand struct {
	flags *flags.GlobalFlags
}

// NewSessionShowCommand creates the SessionShowCommand with the given flags.
func NewSessionShowCommand(flags *flags.GlobalFlags) *SessionShowCommand {
	return &SessionShowCommand{flags: flags}
}

// Generate returns the cobra command for session show.
func (c *SessionShowCommand) Generate() *cobra.Command {
	return &cobra.Command{
		Use:   "show <session-id>",
		Short: "Show a session by ID",
		Long:  "Load and print the conversation history for the given session.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			storage, err := getSessionStorage(c.flags)
			if err != nil {
				return err
			}
			sessionID := args[0]
			msgs, err := storage.Load(sessionID)
			if err != nil {
				return err
			}
			if msgs == nil {
				return fmt.Errorf("session %s not found", sessionID)
			}
			return writeSessionTo(cmd.OutOrStdout(), sessionID, msgs)
		},
	}
}

func writeSessionTo(w io.Writer, sessionID string, msgs []messages.Message) error {
	if _, err := fmt.Fprintf(w, "Session: %s\n", sessionID); err != nil {
		return err
	}
	for _, m := range msgs {
		role := string(m.Role)
		text := m.TextContent()
		if _, err := fmt.Fprintf(w, "[%s] %s\n", role, text); err != nil {
			return err
		}
	}
	return nil
}

// SessionListCommand wraps the session list subcommand.
type SessionListCommand struct {
	flags *flags.GlobalFlags
}

// NewSessionListCommand creates the SessionListCommand with the given flags.
func NewSessionListCommand(flags *flags.GlobalFlags) *SessionListCommand {
	return &SessionListCommand{flags: flags}
}

// Generate returns the cobra command for session list.
func (c *SessionListCommand) Generate() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all sessions",
		Long:  "List session IDs with last modified time, newest first.",
		RunE: func(cmd *cobra.Command, args []string) error {
			storage, err := getSessionStorage(c.flags)
			if err != nil {
				return err
			}
			infos, err := storage.List()
			if err != nil {
				return err
			}
			if len(infos) == 0 {
				_, err := fmt.Fprintln(cmd.OutOrStdout(), "No sessions found.")
				return err
			}
			for _, info := range infos {
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s  %s\n", info.ID, info.ModTime.Format(time.RFC3339)); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

// SessionDeleteCommand wraps the session delete subcommand.
type SessionDeleteCommand struct {
	flags *flags.GlobalFlags
}

// NewSessionDeleteCommand creates the SessionDeleteCommand with the given flags.
func NewSessionDeleteCommand(flags *flags.GlobalFlags) *SessionDeleteCommand {
	return &SessionDeleteCommand{flags: flags}
}

// Generate returns the cobra command for session delete.
func (c *SessionDeleteCommand) Generate() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <session-id>",
		Short: "Delete a session by ID",
		Long:  "Remove the session file. Use session list to see IDs.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			storage, err := getSessionStorage(c.flags)
			if err != nil {
				return err
			}
			sessionID := args[0]
			if err := storage.Delete(sessionID); err != nil {
				return err
			}
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "Deleted session %s\n", sessionID); err != nil {
				return err
			}
			return nil
		},
	}
}
