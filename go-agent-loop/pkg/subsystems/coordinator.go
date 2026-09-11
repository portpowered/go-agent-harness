package subsystems

import (
	"context"
	"fmt"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/logging"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/state"
)

// The coordinator is responsible for executing at each tick, checking the inputs, and then determining to push the next inference step/tool call based on the world state.
// All full messages (model, tool, user) are routed through kernelDeltaInbox as
// SYSTEM.FULL_MESSAGE stream messages so that they share the same FIFO queue
// as streaming deltas, eliminating ordering races between the two paths.

type Coordinator struct {
	logger logging.Logger

	// The engine's historical reconstruction window is intentionally single
	// response. Keep a bounded per-response copy here so a duplex provider can
	// leave one response active while another response produces a continuation.
	modelResponses        map[string]*modelResponseAssembly
	anonymousModelStream  *modelResponseAssembly
	modelResponsesOverlap bool
}

type modelResponseAssembly struct {
	key    string
	deltas []messages.StreamMessage
}

const maxTrackedModelResponses = 16

func NewCoordinator(
	logger logging.Logger) *Coordinator {
	return &Coordinator{
		logger:         logger,
		modelResponses: make(map[string]*modelResponseAssembly),
	}
}

var _ Subsystem = (*Coordinator)(nil)

func (c *Coordinator) logInfo(msg string, fields ...logging.Field) {
	if c.logger != nil {
		c.logger.Info(msg, fields...)
	}
}

// sendInferenceResult pushes a full message to the kernel via the shared delta
// inbox as a SYSTEM.FULL_MESSAGE event.
func (c *Coordinator) sendInferenceResult(ctx context.Context, state *state.LoopState, source messages.ParticipantID, msg messages.Message) {
	state.Outputs.KernelDeltaInbox.Write(ctx, messages.KernelDeltaRequest{
		Source: source,
		Delta: messages.StreamMessage{
			Type:  messages.StreamTypeSystemFullMessage,
			Value: messages.NewInferenceResultValue(string(source), msg),
		},
	})
}

// Execute implements [Subsystem].
// user -> triggers agent
// agent -> triggers tool call,
// tool output -> triggers agent
// agent -(if has no tool call)-> user (close current loop on current turn end)
func (c *Coordinator) Execute(ctx context.Context, curr *state.LoopState) error {
	completedModelResponses, err := c.observeModelResponses(curr.Inputs.ModelInputDelta)
	if err != nil {
		return err
	}
	if c.modelResponsesOverlap {
		c.replaceEngineModelOutputs(curr, completedModelResponses)
	}

	if len(curr.Inputs.ToolOutputMessage) > 0 {
		c.logInfo("Coordinator: tool text output message", logging.Field{Key: "curr.Inputs.ToolOutputMessage", Value: curr.Inputs.ToolOutputMessage})
		// Dispatch tool messages to kernel via unified delta inbox.
		for _, message := range curr.Inputs.ToolOutputMessage {
			c.sendInferenceResult(ctx, curr, messages.Tool, message)
		}
		// if the input receives a message from the tool gateway, then trigger a new assistant message from that call.
		curr.History.ModelDeltaStartIndex = len(curr.History.ConversationDeltaBuffer)
		curr.History.CurrentModelDeltaCount = 0
		passID := c.nextToolContinuationPass(curr)
		// The kernel records full messages asynchronously through the shared
		// delta inbox. Include this completed tool batch in the request snapshot
		// as well, so a session model runner can deliver rich results to the
		// provider before the kernel's history tick catches up.
		conversation := append([]messages.Message(nil), curr.History.ConversationBuffer...)
		if !toolResultsAtHistoryTail(conversation, curr.Inputs.ToolOutputMessage) {
			conversation = append(conversation, curr.Inputs.ToolOutputMessage...)
		}
		curr.Outputs.ModelInbox.Write(ctx, messages.NewInferenceRequest(
			conversation, curr.Tools, passID, curr.InferenceDefaults,
		))
		return nil
	}

	if len(curr.Inputs.ModelOutputMessage) > 0 {
		// Dispatch model messages to kernel via unified delta inbox. Ordering is
		// guaranteed because SYSTEM.FULL_MESSAGE and streaming deltas share the
		// same FIFO queue. CoordinatorDelta (which runs after Coordinator) sends
		// LOOP.END through that same queue, so the kernel always processes all
		// messages before the stream closes.
		for _, message := range curr.Inputs.ModelOutputMessage {
			c.sendInferenceResult(ctx, curr, messages.Model, message)
		}
		// Decide whether to trigger a tool call or deliver to the user.
		// Reasoning-only messages are recorded above but do not trigger further actions.
		hasFinalResponse := false
		for _, message := range curr.Inputs.ModelOutputMessage {
			switch {
			case len(message.ToolCalls) > 0 && !curr.ToolExecutionAvailable:
				// No tool executor is configured for this loop, so a
				// provider-issued tool call cannot be executed. Deliver the
				// message like a final response instead of dispatching a
				// batch into the idle default executor, whose guaranteed
				// failure surfaces as a racy terminal error after the
				// response already completed.
				c.logInfo("Coordinator: model tool call without a configured executor delivered as final response",
					logging.Field{Key: "tool_calls", Value: len(message.ToolCalls)})
				curr.Outputs.UserInbox.Write(ctx, messages.UserRequest{Message: message})
				hasFinalResponse = true
			case len(message.ToolCalls) > 0:
				c.logInfo("Coordinator: model tool call output message", logging.Field{Key: "message", Value: message})
				passID := c.nextToolBatchPass(curr)
				curr.Outputs.ToolInbox.Write(ctx, messages.ToolBatchRequest{
					Calls:      message.ToolCalls,
					LoopPassID: passID,
				})
			case !message.HasOnlyReasoning():
				c.logInfo("Coordinator: model output message", logging.Field{Key: "message", Value: message})
				curr.Outputs.UserInbox.Write(ctx, messages.UserRequest{
					Message: message,
				})
				hasFinalResponse = true
			default:
				c.logInfo("Coordinator: model reasoning output message", logging.Field{Key: "message", Value: message})
			}
		}
		// Set TerminateLoop so CoordinatorDelta sends LOOP.END through the same
		// delta inbox. Because CoordinatorDelta runs after Coordinator (higher tick
		// group), LOOP.END is always enqueued after all SYSTEM.FULL_MESSAGE
		// messages, preserving ordering guarantees.
		//
		// In DuplexSession, auto-termination on final response is suppressed.
		// The session persists until explicitly closed via control plane
		// (session_close or stop).
		if hasFinalResponse && curr.Mode != state.DuplexSession {
			c.logInfo("Coordinator: terminating loop", logging.Field{Key: "hasFinalResponse", Value: hasFinalResponse})
			curr.Inputs.TerminateLoop = true
		}
		// The model delta window belongs to one provider response. Reset it after
		// dispatching the completed response so a later response (for example an
		// acknowledgement or an interruption response) cannot reconstruct and
		// execute tool calls from this response a second time.
		curr.History.ModelDeltaStartIndex = len(curr.History.ConversationDeltaBuffer)
		curr.History.CurrentModelDeltaCount = 0
		return nil
	}

	// In DuplexSession, check for session_close or stop control plane messages
	// from the user. These trigger graceful loop termination.
	if curr.Mode == state.DuplexSession {
		for _, msg := range curr.Inputs.UserControlPlaneMessage {
			if cpType := extractControlPlaneType(msg); cpType == messages.ControlPlaneMessageTypeSessionClose ||
				cpType == messages.ControlPlaneMessageTypeStop {
				c.logInfo("Coordinator: session close requested via control plane",
					logging.Field{Key: "type", Value: string(cpType)})
				curr.Inputs.TerminateLoop = true
				return nil
			}
		}
	}

	if len(curr.Inputs.UserOutputMessage) > 0 {
		// Dispatch user messages to kernel via unified delta inbox.
		for _, message := range curr.Inputs.UserOutputMessage {
			c.logInfo("Coordinator: user text output message", logging.Field{Key: "message", Value: message})
			c.sendInferenceResult(ctx, curr, messages.User, message)
		}
		curr.History.ModelDeltaStartIndex = len(curr.History.ConversationDeltaBuffer)
		curr.History.CurrentModelDeltaCount = 0
		curr.History.CurrentPassID++
		curr.Outputs.ModelInbox.Write(ctx, messages.NewInferenceRequest(
			curr.History.ConversationBuffer, curr.Tools, curr.History.CurrentPassID, curr.InferenceDefaults,
		))
	}
	return nil
}

// nextToolBatchPass returns the generation used to tag a tool batch. Turn-based
// loops advance the generation for every dispatched batch, which lets the
// ordering layer discard work left behind by an interrupt. A duplex provider,
// however, may publish more than one response before any tool result returns.
// Those batches are concurrent work in the same session generation; advancing
// the shared pass for each one makes the first completed batch retire every
// sibling batch as "stale" before its results can be reconstructed.
func (c *Coordinator) nextToolBatchPass(curr *state.LoopState) int {
	if curr.Mode == state.DuplexSession {
		return c.ensureSessionPass(curr)
	}
	curr.History.CurrentPassID++
	return curr.History.CurrentPassID
}

// nextToolContinuationPass follows the same generation rule as tool batches.
// In a duplex session, a result-driven inference request is part of the same
// provider generation and must not invalidate another already-running tool
// batch. A user interrupt still advances CurrentPassID in InterruptHandler,
// so late work from the cancelled generation remains discardable.
func (c *Coordinator) nextToolContinuationPass(curr *state.LoopState) int {
	if curr.Mode == state.DuplexSession {
		return c.ensureSessionPass(curr)
	}
	curr.History.CurrentPassID++
	return curr.History.CurrentPassID
}

func (c *Coordinator) ensureSessionPass(curr *state.LoopState) int {
	if curr.History.CurrentPassID == 0 {
		curr.History.CurrentPassID = 1
	}
	return curr.History.CurrentPassID
}

// observeModelResponses keeps an independent bounded assembly window for each
// provider response. The engine's ordering layer exposes one current model
// window, which is sufficient for serial responses but loses tool calls when a
// second response remains active while a grounded continuation completes.
func (c *Coordinator) observeModelResponses(deltas []messages.StreamMessage) ([]messages.Message, error) {
	if len(deltas) == 0 {
		return nil, nil
	}
	if c.modelResponses == nil {
		c.modelResponses = make(map[string]*modelResponseAssembly)
	}
	completed := make([]messages.Message, 0, 1)
	for _, delta := range deltas {
		if delta.Type == messages.StreamTypeMessageStart {
			key := strings.TrimSpace(delta.ResponseID)
			if key == "" {
				if c.anonymousModelStream != nil {
					key = c.anonymousModelStream.key
				} else {
					key = "anonymous"
				}
			}
			if c.hasActiveModelResponse(key) {
				c.modelResponsesOverlap = true
			}
			if strings.TrimSpace(delta.ResponseID) != "" && c.modelResponses[key] == nil && len(c.modelResponses) >= maxTrackedModelResponses {
				return completed, fmt.Errorf("model response assembly limit %d exceeded", maxTrackedModelResponses)
			}
			assembly := &modelResponseAssembly{key: key}
			if strings.TrimSpace(delta.ResponseID) == "" {
				c.anonymousModelStream = assembly
			} else {
				c.modelResponses[key] = assembly
			}
		}

		assembly := c.modelResponseForDelta(delta)
		if assembly == nil {
			continue
		}
		assembly.deltas = append(assembly.deltas, delta)
		if delta.Type != messages.StreamTypeMessageEnd {
			continue
		}
		completed = append(completed, messages.ReconstructModelMessageFromDeltas(assembly.deltas))
		c.removeModelResponse(assembly)
	}
	return completed, nil
}

func (c *Coordinator) hasActiveModelResponse(excluding string) bool {
	for key := range c.modelResponses {
		if key != excluding {
			return true
		}
	}
	return c.anonymousModelStream != nil && c.anonymousModelStream.key != excluding
}

func (c *Coordinator) modelResponseForDelta(delta messages.StreamMessage) *modelResponseAssembly {
	if responseID := strings.TrimSpace(delta.ResponseID); responseID != "" {
		if assembly := c.modelResponses[responseID]; assembly != nil {
			return assembly
		}
		// A provider may omit ResponseID on later deltas after including it on
		// MESSAGE.START. When exactly one response is active, retain that link.
		if len(c.modelResponses) == 1 && c.anonymousModelStream == nil {
			for _, assembly := range c.modelResponses {
				return assembly
			}
		}
		return nil
	}
	if c.anonymousModelStream != nil {
		return c.anonymousModelStream
	}
	if len(c.modelResponses) == 1 {
		for _, assembly := range c.modelResponses {
			return assembly
		}
	}
	return nil
}

func (c *Coordinator) removeModelResponse(assembly *modelResponseAssembly) {
	if assembly == nil {
		return
	}
	if c.anonymousModelStream == assembly {
		c.anonymousModelStream = nil
	}
	if current := c.modelResponses[assembly.key]; current == assembly {
		delete(c.modelResponses, assembly.key)
	}
}

// replaceEngineModelOutputs removes the single-window messages already copied
// into history by UpdateWorldHistory, then installs the response-scoped
// assemblies. Ordering metadata from the engine boundary is retained so the
// replacement remains part of the same global trace.
func (c *Coordinator) replaceEngineModelOutputs(curr *state.LoopState, completed []messages.Message) {
	engineOutputs := curr.Inputs.ModelOutputMessage
	if len(engineOutputs) > 0 && len(curr.History.ConversationBuffer) >= len(engineOutputs) {
		start := len(curr.History.ConversationBuffer) - len(engineOutputs)
		matches := true
		for index, message := range engineOutputs {
			historyMessage := curr.History.ConversationBuffer[start+index]
			if historyMessage.GlobalIndex != message.GlobalIndex || historyMessage.ActorID != message.ActorID {
				matches = false
				break
			}
		}
		if matches {
			curr.History.ConversationBuffer = curr.History.ConversationBuffer[:start]
		}
	}
	curr.Inputs.ModelOutputMessage = curr.Inputs.ModelOutputMessage[:0]
	for index, message := range completed {
		if index < len(engineOutputs) {
			message.GlobalIndex = engineOutputs[index].GlobalIndex
			message.ActorProvidedID = engineOutputs[index].ActorProvidedID
			message.ActorProvidedIndex = engineOutputs[index].ActorProvidedIndex
			message.ActorStreamID = engineOutputs[index].ActorStreamID
			message.ActorID = engineOutputs[index].ActorID
		}
		curr.Inputs.ModelOutputMessage = append(curr.Inputs.ModelOutputMessage, message)
		curr.History.ConversationBuffer = append(curr.History.ConversationBuffer, message)
	}
}

// toolResultsAtHistoryTail handles the race between the coordinator's
// inference-request tick and the kernel tick that records SYSTEM.FULL_MESSAGE.
// A request must contain the current tool batch exactly once whether the
// kernel has already appended it or not.
func toolResultsAtHistoryTail(history, results []messages.Message) bool {
	if len(results) == 0 || len(history) < len(results) {
		return false
	}
	tail := history[len(history)-len(results):]
	for index := range results {
		if tail[index].Role != results[index].Role || tail[index].ToolCallID != results[index].ToolCallID {
			return false
		}
	}
	return true
}

// TickGroup implements [Subsystem].
func (c *Coordinator) TickGroup() TickGroup {
	return TickGroupCoordinator
}

// extractControlPlaneType returns the ControlPlaneMessageType from the first
// ControlPlanePart found in the message's ContentParts, or "" if none.
func extractControlPlaneType(msg messages.Message) messages.ControlPlaneMessageType {
	for _, part := range msg.ContentParts {
		if cp, ok := part.(messages.ControlPlanePart); ok {
			return cp.ControlPlaneMessageType
		}
	}
	return ""
}
