package state

import (
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// ExecutionMode is the canonical execution mode type. Engine and agentloop packages
// use this directly to avoid duplication.
type ExecutionMode int

const (
	ModeAskOnce    ExecutionMode = 0
	ModeStreaming  ExecutionMode = 1
	ModeTurnTaking ExecutionMode = 2
	DuplexSession  ExecutionMode = 3
)

// This defines the current loop state of the agent loop.
type LoopState struct {
	Inputs  Buffers
	Outputs OutputBuffers
	History History
	// Interaction tracks normalized gateway event state for PNIG-driven turns.
	Interaction messages.InteractionState
	// Mode is the execution mode set by the engine. Subsystems use this to
	// adjust behaviour (e.g. Coordinator skips auto-termination in DuplexSession).
	Mode ExecutionMode
	// SessionID identifies the current session (set in DuplexSession only).
	SessionID string
	// Tools configuration for inference requests
	Tools     []messages.ToolDefinition
	Lifecycle LoopLifecycleState
	// InferenceDefaults holds default parameters applied to every InferenceRequest.
	InferenceDefaults *messages.InferenceDefaults
	// TodoQueue holds deferred messages (e.g. continuation nudges) to be processed
	// after the current turn completes. It persists across turns within a single execution.
	TodoQueue TodoQueue
}

type LoopLifecycleState string

const (
	LoopLifecycleStateCreated   LoopLifecycleState = "created"
	LoopLifecycleStateRunning   LoopLifecycleState = "running"
	LoopLifecycleStatePaused    LoopLifecycleState = "paused"
	LoopLifecycleStateCompleted LoopLifecycleState = "completed"
	LoopLifecycleStateError     LoopLifecycleState = "error"
)

type OutputBuffers struct {
	// Messsages that we are instructing the tool to execute.
	ToolInbox *messages.TypedBuffer[messages.ToolBatchRequest]
	// Messages we want to tell the user to execute
	UserInbox *messages.TypedBuffer[messages.UserRequest]
	// Messages we want to tell the agent to execute
	ModelInbox *messages.TypedBuffer[messages.InferenceRequest]
	// Messages to the kernel deltas.
	KernelDeltaInbox *messages.TypedBuffer[messages.KernelDeltaRequest]
	// These are buffers that we write out to as a consequence of the current tick.
}

// This is the current set of world inputs, such as inputs from the user, tools, or model.
// This is the set of current data that has just been loaded during the current tick.
type Buffers struct {
	// Customers are able to submit multiple inputs such as (text + image, in a single step).
	TerminateLoop           bool
	UserOutputMessage       []messages.Message
	UserInputDelta          []messages.StreamMessage
	UserControlPlaneMessage []messages.Message // The control plane is messages from the user to denote stop/pause/etc.

	// The tools that can be called are submitted in batch, so you may get multiple tool messages at the same time.
	ToolOutputMessage       []messages.Message
	ToolInputDelta          []messages.StreamMessage
	ToolControlPlaneMessage []messages.Message // The control plane is messages from the tool to declare such things as error/failure.

	// The model is able to submit multiple messages at the same time.
	ModelOutputMessage       []messages.Message
	ModelInputDelta          []messages.StreamMessage
	ModelControlPlaneMessage []messages.Message // The control plane is messages from the model to declare such things as error/failure.

	// InteractionEvents are normalized gateway events waiting to be mapped into
	// agent-loop state and outputs during the current tick.
	InteractionEvents []messages.InteractionEvent
}

type History struct {
	// The history of all the messages that have happened so far.
	ConversationBuffer []messages.Message
	// The history of all the deltas that have been messaged so far.
	ConversationDeltaBuffer []messages.StreamMessage

	ModelDeltaStartIndex   int
	CurrentModelDeltaCount int

	// ToolDeltaStartIndex is the index in ConversationDeltaBuffer where the current
	// tool batch's deltas begin (set when a tool MESSAGE.START delta is consumed).
	ToolDeltaStartIndex   int
	CurrentToolDeltaCount int

	// NextGlobalIndex is the next global index to assign to a consumed message or delta.
	// The engine increments it on each consumption to maintain global ordering (see ORDERING.md).
	NextGlobalIndex int

	// CurrentPassID is incremented each time a new InferenceRequest or ToolBatchRequest is
	// dispatched. Runners tag their outgoing deltas with this ID; the ordering layer drops any
	// delta whose LoopPassID is lower than the current value, eliminating stale deltas that
	// arrive after an interrupt has advanced the pass ID.
	CurrentPassID int
}
