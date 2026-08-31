package agent

// This file owns loop construction and execution orchestration, including continuation, one-shot ask, iterative runs, and session-backed runs.

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/logger"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/session"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/gateway"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/inference"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

// BuildLoop constructs an agent loop from the given configuration.
func (e *Executor) BuildLoop(ctx context.Context, cfg *Config) (*RunData, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	filesystemPolicy, err := tools.ResolveFilesystemPolicy(cfg.WorkDir, cfg.AllowPaths...)
	if err != nil {
		return nil, fmt.Errorf("resolve filesystem scope: %w", err)
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
	sessionStorage, err := e.getSessionStorageWithPolicy(cfg, filesystemPolicy)
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
	// Config is cached by ConfigStorage, so keep request-scoped filesystem
	// metadata on a copy instead of mutating the cached configuration.
	loadedCfgCopy := *loadedCfg
	loadedCfgCopy.FilesystemWorkDir = filesystemPolicy.PrimaryRoot()
	loadedCfgCopy.FilesystemAllowPaths = filesystemPolicy.AdditionalRoots()
	loadedCfg = &loadedCfgCopy
	registry := tools.NewToolRegistryFromConfigWithPolicy(loadedCfg, filesystemPolicy)
	if e.inferencerOverride != nil && e.executor != nil {
		loopExecutor = e.executor
		loopToolDefs = e.toolDefs
	} else {
		// dispatch_agent requires the inferencer, so it is registered after the inferencer is built.
		if loadedCfg.Tools.ToolEnabled("dispatch_agent") {
			_ = registry.Register(tools.NewDispatchAgentTool(inf, registry))
		}
		loopExecutor = tools.NewRegistryExecutor(registry)
		loopToolDefs = registry.ToAgentLoopDefs()
	}

	// Load system prompt (use config-filtered tool defs so AGENTS.md matches enabled tools)
	systemPrompt, err := e.LoadSystemPrompt(cfg, sessionStorage.WorkspaceDir(), loopToolDefs)
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
