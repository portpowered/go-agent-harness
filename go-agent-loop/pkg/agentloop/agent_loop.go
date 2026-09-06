package agentloop

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/engine"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/logging"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/participants"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/subsystems"
)

// AgentLoop is the default implementation of AgenticLoop.
type AgentLoop struct {
	engine  *engine.Engine
	config  AgentLoopConfig
	logger  logging.Logger
	mu      sync.Mutex
	outputs []Output
	inputs  []Input

	// deltas is the consumer-facing typed buffer of streaming delta events.
	// It is populated by Run() via a goroutine that forwards from the
	// KernelRunner's delta event channel. Consumers call Deltas() to read from it.
	deltas *messages.TypedBuffer[messages.StreamMessage]
}

// Compile-time check that AgentLoop satisfies AgenticLoop.
var _ AgenticLoop = (*AgentLoop)(nil)

// Deltas returns the typed buffer of streaming delta events emitted during Run().
// Consumers call this before Run() and read from it concurrently with Run().
// The buffer is populated from the KernelRunner's delta event channel when Run()
// is executing; it is empty during Execute/ExecuteStreaming turns.
func (al *AgentLoop) Deltas() *messages.TypedBuffer[messages.StreamMessage] {
	return al.deltas
}

// New creates a new AgentLoop with the given options.
func New(opts ...Option) (*AgentLoop, error) {
	cfg := AgentLoopConfig{
		Mode:           engine.ModeAskOnce,
		BufferCapacity: 64,
	}
	for _, opt := range opts {
		opt(&cfg)
	}

	if cfg.Inferencer == nil && cfg.SessionInferencer == nil {
		return nil, errors.New("inferencer or session inferencer is required")
	}
	if err := validateToolConfiguration(&cfg); err != nil {
		return nil, err
	}

	// Resolve per-participant buffer capacities. Per-participant overrides
	// take precedence over the global BufferCapacity default.
	bufCap := cfg.BufferCapacity
	modelCap := bufCap
	if cfg.ModelBufferCapacity > 0 {
		modelCap = cfg.ModelBufferCapacity
	}
	toolCap := bufCap
	if cfg.ToolBufferCapacity > 0 {
		toolCap = cfg.ToolBufferCapacity
	}
	userCap := bufCap
	if cfg.UserBufferCapacity > 0 {
		userCap = cfg.UserBufferCapacity
	}
	kernelCap := bufCap
	if cfg.KernelBufferCapacity > 0 {
		kernelCap = cfg.KernelBufferCapacity
	}

	// Create active participant runners
	var modelRunner *participants.ModelRunner
	if cfg.SessionInferencer != nil {
		modelRunner = participants.NewSessionModelRunner(cfg.SessionInferencer, modelCap, cfg.SessionConfig)
	} else {
		modelRunner = participants.NewModelRunner(cfg.Inferencer, modelCap)
	}
	userRunner := participants.NewUserRunner(userCap)
	kernelRunner := participants.NewKernelRunner(cfg.Logger, kernelCap)
	interactionRunner := participants.NewInteractionRunner(bufCap)
	var toolRunner *participants.ToolRunner
	if cfg.ToolExecutor != nil {
		toolRunner = participants.NewToolRunner(cfg.ToolExecutor, toolCap)
	} else {
		// Keep an idle tool participant wired for internal loop plumbing even in
		// explicit no-tools mode. validateToolConfiguration guarantees the loop
		// never advertises tools without either an executor or an explicit
		// no-tools decision, so this runner does not act as a public fallback.
		toolRunner = participants.NewToolRunner(&messages.DefaultToolExecutor{}, toolCap)
	}
	if cfg.SessionInferencer != nil && cfg.ToolAcknowledgement != nil {
		policy := *cfg.ToolAcknowledgement
		toolRunner.ConfigureAcknowledgement(policy.Threshold, policy.IsLongRunning, func(ctx context.Context, _ []messages.ToolCall) {
			if err := modelRunner.EnqueueSessionEvent(ctx, messages.StreamMessage{
				Type:  messages.StreamTypeResponseCreate,
				Value: messages.NewToolAcknowledgementResponseCreateValue(),
			}); err != nil && cfg.Logger != nil {
				cfg.Logger.Warn("tool acknowledgement admission failed", logging.Field{Key: "error", Value: err.Error()})
			}
		})
	}

	// Set OnDrop callbacks so operators are alerted when buffers are full.
	if cfg.Logger != nil {
		modelRunner.Inbox.SetOnDrop(func(_ messages.InferenceRequest) {
			cfg.Logger.Warn("buffer drop", logging.Field{Key: "buffer", Value: "model.Inbox"}, logging.Field{Key: "type", Value: "InferenceRequest"})
		})
		modelRunner.DeltaOutbox.SetOnDrop(func(_ messages.StreamMessage) {
			cfg.Logger.Warn("buffer drop", logging.Field{Key: "buffer", Value: "model.DeltaOutbox"}, logging.Field{Key: "type", Value: "StreamMessage"})
		})
		toolRunner.Inbox.SetOnDrop(func(_ messages.ToolBatchRequest) {
			cfg.Logger.Warn("buffer drop", logging.Field{Key: "buffer", Value: "tool.Inbox"}, logging.Field{Key: "type", Value: "ToolBatchRequest"})
		})
		toolRunner.DeltaOutbox.SetOnDrop(func(_ messages.StreamMessage) {
			cfg.Logger.Warn("buffer drop", logging.Field{Key: "buffer", Value: "tool.DeltaOutbox"}, logging.Field{Key: "type", Value: "StreamMessage"})
		})
		userRunner.Inbox.SetOnDrop(func(_ messages.UserRequest) {
			cfg.Logger.Warn("buffer drop", logging.Field{Key: "buffer", Value: "user.Inbox"}, logging.Field{Key: "type", Value: "UserRequest"})
		})
		userRunner.Outbox.SetOnDrop(func(_ messages.UserResponse) {
			cfg.Logger.Warn("buffer drop", logging.Field{Key: "buffer", Value: "user.Outbox"}, logging.Field{Key: "type", Value: "UserResponse"})
		})
		kernelRunner.DeltaInbox.SetOnDrop(func(_ messages.KernelDeltaRequest) {
			cfg.Logger.Warn("buffer drop", logging.Field{Key: "buffer", Value: "kernel.DeltaInbox"}, logging.Field{Key: "type", Value: "KernelDeltaRequest"})
		})
	}

	// Build passive helpers only (recorder, token counter)
	hlps := []subsystems.Subsystem{}

	// InterruptHandler runs at TickGroup=-1 (before all other subsystems) so it
	// can cancel in-flight executions and reset pass state before the Coordinator
	// reacts to the current tick's inputs.
	interruptHandler := subsystems.NewInterruptHandler(modelRunner, toolRunner, cfg.Logger)
	hlps = append(hlps, interruptHandler)
	if cfg.Audio != nil {
		hlps = append(hlps, cfg.Audio)
	}

	// Both Coordinator and CoordinatorDelta write to the same DeltaInbox so that
	// full messages (SYSTEM.FULL_MESSAGE) and streaming deltas share a single
	// FIFO queue, eliminating ordering races. In session mode the tool-result
	// forwarder runs before Coordinator so its provider event is queued before
	// the result-driven follow-up request. Coordinator then enqueues full
	// messages; CoordinatorDelta runs after it (tick group 5) and enqueues
	// LOOP.END, ensuring it always arrives after all messages.
	coordDelta := subsystems.NewCoordinatorDelta(kernelRunner.DeltaInbox, cfg.Logger)
	hlps = append(hlps, coordDelta)

	coord := subsystems.NewCoordinator(cfg.Logger)
	hlps = append(hlps, coord)

	interactionEvents := subsystems.NewInteractionEvents(cfg.Logger)
	hlps = append(hlps, interactionEvents)

	// PingPong is only useful in session mode where keepalive pings are expected.
	if cfg.Mode == engine.DuplexSession {
		// Forward completed tool results before Coordinator emits the
		// result-driven model request. This preserves provider-wire
		// queue/sequence ordering when both are ready in the same tick.
		hlps = append(hlps, subsystems.NewToolResultForwarderWithEnqueuer(modelRunner.EnqueueSessionEvent, cfg.Logger))
		pingPong := subsystems.NewPingPong(kernelRunner.DeltaInbox, cfg.Logger)
		hlps = append(hlps, pingPong)
	}

	if cfg.Recorder != nil {
		hlps = append(hlps, subsystems.NewRecorder(cfg.Recorder, 0))
	}

	if cfg.TokenCounter != nil && cfg.MaxTokens > 0 {
		hlps = append(hlps, subsystems.NewTokenCounter(cfg.TokenCounter, cfg.MaxTokens))
	}

	if cfg.TokenCounter != nil && cfg.MaxTokens > 0 && cfg.PressureThreshold > 0 {
		notifier, err := subsystems.NewContextPressureNotifier(
			cfg.TokenCounter, cfg.MaxTokens, cfg.PressureThreshold, cfg.PressureMessage, userRunner)
		if err != nil {
			return nil, fmt.Errorf("failed to create context pressure notifier: %w", err)
		}
		hlps = append(hlps, notifier)
	}

	eng := engine.NewEngine(cfg.Mode, cfg.Logger, hlps, modelRunner, toolRunner, userRunner, kernelRunner, cfg.Tools, cfg.Clock)
	// Record real executor availability so subsystems can distinguish
	// executable tool calls from provider-issued calls that cannot run: the
	// fallback runner above is idle plumbing, not a public executor.
	eng.State().LoopState.ToolExecutionAvailable = cfg.ToolExecutor != nil
	eng.SetInteractionRunner(interactionRunner)
	eng.State().LoopState.InferenceDefaults = cfg.InferenceDefaults
	if cfg.TickRate > 0 {
		eng.SetTickRate(cfg.TickRate)
	}

	// Add system prompt only if InitialHistory does not already start with a system message
	// (e.g. when continuing a session that was saved with the system message in history).
	hasSystemInHistory := len(cfg.InitialHistory) > 0 && cfg.InitialHistory[0].Role == messages.RoleSystem
	if cfg.SystemPrompt != "" && !hasSystemInHistory {
		systemMessage := messages.NewTextMessage(messages.RoleSystem, cfg.SystemPrompt)
		eng.AddMessages([]messages.Message{systemMessage})
	}
	if len(cfg.InitialHistory) > 0 {
		eng.State().LoopState.History.ConversationBuffer = append(
			eng.State().LoopState.History.ConversationBuffer,
			cfg.InitialHistory...,
		)
	}

	return &AgentLoop{
		engine: eng,
		config: cfg,
		logger: cfg.Logger,
		deltas: messages.NewTypedBuffer[messages.StreamMessage](kernelCap),
	}, nil
}

func validateToolConfiguration(cfg *AgentLoopConfig) error {
	if len(cfg.Tools) == 0 {
		return nil
	}
	if cfg.ToolExecutor != nil {
		return nil
	}
	if cfg.ToolExecution == ToolExecutionDisabled {
		cfg.Tools = nil
		return nil
	}
	return errors.New("tool definitions require WithToolExecutor or WithToolExecutionDisabled")
}

// logInfo emits an info-level log if a logger is configured.
func (al *AgentLoop) logInfo(msg string, fields ...logging.Field) {
	if al.config.Logger != nil {
		al.config.Logger.Info(msg, fields...)
	}
}

// logError emits an error-level log if a logger is configured.
func (al *AgentLoop) logError(msg string, fields ...logging.Field) {
	if al.config.Logger != nil {
		al.config.Logger.Error(msg, fields...)
	}
}

// Execute sends a command and returns an [ExecuteResult] containing all messages
// produced during the agent's turn (model responses, tool-call messages, tool results).
// Use [ExecuteResult.FinalText] when callers need an explicit final text status
// for empty success, missing final text, cancellation, terminal failure, or
// partial output. [ExecuteResult.Text] remains available for legacy text-only
// callers.
func (al *AgentLoop) Execute(ctx context.Context, input ExecuteInput) (ExecuteResult, error) {
	al.logInfo("agentloop: execute called", logging.Field{Key: "command", Value: input.Message})

	msg := input.ToMessage()
	al.engine.AddMessages([]messages.Message{msg})

	kernel := al.engine.GetKernelRunner()
	kernelEventCh := kernel.NewDeltaEventReader(256)

	loopCtx, cancel := context.WithCancel(ctx)
	hotLoopErrCh := make(chan error, 1)
	hotLoopDone := make(chan struct{})

	al.logInfo("agentloop: starting hot loop (streaming)")
	go func() {
		defer close(hotLoopDone)
		err := al.engine.RunHotLoop(loopCtx)
		hotLoopErrCh <- err
		if err != nil {
			al.logError("agentloop: hot loop exited with error (streaming)", logging.Field{Key: "error", Value: err.Error()})
		} else {
			al.logInfo("agentloop: hot loop exited cleanly (streaming)")
		}
	}()

	// stream outputs to the event channel until its completed.
	var resultCh []messages.StreamMessage
	for evt := range kernelEventCh {
		resultCh = append(resultCh, evt)
	}
	cancel()
	// Wait for the hot loop goroutine (and all participant goroutines it owns,
	// including the KernelRunner) to fully exit before returning. Without this,
	// the KernelRunner goroutine from this turn can race with the next call to
	// Execute: it may call closeStreamWithError after NewDeltaEventReader has
	// already registered the next turn's channel, wiping it out and causing the
	// second turn to see an immediately-closed, empty event stream.
	<-hotLoopDone
	loopErr := <-hotLoopErrCh
	// After the internal cleanup cancel, the hot loop often exits with
	// context.Canceled. Treat only that internal cancellation as success; preserve
	// caller-owned cancellation or deadline errors for ExecuteResult.FinalText.
	if errors.Is(loopErr, context.Canceled) && ctx.Err() == nil {
		loopErr = nil
	}

	return ExecuteResult{
		Deltas:   resultCh,
		Messages: al.engine.State().LoopState.History.ConversationBuffer,
		Err:      loopErr,
	}, loopErr
}

// ExecuteStreaming sends a command and returns a [StreamingExecuteResult] whose
// EventStream delivers typed delta events (TEXT.DELTA, REASONING.DELTA, etc.) as
// the model produces them. Consume EventStream to completion to avoid blocking the
// kernel; Messages() becomes available after the turn completes.
func (al *AgentLoop) ExecuteStreaming(ctx context.Context, input ExecuteInput) (*StreamingExecuteResult, error) {
	al.logInfo("agentloop: ExecuteStreaming called", logging.Field{Key: "command", Value: input.Message})

	msg := input.ToMessage()
	al.engine.AddMessages([]messages.Message{msg})

	kernel := al.engine.GetKernelRunner()
	kernelEventCh := kernel.NewDeltaEventReader(256)

	loopCtx, cancel := context.WithCancel(ctx)

	hotLoopErrCh := make(chan error, 1)
	hotLoopDone := make(chan struct{})

	al.logInfo("agentloop: starting hot loop (streaming)")
	go func() {
		defer close(hotLoopDone)
		err := al.engine.RunHotLoop(loopCtx)
		hotLoopErrCh <- err
		if err != nil {
			al.logError("agentloop: hot loop exited with error (streaming)", logging.Field{Key: "error", Value: err.Error()})
		} else {
			al.logInfo("agentloop: hot loop exited cleanly (streaming)")
		}
	}()

	// Forward events to the result channel. Cancel the hot loop only after the kernel
	// closes its channel (LOOP.END processed), so all deltas are delivered first.
	// If the hot loop failed, send an ERROR stream message before closing so the
	// error is delivered as part of the execution response stream.
	resultCh := make(chan streamEvent, 256)
	go func() {
		for evt := range kernelEventCh {
			resultCh <- streamEvent{event: evt}
		}
		cancel()
		<-hotLoopDone
		loopErr := <-hotLoopErrCh
		// After the internal cleanup cancel, the hot loop often exits with
		// context.Canceled. Suppress only that internal cancellation; caller-owned
		// cancellation or deadlines remain observable through Stream.Outcome().
		if errors.Is(loopErr, context.Canceled) && ctx.Err() == nil {
			loopErr = nil
		}
		if loopErr != nil {
			resultCh <- streamEvent{
				event: streamErrorMessage(loopErr),
				err:   loopErr,
			}
		}
		close(resultCh)
	}()

	result := newStreamingExecuteResult()
	result.EventStream = newChanStream(resultCh)

	return result, nil
}

// streamErrorMessage preserves the structured provider/session terminal when
// the ordering engine returns it. Replacing it with a message-only ERROR here
// used to erase classifications such as rate_limited before the CLI rendered
// the failure.
func streamErrorMessage(err error) messages.StreamMessage {
	var deltaErr *engine.StreamDeltaError
	if errors.As(err, &deltaErr) && deltaErr.Value != nil {
		return messages.StreamMessage{Type: messages.StreamTypeError, Value: deltaErr.Value}
	}
	return messages.StreamMessage{Type: messages.StreamTypeError, Value: messages.NewErrorValue(err.Error())}
}

// Run starts the loop in continuous turn-taking mode. It blocks until the context
// is cancelled or the engine encounters an error. Callers inject user messages with
// Send; model responses are forwarded to any writers registered via SetOutputs.
//
// Run requires the loop to have been created with WithMode(engine.ModeTurnTaking).
func (al *AgentLoop) Run(ctx context.Context) error {
	if al.config.Mode != engine.ModeTurnTaking && al.config.Mode != engine.DuplexSession {
		return errors.New("Run is only supported in TurnTaking or Session mode; use Execute or ExecuteStreaming")
	}
	al.logInfo("agentloop: Run called (turn-taking mode)")

	loopCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Forward kernel delta events to the consumer-facing Deltas() buffer.
	// Must be set up before RunHotLoopContinuous starts (KernelRunner reads it on startup).
	kernelDeltaCh := al.engine.GetKernelRunner().NewDeltaEventReader(256)
	forwardCtx, forwardCancel := context.WithCancel(context.Background())
	// If the caller cancels after the engine has reported an error, Run may
	// already be waiting for the forwarding worker in finish. Tie cancellation
	// directly to that worker as well as handling it in the select below so a
	// full public buffer cannot strand shutdown.
	stopForwardOnCancel := context.AfterFunc(ctx, forwardCancel)
	defer stopForwardOnCancel()
	forwardDone := make(chan struct{})
	go func() {
		defer close(forwardDone)
		for msg := range kernelDeltaCh {
			// Keep forwarding independent of the run context. Run cancels the
			// engine as part of shutdown, but the kernel reader can still contain
			// deltas published before that cancellation. Waiting for this worker
			// is the publication barrier that makes those deltas visible before
			// Run returns. Use bounded backpressure so a full consumer buffer is
			// observable as pressure rather than silently dropping the terminal
			// response.
			if outcome := al.deltas.WriteWaitContext(forwardCtx, msg); !outcome.OK() {
				return
			}
		}
	}()

	userOut := al.engine.GetUserRunner().OutChannel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- al.engine.RunHotLoopContinuous(loopCtx)
	}()
	finish := func(err error, received bool) error {
		// The engine has either completed naturally or is being cancelled by
		// the caller. Wait for its participant shutdown to close the kernel
		// delta reader, then join the forwarding worker before exposing the
		// result to the caller.
		if !received {
			err = <-errCh
		}
		<-forwardDone
		forwardCancel()
		return err
	}

	for {
		select {
		case <-ctx.Done():
			al.logInfo("agentloop: context cancelled, stopping turn-taking loop")
			cancel()
			// A consumer may have stopped reading while the forwarding worker is
			// blocked on the bounded public delta buffer. External cancellation is
			// the lifecycle signal that releases that worker; natural engine errors
			// still drain it to preserve the publication barrier.
			forwardCancel()
			_ = finish(nil, false)
			return ctx.Err()

		case err := <-errCh:
			if err != nil {
				al.logError("agentloop: hot loop exited with error (turn-taking)", logging.Field{Key: "error", Value: err.Error()})
			} else {
				al.logInfo("agentloop: hot loop exited cleanly (turn-taking)")
			}
			return finish(err, true)

		case req := <-userOut:
			// The model has produced a text response for this turn. Forward it to
			// any configured output writers.
			al.logInfo("agentloop: received model response, forwarding to outputs")
			text := req.Message.TextContent()
			al.mu.Lock()
			for _, out := range al.outputs {
				_, _ = out.Writer.Write([]byte(text))
			}
			al.mu.Unlock()
		}
	}
}

// Pause pauses the loop.
func (al *AgentLoop) Pause(ctx context.Context) error {
	al.engine.State().SetRunState(engine.RunStatePaused)
	return nil
}

// GetState returns the current loop state.
func (al *AgentLoop) GetState(ctx context.Context) (AgenticLoopState, error) {
	rs := al.engine.State().GetRunState()
	return AgenticLoopState{
		RunState:    RunState(rs),
		Interaction: al.engine.InteractionStateSnapshot(),
	}, nil
}

// GetConversationHistory returns a copy of the conversation buffer (full messages).
func (al *AgentLoop) GetConversationHistory() []messages.Message {
	return al.engine.ConversationHistorySnapshot()
}

// GetConversationDeltas returns a copy of the conversation delta buffer (streaming deltas).
func (al *AgentLoop) GetConversationDeltas() []messages.StreamMessage {
	return al.engine.ConversationDeltasSnapshot()
}

// SetInputs configures input sources for turn-taking mode.
func (al *AgentLoop) SetInputs(ctx context.Context, inputs []Input) error {
	al.mu.Lock()
	defer al.mu.Unlock()
	al.inputs = inputs
	return nil
}

// SetOutputs configures output sinks for turn-taking mode. Each registered
// writer receives the model's text response at the end of every turn.
func (al *AgentLoop) SetOutputs(ctx context.Context, outputs []Output) error {
	al.mu.Lock()
	defer al.mu.Unlock()
	al.outputs = outputs
	return nil
}

func (al *AgentLoop) SendInteractionEvents(ctx context.Context, events []messages.InteractionEvent) error {
	runner := al.engine.GetInteractionRunner()
	if runner == nil {
		return errors.New("interaction runner is not configured")
	}
	return runner.WriteBatch(ctx, events)
}

// Send injects messages into the running loop. In turn-taking mode this resumes
// the loop after the model has responded; calling Send while Execute or
// ExecuteStreaming is in flight will also interrupt / continue the current turn.
// Each message is written to the UserRunner's outbox; the Coordinator picks it
// up on the next tick and routes it to the model for inference.
func (al *AgentLoop) Send(ctx context.Context, msg []messages.Message) error {
	al.logInfo("agentloop: Send called", logging.Field{Key: "count", Value: len(msg)})
	for _, m := range msg {
		if err := al.engine.GetUserRunner().Write(ctx, m); err != nil {
			return fmt.Errorf("agentloop Send: %w", err)
		}
	}
	return nil
}

// SendAudioInput injects raw PCM audio into the running session loop for barge-in
// and user audio forwarding. Its unspecified origin preserves the legacy
// interrupting-by-default behavior. Only meaningful in DuplexSession mode.
// Returns an error if the loop is not in session mode or the channel is full.
func (al *AgentLoop) SendAudioInput(ctx context.Context, pcm []byte) error {
	mr := al.engine.GetModelRunner()
	if mr == nil || mr.UserAudioInbox == nil {
		return fmt.Errorf("SendAudioInput: not in session mode")
	}
	return mr.EnqueueSessionAudioInput(ctx, pcm)
}

// SendAudioInputWithPolicy injects raw PCM audio together with the admission
// policy that was established by the caller. The policy is evaluated only for
// contentful audio while an eligible response is active; silence is always
// forwarded without cancellation. Unknown policies use the interrupting
// default defined by messages.SessionAudioInputPolicy.
func (al *AgentLoop) SendAudioInputWithPolicy(ctx context.Context, pcm []byte, policy messages.SessionAudioInputPolicy) error {
	mr := al.engine.GetModelRunner()
	if mr == nil || mr.UserAudioInbox == nil {
		return fmt.Errorf("SendAudioInputWithPolicy: not in session mode")
	}
	return mr.EnqueueSessionAudioInputWithPolicy(ctx, pcm, policy)
}

// SendSessionEvent delivers a pre-built outbound StreamMessage to the running
// session (DuplexSession mode). The message is forwarded to the provider
// session unchanged and in order relative to audio sent via SendAudioInput.
// It carries control-plane turns such as MESSAGE.END, which realtime
// providers translate into input_audio_buffer.commit plus response.create.
func (al *AgentLoop) SendSessionEvent(ctx context.Context, msg messages.StreamMessage) error {
	mr := al.engine.GetModelRunner()
	if mr == nil || mr.UserEventInbox == nil {
		return fmt.Errorf("SendSessionEvent: not in session mode")
	}
	return mr.EnqueueSessionEvent(ctx, msg)
}

// EnqueueTodo appends a message to the TODO queue for deferred processing.
func (al *AgentLoop) EnqueueTodo(msg string) {
	al.engine.State().LoopState.TodoQueue.Enqueue(msg)
}

// DequeueTodo removes and returns the next TODO message. Returns false if the queue is empty.
func (al *AgentLoop) DequeueTodo() (string, bool) {
	return al.engine.State().LoopState.TodoQueue.Dequeue()
}

// TodoQueueLen returns the number of messages in the TODO queue.
func (al *AgentLoop) TodoQueueLen() int {
	return al.engine.State().LoopState.TodoQueue.Len()
}

// SendInterrupt cancels the current in-flight model or tool execution and resumes
// inference from the partial response accumulated so far. The optional followUp
// message is appended to the conversation as a new user turn before the resumed
// inference is dispatched; pass nil to simply interrupt without adding context.
//
// The interrupt is delivered as a control-plane message through the user runner so
// it flows through the same ordering path as regular messages. The InterruptHandler
// subsystem handles it at TickGroup=-1, before the Coordinator reacts.
func (al *AgentLoop) SendInterrupt(ctx context.Context, followUp *messages.Message) error {
	al.logInfo("agentloop: SendInterrupt called")
	parts := []messages.ContentPart{
		messages.ControlPlanePart{ControlPlaneMessageType: messages.ControlPlaneMessageTypeInterrupt},
	}
	if followUp != nil {
		parts = append(parts, followUp.ContentParts...)
	}
	interruptMsg := messages.Message{
		Role:         messages.RoleUser,
		ContentParts: parts,
	}
	if err := al.engine.GetUserRunner().Write(ctx, interruptMsg); err != nil {
		return fmt.Errorf("agentloop SendInterrupt: %w", err)
	}
	return nil
}
