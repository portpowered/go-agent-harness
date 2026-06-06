package agentloop

import (
	"time"

	"github.com/portpowered/go-agent-loop/pkg/logging"
	"github.com/portpowered/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-loop/pkg/state"
	"github.com/portpowered/go-agent-loop/pkg/subsystems"
)

// AgentLoopConfig holds configuration for creating an AgentLoop.
type AgentLoopConfig struct {
	Mode              state.ExecutionMode
	Inferencer        messages.Inferencer
	SessionInferencer messages.SessionInferencer
	ToolExecutor      messages.ToolExecutor
	Tools             []messages.ToolDefinition
	Recorder          subsystems.StateRecorder
	TokenCounter      subsystems.TokenCounter
	MaxTokens         int
	PressureThreshold float64 // if > 0, wires ContextPressureNotifier (requires TokenCounter)
	PressureMessage   string  // custom warning message for ContextPressureNotifier (empty = default)
	SystemPrompt      string
	InitialHistory    []messages.Message
	BufferCapacity    int
	Logger            logging.Logger
	InferenceDefaults *messages.InferenceDefaults
	TickRate          time.Duration
	// SessionConfig, when set, is sent as SESSION.UPDATE immediately after the
	// inference provider emits SESSION.CREATED. Only used in DuplexSession mode.
	SessionConfig *messages.SessionUpdateConfig

	// Per-participant buffer capacity overrides. When set (> 0), these take
	// precedence over BufferCapacity for the corresponding participant.
	// This allows tuning buffer sizes for different workload profiles:
	//   - Model buffers: larger for streaming-heavy workloads with many deltas
	//   - Tool buffers: larger when running many concurrent tool calls
	//   - User buffers: smaller is usually fine (low-frequency user input)
	//   - Kernel buffers: larger for high-throughput delta forwarding
	ModelBufferCapacity  int
	ToolBufferCapacity   int
	UserBufferCapacity   int
	KernelBufferCapacity int
}

// Option is a functional option for configuring an AgentLoop.
type Option func(*AgentLoopConfig)

func WithLogger(logger logging.Logger) Option {
	return func(c *AgentLoopConfig) {
		c.Logger = logger
	}
}

// WithMode sets the execution mode.
func WithMode(mode state.ExecutionMode) Option {
	return func(c *AgentLoopConfig) {
		c.Mode = mode
	}
}

// WithInferencer sets the inference provider.
func WithInferencer(inf messages.Inferencer) Option {
	return func(c *AgentLoopConfig) {
		c.Inferencer = inf
	}
}

// WithSessionInferencer sets the session inference provider for duplex session mode.
// When configured, the agent loop establishes a persistent bidirectional session
// and routes session events through its delta buffer instead of running
// request/response inference. Use with WithMode(engine.DuplexSession).
func WithSessionInferencer(inf messages.SessionInferencer) Option {
	return func(c *AgentLoopConfig) {
		c.SessionInferencer = inf
	}
}

// WithToolExecutor sets the tool executor.
func WithToolExecutor(exec messages.ToolExecutor) Option {
	return func(c *AgentLoopConfig) {
		c.ToolExecutor = exec
	}
}

// WithTools sets the available tool definitions.
func WithTools(tools []messages.ToolDefinition) Option {
	return func(c *AgentLoopConfig) {
		c.Tools = tools
	}
}

// WithRecorder sets the state recorder.
func WithRecorder(rec subsystems.StateRecorder) Option {
	return func(c *AgentLoopConfig) {
		c.Recorder = rec
	}
}

// WithTokenCounter sets the token counter and max token limit.
func WithTokenCounter(counter subsystems.TokenCounter, maxTokens int) Option {
	return func(c *AgentLoopConfig) {
		c.TokenCounter = counter
		c.MaxTokens = maxTokens
	}
}

// WithSystemPrompt sets the initial system prompt.
func WithSystemPrompt(prompt string) Option {
	return func(c *AgentLoopConfig) {
		c.SystemPrompt = prompt
	}
}

// WithInitialHistory sets conversation history to prepend before the first user message.
// Used when continuing a session (e.g. --continue-last-session, --session-id).
func WithInitialHistory(msgs []messages.Message) Option {
	return func(c *AgentLoopConfig) {
		c.InitialHistory = msgs
	}
}

// WithBufferCapacity sets the capacity for participant buffers.
func WithBufferCapacity(capacity int) Option {
	return func(c *AgentLoopConfig) {
		c.BufferCapacity = capacity
	}
}

// WithModelBufferCapacity sets the buffer capacity for the model participant
// (inference request inbox and streaming delta outbox). Increase for
// streaming-heavy workloads that produce many deltas per response.
// Default: uses BufferCapacity (64).
func WithModelBufferCapacity(capacity int) Option {
	return func(c *AgentLoopConfig) {
		c.ModelBufferCapacity = capacity
	}
}

// WithToolBufferCapacity sets the buffer capacity for the tool participant
// (tool batch request inbox and tool delta outbox). Increase when running
// many concurrent tool calls that produce large result payloads.
// Default: uses BufferCapacity (64).
func WithToolBufferCapacity(capacity int) Option {
	return func(c *AgentLoopConfig) {
		c.ToolBufferCapacity = capacity
	}
}

// WithUserBufferCapacity sets the buffer capacity for the user participant
// (user request inbox and user response outbox). Typically needs less
// capacity than model or tool buffers since user messages are infrequent.
// Default: uses BufferCapacity (64).
func WithUserBufferCapacity(capacity int) Option {
	return func(c *AgentLoopConfig) {
		c.UserBufferCapacity = capacity
	}
}

// WithKernelBufferCapacity sets the buffer capacity for the kernel participant
// (delta inbox that receives all forwarded deltas). Increase for workloads
// with high delta throughput where the consumer may lag behind production.
// Default: uses BufferCapacity (64).
func WithKernelBufferCapacity(capacity int) Option {
	return func(c *AgentLoopConfig) {
		c.KernelBufferCapacity = capacity
	}
}

// WithInferenceDefaults sets default inference parameters (MaxTokens, Temperature,
// StopSequences) that are applied to every InferenceRequest dispatched by the
// coordinator. Per-request values on InferenceRequest take precedence.
func WithInferenceDefaults(defaults messages.InferenceDefaults) Option {
	return func(c *AgentLoopConfig) {
		c.InferenceDefaults = &defaults
	}
}

// WithTickRate sets the minimum interval between engine ticks in the hot loop.
// Zero (default) means no delay — the loop runs as fast as possible, which is
// optimal for latency-sensitive workloads. Setting a tick rate (e.g. 10ms)
// reduces CPU usage at the cost of added latency per tick, useful for
// background agents or resource-constrained deployments.
func WithTickRate(d time.Duration) Option {
	return func(c *AgentLoopConfig) {
		c.TickRate = d
	}
}

// WithSessionConfig sets the session configuration to send as SESSION.UPDATE
// immediately after the inference provider emits SESSION.CREATED. Only active
// in DuplexSession mode. Use this to configure the model, system prompt, and
// input/output modalities for a realtime session (e.g. Grok realtime API).
func WithSessionConfig(cfg messages.SessionUpdateConfig) Option {
	return func(c *AgentLoopConfig) {
		c.SessionConfig = &cfg
	}
}

// WithContextPressureNotifier enables the ContextPressureNotifier subsystem.
// When threshold is > 0 and a TokenCounter is configured, the notifier fires once
// when token usage exceeds threshold * MaxTokens, injecting a warning interrupt.
// message is the warning text (empty = default message).
// If TokenCounter is not configured, the option is silently ignored.
func WithContextPressureNotifier(threshold float64, message string) Option {
	return func(c *AgentLoopConfig) {
		c.PressureThreshold = threshold
		c.PressureMessage = message
	}
}
