package engine

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/logging"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/participants"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/state"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/subsystems"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

// Engine is the core loop that orchestrates participants and subsystems.
type Engine struct {
	subsystems []subsystems.Subsystem
	state      *SharedState
	// loopMu protects the mutable LoopState owned by the hot loop. Public
	// snapshots take its read lock so history cannot be copied while a tick is
	// appending or truncating the conversation buffers.
	loopMu    sync.RWMutex
	mode      ExecutionMode
	logger    logging.Logger
	tickCount int

	// tickRate controls the minimum interval between ticks in the hot loop.
	// Zero (default) means no delay — the loop runs as fast as possible.
	// When set, the hot loop sleeps for the remaining time after each tick
	// if the tick completed faster than the configured rate.
	tickRate time.Duration
	clock    clock.TimerSource

	ordering *GlobalOrdering

	modelRunner       *participants.ModelRunner
	toolRunner        *participants.ToolRunner
	interactionRunner *participants.InteractionRunner
	userRunner        *participants.UserRunner
	kernelRunner      *participants.KernelRunner
	modelParticipant  *participants.ActiveParticipant
	toolParticipant   *participants.ActiveParticipant
	userParticipant   *participants.ActiveParticipant
	kernelParticipant *participants.ActiveParticipant

	enforceToolDispatchOrdering bool
}

// TickState provides a read-only snapshot of engine state for inspection during
// manual tick control. All fields are copied values safe to read after the call.
type TickState struct {
	TickCount           int
	ConversationLen     int
	DeltaBufferLen      int
	ModelInboxLen       int
	ToolInboxLen        int
	UserInboxLen        int
	KernelDeltaInboxLen int
}

func NewEngine(
	mode ExecutionMode,
	logger logging.Logger,
	hlps []subsystems.Subsystem,
	modelRunner *participants.ModelRunner,
	toolRunner *participants.ToolRunner,
	userRunner *participants.UserRunner,
	kernelRunner *participants.KernelRunner,
	tools []messages.ToolDefinition,
	clocks ...clock.TimerSource,
) *Engine {
	var outputs state.OutputBuffers
	if toolRunner != nil && toolRunner.Inbox != nil {
		outputs.ToolInbox = toolRunner.Inbox
	}
	if userRunner != nil && userRunner.Inbox != nil {
		outputs.UserInbox = userRunner.Inbox
	}
	if modelRunner != nil && modelRunner.Inbox != nil {
		outputs.ModelInbox = modelRunner.Inbox
	}
	if kernelRunner != nil && kernelRunner.DeltaInbox != nil {
		outputs.KernelDeltaInbox = kernelRunner.DeltaInbox
	}
	e := &Engine{
		subsystems:   orderedSubsystems(hlps),
		state:        NewSharedState(outputs),
		mode:         mode,
		logger:       logger,
		modelRunner:  modelRunner,
		toolRunner:   toolRunner,
		kernelRunner: kernelRunner,
		clock:        clock.Real{},
	}
	if len(clocks) > 0 {
		e.SetClock(clocks[0])
	}
	e.ordering = NewGlobalOrdering(modelRunner, toolRunner, userRunner, logger)

	e.state.LoopState.Mode = state.ExecutionMode(mode)
	e.state.LoopState.Tools = tools
	if modelRunner != nil {
		e.modelParticipant = participants.NewActiveParticipant(messages.Model, modelRunner)
		e.modelRunner = modelRunner
	}
	if userRunner != nil {
		e.userParticipant = participants.NewActiveParticipant(messages.User, userRunner)
		e.userRunner = userRunner
	}
	if toolRunner != nil {
		e.toolParticipant = participants.NewActiveParticipant(messages.Tool, toolRunner)
	}
	if kernelRunner != nil {
		e.kernelParticipant = participants.NewActiveParticipant(messages.Kernel, kernelRunner)
		e.kernelRunner = kernelRunner
	}

	return e
}

// SetTickRate configures the minimum interval between ticks in the hot loop.
// Zero (default) means no delay. Only affects RunHotLoop/RunHotLoopContinuous;
// manual tick methods (TickOnce, TickN, TickUntil) are not affected.
func (e *Engine) SetTickRate(d time.Duration) {
	e.tickRate = d
}

func (e *Engine) logError(msg string, err error) {
	if e.logger != nil && err != nil {
		e.logger.Error(msg, logging.Field{Key: "error", Value: err.Error()})
	}
}

// State returns the engine's shared state.
func (e *Engine) State() *SharedState {
	return e.state
}

// GetUserRunner returns the user runner for direct access by the agent loop and tests.
func (e *Engine) GetUserRunner() *participants.UserRunner {
	return e.userRunner
}

func (e *Engine) GetKernelRunner() *participants.KernelRunner {
	return e.kernelRunner
}

func (e *Engine) SetInteractionRunner(r *participants.InteractionRunner) {
	e.interactionRunner = r
	e.ordering.SetInteractionRunner(r)
}

func (e *Engine) GetInteractionRunner() *participants.InteractionRunner {
	return e.interactionRunner
}

// GetModelRunner returns the model runner for direct access by the agent loop and tests.
func (e *Engine) GetModelRunner() *participants.ModelRunner {
	return e.modelRunner
}

// RunHotLoop runs the hot loop and seeds inference from the current history.
func (e *Engine) RunHotLoop(ctx context.Context) error {
	return e.runHotLoop(ctx, true)
}

// RunHotLoopContinuous starts the hot loop without initial inference.
func (e *Engine) RunHotLoopContinuous(ctx context.Context) error {
	return e.runHotLoop(ctx, false)
}

func (e *Engine) runHotLoop(ctx context.Context, sendInitialInference bool) error {
	e.state.SetRunState(RunStateRunning)
	e.enforceToolDispatchOrdering = true
	defer func() {
		e.enforceToolDispatchOrdering = false
		if e.state.GetRunState() == RunStateRunning {
			e.state.SetRunState(RunStateStopped)
		}
	}()

	if e.userParticipant != nil {
		e.userParticipant.Start(ctx)
		defer e.userParticipant.Stop()
	}
	for _, participant := range []*participants.ActiveParticipant{e.toolParticipant, e.kernelParticipant, e.modelParticipant} {
		if participant != nil {
			participant.Start(ctx)
			defer participant.Stop()
		}
	}

	if sendInitialInference {
		e.loopMu.Lock()
		e.state.LoopState.History.ModelDeltaStartIndex = len(e.state.LoopState.History.ConversationDeltaBuffer)
		e.state.LoopState.History.CurrentModelDeltaCount = 0
		e.state.LoopState.History.CurrentPassID++
		e.modelRunner.Inbox.Write(ctx, messages.NewInferenceRequest(
			e.state.LoopState.History.ConversationBuffer,
			e.state.LoopState.Tools,
			e.state.LoopState.History.CurrentPassID,
			e.state.LoopState.InferenceDefaults,
		))
		e.loopMu.Unlock()
	}

	for {
		var tickStart time.Time
		if e.tickRate > 0 {
			tickStart = e.clock.Now()
		}

		err := e.Tick(ctx)
		if err != nil {
			return err
		}

		if err := e.waitForNextTick(ctx, tickStart); err != nil {
			return err
		}
	}
}

func (e *Engine) Tick(ctx context.Context) error {
	e.loopMu.Lock()
	defer e.loopMu.Unlock()

	err := e.ordering.ReadTick(ctx, e.state.LoopState)
	if err != nil {
		e.logError("engine: hot loop failed reading data buffer", err)
		e.state.SetError(err)
		return err
	}
	e.ordering.UpdateWorldHistory(e.state.LoopState)
	err = e.executeWorldState(ctx, e.state)
	if err != nil {
		e.logError("engine: hot loop failed executing world state", err)
		return err
	}
	e.ordering.FlushInputs(e.state.LoopState)
	e.tickCount++
	return nil
}

// TickOnce executes one deterministic tick cycle.
func (e *Engine) TickOnce(ctx context.Context) error {
	return e.Tick(ctx)
}

// TickN executes n tick cycles and returns the first error.
func (e *Engine) TickN(ctx context.Context, n int) error {
	for i := 0; i < n; i++ {
		if err := e.Tick(ctx); err != nil {
			return err
		}
	}
	return nil
}

// TickUntil ticks until predicate succeeds or maxTicks is reached.
func (e *Engine) TickUntil(ctx context.Context, predicate func() bool, maxTicks int) (int, error) {
	for i := 0; i < maxTicks; i++ {
		if predicate() {
			return i, nil
		}
		if err := e.Tick(ctx); err != nil {
			return i + 1, err
		}
	}
	if predicate() {
		return maxTicks, nil
	}
	return maxTicks, fmt.Errorf("predicate not satisfied after %d ticks", maxTicks)
}

// TickState returns a read-only snapshot of tick and buffer state.
func (e *Engine) TickState() TickState {
	e.loopMu.RLock()
	defer e.loopMu.RUnlock()

	ls := e.state.LoopState
	return TickState{
		TickCount:           e.tickCount,
		ConversationLen:     len(ls.History.ConversationBuffer),
		DeltaBufferLen:      len(ls.History.ConversationDeltaBuffer),
		ModelInboxLen:       ls.Outputs.ModelInbox.Len(),
		ToolInboxLen:        ls.Outputs.ToolInbox.Len(),
		UserInboxLen:        ls.Outputs.UserInbox.Len(),
		KernelDeltaInboxLen: ls.Outputs.KernelDeltaInbox.Len(),
	}
}

func (e *Engine) executeWorldState(ctx context.Context, state *SharedState) error {
	for _, h := range e.subsystems {
		if err := h.Execute(ctx, state.LoopState); err != nil {
			wrapped := fmt.Errorf("helper at tick group %d failed: %w", h.TickGroup(), err)
			e.logError("engine: subsystem execute failed", wrapped)
			return wrapped
		}
		if e.enforceToolDispatchOrdering && h.TickGroup() == subsystems.TickGroupCoordinator {
			if err := e.waitForToolDispatch(ctx, state.LoopState); err != nil {
				wrapped := fmt.Errorf("tool dispatch start barrier failed: %w", err)
				e.logError("engine: tool dispatch start barrier failed", wrapped)
				return wrapped
			}
		}
	}
	return nil
}

func (e *Engine) waitForToolDispatch(ctx context.Context, loopState *state.LoopState) error {
	if e.toolRunner == nil || loopState == nil || !loopState.ToolExecutionAvailable {
		return nil
	}
	var calls []messages.ToolCall
	for _, message := range loopState.Inputs.ModelOutputMessage {
		calls = append(calls, message.ToolCalls...)
	}
	return e.toolRunner.WaitForCallsStarted(ctx, calls)
}

func (e *Engine) AddMessages(messages []messages.Message) {
	e.loopMu.Lock()
	defer e.loopMu.Unlock()

	e.state.LoopState.History.ConversationBuffer = append(e.state.LoopState.History.ConversationBuffer, messages...)
	// Populate ConversationDeltaBuffer so GetConversationDeltas reflects the full history.
	e.state.LoopState.History.ConversationDeltaBuffer = append(
		e.state.LoopState.History.ConversationDeltaBuffer,
		mapMessagesToDeltas(messages)...,
	)
}

// ConversationHistorySnapshot returns a synchronized copy of the conversation
// history. The hot loop owns the backing slice and may replace or truncate it
// between ticks, so callers must use this boundary instead of reading
// State().LoopState.History directly while the engine is running.
func (e *Engine) ConversationHistorySnapshot() []messages.Message {
	e.loopMu.RLock()
	defer e.loopMu.RUnlock()

	return append([]messages.Message(nil), e.state.LoopState.History.ConversationBuffer...)
}

// ConversationDeltasSnapshot returns a synchronized copy of the conversation
// delta history. The read lock establishes a causal boundary with the hot
// loop's ordering update and prevents copying a slice while it is being
// appended or truncated.
func (e *Engine) ConversationDeltasSnapshot() []messages.StreamMessage {
	e.loopMu.RLock()
	defer e.loopMu.RUnlock()

	return append([]messages.StreamMessage(nil), e.state.LoopState.History.ConversationDeltaBuffer...)
}

// InteractionStateSnapshot returns a copy of the normalized interaction state
// at the same synchronization boundary as the conversation snapshots.
func (e *Engine) InteractionStateSnapshot() messages.InteractionState {
	e.loopMu.RLock()
	defer e.loopMu.RUnlock()

	return messages.CloneInteractionState(e.state.LoopState.Interaction)
}

func mapMessagesToDeltas(msgs []messages.Message) []messages.StreamMessage {
	delta := make([]messages.StreamMessage, 0, len(msgs))
	for _, msg := range msgs {
		delta = append(delta, textMessageDeltas(msg.Role, msg.TextContent())...)
	}
	return delta
}

// textMessageDeltas returns the delta event sequence for a plain-text message.
// These events are appended to ConversationDeltaBuffer when a message is injected
// directly into history (system prompt in New, user input in Execute/ExecuteStreaming)
// rather than flowing through the hot loop's delta dispatch path.
// Returns nil if text is empty.
func textMessageDeltas(role messages.Role, text string) []messages.StreamMessage {
	if text == "" {
		return nil
	}
	return []messages.StreamMessage{
		{Type: messages.StreamTypeMessageStart, Role: role, Value: messages.NewMessageStartValue()},
		{Type: messages.StreamTypeTextStart, Role: role, Value: messages.NewTextStartValue()},
		{Type: messages.StreamTypeTextDelta, Role: role, Value: messages.NewTextDeltaValue(text)},
		{Type: messages.StreamTypeTextEnd, Role: role, Value: messages.NewTextEndValue()},
		{Type: messages.StreamTypeMessageEnd, Role: role, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
	}
}
