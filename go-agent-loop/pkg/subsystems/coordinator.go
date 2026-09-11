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
	modelResponseOrder    []string
	latestModelResponse   *modelResponseAssembly
	anonymousModelStream  *modelResponseAssembly
	modelResponsesOverlap bool
}

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
	} else { // The engine output is authoritative for a single response.
	}
	// Completed response assemblies are dispatched only after their boundary.
	// This keeps overlapping provider responses correlated before tool routing.
	// Each completed assembly is retired before the next continuation tick.

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
	} else if len(curr.Inputs.ModelOutputMessage) > 0 {
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
		c.resetModelDeltaWindow(curr)
		return nil
	}

	// In DuplexSession, check for session_close or stop control plane messages.
	if curr.Mode == state.DuplexSession && c.hasSessionCloseControl(curr) {
		curr.Inputs.TerminateLoop = true
	} else {
		if len(curr.Inputs.UserOutputMessage) > 0 {
			// Dispatch user messages to kernel via unified delta inbox.
			for _, message := range curr.Inputs.UserOutputMessage {
				c.logInfo("Coordinator: user text output message", logging.Field{Key: "message", Value: message})
				c.sendInferenceResult(ctx, curr, messages.User, message)
			}
			c.resetModelDeltaWindow(curr)
			curr.History.CurrentPassID++
			curr.Outputs.ModelInbox.Write(ctx, messages.NewInferenceRequest(
				curr.History.ConversationBuffer, curr.Tools, curr.History.CurrentPassID, curr.InferenceDefaults,
			))
		} else { // No user message needs dispatch on this tick.
		}
	}
	return nil
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

type modelResponseAssembly struct {
	key               string
	deltas            []messages.StreamMessage
	implicitToolCalls bool
}

const maxTrackedModelResponses = 16

// observeModelResponses keeps a bounded assembly window per provider response.
func (c *Coordinator) observeModelResponses(deltas []messages.StreamMessage) ([]messages.Message, error) {
	if len(deltas) == 0 {
		return nil, nil
	}
	if c.modelResponses == nil {
		c.modelResponses = make(map[string]*modelResponseAssembly)
	}
	completed := make([]messages.Message, 0, 1)
	for _, delta := range deltas {
		assembly, err := c.modelResponseAssemblyForDelta(delta)
		if err != nil {
			return completed, err
		}
		if assembly == nil {
			continue
		}
		assembly.deltas = append(assembly.deltas, delta)
		if delta.ResponseID == "" && isToolCallDelta(delta.Type) {
			assembly.implicitToolCalls = true
		}
		if delta.Type == messages.StreamTypeMessageEnd {
			completed = append(completed, c.completeModelResponse(assembly))
		}
	}
	return completed, nil
}

func (c *Coordinator) startModelResponse(delta messages.StreamMessage) (*modelResponseAssembly, error) {
	if delta.Type != messages.StreamTypeMessageStart {
		return nil, nil
	}
	responseID := strings.TrimSpace(delta.ResponseID)
	key := responseID
	if key == "" {
		key = c.anonymousResponseKey()
	}
	if c.hasActiveModelResponse(key) {
		c.modelResponsesOverlap = true
	}
	if responseID != "" && c.modelResponses[key] == nil && len(c.modelResponses) >= maxTrackedModelResponses {
		return nil, fmt.Errorf("model response assembly limit %d exceeded", maxTrackedModelResponses)
	}
	assembly := &modelResponseAssembly{key: key}
	if responseID == "" {
		c.anonymousModelStream = assembly
	} else {
		c.modelResponses[key] = assembly
		c.modelResponseOrder = append(c.modelResponseOrder, key)
	}
	c.latestModelResponse = assembly
	return assembly, nil
}

func (c *Coordinator) anonymousResponseKey() string {
	if c.anonymousModelStream != nil {
		return c.anonymousModelStream.key
	}
	return "anonymous"
}

func (c *Coordinator) hasActiveModelResponse(excluding string) bool {
	for key := range c.modelResponses {
		if key != excluding {
			return true
		}
	}
	return c.anonymousModelStream != nil && c.anonymousModelStream.key != excluding
}

func (c *Coordinator) modelResponseForDelta(delta messages.StreamMessage) (*modelResponseAssembly, error) {
	responseID := strings.TrimSpace(delta.ResponseID)
	if responseID != "" {
		if assembly := c.modelResponses[responseID]; assembly != nil {
			return assembly, nil
		}
		// Some provider replays omit the response ID on response.created but
		// include one on a terminal failure. Preserve that terminal boundary
		// only when it can belong to the sole active anonymous response; an
		// unknown non-terminal event must still be rejected rather than routed
		// into a live sibling.
		if isTerminalOnlyModelError(delta) && len(c.modelResponses) == 0 && c.anonymousModelStream != nil {
			return c.anonymousModelStream, nil
		}
		if len(c.modelResponses) == 0 && c.anonymousModelStream == nil && isTerminalOnlyModelError(delta) {
			return nil, nil
		}
		return nil, fmt.Errorf("model response delta references unknown response %q", responseID)
	}
	if c.latestModelResponse != nil {
		return c.latestModelResponse, nil
	}
	return c.onlyModelResponse(), nil
}

func (c *Coordinator) onlyModelResponse() *modelResponseAssembly {
	if len(c.modelResponses) != 1 {
		return nil
	}
	for _, assembly := range c.modelResponses {
		return assembly
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
		c.deleteModelResponseKey(assembly.key)
	}
	if c.latestModelResponse == assembly {
		c.restoreLatestModelResponse()
	}
}

func (c *Coordinator) deleteModelResponseKey(key string) {
	delete(c.modelResponses, key)
	for index, activeKey := range c.modelResponseOrder {
		if activeKey == key {
			c.modelResponseOrder = append(c.modelResponseOrder[:index], c.modelResponseOrder[index+1:]...)
			return
		}
	}
}

func (c *Coordinator) restoreLatestModelResponse() {
	c.latestModelResponse = nil
	for index := len(c.modelResponseOrder) - 1; index >= 0; index-- {
		if active := c.modelResponses[c.modelResponseOrder[index]]; active != nil {
			c.latestModelResponse = active
			return
		}
	}
	c.latestModelResponse = c.anonymousModelStream
}

func isToolCallDelta(deltaType messages.StreamMessageType) bool {
	return deltaType == messages.StreamTypeToolCallStart ||
		deltaType == messages.StreamTypeToolCallDelta ||
		deltaType == messages.StreamTypeToolCallEnd
}

// completeModelResponse joins only provider responses whose tool events were
// unscoped. Some Realtime events omit response_id even though response.created
// opened multiple response IDs; those siblings are one provider tool turn from
// the loop's point of view. Explicitly scoped C48 responses remain independent.
func (c *Coordinator) completeModelResponse(assembly *modelResponseAssembly) messages.Message {
	assemblies := make([]*modelResponseAssembly, 0, len(c.modelResponses)+1)
	for _, key := range c.modelResponseOrder {
		candidate := c.modelResponses[key]
		if candidate == assembly || (assembly.implicitToolCalls && candidate != nil && candidate.implicitToolCalls) {
			assemblies = append(assemblies, candidate)
		}
	}
	if len(assemblies) == 0 {
		assemblies = append(assemblies, assembly)
	}
	var deltas []messages.StreamMessage
	for _, candidate := range assemblies {
		deltas = append(deltas, candidate.deltas...)
	}
	for _, candidate := range assemblies {
		c.removeModelResponse(candidate)
	}
	return messages.ReconstructModelMessageFromDeltas(deltas)
}

// toolResultsAtHistoryTail handles the race between coordinator and kernel
// ticks, ensuring a continuation request includes each tool batch once.
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
