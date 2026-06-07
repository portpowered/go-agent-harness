package agent

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	"github.com/portpowered/agent-cli/internal/config"
	"github.com/portpowered/agent-cli/internal/execctx"
	"github.com/portpowered/agent-cli/internal/input"
	"github.com/portpowered/agent-cli/internal/logger"
	"github.com/portpowered/agent-cli/internal/output"
	"github.com/portpowered/agent-cli/internal/session"
	"github.com/portpowered/agent-cli/internal/skills"
	"github.com/portpowered/agent-cli/internal/sysinfo"
	"github.com/portpowered/agent-cli/internal/tools"
	"github.com/portpowered/agent-cli/internal/workspace"
	"github.com/portpowered/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-llm-gateway/pkg/gateway"
	"github.com/portpowered/go-llm-gateway/pkg/inference"
	"github.com/portpowered/go-llm-gateway/pkg/testing"
)

// RunData holds the runtime data for an agent execution.
type RunData struct {
	SessionID      string
	SessionManager *session.Storage
	Loop           *agentloop.AgentLoop
	Recorder       *testing.RecordRoundTripper
	Models         *config.ModelsConfig
	LoggerCloser   io.Closer // closed when the run finishes so the log file handle is released
	ConfigDir      string    // config directory for skills and context
}

// CloseLogger releases the log file handle if the run used file logging. Safe to call if LoggerCloser is nil.
func (r *RunData) CloseLogger() {
	if r != nil && r.LoggerCloser != nil {
		_ = r.LoggerCloser.Close()
		r.LoggerCloser = nil
	}
}

// Executor constructs and executes agent loops based on configuration.
type Executor struct {
	executor             messages.ToolExecutor
	toolDefs             []messages.ToolDefinition
	inferencerOverride   messages.Inferencer
	relaxModelValidation bool
}

// NewExecutor creates a new Executor with the given dependencies.
func NewExecutor(executor messages.ToolExecutor, toolDefs []messages.ToolDefinition, inferencerOverride messages.Inferencer, relaxModelValidation ...bool) *Executor {
	relax := false
	if len(relaxModelValidation) > 0 {
		relax = relaxModelValidation[0]
	}
	return &Executor{
		executor:             executor,
		toolDefs:             toolDefs,
		inferencerOverride:   inferencerOverride,
		relaxModelValidation: relax,
	}
}

// loadConfig loads the configuration from disk, applying CLI overrides and
// validating provider requirements for real executions.
func (e *Executor) loadConfig(cfg *Config) (*config.Config, error) {
	return e.loadConfigWithOptions(cfg, true)
}

// loadConfigAllowingInferencerOverride loads config even when provider
// credentials are intentionally absent because a test inferencer override will
// satisfy execution.
func (e *Executor) loadConfigAllowingInferencerOverride(cfg *Config) (*config.Config, error) {
	return e.loadConfigWithOptions(cfg, e.inferencerOverride == nil)
}

func (e *Executor) loadConfigWithOptions(cfg *Config, validate bool) (*config.Config, error) {
	configDir := cfg.ConfigDir
	storage, err := config.NewDefaultConfigStorage(configDir)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize config: %w", err)
	}

	loadedCfg, err := storage.Load()
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}

	// Apply CLI flag overrides (--api-key, --model, --provider, --base-url)
	if cfg.APIKey != "" || cfg.Model != "" || cfg.Provider != "" || cfg.BaseURL != "" {
		data := loadedCfg.ApplyOverrides(cfg.APIKey, cfg.Model, cfg.Provider, cfg.BaseURL)
		loadedCfg = &data
	}

	shouldValidate := validate && !e.relaxModelValidation
	if e.inferencerOverride != nil && cfg.ConfigDir == "" {
		shouldValidate = false
	}
	if shouldValidate {
		if err := loadedCfg.Validate(); err != nil {
			return nil, err
		}
	}

	return loadedCfg, nil
}

// GetSessionStorage returns a session storage instance.
func (e *Executor) GetSessionStorage(cfg *Config) (*session.Storage, error) {
	return e.getSessionStorage(cfg)
}

// getSessionStorage returns a session storage instance.
func (e *Executor) getSessionStorage(cfg *Config) (*session.Storage, error) {
	configDir := cfg.ConfigDir
	workspaceDir := configDir
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

// getInitialHistory resolves the session ID and loads initial history based on config.
func (e *Executor) getInitialHistory(cfg *Config, sessionStorage *session.Storage) ([]messages.Message, string, error) {
	var initialHistory []messages.Message
	var sessionID string
	var err error

	if cfg.SessionID != "" {
		sessionID = cfg.SessionID
		initialHistory, err = sessionStorage.Load(sessionID)
		if err != nil {
			return nil, "", fmt.Errorf("load session %s: %w", sessionID, err)
		}
	} else if cfg.ContinueLastSession {
		sessionID, err = sessionStorage.Latest()
		if err != nil {
			return nil, "", fmt.Errorf("find latest session: %w", err)
		}
		if sessionID == "" {
			return nil, "", fmt.Errorf("no previous session to continue (use --session-id or run an ask first)")
		}
		initialHistory, err = sessionStorage.Load(sessionID)
		if err != nil {
			return nil, "", fmt.Errorf("load latest session: %w", err)
		}
	} else if len(cfg.InitialHistory) > 0 {
		// Use provided initial history (for chat with explicit session)
		initialHistory = cfg.InitialHistory
		sessionID = cfg.SessionID
		if sessionID == "" {
			sessionID = sessionStorage.NewSessionID()
		}
	} else {
		sessionID = sessionStorage.NewSessionID()
	}

	return initialHistory, sessionID, nil
}

// LoadSystemPrompt loads and resolves the system prompt from config or file.
// It is exported so callers (e.g. /system command) can display the resolved prompt.
func (e *Executor) LoadSystemPrompt(cfg *Config, workspaceDir string, toolDefs []messages.ToolDefinition) (string, error) {
	return e.loadSystemPrompt(cfg, workspaceDir, toolDefs)
}

// loadSystemPrompt loads the system prompt from config or file.
func (e *Executor) loadSystemPrompt(cfg *Config, workspaceDir string, toolDefs []messages.ToolDefinition) (string, error) {
	systemPrompt := ""
	if cfg.SystemPrompt == "none" {
		// Explicitly no system prompt
	} else if cfg.SystemPrompt != "" {
		if _, err := os.Stat(cfg.SystemPrompt); err == nil {
			data, err := os.ReadFile(cfg.SystemPrompt)
			if err != nil {
				return "", fmt.Errorf("read system prompt %s: %w", cfg.SystemPrompt, err)
			}
			systemPrompt = string(data)
		} else if os.IsNotExist(err) {
			systemPrompt = cfg.SystemPrompt
		} else {
			return "", fmt.Errorf("system prompt path %s: %w", cfg.SystemPrompt, err)
		}
	} else {
		// Default: use AGENTS.md from workspace
		if err := workspace.EnsureAgentsMD(workspaceDir, toolDefs); err != nil {
			return "", fmt.Errorf("initialize AGENTS.md: %w", err)
		}
		agentsPath := filepath.Join(workspaceDir, "AGENTS.md")
		if data, err := os.ReadFile(agentsPath); err == nil {
			systemPrompt = string(data)
		}
	}

	// Prepend dynamic system info unless explicitly disabled.
	if !cfg.NoSystemInformation {
		var model, provider string
		loadedCfg, err := e.loadConfigAllowingInferencerOverride(cfg)
		if err == nil {
			provider = loadedCfg.Model.Provider
			if provider == "fal" && loadedCfg.Model.Fal != nil {
				model = loadedCfg.Model.Fal.Model
			} else if active, err := loadedCfg.ActiveOpenAIConfig(); err == nil {
				model = active.Model
			}
		}
		systemPrompt = sysinfo.Collect(model, provider).Format() + systemPrompt
	}

	// Append skills metadata (name + description) so the model knows available skills.
	if workspaceDir != "" {
		configSkillsDir := cfg.ConfigDir
		loader := skills.NewLoader(workspaceDir, configSkillsDir)
		summary, err := loader.BuildSummary()
		if err == nil && summary != "" {
			systemPrompt = systemPrompt + "\n\n---\n\n" + summary
		}
	}

	// Append iteration-specific suffix (for loop mode).
	if cfg.SystemPromptSuffix != "" {
		if systemPrompt != "" {
			systemPrompt = systemPrompt + "\n\n" + cfg.SystemPromptSuffix
		} else {
			systemPrompt = cfg.SystemPromptSuffix
		}
	}

	return systemPrompt, nil
}

// BuildLoop constructs an agent loop from the given configuration.
func (e *Executor) BuildLoop(ctx context.Context, cfg *Config) (*RunData, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	loadedCfg, err := e.loadConfigAllowingInferencerOverride(cfg)
	if err != nil {
		return nil, err
	}

	// Set up logger context
	verbosityLevel := cfg.VerbosityLevel
	if cfg.Verbose && verbosityLevel == 0 {
		// Backward compatibility: if Verbose is true but VerbosityLevel is 0, set to 1
		verbosityLevel = 1
	}

	zapLogger, loggerCloser := logger.NewVerboseLoggerWithCloser(verbosityLevel, cfg.ConfigDir, cfg.LogToStdout)
	ctx = logger.WithLogger(ctx, zapLogger)

	// Build inferencer
	var inf messages.Inferencer
	var recordRT *testing.RecordRoundTripper
	if e.inferencerOverride != nil {
		inf = e.inferencerOverride
	} else {
		httpRuntime, err := buildProviderHTTPRuntime(cfg)
		if err != nil {
			return nil, err
		}

		factory := NewProviderFactory()
		RegisterOpenAIProvider(factory, "openai", "openrouter", "local")
		RegisterFalProvider(factory, "fal")

		result, err := factory.Build(loadedCfg.Model.Provider, ProviderBuildContext{
			LoadedConfig: loadedCfg,
			Logger:       zapLogger,
			HTTPClient:   httpRuntime.Client,
		})
		if err != nil {
			return nil, err
		}
		provider := result.Provider
		recordRT = httpRuntime.Recorder

		gw, err := gateway.NewGateway(gateway.WithProvider(provider))
		if err != nil {
			return nil, fmt.Errorf("failed to create gateway: %w", err)
		}

		infOpts := []inference.Option{}
		if cfg.ModelConfig != "" {
			infOpts = append(infOpts, inference.WithModelConfig(cfg.ModelConfig))
		}
		if loadedCfg.Model.Provider == "fal" && loadedCfg.Model.Fal != nil {
			infOpts = append(infOpts, inference.WithModel(loadedCfg.Model.Fal.Model))
		}
		inf = inference.NewGatewayInferencer(gw, infOpts...)
	}

	// Get session storage
	sessionStorage, err := e.getSessionStorage(cfg)
	if err != nil {
		return nil, fmt.Errorf("get workspace dir: %w", err)
	}

	// Load models config
	configDir := cfg.ConfigDir
	modelsStorage, err := config.NewModelsConfigStorage(configDir)
	if err != nil {
		return nil, fmt.Errorf("init models config: %w", err)
	}
	modelsConfig, err := modelsStorage.Load()
	if err != nil {
		return nil, fmt.Errorf("load models config: %w", err)
	}

	// Get initial history and session ID
	var initialHistory []messages.Message
	var sessionID string
	if len(cfg.InitialHistory) > 0 && cfg.SessionID != "" {
		// Explicit session override (for chat)
		sessionID = cfg.SessionID
		initialHistory = cfg.InitialHistory
	} else {
		initialHistory, sessionID, err = e.getInitialHistory(cfg, sessionStorage)
		if err != nil {
			return nil, err
		}
	}

	// Build tool registry from config so only enabled tools are available.
	// When an inferencer override is set (test mode), use the injected executor
	// directly so that mock tool executors are discoverable by the agent loop.
	var loopExecutor messages.ToolExecutor
	var loopToolDefs []messages.ToolDefinition
	registry := tools.NewToolRegistryFromConfig(loadedCfg)
	if e.inferencerOverride != nil && e.executor != nil {
		loopExecutor = e.executor
		loopToolDefs = e.toolDefs
	} else {
		// dispatch_agent requires the inferencer, so it is registered after the inferencer is built.
		if loadedCfg.Tools.ToolEnabled("dispatch_agent") {
			registry.Register(tools.NewDispatchAgentTool(inf, registry))
		}
		loopExecutor = tools.NewRegistryExecutor(registry)
		loopToolDefs = registry.ToAgentLoopDefs()
	}

	// Load system prompt (use config-filtered tool defs so AGENTS.md matches enabled tools)
	systemPrompt, err := e.loadSystemPrompt(cfg, sessionStorage.WorkspaceDir(), loopToolDefs)
	if err != nil {
		return nil, err
	}

	// Build agent loop
	zapLogger = logger.GetRequestLoggerFromContext(ctx)
	loopOpts := []agentloop.Option{
		agentloop.WithInferencer(inf),
		agentloop.WithToolExecutor(loopExecutor),
		agentloop.WithTools(loopToolDefs),
		agentloop.WithLogger(logger.NewZapAgentLoopAdapter(zapLogger)),
	}
	if systemPrompt != "" {
		loopOpts = append(loopOpts, agentloop.WithSystemPrompt(systemPrompt))
	}
	if len(initialHistory) > 0 {
		loopOpts = append(loopOpts, agentloop.WithInitialHistory(initialHistory))
	}

	// Apply inference defaults from model config (e.g. repetition penalty).
	if defaults := e.buildInferenceDefaults(loadedCfg); defaults != nil {
		loopOpts = append(loopOpts, agentloop.WithInferenceDefaults(*defaults))
	}

	// Wire session replay into the agent loop if a session capture file exists.
	// Replay takes priority over record (same as HTTP capture).
	if cfg.ReplayCapturePath != "" {
		sessionCapturePath := cfg.ReplayCapturePath + ".session.json"
		if _, statErr := os.Stat(sessionCapturePath); statErr == nil {
			replayInf := testing.NewReplaySessionInferencer(sessionCapturePath)
			loopOpts = append(loopOpts, agentloop.WithSessionInferencer(replayInf))
		}
	}

	loop, err := agentloop.New(loopOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create agent loop: %w", err)
	}

	return &RunData{
		SessionID:      sessionID,
		SessionManager: sessionStorage,
		Loop:           loop,
		Recorder:       recordRT,
		Models:         modelsConfig,
		LoggerCloser:   loggerCloser,
		ConfigDir:      cfg.ConfigDir,
	}, nil
}

// ExecuteStreamingTurn starts a single agent turn in streaming mode and returns
// the event stream directly. The caller is responsible for draining the stream,
// calling stream.Close() when done, and then calling SaveSession, FlushRecorder,
// and CloseLogger as needed. Presentation (writing to out) is the caller's responsibility.
func (e *Executor) ExecuteStreamingTurn(ctx context.Context, runData *RunData, execInput agentloop.ExecuteInput, cfg *Config) (agentloop.Stream, error) {
	ctx = execctx.WithWorkspaceDir(ctx, runData.SessionManager.WorkspaceDir())
	ctx = execctx.WithConfigDir(ctx, runData.ConfigDir)
	execInput.OutputReasoningStream = cfg.OutputReasoningTokens
	streamResult, err := runData.Loop.ExecuteStreaming(ctx, execInput)
	if err != nil {
		return nil, err
	}
	return streamResult.EventStream, nil
}

// ExecuteOneTurn runs a single agent turn and writes the result to out.
// When cfg.Stream is true, it uses ExecuteStreamingTurn and handles presentation
// by draining the stream to out (or JSON lines when cfg.OutputJSON is set).
func (e *Executor) ExecuteOneTurn(ctx context.Context, runData *RunData, execInput agentloop.ExecuteInput, cfg *Config, out io.Writer) (result string, execErr error) {
	ctx = execctx.WithWorkspaceDir(ctx, runData.SessionManager.WorkspaceDir())
	ctx = execctx.WithConfigDir(ctx, runData.ConfigDir)
	streaming := cfg.Stream
	outputJSON := cfg.OutputJSON
	execInput.OutputReasoningStream = cfg.OutputReasoningTokens

	binaryModality := cfg.OutputModality != "" && cfg.OutputModality != "text"

	if streaming {
		stream, err := e.ExecuteStreamingTurn(ctx, runData, execInput, cfg)
		if err != nil {
			return "", err
		}
		defer func() { _ = stream.Close() }()
		if binaryModality && out != nil {
			n, writeErr := output.WriteBinaryModalityStream(out, stream, cfg.OutputModality)
			if writeErr != nil {
				return "", writeErr
			}
			if n == 0 {
				if _, err := fmt.Fprintf(cfg.Stderr(), "no %s content in response\n", cfg.OutputModality); err != nil {
					return "", fmt.Errorf("write binary modality warning: %w", err)
				}
			}
		} else if outputJSON {
			for stream.HasNext() {
				evt := stream.Response()
				// Route refusal events to stderr as structured JSON, not to stdout NDJSON stream.
				if evt.Type == messages.StreamTypeRefusal {
					if v, ok := evt.Value.(*messages.RefusalValue); ok && v.Message != "" {
						if writeErr := output.WriteRefusalJSON(cfg.Stderr(), v.Message, cfg.Model); writeErr != nil {
							return "", writeErr
						}
					}
					continue
				}
				if writeErr := output.WriteStreamEventJSON(out, evt); writeErr != nil {
					execErr = writeErr
					return "", execErr
				}
			}
		} else if out != nil {
			execErr = output.WriteEventStreamToWriter(out, cfg.Stderr(), stream)
		}
		return "", execErr
	}

	execResult, err := runData.Loop.Execute(ctx, execInput)
	if err != nil {
		return "", err
	}

	// Surface any refusals from assistant messages to stderr.
	for _, m := range execResult.Messages {
		if m.Role == messages.RoleAssistant && m.Refusal != "" {
			if outputJSON {
				if writeErr := output.WriteRefusalJSON(cfg.Stderr(), m.Refusal, cfg.Model); writeErr != nil {
					return "", writeErr
				}
			} else {
				output.WriteRefusal(cfg.Stderr(), m.Refusal)
			}
		}
	}

	if binaryModality && out != nil {
		n, writeErr := output.WriteBinaryModalityMessages(out, execResult.Messages, cfg.OutputModality)
		if writeErr != nil {
			return "", writeErr
		}
		if n == 0 {
			if _, err := fmt.Fprintf(cfg.Stderr(), "no %s content in response\n", cfg.OutputModality); err != nil {
				return "", fmt.Errorf("write binary modality warning: %w", err)
			}
		}
		return "", nil
	}
	if outputJSON {
		return "", output.WriteMessagesJSON(out, execResult.Messages)
	}
	result = execResult.Text()
	if out != nil {
		if _, err := fmt.Fprintln(out, result); err != nil {
			return "", fmt.Errorf("write output: %w", err)
		}
	}
	return result, nil
}

// DefaultMaxContinuationDepth is the maximum number of TODO queue re-invocations
// per turn when MaxContinuationDepth is not explicitly set.
const DefaultMaxContinuationDepth = 3

// DefaultContinuationNudgeMessage is the message enqueued in the TODO queue when
// the model stops early and continuationNudgeEnabled is true.
const DefaultContinuationNudgeMessage = "Please continue where you left off."

// executeWithContinuation runs ExecuteOneTurn and then drains the TODO queue,
// re-invoking inference for each dequeued message up to maxContinuationDepth.
// The stopWord, if non-empty, causes the loop to stop early when found in the
// result text. When the model config has ContinuationNudgeEnabled set, a nudge
// message is auto-enqueued whenever the model stops without a tool call or
// stop-word. It returns the final result text and any error.
func (e *Executor) executeWithContinuation(
	ctx context.Context,
	runData *RunData,
	input agentloop.ExecuteInput,
	cfg *Config,
	out io.Writer,
	stopWord string,
) (string, error) {
	maxDepth := cfg.MaxContinuationDepth
	if maxDepth <= 0 {
		maxDepth = DefaultMaxContinuationDepth
	}

	// Load continuation nudge settings from model config.
	nudgeEnabled, nudgeMsg := e.loadContinuationNudgeConfig(cfg)

	result, err := e.ExecuteOneTurn(ctx, runData, input, cfg, out)
	if err != nil {
		return result, err
	}

	// Check for stop word — if found, no continuation needed.
	if stopWord != "" && strings.Contains(result, stopWord) {
		return result, nil
	}

	// Auto-enqueue a continuation nudge if the model stopped early.
	if nudgeEnabled {
		maybeEnqueueContinuationNudge(runData, stopWord, nudgeMsg)
	}

	// Drain the TODO queue, re-invoking inference for each dequeued message.
	for depth := 0; depth < maxDepth; depth++ {
		msg, ok := runData.Loop.DequeueTodo()
		if !ok {
			break
		}

		continuationInput := agentloop.ExecuteInput{
			Message: msg,
		}
		result, err = e.ExecuteOneTurn(ctx, runData, continuationInput, cfg, out)
		if err != nil {
			return result, err
		}

		if stopWord != "" && strings.Contains(result, stopWord) {
			break
		}

		// Auto-enqueue again if the model still stopped early.
		if nudgeEnabled {
			maybeEnqueueContinuationNudge(runData, stopWord, nudgeMsg)
		}
	}

	return result, nil
}

// buildInferenceDefaults constructs InferenceDefaults from the model config.
// Returns nil when no defaults need to be set.
func (e *Executor) buildInferenceDefaults(cfg *config.Config) *messages.InferenceDefaults {
	rp := cfg.Model.RepetitionPenalty
	if rp == 0 || rp == 1.0 {
		return nil
	}
	return &messages.InferenceDefaults{
		FrequencyPenalty: &rp,
	}
}

// loadContinuationNudgeConfig reads the continuation nudge settings from the
// model config. Returns (enabled, message). If the config cannot be loaded,
// nudge is disabled.
func (e *Executor) loadContinuationNudgeConfig(cfg *Config) (bool, string) {
	loadedCfg, err := e.loadConfig(cfg)
	if err != nil {
		return false, ""
	}
	mc := loadedCfg.Model
	if !mc.ContinuationNudgeEnabled {
		return false, ""
	}
	msg := mc.ContinuationNudgeMessage
	if msg == "" {
		msg = DefaultContinuationNudgeMessage
	}
	return true, msg
}

// maybeEnqueueContinuationNudge checks the last assistant message in the
// conversation history. If the message has no tool calls and the result does
// not contain the stop-word, it enqueues the nudge message in the TODO queue.
func maybeEnqueueContinuationNudge(runData *RunData, stopWord string, nudgeMsg string) {
	history := runData.Loop.GetConversationHistory()

	// Find the last assistant message.
	for i := len(history) - 1; i >= 0; i-- {
		m := history[i]
		if m.Role != messages.RoleAssistant {
			continue
		}
		// If the assistant used tool calls, this is not an early stop.
		if len(m.ToolCalls) > 0 {
			return
		}
		// If the stop-word appears in the assistant text, this is not an early stop.
		if stopWord != "" && strings.Contains(m.TextContent(), stopWord) {
			return
		}
		// Early stop detected — enqueue nudge.
		runData.Loop.EnqueueTodo(nudgeMsg)
		return
	}
}

// SaveSession saves the conversation history to the session storage.
func (e *Executor) SaveSession(runData *RunData) error {
	if runData.SessionID == "" {
		return nil
	}
	history := runData.Loop.GetConversationHistory()
	if len(history) > 0 {
		if err := runData.SessionManager.Save(runData.SessionID, history); err != nil {
			return fmt.Errorf("save session: %w", err)
		}
	}
	return nil
}

// FlushRecorder flushes the recorder to file if recording is enabled.
func (e *Executor) FlushRecorder(runData *RunData, recordPath string) error {
	if runData.Recorder != nil && recordPath != "" {
		if err := runData.Recorder.FlushToFile(recordPath); err != nil {
			return fmt.Errorf("failed to flush captures: %w", err)
		}
	}
	return nil
}

// NewChatSessionID returns a new session ID in the workspace (for chat).
func (e *Executor) NewChatSessionID(cfg *Config) (string, error) {
	storage, err := e.getSessionStorage(cfg)
	if err != nil {
		return "", err
	}
	return storage.NewSessionID(), nil
}

// validateOutputModality checks that the requested output modality is supported by the configured model.
// Returns nil if modality is empty, "text", or supported by the model. Returns an error otherwise.
func (e *Executor) validateOutputModality(cfg *Config, runData *RunData) error {
	if e.relaxModelValidation {
		return nil
	}
	modality := cfg.OutputModality
	if modality == "" || modality == "text" {
		return nil
	}
	if e.inferencerOverride != nil && cfg.ConfigDir == "" {
		return nil
	}

	loadedCfg, err := e.loadConfig(cfg)
	if err != nil {
		return nil // skip validation if config can't be loaded
	}
	active, err := loadedCfg.ActiveOpenAIConfig()
	if err != nil {
		return nil // skip validation if provider config unavailable
	}
	modelName := active.Model
	if runData.Models == nil {
		return nil
	}
	modelInfo := runData.Models.Lookup(modelName)
	if modelInfo == nil {
		return nil // unknown model — allow the request and let the provider decide
	}
	if !modelInfo.SupportsOutputModality(modality) {
		return fmt.Errorf("model %q does not support %s output", modelName, modality)
	}
	return nil
}

// validateInputMimeTypes checks that every file content part in the input is accepted by the
// configured model's supportedInputMimeTypes list. Follows the same resolution flow as
// validateOutputModality: silently allows if config or model info is unavailable.
func (e *Executor) validateInputMimeTypes(cfg *Config, runData *RunData, execInput agentloop.ExecuteInput) error {
	if e.relaxModelValidation || len(execInput.ContentParts) == 0 {
		return nil
	}
	if e.inferencerOverride != nil && cfg.ConfigDir == "" {
		return nil
	}
	loadedCfg, err := e.loadConfig(cfg)
	if err != nil {
		return nil // skip validation if config can't be loaded
	}
	active, err := loadedCfg.ActiveOpenAIConfig()
	if err != nil {
		return nil // skip validation if provider config unavailable
	}
	modelName := active.Model
	if runData.Models == nil {
		return nil
	}
	modelInfo := runData.Models.Lookup(modelName)
	if modelInfo == nil {
		return nil // unknown model — allow and let the provider decide
	}
	return input.ValidateContentPartsMimeTypes(execInput.ContentParts, modelName, modelInfo.SupportedInputMimeTypes)
}

// RunAsk runs a one-shot ask: builds the loop from cfg, executes one turn, saves session.
// When streaming, out receives all tokens (reasoning then text when cfg.OutputReasoningTokens is set).
func (e *Executor) RunAsk(ctx context.Context, cfg *Config, input agentloop.ExecuteInput, out io.Writer) (string, error) {
	if err := cfg.Validate(); err != nil {
		return "", err
	}

	runData, err := e.BuildLoop(ctx, cfg)
	if err != nil {
		return "", err
	}
	defer runData.CloseLogger()

	if err := e.validateOutputModality(cfg, runData); err != nil {
		return "", err
	}
	if err := e.validateInputMimeTypes(cfg, runData, input); err != nil {
		return "", err
	}

	result, execErr := e.executeWithContinuation(ctx, runData, input, cfg, out, "")

	if flushErr := e.FlushRecorder(runData, cfg.RecordCapturePath); flushErr != nil {
		if execErr != nil {
			execErr = fmt.Errorf("%w (flush: %v)", execErr, flushErr)
		} else {
			execErr = flushErr
		}
	}

	if execErr == nil {
		if saveErr := e.SaveSession(runData); saveErr != nil {
			return result, saveErr
		}
	}

	return result, execErr
}

// IterativeLoopConfig holds CLI-level configuration for an iterative loop run.
type IterativeLoopConfig struct {
	// MaxIterations is the maximum number of loop iterations. Defaults to 5 if <= 0.
	MaxIterations int

	// StopWord is a case-sensitive string that, when found in the last assistant
	// response, signals completion and stops the loop early.
	StopWord string

	// ContextPressureThreshold enables the ContextPressureNotifier when > 0.
	// Ignored here (handled by IterativeLoop when token counting is configured).
	ContextPressureThreshold float64

	// ContextPressureMessage is the custom warning injected at pressure threshold.
	ContextPressureMessage string

	// TraceID, if set, loads an existing trace record and resumes from it.
	TraceID string
}

// IterationRunResult holds the result of a single loop iteration.
type IterationRunResult struct {
	Iteration int
	SessionID string
	Text      string
	Err       error
}

// IterativeRunResult holds the combined result of a loop run.
type IterativeRunResult struct {
	TraceID    string
	Iterations []IterationRunResult
	Completed  bool
}

// BuildIterationAnnotation returns the iteration-specific annotation appended to the system prompt.
func BuildIterationAnnotation(i, maxIter int, stopWord string) string {
	annotation := fmt.Sprintf("You are on iteration %d of %d.", i, maxIter)
	if stopWord != "" {
		annotation += fmt.Sprintf(" When you are finished, end your response with %s.", stopWord)
	}
	return annotation
}

// RunIterativeLoop runs the agent iteratively, printing iteration banners, managing the trace record,
// and saving each iteration's session. Each iteration runs with a fresh context window.
//
// Ctrl+C (SIGINT) cancels the current iteration, updates the trace record as interrupted,
// prints a resume message, and returns. The loop can be resumed by passing the printed trace ID
// via IterativeLoopConfig.TraceID.
//
// Errors from individual iterations are recorded in IterativeRunResult and the loop continues;
// only setup errors (trace load, session storage) are returned as hard failures.
func (e *Executor) RunIterativeLoop(
	ctx context.Context,
	cfg *Config,
	loopCfg IterativeLoopConfig,
	input agentloop.ExecuteInput,
	out io.Writer,
) (IterativeRunResult, error) {
	result := IterativeRunResult{}

	maxIter := loopCfg.MaxIterations
	if maxIter <= 0 {
		maxIter = 5
	}

	// Get session storage for trace management.
	sessionStorage, err := e.getSessionStorage(cfg)
	if err != nil {
		return result, fmt.Errorf("get session storage: %w", err)
	}

	// Create or load trace record.
	var trace session.TraceRecord
	if loopCfg.TraceID != "" {
		existing, loadErr := sessionStorage.LoadTrace(loopCfg.TraceID)
		if loadErr != nil {
			return result, fmt.Errorf("load trace %s: %w", loopCfg.TraceID, loadErr)
		}
		if existing != nil {
			trace = *existing
		}
	}

	// Compute start iteration and restore config when resuming from an existing trace.
	startIter := 1
	if trace.TraceID != "" {
		// Restore loop config from the saved trace so resume uses the original parameters.
		maxIter = trace.Config.MaxIterations
		loopCfg.StopWord = trace.Config.StopWord
		input.Message = trace.Config.Prompt

		if len(trace.Iterations) > 0 {
			lastIter := trace.Iterations[len(trace.Iterations)-1]
			if lastIter.Status == session.IterationStatusInterrupted {
				// Restart the interrupted iteration fresh — discard its partial record.
				startIter = lastIter.Iteration
				trace.Iterations = trace.Iterations[:len(trace.Iterations)-1]
			} else {
				startIter = lastIter.Iteration + 1
			}
		}
		if _, err := fmt.Fprintf(out, "[Resuming trace %s from iteration %d/%d]\n", trace.TraceID, startIter, maxIter); err != nil {
			return IterativeRunResult{}, fmt.Errorf("write resume trace banner: %w", err)
		}
	}

	if trace.TraceID == "" {
		trace = session.TraceRecord{
			TraceID: sessionStorage.NewTraceID(),
			Status:  session.TraceStatusRunning,
			Config: session.TraceConfig{
				MaxIterations: maxIter,
				StopWord:      loopCfg.StopWord,
				Prompt:        input.Message,
			},
		}
		if saveErr := sessionStorage.SaveTrace(trace); saveErr != nil {
			return result, fmt.Errorf("save trace: %w", saveErr)
		}
	}
	result.TraceID = trace.TraceID

	if _, err := fmt.Fprintf(out, "Trace ID: %s\n", trace.TraceID); err != nil {
		return result, fmt.Errorf("write trace ID: %w", err)
	}

	// Set up SIGINT handling: cancel the loop context on Ctrl+C so the current
	// iteration is gracefully stopped and the trace is saved as interrupted.
	sigCtx, sigCancel := signal.NotifyContext(ctx, os.Interrupt)
	defer sigCancel()

	for i := startIter; i <= maxIter; i++ {
		if _, err := fmt.Fprintf(out, "\n--- Iteration %d/%d ---\n", i, maxIter); err != nil {
			return result, fmt.Errorf("write iteration header: %w", err)
		}

		// Build iteration config: fresh session, iteration-specific annotation appended to system prompt.
		iterCfg := *cfg
		iterCfg.SessionID = ""
		iterCfg.ContinueLastSession = false
		iterCfg.InitialHistory = nil
		iterCfg.SystemPromptSuffix = BuildIterationAnnotation(i, maxIter, loopCfg.StopWord)

		// Build and run this iteration using the signal-aware context.
		runData, buildErr := e.BuildLoop(sigCtx, &iterCfg)
		var text string
		var execErr error
		var sessionID string

		if buildErr != nil {
			execErr = buildErr
		} else if mimeErr := e.validateInputMimeTypes(&iterCfg, runData, input); mimeErr != nil {
			execErr = mimeErr
		} else {
			sessionID = runData.SessionID
			text, execErr = e.executeWithContinuation(sigCtx, runData, input, &iterCfg, out, loopCfg.StopWord)
			if execErr == nil {
				_ = e.SaveSession(runData)
			}
			runData.CloseLogger()
		}

		// Detect whether the iteration was stopped by a signal (Ctrl+C).
		interrupted := sigCtx.Err() != nil

		// Record iteration in trace.
		var iterStatus session.IterationStatus
		if interrupted {
			iterStatus = session.IterationStatusInterrupted
		} else if execErr != nil {
			iterStatus = session.IterationStatusFailed
		} else {
			iterStatus = session.IterationStatusCompleted
		}
		trace.CurrentIteration = i
		trace.Iterations = append(trace.Iterations, session.IterationTrace{
			Iteration: i,
			SessionID: sessionID,
			Status:    iterStatus,
		})

		if interrupted {
			trace.Status = session.TraceStatusInterrupted
			_ = sessionStorage.SaveTrace(trace)
			if _, err := fmt.Fprintf(out, "\n[Interrupted. Resume with: --loop --trace-id %s]\n", trace.TraceID); err != nil {
				return result, fmt.Errorf("write interrupted trace banner: %w", err)
			}
			result.Iterations = append(result.Iterations, IterationRunResult{
				Iteration: i,
				SessionID: sessionID,
				Text:      text,
				Err:       context.Canceled,
			})
			return result, nil
		}

		_ = sessionStorage.SaveTrace(trace)

		result.Iterations = append(result.Iterations, IterationRunResult{
			Iteration: i,
			SessionID: sessionID,
			Text:      text,
			Err:       execErr,
		})

		if execErr != nil {
			// Record error and continue to next iteration.
			continue
		}

		// Check for stop word (case-sensitive containment).
		if loopCfg.StopWord != "" && strings.Contains(text, loopCfg.StopWord) {
			result.Completed = true
			break
		}
	}

	// Update final trace status.
	trace.Status = session.TraceStatusCompleted
	_ = sessionStorage.SaveTrace(trace)

	return result, nil
}

// RunAskWithSession runs one turn using the given session ID: loads existing history, runs the loop, saves.
// Use for multi-turn chat. Pass empty sessionID to use normal ask behavior (session-id, continue-last-session, or new).
func (e *Executor) RunAskWithSession(ctx context.Context, sessionID string, cfg *Config, input agentloop.ExecuteInput, out io.Writer) (string, error) {
	if sessionID == "" {
		return e.RunAsk(ctx, cfg, input, out)
	}

	storage, err := e.getSessionStorage(cfg)
	if err != nil {
		return "", err
	}
	initialHistory, err := storage.Load(sessionID)
	if err != nil {
		return "", fmt.Errorf("load session %s: %w", sessionID, err)
	}
	if initialHistory == nil {
		initialHistory = []messages.Message{}
	}

	cfgWithHistory := *cfg
	cfgWithHistory.SessionID = sessionID
	cfgWithHistory.InitialHistory = initialHistory
	if err := cfgWithHistory.Validate(); err != nil {
		return "", err
	}

	runData, err := e.BuildLoop(ctx, &cfgWithHistory)
	if err != nil {
		return "", err
	}
	defer runData.CloseLogger()

	if err := e.validateInputMimeTypes(&cfgWithHistory, runData, input); err != nil {
		return "", err
	}

	result, execErr := e.executeWithContinuation(ctx, runData, input, &cfgWithHistory, out, "")

	if flushErr := e.FlushRecorder(runData, cfg.RecordCapturePath); flushErr != nil {
		if execErr != nil {
			execErr = fmt.Errorf("%w (flush: %v)", execErr, flushErr)
		} else {
			execErr = flushErr
		}
	}

	if execErr == nil {
		if saveErr := e.SaveSession(runData); saveErr != nil {
			return result, saveErr
		}
	}

	return result, execErr
}
