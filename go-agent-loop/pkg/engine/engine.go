package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/portpowered/go-agent-loop/pkg/logging"
	"github.com/portpowered/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-loop/pkg/participants"
	"github.com/portpowered/go-agent-loop/pkg/state"
	"github.com/portpowered/go-agent-loop/pkg/subsystems"
)

// Engine is the core loop that orchestrates participants and subsystems.
type Engine struct {
	subsystems []subsystems.Subsystem
	state      *SharedState
	mode       ExecutionMode
	logger     logging.Logger
	tickCount  int

	// tickRate controls the minimum interval between ticks in the hot loop.
	// Zero (default) means no delay — the loop runs as fast as possible.
	// When set, the hot loop sleeps for the remaining time after each tick
	// if the tick completed faster than the configured rate.
	tickRate time.Duration

	// Global ordering: assigns strictly increasing indices to consumed messages/deltas.
	ordering *GlobalOrdering

	// Active participant runners
	modelRunner  *participants.ModelRunner
	userRunner   *participants.UserRunner
	kernelRunner *participants.KernelRunner
	// Active participant lifecycle
	modelParticipant  *participants.ActiveParticipant
	toolParticipant   *participants.ActiveParticipant
	userParticipant   *participants.ActiveParticipant
	kernelParticipant *participants.ActiveParticipant
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
) *Engine {
	outputs := state.OutputBuffers{}
	if toolRunner != nil && toolRunner.Inbox != nil {
		outputs.ToolInbox = *toolRunner.Inbox
	}
	if userRunner != nil && userRunner.Inbox != nil {
		outputs.UserInbox = *userRunner.Inbox
	}
	if modelRunner != nil && modelRunner.Inbox != nil {
		outputs.ModelInbox = *modelRunner.Inbox
	}
	if kernelRunner != nil && kernelRunner.DeltaInbox != nil {
		outputs.KernelDeltaInbox = *kernelRunner.DeltaInbox
	}
	e := &Engine{
		subsystems:   orderedSubsystems(hlps),
		state:        NewSharedState(outputs),
		mode:         mode,
		logger:       logger,
		modelRunner:  modelRunner,
		kernelRunner: kernelRunner,
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

// GetModelRunner returns the model runner for direct access by the agent loop and tests.
func (e *Engine) GetModelRunner() *participants.ModelRunner {
	return e.modelRunner
}

// RunHotLoop runs a select-based hot loop over active participant outboxes.
// It seeds an initial inference request from the current conversation history,
// so a user message must be present in history before calling this.
// The model and tool runners operate as background goroutines; the loop dispatches
// messages between them and runs passive helpers (recorder, token counter) inline.
func (e *Engine) RunHotLoop(ctx context.Context) error {
	return e.runHotLoop(ctx, true)
}

// RunHotLoopContinuous starts the hot loop without sending an initial inference
// request. Used for turn-taking mode where user messages are injected externally
// via Send; the Coordinator will trigger inference once a user message arrives.
func (e *Engine) RunHotLoopContinuous(ctx context.Context) error {
	return e.runHotLoop(ctx, false)
}

func (e *Engine) runHotLoop(ctx context.Context, sendInitialInference bool) error {
	e.state.SetRunState(RunStateRunning)
	defer func() {
		if e.state.GetRunState() == RunStateRunning {
			e.state.SetRunState(RunStateStopped)
		}
	}()

	if e.userParticipant != nil {
		e.userParticipant.Start(ctx)
		defer e.userParticipant.Stop()
	}
	// Start active participants
	if e.modelParticipant != nil {
		e.modelParticipant.Start(ctx)
		defer e.modelParticipant.Stop()
	}
	if e.toolParticipant != nil {
		e.toolParticipant.Start(ctx)
		defer e.toolParticipant.Stop()
	}
	if e.kernelParticipant != nil {
		e.kernelParticipant.Start(ctx)
		defer e.kernelParticipant.Stop()
	}

	if sendInitialInference {
		// Send initial inference request seeded from current conversation history.
		e.state.LoopState.History.ModelDeltaStartIndex = len(e.state.LoopState.History.ConversationDeltaBuffer)
		e.state.LoopState.History.CurrentModelDeltaCount = 0
		e.state.LoopState.History.CurrentPassID++
		e.modelRunner.Inbox.Write(ctx, messages.NewInferenceRequest(
			e.state.LoopState.History.ConversationBuffer,
			e.state.LoopState.Tools,
			e.state.LoopState.History.CurrentPassID,
			e.state.LoopState.InferenceDefaults,
		))
	}

	for {
		var tickStart time.Time
		if e.tickRate > 0 {
			tickStart = time.Now()
		}

		err := e.Tick(ctx)
		if err != nil {
			return err
		}

		// Adaptive sleeping: if a tick rate is configured, sleep for the remaining
		// time so the hot loop doesn't consume more CPU than needed.
		if e.tickRate > 0 {
			elapsed := time.Since(tickStart)
			if remaining := e.tickRate - elapsed; remaining > 0 {
				time.Sleep(remaining)
			}
		}
	}
}

func (e *Engine) Tick(ctx context.Context) error {
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

// TickOnce executes exactly one tick cycle (ReadTick + UpdateWorldHistory +
// executeWorldState + FlushInputs) and returns. Designed for manual, deterministic
// engine stepping in tests. Data must be present in participant outboxes or the
// call will block until data arrives or ctx is cancelled.
func (e *Engine) TickOnce(ctx context.Context) error {
	return e.Tick(ctx)
}

// TickN executes exactly n tick cycles sequentially. Returns on the first error
// encountered. The tick count reflects the number of successful ticks completed.
func (e *Engine) TickN(ctx context.Context, n int) error {
	for i := 0; i < n; i++ {
		if err := e.Tick(ctx); err != nil {
			return err
		}
	}
	return nil
}

// TickUntil ticks until predicate returns true or maxTicks is reached. The predicate
// is checked before each tick, so if already satisfied, zero ticks are executed.
// Returns the number of ticks executed and an error if maxTicks was exceeded without
// the predicate being satisfied, or if a tick returned an error.
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

// TickState returns a read-only snapshot of the current engine state including
// tick count and buffer occupancy. Safe to call between manual ticks.
func (e *Engine) TickState() TickState {
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
	}
	return nil
}

func (e *Engine) AddMessages(messages []messages.Message) {
	e.state.LoopState.History.ConversationBuffer = append(e.state.LoopState.History.ConversationBuffer, messages...)
	// Populate ConversationDeltaBuffer so GetConversationDeltas reflects the full history.
	e.state.LoopState.History.ConversationDeltaBuffer = append(
		e.state.LoopState.History.ConversationDeltaBuffer,
		mapMessagesToDeltas(messages)...,
	)
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
